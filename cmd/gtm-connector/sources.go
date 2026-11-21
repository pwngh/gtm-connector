package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The sources of a workspace. This file is the one place where a workspace's stored connections become
// the reads its commands use. accounts, brief, inspect, audit, demo and connect name no source of their
// own: they go through workspaceReads and sourceKinds.
//
// A workspace's connections are rows, one for each of "crm", "calls" and "slack" (postgres.Connection).
// A row holds a mode, which names the kind of source it is connected to; a binding, the JSON object of
// what the operator chose, which connect status shows; and the digest of the identity the source is
// pinned to. No row holds a credential, an organization ID or an address.
//
// To wire one real source, a change does three things, all in this file's terms:
//
//  1. It implements the interface of its source: crmSource for Salesforce, callsSource for imported
//     transcripts, slackSource for a mapped Slack thread. A refusal a person can act on is a *failure,
//     which a command prints with its exit code and next command. Any other error prints as "internal
//     error", so the text of an upstream answer never reaches the screen. In a card's read, any error
//     ends the read as failed and its receipt says so.
//  2. In its entry of sourceKinds it sets the opener of its source (crm, calls or slack): the function
//     that turns the connection's row into a connected source, with the backend and binding a card
//     records for it and the words a command names it by. An opener reads the row and nothing else. It
//     makes no request and starts no process. A source that signs in does so on its first read, and
//     checks then that it reached the identity the row is pinned to.
//  3. In the same entry it sets attach: what "connect NAME" checks before the row is saved, and the
//     binding and identity digest the row is saved with.
//
// accounts, brief, inspect and audit do not change for a new source. What they need beyond the reads is
// here too: outside a card they read a real CRM as its tool, under a receipt committed before the read
// (ownerRead); a card without --as-of is planned from today for a real CRM (day); and the workspace's
// seller list reaches every card (sources). TestASourceRegisteredInItsKindIsReadByEveryCommand wires a
// CRM this way, against an invented org.
//
// An entry with no opener is refused by name by every command that would read it, and an entry with no
// attach is refused by connect. Neither is read as demo data: only a row whose mode is "demo" opens the
// demo book, and only beside the demo CRM. A workspace is made for demo data or for real sources and
// keeps that, so the two never share one.
//
// serve's MCP endpoint (mcp.go) serves the demo workspace. It takes that workspace's connections from
// here and reads the demo book itself.

// crmSource is a workspace's CRM.
type crmSource interface {
	// Read is one of the two CRM reads, "get_account" or "list_opportunities", for one account: its
	// records, each with its record_ref. get_account gives the account, then its contacts, then its
	// contracts when the CRM has contracts. An account the CRM does not hold is errAccountNotFound.
	Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error)
	// SearchAccounts gives the accounts whose name holds the query as literal text, the first hundred by
	// name and then ID, and whether more match.
	SearchAccounts(ctx context.Context, query string) (accounts []*strictjson.Object, more bool, err error)
}

// errAccountNotFound is a CRM's answer for an account it does not hold.
var errAccountNotFound = errors.New("account not found")

// callsSource is a workspace's call records.
type callsSource interface {
	// CallNotes is the read_call_notes read of a card: the account's call records, each with its
	// record_ref.
	CallNotes(ctx context.Context, accountID string) ([]*strictjson.Object, error)
}

// slackSource is a workspace's Slack.
type slackSource interface {
	// Messages is the read_slack_messages read of a card: the account's messages, each with its
	// record_ref, and for a live thread the snapshot of the read they are checked against. An account
	// with no thread selected gives cards.ErrNotSelected.
	Messages(ctx context.Context, accountID string) (records []*strictjson.Object, snapshot *strictjson.Object, err error)
}

// connected is one source of a workspace, opened.
type connected[R any] struct {
	reads R
	// backend is the source as a card records it: "sqlite" or "salesforce" for the CRM, "fixture" or
	// "imported" for calls, "fixture" or "slack" for Slack. "none" is a source that is not connected.
	backend string
	// binding is what a card records the source as bound to, or nil.
	binding any
	// described is how a command names the source on its sources line, such as "demo data".
	described string
}

// opening is what an opener is given.
type opening struct {
	// store and workspace are for a source whose records are kept in PostgreSQL, as imported transcripts
	// and mapped threads are.
	store     *postgres.Store
	workspace postgres.Workspace
	// connection is the source's own row, and binding is the row's binding, read.
	connection postgres.Connection
	binding    *strictjson.Object
	// crm is the workspace's CRM, opened before its calls and Slack, which belong to it.
	crm connected[crmSource]
}

