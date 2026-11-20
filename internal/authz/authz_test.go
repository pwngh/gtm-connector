package authz_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

var ctx = context.Background()

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func TestGrantsGuardEveryUnitAndReceiptRead(t *testing.T) {
	f := postgrestest.New(t)
	evidence := authz.Evidence{Store: f.Store}
	reader := authz.Principal{WorkspaceID: f.Workspace.ID, ID: f.Reader.ID}
	outsider := authz.Principal{WorkspaceID: f.Workspace.ID, ID: f.Outsider.ID}

	view, err := evidence.Card(ctx, reader, f.Card.ID)
	if err != nil || len(view.Units) != 2 || len(view.Gaps) != 1 {
		t.Fatalf("granted card: %v", err)
	}
	unit, err := evidence.Unit(ctx, reader, f.Card.ID, 2)
	if err != nil || unit.Receipt == nil || unit.Receipt.ID != f.Read.ID {
		t.Fatalf("granted unit: %+v %v", unit, err)
	}
	if _, err := evidence.Receipt(ctx, reader, f.Seal.ID); err != nil {
		t.Fatal(err)
	}
	if cards, err := evidence.AccountCards(ctx, reader, postgrestest.Account); err != nil || len(cards) != 1 {
		t.Fatalf("granted cards: %v", err)
	}

	// An ungranted principal gets neither a unit nor its receipt, and a
	// denial is never an empty result.
	_, err = evidence.Card(ctx, outsider, f.Card.ID)
	is(t, err, authz.ErrNotPermitted)
	_, err = evidence.Unit(ctx, outsider, f.Card.ID, 2)
	is(t, err, authz.ErrNotPermitted)
	for _, receipt := range []string{f.Read.ID, f.Seal.ID, f.Parent.ID} {
		_, err = evidence.Receipt(ctx, outsider, receipt)
		is(t, err, authz.ErrNotPermitted)
	}
	cards, err := evidence.AccountCards(ctx, outsider, postgrestest.Account)
	is(t, err, authz.ErrNotPermitted)
	if cards != nil {
		t.Fatal("a denial came with a list")
	}

	// A revoked grant reads nothing.
	if err := f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account); err != nil {
		t.Fatal(err)
	}
	_, err = evidence.Unit(ctx, reader, f.Card.ID, 1)
	is(t, err, authz.ErrNotPermitted)
	_, err = evidence.Receipt(ctx, reader, f.Read.ID)
	is(t, err, authz.ErrNotPermitted)
}

func TestOtherWorkspacesAndMissingThings(t *testing.T) {
	f := postgrestest.New(t)
	evidence := authz.Evidence{Store: f.Store}
	other, err := f.Store.EnsureWorkspace(ctx, "other", "demo", "Other")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := f.Store.CreatePrincipal(ctx, other, "person", "Stranger")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Store.EnsureAccount(ctx, other.ID, postgrestest.Account, "Same ref, other workspace"); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.GrantAccount(ctx, other.ID, stranger.ID, postgrestest.Account); err != nil {
		t.Fatal(err)
	}
	// The same account ref granted in another workspace opens nothing here.
	p := authz.Principal{WorkspaceID: other.ID, ID: stranger.ID}
	_, err = evidence.Card(ctx, p, f.Card.ID)
	is(t, err, authz.ErrNotPermitted)
	_, err = evidence.Receipt(ctx, p, f.Read.ID)
	is(t, err, authz.ErrNotPermitted)
	if cards, err := evidence.AccountCards(ctx, p, postgrestest.Account); err != nil || len(cards) != 0 {
		t.Fatalf("other workspace's cards: %v", err)
	}
	if !errors.Is(authz.ErrNotPermitted, postgres.ErrNotPermitted) || authz.ErrNotPermitted != postgres.ErrNotPermitted {
		t.Fatal("two refusals")
	}

	reader := authz.Principal{WorkspaceID: f.Workspace.ID, ID: f.Reader.ID}
	_, err = evidence.Card(ctx, reader, "not-a-uuid")
	is(t, err, authz.ErrNotPermitted)
	_, err = evidence.Unit(ctx, reader, f.Card.ID, 99)
	is(t, err, authz.ErrNotPermitted)
	_, err = evidence.AccountCards(ctx, reader, "acc-999")
	is(t, err, authz.ErrNotPermitted)

	// A receipt about no account is not for the browser.
	owner, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "owner-audit", TokenID: "anonymous", Tool: "audit", Arguments: json.RawMessage(`{}`),
		Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evidence.Receipt(ctx, reader, owner.ID)
	is(t, err, authz.ErrNotPermitted)
}

// A card whose unit rests on a read of another account is refused whole,
// even if such a row got in around the write path.
func TestAUnitOnAnotherAccountsReadClosesTheCard(t *testing.T) {
	f := postgrestest.New(t)
	if err := f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Other"); err != nil {
		t.Fatal(err)
	}
	brief, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "other-brief", TokenID: "anonymous", Tool: postgres.Composite, AccountRef: "acc-004",
		Arguments: json.RawMessage(`{}`), Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	read, _, err := f.Store.RunChildRead(ctx, postgres.ChildReadStart{ParentID: brief.ID, Ordinal: 1, Tool: "get_account",
		Scope: "crm:read", ConnectionID: f.Connection.ID, Arguments: json.RawMessage(`{}`), Lease: time.Minute},
		func(context.Context) ([]postgres.RecordVersion, error) {
			return []postgres.RecordVersion{{Source: "crm", Table: "accounts", RecordID: "acc-004", Body: []byte("{}")}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, statement := range []string{
		"ALTER TABLE evidence_units DISABLE TRIGGER USER",
		`INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, receipt_id, body)
		 VALUES ('` + f.Workspace.ID + `', '` + f.Card.ID + `', 3, 'record', 'crm/accounts/acc-004', 'get_account', '` +
			read.ID + `', '{}')`,
		"ALTER TABLE evidence_units ENABLE TRIGGER USER",
	} {
		if _, err := conn.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	reader := authz.Principal{WorkspaceID: f.Workspace.ID, ID: f.Reader.ID}
	_, err = authz.Evidence{Store: f.Store}.Card(ctx, reader, f.Card.ID)
	is(t, err, authz.ErrNotPermitted)
	_, err = authz.Evidence{Store: f.Store}.Unit(ctx, reader, f.Card.ID, 3)
	is(t, err, authz.ErrNotPermitted)
}
