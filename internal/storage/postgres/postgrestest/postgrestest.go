// Package postgrestest builds a small published card on a fresh database,
// through the same step-by-step functions the connector uses, for tests of
// the surfaces that read it.
package postgrestest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Fixture is a demo workspace with one published card for acc-003.
type Fixture struct {
	URL        string
	Store      *postgres.Store
	Workspace  postgres.Workspace
	Connection postgres.Connection
	// Reader holds a grant on acc-003; Outsider holds none.
	Reader, Outsider postgres.Principal
	Card             postgres.Card
	// Parent is the brief's prepare_renewal_brief call, which seals the
	// card, so Seal is Parent; Read is its child read.
	Parent, Read, Seal postgres.Receipt
	// SealLease is the lease the call was started with, which finishes it.
	SealLease string
}

// Account is the fixture's account.
const Account = "acc-003"

// Record is a record version whose body is the record as a card shows it.
func Record(table, id string, fields ...any) postgres.RecordVersion {
	record := strictjson.NewObject(append(fields, "record_ref", "crm/"+table+"/"+id)...)
	body, _ := strictjson.Dumps(record, strictjson.Prepared)
	return postgres.RecordVersion{Source: "crm", Table: table, RecordID: id, Body: []byte(body)}
}

// CardBytes is a card document as PublishCard requires it: it names the
// brief, the account and the seal (a prepare_renewal_brief call seals a
// prepared brief, anything else a card made by brief), embeds each read as
// stored, shows each record, holds exactly these units and gaps, and prints
// AccountLine under RECORD VALUES.
func CardBytes(t testing.TB, seal postgres.Receipt, account, brief string, reads []postgres.Receipt,
	records []postgres.RecordVersion, units []postgres.Unit, gaps []postgres.Gap) []byte {
	t.Helper()
	embedded := make([]any, len(reads))
	for i, r := range reads {
		value, err := r.AuditValue()
		if err != nil {
			t.Fatal(err)
		}
		embedded[i] = value
	}
	shown := make([]any, len(records))
	for i, v := range records {
		shown[i] = load(t, v.Body)
	}
	bodies := make([]any, len(units))
	for _, u := range units {
		bodies[u.N-1] = load(t, u.Body)
	}
	list := make([]any, len(gaps))
	for i, g := range gaps {
		var count, byRole any
		if g.Count != nil {
			count = *g.Count
		}
		if len(g.ByRole) > 0 {
			byRole = load(t, g.ByRole)
		}
		list[i] = strictjson.NewObject("code", g.Code, "disposition", g.Disposition, "count", count,
			"by_role", byRole, "text", g.Text)
	}
	doc := strictjson.NewObject("schema_version", 3, "brief_id", brief, "account_id", account)
	if seal.Tool == postgres.Composite {
		doc.Set("kind", "selected_brief")
		doc.Set("prepared_by", strictjson.NewObject("outer_audit_id", seal.ID, "token_id", seal.TokenID))
	} else {
		doc.Set("sealed_by", seal.ID)
	}
	doc.Set("text", text(account, []evidence.Line{AccountLine()}))
	doc.Set("units", bodies)
	doc.Set("gaps", list)
	doc.Set("evidence", strictjson.NewObject("brief_id", brief, "account_id", account, "receipts", embedded,
		"records", shown))
	out, err := strictjson.Dumps(doc, strictjson.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(out)
}

// AsOf is the day a card WithLines makes is as of.
const AsOf = "2027-01-15"

// WithLines makes a card as of AsOf whose text prints these lines under
// ATTENTION and RECORD VALUES, signals first, and projects them, as the maker
// does.
func WithLines(t testing.TB, pub *postgres.CardPublication, lines ...evidence.Line) {
	t.Helper()
	doc := load(t, pub.Bytes).(*strictjson.Object)
	doc.Set("as_of", AsOf)
	doc.Set("text", text(pub.AccountRef, lines))
	out, err := strictjson.Dumps(doc, strictjson.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	pub.Bytes, pub.Lines = []byte(out), LineRows(t, lines...)
}

// text is a card's text that prints these lines under ATTENTION and RECORD
// VALUES, signals first.
func text(account string, lines []evidence.Line) string {
	var signals, values []evidence.Line
	for _, l := range lines {
		if l.Section == "signal" {
			signals = append(signals, l)
		} else {
			values = append(values, l)
		}
	}
	out := "DEAL CARD " + account + "\n"
	if len(signals) > 0 {
		out += "\n" + evidence.AttentionHeading + "\n" + strings.Join(evidence.RenderLines(signals), "\n") + "\n"
	}
	return out + "\n" + evidence.ValuesHeading + "\n" + strings.Join(evidence.RenderLines(values), "\n") + "\n"
}

// LineRows are lines as a publication carries them.
func LineRows(t testing.TB, lines ...evidence.Line) []postgres.Line {
	t.Helper()
	rows := make([]postgres.Line, len(lines))
	for i, l := range lines {
		body, err := strictjson.Dumps(l.Value(), strictjson.Prepared)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = postgres.Line{Section: l.Section, Body: json.RawMessage(body)}
	}
	return rows
}

// AccountLine is the one line a card CardBytes makes prints: its account,
// unit 1.
func AccountLine() evidence.Line {
	return evidence.Line{Section: "value", Code: "account", Label: "Account",
		Text: `"Invented account"; industry Missing`, Cites: []int{1}}
}

// FixtureLines are the fixture card's lines, each citing its account record: a
// signal, the renewal date with its countdown, the annual recurring revenue
// with its amount, and the account.
func FixtureLines() []evidence.Line {
	usd := "USD"
	return []evidence.Line{
		{Section: "signal", Code: "crm_at_risk", Text: "CRM marks this account at risk.", Cites: []int{1}},
		{Section: "value", Code: "renewal_date", Label: "Renewal date", Text: "2027-02-08, in 24 days", Cites: []int{1},
			Countdown: &evidence.Countdown{Date: "2027-02-08", Days: 24, CountsTo: "renewal"}},
		{Section: "value", Code: "arr", Label: "Annual recurring revenue", Text: "USD 450,000.00", Cites: []int{1},
			Amount: &evidence.Amount{Amount: "450000.00", Currency: &usd}},
		AccountLine(),
	}
}

func load(t testing.TB, raw []byte) any {
	t.Helper()
	value, err := strictjson.Loads(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// UnitBody is a unit body carrying the columns PublishCard checks.
func UnitBody(n int, kind, recordRef, tool, shown string, extra map[string]any) json.RawMessage {
	body := map[string]any{"n": n, "kind": kind, "record_ref": recordRef, "tool": tool}
	if shown != "" {
		body["shown"] = shown
	}
	for k, v := range extra {
		body[k] = v
	}
	out, _ := json.Marshal(body)
	return out
}

// Prepared starts a prepare_renewal_brief call for brief as principal, reads
// records under it in one get_account child read, and describes the card
// sealed by that call resting on the read: a record unit on each record, and
// one gap. The call stays pending until the card is published.
func Prepared(t testing.TB, store *postgres.Store, f *Fixture, operation, brief, principal, token string,
	records ...postgres.RecordVersion) (postgres.CardPublication, postgres.Receipt, postgres.Receipt) {
	t.Helper()
	return PreparedFor(t, store, f, Account, operation, brief, principal, token, records...)
}

// PreparedFor is Prepared for another account of the fixture's workspace.
func PreparedFor(t testing.TB, store *postgres.Store, f *Fixture, account, operation, brief, principal, token string,
	records ...postgres.RecordVersion) (postgres.CardPublication, postgres.Receipt, postgres.Receipt) {
	t.Helper()
	ctx := context.Background()
	arguments, _ := json.Marshal(map[string]string{"account_id": account})
	call, err := store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: operation,
		PrincipalID: principal, TokenID: token, Tool: postgres.Composite, Scope: "crm:read", AccountRef: account,
		Arguments: arguments, RequestID: json.RawMessage(`1`), Lease: time.Minute})
	must(t, err)
	read, _, err := store.RunChildRead(ctx, postgres.ChildReadStart{ParentID: call.ID, Ordinal: 1,
		Tool: "get_account", Scope: "crm:read", ConnectionID: f.Connection.ID, BriefID: brief,
		Arguments: arguments, Lease: time.Minute,
	}, func(context.Context) ([]postgres.RecordVersion, error) { return records, nil })
	must(t, err)
	ids, err := store.ReceiptRecordIDs(ctx, f.Workspace.ID, read.ID)
	must(t, err)
	units := make([]postgres.Unit, len(records))
	for i, v := range records {
		units[i] = postgres.Unit{N: i + 1, Kind: "record", RecordRef: v.RecordRef(), Tool: "get_account",
			ReceiptID: read.ID, SourceRecordID: ids[v.RecordRef()],
			Body: UnitBody(i+1, "record", v.RecordRef(), "get_account", "", nil)}
	}
	gaps := []postgres.Gap{{Code: "neutralized", Disposition: "note", Text: "none"}}
	rules := "3"
	return postgres.CardPublication{WorkspaceID: f.Workspace.ID, AccountRef: account, BriefID: brief,
		ParentReceiptID: call.ID, PrincipalID: principal, SealReceiptID: call.ID, SealLeaseToken: call.LeaseToken,
		Kind: "go", SchemaVersion: 3, RulesVersion: &rules,
		Bytes: CardBytes(t, call, account, brief, []postgres.Receipt{read}, records, units, gaps), Units: units,
		Gaps: gaps, Lines: LineRows(t, AccountLine()), ReadReceiptIDs: []string{read.ID}}, call, read
}

// Published is a card published by Publish, with its brief receipt and read.
type Published struct {
	Card       postgres.Card
	Seal, Read postgres.Receipt
}

// Publish grants principal the account, recording the account if it is
// new, and publishes a one-unit card for it sealed by that principal, with
// these gaps.
func Publish(t testing.TB, f *Fixture, account, principal, brief string, gaps ...postgres.Gap) Published {
	t.Helper()
	ctx := context.Background()
	must(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, account, "Invented account "+account))
	must(t, f.Store.GrantAccount(ctx, f.Workspace.ID, principal, account))
	record := Record("accounts", account, "id", account, "name", "Invented account "+account)
	pub, call, read := PreparedFor(t, f.Store, f, account, brief+"-call", brief, principal,
		postgres.TokenID(""), record)
	if len(gaps) > 0 {
		pub.Gaps = gaps
		pub.Bytes = CardBytes(t, call, account, brief, []postgres.Receipt{read}, []postgres.RecordVersion{record},
			pub.Units, gaps)
	}
	card, err := f.Store.PublishCard(ctx, pub)
	must(t, err)
	seal, err := f.Store.Receipt(ctx, f.Workspace.ID, call.ID)
	must(t, err)
	read, err = f.Store.Receipt(ctx, f.Workspace.ID, read.ID)
	must(t, err)
	return Published{card, seal, read}
}

// New migrates a fresh database and publishes the fixture card on it.
func New(t testing.TB) *Fixture { t.Helper(); return newFixture(t, false) }

// BeforeLines is New with the fixture card published as a binary from before
// lines were projected published it: with no lines and no line count, and
// every other rule checked at commit.
func BeforeLines(t testing.TB) *Fixture { t.Helper(); return newFixture(t, true) }

// PublishUncounted writes a card as PublishCard did before lines were
// projected: on a database from before them, exactly so; on one that keeps
// lines, as a card the migration found there, listed in cards_before_lines,
// with the triggers that require a count and fix that list off while it is
// written, on the fixture's own database.
// It gives the card's ID.
func PublishUncounted(t testing.TB, f *Fixture, pub postgres.CardPublication) string {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256(pub.Bytes)
	_, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: pub.SealReceiptID,
		LeaseToken: pub.SealLeaseToken, Status: "ok", HTTPStatus: 200,
		Rows: []string{"briefs/" + pub.BriefID + "/" + hex.EncodeToString(sum[:])}})
	must(t, err)
	conn, err := pgx.Connect(ctx, f.URL)
	must(t, err)
	defer conn.Close(ctx)
	var lined bool
	must(t, conn.QueryRow(ctx, "SELECT to_regclass('card_lines') IS NOT NULL").Scan(&lined))
	toggle := func(on string) {
		if lined {
			_, err := conn.Exec(ctx, "ALTER TABLE cards "+on+" TRIGGER cards_count_lines")
			must(t, err)
			_, err = conn.Exec(ctx, "ALTER TABLE cards_before_lines "+on+" TRIGGER cards_before_lines_fixed")
			must(t, err)
		}
	}
	toggle("DISABLE")
	var id string
	must(t, pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version, card_bytes,
				card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, sha256($7), $8, $9, $10, $11, $12) RETURNING id`,
			pub.WorkspaceID, pub.AccountRef, pub.BriefID, pub.Kind, pub.SchemaVersion, pub.RulesVersion, pub.Bytes,
			pub.ParentReceiptID, pub.SealReceiptID, len(pub.Units), len(pub.Gaps), len(pub.ReadReceiptIDs)).
			Scan(&id); err != nil {
			return err
		}
		for _, u := range pub.Units {
			if _, err := tx.Exec(ctx, `INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool,
				shown, receipt_id, source_record_id, body) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10)`,
				pub.WorkspaceID, id, u.N, u.Kind, u.RecordRef, u.Tool, u.Shown, u.ReceiptID, u.SourceRecordID,
				u.Body); err != nil {
				return err
			}
		}
		for i, g := range pub.Gaps {
			if _, err := tx.Exec(ctx, `INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, count,
				by_role, text) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, pub.WorkspaceID, id, i, g.Code, g.Disposition,
				g.Count, g.ByRole, g.Text); err != nil {
				return err
			}
		}
		for i, read := range pub.ReadReceiptIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO card_reads (workspace_id, card_id, ordinal, receipt_id)
				VALUES ($1, $2, $3, $4)`, pub.WorkspaceID, id, i, read); err != nil {
				return err
			}
		}
		if !lined {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO cards_before_lines (workspace_id, card_id) VALUES ($1, $2)`,
			pub.WorkspaceID, id)
		return err
	}))
	toggle("ENABLE")
	return id
}