// attaching is what connect was asked to attach.
type attaching struct {
	// args are connect's own options, such as --org.
	args parsed
	// crm is the workspace's CRM as it is connected now, or nil.
	crm *postgres.Connection
}

// attached is what a source's row is saved with.
type attached struct {
	// binding is what the operator chose. connect status shows it. Nil is the empty object.
	binding *strictjson.Object
	// identity is the SHA-256 of the identity the source is pinned to, or "".
	identity string
}

// sourceKind is one kind of source a workspace can be connected to.
type sourceKind struct {
	provider string // the name connect takes: "demo", "salesforce", "calls" or "slack"
	source   string // the source it fills: "crm", "calls" or "slack"
	mode     string // the mode of its connection row
	shown    string // the provider connect status shows
	title    string // how connect's first line names it
	plural   bool   // whether that name takes "were"
	detail   string // what connect's table and a refusal say the source is connected to
	// live is a real source. Its workspace is made as one that holds no demo data.
	live bool
	// options are the options connect takes with it, besides --remove.
	options []string
	// attach checks what connect was asked for before anything is saved. Nil is a source connect does
	// not attach in this build.
	attach func(ctx context.Context, a attaching) (attached, error)
	// The opener of the kind's own source. A kind with none is not read in this build.
	crm   func(ctx context.Context, o opening) (connected[crmSource], error)
	calls func(ctx context.Context, o opening) (connected[callsSource], error)
	slack func(ctx context.Context, o opening) (connected[slackSource], error)
}

// sourceNames are the sources of a workspace, the CRM first: calls and Slack belong to it.
var sourceNames = []string{"crm", "calls", "slack"}

// sourceKinds is every kind of source. "connect demo" attaches the three demo kinds together.
var sourceKinds = []*sourceKind{
	{provider: "demo", source: "crm", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		attach: attachDemo, crm: demoCRM},
	{provider: "demo", source: "calls", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		attach: attachDemo, calls: demoCalls},
	{provider: "demo", source: "slack", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		attach: attachDemo, slack: demoSlack},
	// internal/sources/salesforce reads it. Nothing opens it here yet.
	{provider: "salesforce", source: "crm", mode: "sf", shown: "salesforce", title: "Salesforce",
		detail: "Salesforce", live: true, options: []string{"--org", "--environment", "--seller"}},
	// Imported transcripts. Nothing opens them here yet.
	{provider: "calls", source: "calls", mode: "imported", shown: "imported", title: "Call transcripts", plural: true,
		detail: "imported transcripts", live: true, options: []string{"--seller"}},
	// internal/sources/slack reads it. Nothing opens it here yet.
	{provider: "slack", source: "slack", mode: "slack", shown: "slack", title: "Slack",
		detail: "the saved Slack sign-in", live: true, options: []string{"--seller"}},
}

// providerNames are the names connect takes for a source, each once, in the order of sourceKinds.
func providerNames() []string {
	var names []string
	for _, kind := range sourceKinds {
		if len(names) == 0 || names[len(names)-1] != kind.provider {
			names = append(names, kind.provider)
		}
	}
	return names
}

