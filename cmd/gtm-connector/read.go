package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The read commands: accounts, brief, inspect and audit. They are the
// commands of the person who runs the install, so they connect as the app role and read a whole workspace.
// A card they make is the owner's: its receipts name no principal, and a bearer made for the run, never
// stored or shown, gives them their token ID. What a workspace reads comes from sources.go.

// defaultWorkspace is the workspace a command reads without --state-dir: the one setup makes.
const defaultWorkspace = "demo"

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// hashedSlug is the form of a slug made from a name that is not one.
var hashedSlug = regexp.MustCompile(`^dir-[0-9a-f]{32}$`)

// workspaceSlug is the workspace a --state-dir names. A name that is a slug is that workspace's slug. Any
// other name, such as the path of a folder, names the workspace whose slug is "dir-" and the first 32 hex
// digits of the SHA-256 of the name as given.
//
// A name already in that "dir-" form is not taken as a slug. It is hashed like any other name, so no name
// a person types can select the workspace that a folder path names.
func workspaceSlug(name string) string {
	if slugPattern.MatchString(name) && !hashedSlug.MatchString(name) {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	return "dir-" + hex.EncodeToString(digest[:16])
}

// run is one command with its arguments.
type run struct {
	command string
	args    parsed
	screen  screen
	// name is the workspace's name as --state-dir gave it.
	name      string
	store     *postgres.Store
	workspace postgres.Workspace
	// exists is whether a workspace has that name. Nothing is in one that does not exist.
	exists bool
	// action is the form inspect was given: account, brief, numbers or receipt.
	action string
	// admitted is the operator's word a card or receipt inspect shows was admitted on, or nil for one made here.
	admitted *postgres.OperatorWord
}

// folder is the --state-dir that names this workspace in the commands printed for it.
func (r *run) folder() string {
	if strings.Contains(r.name, "://") {
		// A URL given by mistake may hold a password, so it is not printed back.
		return " --state-dir NAME"
	}
	return " --state-dir " + shellQuote(r.name)
}

// here is the same, left out when the workspace is the default.
func (r *run) here() string {
	if r.name == defaultWorkspace {
		return ""
	}
	return r.folder()
}

// open connects to the database and finds the workspace.
func (r *run) open(ctx context.Context) error {
	c, err := loadConfig()
	if err != nil {
		// loadConfig's errors are this program's own sentences about the settings it was given.
		return local("%s", err)
	}
	if r.store, err = openAppStore(ctx, c); err != nil {
		return err
	}
	r.workspace, err = r.store.Workspace(ctx, workspaceSlug(r.name))
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	r.exists = err == nil
	return err
}

// announce names what a command reads, once, on stderr. A JSON result keeps stderr for its one error.
func (r *run) announce(line string) {
	if line != "" && !r.screen.json {
		fmt.Fprintln(r.screen.err, line)
	}
}

// reads is what the workspace is connected to. A workspace that does not exist has no CRM.
func (r *run) reads(ctx context.Context) (*reads, error) {
	if !r.exists {
		return nil, errNotConnected
	}
	return workspaceReads(ctx, r.store, r.workspace)
}

// noAuditLog is the refusal of a workspace that does not exist, which holds no receipt.
func (r *run) noAuditLog() *failure {
	return &failure{code: 2, message: "audit database not found; run the demo first",
		next: "gtm-connector demo" + r.folder()}
}

// runCommand runs one command and gives its exit code.
func runCommand(ctx context.Context, command string, tokens []string, s screen) int {
	spec := commands[command]
	args, err := spec.parse(tokens)
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
	result, err := r.result(ctx)
	if err != nil {
		return s.fail(command, r.failed(ctx, err))
	}
	if result == nil {
		return 0
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
		return 0
	}
	fmt.Fprintln(s.out, r.human(result))
	if cut, ok := result.(*strictjson.Object); ok && command == "accounts" {
		// On stderr, so piped rows stay rows.
		listed, _ := get(cut, "accounts").([]any)
		fmt.Fprintf(s.err, "Showing the first %d accounts. More match; narrow the search with --query.\n", len(listed))
	}
	return 0
}

// failed is an error as the command reports it: its exit code, its message and what to run next.
func (r *run) failed(ctx context.Context, err error) *failure {
	var (
		known    *failure
		told     *cards.Diagnostic
		unported *unportedSource
		request  *cards.RequestError
		rejected *evidence.Rejected
		read     *cards.ReadError
	)
	switch {
	case errors.As(err, &known):
		return known
	case errors.As(err, &told):
		// A source's refusal, in the fixed words it was written in for the person running the command.
		return &failure{code: told.Exit, message: told.Message, next: told.Next}
	case ctx.Err() != nil:
		return &failure{code: 130, message: "Interrupted. Completed local writes are retained.",
			next: "gtm-connector audit --help"}
	case errors.Is(err, errNotConnected):
		return r.notConnected()
	case errors.Is(err, postgres.ErrPinned):
		return pinnedElsewhere
	case errors.As(err, &unported):
		return &failure{code: 3, message: unported.Error()}
	case errors.As(err, &request):
		return refused("%s", request.Message)
	case errors.As(err, &rejected):
		// The card's own ID, then that it was refused: the evidence stays out of the message.
		message, _, _ := strings.Cut(err.Error(), ": ")
		return &failure{code: 4, message: message}
	case errors.As(err, &read) && read.HTTPStatus != 0:
		return &failure{code: 5, message: fmt.Sprintf("%s: HTTP %d; no brief generated", read.Tool, read.HTTPStatus)}
	case errors.As(err, &read):
		return &failure{code: 5, message: read.Tool + ": invalid or failed tool result; no brief generated"}
	case errors.Is(err, cards.ErrBriefing):
		return &failure{code: 5, message: "required evidence could not be collected; no brief generated"}
	case errors.Is(err, receipts.ErrSourceChanged):
		return &failure{code: 3, message: "A source of this workspace was connected again while it was read; " +
			"nothing was published. Run the command again."}
	}
	return &failure{code: 1, message: plain(err)}
}

// notConnected is the refusal of a workspace with no CRM, with what this build connects. When Salesforce
// can be attached, it is the Python CLI's own advice, with the workspace named: the default one holds demo
// data, so Salesforce goes in one named with --state-dir.
func (r *run) notConnected() *failure {
	advice := "Run gtm-connector connect demo" + r.here() + " for invented data."
	if kindsNamed("salesforce")[0].attach != nil {
		advice = "Run gtm-connector connect salesforce --org ALIAS" + cmp.Or(r.here(), " --state-dir NAME") +
			", or gtm-connector connect demo" + r.here() + " for invented data."
	}
	return &failure{code: 2, next: "gtm-connector connect --help", message: "No CRM is connected. " + advice}
}

func (r *run) result(ctx context.Context) (any, error) {
	switch r.command {
	case "accounts":
		return r.accounts(ctx)
	case "brief":
		return r.brief(ctx)
	case "inspect":
		return r.inspect(ctx)
	case "connect":
		return r.connect(ctx)
	case "demo":
		return r.demo(ctx)
	case "admit":
		return r.admit(ctx)
	}
	return r.audit(ctx)
}

// accounts searches the connected CRM by name. A complete list is an array. A cut one is an object that
// says so. Names are shown inert.
func (r *run) accounts(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	if err := from.open(ctx, "crm"); err != nil {
		return nil, err
	}
	r.announce(from.crmLine())
	query, _ := r.args.text("--query")
	found, more, err := from.searchAccounts(ctx, query)
	if err != nil {
		return nil, err
	}
	rows := make([]any, len(found))
	for i, row := range found {
		rows[i] = row
	}
	shown, err := neutral(rows)
	if err != nil || !more {
		return shown, err
	}
	return strictjson.NewObject("accounts", shown, "has_more", true), nil
}

// brief reads one account through the card maker and publishes its card. The raw records stay in the
// stored card: the result holds neutralized text only.
func (r *run) brief(ctx context.Context) (any, error) {
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
	// The card reads the CRM, Slack, and the calls unless its grant leaves them out. A source it will read
	// and this build cannot is refused here, before anything is read or written. One it will not read is
	// not opened.
	scopes, reading := []string{"crm:read", "calls:read", "slack:read"}, []string{"crm", "calls", "slack"}
	if _, without := r.args.text("--without-calls"); without {
		scopes, reading = []string{"crm:read", "slack:read"}, []string{"crm", "slack"}
	}
	if err := from.open(ctx, reading...); err != nil {
		return nil, err
	}
	r.announce(from.sourcesLine())
	// The maker's own checks of the request come before any lookup or write: an account ID that is not of
	// this workspace's CRM, or a day that is not one, is refused with nothing read and nothing written.
	if err := cards.CheckRequest(from.sources(), account, asOf); err != nil {
		return nil, err
	}
	// A card belongs to an account of its workspace, so an account the CRM holds is listed there first.
	if err := from.list(ctx, account); err != nil {
		return nil, err
	}
	sealed, err := from.maker().Brief(ctx, cards.Brief{
		Principal: cards.Principal{WorkspaceID: r.workspace.ID, TokenID: receipts.Fingerprint(randomToken()),
			Scopes: scopes},
		OperationID: "brief-" + receipts.RandomID(), AccountID: account, AsOf: asOf, IncludeSlack: true})
	if err != nil {
		return nil, err
	}
	value, err := strictjson.Loads(sealed.Payload)
	if err != nil {
		return nil, err
	}
	card := value.(*strictjson.Object)
	card.Delete("evidence")
	return card, nil
}

var (
	briefID   = regexp.MustCompile(`^brief-[0-9a-f]{32}$`)
	accountID = regexp.MustCompile(`^acc-(?:[0-9]{3}|sf-[A-Za-z0-9]{18})$`)
	receiptID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

const inspectForms = "inspect takes an account ID (acc-003, or acc-sf- and 18 characters), a brief ID (brief- and " +
	"32 lowercase hex digits), a brief ID and numbers printed on its card, or a receipt ID from audit."

// inspect shows an account, a saved card, numbers on it, or a receipt. Only the account is read from a
// source; the other forms read the stored cards and receipts, and nothing is written.
func (r *run) inspect(ctx context.Context) (any, error) {
	target, numbers := r.args.positionals[0], r.args.positionals[1:]
	switch {
	case briefID.MatchString(target) && len(numbers) > 0:
		r.action = "numbers"
	case briefID.MatchString(target):
		r.action = "brief"
	case len(numbers) > 0:
		return nil, refused(inspectForms)
	case accountID.MatchString(target):
		r.action = "account"
	case receiptID.MatchString(target):
		r.action = "receipt"
	default:
		return nil, refused(inspectForms)
	}
	role, filtered := r.args.text("--role")
	if filtered && r.action != "brief" {
		return nil, refused("--role lists one role's sentences of a saved card. Give it a brief ID alone.")
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	switch r.action {
	case "account":
		return r.account(ctx, target)
	case "receipt":
		if !r.exists {
			return nil, r.noAuditLog()
		}
		var value *strictjson.Object
		receipt, err := r.store.Receipt(ctx, r.workspace.ID, target)
		switch {
		case errors.Is(err, postgres.ErrNotFound):
			// A receipt the Python package wrote, admitted with its card.
			value, err = r.admittedReceipt(ctx, target)
		case err == nil:
			value, err = receipt.AuditValue()
		}
		if err != nil {
			return nil, err
		}
		// A caller's arguments can hold any text: shown, it is inert as source text on a card is.
		arguments, err := neutral(get(value, "arguments"))
		value.Set("arguments", arguments)
		if r.admitted != nil {
			value.Set("admitted", strictjson.NewObject("from_org", r.admitted.FromOrg, "note", admittedNote))
		}
		return value, err
	}
	// A card is read from the sources its workspace is connected to, so a workspace with none holds no card.
	from, err := r.reads(ctx)
	if errors.Is(err, errNotConnected) {
		return nil, refused("%s", cards.ErrNoCard)
	}
	if err != nil {
		return nil, err
	}
	card, err := r.sealed(ctx, target, from.sources())
	if err != nil {
		return nil, err
	}
	if r.action == "numbers" {
		traced, err := traces(card, target, numbers)
		if err != nil || r.admitted == nil {
			return traced, err
		}
		for _, found := range traced.([]any) {
			found.(*strictjson.Object).Set("admitted", r.admittedValue())
		}
		return traced, nil
	}
	units, _ := get(card, "units").([]any)
	listed := []any{}
	for _, value := range units {
		unit, err := evidence.AsShown(value.(*strictjson.Object))
		if err != nil {
			return nil, err
		}
		if !filtered || get(unit, "role") == role {
			listed = append(listed, unit)
		}
	}
	result := strictjson.NewObject("brief_id", target, "account_id", get(card, "account_id"), "units", listed)
	if r.admitted != nil {
		result.Set("admitted", r.admittedValue())
	}
	return result, nil
}

// account is the account as the CRM returns it, with the newest saved card of it that the workspace holds
// and inspect can open.
func (r *run) account(ctx context.Context, id string) (any, error) {
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	if err := from.open(ctx, "crm"); err != nil {
		return nil, err
	}
	r.announce(from.crmLine())
	found, ok, err := from.account(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &failure{code: 2, message: "account not found: " + id, next: "gtm-connector accounts" + r.folder()}
	}
	shown, err := neutral(found)
	if err != nil {
		return nil, err
	}
	account := shown.(*strictjson.Object)
	briefs, err := r.store.AccountBriefs(ctx, r.workspace.ID, id)
	if err != nil {
		return nil, err
	}
	var newest any
	for _, brief := range briefs {
		if card, err := r.sealed(ctx, brief, from.sources()); err == nil && get(card, "account_id") == id {
			newest = brief
			break
		}
	}
	account.Set("newest_brief_id", newest)
	return account, nil
}

// sealed is a stored card, only when its bytes are the ones its seal receipt names, its read receipts are
// the ones the workspace holds, it was read from the sources the workspace is bound to, and every number
// on it resolves (cards.VerifyCard).
func (r *run) sealed(ctx context.Context, brief string, bound evidence.Sources) (*strictjson.Object, error) {
	stored, err := r.store.CardByBrief(ctx, r.workspace.ID, brief)
	if errors.Is(err, postgres.ErrNotFound) {
		// A card the Python package made, admitted into this workspace.
		return r.admittedCard(ctx, brief, bound)
	}
	if err != nil {
		return nil, err
	}
	value, err := strictjson.Loads(stored.Bytes)
	card, _ := value.(*strictjson.Object)
	if err != nil || card == nil {
		return nil, refused("%s", cards.ErrInvalidCard)
	}
	if get(card, "kind") == "selected_brief" {
		return nil, refused("This brief was prepared by an assistant with prepare_renewal_brief, which reads it " +
			"back with read_renewal_brief. inspect reads cards saved by brief or demo.")
	}
	log := cards.StoreLog{Store: &postgres.ReceiptStore{Store: r.store}, WorkspaceID: r.workspace.ID}
	if _, err := cards.VerifyCard(ctx, stored.Bytes, brief, log, bound); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, refused("%s", err)
	}
	return card, nil
}

// replaced is whether a speaker label holds what neutralizing can put there: a bracketed mention, user,
// channel, link or character code, or a number in parentheses. Two labels that differ only where these
// stand print alike.
func replaced(label string) bool {
	runes := []rune(label)
	for i, c := range runes {
		switch c {
		case '[':
			rest := string(runes[i+1:])
			for _, word := range []string{"mention", "user", "channel", "link", "U+"} {
				if strings.HasPrefix(rest, word) && strings.Contains(rest[len(word):], "]") {
					return true
				}
			}
		case '(':
			end := i + 1
			for end < len(runes) && unicodetab.ReDigit(runes[end]) {
				end++
			}
			if end > i+1 && end < len(runes) && runes[end] == ')' {
				return true
			}
		}
	}
	return false
}

// traces resolves numbers on one saved card from its sealed units and evidence, never from the card's
// text: each to its record, the receipt of the read that returned it, and, for a sentence, the next whole
// sentence under the same speaker label in the same record.
func traces(card *strictjson.Object, brief string, numbers []string) (any, error) {
	units, _ := get(card, "units").([]any)
	snapshot, _ := get(card, "evidence").(*strictjson.Object)
	trace := evidence.NewTrace(snapshot)
	traced := make([]any, len(numbers))
	for i, text := range numbers {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > len(units) {
			return nil, refused("This card has numbers 1 to %d.", len(units))
		}
		receipt, record, ok := trace.Resolve(units, n)
		if !ok {
			return nil, refused("Number %d does not resolve to a record and a receipt of its own card.", n)
		}
		sealed := units[n-1].(*strictjson.Object)
		unit := sealed.Copy()
		unit.Delete("tool")
		unit.Set("receipt", strictjson.NewObject("audit_id", get(receipt, "audit_id"), "tool", get(receipt, "tool"),
			"status", get(receipt, "status"), "http_status", get(receipt, "http_status")))
		if get(unit, "kind") == "record" {
			fields, err := recordFields(record)
			if err != nil {
				return nil, err
			}
			unit.Set("fields", fields)
		}
		if unit, err = evidence.AsShown(unit); err != nil {
			return nil, err
		}
		var following any
		if speaker, labelled := get(sealed, "speaker").(string); get(sealed, "kind") == "sentence" && labelled &&
			!replaced(speaker) {
			for _, later := range units[n:] {
				next, _ := later.(*strictjson.Object)
				if get(next, "kind") == "sentence" && get(next, "partial") == false &&
					get(next, "record_ref") == get(sealed, "record_ref") && get(next, "speaker") == speaker {
					following = get(next, "n")
					break
				}
			}
		}
		found := strictjson.NewObject("brief_id", brief, "account_id", get(card, "account_id"))
		for k, key := range unit.Keys {
			found.Set(key, unit.Values[k])
		}
		found.Set("next_by_speaker", following)
		traced[i] = found
	}
	return traced, nil
}

// recordFields is a record's plain values by name, each string neutralized and on one line.
func recordFields(record *strictjson.Object) (*strictjson.Object, error) {
	keys := append([]string(nil), record.Keys...)
	sort.Strings(keys)
	fields := &strictjson.Object{}
	for _, key := range keys {
		value := get(record, key)
		text, isText := value.(string)
		if _, plain := evidence.PyStr(value); key == "record_ref" || !plain {
			continue
		}
		if isText {
			safe, _, err := evidence.SafeText(text)
			if err != nil {
				return nil, err
			}
			value = strings.Join(strings.FieldsFunc(safe, unicodetab.IsSpace), " ")
		}
		fields.Set(key, value)
	}
	return fields, nil
}

// filters reads audit's options, as inspection.ReceiptFilters checks them before any receipt is read.
func (r *run) filters() (postgres.ReceiptFilter, error) {
	var f postgres.ReceiptFilter
	blank := func(text string) bool {
		return strings.TrimFunc(text, unicodetab.IsSpace) == ""
	}
	for name, field := range map[string]**string{"--tool": &f.Tool, "--caller": &f.TokenID, "--status": &f.Status} {
		if text, given := r.args.text(name); given {
			*field = &text
		}
	}
	since, hasSince := r.args.text("--since")
	until, hasUntil := r.args.text("--until")
	for _, name := range []string{"--tool", "--caller", "--since", "--until", "--status"} {
		if text, given := r.args.text(name); given && blank(text) {
			return f, refused("Receipt filters must be nonempty text.")
		}
	}
	if f.TokenID != nil && !callerPattern.MatchString(*f.TokenID) {
		return f, refused("--caller must be a complete SHA-256 fingerprint or anonymous, not a bearer token.")
	}
	var err error
	if hasSince {
		if f.Since, err = timestamp(since, "--since"); err != nil {
			return f, err
		}
	}
	if hasUntil {
		if f.Until, err = timestamp(until, "--until"); err != nil {
			return f, err
		}
	}
	if hasUntil && f.Until.IsZero() {
		// The store reads a zero time as no bound. Nothing started before the first instant, so the
		// bound is kept by moving it one step.
		f.Until = f.Until.Add(time.Nanosecond)
	}
	if hasSince && hasUntil && !f.Since.Before(f.Until) {
		return f, refused("--since must be earlier than --until.")
	}
	if brief, given := r.args.text("--brief"); given {
		f.BriefID = &brief
	}
	return f, nil
}

var callerPattern = regexp.MustCompile(`^(?:[0-9a-f]{64}|anonymous)$`)

// timestamp reads an ISO 8601 time that carries its UTC offset.
func timestamp(text, label string) (time.Time, error) {
	if moment, ok := evidence.Instant(text); ok {
		return moment, nil
	}
	// A day alone, or a time with no offset, is a time and says where it was not.
	_, dayErr := evidence.DateFromISO(text)
	if _, timed := evidence.Instant(text + "+00:00"); dayErr == nil || timed {
		return time.Time{}, refused("%s must include its UTC offset.", label)
	}
	return time.Time{}, refused("%s must be an ISO 8601 timestamp with a UTC offset.", label)
}

// audit lists the workspace's receipts in the order they started. With --follow it prints the matching
// receipts and then each new or changed one, until it is interrupted.
func (r *run) audit(ctx context.Context) (any, error) {
	filter, err := r.filters()
	if err != nil {
		return nil, err
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	if !r.exists {
		return nil, r.noAuditLog()
	}
	read := func() ([]any, error) {
		found, err := r.store.Receipts(ctx, r.workspace.ID, filter)
		if err != nil {
			return nil, err
		}
		rows := make([]any, len(found))
		for i, receipt := range found {
			if rows[i], err = receipt.AuditValue(); err != nil {
				return nil, err
			}
		}
		return rows, nil
	}
	if _, follow := r.args.text("--follow"); !follow {
		return read()
	}
	// Whole rows are compared, so a pending receipt is printed again when it finishes.
	seen := map[string]string{}
	for {
		rows, err := read()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			line, err := strictjson.Dumps(row, strictjson.Quoted)
			if err != nil {
				return nil, err
			}
			id, _ := get(row.(*strictjson.Object), "audit_id").(string)
			if seen[id] == line {
				continue
			}
			seen[id] = line
			if !r.screen.json {
				line = r.auditTable([]any{row})
			}
			fmt.Fprintln(r.screen.out, line)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
