package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
)

// current fails the test unless MigrationsCurrent answers want within two
// seconds.
func current(t *testing.T, store *postgres.Store, want bool, state string) {
	t.Helper()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	got, err := store.MigrationsCurrent(bounded)
	if err != nil || got != want {
		t.Fatalf("%s: current is %v (%v)", state, got, err)
	}
}

func TestMigrationsAreCurrentOnlyWhenExactlyTheEmbeddedOnesAreApplied(t *testing.T) {
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	latest := postgres.LatestSchemaVersion()

	current(t, store, false, "an empty database")
	ok(t, postgres.MigrateTo(store, ctx, latest-1))
	current(t, store, false, "one migration behind")
	ok(t, store.Migrate(ctx))
	current(t, store, true, "every migration applied")

	exec(t, url, fmt.Sprintf("INSERT INTO schema_migrations (version, name, sha256) VALUES (%d, 'later.sql', 'x')", latest+1))
	current(t, store, false, "one migration ahead")
	exec(t, url, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest+1))
	current(t, store, true, "the later migration removed")

	exec(t, url, "UPDATE schema_migrations SET sha256 = 'changed' WHERE version = 1")
	current(t, store, false, "a migration whose file changed")

	store.Close()
	if _, err := store.MigrationsCurrent(ctx); err == nil {
		t.Fatal("a closed store answered")
	}
}

// A migration holds the migration lock and writes its row before it commits,
// and a restore creates schema_migrations inside its one transaction. The
// check answers during both, from what is committed, and holds neither up.
func TestMigrationsCurrentWaitsForNoMigrationOrRestore(t *testing.T) {
	begin := func(url string) pgx.Tx {
		conn, err := pgx.Connect(ctx, url)
		ok(t, err)
		t.Cleanup(func() { conn.Close(ctx) })
		tx, err := conn.Begin(ctx)
		ok(t, err)
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", postgres.MigrationLock)
		ok(t, err)
		return tx
	}

	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	ok(t, store.Migrate(ctx))
	migrating := begin(url)
	_, err = migrating.Exec(ctx, "INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, 'later.sql', 'x')",
		postgres.LatestSchemaVersion()+1)
	ok(t, err)
	current(t, store, true, "during a migration")
	ok(t, migrating.Commit(ctx))
	current(t, store, false, "after that migration")

	empty := pgtest.URL(t)
	fresh, err := postgres.Open(ctx, empty)
	ok(t, err)
	t.Cleanup(fresh.Close)
	restoring := begin(empty)
	_, err = restoring.Exec(ctx, "CREATE TABLE schema_migrations (version integer PRIMARY KEY, sha256 text NOT NULL)")
	ok(t, err)
	current(t, fresh, false, "during a restore")
	ok(t, restoring.Commit(ctx))
}
