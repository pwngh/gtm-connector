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
// the reads its commands use. accounts, brief, inspect, audit, demo, connect, token and the MCP tools of
// serve and mcp stdio name no source of their own: they go through workspaceReads and sourceKinds.
//
// A workspace's connections are rows, one for each of "crm", "calls" and "slack" (postgres.Connection).
// A row holds a mode, which names the kind of source it is connected to; a binding, the JSON object of
// what the operator chose, which connect status shows; the digest of the identity the source is pinned
// to; and a generation, which moves on every change. No row holds a credential, an organization ID or an
// address.
//
// To wire one real source, a change does three things, all in this file's terms:
//
//  1. It implements the interface of its source: crmSource for Salesforce, callsSource for imported
//     transcripts, slackSource for a mapped Slack thread. A refusal a person can act on is a *failure,
//     which a command prints with its exit code and next command. Any other error prints as a fixed line
//     that names its kind (plain), so the text of an upstream answer never reaches the screen. In a
//     card's read, any error ends the read as failed and its receipt says so.
//  2. In its entry of sourceKinds it sets the opener of its source (crm, calls or slack): the function
//     that turns the connection's row into the source's reads. An opener reads the row and nothing else.
//     It makes no request and starts no process. A source that signs in does so on its first read, and
//     checks then that it reached the identity the row is pinned to. A source is opened only by a command
//     that reads it.
//  3. In the same entry it sets attach: what "connect NAME" checks before the row is saved, and the
//     binding and identity digest the row is saved with. The identity is the digest a card records in the
//     source's binding (bound), as salesforce.Identity.Binding gives it.
//
// What a card records of a source, its backend and its binding, and how a command names it are in the
// entry already, and come from the row alone. So a saved card is verified, and a card that is denied a
// source is made, without that source being opened.
//
// The rules this file keeps for every source, so that no source's own code has to:
//
//   - The generation. The reads are opened from rows at one generation each. Every receipt a command, a
//     card or an MCP call starts through them must have started under that generation; if the row has
//     moved since, by a reconnect or a removal, the receipt is finished as error/409 source_changed and
//     nothing is read. The store already refuses a read whose row moves after its receipt starts, and a
//     card whose reads are not all current when it is published.
//   - The pin. A row keeps the identity it was first connected to, also when it is detached: removal sets
//     the mode to "none" and clears the binding, and the digest stays. attach is handed that pin, and
//     connect refuses an identity that is not the pinned one, as the Python package refuses a folder
//     another org. The store refuses it for any writer. A workspace that holds one org's accounts, cards,
//     receipts and sellers is never handed to another.
//   - The CRM's dependents. Calls and Slack belong to their CRM. Removing the CRM detaches both, and its
//     pin stays. Connecting the same CRM again keeps them. A CRM with an identity is refused in a workspace
//     whose CRM row has no pin, because what that workspace holds was read through a CRM this one is not
//     known to be.
//   - The kind of workspace. A workspace is made for demo data or for real sources and keeps that, so the
//     two never share one. Only a row whose mode is "demo" opens the demo book, and only beside the demo
//     CRM. An entry with no opener is refused by name by a command that reads it, and an entry with no
//     attach is refused by connect. Neither is read as demo data.
//   - Receipts outside a card. accounts and inspect read a real CRM as its tool, under a receipt committed
//     before the read (ownerRead). A card without --as-of is planned from today for a real CRM (day), and
//     the workspace's seller list reaches every card (sources).
//
// The salesforce entry is wired this way (source_salesforce.go). TestSalesforceIsReadByEveryCommand runs
// every command through it against an invented org.

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

// opening is what an opener is given.
type opening struct {
	// store and workspace are for a source whose records are kept in PostgreSQL, as imported transcripts
	// and mapped threads are.
	store     *postgres.Store
	workspace postgres.Workspace
	// connection is the source's own row, and binding is the row's binding, read.
	connection postgres.Connection
	binding    *strictjson.Object
	// crmBackend and crmBinding are the workspace's CRM as a card records it. Calls and Slack belong to it.
	crmBackend string
	crmBinding any
}

