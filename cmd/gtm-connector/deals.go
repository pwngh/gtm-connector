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
// The ID is the first word of the account's name, the year the deal renews and a key of the account:
// thornfield-2027-2e3hke. The word is the first word with an ASCII letter or digit once its accents are
// taken off, lowered, with every other character dropped and cut to 40 characters, or "deal" when no word
// has one. The year is the year of the day the deal renews, or of the day it was registered when the CRM
// gives none. The key is six characters of the digest of the account's ID (postgres.RegisterDeal), so an
// ID says nothing of another account's deals. The account's next deal of the same year takes the lowest
// free number from 2 after it: thornfield-2027-2e3hke-2.

var dealActions = map[string]arguments{
	"new":  {options: []option{{name: "--as-of", value: true}}, required: "account_id", help: dealNewHelp},
	"list": {options: []option{{name: "--all"}}, help: dealListHelp},
	"show": {required: "deal_id", help: dealShowHelp},
	"close": {options: []option{{name: "--reason", value: true}, {name: "--no-card"}, {name: "--as-of", value: true}},
		required: "deal_id", help: dealCloseHelp},
	"matches": {options: []option{{name: "--all"}}, help: dealMatchesHelp},
	"decide": {options: []option{{name: "--deal", value: true}, {name: "--reject"}}, required: "match_id",
		help: dealDecideHelp},
}

// dealID is the form of a deal's ID.
var dealID = regexp.MustCompile(`^[a-z0-9]{1,40}-[0-9]{4}-([a-z2-7]{6}|[a-z2-7]{12})(-[1-9][0-9]{0,5})?$`)

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
		return s.fail("deal", refused("argument action: invalid choice (choose from 'new', 'list', 'show', 'close', "+
			"'matches', 'decide')"))
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
	case "matches":
		result, err = r.listMatches(ctx)
	case "decide":
		result, err = r.decideMatch(ctx)
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
	if action == "matches" || action == "decide" {
		fmt.Fprintln(s.out, r.matchesText(result))
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
	// A deal belongs to an account of its workspace, so an account the CRM holds is listed there first, and
	// the receipts of the deal's reads name it.
	if err := from.list(ctx, account); err != nil {
		return nil, err
	}
	deal, err := from.registerDeal(ctx, "", []string{"crm:read"}, nil, account, asOf)
	if errors.Is(err, errAccountNotFound) {
		return nil, &failure{code: 2, message: "account not found: " + account, next: "gtm-connector accounts" + r.folder()}
	}
	if err != nil {
		return nil, err
	}
	return dealValue(deal)
}

// listDeals lists the workspace's open deals, and with --all the closed ones after them.
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
	_, all := r.args.text("--all")
	for _, deal := range deals {
		if deal.State != "open" && !all {
			r.closed++
			continue
		}
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
		return postgres.Deal{}, refused("A deal ID is a word, the year it renews, a key of its account and, for " +
			"the account's next deal of that year, a number, such as thornfield-2027-2e3hke. gtm-connector deal " +
			"list prints them.")
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
		// A deal's newest card may be a brief an assistant prepared, which the assistant reads back and no
		// command shows: the deal names it, and its card here is null. That is told from the stored card
		// alone, before the workspace's sources are looked at.
		prepared, err := r.preparedBrief(ctx, deal.LatestCard.BriefID)
		if err != nil {
			return nil, err
		}
		if prepared {
			value.Set("latest_card_prepared", true)
		} else {
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
	}
	read := []any{}
	for _, m := range deal.Read {
		match, err := matchValue(m)
		if err != nil {
			return nil, err
		}
		read = append(read, match)
	}
	return strictjson.NewObject("deal", value, "card", card, "matches", read), nil
}

