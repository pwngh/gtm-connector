package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The deal command registers, lists, shows and closes deals. A deal is one renewal of one account under a
// readable ID. Registering reads the account and its opportunities through the workspace's CRM, each read a
// tool call under its own receipt, and binds the account, and the opportunity and contract the reads
// returned. brief makes an account's card for its open deal.
//
// The ID is the first word of the account's name and the year the deal renews: thornfield-2027. The word
// is the first word with an ASCII letter or digit once its accents are taken off, lowered, with every other
// character dropped and cut to 40 characters, or "deal" when no word has one. The year is the year of the
// day the deal renews, or of the day it was registered when the CRM gives none. An ID a deal of the
// workspace already holds, open or closed, takes the lowest free number from 2 after it: thornfield-2027-2.

var dealActions = map[string]arguments{
	"new":   {options: []option{{name: "--as-of", value: true}}, required: "account_id", help: dealNewHelp},
	"list":  {help: dealListHelp},
	"show":  {required: "deal_id", help: dealShowHelp},
	"close": {required: "deal_id", help: dealCloseHelp},
}

// dealID is the form of a deal's ID.
var dealID = regexp.MustCompile(`^[a-z0-9]{1,40}-[0-9]{4}(-[1-9][0-9]{0,5})?$`)

// errNoDeal is a deal ID the workspace does not hold.
var errNoDeal = &failure{code: 2, message: "No deal of this workspace has that ID.", next: "gtm-connector deal list"}