// attaching is what connect was asked to attach.
type attaching struct {
	// args are connect's own options, such as --org.
	args parsed
	// crm is the workspace's CRM as it is connected now, or nil.
	crm *postgres.Connection
	// pinned is the identity the source's row is pinned to, kept when it was detached, or "". connect
	// refuses any other identity, so attach need not compare. It is here for a source that can say more
	// than a refusal, such as which sign-in to use.
	pinned string
}

// attached is what a source's row is saved with.
type attached struct {
	// mode is the row's mode when it is not the kind's own, as env_token is of the salesforce kind.
	mode string
	// binding is what the operator chose. connect status shows it. Nil is the empty object.
	binding *strictjson.Object
	// identity is the SHA-256 of the identity the source is pinned to, or "" for a source with none.
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
	// besideDemo is a real source that a demo workspace may also hold, beside the demo CRM: the one
	// exception to the kind of workspace.
	besideDemo bool
	// options are the options connect takes with it, besides --remove.
	options []string
	// backend is the source as a card records it: "sqlite" or "salesforce" for the CRM, "fixture" or
	// "imported" for calls, "fixture" or "slack" for Slack.
	backend string
	// bound is what a card records the source as bound to, from its row and the CRM's own binding. Nil
	// records none.
	bound func(row postgres.Connection, crm any) any
	// described is how a command names the source on its sources line, from the row's binding.
	described func(binding *strictjson.Object) string
	// attach checks what connect was asked for before anything is saved. Nil is a source connect does
	// not attach in this build.
	attach func(ctx context.Context, a attaching) (attached, error)
	// The opener of the kind's own source. A kind with none is not read in this build.
	crm   func(ctx context.Context, o opening) (crmSource, error)
	calls func(ctx context.Context, o opening) (callsSource, error)
	slack func(ctx context.Context, o opening) (slackSource, error)
}

// sourceNames are the sources of a workspace, the CRM first: calls and Slack belong to it.
var sourceNames = []string{"crm", "calls", "slack"}

// sourceKinds is every kind of source. "connect demo" attaches the three demo kinds together.
var sourceKinds = []*sourceKind{
	{provider: "demo", source: "crm", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		backend: "sqlite", bound: func(postgres.Connection, any) any { return evidence.FixtureCRMBinding() },
		described: demoData, attach: attachDemo, crm: demoCRM},
	{provider: "demo", source: "calls", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		backend: "fixture", described: demoData, attach: attachDemo, calls: demoCalls},
	{provider: "demo", source: "slack", mode: "demo", shown: "demo", title: "Demo data", detail: "demo data",
		backend: "fixture", described: demoData, attach: attachDemo, slack: demoSlack},
	// An org signed in with sf, read by internal/sources/salesforce (source_salesforce.go). The mode
	// env_token, a token handed to the process, is of this kind: attachSalesforce saves it for
	// --token-env, and openSalesforce reads both.
	{provider: "salesforce", source: "crm", mode: "sf", shown: "salesforce", title: "Salesforce",
		detail: "Salesforce", live: true, options: []string{"--org", "--token-env", "--environment", "--seller"},
		backend: "salesforce", bound: func(row postgres.Connection, _ any) any {
			return pinnedBinding("crm_backend", "salesforce", row)
		},
		described: func(binding *strictjson.Object) string {
			return "Salesforce (" + oneLine(cellText(get(binding, "environment"))) + "), identity verified"
		},
		attach: attachSalesforce, crm: openSalesforce},
	// Imports are bound to their CRM, and may sit beside the demo CRM (source_calls.go).
	{provider: "calls", source: "calls", mode: "imported", shown: "imported", title: "Call transcripts", plural: true,
		detail: "imported transcripts", live: true, besideDemo: true, options: []string{"--seller"},
		backend: "imported", bound: func(_ postgres.Connection, crm any) any { return crm },
		described: func(*strictjson.Object) string {
			return "imported transcripts, whose provider and statements are unverified"
		}, attach: attachTranscripts, calls: openTranscripts},
	// source_slack.go opens it: the mapped thread of an account, read through internal/sources/slack.
	{provider: "slack", source: "slack", mode: "slack", shown: "slack", title: "Slack",
		detail: "the saved Slack sign-in", live: true, options: []string{"--seller"},
		backend: "slack", bound: func(row postgres.Connection, _ any) any {
			return pinnedBinding("provider", "slack", row)
		},
		described: func(*strictjson.Object) string {
			return "the mapped thread, whose access is checked when it is read"
		}, attach: attachSlack, slack: openSlack},
}

