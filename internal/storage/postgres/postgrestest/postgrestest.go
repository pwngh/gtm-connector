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
	// Parent is the brief's receipt, Read its child read, Seal the card's seal.
	Parent, Read, Seal postgres.Receipt
}

// Account is the fixture's account.
const Account = "acc-003"

// CardBytes is a card document whose "units" and "gaps" are exactly these,
// as PublishCard requires.
func CardBytes(text string, units []postgres.Unit, gaps []postgres.Gap) []byte {
	doc := map[string]any{"schema_version": 3, "text": text, "units": []json.RawMessage{}, "gaps": []any{}}
	bodies := make([]json.RawMessage, len(units))
	for _, u := range units {
		bodies[u.N-1] = u.Body
	}
	doc["units"] = bodies
	list := make([]any, len(gaps))
	for i, g := range gaps {
		var byRole any
		if len(g.ByRole) > 0 {
			byRole = g.ByRole
		}
		list[i] = map[string]any{"code": g.Code, "disposition": g.Disposition, "count": g.Count,
			"by_role": byRole, "text": g.Text}
	}
	doc["gaps"] = list
	out, _ := json.Marshal(doc)
	return out
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

	f.Parent, err = store.StartReceipt(ctx, postgres.ReceiptStart{
		WorkspaceID: f.Workspace.ID, OperationID: "fixture-brief", PrincipalID: f.Reader.ID,
		TokenID: postgres.TokenID(""), Tool: postgres.Composite, AccountRef: Account,
		Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: time.Minute})
	must(t, err)
	parentToken := f.Parent.LeaseToken
	f.Read, _, err = store.RunChildRead(ctx, postgres.ChildReadStart{
		ParentID: f.Parent.ID, Ordinal: 1, Tool: "get_account", Scope: "crm:read",
		ConnectionID: f.Connection.ID, Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: time.Minute,
	}, func(context.Context) ([]postgres.RecordVersion, error) {
		return []postgres.RecordVersion{
			{Source: "crm", Table: "accounts", RecordID: Account, Body: []byte(`{"id":"acc-003","name":"Invented account"}`)},
			{Source: "crm", Table: "contacts", RecordID: "con-001", Body: []byte(`{"id":"con-001"}`)},
		}, nil
	})
	must(t, err)
	versions := map[string]string{}
	rows, err := store.ReceiptRecordIDs(ctx, f.Workspace.ID, f.Read.ID)
	must(t, err)
	for ref, id := range rows {
		versions[ref] = id
	}
	f.Seal, err = store.StartReceipt(ctx, postgres.ReceiptStart{
		WorkspaceID: f.Workspace.ID, OperationID: "fixture-seal", PrincipalID: f.Reader.ID,
		TokenID: postgres.TokenID(""), Tool: "save_brief", AccountRef: Account, BriefID: "brief-1",
		Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"brief-1"}`), Lease: time.Minute})
	must(t, err)
	units := []postgres.Unit{
		{N: 1, Kind: "record", RecordRef: "crm/accounts/acc-003", Tool: "get_account", ReceiptID: f.Read.ID,
			SourceRecordID: versions["crm/accounts/acc-003"],
			Body:           UnitBody(1, "record", "crm/accounts/acc-003", "get_account", "", nil)},
		{N: 2, Kind: "sentence", RecordRef: "crm/contacts/con-001", Tool: "get_account", Shown: "yes",
			ReceiptID: f.Read.ID, SourceRecordID: versions["crm/contacts/con-001"],
			Body: UnitBody(2, "sentence", "crm/contacts/con-001", "get_account", "yes",
				map[string]any{"text": "We agree."})},
	}
	gaps := []postgres.Gap{{Code: "neutralized", Disposition: "note", Count: ptr(1),
		Text: "1 sentence had markup neutralized."}}
	rules := "3"
	f.Card, err = store.PublishCard(ctx, postgres.CardPublication{
		WorkspaceID: f.Workspace.ID, AccountRef: Account, BriefID: "brief-1", ParentReceiptID: f.Parent.ID,
		SealReceiptID: f.Seal.ID, SealLeaseToken: f.Seal.LeaseToken, Kind: "go", SchemaVersion: 3,
		RulesVersion: &rules, Bytes: CardBytes("DEAL CARD acc-003\n", units, gaps), Units: units, Gaps: gaps,
	})
	must(t, err)
	f.Parent, err = store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: f.Parent.ID,
		LeaseToken: parentToken, Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/brief-1"}})
	must(t, err)
	f.Seal, err = store.Receipt(ctx, f.Workspace.ID, f.Seal.ID)
	must(t, err)
	return f
}

func ptr(n int) *int { return &n }

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
