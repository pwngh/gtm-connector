package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// A publication's transaction gives up by itself, and what it has committed is the caller's.

// session opens another connection to the database, closed when the test ends.
func session(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	ok(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// A publication that waits on a lock another session holds gives up at its lock timeout, and at its
// statement timeout when that comes first. It writes no card and leaves its seal pending, so the same
// publication goes through once the lock is gone. The reader's grant is the lock: a publication checks it
// under a share lock.
func TestAPublicationGivesUpOnALock(t *testing.T) {
	for name, limits := range map[string][2]time.Duration{
		"at the lock timeout":      {200 * time.Millisecond, time.Minute},
		"at the statement timeout": {time.Minute, 200 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			f := postgrestest.New(t)
			postgres.SetPublishTimeouts(f.Store, limits[0], limits[1])
			account := postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account, "name", "Invented account")
			publication, call, _ := postgrestest.Prepared(t, f.Store, f, "waiting-brief", "brief-waiting", f.Reader.ID,
				postgres.TokenID(""), account)

			holder := session(t, f.URL)
			_, err := holder.Exec(ctx, "BEGIN")
			ok(t, err)
			_, err = holder.Exec(ctx, "SELECT 1 FROM account_grants WHERE principal_id = $1 FOR UPDATE", f.Reader.ID)
			ok(t, err)

			// The caller's own context lasts far longer than either timeout.
			bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			began := time.Now()
			_, err = f.Store.PublishCard(bounded, publication)
			if !errors.Is(err, postgres.ErrPublishTimeout) || time.Since(began) > 10*time.Second {
				t.Fatalf("the publication behind a lock: %v after %s", err, time.Since(began))
			}
			if n := count(t, f, "SELECT count(*) FROM cards WHERE brief_id = 'brief-waiting'"); n != 0 {
				t.Fatalf("%d cards written", n)
			}
			seal, err := f.Store.Receipt(ctx, f.Workspace.ID, call.ID)
			if err != nil || seal.Status != "pending" {
				t.Fatalf("the seal: %+v (%v)", seal, err)
			}

			_, err = holder.Exec(ctx, "ROLLBACK")
			ok(t, err)
			card, err := f.Store.PublishCard(ctx, publication)
			if err != nil || card.BriefID != "brief-waiting" {
				t.Fatalf("the publication once the lock is gone: %v", err)
			}
		})
	}
}

// reading is a demo workspace whose reader holds a grant on acc-003, with the card maker reading the demo
// book through the store.
type reading struct {
	url   string
	store *postgres.Store
	maker *cards.Maker
	brief cards.Brief
}

func newReading(t *testing.T) *reading {
	t.Helper()
	r := &reading{url: pgtest.URL(t)}
	var err error
	r.store, err = postgres.Open(ctx, r.url)
	ok(t, err)
	t.Cleanup(r.store.Close)
	ok(t, r.store.Migrate(ctx))
	workspace, err := r.store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	ok(t, err)
	connections := map[string]string{}
	for _, source := range []string{"crm", "calls", "slack"} {
		connection, err := r.store.EnsureConnection(ctx, workspace, source, "demo")
		ok(t, err)
		connections[source] = connection.ID
	}
	ok(t, r.store.EnsureAccount(ctx, workspace.ID, postgrestest.Account, "Invented account"))
	reader, err := r.store.CreatePrincipal(ctx, workspace, "person", "Demo reader")
	ok(t, err)
	ok(t, r.store.GrantAccount(ctx, workspace.ID, reader.ID, postgrestest.Account))
	book, err := demo.Load(oracletest.Read(t, "cards/demo_book.json"))
	ok(t, err)
	r.maker = &cards.Maker{Receipts: &postgres.ReceiptStore{Store: r.store}, Source: book,
		Sources: evidence.Sources{CRMBackend: "sqlite", CRMBinding: evidence.FixtureCRMBinding(), CallsBackend: "fixture",
			SlackBackend: "fixture"}, Connections: connections}
	r.brief = cards.Brief{Principal: cards.Principal{WorkspaceID: workspace.ID, PrincipalID: reader.ID,
		TokenID: receipts.Fingerprint("a bearer"), Scopes: []string{"crm:read", "calls:read", "slack:read"}},
		OperationID: "brief", AccountID: postgrestest.Account, AsOf: cards.DemoAsOf, IncludeSlack: true}
	return r
}

