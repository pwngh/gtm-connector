package postgres_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
)

// A database at schema 1, as a 0001 binary left it, upgrades and restores
// through two backup generations; one holding rows 0002 refuses is refused at
// migration and at restore, and stays as it was.

// schemaOne is a fresh database at schema 1 and its URL.
func schemaOne(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	ok(t, postgres.MigrateTo(store, ctx, 1))
	return store, url
}

func exec(t *testing.T, url string, statements ...string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	ok(t, err)
	defer conn.Close(ctx)
	ok(t, pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, s := range statements {
			if _, err := tx.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}))
}

func scalar(t *testing.T, url, query string) string {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	ok(t, err)
	defer conn.Close(ctx)
	var out string
	ok(t, conn.QueryRow(ctx, query).Scan(&out))
	return out
}

// populate writes, at schema 1, what a 0001 binary wrote: a workspace, a
// principal with a grant, a connection, a refused call whose arguments were
// given as {"b": 1, "a": 2}, and a prepare_renewal_brief call with one finished
// child read that bound a record. childOrdinal and parentTool shape the child.
func populate(t *testing.T, url string, parentTool string, childOrdinal int) {
	t.Helper()
	ordinal := map[int]string{0: "0", 1: "1"}[childOrdinal]
	exec(t, url,
		`INSERT INTO workspaces (id, slug, kind, display_name) VALUES
			('11111111-1111-4111-8111-111111111111', 'demo', 'demo', 'Demo workspace')`,
		`INSERT INTO principals (id, workspace_id, workspace_kind, kind, display_name) VALUES
			('22222222-2222-4222-8222-222222222222', '11111111-1111-4111-8111-111111111111', 'demo', 'person', 'Reader')`,
		`INSERT INTO accounts (workspace_id, ref, display_name) VALUES
			('11111111-1111-4111-8111-111111111111', 'acc-003', 'Invented account')`,
		`INSERT INTO account_grants (workspace_id, principal_id, account_ref) VALUES
			('11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222', 'acc-003')`,
		`INSERT INTO connections (id, workspace_id, workspace_kind, source, mode) VALUES
			('33333333-3333-4333-8333-333333333333', '11111111-1111-4111-8111-111111111111', 'demo', 'crm', 'demo')`,
		`INSERT INTO receipts (id, workspace_id, operation_id, token_id, tool, arguments, request_id, lease_token,
			lease_expires_at) VALUES ('44444444-4444-4444-8444-444444444444', '11111111-1111-4111-8111-111111111111',
			'refused', 'anonymous', 'get_account', '{"b": 1, "a": 2}', '7', gen_random_uuid(), now() + interval '1 hour')`,
		`UPDATE receipts SET status = 'error', http_status = 403, finished_at = now(), finishing_lease = lease_token
			WHERE id = '44444444-4444-4444-8444-444444444444'`,
		`INSERT INTO receipts (id, workspace_id, operation_id, principal_id, token_id, tool, scope, requested_account,
			account_ref, arguments, request_id, lease_token, lease_expires_at) VALUES
			('55555555-5555-4555-8555-555555555555', '11111111-1111-4111-8111-111111111111', 'brief',
			'22222222-2222-4222-8222-222222222222', 'anonymous', '`+parentTool+`', 'crm:read', 'acc-003', 'acc-003',
			'{"account_id": "acc-003"}', '1', gen_random_uuid(), now() + interval '1 hour')`,
		`INSERT INTO receipts (id, workspace_id, operation_id, parent_id, ordinal, principal_id, token_id, tool, scope,
			requested_account, account_ref, connection_id, connection_generation, arguments, request_id, brief_id,
			lease_token, lease_expires_at) VALUES ('66666666-6666-4666-8666-666666666666',
			'11111111-1111-4111-8111-111111111111', 'brief#`+ordinal+`', '55555555-5555-4555-8555-555555555555', `+ordinal+`,
			'22222222-2222-4222-8222-222222222222', 'anonymous', 'get_account', 'crm:read', 'acc-003', 'acc-003',
			'33333333-3333-4333-8333-333333333333', 1, '{"account_id": "acc-003"}',
			'{"kind": "brief_child", "parent_audit_id": "55555555-5555-4555-8555-555555555555", "ordinal": `+ordinal+`}',
			'brief-1', gen_random_uuid(), now() + interval '1 hour')`,
		`INSERT INTO source_records (id, workspace_id, source, record_table, record_id, content_version, raw_body,
			body_sha256) VALUES ('77777777-7777-4777-8777-777777777777', '11111111-1111-4111-8111-111111111111', 'crm',
			'accounts', 'acc-003', 'v1', '\x7b7d', sha256('\x7b7d'))`,
		`INSERT INTO receipt_records (workspace_id, receipt_id, ordinal, source_record_id) VALUES
			('11111111-1111-4111-8111-111111111111', '66666666-6666-4666-8666-666666666666', 0,
			'77777777-7777-4777-8777-777777777777')`,
		`UPDATE receipts SET status = 'ok', http_status = 200, rows = '["crm/accounts/acc-003"]', row_count = 1,
			finished_at = now(), finishing_lease = lease_token WHERE id = '66666666-6666-4666-8666-666666666666'`,
		`UPDATE receipts SET status = 'error', http_status = 200, error = 'request_failed', finished_at = now(),
			finishing_lease = lease_token WHERE id = '55555555-5555-4555-8555-555555555555'`,
	)
}