// kindsNamed are the kinds connect attaches for one name.
func kindsNamed(provider string) []*sourceKind {
	var kinds []*sourceKind
	for _, kind := range sourceKinds {
		if kind.provider == provider {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// kindOf is the kind of one connection row, or nil for a mode no kind has. "fixture" is the demo data
// too.
func kindOf(source, mode string) *sourceKind {
	if mode == "fixture" {
		mode = "demo"
	}
	for _, kind := range sourceKinds {
		if kind.source == source && kind.mode == mode {
			return kind
		}
	}
	return nil
}

// errNotConnected is a workspace with no CRM: there is nothing to read an account from.
var errNotConnected = errors.New("no CRM is connected")

// unportedSource is a connected source that this build has no opener for.
type unportedSource struct{ source string }

func (e *unportedSource) Error() string {
	return "This workspace is connected to " + e.source + ", which this command does not read yet."
}

// opened opens one connection with the opener of its kind. A source with none is refused by name.
func opened[R any](ctx context.Context, o opening, named string,
	opener func(context.Context, opening) (connected[R], error)) (connected[R], error) {
	if opener == nil {
		return connected[R]{}, &unportedSource{named}
	}
	return opener(ctx, o)
}

// reads is what a workspace's commands read: its three sources, opened, the connection of each, and the
// sellers its operator listed.
type reads struct {
	crm         connected[crmSource]
	calls       connected[callsSource]
	slack       connected[slackSource]
	connections map[string]string // each connected source's connection ID
	sellers     []string
}

// workspaceReads opens what a workspace is connected to. A workspace with no CRM gives errNotConnected.
// A connection of a kind this build does not read is refused by name, whichever command asked.
func workspaceReads(ctx context.Context, store *postgres.Store, workspace postgres.Workspace) (reads, error) {
	r := reads{connections: map[string]string{},
		calls: connected[callsSource]{backend: "none", described: "not connected"},
		slack: connected[slackSource]{backend: "none", described: "not connected"}}
	rows, err := store.Connections(ctx, workspace.ID)
	if err != nil {
		return r, err
	}
	held := map[string]postgres.Connection{}
	for _, row := range rows {
		if row.Mode != "none" {
			held[row.Source] = row
		}
	}
	if _, has := held["crm"]; !has {
		return r, errNotConnected
	}
	o := opening{store: store, workspace: workspace}
	for _, source := range sourceNames {
		row, has := held[source]
		if !has {
			continue
		}
		kind := kindOf(source, row.Mode)
		if kind == nil {
			// A mode no kind has: nothing opens it, and no refusal can say what it is.
			kind = &sourceKind{detail: "a source this build does not know"}
		}
		o.connection = row
		if o.binding, err = bindingOf(row); err != nil {
			return r, err
		}
		switch source {
		case "crm":
			r.crm, err = opened(ctx, o, kind.detail, kind.crm)
			o.crm = r.crm
		case "calls":
			r.calls, err = opened(ctx, o, kind.detail, kind.calls)
		case "slack":
			r.slack, err = opened(ctx, o, kind.detail, kind.slack)
		}
		if err != nil {
			return r, err
		}
		r.connections[source] = row.ID
	}
	r.sellers, err = store.Sellers(ctx, workspace.ID)
	return r, err
}

// bindingOf reads a row's binding, which is one JSON object.
func bindingOf(row postgres.Connection) (*strictjson.Object, error) {
	value, err := strictjson.Loads([]byte(row.Binding))
	binding, ok := value.(*strictjson.Object)
	if err != nil || !ok || binding == nil {
		return nil, local("The %s connection of this workspace holds a binding that is not a JSON object.", row.Source)
	}
	return binding, nil
}

// sources is what the workspace is connected to, as a card records it.
func (r reads) sources() evidence.Sources {
	return evidence.Sources{CRMBackend: r.crm.backend, CRMBinding: r.crm.binding,
		CallsBackend: r.calls.backend, CallsBinding: r.calls.binding,
		SlackBackend: r.slack.backend, SlackBinding: r.slack.binding, Sellers: r.sellers}
}

// maker makes cards from these sources on PostgreSQL.
func (r reads) maker(store *postgres.Store) *cards.Maker {
	return &cards.Maker{Receipts: &postgres.ReceiptStore{Store: store}, Source: cardSource{r}, Sources: r.sources(),
		Connections: r.connections}
}

// day is the day a card is planned from when the command names none: the demo book's own day, and today
// in UTC for a CRM that is read as it is now.
func (r reads) day() string {
	if r.crm.backend == "sqlite" {
		return cards.DemoAsOf
	}
	return time.Now().UTC().Format("2006-01-02")
}

// sourcesLine names what a card reads, once, on stderr.
func (r reads) sourcesLine() string {
	crm, calls, slack := r.crm.described, r.calls.described, r.slack.described
	if crm == calls && calls == slack {
		return "Sources: CRM, calls, and Slack are " + crm + "."
	}
	return "Sources: CRM is " + crm + "; calls are " + calls + "; Slack is " + slack + "."
}

// cardSource is a workspace's three sources as the card maker reads them: one read for each tool, from
// the source the tool belongs to.
type cardSource struct{ reads }

func (s cardSource) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	switch tool {
	case "get_account", "list_opportunities":
		records, err := s.crm.reads.Read(ctx, tool, accountID)
		return "crm", records, err
	case "read_call_notes":
		records, err := s.calls.reads.CallNotes(ctx, accountID)
		return "calls", records, err
	case "read_slack_messages":
		records, _, err := s.slack.reads.Messages(ctx, accountID)
		return "slack", records, err
	}
	return "", nil, fmt.Errorf("no source has a read named %q", tool)
}

// ReadThread is the Slack read of a live thread, with the snapshot the maker checks its messages against.
func (s cardSource) ReadThread(ctx context.Context, accountID string) ([]*strictjson.Object, *strictjson.Object, error) {
	return s.slack.reads.Messages(ctx, accountID)
}

// A read a command makes outside a card.
//
// The owner reads the demo book as it is: it is built into the program, and reading it is no tool call.
// Any other CRM is read as its tool, under a receipt committed before the read, and its records are
// released only once the receipt has bound them.

// toolCall is one source read made as a tool call of its own.
type toolCall struct {
	token     string   // receipts.Fingerprint of the bearer made for the run
	scopes    []string // what that bearer grants
	run       string   // the run the call belongs to, which audit --brief selects
	request   int      // the call's number in its run, from 1
	tool      string
	arguments *strictjson.Object
	account   string // the account the call names, or ""
}

// errDenied is a tool call whose grant lacks the tool's scope. Its receipt is finished as refused, and
// nothing was read.
var errDenied = errors.New("the grant does not hold the scope of this tool")

// call makes one tool call in a workspace. It commits the receipt, refuses a grant without the tool's
// scope, reads, and binds the records to the receipt before it returns them, with the finished receipt.
func (r reads) call(ctx context.Context, store *postgres.Store, workspaceID string, c toolCall,
	read func(context.Context) ([]*strictjson.Object, error)) (receipts.Receipt, []*strictjson.Object, error) {
	log := &postgres.ReceiptStore{Store: store}
	scope := mcp.Definitions[c.tool].Scope
	started, err := log.Start(ctx, receipts.Start{WorkspaceID: workspaceID,
		OperationID: fmt.Sprintf("%s/%d", c.run, c.request), TokenID: c.token, Tool: c.tool, Scope: scope,
		AccountRef: c.account, ConnectionID: r.connections[strings.TrimSuffix(scope, ":read")],
		Arguments: c.arguments, RequestID: receipts.RPC(c.request), BriefID: &c.run})
	if err != nil {
		return started, nil, err
	}
	began := time.Now()
	elapsed := func() float64 { return float64(time.Since(began).Microseconds()) / 1000 }
	// A call that fails is finished even when the caller has gone.
	fail := func(outcome receipts.Outcome, cause error) (receipts.Receipt, []*strictjson.Object, error) {
		outcome.ElapsedMS = elapsed()
		finished, err := log.Finish(context.WithoutCancel(ctx), started.Ticket(), outcome)
		return finished, nil, errors.Join(cause, err)
	}
	if !slices.Contains(c.scopes, scope) {
		return fail(receipts.Denied(), errDenied)
	}
	records, err := read(ctx)
	if err != nil {
		return fail(receipts.Failed(200, "request_failed"), err)
	}
	versions, err := cards.RecordVersions(records)
	if err != nil {
		return fail(receipts.Failed(200, "request_failed"), err)
	}
	finished, err := log.FinishRead(ctx, started.Ticket(), versions, elapsed())
	if err != nil {
		return finished, nil, err
	}
	return finished, records, nil
}

// referenced gives each account of a search its record_ref, as the search_accounts tool returns it.
func referenced(accounts []*strictjson.Object) []*strictjson.Object {
	records := make([]*strictjson.Object, len(accounts))
	for i, account := range accounts {
		records[i] = account.Copy()
		records[i].Set("record_ref", "crm/accounts/"+cellText(get(account, "id")))
	}
	return records
}

// ownerRead is one CRM read of the owner's command outside a card. The demo book is read as it is. Any
// other CRM is read as the tool, in a run of its own, under a bearer made for that run which grants
// crm:read and is never stored or shown.
func (r reads) ownerRead(ctx context.Context, c *run, tool string, arguments *strictjson.Object,
	read func(context.Context) ([]*strictjson.Object, error)) ([]*strictjson.Object, error) {
	if r.crm.backend == "sqlite" {
		return read(ctx)
	}
	account, _ := get(arguments, "account_id").(string)
	_, records, err := r.call(ctx, c.store, c.workspace.ID, toolCall{token: receipts.Fingerprint(randomToken()),
		scopes: []string{"crm:read"}, run: "inspect-" + strings.ReplaceAll(receipts.RandomID(), "-", ""), request: 1,
		tool: tool, arguments: arguments, account: account}, read)
	return records, err
}

// crmLine names the CRM alone, on stderr, for a command that reads nothing else. The demo book is not
// announced.
func (r reads) crmLine() string {
	if r.crm.backend == "sqlite" {
		return ""
	}
	return "Sources: CRM is " + r.crm.described + "."
}

// searchAccounts is the accounts command's search of the CRM.
func (r reads) searchAccounts(ctx context.Context, c *run, query string) (accounts []*strictjson.Object, more bool, err error) {
	records, err := r.ownerRead(ctx, c, "search_accounts", strictjson.NewObject("query", query),
		func(ctx context.Context) ([]*strictjson.Object, error) {
			found, cut, err := r.crm.reads.SearchAccounts(ctx, query)
			more = cut
			return referenced(found), err
		})
	for _, record := range records {
		record.Delete("record_ref")
	}
	return records, more, err
}

// account is one account as inspect shows it: the account as the CRM returns it, then its contacts, and
// its contracts when the CRM has contracts. It gives false for an account the CRM does not hold.
func (r reads) account(ctx context.Context, c *run, accountID string) (*strictjson.Object, bool, error) {
	records, err := r.ownerRead(ctx, c, "get_account", strictjson.NewObject("account_id", accountID),
		func(ctx context.Context) ([]*strictjson.Object, error) {
			return r.crm.reads.Read(ctx, "get_account", accountID)
		})
	if errors.Is(err, errAccountNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var account *strictjson.Object
	rows := map[string][]any{"contacts": {}, "contracts": {}}
	for _, record := range records {
		ref, _ := get(record, "record_ref").(string)
		parts := strings.SplitN(ref, "/", 3)
		row := record.Copy()
		row.Delete("record_ref")
		switch {
		case len(parts) == 3 && parts[1] == "accounts" && account == nil && parts[2] == accountID:
			account = row
		case len(parts) == 3 && rows[parts[1]] != nil:
			rows[parts[1]] = append(rows[parts[1]], row)
		default:
			return nil, false, errors.New("a get_account read returned a record that is not the account's")
		}
	}
	if account == nil {
		return nil, false, errors.New("a get_account read returned no account")
	}
	account.Set("contacts", rows["contacts"])
	if r.crm.backend != "sqlite" {
		account.Set("contracts", rows["contracts"])
	}
	return account, true, nil
}

// list records an account in the workspace under the name its CRM gives it, so that a receipt and a card
// can name it. An account the CRM does not hold is not listed.
func (r reads) list(ctx context.Context, c *run, accountID string) error {
	account, found, err := r.account(ctx, c, accountID)
	if err != nil || !found {
		return err
	}
	name, _ := get(account, "name").(string)
	return c.store.EnsureAccount(ctx, c.workspace.ID, accountID, name)
}

// The demo kinds: the invented book, built into the program, as each of the three sources.

// seededBook is the demo book, loaded once. No command changes it.
var seededBook = sync.OnceValues(demo.Seeded)

// demoBook is the demo book as a CRM, as call records and as Slack.
type demoBook struct{ book *demo.Store }

func (d demoBook) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	_, records, err := d.book.Read(ctx, tool, accountID)
	if errors.Is(err, demo.ErrAccountNotFound) {
		err = errAccountNotFound
	}
	return records, err
}

func (d demoBook) SearchAccounts(_ context.Context, query string) ([]*strictjson.Object, bool, error) {
	accounts, more := d.book.SearchAccounts(query)
	return accounts, more, nil
}

func (d demoBook) CallNotes(ctx context.Context, accountID string) ([]*strictjson.Object, error) {
	return d.Read(ctx, "read_call_notes", accountID)
}

func (d demoBook) Messages(ctx context.Context, accountID string) ([]*strictjson.Object, *strictjson.Object, error) {
	records, err := d.Read(ctx, "read_slack_messages", accountID)
	return records, nil, err
}

func attachDemo(context.Context, attaching) (attached, error) { return attached{}, nil }

func demoCRM(context.Context, opening) (connected[crmSource], error) {
	book, err := seededBook()
	return connected[crmSource]{reads: demoBook{book}, backend: "sqlite", binding: evidence.FixtureCRMBinding(),
		described: "demo data"}, err
}

// errDemoBesideReal refuses demo calls or Slack in a workspace whose CRM is not the demo book.
var errDemoBesideReal = &failure{code: 3, message: "This workspace names demo data beside a real CRM. " +
	"Demo data is read only beside the demo CRM, so nothing was read."}

func demoCalls(_ context.Context, o opening) (connected[callsSource], error) {
	if o.crm.backend != "sqlite" {
		return connected[callsSource]{}, errDemoBesideReal
	}
	book, err := seededBook()
	return connected[callsSource]{reads: demoBook{book}, backend: "fixture", described: "demo data"}, err
}

func demoSlack(_ context.Context, o opening) (connected[slackSource], error) {
	if o.crm.backend != "sqlite" {
		return connected[slackSource]{}, errDemoBesideReal
	}
	book, err := seededBook()
	return connected[slackSource]{reads: demoBook{book}, backend: "fixture", described: "demo data"}, err
}
