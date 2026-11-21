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

// stands fails the test unless MigrationState answers want within two
// seconds.
func stands(t *testing.T, store *postgres.Store, want postgres.MigrationState, state string) {
	t.Helper()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	got, err := store.MigrationState(bounded)
	if err != nil || got != want {
		t.Fatalf("%s: the migrations are %q (%v), not %s", state, got, err, want)
	}
}

// The tests name no migration by its number: the newest is whatever
// LatestSchemaVersion says, and a later one is one past it.
func TestMigrationsAreCurrentOnlyWhenExactlyTheEmbeddedOnesAreApplied(t *testing.T) {
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	latest := postgres.LatestSchemaVersion()
	later := fmt.Sprintf("INSERT INTO schema_migrations (version, name, sha256) VALUES (%d, 'later.sql', 'x')", latest+1)

	stands(t, store, postgres.MigrationsBehind, "an empty database")
	ok(t, postgres.MigrateTo(store, ctx, latest-1))
	stands(t, store, postgres.MigrationsBehind, "one migration behind")
	ok(t, store.Migrate(ctx))
	stands(t, store, postgres.MigrationsCurrent, "every migration applied")

	exec(t, url, later)
	stands(t, store, postgres.MigrationsAhead, "one migration ahead")
	exec(t, url, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest+1))
	stands(t, store, postgres.MigrationsCurrent, "the later migration removed")

	// Every applied migration's digest is recorded, so one that differs shows.
	digest := scalar(t, url, "SELECT sha256 FROM schema_migrations WHERE version = 1")
	if scalar(t, url, "SELECT count(*)::text FROM schema_migrations WHERE sha256 !~ '^[0-9a-f]{64}$'") != "0" {
		t.Fatalf("a migration has no digest recorded; the first has %q", digest)
	}
	exec(t, url, "UPDATE schema_migrations SET sha256 = 'changed' WHERE version = 1")
	stands(t, store, postgres.MigrationsChanged, "a migration whose file changed")

	// Changed is stated before ahead, and ahead before behind.
	exec(t, url, later)
	stands(t, store, postgres.MigrationsChanged, "changed and ahead")
	exec(t, url, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest))
	stands(t, store, postgres.MigrationsChanged, "changed, ahead and behind")
	exec(t, url, fmt.Sprintf("UPDATE schema_migrations SET sha256 = '%s' WHERE version = 1", digest))
	stands(t, store, postgres.MigrationsAhead, "ahead and behind")
	exec(t, url, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest+1))
	stands(t, store, postgres.MigrationsBehind, "the newest migration's row removed")

	store.Close()
	if _, err := store.MigrationState(ctx); err == nil {
		t.Fatal("a closed store answered")
	}
}

// Each state a server does not run on has its own line, and each line says
// what to do or what is wrong.
func TestEachMigrationStateRefusesInOneLine(t *testing.T) {
	if err := postgres.MigrationsCurrent.Err(); err != nil {
		t.Fatalf("current migrations refuse: %v", err)
	}
	for state, want := range map[postgres.MigrationState]string{
		postgres.MigrationsBehind:  "the database is behind this binary: a migration is not applied; run setup",
		postgres.MigrationsAhead:   "the database is ahead of this binary: this binary is older than the database",
		postgres.MigrationsChanged: "the database's migrations changed: an applied migration's contents differ from this binary's",
	} {
		if err := state.Err(); err == nil || err.Error() != want {
			t.Errorf("%s: %v", state, err)
		}
	}
}

// Setup refuses a database that is ahead or changed in the lines serve
// refuses it with, and says changed before ahead. It applies nothing.
func TestSetupRefusesAnAheadOrAChangedDatabaseInTheSameLines(t *testing.T) {
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	latest := postgres.LatestSchemaVersion()
	ok(t, postgres.MigrateTo(store, ctx, latest-1))
	refused := func(state string, want postgres.MigrationState) {
		t.Helper()
		if err := store.Migrate(ctx); err == nil || err.Error() != want.Err().Error() {
			t.Fatalf("%s: setup answered %v", state, err)
		}
		if v, err := store.SchemaVersion(ctx); err != nil || v == latest {
			t.Fatalf("%s: a refused setup migrated to schema %d (%v)", state, v, err)
		}
	}
	digest := scalar(t, url, "SELECT sha256 FROM schema_migrations WHERE version = 1")

	exec(t, url, fmt.Sprintf("INSERT INTO schema_migrations (version, name, sha256) VALUES (%d, 'later.sql', 'x')", latest+1))
	refused("ahead", postgres.MigrationsAhead)
	exec(t, url, "UPDATE schema_migrations SET sha256 = 'changed' WHERE version = 1")
	refused("changed and ahead", postgres.MigrationsChanged)
	exec(t, url, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest+1))
	refused("changed", postgres.MigrationsChanged)

	exec(t, url, fmt.Sprintf("UPDATE schema_migrations SET sha256 = '%s' WHERE version = 1", digest))
	ok(t, store.Migrate(ctx))
	stands(t, store, postgres.MigrationsCurrent, "behind, and so migrated")
}

// A migration holds the migration lock and writes its row before it commits,
// and a restore creates schema_migrations inside its one transaction. The
// check answers during both, from what is committed, and holds neither up.
func TestMigrationStateWaitsForNoMigrationOrRestore(t *testing.T) {
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
	stands(t, store, postgres.MigrationsCurrent, "during a migration")
	ok(t, migrating.Commit(ctx))
	stands(t, store, postgres.MigrationsAhead, "after that migration")

	empty := pgtest.URL(t)
	fresh, err := postgres.Open(ctx, empty)
	ok(t, err)
	t.Cleanup(fresh.Close)
	restoring := begin(empty)
	_, err = restoring.Exec(ctx, "CREATE TABLE schema_migrations (version integer PRIMARY KEY, sha256 text NOT NULL)")
	ok(t, err)
	stands(t, fresh, postgres.MigrationsBehind, "during a restore")
	ok(t, restoring.Commit(ctx))
}
