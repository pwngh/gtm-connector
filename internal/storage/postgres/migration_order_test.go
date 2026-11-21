package postgres_test

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
)

// A new migration is named by its UTC minute. The numbered files stop at
// 0013, and a name that is not PREFIX_words.sql is refused.
func TestMigrationFilesAreNamedByTheirTime(t *testing.T) {
	if err := postgres.CheckMigrationNames(); err != nil {
		t.Fatalf("the embedded files: %v", err)
	}
	if err := postgres.CheckMigrationNames("20261121T1830_deal_matches.sql", "20261121T1830_sql_views.sql"); err != nil {
		t.Fatalf("two stamped files: %v", err)
	}
	for name, want := range map[string]string{
		"0014_deal_matches.sql":          "is numbered; name a new migration by its UTC time",
		"0013_deals.sql":                 "is given twice",
		"20261121_deal_matches.sql":      "is not named YYYYMMDDTHHMM_words.sql",
		"20261121T1830_Deal_Matches.sql": "is not named YYYYMMDDTHHMM_words.sql",
		"20261121T1830_deal_matches.up":  "is not named YYYYMMDDTHHMM_words.sql",
		"20261321T1830_deal_matches.sql": "is not stamped with a UTC time",
		"20261121T2460_deal_matches.sql": "is not stamped with a UTC time",
	} {
		if err := postgres.CheckMigrationNames(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	names := postgres.MigrationNames()
	if len(names) < 13 || names[12] != "0013_deals.sql" || !slices.IsSorted(names) {
		t.Fatalf("the embedded files in order: %v", names)
	}
}

// numberedTable is schema_migrations as binaries with numbered migrations
// made it, v0.1.0 among them.
const numberedTable = `CREATE TABLE schema_migrations (
	version integer PRIMARY KEY,
	name text NOT NULL,
	sha256 text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now())`

// migrateNumbered does what such a binary did: it applies files 0001 to n and
// records each under its number.
func migrateNumbered(t *testing.T, url string, n int) {
	t.Helper()
	names, texts := postgres.MigrationNames(), postgres.MigrationSQL()
	exec(t, url, numberedTable)
	for i := range n {
		exec(t, url, texts[i], fmt.Sprintf("INSERT INTO schema_migrations (version, name, sha256) VALUES (%d, '%s', '%s')",
			i+1, names[i], postgres.MigrationDigest(texts[i])))
	}
}

// A database a binary with numbered migrations left at 0012 (v0.1.0) or at
// 0013 starts this binary: before setup its state reads from the old table,
// setup keys the table by name and keeps every recorded digest, and a
// stamped migration then applies. A backup of it restores.
func TestANumberedDatabaseUpgrades(t *testing.T) {
	for _, n := range []int{12, 13} {
		t.Run(fmt.Sprintf("at %04d", n), func(t *testing.T) {
			url := pgtest.URL(t)
			migrateNumbered(t, url, n)
			populate(t, url, "prepare_renewal_brief", 1)
			store, err := postgres.Open(ctx, url)
			ok(t, err)
			t.Cleanup(store.Close)
			names := postgres.MigrationNames()
			want := map[bool]postgres.MigrationState{true: postgres.MigrationsCurrent, false: postgres.MigrationsBehind}
			stands(t, store, want[n == len(names)], "before setup")

			// A backup the old binary made is taken from these tables.
			backup := dumpAt(t, store, n)
			recorded := "SELECT string_agg(name || ' ' || sha256, ',' ORDER BY name) FROM schema_migrations"
			before := scalar(t, url, recorded)

			ok(t, store.Migrate(ctx))
			if v, _ := store.AppliedMigrations(ctx); !slices.Equal(v, names) {
				t.Fatalf("migrated to %v", v)
			}
			if after := scalar(t, url, recorded+" WHERE name <= '"+names[n-1]+"'"); after != before {
				t.Fatalf("the recorded digests were\n%s\nand are\n%s", before, after)
			}
			if key := scalar(t, url, `SELECT string_agg(attname, ',' ORDER BY attnum) FROM pg_attribute
				WHERE attrelid = 'schema_migrations'::regclass AND attnum > 0 AND NOT attisdropped`); key != "name,sha256,applied_at" {
				t.Fatalf("schema_migrations holds %s", key)
			}
			if key := scalar(t, url, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = 'schema_migrations'::regclass AND contype = 'p'`); key != "PRIMARY KEY (name)" {
				t.Fatalf("schema_migrations is keyed by %s", key)
			}
			stands(t, store, postgres.MigrationsCurrent, "after setup")
			ok(t, store.Migrate(ctx))

			const stamped = "20261121T1830_upgrade_probe.sql"
			postgres.AddMigrations(store, map[string]string{stamped: "CREATE TABLE upgrade_probe (id integer)"})
			stands(t, store, postgres.MigrationsBehind, "a stamped migration not applied")
			ok(t, store.Migrate(ctx))
			stands(t, store, postgres.MigrationsCurrent, "the stamped migration applied")
			postgres.AddMigrations(store, nil)
			stands(t, store, postgres.MigrationsAhead, "a binary without the stamped migration")

			restored, restoredURL, err := backup.restore(t)
			ok(t, err)
			if v, _ := restored.AppliedMigrations(ctx); !slices.Equal(v, names) {
				t.Fatalf("restored with %v", v)
			}
			stands(t, restored, postgres.MigrationsCurrent, "the restored backup")
			const receipts = "SELECT string_agg(operation_id || ' ' || arguments::text, ',' ORDER BY operation_id) FROM receipts"
			if a, b := scalar(t, url, receipts), scalar(t, restoredURL, receipts); a != b {
				t.Fatalf("receipts %s, restored as %s", a, b)
			}
		})
	}
}

// A database whose numbered table records a changed file or a file this
// binary does not hold is refused before setup changes it.
func TestANumberedDatabaseIsRefusedUnchanged(t *testing.T) {
	for label, statement := range map[string]string{
		"changed": "UPDATE schema_migrations SET sha256 = 'changed' WHERE version = 1",
		"ahead":   "INSERT INTO schema_migrations (version, name, sha256) VALUES (14, '0014_later.sql', 'x')",
	} {
		t.Run(label, func(t *testing.T) {
			url := pgtest.URL(t)
			migrateNumbered(t, url, 13)
			exec(t, url, statement)
			store, err := postgres.Open(ctx, url)
			ok(t, err)
			t.Cleanup(store.Close)
			if err := store.Migrate(ctx); err == nil || !strings.Contains(err.Error(), label) {
				t.Fatalf("setup answered %v", err)
			}
			if scalar(t, url, "SELECT count(*)::text FROM pg_attribute WHERE attrelid = 'schema_migrations'::regclass AND attname = 'version'") != "1" {
				t.Fatal("a refused setup converted schema_migrations")
			}
		})
	}
}

// Two migrations written apart, stamped a before b, merge in either order: a
// database that applied b and then a, and one that applied a and then b, hold
// the same schema, and a backup of either restores. Both add a column to one
// table, so the columns stand in apply order; a restore loads them by name.
func TestMigrationsWrittenApartApplyInEitherOrder(t *testing.T) {
	const a, b = "20261121T1830_a_note.sql", "20261121T1845_b_note.sql"
	files := map[string]string{
		a: "ALTER TABLE workspaces ADD COLUMN a_note text NOT NULL DEFAULT ''",
		b: "ALTER TABLE workspaces ADD COLUMN b_note text; CREATE INDEX workspaces_b_note ON workspaces (b_note)",
	}
	only := func(name string) map[string]string { return map[string]string{name: files[name]} }
	build := func(first, second string) (*postgres.Store, string) {
		url := pgtest.URL(t)
		store, err := postgres.Open(ctx, url)
		ok(t, err)
		t.Cleanup(store.Close)
		postgres.AddMigrations(store, only(first))
		ok(t, store.Migrate(ctx))
		postgres.AddMigrations(store, files)
		stands(t, store, postgres.MigrationsBehind, "the second file merged")
		ok(t, store.Migrate(ctx))
		stands(t, store, postgres.MigrationsCurrent, "both applied")
		exec(t, url, `INSERT INTO workspaces (id, slug, kind, display_name, a_note, b_note) VALUES
			('11111111-1111-4111-8111-111111111111', 'demo', 'demo', 'Demo workspace', 'from a', 'from b')`)
		return store, url
	}
	bThenA, bThenAURL := build(b, a)
	aThenB, aThenBURL := build(a, b)

	if order := scalar(t, bThenAURL, "SELECT string_agg(name, ' ' ORDER BY applied_at, name) FROM schema_migrations WHERE name IN ('"+a+"', '"+b+"')"); order != b+" "+a {
		t.Fatalf("applied as %s", order)
	}
	if one, other := schema(t, bThenAURL), schema(t, aThenBURL); one != other {
		t.Fatalf("b then a holds\n%s\na then b holds\n%s", one, other)
	}

	for label, store := range map[string]*postgres.Store{"b then a": bThenA, "a then b": aThenB} {
		tables := map[string]*bytes.Buffer{}
		applied, copies, err := store.Dump(ctx, func(table string) (io.WriteCloser, error) {
			tables[table] = &bytes.Buffer{}
			return memoryFile{tables[table]}, nil
		})
		ok(t, err)
		url := pgtest.URL(t)
		fresh, err := postgres.Open(ctx, url)
		ok(t, err)
		t.Cleanup(fresh.Close)
		postgres.AddMigrations(fresh, files)
		ok(t, fresh.Restore(ctx, strings.Repeat("f", 64), applied, copies, func(table string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(tables[table].Bytes())), nil
		}))
		if notes := scalar(t, url, "SELECT a_note || ', ' || b_note FROM workspaces"); notes != "from a, from b" {
			t.Fatalf("%s: restored the notes as %s", label, notes)
		}
		if one, other := schema(t, url), schema(t, aThenBURL); one != other {
			t.Fatalf("%s: restored\n%s\nnot\n%s", label, one, other)
		}
	}
}

// schema describes every column, index, constraint, function and trigger in
// a database, and the applied migrations with their digests, in name order.
func schema(t *testing.T, url string) string {
	t.Helper()
	return scalar(t, url, `SELECT string_agg(x, E'\n' ORDER BY x) FROM (
		SELECT format('column %s.%s %s %s %s', table_name, column_name, data_type, is_nullable, column_default) x
		  FROM information_schema.columns WHERE table_schema = current_schema()
		UNION ALL SELECT format('index %s', pg_get_indexdef(i.indexrelid))
		  FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid WHERE c.relnamespace = current_schema()::regnamespace
		UNION ALL SELECT format('constraint %s %s %s', conrelid::regclass, conname, pg_get_constraintdef(oid))
		  FROM pg_constraint WHERE connamespace = current_schema()::regnamespace
		UNION ALL SELECT format('function %s %s', p.oid::regprocedure, md5(pg_get_functiondef(p.oid)))
		  FROM pg_proc p WHERE pronamespace = current_schema()::regnamespace AND prokind = 'f'
		UNION ALL SELECT format('trigger %s %s', tgrelid::regclass, pg_get_triggerdef(oid))
		  FROM pg_trigger WHERE NOT tgisinternal
		UNION ALL SELECT format('migration %s %s', name, sha256) FROM schema_migrations) s`)
}
