package postgres_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// reportingCounts are, for each reporting view, the count of its rows and the count of the stored rows it
// reports, each as "view=N".
func reportingCounts(t *testing.T, url string) (views, stored string) {
	t.Helper()
	views = scalar(t, url, `SELECT format('deals=%s cards=%s card_lines=%s units=%s receipts=%s deal_cards=%s',
		(SELECT count(*) FROM reporting.deals), (SELECT count(*) FROM reporting.cards),
		(SELECT count(*) FROM reporting.card_lines), (SELECT count(*) FROM reporting.units),
		(SELECT count(*) FROM reporting.receipts), (SELECT count(*) FROM reporting.cards WHERE deal_id IS NOT NULL))`)
	stored = scalar(t, url, `SELECT format('deals=%s cards=%s card_lines=%s units=%s receipts=%s deal_cards=%s',
		(SELECT count(*) FROM deals), (SELECT count(*) FROM cards), (SELECT count(*) FROM card_lines),
		(SELECT count(*) FROM evidence_units), (SELECT count(*) FROM receipts), (SELECT count(*) FROM deal_cards))`)
	return views, stored
}

// Each reporting view holds one row for each stored row it reports, and every row leads back to its
// record: a deal's latest card is a card of the deal, a line's citations are units of its card, and a
// unit's read and a card's seal are receipts. The views come back the same after a backup is restored.
func TestReportingViewsCountTheStoredRows(t *testing.T) {
	f := postgrestest.BeforeLines(t)
	postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-since")
	fillEveryTable(t, f)

	views, stored := reportingCounts(t, f.URL)
	if views != stored {
		t.Fatalf("the views count %s, the tables hold %s", views, stored)
	}
	for _, zero := range []string{"deals=0", "cards=0", "card_lines=0", "units=0", "receipts=0", "deal_cards=0"} {
		if strings.Contains(views, zero+" ") || strings.HasSuffix(views, zero) {
			t.Fatalf("the fixture leaves a view empty: %s", views)
		}
	}
	for label, query := range map[string]string{
		"a deal's latest card is a card of the deal": `SELECT count(*)::text FROM reporting.deals d
			LEFT JOIN reporting.cards c ON c.card_id = d.latest_card_id AND c.deal_id = d.deal_id
			WHERE (d.latest_card_id IS NULL) <> (d.card_count = 0) OR d.latest_card_id IS NOT NULL AND c.card_id IS NULL`,
		"a line cites units of its card": `SELECT count(*)::text FROM reporting.card_lines l, unnest(l.cites) n
			WHERE NOT EXISTS (SELECT 1 FROM reporting.units u WHERE u.card_id = l.card_id AND u.n = n)`,
		"a unit's read is a receipt": `SELECT count(*)::text FROM reporting.units u WHERE u.receipt_id IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM reporting.receipts r WHERE r.receipt_id = u.receipt_id)`,
		"a card's seal is a receipt": `SELECT count(*)::text FROM reporting.cards c
			WHERE NOT EXISTS (SELECT 1 FROM reporting.receipts r WHERE r.receipt_id = c.seal_receipt_id)`,
		"a card's digest and day are its own": `SELECT count(*)::text FROM reporting.cards v JOIN cards c ON c.id = v.card_id
			WHERE v.sha256 <> encode(c.card_sha256, 'hex') OR v.size <> octet_length(c.card_bytes)
			   OR v.as_of::text IS DISTINCT FROM convert_from(c.card_bytes, 'UTF8')::jsonb ->> 'as_of'`,
		"a line's values are its body's": `SELECT count(*)::text FROM reporting.card_lines v
			JOIN card_lines l ON l.card_id = v.card_id AND l.ordinal = v.ordinal
			WHERE v.code IS DISTINCT FROM l.body ->> 'code' OR v.text IS DISTINCT FROM l.body ->> 'text'
			   OR to_jsonb(v.cites) <> l.body -> 'cites'
			   OR (l.body -> 'countdown' <> 'null'::jsonb) <> (v.countdown_date IS NOT NULL)
			   OR (l.body -> 'amount' <> 'null'::jsonb) <> (v.amount IS NOT NULL)`,
	} {
		if n := scalar(t, f.URL, query); n != "0" {
			t.Errorf("%s: %s rows do not", label, n)
		}
	}

	files := dump(t, f)
	_, url := freshStoreURL(t)
	restored, err := postgres.Open(ctx, url)
	ok(t, err)
	defer restored.Close()
	ok(t, restored.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data)))
	again, _ := reportingCounts(t, url)
	every := func(u string) string {
		return scalar(t, u, `SELECT string_agg(x::text, E'\n' ORDER BY x::text) FROM (
			SELECT d::text AS x FROM reporting.deals d UNION ALL SELECT c::text FROM reporting.cards c
			UNION ALL SELECT l::text FROM reporting.card_lines l UNION ALL SELECT u::text FROM reporting.units u
			UNION ALL SELECT r::text FROM reporting.receipts r) rows`)
	}
	if again != views || every(url) != every(f.URL) {
		t.Fatalf("after a restore the views count %s, not %s, or hold other rows", again, views)
	}
	const inReporting = `pg_class c JOIN pg_namespace s ON s.oid = c.relnamespace AND s.nspname = 'reporting'`
	granted := `SELECT count(*)::text FROM ` + inReporting + ` WHERE has_table_privilege(gtm_reporting_role(), c.oid, 'SELECT')`
	if n := scalar(t, url, granted); n != "5" {
		t.Fatalf("after a restore the role may read %s views", n)
	}
	// Each view, and each of its time columns, says what it holds.
	uncommented := `SELECT coalesce(string_agg(x, ' '), '') FROM (
		SELECT c.relname::text AS x FROM ` + inReporting + ` WHERE obj_description(c.oid, 'pg_class') IS NULL
		UNION ALL
		SELECT c.relname || '.' || a.attname FROM ` + inReporting + ` JOIN pg_attribute a ON a.attrelid = c.oid
		 WHERE a.attnum > 0 AND a.atttypid IN ('timestamptz'::regtype, 'date'::regtype)
		   AND col_description(c.oid, a.attnum) IS NULL) missing`
	if missing := scalar(t, url, uncommented); missing != "" {
		t.Errorf("no comment on %s", missing)
	}
}

