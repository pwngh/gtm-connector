package cards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The parity families again, with the maker reading, sealing and publishing through PostgreSQL.
//
// PostgreSQL gives every receipt its ID (gen_random_uuid) and its start time (clock_timestamp) when it
// commits, and takes neither from a caller, so the frozen generator's receipt IDs and times cannot be
// reproduced. Before a card is compared with the oracle exactly those are put back: in the card, each read
// receipt's audit_id and started_at under evidence.receipts, sealed_by, and the text's "Sources read" line,
// which states the first and last start time of the ok reads (checked first to be what the stored times
// give); in the seal, its audit_id, its started_at, and the digest its row names, which is the digest of
// the card being compared. Everything else is compared as stored: every other byte of the card, its SHA-256
// after that replacement, and every other column of the seal. The stored bytes themselves must re-verify
// against the stored receipts.

func TestCardsOnPostgres(t *testing.T) {
	demoStoreFamilies(t, newPostgresCards)
	recordedInputFamilies(t, newPostgresCards)
}

// postgresCards builds a family's cards in one demo workspace of a fresh database, as its local owner.
type postgresCards struct {
	url         string
	conn        *pgx.Conn // the test's own, to look at the rows
	store       *postgres.Store
	receipts    *postgres.ReceiptStore
	workspace   string
	connections map[string]string
}

func newPostgresCards(t *testing.T) cardStore {
	ctx := context.Background()
	c := &postgresCards{url: pgtest.URL(t), connections: map[string]string{}}
	var err error
	if c.store, err = postgres.Open(ctx, c.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.store.Close)
	if err := c.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if c.conn, err = pgx.Connect(ctx, c.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Close(context.Background()) })
	workspace, err := c.store.EnsureWorkspace(ctx, "parity", "demo", "Parity workspace")
	if err != nil {
		t.Fatal(err)
	}
	c.workspace = workspace.ID
	for _, source := range []string{"crm", "calls", "slack"} {
		connection, err := c.store.EnsureConnection(ctx, workspace, source, "demo")
		if err != nil {
			t.Fatal(err)
		}
		c.connections[source] = connection.ID
	}
	c.receipts = &postgres.ReceiptStore{Store: c.store}
	return c
}

func (c *postgresCards) maker(t *testing.T, sc *scope, s setup, row fixtureRow, b Brief) (*Maker, Brief) {
	if err := c.store.EnsureAccount(context.Background(), c.workspace, row.AccountID, "Invented account"); err != nil {
		t.Fatal(err)
	}
	b.Principal.WorkspaceID = c.workspace
	// Operation IDs are unique in the database; the oracle's are per scope.
	digest := sha256.Sum256([]byte(s.scope))
	b.OperationID = "parity-" + hex.EncodeToString(digest[:16])
	return &Maker{Receipts: c.receipts, Source: s.source, Sources: s.sources, Connections: c.connections,
		NewID: sc.uuid, Now: still}, b
}

func (c *postgresCards) published(t *testing.T, sealed *Sealed, s setup, row fixtureRow,
	mismatches *oracletest.Mismatches) ([]byte, string, receipts.Receipt) {
	ctx, conn := context.Background(), c.conn
	var stored, digest []byte
	var kind string
	var units, gaps, unbound int
	err := conn.QueryRow(ctx, `
		SELECT card_bytes, card_sha256, kind,
		       (SELECT count(*) FROM evidence_units u WHERE u.card_id = c.id),
		       (SELECT count(*) FROM gaps g WHERE g.card_id = c.id),
		       (SELECT count(*) FROM evidence_units u WHERE u.card_id = c.id
		           AND (u.receipt_id IS NULL OR u.source_record_id IS NULL))
		  FROM cards c WHERE workspace_id = $1 AND brief_id = $2`, c.workspace, sealed.BriefID).
		Scan(&stored, &digest, &kind, &units, &gaps, &unbound)
	if err != nil {
		t.Fatalf("%s: the published card: %v", row.Key, err)
	}
	sum := sha256.Sum256(stored)
	if !bytes.Equal(stored, sealed.Payload) || !bytes.Equal(digest, sum[:]) || hex.EncodeToString(sum[:]) != sealed.SHA256 ||
		sealed.Seal.Rows[0] != receipts.Reference(sealed.BriefID, stored) {
		mismatches.Add("%s: the stored bytes, their digest and the seal disagree", row.Key)
	}
	if kind != "go" || units != len(sealed.Units) || gaps != len(sealed.Gaps) || unbound != 0 {
		mismatches.Add("%s: %s card with %d units (%d unbound) and %d gaps projected, want %d and %d", row.Key,
			kind, units, unbound, gaps, len(sealed.Units), len(sealed.Gaps))
	}
	if !projectionsMatch(t, conn, c.workspace, sealed.BriefID, row.Card) {
		mismatches.Add("%s: the projected units or gaps differ from the oracle's", row.Key)
	}
	log := StoreLog{Store: c.receipts, WorkspaceID: c.workspace}
	if _, err := VerifyCard(ctx, stored, sealed.BriefID, log, s.sources); err != nil {
		mismatches.Add("%s: the stored card does not verify: %v", row.Key, err)
	}
	normalised, seal, err := normalise(stored, sealed.BriefID, sealed.Seal, row)
	if err != nil {
		mismatches.Add("%s: %v", row.Key, err)
		return stored, sealed.SHA256, sealed.Seal
	}
	sum = sha256.Sum256(normalised)
	return normalised, hex.EncodeToString(sum[:]), seal
}