func newFixture(t testing.TB, beforeLines bool) *Fixture {
	t.Helper()
	ctx := context.Background()
	f := &Fixture{URL: pgtest.URL(t)}
	store, err := postgres.Open(ctx, f.URL)
	must(t, err)
	t.Cleanup(store.Close)
	must(t, store.Migrate(ctx))
	f.Store = store
	f.Workspace, err = store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	must(t, err)
	f.Reader, err = store.CreatePrincipal(ctx, f.Workspace, "person", "Demo reader")
	must(t, err)
	f.Outsider, err = store.CreatePrincipal(ctx, f.Workspace, "person", "Demo outsider")
	must(t, err)
	must(t, store.EnsureAccount(ctx, f.Workspace.ID, Account, "Invented account"))
	must(t, store.GrantAccount(ctx, f.Workspace.ID, f.Reader.ID, Account))
	f.Connection, err = store.EnsureConnection(ctx, f.Workspace, "crm", "demo")
	must(t, err)

	account := Record("accounts", Account, "id", Account, "name", "Invented account")
	contact := Record("contacts", "con-001", "id", "con-001")
	pub, call, read := Prepared(t, store, f, "fixture-brief", "brief-1", f.Reader.ID, postgres.TokenID(""),
		account, contact)
	// Unit 2 is a sentence of the contact record.
	pub.Units[1] = postgres.Unit{N: 2, Kind: "sentence", RecordRef: "crm/contacts/con-001", Tool: "get_account",
		Shown: "yes", ReceiptID: read.ID, SourceRecordID: pub.Units[1].SourceRecordID,
		Body: UnitBody(2, "sentence", "crm/contacts/con-001", "get_account", "yes", map[string]any{"text": "We agree."})}
	pub.Gaps = []postgres.Gap{{Code: "neutralized", Disposition: "note", Count: ptr(1),
		Text: "1 sentence had markup neutralized."}}
	pub.Bytes = CardBytes(t, call, Account, "brief-1", []postgres.Receipt{read}, []postgres.RecordVersion{account,
		contact}, pub.Units, pub.Gaps)
	if beforeLines {
		f.Card, err = store.Card(ctx, f.Workspace.ID, PublishUncounted(t, f, pub))
		must(t, err)
	} else {
		WithLines(t, &pub, FixtureLines()...)
		f.Card, err = store.PublishCard(ctx, pub)
		must(t, err)
	}
	f.Parent, err = store.Receipt(ctx, f.Workspace.ID, call.ID)
	must(t, err)
	f.Read, err = store.Receipt(ctx, f.Workspace.ID, read.ID)
	must(t, err)
	f.Seal, f.SealLease = f.Parent, call.LeaseToken
	return f
}

