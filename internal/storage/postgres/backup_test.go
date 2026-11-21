package postgres_test

import (
	"crypto/sha256"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// tables lists the tables of a database's schema.
func tables(t *testing.T, url string) []string {
	t.Helper()
	list := scalar(t, url, `SELECT coalesce(string_agg(tablename, ' ' ORDER BY tablename), '')
		FROM pg_tables WHERE schemaname = current_schema()`)
	return strings.Fields(list)
}

// Every table the migrations make is backed up or left out by name, so a
// new table cannot be dropped from backups without anyone deciding it.
func TestEveryTableIsBackedUpOrLeftOut(t *testing.T) {
	f := postgrestest.New(t)
	backedUp := postgres.BackupTableNames(postgres.LatestSchemaVersion())
	for _, table := range tables(t, f.URL) {
		_, left := postgres.NotBackedUp()[table]
		if slices.Contains(backedUp, table) == left {
			t.Errorf("table %s: backed up %v, left out %v", table, !left, left)
		}
	}
	for _, table := range backedUp {
		if !slices.Contains(tables(t, f.URL), table) {
			t.Errorf("the backup lists %s, which no migration makes", table)
		}
	}
	for table := range postgres.NotBackedUp() {
		if !slices.Contains(tables(t, f.URL), table) {
			t.Errorf("the backup leaves out %s, which no migration makes", table)
		}
	}
}

// fillEveryTable adds to the fixture a row for each table it leaves empty: a
// machine principal and its token, a delivery, a revoked grant, a disabled
// principal, a session, a consumed pre-session and an imported transcript.
func fillEveryTable(t *testing.T, f *postgrestest.Fixture) {
	t.Helper()
	machine, err := f.Store.CreatePrincipal(ctx, f.Workspace, "machine", "Demo machine")
	ok(t, err)
	ok(t, sql(t, f, `INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, selection,
		expires_at) VALUES ($1, $2, sha256('token'), ARRAY['crm:read', 'calls:read'], ARRAY['acc-003'],
		now() + interval '1 day')`, f.Workspace.ID, machine.ID))
	ok(t, f.Store.MarkDelivered(ctx, f.Seal.ID))
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Other account"))
	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, f.Outsider.ID, "acc-004"))
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Outsider.ID, "acc-004"))
	ok(t, f.Store.DisablePrincipal(ctx, f.Workspace.ID, f.Outsider.ID))
	_, err = f.Store.CreateSession(ctx, id(9), f.Reader, time.Hour)
	ok(t, err)
	pre := sha256.Sum256([]byte("pre-session"))
	_, err = f.Store.SignIn(ctx, postgres.Admission{PreSession: true, IDSHA256: pre[:],
		ExpiresAt: time.Now().Add(time.Hour)}, id(10), f.Reader, time.Hour)
	ok(t, err)
	// One transcript imported through an AccountNumber fills the five tables of imports.
	publish(t, imports(t, f), importing{binding: salesforceBinding, account: "acc-sf-001000000000001AAA",
		number: "INVENTED-1"})
	// One mapped thread fills the Slack mappings.
	_, _, err = f.Store.SaveSlackMapping(ctx, f.Workspace.ID,
		slackMapping(t, "acc-004", "C0123", "1790000000.000001", strings.Repeat("7", 64)), false, false)
	ok(t, err)
	// The Python card of a recorded folder fills the admitted cards and receipts.
	folder, err := cards.OpenTrustedExport(oracletest.Path(t, "folders/cli_card"))
	ok(t, err)
	brief := "brief-2430d066000040008000000000000001"
	seal := "2430d066-0000-4000-8000-000000000006"
	added, err := f.Store.Admit(ctx, f.Workspace.ID, []postgres.AdmittedCard{{BriefID: brief, Kind: "cli",
		AccountID: "acc-003", Bytes: folder.Payload(brief), SealID: seal}}, folder.Lines)
	ok(t, err)
	if len(added.Cards) != 1 || len(added.Receipts) != len(folder.Lines) {
		t.Fatalf("admitted %+v", added)
	}
}

// A backup restores every row of every table it holds, column for column,
// and nothing of the tables it leaves out.
func TestABackupRestoresEveryRow(t *testing.T) {
	// A card from before lines were kept, then one that keeps them.
	f := postgrestest.BeforeLines(t)
	postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-since")
	fillEveryTable(t, f)
	files := dump(t, f)
	fresh, url := freshStoreURL(t)
	ok(t, fresh.Restore(ctx, strings.Repeat("d", 64), files.version, files.copies, opener(files.data)))

	every := func(u, table string) string {
		return scalar(t, u, `SELECT coalesce(string_agg(x::text, E'\n' ORDER BY x::text), '') FROM `+table+` x`)
	}
	for _, table := range postgres.BackupTableNames(postgres.LatestSchemaVersion()) {
		before, after := every(f.URL, table), every(url, table)
		if before == "" {
			t.Errorf("the fixture leaves %s empty", table)
		}
		if before != after {
			t.Errorf("%s restored as\n%s\nnot\n%s", table, after, before)
		}
	}
	for table := range postgres.NotBackedUp() {
		if every(f.URL, table) == "" && table != "restores" {
			t.Errorf("the fixture leaves %s empty", table)
		}
	}
	for _, table := range []string{"sessions", "consumed_presessions"} {
		if rows := every(url, table); rows != "" {
			t.Errorf("a restore kept %s: %s", table, rows)
		}
	}
	if n := scalar(t, url, "SELECT count(*)::text FROM restores"); n != "1" {
		t.Errorf("%s restores recorded", n)
	}
}