// sameKind are the two modes the schema allows beside the kinds' own, each a row of the kind it names.
var sameKind = map[string]string{"fixture": "demo", "env_token": "sf"}

// pinnedBinding is the binding a card records for a source that is pinned to an identity: what it is, and
// the digest its row holds.
func pinnedBinding(key, named string, row postgres.Connection) any {
	return strictjson.NewObject("schema_version", 1, key, named, "identity_sha256", row.IdentitySHA256)
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

// kindOf is the kind of one connection row, or nil for a mode no kind has.
func kindOf(source, mode string) *sourceKind {
	if same, other := sameKind[mode]; other {
		mode = same
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

// reads is what a workspace's commands read: the rows of its connected sources as they were when it was
// asked, the sellers its operator listed, and each source's reads, opened when a command first reads it.
type reads struct {
	store     *postgres.Store
	workspace postgres.Workspace
	rows      map[string]postgres.Connection // each connected source's row
	kinds     map[string]*sourceKind
	bindings  map[string]*strictjson.Object // each row's binding, read
	sellers   []string
	// The reads opened so far.
	crmReads   crmSource
	callsReads callsSource
	slackReads slackSource
}

// workspaceReads reads what a workspace is connected to, and opens nothing. A workspace with no CRM gives
// errNotConnected. A row of a mode no kind has is refused.
func workspaceReads(ctx context.Context, store *postgres.Store, workspace postgres.Workspace) (*reads, error) {
	r := &reads{store: store, workspace: workspace, rows: map[string]postgres.Connection{},
		kinds: map[string]*sourceKind{}, bindings: map[string]*strictjson.Object{}}
	rows, err := store.Connections(ctx, workspace.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Mode == "none" {
			continue
		}
		if r.kinds[row.Source] = kindOf(row.Source, row.Mode); r.kinds[row.Source] == nil {
			return nil, &unportedSource{"a source this build does not know"}
		}
		if r.bindings[row.Source], err = bindingOf(row); err != nil {
			return nil, err
		}
		r.rows[row.Source] = row
	}
	if _, connected := r.rows["crm"]; !connected {
		return nil, errNotConnected
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

// backend is a source as a card records it, or "none" for one that is not connected.
func (r *reads) backend(source string) string {
	if kind := r.kinds[source]; kind != nil {
		return kind.backend
	}
	return "none"
}

// bound is what a card records a source as bound to, or nil.
func (r *reads) bound(source string) any {
	kind := r.kinds[source]
	if kind == nil || kind.bound == nil {
		return nil
	}
	var crm any
	if source != "crm" {
		crm = r.bound("crm")
	}
	return kind.bound(r.rows[source], crm)
}

// sources is what the workspace is connected to, as a card records it.
func (r *reads) sources() evidence.Sources {
	return evidence.Sources{CRMBackend: r.backend("crm"), CRMBinding: r.bound("crm"),
		CallsBackend: r.backend("calls"), CallsBinding: r.bound("calls"),
		SlackBackend: r.backend("slack"), SlackBinding: r.bound("slack"), Sellers: r.sellers}
}

// connections is each connected source's connection ID, and generations the generation of its row when
// the workspace's reads were asked for. A receipt that starts under another generation reads nothing.
func (r *reads) connections() map[string]string {
	ids := map[string]string{}
	for source, row := range r.rows {
		ids[source] = row.ID
	}
	return ids
}

func (r *reads) generations() map[string]int64 {
	generations := map[string]int64{}
	for source, row := range r.rows {
		generations[source] = row.Generation
	}
	return generations
}

// opening is what the opener of one connected source is given.
func (r *reads) opening(source string) opening {
	return opening{store: r.store, workspace: r.workspace, connection: r.rows[source], binding: r.bindings[source],
		crmBackend: r.backend("crm"), crmBinding: r.bound("crm")}
}

// crm, calls and slack give a source's reads, opening it on its first use. A source this build has no
// opener for is refused by name.
func (r *reads) crm(ctx context.Context) (crmSource, error) {
	if r.crmReads == nil {
		kind := r.kinds["crm"]
		if kind.crm == nil {
			return nil, &unportedSource{kind.detail}
		}
		opened, err := kind.crm(ctx, r.opening("crm"))
		if err != nil {
			return nil, err
		}
		r.crmReads = opened
	}
	return r.crmReads, nil
}

func (r *reads) calls(ctx context.Context) (callsSource, error) {
	if r.callsReads == nil {
		kind := r.kinds["calls"]
		switch {
		case kind == nil:
			return nil, errors.New("calls are not connected")
		case kind.calls == nil:
			return nil, &unportedSource{kind.detail}
		}
		opened, err := kind.calls(ctx, r.opening("calls"))
		if err != nil {
			return nil, err
		}
		r.callsReads = opened
	}
	return r.callsReads, nil
}

func (r *reads) slack(ctx context.Context) (slackSource, error) {
	if r.slackReads == nil {
		kind := r.kinds["slack"]
		switch {
		case kind == nil:
			return nil, errors.New("Slack is not connected")
		case kind.slack == nil:
			return nil, &unportedSource{kind.detail}
		}
		opened, err := kind.slack(ctx, r.opening("slack"))
		if err != nil {
			return nil, err
		}
		r.slackReads = opened
	}
	return r.slackReads, nil
}

// open opens the named sources that are connected, so that a command which will read them is refused
// before it writes anything when one cannot be read.
func (r *reads) open(ctx context.Context, sources ...string) error {
	for _, source := range sources {
		var err error
		switch {
		case r.kinds[source] == nil:
		case source == "crm":
			_, err = r.crm(ctx)
		case source == "calls":
			_, err = r.calls(ctx)
		case source == "slack":
			_, err = r.slack(ctx)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// maker makes cards from these sources on PostgreSQL. Each read opens its source when it is made.
func (r *reads) maker() *cards.Maker {
	return &cards.Maker{Receipts: &postgres.ReceiptStore{Store: r.store}, Source: cardSource{r}, Sources: r.sources(),
		Connections: r.connections(), Generations: r.generations()}
}

// day is the day a card is planned from when the command names none: the demo book's own day, and today
// in UTC for a CRM that is read as it is now.
func (r *reads) day() string {
	if r.backend("crm") == "sqlite" {
		return cards.DemoAsOf
	}
	return time.Now().UTC().Format("2006-01-02")
}

// described is how a command names one source: by its kind, or as not connected.
func (r *reads) described(source string) string {
	if kind := r.kinds[source]; kind != nil {
		return kind.described(r.bindings[source])
	}
	return "not connected"
}

// sourcesLine names what a card reads, once, on stderr.
func (r *reads) sourcesLine() string {
	crm, calls, slack := r.described("crm"), r.described("calls"), r.described("slack")
	if crm == calls && calls == slack {
		return "Sources: CRM, calls, and Slack are " + crm + "."
	}
	return "Sources: CRM is " + crm + "; calls are " + calls + "; Slack is " + slack + "."
}

// crmLine names the CRM alone, on stderr, for a command that reads nothing else. The demo book is not
// announced.
func (r *reads) crmLine() string {
	if r.backend("crm") == "sqlite" {
		return ""
	}
	return "Sources: CRM is " + r.described("crm") + "."
}

// cardSource is a workspace's three sources as the card maker reads them: one read for each tool, from
// the source the tool belongs to, opened when it is first read.
type cardSource struct{ *reads }

func (s cardSource) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	switch tool {
	case "get_account", "list_opportunities":
		crm, err := s.crm(ctx)
		if err != nil {
			return "crm", nil, err
		}
		records, err := crm.Read(ctx, tool, accountID)
		return "crm", records, err
	case "read_call_notes":
		calls, err := s.calls(ctx)
		if err != nil {
			return "calls", nil, err
		}
		records, err := calls.CallNotes(ctx, accountID)
		return "calls", records, err
	case "read_slack_messages":
		records, _, err := s.ReadThread(ctx, accountID)
		return "slack", records, err
	}
	return "", nil, fmt.Errorf("no source has a read named %q", tool)
}

// ReadThread is the Slack read, with the snapshot the maker checks a live thread's messages against.
func (s cardSource) ReadThread(ctx context.Context, accountID string) ([]*strictjson.Object, *strictjson.Object, error) {
	slack, err := s.slack(ctx)
	if err != nil {
		return nil, nil, err
	}
	return slack.Messages(ctx, accountID)
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
	// principal is the principal the call is made for, or "" for the owner.
	principal string
	// scope is the scope of a read that is no registered tool, such as a check's. A registered tool takes none.
	scope string
}

// errDenied is a tool call whose grant lacks the tool's scope. Its receipt is finished as refused, and
// nothing was read.
var errDenied = errors.New("the grant does not hold the scope of this tool")

// settling is how long a receipt may take to finish after its caller has gone.
const settling = 5 * time.Second

// call makes one tool call in the workspace. It commits the receipt, refuses a receipt that started under
// another generation than the source was opened under, refuses a grant without the tool's scope, reads,
// and binds the records to the receipt before it returns them, with the finished receipt. read decides the
// read's outcome: what it returns is bound, and its error fails the receipt. A receipt this call started
// is never left pending: when the caller has gone it is finished as an error on a context of its own.
func (r *reads) call(ctx context.Context, c toolCall,
	read func(context.Context) ([]*strictjson.Object, error)) (receipts.Receipt, []*strictjson.Object, error) {
	log := &postgres.ReceiptStore{Store: r.store}
	// A registered tool's definition decides its scope, and so its source and connection. Only a read that
	// is no registered tool, such as one of a check, names its own.
	definition, registered := mcp.Definitions[c.tool]
	scope := definition.Scope
	switch {
	case registered && c.scope != "":
		return receipts.Receipt{}, nil, fmt.Errorf("%s is a registered tool and takes its own scope", c.tool)
	case !registered && c.scope == "":
		return receipts.Receipt{}, nil, fmt.Errorf("%s is no registered tool and names no scope", c.tool)
	case !registered:
		scope = c.scope
	}
	row := r.rows[strings.TrimSuffix(scope, ":read")]
	started, err := log.Start(ctx, receipts.Start{WorkspaceID: r.workspace.ID,
		OperationID: fmt.Sprintf("%s/%d", c.run, c.request), PrincipalID: c.principal, TokenID: c.token,
		Tool: c.tool, Scope: scope, AccountRef: c.account, ConnectionID: row.ID, Arguments: c.arguments,
		RequestID: receipts.RPC(c.request), BriefID: &c.run})
	if err != nil {
		return started, nil, err
	}
	began := time.Now()
	elapsed := func() float64 { return float64(time.Since(began).Microseconds()) / 1000 }
	fail := func(outcome receipts.Outcome, cause error) (receipts.Receipt, []*strictjson.Object, error) {
		outcome.ElapsedMS = elapsed()
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settling)
		defer cancel()
		finished, err := log.Finish(settle, started.Ticket(), outcome)
		return finished, nil, errors.Join(cause, err)
	}
	if started.ConnectionID != "" && started.Generation != row.Generation {
		return fail(receipts.Failed(409, "source_changed"), fmt.Errorf("%s: %w", c.tool, receipts.ErrSourceChanged))
	}
	if !slices.Contains(c.scopes, scope) {
		return fail(receipts.Denied(), errDenied)
	}
	records, err := read(ctx)
	if err == nil {
		// A caller who has gone is given nothing, whatever the source answered.
		err = ctx.Err()
	}
	if err != nil {
		return fail(receipts.Failed(200, "request_failed"), err)
	}
	versions, err := cards.RecordVersions(records)
	if err != nil {
		return fail(receipts.Failed(200, "request_failed"), err)
	}
	if started.ConnectionID == "" {
		// A call of no connected source, as a check of an org before it is connected, binds no record.
		if len(versions) > 0 {
			return fail(receipts.Failed(200, "request_failed"), errors.New("a call of no connected source returned records"))
		}
		finished, err := log.Finish(ctx, started.Ticket(), receipts.Outcome{Status: "ok", HTTPStatus: 200,
			Rows: []string{}, ElapsedMS: elapsed()})
		return finished, records, err
	}
	finished, err := log.FinishRead(ctx, started.Ticket(), versions, elapsed())
	switch {
	case errors.Is(err, receipts.ErrSourceChanged):
		// The store finished the receipt itself, as fenced.
		return finished, nil, err
	case err != nil:
		return fail(receipts.Failed(200, "request_failed"), err)
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
func (r *reads) ownerRead(ctx context.Context, tool string, arguments *strictjson.Object,
	read func(context.Context) ([]*strictjson.Object, error)) ([]*strictjson.Object, error) {
	if r.backend("crm") == "sqlite" {
		return read(ctx)
	}
	account, _ := get(arguments, "account_id").(string)
	_, records, err := r.call(ctx, toolCall{token: receipts.Fingerprint(randomToken()),
		scopes: []string{"crm:read"}, run: "inspect-" + strings.ReplaceAll(receipts.RandomID(), "-", ""), request: 1,
		tool: tool, arguments: arguments, account: account}, read)
	return records, err
}

// searchAccounts is the accounts command's search of the CRM.
func (r *reads) searchAccounts(ctx context.Context, query string) (accounts []*strictjson.Object, more bool, err error) {
	crm, err := r.crm(ctx)
	if err != nil {
		return nil, false, err
	}
	records, err := r.ownerRead(ctx, "search_accounts", strictjson.NewObject("query", query),
		func(ctx context.Context) ([]*strictjson.Object, error) {
			found, cut, err := crm.SearchAccounts(ctx, query)
			more = cut
			return referenced(found), err
		})
	for _, record := range records {
		record.Delete("record_ref")
	}
	return records, more, err
}

// errNoAccountData is a get_account read whose answer is not one account with what belongs to it.
var errNoAccountData = &failure{code: 5, message: "get_account: invalid or failed tool result; no account data returned"}

// accountOf is the records of a get_account read as inspect shows them: the account as the CRM returns
// it, then its contacts, and its contracts when the CRM has contracts. Records that are not exactly one
// account and what belongs to it are refused.
func accountOf(records []*strictjson.Object, accountID string, contracts bool) (*strictjson.Object, error) {
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
			return nil, errNoAccountData
		}
	}
	if account == nil {
		return nil, errNoAccountData
	}
	account.Set("contacts", rows["contacts"])
	if contracts {
		account.Set("contracts", rows["contracts"])
	}
	return account, nil
}

// account is one account as inspect shows it. It gives false for an account the CRM does not hold. The
// read's outcome is decided before its receipt finishes: an answer that is not the account fails the read.
func (r *reads) account(ctx context.Context, accountID string) (*strictjson.Object, bool, error) {
	crm, err := r.crm(ctx)
	if err != nil {
		return nil, false, err
	}
	var account *strictjson.Object
	_, err = r.ownerRead(ctx, "get_account", strictjson.NewObject("account_id", accountID),
		func(ctx context.Context) ([]*strictjson.Object, error) {
			records, err := crm.Read(ctx, "get_account", accountID)
			if err == nil {
				account, err = accountOf(records, accountID, r.backend("crm") != "sqlite")
			}
			return records, err
		})
	if errors.Is(err, errAccountNotFound) {
		return nil, false, nil
	}
	return account, err == nil, err
}

// list records an account in the workspace under the name its CRM gives it, so that a receipt and a card
// can name it. An account the CRM does not hold is not listed.
func (r *reads) list(ctx context.Context, accountID string) error {
	account, found, err := r.account(ctx, accountID)
	if err != nil || !found {
		return err
	}
	name, _ := get(account, "name").(string)
	return r.store.EnsureAccount(ctx, r.workspace.ID, accountID, name)
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

func demoData(*strictjson.Object) string { return "demo data" }

func attachDemo(context.Context, attaching) (attached, error) { return attached{}, nil }

// opened is the demo book as one of its sources. Demo calls and Slack are read only beside the demo CRM.
func openedDemo(o opening) (demoBook, error) {
	if o.crmBackend != "sqlite" {
		return demoBook{}, &failure{code: 3, message: "This workspace names demo data beside a real CRM. " +
			"Demo data is read only beside the demo CRM, so nothing was read."}
	}
	book, err := seededBook()
	return demoBook{book}, err
}

func demoCRM(_ context.Context, o opening) (crmSource, error)     { return openedDemo(o) }
func demoCalls(_ context.Context, o opening) (callsSource, error) { return openedDemo(o) }
func demoSlack(_ context.Context, o opening) (slackSource, error) { return openedDemo(o) }