// OracleGaps gives each gap with one of these codes as the first oracle demo
// card stating it wrote it. A null count or by_role stays nil, and a gap
// without a count key fails the test.
func OracleGaps(t testing.TB, codes ...string) []postgres.Gap {
	t.Helper()
	found := map[string]map[string]json.RawMessage{}
	for _, line := range oracletest.Lines(t, "cards/demo.jsonl.gz") {
		var row struct{ Card string }
		var card struct{ Gaps []map[string]json.RawMessage }
		if json.Unmarshal(line, &row) != nil || json.Unmarshal([]byte(row.Card), &card) != nil {
			t.Fatal("cards/demo.jsonl.gz holds a line that is not a card")
		}
		for _, g := range card.Gaps {
			var code string
			json.Unmarshal(g["code"], &code)
			if found[code] == nil {
				found[code] = g
			}
		}
	}
	gaps := make([]postgres.Gap, len(codes))
	for i, code := range codes {
		g := found[code]
		if g == nil {
			t.Fatalf("no oracle demo card states %s", code)
		}
		count, counted := g["count"]
		if !counted {
			t.Fatalf("the oracle's %s gap has no count key", code)
		}
		gaps[i] = postgres.Gap{Code: code}
		json.Unmarshal(g["disposition"], &gaps[i].Disposition)
		json.Unmarshal(g["text"], &gaps[i].Text)
		if string(count) != "null" {
			gaps[i].Count = new(int)
			json.Unmarshal(count, gaps[i].Count)
		}
		if string(g["by_role"]) != "null" {
			gaps[i].ByRole = g["by_role"]
		}
	}
	return gaps
}

