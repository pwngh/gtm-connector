package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// demoCommand makes the demo book's cards in the demo workspace, as the app
// role, after setup has made the demo principal.
func demoCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	again := flags.Bool("again", false, "publish a new card for every account, also those that hold one")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openAppStore(ctx, c)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = makeDemoCards(ctx, store, demoLogin, *again, os.Stdout)
	return err
}

// The demo book's sources, as the demo stores give them.
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
		return run, fmt.Errorf("the demo principal %s is disabled; enable it, or start a new database", login)
	case errors.Is(err, postgres.ErrNotFound) || err == nil && principal.WorkspaceID != workspace.ID:
		return run, fmt.Errorf("no demo principal %s in the demo workspace; run setup first", login)
	case err != nil:
		return run, err
	}
	for _, source := range []string{"crm", "calls", "slack"} {
		if _, err := store.EnsureConnection(ctx, workspace, source, "demo"); err != nil {
			return run, err
		}
	}
	from, err := workspaceReads(ctx, store, workspace)
	if err != nil {
		return run, err
	}
	book := from.book
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