// The card maker is told of the database's own timeout in its fixed error, well inside its own limit: the
// database gave up first. The seal finishes as an error and no card is stored.
func TestTheMakerIsToldWhenTheDatabaseGivesUpOnAPublication(t *testing.T) {
	r := newReading(t)
	postgres.SetPublishTimeouts(r.store, 200*time.Millisecond, time.Minute)
	holder := session(t, r.url)
	_, err := holder.Exec(ctx, "BEGIN")
	ok(t, err)
	_, err = holder.Exec(ctx, "SELECT 1 FROM account_grants WHERE principal_id = $1 FOR UPDATE", r.brief.Principal.PrincipalID)
	ok(t, err)

	began := time.Now()
	sealed, err := r.maker.Brief(ctx, r.brief)
	if sealed != nil || err != cards.ErrNotPublished || time.Since(began) > cards.SettleLimit/2 {
		t.Fatalf("a card: %t; %v after %s", sealed != nil, err, time.Since(began))
	}
	seal, err := r.store.ReceiptByOperation(ctx, r.brief.Principal.WorkspaceID, "brief/seal")
	if err != nil || seal.Status != "error" || *seal.Error != "brief_save_failed" {
		t.Fatalf("the seal: %+v (%v)", seal, err)
	}
	if n := count(t, &postgrestest.Fixture{URL: r.url}, "SELECT count(*) FROM cards"); n != 0 {
		t.Fatalf("%d cards stored", n)
	}
}

// answered is a store that keeps what its first publication answered, under a context of two seconds.
type answered struct {
	receipts.Store
	calls int
	err   error
}

func (a *answered) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	card, err := a.Store.Publish(bounded, p)
	if a.calls++; a.calls == 1 {
		a.err = err
	}
	return card, err
}

// Once the commit has succeeded the publication answers with the card. Another session asks for the gaps
// table for itself while the publication's transaction still holds it, so it gets the table the moment the
// transaction commits, and nothing can read a gap after that. The publication read what it returns inside
// its transaction, so it does not wait.
func TestACommittedPublicationAnswersWithItsCard(t *testing.T) {
	r := newReading(t)
	store := &answered{Store: r.maker.Receipts}
	r.maker.Receipts = store
	taker, watcher := session(t, r.url), session(t, r.url)
	_, err := taker.Exec(ctx, "BEGIN")
	ok(t, err)
	taken, asked := make(chan error, 1), false
	postgres.SetCrash(r.store, func(point string) error {
		if point != "publish:sealed" || asked {
			return nil
		}
		asked = true
		go func() {
			_, err := taker.Exec(ctx, "LOCK TABLE gaps IN ACCESS EXCLUSIVE MODE")
			taken <- err
		}()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			var waiting int
			if err := watcher.QueryRow(ctx, `SELECT count(*) FROM pg_locks
				WHERE relation = to_regclass('gaps') AND mode = 'AccessExclusiveLock' AND NOT granted`).Scan(&waiting); err != nil {
				return err
			}
			if waiting == 1 {
				return nil
			}
		}
		return errors.New("the other session never waited for the gaps table: the card wrote no gap")
	})

	sealed, err := r.maker.Brief(ctx, r.brief)
	if store.err != nil {
		t.Errorf("the publication committed and answered %v", store.err)
	}
	if err != nil || sealed == nil || sealed.Seal.Status != "ok" || len(sealed.Gaps) == 0 {
		t.Fatalf("a card: %t; %v", sealed != nil, err)
	}
	if err := <-taken; err != nil {
		t.Fatalf("the other session took the gaps table: %v", err)
	}
	_, err = taker.Exec(ctx, "ROLLBACK")
	ok(t, err)
	if n := count(t, &postgrestest.Fixture{URL: r.url}, "SELECT count(*) FROM cards"); n != 1 {
		t.Fatalf("%d cards stored", n)
	}
}
