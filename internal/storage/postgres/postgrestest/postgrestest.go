// Package postgrestest builds a small published card on a fresh database,
// through the same step-by-step functions the connector uses, for tests of
// the surfaces that read it.
package postgrestest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
// stored, shows each record, and holds exactly these units and gaps.
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
	doc.Set("text", "DEAL CARD "+account+"\n")
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
	ctx := context.Background()
	call, err := store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: operation,
		PrincipalID: principal, TokenID: token, Tool: postgres.Composite, Scope: "crm:read", AccountRef: Account,
		Arguments: json.RawMessage(`{"account_id": "acc-003"}`), RequestID: json.RawMessage(`1`), Lease: time.Minute})
	must(t, err)
	read, _, err := store.RunChildRead(ctx, postgres.ChildReadStart{ParentID: call.ID, Ordinal: 1,
		Tool: "get_account", Scope: "crm:read", ConnectionID: f.Connection.ID, BriefID: brief,
		Arguments: json.RawMessage(`{"account_id": "acc-003"}`), Lease: time.Minute,
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
	return postgres.CardPublication{WorkspaceID: f.Workspace.ID, AccountRef: Account, BriefID: brief,
		ParentReceiptID: call.ID, PrincipalID: principal, SealReceiptID: call.ID, SealLeaseToken: call.LeaseToken,
		Kind: "go", SchemaVersion: 3, RulesVersion: &rules,
		Bytes: CardBytes(t, call, Account, brief, []postgres.Receipt{read}, records, units, gaps), Units: units,
		Gaps: gaps, ReadReceiptIDs: []string{read.ID}}, call, read
}

// New migrates a fresh database and publishes the fixture card on it.
func New(t testing.TB) *Fixture {
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
	f.Card, err = store.PublishCard(ctx, pub)
	must(t, err)
	f.Parent, err = store.Receipt(ctx, f.Workspace.ID, call.ID)
	must(t, err)
	f.Read, err = store.Receipt(ctx, f.Workspace.ID, read.ID)
	must(t, err)
	f.Seal, f.SealLease = f.Parent, call.LeaseToken
	return f
}

func ptr(n int) *int { return &n }

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
