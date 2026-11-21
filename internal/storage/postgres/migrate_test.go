package postgres_test

import (
	"context"
	"fmt"
	"slices"
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

// later records a migration this binary does not hold, as a newer binary's
// setup leaves it, and notLater removes it.
const (
	later    = "INSERT INTO schema_migrations (name, sha256) VALUES ('29991231T2359_later.sql', 'x')"
	notLater = "DELETE FROM schema_migrations WHERE name = '29991231T2359_later.sql'"
)

// The tests name no migration: the first and the newest are the ends of
// MigrationNames, and a later one is stamped after every file.
func TestMigrationsAreCurrentOnlyWhenExactlyTheEmbeddedOnesAreApplied(t *testing.T) {
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	names := postgres.MigrationNames()
	first, newest := names[0], names[len(names)-1]

	stands(t, store, postgres.MigrationsBehind, "an empty database")
	ok(t, postgres.MigrateTo(store, ctx, len(names)-1))
	stands(t, store, postgres.MigrationsBehind, "one migration behind")
	ok(t, store.Migrate(ctx))
	stands(t, store, postgres.MigrationsCurrent, "every migration applied")

	exec(t, url, later)
	stands(t, store, postgres.MigrationsAhead, "one migration ahead")
	exec(t, url, notLater)
	stands(t, store, postgres.MigrationsCurrent, "the later migration removed")

	// Every applied migration's digest is recorded, so one that differs shows.
	digest := scalar(t, url, "SELECT sha256 FROM schema_migrations WHERE name = '"+first+"'")
	if scalar(t, url, "SELECT count(*)::text FROM schema_migrations WHERE sha256 !~ '^[0-9a-f]{64}$'") != "0" {
		t.Fatalf("a migration has no digest recorded; the first has %q", digest)
	}
	exec(t, url, "UPDATE schema_migrations SET sha256 = 'changed' WHERE name = '"+first+"'")
	stands(t, store, postgres.MigrationsChanged, "a migration whose file changed")

	// Changed is stated before ahead, and ahead before behind.
	exec(t, url, later)
	stands(t, store, postgres.MigrationsChanged, "changed and ahead")
	exec(t, url, "DELETE FROM schema_migrations WHERE name = '"+newest+"'")
	stands(t, store, postgres.MigrationsChanged, "changed, ahead and behind")
	exec(t, url, fmt.Sprintf("UPDATE schema_migrations SET sha256 = '%s' WHERE name = '%s'", digest, first))
	stands(t, store, postgres.MigrationsAhead, "ahead and behind")
	exec(t, url, notLater)
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
	names := postgres.MigrationNames()
	ok(t, postgres.MigrateTo(store, ctx, len(names)-1))
	refused := func(state string, want postgres.MigrationState) {
		t.Helper()
		if err := store.Migrate(ctx); err == nil || err.Error() != want.Err().Error() {
			t.Fatalf("%s: setup answered %v", state, err)
		}
		if v, err := store.AppliedMigrations(ctx); err != nil || slices.Contains(v, names[len(names)-1]) {
			t.Fatalf("%s: a refused setup migrated to %v (%v)", state, v, err)
		}
	}
	digest := scalar(t, url, "SELECT sha256 FROM schema_migrations WHERE name = '"+names[0]+"'")

	exec(t, url, later)
	refused("ahead", postgres.MigrationsAhead)
	exec(t, url, "UPDATE schema_migrations SET sha256 = 'changed' WHERE name = '"+names[0]+"'")
	refused("changed and ahead", postgres.MigrationsChanged)
	exec(t, url, notLater)
	refused("changed", postgres.MigrationsChanged)

	exec(t, url, fmt.Sprintf("UPDATE schema_migrations SET sha256 = '%s' WHERE name = '%s'", digest, names[0]))
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
	_, err = migrating.Exec(ctx, later)
	ok(t, err)
	stands(t, store, postgres.MigrationsCurrent, "during a migration")
	ok(t, migrating.Commit(ctx))
	stands(t, store, postgres.MigrationsAhead, "after that migration")

	empty := pgtest.URL(t)
	fresh, err := postgres.Open(ctx, empty)
	ok(t, err)
	t.Cleanup(fresh.Close)
	restoring := begin(empty)
	_, err = restoring.Exec(ctx, "CREATE TABLE schema_migrations (name text PRIMARY KEY, sha256 text NOT NULL)")
	ok(t, err)
	stands(t, fresh, postgres.MigrationsBehind, "during a restore")
	ok(t, restoring.Commit(ctx))
}

// A database that is not encoded as UTF-8 is refused before any migration, with one line that says how to make
// one that is. The checks the migrations install name a character outside ASCII only for a document that holds
// the escape \u0000, so on such a database, migrated by hand, every other card is still checked.
func TestADatabaseNotInUTF8IsRefused(t *testing.T) {
	for _, encoding := range []string{"SQL_ASCII", "LATIN1"} {
		t.Run(encoding, func(t *testing.T) {
			url := pgtest.URLEncoded(t, encoding)
			store, err := postgres.Open(ctx, url)
			ok(t, err)
			defer store.Close()
			want := "the database is encoded as " + encoding + "; create it with ENCODING 'UTF8' TEMPLATE template0"
			if err := store.Migrate(ctx); err == nil || err.Error() != want {
				t.Fatalf("got %v, want %s", err, want)
			}
			if version, err := store.AppliedMigrations(ctx); err != nil || len(version) != 0 {
				t.Fatalf("migrations %v after the refusal (%v)", version, err)
			}
			conn, err := pgx.Connect(ctx, url)
			ok(t, err)
			defer conn.Close(ctx)
			for _, migration := range postgres.MigrationSQL() {
				_, err := conn.Exec(ctx, migration)
				ok(t, err)
			}
			var violation *string
			ok(t, conn.QueryRow(ctx, "SELECT gtm_card_violation(gen_random_uuid(), true)").Scan(&violation))
			if violation != nil {
				t.Fatalf("a card that is not there: %s", *violation)
			}
			var value string
			ok(t, conn.QueryRow(ctx, `SELECT gtm_jsonb_without_nul(convert_to('{"a": "x\\u0000"}', 'UTF8')) ->> 'a'`).
				Scan(&value))
			if value != `x\u0000` {
				t.Fatalf("an escaped backslash before u0000: %q", value)
			}
		})
	}
}

// A document holds the escape \u0000 only where its backslash is not itself escaped.
func TestADocumentHoldsTheNULEscapeExactly(t *testing.T) {
	conn, err := pgx.Connect(ctx, pgtest.URL(t))
	ok(t, err)
	defer conn.Close(ctx)
	for _, migration := range postgres.MigrationSQL() {
		_, err := conn.Exec(ctx, migration)
		ok(t, err)
	}
	for document, want := range map[string]bool{
		`"\u0000"`:        true,
		`"\\u0000"`:       false,
		`"\\\u0000"`:      true,
		`"\\\\u0000"`:     false,
		`"\\\\\u0000"`:    true,
		`"a\u00001"`:      true,
		`"\u00000\\"`:     true,
		`"u0000"`:         false,
		`"\u0001"`:        false,
		"\"\xff\\u0000\"": false,
	} {
		var holds bool
		ok(t, conn.QueryRow(ctx, "SELECT gtm_holds_nul_escape($1::bytea)", []byte(document)).Scan(&holds))
		if holds != want {
			t.Errorf("%q: holds the escape %v, want %v", document, holds, want)
		}
	}
}