// projectionsMatch is whether the unit and gap rows hold, as values, the oracle card's units and gaps.
func projectionsMatch(t *testing.T, conn *pgx.Conn, workspace, briefID, card string) bool {
	ctx := context.Background()
	want, err := strictjson.Loads([]byte(card))
	if err != nil {
		t.Fatal(err)
	}
	wantUnits, _ := want.(*strictjson.Object).Get("units")
	wantGaps, _ := want.(*strictjson.Object).Get("gaps")
	rows, err := conn.Query(ctx, `SELECT u.body::text FROM evidence_units u JOIN cards c ON c.id = u.card_id
		WHERE c.workspace_id = $1 AND c.brief_id = $2 ORDER BY u.n`, workspace, briefID)
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]any, len(bodies))
	for i, body := range bodies {
		if units[i], err = strictjson.Loads([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = conn.Query(ctx, `
		SELECT json_build_object('code', g.code, 'disposition', g.disposition, 'count', g.count,
		       'by_role', g.by_role, 'text', g.text)::text
		  FROM gaps g JOIN cards c ON c.id = g.card_id
		 WHERE c.workspace_id = $1 AND c.brief_id = $2 ORDER BY g.ordinal`, workspace, briefID)
	if err != nil {
		t.Fatal(err)
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	gaps := make([]any, len(texts))
	for i, text := range texts {
		if gaps[i], err = strictjson.Loads([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	return evidence.Equal(units, wantUnits) && evidence.Equal(gaps, wantGaps)
}

// sourcesRead is the text's line on when the ok reads started, as evidence.Build writes it.
func sourcesRead(reads []any) string {
	var first, last time.Time
	for _, value := range reads {
		r := value.(*strictjson.Object)
		at, err := time.Parse(time.RFC3339Nano, get(r, "started_at").(string))
		if err != nil || get(r, "status") != "ok" {
			continue
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	from, to := first.Format("2006-01-02T15:04:05Z"), last.Format("2006-01-02T15:04:05Z")
	if from == to {
		return "Sources read " + from + "."
	}
	return "Sources read " + from + " to " + to + "."
}

// normalise puts the oracle's receipt IDs and start times into a card and its seal, as the comment at the
// top of this file says, and nothing else.
func normalise(stored []byte, briefID string, seal receipts.Receipt, row fixtureRow) ([]byte, receipts.Receipt, error) {
	reads := func(card []byte) ([]any, string, error) {
		value, err := strictjson.Loads(card)
		if err != nil {
			return nil, "", err
		}
		snapshot, _ := get(value.(*strictjson.Object), "evidence").(*strictjson.Object)
		list, _ := get(snapshot, "receipts").([]any)
		sealedBy, _ := get(value.(*strictjson.Object), "sealed_by").(string)
		return list, sealedBy, nil
	}
	got, gotSeal, err := reads(stored)
	if err != nil {
		return nil, seal, err
	}
	want, wantSeal, err := reads([]byte(row.Card))
	if err != nil {
		return nil, seal, err
	}
	const startedAt = `"started_at": "`
	text := string(stored)
	if len(got) != len(want) || strings.Count(text, startedAt) != len(got) ||
		strings.Count(row.Card, startedAt) != len(want) || gotSeal != seal.AuditID {
		return nil, seal, fmt.Errorf("the card holds %d read receipts, the oracle's %d", len(got), len(want))
	}
	gotLine, wantLine := sourcesRead(got), sourcesRead(want)
	if strings.Count(text, gotLine) != 1 || strings.Count(row.Card, wantLine) != 1 {
		return nil, seal, fmt.Errorf("the text does not state %q once", gotLine)
	}
	ids := []string{`"` + gotSeal + `"`, `"` + wantSeal + `"`, gotLine, wantLine}
	var times []string
	for i := range got {
		g, w := got[i].(*strictjson.Object), want[i].(*strictjson.Object)
		ids = append(ids, `"`+get(g, "audit_id").(string)+`"`, `"`+get(w, "audit_id").(string)+`"`)
		times = append(times, get(w, "started_at").(string))
	}
	text = strings.NewReplacer(ids...).Replace(text)
	// The i-th started_at is the i-th read receipt's.
	var out strings.Builder
	for _, at := range times {
		before, after, _ := strings.Cut(text, startedAt)
		end := strings.IndexByte(after, '"')
		out.WriteString(before + startedAt + at)
		text = after[end:]
	}
	out.WriteString(text)
	normalised := []byte(out.String())

	value, err := strictjson.Loads(row.Seal)
	if err != nil {
		return nil, seal, err
	}
	oracleSeal, err := receipts.Decode(value)
	if err != nil {
		return nil, seal, err
	}
	seal.AuditID, seal.StartedAt = oracleSeal.AuditID, oracleSeal.StartedAt
	seal.Rows = []string{receipts.Reference(briefID, normalised)}
	return normalised, seal, nil
}

// e2e is a demo workspace on a fresh database whose reader holds a grant on acc-003, for the maker to read
// the demo book through.
type e2e struct {
	*postgresCards
	reader postgres.Principal
	reads  map[string]int
}

func newE2E(t *testing.T) *e2e {
	c := newPostgresCards(t).(*postgresCards)
	ctx := context.Background()
	if err := c.store.EnsureAccount(ctx, c.workspace, "acc-003", "Invented account"); err != nil {
		t.Fatal(err)
	}
	w, err := c.store.EnsureWorkspace(ctx, "parity", "demo", "Parity workspace")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := c.store.CreatePrincipal(ctx, w, "person", "Demo reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.GrantAccount(ctx, c.workspace, reader.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	return &e2e{postgresCards: c, reader: reader, reads: map[string]int{}}
}

// during runs before each source read of the demo book, after its receipt committed.
type during struct {
	Source
	before func(tool string)
}

func (d during) Read(tool, accountID string) (string, []*strictjson.Object, error) {
	d.before(tool)
	return d.Source.Read(tool, accountID)
}

// maker reads the demo book; before runs before each read and counts it.
func (e *e2e) maker(t *testing.T, store receipts.Store, before func(tool string)) *Maker {
	return &Maker{Receipts: store, Source: during{demoBook(t), func(tool string) {
		e.reads[tool]++
		if before != nil {
			before(tool)
		}
	}}, Sources: demoSources(nil), Connections: e.connections}
}

func (e *e2e) principal() Principal {
	return Principal{WorkspaceID: e.workspace, PrincipalID: e.reader.ID, TokenID: receipts.Fingerprint("a bearer"),
		Scopes: []string{"crm:read", "calls:read", "slack:read"}}
}

func (e *e2e) brief(operation string) Brief {
	return Brief{Principal: e.principal(), OperationID: operation, AccountID: "acc-003", AsOf: DemoAsOf,
		IncludeSlack: true}
}

func (e *e2e) prepare(operation string) Prepare {
	return Prepare{Principal: e.principal(), Selection: Selection{"acc-003": true}, OperationID: operation,
		RequestID: receipts.RPC(1), Arguments: strictjson.NewObject("account_id", "acc-003")}
}

func (e *e2e) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The receipts rule, end to end on PostgreSQL: no read without a committed receipt, no card from a read the
// source changed under, and no card for a principal whose grant was revoked before publication.
func TestReceiptsRuleOnPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("the store is unavailable at Start: nothing is read", func(t *testing.T) {
		e := newE2E(t)
		down, err := postgres.Open(ctx, e.url)
		if err != nil {
			t.Fatal(err)
		}
		down.Close()
		m := e.maker(t, &postgres.ReceiptStore{Store: down}, nil)
		if _, err := m.Brief(ctx, e.brief("down-brief")); err == nil {
			t.Fatal("a card without a receipt store")
		}
		if _, err := m.Prepare(ctx, e.prepare("down-prepare")); err == nil {
			t.Fatal("a prepared brief without a receipt store")
		}
		if len(e.reads) != 0 || e.count(t, "SELECT count(*) FROM receipts") != 0 {
			t.Fatalf("read %v with no receipt", e.reads)
		}
	})
	t.Run("a generation bump during a read gives no card", func(t *testing.T) {
		e := newE2E(t)
		bump := func(tool string) {
			if tool == "list_opportunities" {
				if _, err := e.store.BumpConnectionGeneration(ctx, e.connections["crm"]); err != nil {
					t.Error(err)
				}
			}
		}
		m := e.maker(t, e.receipts, bump)
		_, err := m.Brief(ctx, e.brief("bumped-brief"))
		if !errors.Is(err, receipts.ErrSourceChanged) {
			t.Fatalf("the CLI card: %v", err)
		}
		_, err = m.Prepare(ctx, e.prepare("bumped-prepare"))
		if !errors.Is(err, receipts.ErrSourceChanged) {
			t.Fatalf("the prepared brief: %v", err)
		}
		if n := e.count(t, "SELECT count(*) FROM cards"); n != 0 {
			t.Fatalf("%d cards", n)
		}
		// Each fenced read finished as source_changed and bound nothing; the prepared brief's call failed.
		if e.count(t, `SELECT count(*) FROM receipts WHERE tool = 'list_opportunities' AND status = 'error'
			AND http_status = 409 AND error = 'source_changed' AND row_count = 0`) != 2 ||
			e.count(t, `SELECT count(*) FROM receipt_records b JOIN receipts r ON r.id = b.receipt_id
			WHERE r.tool = 'list_opportunities'`) != 0 ||
			e.count(t, `SELECT count(*) FROM receipts WHERE operation_id = 'bumped-prepare' AND status = 'error'`) != 1 {
			t.Fatal("the fenced reads were not recorded as fenced")
		}
		// After the change, a new card reads at the new generation.
		if _, err := e.maker(t, e.receipts, nil).Brief(ctx, e.brief("after-the-bump")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a grant revoked before publication gives no card", func(t *testing.T) {
		e := newE2E(t)
		revoke := func(tool string) {
			if tool == "read_slack_messages" { // the last read
				if err := e.store.RevokeAccountGrant(ctx, e.workspace, e.reader.ID, "acc-003"); err != nil {
					t.Error(err)
				}
			}
		}
		_, err := e.maker(t, e.receipts, revoke).Brief(ctx, e.brief("revoked-brief"))
		if !errors.Is(err, receipts.ErrNotPermitted) {
			t.Fatalf("the CLI card: %v", err)
		}
		if err := e.store.GrantAccount(ctx, e.workspace, e.reader.ID, "acc-003"); err != nil {
			t.Fatal(err)
		}
		_, err = e.maker(t, e.receipts, revoke).Prepare(ctx, e.prepare("revoked-prepare"))
		if !errors.Is(err, receipts.ErrNotPermitted) {
			t.Fatalf("the prepared brief: %v", err)
		}
		if n := e.count(t, "SELECT count(*) FROM cards"); n != 0 {
			t.Fatalf("%d cards", n)
		}
		if e.count(t, `SELECT count(*) FROM receipts WHERE (operation_id = 'revoked-brief/seal'
			OR operation_id = 'revoked-prepare') AND status = 'error' AND http_status = 403
			AND error = 'not_permitted'`) != 2 {
			t.Fatal("the seals were not finished as not permitted")
		}
	})
}

// A prepared brief on PostgreSQL: its reads are children of its call, which seals it, and it verifies.
func TestPreparedBriefOnPostgres(t *testing.T) {
	ctx := context.Background()
	e := newE2E(t)
	sealed, err := e.maker(t, e.receipts, nil).Prepare(ctx, e.prepare("prepared"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var stored []byte
	var parent, seal string
	if err := conn.QueryRow(ctx, `SELECT card_bytes, parent_receipt_id::text, seal_receipt_id::text FROM cards
		WHERE brief_id = $1`, sealed.BriefID).Scan(&stored, &parent, &seal); err != nil {
		t.Fatal(err)
	}
	if parent != sealed.Seal.AuditID || seal != parent || !bytes.Equal(stored, sealed.Payload) ||
		e.count(t, `SELECT count(*) FROM receipts WHERE parent_id = $1 AND brief_id = $2`, parent, sealed.BriefID) != 4 {
		t.Fatalf("published under %s, sealed by %s", parent, seal)
	}
	if _, err := VerifyPrepared(ctx, stored, sealed.BriefID, StoreLog{Store: e.receipts, WorkspaceID: e.workspace},
		demoSources(nil)); err != nil {
		t.Fatal(err)
	}
}