// closeDeal closes an open deal as the owner, with their reason. It first makes the deal's final card, as
// brief makes a card, and the close names that card: when the deal has a newer one by then, or is closed by
// then, the deal is not closed by this command, and the card it made stays the deal's. With --no-card it
// reads nothing, and the final card is the newest the deal has. A read that fails leaves the deal open. A
// closed deal is shown as it is, and an unknown one is refused under a close_deal receipt.
func (r *run) closeDeal(ctx context.Context) (any, error) {
	given, has := r.args.text("--reason")
	if !has {
		return nil, refused("the following arguments are required: --reason")
	}
	reason, err := postgres.CloseReason(given)
	if err != nil {
		return nil, refused("A reason is one line of 1 to 500 characters.")
	}
	deal, err := r.stored(ctx)
	if errors.Is(err, errNoDeal) && r.exists {
		// The refusal has a receipt, as a decision of an unknown match has.
		_, _, err = r.store.CloseDeal(ctx, postgres.DealClose{WorkspaceID: r.workspace.ID, Ref: r.args.positionals[0],
			Reason: reason})
		if errors.Is(err, postgres.ErrNotFound) {
			err = errNoDeal
		}
	}
	if err != nil {
		return nil, err
	}
	if deal.State != "open" {
		return dealValue(deal)
	}
	closing := postgres.DealClose{WorkspaceID: r.workspace.ID, Ref: deal.Ref, Reason: reason}
	brief := ""
	if _, without := r.args.text("--no-card"); !without {
		from, err := r.reads(ctx)
		if err != nil {
			return nil, err
		}
		asOf, given := r.args.text("--as-of")
		if !given {
			asOf = from.day()
		}
		if err := from.open(ctx, "crm", "calls", "slack"); err != nil {
			return nil, err
		}
		r.announce(from.sourcesLine())
		if err := cards.CheckRequest(from.sources(), deal.AccountRef, asOf); err != nil {
			return nil, err
		}
		sealed, err := from.publish(ctx, cards.Principal{WorkspaceID: r.workspace.ID,
			TokenID: receipts.Fingerprint(randomToken()), Scopes: []string{"crm:read", "calls:read", "slack:read"}},
			deal.AccountRef, asOf, deal.Ref)
		if err != nil {
			return nil, err
		}
		if from.stage != "" {
			// The CRM's closed stage came first: the deal closed on the card just made, and the reason given
			// here is not recorded.
			closed, err := r.store.Deal(ctx, r.workspace.ID, deal.Ref)
			if err != nil {
				return nil, err
			}
			r.announce("Final card: " + sealed.BriefID + ".")
			r.announce("The CRM holds the deal's opportunity as closed, " + from.stage + ", so the deal closed on " +
				"that as the card was made. The reason given was not recorded.")
			return dealValue(closed)
		}
		card, err := r.store.CardByBrief(ctx, r.workspace.ID, sealed.BriefID)
		if err != nil {
			return nil, err
		}
		closing.Card, brief = card.ID, sealed.BriefID
	}
	deal, changed, err := r.store.CloseDeal(ctx, closing)
	if changed && err != nil {
		// The close committed, and reading the deal back failed: read it again.
		deal, err = r.store.Deal(ctx, r.workspace.ID, closing.Ref)
	}
	switch {
	case errors.Is(err, postgres.ErrDealChanged) && brief != "":
		return nil, &failure{code: 3, message: "The deal got a newer card, or closed, while this ran, so this " +
			"command did not close it. The card it made, " + brief + ", stays the deal's. Run the command again."}
	case errors.Is(err, postgres.ErrDealChanged):
		return nil, &failure{code: 3, message: "The deal got a newer card, or closed, while this ran, so this " +
			"command did not close it. Run the command again."}
	case err != nil:
		return nil, err
	}
	if brief != "" {
		r.announce("Final card: " + brief + ".")
	}
	return dealValue(deal)
}

