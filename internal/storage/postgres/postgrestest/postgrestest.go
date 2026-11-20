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
		TokenID: postgres.TokenID(""), Tool: "brief", AccountRef: Account,
		Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: time.Minute})
	must(t, err)
	f.Read, _, err = store.RunChildRead(ctx, postgres.ChildReadStart{
		ParentID: f.Parent.ID, Ordinal: 0, Tool: "get_account", Scope: "crm:read",
		ConnectionID: f.Connection.ID, Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: time.Minute,
	}, func(context.Context) ([]postgres.RecordVersion, error) {
		return []postgres.RecordVersion{
			{Source: "crm", Table: "accounts", RecordID: Account, Body: []byte(`{"id":"acc-003","name":"Invented account"}`)},
			{Source: "calls", Table: "calls", RecordID: "call-1", Body: []byte(`{"id":"call-1"}`)},
		}, nil
	})
	must(t, err)
	f.Seal, err = store.StartReceipt(ctx, postgres.ReceiptStart{
		WorkspaceID: f.Workspace.ID, OperationID: "fixture-seal", PrincipalID: f.Reader.ID,
		TokenID: postgres.TokenID(""), Tool: "save_brief", AccountRef: Account, BriefID: "brief-1",
		Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"brief-1"}`), Lease: time.Minute})
	must(t, err)
	rules := "3"
	f.Card, err = store.PublishCard(ctx, postgres.CardPublication{
		WorkspaceID: f.Workspace.ID, AccountRef: Account, BriefID: "brief-1", PrincipalID: f.Reader.ID,
		SealReceiptID: f.Seal.ID, SealLeaseToken: f.Seal.LeaseToken, Kind: "go", SchemaVersion: 3,
		RulesVersion: &rules, Bytes: []byte(`{"schema_version":3,"text":"DEAL CARD acc-003\n"}`),
		Units: []postgres.Unit{
			{N: 1, Kind: "record", RecordRef: "crm/accounts/acc-003", Tool: "get_account", ReceiptID: f.Read.ID,
				Body: json.RawMessage(`{"n":1,"kind":"record","record_ref":"crm/accounts/acc-003"}`)},
			{N: 2, Kind: "sentence", RecordRef: "calls/calls/call-1", Tool: "get_account", Shown: "yes",
				ReceiptID: f.Read.ID, Body: json.RawMessage(`{"n":2,"kind":"sentence","text":"We agree."}`)},
		},
		Gaps: []postgres.Gap{{Code: "neutralized", Disposition: "note", Count: ptr(1),
			Text: "1 sentence had markup neutralized."}},
		ReadReceiptIDs: []string{f.Read.ID},
	})
	must(t, err)
	f.Parent, err = store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: f.Parent.ID,
		LeaseToken: f.Parent.LeaseToken, Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/brief-1"}})
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