// runDeal runs the deal command and gives its exit code.
func runDeal(ctx context.Context, tokens []string, s screen) int {
	if len(tokens) == 0 {
		return s.fail("deal", refused("the following arguments are required: action"))
	}
	if tokens[0] == "-h" || tokens[0] == "--help" {
		fmt.Fprint(s.out, dealHelp)
		return 0
	}
	action := tokens[0]
	spec, known := dealActions[action]
	if !known {
		return s.fail("deal", refused("argument action: invalid choice (choose from 'new', 'list', 'show', 'close')"))
	}
	command := "deal " + action
	args, err := spec.parse(tokens[1:])
	if err != nil {
		return s.fail(command, refused("%s", err))
	}
	if args.help {
		fmt.Fprint(s.out, spec.help)
		return 0
	}
	r := &run{command: command, args: args, screen: s, name: defaultWorkspace}
	if name, given := args.text("--state-dir"); given {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	var result any
	switch action {
	case "new":
		result, err = r.newDeal(ctx)
	case "list":
		result, err = r.listDeals(ctx)
	case "show":
		result, err = r.showDeal(ctx)
	default:
		result, err = r.closeDeal(ctx)
	}
	if err != nil {
		return s.fail(command, r.failed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
		return 0
	}
	fmt.Fprintln(s.out, r.dealText(action, result))
	return 0
}

// newDeal registers a deal for one account, as the owner.
func (r *run) newDeal(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	account := r.args.positionals[0]
	asOf, given := r.args.text("--as-of")
	if !given {
		asOf = from.day()
	}
	if err := from.open(ctx, "crm"); err != nil {
		return nil, err
	}
	r.announce(from.crmLine())
	if err := cards.CheckRequest(from.sources(), account, asOf); err != nil {
		return nil, err
	}
	deal, err := from.registerDeal(ctx, "", []string{"crm:read"}, account, asOf)
	if errors.Is(err, errAccountNotFound) {
		return nil, &failure{code: 2, message: "account not found: " + account, next: "gtm-connector accounts" + r.folder()}
	}
	if err != nil {
		return nil, err
	}
	return dealValue(deal)
}

// listDeals lists the workspace's deals, the open ones first.
func (r *run) listDeals(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	listed := []any{}
	if !r.exists {
		return listed, nil
	}
	deals, err := r.store.Deals(ctx, r.workspace.ID)
	if err != nil {
		return nil, err
	}
	for _, deal := range deals {
		value, err := dealValue(deal)
		if err != nil {
			return nil, err
		}
		listed = append(listed, value)
	}
	return listed, nil
}

// stored is one deal of the workspace, by the ID the command was given.
func (r *run) stored(ctx context.Context) (postgres.Deal, error) {
	id := r.args.positionals[0]
	if !dealID.MatchString(id) {
		return postgres.Deal{}, refused("A deal ID is a word, the year it renews and, after a clash, a number, " +
			"such as thornfield-2027. gtm-connector deal list prints them.")
	}
	if err := r.open(ctx); err != nil {
		return postgres.Deal{}, err
	}
	if !r.exists {
		return postgres.Deal{}, errNoDeal
	}
	deal, err := r.store.Deal(ctx, r.workspace.ID, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Deal{}, errNoDeal
	}
	return deal, err
}

// showDeal is a deal and its newest card, verified as inspect verifies a saved card, without its evidence.
func (r *run) showDeal(ctx context.Context) (any, error) {
	deal, err := r.stored(ctx)
	if err != nil {
		return nil, err
	}
	value, err := dealValue(deal)
	if err != nil {
		return nil, err
	}
	var card any
	if deal.LatestCard != nil {
		from, err := r.reads(ctx)
		if errors.Is(err, errNotConnected) {
			return nil, refused("%s", cards.ErrNoCard)
		}
		if err != nil {
			return nil, err
		}
		sealed, err := r.sealed(ctx, deal.LatestCard.BriefID, from.sources())
		if err != nil {
			return nil, err
		}
		sealed.Delete("evidence")
		card = sealed
	}
	return strictjson.NewObject("deal", value, "card", card), nil
}

// closeDeal closes an open deal. A closed one is shown as it is.
func (r *run) closeDeal(ctx context.Context) (any, error) {
	deal, err := r.stored(ctx)
	if err != nil {
		return nil, err
	}
	deal, _, err = r.store.CloseDeal(ctx, r.workspace.ID, deal.Ref)
	if err != nil {
		return nil, err
	}
	return dealValue(deal)
}

// registerDeal reads the account and then its opportunities through the workspace's CRM, each as a tool
// call of its own under a receipt committed before the read, by principal ("" for the owner) with scopes,
// and registers the deal on what they returned. A grant without crm:read is refused under the first
// receipt, and nothing is read.
func (r *reads) registerDeal(ctx context.Context, principal string, scopes []string, account, asOf string) (
	postgres.Deal, error) {
	crm, err := r.crm(ctx)
	if err != nil {
		return postgres.Deal{}, sourceFailure{err}
	}
	run := "deal-" + strings.ReplaceAll(receipts.RandomID(), "-", "")
	token := receipts.Fingerprint(randomToken())
	read := func(n int, tool string, use func([]*strictjson.Object) error) (receipts.Receipt, error) {
		finished, _, err := r.call(ctx, toolCall{token: token, scopes: scopes, run: run, request: n, tool: tool,
			arguments: strictjson.NewObject("account_id", account), account: account, principal: principal},
			func(ctx context.Context) ([]*strictjson.Object, error) {
				records, err := crm.Read(ctx, tool, account)
				if err == nil {
					err = use(records)
				}
				if err != nil {
					return nil, sourceFailure{err}
				}
				return records, nil
			})
		return finished, err
	}
	var accountRecords []*strictjson.Object
	var name string
	accountRead, err := read(1, "get_account", func(records []*strictjson.Object) error {
		found, err := accountOf(records, account, r.backend("crm") != "sqlite")
		if err == nil {
			accountRecords = records
			name, _ = get(found, "name").(string)
		}
		return err
	})
	if err != nil {
		return postgres.Deal{}, err
	}
	// A deal belongs to an account of its workspace, so the account is listed under the name its CRM gives it.
	if err := r.store.EnsureAccount(ctx, r.workspace.ID, account, name); err != nil {
		return postgres.Deal{}, err
	}
	var opportunities []*strictjson.Object
	opportunitiesRead, err := read(2, "list_opportunities", func(records []*strictjson.Object) error {
		opportunities = records
		return nil
	})
	if err != nil {
		return postgres.Deal{}, err
	}
	registration := renewalOf(accountRecords, opportunities, asOf)
	registration.WorkspaceID, registration.PrincipalID, registration.AccountRef = r.workspace.ID, principal, account
	registration.Word = dealWord(name)
	registration.AccountReadID, registration.OpportunitiesReadID = accountRead.AuditID, opportunitiesRead.AuditID
	return r.store.RegisterDeal(ctx, registration)
}

// renewalOf is what a deal binds of the records its two reads returned, and the year it renews.
//
// The contract is the one a card counts the renewal to: the earliest that ends on or after asOf, by ID on a
// tie. The opportunity is the open one that closes first, by ID on a tie: one not closed, or for a CRM
// without that field, one whose stage is not closed_won or closed_lost. The deal renews on the account's
// renewal date, or else on that contract's end, or else on that opportunity's close date, and when the
// records give none it has no day and takes the year of asOf.
func renewalOf(accountRecords, opportunities []*strictjson.Object, asOf string) postgres.DealRegistration {
	var in postgres.DealRegistration
	var account *strictjson.Object
	var contractEnd, closes string
	for _, record := range accountRecords {
		table, id := recordOf(record)
		switch table {
		case "accounts":
			account = record
		case "contracts":
			end := day(get(record, "end_date"))
			if end != "" && end >= asOf && (contractEnd == "" || end < contractEnd || end == contractEnd && id < in.ContractRef) {
				contractEnd, in.ContractRef = end, id
			}
		}
	}
	for _, record := range opportunities {
		table, id := recordOf(record)
		closed, isBool := get(record, "is_closed").(bool)
		if !isBool {
			stage := get(record, "stage")
			closed = stage == "closed_won" || stage == "closed_lost"
		}
		if table != "opportunities" || closed {
			continue
		}
		// An opportunity with no close date comes after every one with a date.
		on := day(get(record, "close_date"))
		var earlier bool
		switch {
		case in.OpportunityRef == "":
			earlier = true
		case on == closes:
			earlier = id < in.OpportunityRef
		default:
			earlier = on != "" && (closes == "" || on < closes)
		}
		if earlier {
			closes, in.OpportunityRef = on, id
		}
	}
	for _, on := range []string{day(get(account, "renewal_date")), contractEnd, closes} {
		if on != "" {
			in.RenewsOn = on
			break
		}
	}
	year := asOf
	if in.RenewsOn != "" {
		year = in.RenewsOn
	}
	in.Year, _ = strconv.Atoi(year[:4])
	return in
}

// recordOf is a record's table and ID, from its record_ref crm/TABLE/ID.
func recordOf(record *strictjson.Object) (table, id string) {
	ref, _ := get(record, "record_ref").(string)
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) != 3 || parts[0] != "crm" {
		return "", ""
	}
	return parts[1], parts[2]
}

// day is a value that is a day written YYYY-MM-DD, or "".
func day(value any) string {
	text, _ := value.(string)
	if _, err := time.Parse(time.DateOnly, text); err != nil {
		return ""
	}
	return text
}

// dealWord is the word a deal's ID starts with, from the account's name.
func dealWord(name string) string {
	for _, word := range strings.FieldsFunc(name, unicodetab.IsSpace) {
		var b strings.Builder
		for _, c := range unicodetab.NFD([]rune(word)) {
			switch {
			case c >= 'A' && c <= 'Z':
				b.WriteRune(c + 'a' - 'A')
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
				b.WriteRune(c)
			}
			if b.Len() == 40 {
				break
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return "deal"
}

// dealValue is a deal as the command and the API give it. The account's name is source text, shown inert.
func dealValue(d postgres.Deal) (*strictjson.Object, error) {
	name, _, err := evidence.SafeText(d.AccountName)
	if err != nil {
		return nil, err
	}
	orNull := func(text string) any {
		if text == "" {
			return nil
		}
		return text
	}
	var closed, card any
	if d.ClosedAt != nil {
		closed = utcSeconds(*d.ClosedAt)
	}
	if d.LatestCard != nil {
		card = strictjson.NewObject("id", d.LatestCard.ID, "brief_id", d.LatestCard.BriefID,
			"published_at", utcSeconds(d.LatestCard.PublishedAt))
	}
	return strictjson.NewObject("id", d.Ref, "account", d.AccountRef, "account_name", name, "state", d.State,
		"opportunity", orNull(d.OpportunityRef), "contract", orNull(d.ContractRef), "renews_on", orNull(d.RenewsOn),
		"registered_by", orNull(d.RegisteredBy), "registered_at", utcSeconds(d.RegisteredAt), "closed_at", closed,
		"reads", strictjson.NewObject("get_account", d.AccountReadID, "list_opportunities", d.OpportunitiesReadID),
		"latest_card", card), nil
}

// dealText is a deal command's result as a person reads it.
func (r *run) dealText(action string, result any) string {
	if action == "list" {
		listed := result.([]any)
		if len(listed) == 0 {
			return "No deals. Register one: gtm-connector deal new ACCOUNT_ID" + r.here()
		}
		rows := make([][]any, len(listed))
		for i, value := range listed {
			deal := value.(*strictjson.Object)
			rows[i] = []any{get(deal, "id"), get(deal, "account"), get(deal, "account_name"), get(deal, "state"),
				orNone(get(deal, "renews_on"))}
		}
		return r.screen.table([]string{"ID", "ACCOUNT", "NAME", "STATE", "RENEWS"}, rows)
	}
	deal, _ := result.(*strictjson.Object)
	var card *strictjson.Object
	if action == "show" {
		card, _ = get(deal, "card").(*strictjson.Object)
		deal = get(deal, "deal").(*strictjson.Object)
	}
	fields := [][]any{{"ID", get(deal, "id")}, {"Account", get(deal, "account")},
		{"Name", strictjson.Quote(get(deal, "account_name").(string))}, {"State", get(deal, "state")},
		{"Opportunity", orNone(get(deal, "opportunity"))}, {"Contract", orNone(get(deal, "contract"))},
		{"Renews", orNone(get(deal, "renews_on"))}}
	text := r.screen.table([]string{"FIELD", "VALUE"}, fields)
	switch {
	case action != "show":
	case card != nil:
		text += "\n\n" + r.cardText(card)
	default:
		text += "\n\n" + oneLine("No card yet. Make one: gtm-connector brief "+get(deal, "account").(string)+r.here())
	}
	return text
}

// orNone is a value, or "none" for null.
func orNone(value any) any {
	if value == nil {
		return "none"
	}
	return value
}

// dealRegistry registers deals for the people signed in to serve, each in the workspace they belong to,
// reading the CRM as that workspace is connected at the moment of the request. A person with no live grant
// on the account is refused under the read's receipt, and nothing is read.
type dealRegistry struct{ store *postgres.Store }

func (d dealRegistry) RegisterDeal(ctx context.Context, p authz.Principal, account string) (postgres.Deal, error) {
	workspace, err := d.store.WorkspaceByID(ctx, p.WorkspaceID)
	if err != nil {
		return postgres.Deal{}, err
	}
	from, err := workspaceReads(ctx, d.store, workspace)
	if err != nil {
		return postgres.Deal{}, fmt.Errorf("%w: %w", httptransport.ErrSourceFailed, err)
	}
	asOf := from.day()
	var scopes []string
	granted, err := d.store.HasAccountGrant(ctx, workspace.ID, p.ID, account)
	if err != nil {
		return postgres.Deal{}, err
	}
	if granted {
		scopes = []string{"crm:read"}
	}
	deal, err := from.registerDeal(ctx, p.ID, scopes, account, asOf)
	var failed sourceFailure
	switch {
	case errors.Is(err, errDenied):
		return postgres.Deal{}, authz.ErrNotPermitted
	case errors.As(err, &failed):
		return postgres.Deal{}, fmt.Errorf("%w: %w", httptransport.ErrSourceFailed, err)
	}
	return deal, err
}

// sourceFailure is a failure of the CRM, or of what it returned, as against one of the database.
type sourceFailure struct{ error }

func (e sourceFailure) Unwrap() error { return e.error }

const dealHelp = `usage: gtm-connector deal [-h] {new,list,show,close} ...

Register, list, show and close deals: one renewal of one account, under a readable ID.

positional arguments:
  {new,list,show,close}
    new                 Register a deal for an account from the connected CRM.
    list                List the deals of a workspace.
    show                Show a deal's bindings and its newest card.
    close               Close a deal.

options:
  -h, --help            show this help message and exit

Each action takes --json, --state-dir WORKSPACE and --help.
Example: gtm-connector deal new acc-003
`

const dealNewHelp = `usage: gtm-connector deal new [-h] [--json] [--state-dir STATE_DIR]
                              [--as-of AS_OF]
                              account_id

Register a deal for one account. The account and its opportunities are read from the connected CRM, each
under a receipt, and the deal binds the account, its open opportunity and its contract.

positional arguments:
  account_id            The CRM account ID, such as acc-003.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --as-of AS_OF         The day the contract is chosen from, YYYY-MM-DD.

Example: gtm-connector deal new acc-003
`

const dealListHelp = `usage: gtm-connector deal list [-h] [--json] [--state-dir STATE_DIR]

List the deals of a workspace, the open ones first.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
`

const dealShowHelp = `usage: gtm-connector deal show [-h] [--json] [--state-dir STATE_DIR] deal_id

Show a deal's bindings and its newest card, which brief made for it.

positional arguments:
  deal_id               The deal's ID, such as thornfield-2027.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
`

const dealCloseHelp = `usage: gtm-connector deal close [-h] [--json] [--state-dir STATE_DIR] deal_id

Close a deal. Its cards and receipts are kept, and brief makes the account's card again.

positional arguments:
  deal_id               The deal's ID, such as thornfield-2027.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
`