// backupAt dumps a store at this schema version into memory.
type backupAt struct {
	version int
	copies  []postgres.TableCopy
	files   map[string]*bytes.Buffer
}

func dumpAt(t *testing.T, s *postgres.Store, version int) backupAt {
	t.Helper()
	b := backupAt{version: version, files: map[string]*bytes.Buffer{}}
	var err error
	b.copies, err = postgres.DumpAt(s, ctx, version, func(table string) (io.WriteCloser, error) {
		b.files[table] = &bytes.Buffer{}
		return memoryFile{b.files[table]}, nil
	})
	ok(t, err)
	return b
}

func (b backupAt) restore(t *testing.T) (*postgres.Store, string, error) {
	t.Helper()
	url := pgtest.URL(t)
	fresh, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(fresh.Close)
	return fresh, url, fresh.Restore(ctx, strings.Repeat("c", 64), b.version, b.copies,
		func(table string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(b.files[table].Bytes())), nil
		})
}

func TestAnUpgradeFrom0001KeepsItsData(t *testing.T) {
	one, url := schemaOne(t)
	populate(t, url, "prepare_renewal_brief", 1)
	first := dumpAt(t, one, 1)
	if len(first.copies) != len(postgres.BackupTableNames(1)) {
		t.Fatalf("a schema 1 backup lists %d tables", len(first.copies))
	}

	// In place.
	ok(t, one.Migrate(ctx))
	if v, _ := one.SchemaVersion(ctx); v != postgres.LatestSchemaVersion() ||
		scalar(t, url, "SELECT count(*)::text FROM gtm_integrity_violations()") != "0" {
		t.Fatal("the upgrade in place")
	}
	// A receipt written before 0002 keeps jsonb's order; one written after keeps what was given.
	if got := scalar(t, url, "SELECT arguments::text FROM receipts WHERE operation_id = 'refused'"); got != `{"a": 2, "b": 1}` {
		t.Fatalf("pre-0002 arguments read back as %s", got)
	}
	exec(t, url, `INSERT INTO receipts (workspace_id, operation_id, token_id, tool, arguments, request_id, lease_token,
		lease_expires_at) VALUES ('11111111-1111-4111-8111-111111111111', 'after', 'anonymous', 'get_account',
		'{"b": 1, "a": 2}', 'null', gen_random_uuid(), now() + interval '1 hour')`)
	if got := scalar(t, url, "SELECT arguments::text FROM receipts WHERE operation_id = 'after'"); got != `{"b": 1, "a": 2}` {
		t.Fatalf("post-0002 arguments read back as %s", got)
	}

	// From the schema 1 backup, then from the schema 2 backup of that.
	second, secondURL, err := first.restore(t)
	ok(t, err)
	if v, _ := second.SchemaVersion(ctx); v != postgres.LatestSchemaVersion() {
		t.Fatalf("restored at schema %d", v)
	}
	third := dumpAt(t, second, postgres.LatestSchemaVersion())
	if len(third.copies) != len(postgres.BackupTableNames(postgres.LatestSchemaVersion())) {
		t.Fatalf("a schema 2 backup lists %d tables", len(third.copies))
	}
	last, lastURL, err := third.restore(t)
	ok(t, err)
	for _, query := range []string{"SELECT count(*)::text FROM receipts", "SELECT count(*)::text FROM receipt_records",
		"SELECT string_agg(arguments::text, ' ' ORDER BY operation_id) FROM receipts"} {
		if a, b := scalar(t, secondURL, query), scalar(t, lastURL, query); a != b || a == "0" {
			t.Fatalf("%s: %s after one restore, %s after two", query, a, b)
		}
	}
	// The restored database answers, and holds exactly this binary's migrations.
	if state, err := last.MigrationState(ctx); err != nil || state != postgres.MigrationsCurrent {
		t.Fatalf("the restored database's migrations are %q (%v)", state, err)
	}
}