// registerDeal reads the account and then its opportunities through the workspace's CRM, each as a tool
// call of its own under a receipt committed before the read, by principal ("" for the owner) with scopes,
// and registers the deal on what they returned. The account must be listed in the workspace, so both
// receipts name it. A grant without crm:read, or one live says is gone just before a read, is refused
// under that read's receipt, and nothing more is read.
func (r *reads) registerDeal(ctx context.Context, principal string, scopes []string,
	live func(context.Context) (bool, error), account, asOf string) (postgres.Deal, error) {
	crm, err := r.crm(ctx)
	if err != nil {
		return postgres.Deal{}, noCRM{err}
	}
	run := "deal-" + strings.ReplaceAll(receipts.RandomID(), "-", "")
	token := receipts.Fingerprint(randomToken())
	read := func(n int, tool string, use func([]*strictjson.Object) error) (receipts.Receipt, error) {
		finished, _, err := r.call(ctx, toolCall{token: token, scopes: scopes, run: run, request: n, tool: tool,
			arguments: strictjson.NewObject("account_id", account), account: account, principal: principal,
			live: live},
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
	deal, err := r.store.RegisterDeal(ctx, registration)
	if err == nil {
		settleMatches(ctx, r.store, r.workspace.ID, account)
	}
	return deal, err
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
	var closed, card, how any
	if d.ClosedAt != nil {
		closed = utcSeconds(*d.ClosedAt)
	}
	if d.Close != nil {
		reason, _, err := evidence.SafeText(d.Close.Reason)
		if err != nil {
			return nil, err
		}
		how = strictjson.NewObject("basis", d.Close.Basis, "reason", orNull(reason), "outcome", orNull(d.Close.Outcome),
			"closed_by", orNull(d.Close.By), "receipt", orNull(d.Close.ReceiptID), "final_card", orNull(d.Close.FinalCard))
	}
	if d.LatestCard != nil {
		card = strictjson.NewObject("id", d.LatestCard.ID, "brief_id", d.LatestCard.BriefID,
			"published_at", utcSeconds(d.LatestCard.PublishedAt))
	}
	return strictjson.NewObject("id", d.Ref, "account", d.AccountRef, "account_name", name, "state", d.State,
		"opportunity", orNull(d.OpportunityRef), "contract", orNull(d.ContractRef), "renews_on", orNull(d.RenewsOn),
		"registered_by", orNull(d.RegisteredBy), "registered_at", utcSeconds(d.RegisteredAt), "closed_at", closed,
		"close", how, "reads", strictjson.NewObject("get_account", d.AccountReadID, "list_opportunities", d.OpportunitiesReadID),
		"latest_card", card), nil
}

// dealText is a deal command's result as a person reads it.
func (r *run) dealText(action string, result any) string {
	if action == "list" {
		listed := result.([]any)
		if len(listed) == 0 {
			if r.closed > 0 {
				return "No open deals. Register one: gtm-connector deal new ACCOUNT_ID" + r.here() +
					"\nList the closed ones: gtm-connector deal list --all" + r.here()
			}
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
	var read []any
	if action == "show" {
		card, _ = get(deal, "card").(*strictjson.Object)
		read, _ = get(deal, "matches").([]any)
		deal = get(deal, "deal").(*strictjson.Object)
	}
	fields := [][]any{{"ID", get(deal, "id")}, {"Account", get(deal, "account")},
		{"Name", strictjson.Quote(get(deal, "account_name").(string))}, {"State", get(deal, "state")},
		{"Opportunity", orNone(get(deal, "opportunity"))}, {"Contract", orNone(get(deal, "contract"))},
		{"Renews", orNone(get(deal, "renews_on"))}}
	if how, _ := get(deal, "close").(*strictjson.Object); how != nil {
		fields = append(fields, []any{"Closed", get(deal, "closed_at")})
		if reason, _ := get(how, "reason").(string); reason != "" {
			fields = append(fields, []any{"Reason", strictjson.Quote(reason)})
		}
		if outcome, _ := get(how, "outcome").(string); outcome != "" {
			fields = append(fields, []any{"Closed by", "the CRM's stage, " + outcome})
		}
		if receipt, _ := get(how, "receipt").(string); receipt != "" {
			fields = append(fields, []any{"Close receipt", receipt}, []any{"Final card", orNone(get(how, "final_card"))})
		}
	}
	// The card names the call and the thread it read, each by its match.
	for _, value := range read {
		m := value.(*strictjson.Object)
		again := ""
		if get(m, "superseded") == true {
			again = ", decided again"
		}
		fields = append(fields, []any{"Read " + get(m, "kind").(string), get(m, "item").(string) + " (match " +
			get(m, "id").(string) + again + ")"})
	}
	text := r.screen.table([]string{"FIELD", "VALUE"}, fields)
	switch {
	case action != "show":
	case card != nil:
		text += "\n\n" + r.cardText(card)
	case get(deal, "latest_card_prepared") == true:
		text += "\n\n" + oneLine("The deal's newest card is a brief an assistant prepared, which the assistant reads "+
			"back. Make a card to read here: gtm-connector brief "+get(deal, "account").(string)+r.here())
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
// reading the CRM as that workspace is connected at the moment of the request. The grant is checked first,
// before any source is opened, and again just before each read. A person with no live grant on the account
// is refused under a receipt, and nothing is read.
type dealRegistry struct{ store *postgres.Store }

func (d dealRegistry) RegisterDeal(ctx context.Context, p authz.Principal, account string) (postgres.Deal, error) {
	workspace, err := d.store.WorkspaceByID(ctx, p.WorkspaceID)
	if err != nil {
		return postgres.Deal{}, err
	}
	live := func(ctx context.Context) (bool, error) {
		return d.store.HasAccountGrant(ctx, workspace.ID, p.ID, account)
	}
	switch granted, err := live(ctx); {
	case err != nil:
		return postgres.Deal{}, err
	case !granted:
		if err := refuseRegistration(ctx, d.store, workspace, p.ID, account); err != nil {
			return postgres.Deal{}, err
		}
		return postgres.Deal{}, authz.ErrNotPermitted
	}
	from, err := workspaceReads(ctx, d.store, workspace)
	if err != nil {
		return postgres.Deal{}, fmt.Errorf("%w: %w", httptransport.ErrNoCRM, err)
	}
	deal, err := from.registerDeal(ctx, p.ID, []string{"crm:read"}, live, account, from.day())
	var (
		failed sourceFailure
		absent noCRM
	)
	switch {
	case errors.Is(err, errDenied):
		return postgres.Deal{}, authz.ErrNotPermitted
	case errors.As(err, &absent):
		return postgres.Deal{}, fmt.Errorf("%w: %w", httptransport.ErrNoCRM, err)
	case errors.As(err, &failed):
		return postgres.Deal{}, fmt.Errorf("%w: %w", httptransport.ErrSourceFailed, err)
	}
	return deal, err
}

// RefreshDeal reads every source of an open deal again as the person and publishes a new card for it, as brief
// does for the owner: each read and the seal name the person. Their grant on the deal's account is checked
// before any source is opened, before each read and as the card is published, and the card is that deal's
// or is not published. A deal that is unknown, or of an account the person holds no live grant on, is refused
// alike, each under a get_account receipt finished as error/403 that reads nothing; the receipt of an unknown
// deal names no account.
func (d dealRegistry) RefreshDeal(ctx context.Context, p authz.Principal, ref string) error {
	workspace, err := d.store.WorkspaceByID(ctx, p.WorkspaceID)
	if err != nil {
		return err
	}
	refuse := func(asked, account string) error {
		if err := refuseRead(ctx, d.store, workspace, p.ID, account, strictjson.NewObject("deal", asked)); err != nil {
			return err
		}
		return authz.ErrNotPermitted
	}
	if !dealID.MatchString(ref) {
		// What is no deal ID is not kept as it was sent.
		return refuse("<invalid>", "")
	}
	deal, err := d.store.Deal(ctx, workspace.ID, ref)
	if errors.Is(err, postgres.ErrNotFound) {
		return refuse(ref, "")
	} else if err != nil {
		return err
	}
	live := func(ctx context.Context) (bool, error) {
		return d.store.HasAccountGrant(ctx, workspace.ID, p.ID, deal.AccountRef)
	}
	switch granted, err := live(ctx); {
	case err != nil:
		return err
	case !granted:
		return refuse(ref, deal.AccountRef)
	case deal.State != "open":
		return httptransport.ErrDealClosed
	}
	from, err := workspaceReads(ctx, d.store, workspace)
	if errors.Is(err, errNotConnected) {
		return fmt.Errorf("%w: %w", httptransport.ErrNoCRM, err)
	} else if err != nil {
		return err
	}
	// A person signed in to the browser reads every source of an account they hold a grant on. A grant that
	// cannot be read is not a grant that ended: the read stops, and the failure is the store's.
	scopes := []string{"crm:read", "calls:read", "slack:read"}
	var unread error
	err = from.open(ctx, "crm", "calls", "slack")
	if err == nil {
		_, err = from.publish(ctx, cards.Principal{WorkspaceID: workspace.ID, PrincipalID: p.ID,
			TokenID: receipts.Fingerprint(randomToken()), Scopes: scopes,
			Verify: func() ([]string, bool) {
				granted, err := live(ctx)
				if err != nil {
					unread = err
				}
				return scopes, err == nil && granted
			}}, deal.AccountRef, from.day(), deal.Ref)
	}
	var (
		read     *cards.ReadError
		told     *cards.Diagnostic
		rejected *evidence.Rejected
		unported *unportedSource
	)
	switch {
	case unread != nil:
		return unread
	case err == nil, errors.Is(err, receipts.ErrSourceChanged), errors.Is(err, postgres.ErrDealChanged):
		return err
	case errors.Is(err, cards.ErrInvalidToken), errors.Is(err, receipts.ErrNotPermitted):
		// The grant ended while the card was read, or as it was published.
		return authz.ErrNotPermitted
	case errors.As(err, &rejected), errors.Is(err, cards.ErrEvidenceInvalid), errors.Is(err, cards.ErrRejected):
		return fmt.Errorf("%w: %w", httptransport.ErrNoCard, err)
	case errors.As(err, &unported):
		return fmt.Errorf("%w: %w", httptransport.ErrNoCRM, err)
	case errors.As(err, &read), errors.As(err, &told), errors.Is(err, cards.ErrBriefing),
		errors.Is(err, cards.ErrSourceFailed):
		return fmt.Errorf("%w: %w", httptransport.ErrSourceFailed, err)
	}
	return err
}

// refuseRegistration writes the receipt of a registration refused before any source was opened: the
// get_account read it would have made, refused as error/403, naming no connection.
func refuseRegistration(ctx context.Context, store *postgres.Store, workspace postgres.Workspace, principal,
	account string) error {
	return refuseRead(ctx, store, workspace, principal, account, strictjson.NewObject("account_id", account))
}

// refuseRead writes the receipt of a deal request refused before any source was opened: the get_account read
// it would have made, with these arguments, refused as error/403, naming no connection. account is "" when
// the request named no account the workspace holds.
func refuseRead(ctx context.Context, store *postgres.Store, workspace postgres.Workspace, principal, account string,
	arguments *strictjson.Object) error {
	log := &postgres.ReceiptStore{Store: store}
	run := "deal-" + strings.ReplaceAll(receipts.RandomID(), "-", "")
	started, err := log.Start(ctx, receipts.Start{WorkspaceID: workspace.ID, OperationID: run + "/1",
		PrincipalID: principal, TokenID: receipts.Fingerprint(randomToken()), Tool: "get_account",
		Scope: "crm:read", AccountRef: account, Arguments: arguments, RequestID: receipts.RPC(1),
		BriefID: &run})
	if err != nil {
		return err
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settling)
	defer cancel()
	_, err = log.Finish(settle, started.Ticket(), receipts.Denied())
	return err
}

// sourceFailure is a failure of a CRM read, or of what it returned, under the read's receipt, as against
// one of the database.
type sourceFailure struct{ error }

func (e sourceFailure) Unwrap() error { return e.error }

// noCRM is a CRM that could not be opened, before any read or receipt.
type noCRM struct{ error }

func (e noCRM) Unwrap() error { return e.error }

const dealHelp = `usage: gtm-connector deal [-h] {new,list,show,close,matches,decide} ...

Register, list, show and close deals: one renewal of one account, under a readable ID. List the matches of
calls and Slack threads that wait for a person, and decide one.

positional arguments:
  {new,list,show,close,matches,decide}
    new                 Register a deal for an account from the connected CRM.
    list                List the deals of a workspace.
    show                Show a deal's bindings and its newest card.
    close               Close a deal.
    matches             List the matches that wait for a person.
    decide              Match a call or thread to a deal, or keep it off every card.

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

const dealListHelp = `usage: gtm-connector deal list [-h] [--json] [--state-dir STATE_DIR] [--all]

List the open deals of a workspace. With --all, list the closed ones after them.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --all                 List the closed deals too.
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

const dealCloseHelp = `usage: gtm-connector deal close [-h] [--json] [--state-dir STATE_DIR]
                                --reason REASON [--no-card] [--as-of AS_OF]
                                deal_id

Close a deal, with a reason. The close first makes the deal's final card, as brief makes a card, and gets a
close_deal receipt that names the reason and that card. The deal's cards and receipts are kept, and brief
makes the account's card again. A read that fails leaves the deal open.

positional arguments:
  deal_id               The deal's ID, such as thornfield-2027-2e3hke.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --reason REASON       Why the deal closes: one line of 1 to 500 characters.
  --no-card             Read nothing. The final card is the newest the deal has.
  --as-of AS_OF         The final card's planning date, YYYY-MM-DD.

Example: gtm-connector deal close thornfield-2027-2e3hke --reason "Renewed for two years"
`

const dealMatchesHelp = `usage: gtm-connector deal matches [-h] [--json] [--state-dir STATE_DIR] [--all]

List the matches of calls and Slack threads that wait for a person: those that fit more than one deal of
their account. With --all, list every call's and thread's current decision.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --all                 List every current decision, not only those that wait.
`

const dealDecideHelp = `usage: gtm-connector deal decide [-h] [--json] [--state-dir STATE_DIR]
                                 (--deal DEAL_ID | --reject)
                                 match_id

Decide a match as the owner: put its call or thread on one deal of its account, or keep it off every card.
The decision gets a receipt and replaces the match's current one.

positional arguments:
  match_id              The match's ID, from gtm-connector deal matches.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --deal DEAL_ID        The deal of the match's account it belongs to.
  --reject              Keep the call or thread off every card.

Example: gtm-connector deal decide MATCH_ID --deal thornfield-2027-2e3hke
`