func ptr(n int) *int { return &n }

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Read is one finished top-level CRM read of account by principal ("" for the owner), which returned these
// records.
func Read(t testing.TB, f *Fixture, tool, account, principal string, records ...postgres.RecordVersion) postgres.Receipt {
	t.Helper()
	ctx := context.Background()
	arguments, _ := json.Marshal(map[string]string{"account_id": account})
	var id [16]byte
	_, err := rand.Read(id[:])
	must(t, err)
	started, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "read-" + hex.EncodeToString(id[:]), PrincipalID: principal, TokenID: postgres.TokenID(""),
		Tool: tool, Scope: "crm:read", AccountRef: account, ConnectionID: f.Connection.ID, Arguments: arguments,
		RequestID: json.RawMessage(`1`), Lease: time.Minute})
	must(t, err)
	finished, err := f.Store.FinishRead(ctx, started.ID, started.LeaseToken, records, 1)
	must(t, err)
	return finished
}

// RegisterDeal registers an open deal of account, named from word and 2027, by principal ("" for the
// owner), binding the account and the opportunity opp-1 that its two reads returned.
func RegisterDeal(t testing.TB, f *Fixture, account, principal, word string) postgres.Deal {
	t.Helper()
	accountRead := Read(t, f, "get_account", account, principal,
		Record("accounts", account, "id", account, "name", "Invented account "+account))
	opportunities := Read(t, f, "list_opportunities", account, principal,
		Record("opportunities", "opp-1", "id", "opp-1", "account_id", account))
	deal, err := f.Store.RegisterDeal(context.Background(), postgres.DealRegistration{WorkspaceID: f.Workspace.ID,
		PrincipalID: principal, AccountRef: account, Word: word, Year: 2027, OpportunityRef: "opp-1",
		RenewsOn: "2027-02-08", AccountReadID: accountRead.ID, OpportunitiesReadID: opportunities.ID})
	must(t, err)
	return deal
}
