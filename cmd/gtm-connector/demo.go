package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// demo is two commands under one name, told apart by --state-dir.
//
// Without it, demo fills the install's demo workspace for the web app: a card for each account of the demo
// book, for the demo sign-in. `make demo` and scripts/smoke.sh run it this way.
//
// With it, demo is the Python CLI's demo in the workspace named: one invented renewal, looked up, with one
// read refused for a grant that lacks its scope, and its card.
func (r *run) demo(ctx context.Context) (any, error) {
	_, again := r.args.text("--again")
	if _, named := r.args.text("--state-dir"); named {
		if again {
			return nil, refused("--again publishes the demo book again. It is given without --state-dir.")
		}
		return r.renewal(ctx)
	}
	if r.screen.json {
		return nil, refused("demo prints the cards it publishes as text. With --state-dir NAME it opens one " +
			"renewal in that workspace and takes --json.")
	}
	c, err := loadConfig()
	if err != nil {
		return nil, local("%s", err)
	}
	if r.store, err = openAppStore(ctx, c); err != nil {
		return nil, err
	}
	_, err = makeDemoCards(ctx, r.store, demoLogin, again, r.screen.out)
	return nil, err
}

// demoSources is the demo book's three sources as a card records them.
var demoSources = evidence.Sources{CRMBackend: "sqlite", CRMBinding: evidence.FixtureCRMBinding(),
	CallsBackend: "fixture", SlackBackend: "fixture"}

// demoRun is what one run of the demo did.
type demoRun struct {
	Published, Held int      // cards published; accounts that hold a card already
	Revoked         []string // accounts whose grant was revoked, left alone
}

// makeDemoCards loads the demo book into the demo workspace: its accounts, a
// grant on each for the demo principal signing in as login, and a
// connection per source. A grant that exists is left as it is, so a revoked
// one stays revoked and its account gets no card. Then it makes and
// publishes the card of each account that holds none, or of every granted
// account with again, through the maker on PostgreSQL, as that principal,
// under a bearer made for this run that is never stored or shown. A run that
// stopped part way is finished by running again. Cards and receipts are
// never deleted, so each card again adds rows only a new database removes.
func makeDemoCards(ctx context.Context, store *postgres.Store, login string, again bool, out io.Writer) (demoRun, error) {
	var run demoRun
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		return run, err
	}
	principal, err := store.DemoPrincipal(ctx, login)
	switch {
	case errors.Is(err, postgres.ErrDisabled):
		return run, local("the demo principal %s is disabled; enable it, or start a new database", login)
	case errors.Is(err, postgres.ErrNotFound) || err == nil && principal.WorkspaceID != workspace.ID:
		return run, local("no demo principal %s in the demo workspace; run setup first", login)
	case err != nil:
		return run, err
	}
	for _, source := range sourceNames {
		if _, err := store.EnsureConnection(ctx, workspace, source, "demo"); err != nil {
			return run, err
		}
	}
	from, err := workspaceReads(ctx, store, workspace)
	if err != nil {
		return run, err
	}
	book, err := seededBook()
	if err != nil {
		return run, err
	}
	held, err := store.AccountsWithCards(ctx, workspace.ID, principal.ID, cards.DemoAsOf)
	if err != nil {
		return run, err
	}
	var todo []string
	for _, account := range book.Accounts() {
		if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
			return run, err
		}
		live, err := store.EnsureAccountGrant(ctx, workspace.ID, principal.ID, account.ID)
		switch {
		case err != nil:
			return run, err
		case !live:
			run.Revoked = append(run.Revoked, account.ID)
		case held[account.ID] && !again:
			run.Held++
		default:
			todo = append(todo, account.ID)
		}
	}
	maker := from.maker(store)
	actor := cards.Principal{WorkspaceID: workspace.ID, PrincipalID: principal.ID,
		TokenID: receipts.Fingerprint(randomToken()), Scopes: []string{"crm:read", "calls:read", "slack:read"}}
	operation := receipts.RandomID()
	for _, account := range todo {
		sealed, err := maker.Brief(ctx, cards.Brief{Principal: actor, OperationID: "demo-" + operation + "-" + account,
			AccountID: account, AsOf: cards.DemoAsOf, IncludeSlack: true})
		if err != nil {
			return run, fmt.Errorf("%s: %w", account, err)
		}
		run.Published++
		fmt.Fprintf(out, "%s  %s  %d units  sha256 %s\n", account, sealed.BriefID, len(sealed.Units), sealed.SHA256)
	}
	fmt.Fprintf(out, "%d cards published for %s; %d accounts hold one already", run.Published, login, run.Held)
	if run.Held > 0 {
		fmt.Fprint(out, " (demo --again publishes another; cards are never deleted)")
	}
	fmt.Fprintln(out, ".")
	if len(run.Revoked) > 0 {
		fmt.Fprintf(out, "Left alone, their grant revoked: %v.\n", run.Revoked)
	}
	return run, nil
}

// The renewal the demo opens (demo.ACCOUNT_ID and ACCOUNT_NAME of the Python package).
const (
	renewalAccount = "acc-003"
	renewalName    = "Thornfield Wren LLP"
)