func TestAnUpgradeFrom0001RefusesRowsItCannotKeep(t *testing.T) {
	// The 0001 fixture wrote a child numbered 0 under a "brief" receipt.
	one, url := schemaOne(t)
	populate(t, url, "brief", 0)
	backup := dumpAt(t, one, 1)
	err := one.Migrate(ctx)
	if code(err) != "GT018" || !strings.Contains(err.Error(), "66666666-6666-4666-8666-666666666666") {
		t.Fatalf("the upgrade: %v", err)
	}
	if v, _ := one.SchemaVersion(ctx); v != 1 {
		t.Fatalf("left at schema %d", v)
	}
	fresh, _, err := backup.restore(t)
	if err == nil || !strings.Contains(err.Error(), "0002") {
		t.Fatalf("a schema 1 backup with the same rows restored: %v", err)
	}
	if v, _ := fresh.SchemaVersion(ctx); v != 0 {
		t.Fatalf("a refused restore left schema %d", v)
	}
}

// The read-scopes migration takes crm:write out of what a machine token and a receipt may hold. A database
// one schema behind it that holds either is refused by name and left as it was, and a migrated one refuses
// both.
func TestTheReadScopesMigrationRefusesAWriteScope(t *testing.T) {
	before := postgres.MigrationVersion("read_scopes_only") - 1
	if before < 3 {
		t.Fatalf("the read-scopes migration is numbered %d", before+1)
	}
	const workspace, machine = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	token := func(id, scopes string) string {
		return `INSERT INTO machine_tokens (id, workspace_id, principal_id, token_sha256, scopes, expires_at) VALUES
			('` + id + `', '` + workspace + `', '` + machine + `', sha256('` + id + `'), ARRAY[` + scopes + `],
			now() + interval '1 day')`
	}
	receipt := func(id, scope string) string {
		return `INSERT INTO receipts (id, workspace_id, operation_id, token_id, tool, scope, arguments, request_id,
			lease_token, lease_expires_at) VALUES ('` + id + `', '` + workspace + `', '` + id + `', 'anonymous',
			'log_activity', '` + scope + `', '{}', 'null', gen_random_uuid(), now() + interval '1 hour')`
	}
	const writeToken, mixedToken, writeReceipt = "88888888-8888-4888-8888-888888888888",
		"99999999-9999-4999-8999-999999999999", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	behind := func() (*postgres.Store, string) {
		url := pgtest.URL(t)
		store, err := postgres.Open(ctx, url)
		ok(t, err)
		t.Cleanup(store.Close)
		ok(t, postgres.MigrateTo(store, ctx, before))
		exec(t, url,
			`INSERT INTO workspaces (id, slug, kind, display_name) VALUES ('`+workspace+`', 'demo', 'demo', 'Demo workspace')`,
			`INSERT INTO principals (id, workspace_id, workspace_kind, kind, display_name) VALUES
				('`+machine+`', '`+workspace+`', 'demo', 'machine', 'Machine')`,
			token("77777777-7777-4777-8777-777777777777", `'crm:read', 'calls:read', 'slack:read'`),
			receipt("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "crm:read"))
		return store, url
	}
	const rows = "SELECT ((SELECT count(*) FROM machine_tokens) + (SELECT count(*) FROM receipts))::text"

	for _, held := range []struct{ label, row, id string }{
		{"a token with crm:write alone", token(writeToken, `'crm:write'`), writeToken},
		{"a token with crm:write beside a read scope", token(mixedToken, `'crm:read', 'crm:write'`), mixedToken},
		{"a receipt naming crm:write", receipt(writeReceipt, "crm:write"), writeReceipt},
	} {
		store, url := behind()
		exec(t, url, held.row)
		err := store.Migrate(ctx)
		if code(err) != "GT018" || !strings.Contains(err.Error(), held.id) {
			t.Fatalf("%s: the upgrade answered %v", held.label, err)
		}
		if v, _ := store.SchemaVersion(ctx); v != before {
			t.Fatalf("%s: left at schema %d", held.label, v)
		}
		if n := scalar(t, url, rows); n != "3" {
			t.Fatalf("%s: %s rows are left of 3", held.label, n)
		}
	}

	// With read scopes only, the database migrates, keeps its rows, and then refuses the write scope.
	store, url := behind()
	ok(t, store.Migrate(ctx))
	if v, _ := store.SchemaVersion(ctx); v != postgres.LatestSchemaVersion() {
		t.Fatalf("migrated to schema %d", v)
	}
	if n := scalar(t, url, rows); n != "2" {
		t.Fatalf("%s rows are left of 2", n)
	}
	conn, err := pgx.Connect(ctx, url)
	ok(t, err)
	defer conn.Close(ctx)
	for _, refused := range []string{token(writeToken, `'crm:write'`), token(mixedToken, `'crm:read', 'crm:write'`),
		token(mixedToken, `'crm:read', 'admin'`), receipt(writeReceipt, "crm:write")} {
		if _, err := conn.Exec(ctx, refused); code(err) != "23514" {
			t.Fatalf("after the migration the store answered %v to %s", err, refused)
		}
	}
	if _, err := conn.Exec(ctx, receipt("cccccccc-cccc-4ccc-8ccc-cccccccccccc", "slack:read")); err != nil {
		t.Fatalf("a read scope on a receipt: %v", err)
	}
}
