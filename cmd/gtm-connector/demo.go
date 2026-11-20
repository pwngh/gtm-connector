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
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// demoCommand makes the demo book's cards in the demo workspace, as the
// app role, after setup has made the demo principal.
func demoCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.databaseURL == "" {
		return errors.New("set GTM_DATABASE_URL to the app role's PostgreSQL URL")
	}
	store, err := openStore(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if version, err := store.SchemaVersion(ctx); err != nil {
		return err
	} else if version != postgres.LatestSchemaVersion() {
		return fmt.Errorf("the database is at schema %d, not %d; run setup first", version, postgres.LatestSchemaVersion())
	}
	made, err := makeDemoCards(ctx, store, demoLogin, os.Stdout)
	if err == nil {
		fmt.Printf("%d cards published for %s. Sign in as %s to read them.\n", made, demoLogin, demoLogin)
	}
	return err
}

// The demo book's sources, as the demo stores give them.
var demoSources = evidence.Sources{CRMBackend: "sqlite", CRMBinding: evidence.FixtureCRMBinding(),
	CallsBackend: "fixture", SlackBackend: "fixture"}

// makeDemoCards loads the demo book into the demo workspace: its accounts,
// a grant on each for the demo principal signing in as login, and a
// connection per source. Then it makes and publishes each account's card
// through the maker on PostgreSQL, as that principal, under a bearer made
// for this run that is never stored or shown. Each run publishes a new card
// per account. It returns how many it published.
func makeDemoCards(ctx context.Context, store *postgres.Store, login string, out io.Writer) (int, error) {
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		return 0, err
	}
	principal, _, err := store.DemoPrincipalByLogin(ctx, login)
	if errors.Is(err, postgres.ErrNotFound) || err == nil && principal.WorkspaceID != workspace.ID {
		return 0, fmt.Errorf("no demo principal %s in the demo workspace; run setup first", login)
	}
	if err != nil {
		return 0, err
	}
	book, err := demo.Seeded()
	if err != nil {
		return 0, err
	}
	connections := map[string]string{}
	for _, source := range []string{"crm", "calls", "slack"} {
		connection, err := store.EnsureConnection(ctx, workspace, source, "demo")
		if err != nil {
			return 0, err
		}
		connections[source] = connection.ID
	}
	accounts := book.Accounts()
	for _, account := range accounts {
		if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
			return 0, err
		}
		if err := store.GrantAccount(ctx, workspace.ID, principal.ID, account.ID); err != nil {
			return 0, err
		}
	}
	maker := &cards.Maker{Receipts: &postgres.ReceiptStore{Store: store}, Source: book, Sources: demoSources,
		Connections: connections}
	actor := cards.Principal{WorkspaceID: workspace.ID, PrincipalID: principal.ID,
		TokenID: receipts.Fingerprint(randomToken()), Scopes: []string{"crm:read", "calls:read", "slack:read"}}
	run := receipts.RandomID()
	for i, account := range accounts {
		sealed, err := maker.Brief(ctx, cards.Brief{Principal: actor, OperationID: "demo-" + run + "-" + account.ID,
			AccountID: account.ID, AsOf: cards.DemoAsOf, IncludeSlack: true})
		if err != nil {
			return i, fmt.Errorf("%s: %w", account.ID, err)
		}
		fmt.Fprintf(out, "%s  %s  %d units  sha256 %s\n", account.ID, sealed.BriefID, len(sealed.Units), sealed.SHA256)
	}
	return len(accounts), nil
}