// unfit is a workspace the renewal demo cannot run in, or a step of it that did not go as the story says:
// exit 4.
func unfit(format string, values ...any) *failure {
	return &failure{code: 4, message: "demo invariant failed: " + fmt.Sprintf(format, values...)}
}

// renewal is demo.run of the Python package, in the workspace --state-dir names: it looks the account up,
// has a read of its call notes refused for a grant without calls:read, and makes its card. Each step is a
// tool call under its receipt. A workspace that does not exist is made and connected to the demo data. One
// that is connected to anything else is refused. Nothing is written to a source: no command of this
// program writes to one.
func (r *run) renewal(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	elsewhere := &failure{code: 2, next: "gtm-connector connect --help",
		message: "This workspace is connected to other sources. Give the demo its own --state-dir."}
	if r.exists && r.workspace.Kind != "demo" {
		return nil, elsewhere
	}
	from, err := r.reads(ctx)
	if errors.Is(err, errNotConnected) {
		// A workspace that reads nothing yet is connected to the demo data, as connect demo connects it.
		if _, _, err = r.attach(ctx, kindsNamed("demo"), nil); err == nil {
			from, err = r.reads(ctx)
		}
	}
	if err != nil {
		return nil, err
	}
	if from.crm.backend != "sqlite" || from.calls.backend != "fixture" || from.slack.backend != "fixture" {
		return nil, elsewhere
	}
	run := "demo-" + strings.ReplaceAll(receipts.RandomID(), "-", "")
	grant := toolCall{token: receipts.Fingerprint(randomToken()), scopes: []string{"crm:read"}, run: run}

	lookup := grant
	lookup.request, lookup.tool, lookup.arguments = 1, "search_accounts", strictjson.NewObject("query", renewalName)
	searched, found, err := from.call(ctx, r.store, r.workspace.ID, lookup,
		func(ctx context.Context) ([]*strictjson.Object, error) {
			accounts, _, err := from.crm.reads.SearchAccounts(ctx, renewalName)
			return referenced(accounts), err
		})
	if err != nil {
		return nil, err
	}
	if len(found) != 1 || get(found[0], "id") != renewalAccount {
		return nil, unfit("the lookup must return %s (%s) once", renewalName, renewalAccount)
	}
	account := found[0]
	if err := r.store.EnsureAccount(ctx, r.workspace.ID, renewalAccount, renewalName); err != nil {
		return nil, err
	}

	notes := grant
	notes.request, notes.tool, notes.account = 2, "read_call_notes", renewalAccount
	notes.arguments = strictjson.NewObject("account_id", renewalAccount)
	denied, _, err := from.call(ctx, r.store, r.workspace.ID, notes,
		func(ctx context.Context) ([]*strictjson.Object, error) {
			return from.calls.reads.CallNotes(ctx, renewalAccount)
		})
	if !errors.Is(err, errDenied) || denied.HTTPStatus == nil || *denied.HTTPStatus != 403 {
		return nil, errors.Join(unfit("the call-note scope refusal was not enforced"), err)
	}

	sealed, err := from.maker(r.store).Brief(ctx, cards.Brief{
		Principal: cards.Principal{WorkspaceID: r.workspace.ID, TokenID: receipts.Fingerprint(randomToken()),
			Scopes: []string{"crm:read", "calls:read", "slack:read"}},
		OperationID: run + "/card", AccountID: renewalAccount, AsOf: cards.DemoAsOf, IncludeSlack: true})
	if err != nil {
		return nil, err
	}
	value, err := strictjson.Loads(sealed.Payload)
	if err != nil {
		return nil, err
	}
	card := value.(*strictjson.Object)
	reads, _ := get(get(card, "evidence").(*strictjson.Object), "receipts").([]any)
	if len(reads) != 4 {
		return nil, unfit("the card did not finish its four planned reads")
	}
	for _, read := range reads {
		if get(read.(*strictjson.Object), "status") != "ok" {
			return nil, unfit("the card did not finish its four planned reads")
		}
	}
	brief := &strictjson.Object{}
	for _, key := range []string{"brief_id", "account_id", "as_of", "text", "units", "gaps"} {
		brief.Set(key, get(card, key))
	}
	shown := &strictjson.Object{}
	for _, key := range []string{"id", "name", "record_ref", "renewal_date"} {
		shown.Set(key, get(account, key))
	}
	return strictjson.NewObject("schema_version", 2, "status", "accepted", "workflow", "renewal-demo",
		"data_mode", "demo", "sources", get(card, "sources"), "account", shown,
		"lookup", strictjson.NewObject("query", renewalName, "matches", len(found), "receipt_id", searched.AuditID),
		"denied_read", strictjson.NewObject("tool", "read_call_notes", "status", *denied.HTTPStatus,
			"required_scope", "calls:read", "receipt_id", denied.AuditID),
		"brief", brief,
		// The two calls of this run, the card's reads, and the receipt that seals the card.
		"audit", strictjson.NewObject("run_id", run, "rows", 2+len(reads)+1),
		"activities_created", 0), nil
}