// The reporting role reads the views and nothing else: no table, and no column of a view holds a
// credential, a token, a session, a lease, a call's arguments, a card's bytes or a unit's body.
func TestTheReportingRoleReadsOnlyTheViews(t *testing.T) {
	f := postgrestest.New(t)
	fillEveryTable(t, f)
	// The open deal's newest card prints the fixture card's lines.
	record := postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account, "name", "Invented account")
	pub, _, _ := postgrestest.Prepared(t, f.Store, f, "brief-lines-call", "brief-lines", f.Reader.ID,
		postgres.TokenID(""), record)
	postgrestest.WithLines(t, &pub, postgrestest.FixtureLines()...)
	card, err := f.Store.PublishCard(ctx, pub)
	ok(t, err)
	ok(t, sql(t, f, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref)
		SELECT workspace_id, $1, ref, account_ref FROM deals WHERE account_ref = $2 AND state = 'open'`,
		card.ID, postgrestest.Account))
	name := "gtm_report_" + strings.ReplaceAll(f.Workspace.ID[:8], "-", "")
	const password = "report-password-for-this-test"
	reporting := scalar(t, f.URL, "SELECT gtm_reporting_role()")
	ok(t, sql(t, f, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE %s", name, password, reporting)))
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), f.URL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		conn.Exec(context.Background(), "DROP ROLE "+name)
	})
	parsed, err := pgx.ParseConfig(f.URL)
	ok(t, err)
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", name, password,
		parsed.Host, parsed.Port, parsed.Database))
	ok(t, err)
	defer conn.Close(ctx)

	views := strings.Fields(scalar(t, f.URL, `SELECT string_agg(table_name, ' ' ORDER BY table_name)
		FROM information_schema.views WHERE table_schema = 'reporting'`))
	if strings.Join(views, " ") != "card_lines cards deals receipts units" {
		t.Fatalf("reporting views %v", views)
	}
	// The fixture card states the day it is as of, its renewal line counts down to its day, and its ARR line
	// holds its amount.
	// Every card the fixture stores is as of its day, as each card's own bytes state it.
	if n, want := scalar(t, f.URL, `SELECT count(*)::text FROM reporting.cards WHERE as_of = '`+postgrestest.AsOf+`'`),
		scalar(t, f.URL, `SELECT count(*)::text FROM cards
			WHERE convert_from(card_bytes, 'UTF8')::json ->> 'as_of' = '`+postgrestest.AsOf+`'`); n != want || n == "0" {
		t.Errorf("%s cards are as of the fixture card's day, of %s stored", n, want)
	}
	if n := scalar(t, f.URL, `SELECT count(*)::text FROM reporting.card_lines
		WHERE code = 'renewal_date' AND countdown_date IS NOT NULL AND countdown_days IS NOT NULL AND cites = '{1}'`); n == "0" {
		t.Error("no renewal line counts down to its day")
	}
	if n := scalar(t, f.URL, `SELECT count(*)::text FROM reporting.card_lines WHERE code = 'arr' AND amount > 0`); n == "0" {
		t.Error("no ARR line holds its amount")
	}
	for _, view := range views {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM reporting."+view).Scan(&n); err != nil || n == 0 {
			t.Errorf("the reporting role reads %d rows of %s (%v)", n, view, err)
		}
	}
	// The query docs/api.md gives finds the open deal and the days to its renewal.
	var deal string
	var days int
	if err := conn.QueryRow(ctx, `SELECT d.deal_id, l.countdown_days
		  FROM reporting.deals d
		  JOIN reporting.cards c ON c.card_id = d.latest_card_id
		  JOIN reporting.card_lines l ON l.card_id = c.card_id AND l.code = 'renewal_date'
		 WHERE d.state = 'open'`).Scan(&deal, &days); err != nil || deal == "" {
		t.Errorf("the documented query: %q %d (%v)", deal, days, err)
	}
	// Every view holds the rows of every workspace of the database, each with its workspace_id.
	if n := scalar(t, f.URL, `SELECT count(*)::text FROM information_schema.views v WHERE v.table_schema = 'reporting'
		AND NOT EXISTS (SELECT 1 FROM information_schema.columns c WHERE c.table_schema = 'reporting'
		AND c.table_name = v.table_name AND c.column_name = 'workspace_id')`); n != "0" {
		t.Errorf("%s views have no workspace_id", n)
	}
	forbidden := regexp.MustCompile(`token|lease|password|secret|session|credential|argument|request|bytes|body|binding|digest_of`)
	columns := strings.Fields(scalar(t, f.URL, `SELECT string_agg(table_name || '.' || column_name, ' ')
		FROM information_schema.columns WHERE table_schema = 'reporting'`))
	for _, column := range columns {
		if forbidden.MatchString(column) {
			t.Errorf("the view column %s", column)
		}
	}
	for _, table := range tables(t, f.URL) {
		if _, err := conn.Exec(ctx, "SELECT 1 FROM "+pgx.Identifier{table}.Sanitize()+" LIMIT 1"); code(err) != "42501" {
			t.Errorf("the reporting role read the table %s (%v)", table, err)
		}
	}
	// A view that joins is no view a row can be written through (55000); every other write is a privilege the
	// role does not hold (42501).
	for _, statement := range []string{
		"INSERT INTO reporting.deals (deal_id) VALUES ('x')",
		"UPDATE reporting.receipts SET status = 'ok'",
		"DELETE FROM reporting.units",
		"DELETE FROM reporting.cards",
		"CREATE VIEW reporting.extra AS SELECT 1",
		"CREATE TABLE extra (x int)",
		"SELECT gtm_integrity_violations()",
	} {
		if _, err := conn.Exec(ctx, statement); code(err) != "42501" && code(err) != "55000" {
			t.Errorf("the reporting role ran %q (%v)", statement, err)
		}
	}
	if views, stored := reportingCounts(t, f.URL); views != stored {
		t.Errorf("after the refused writes the views count %s, the tables hold %s", views, stored)
	}
}
