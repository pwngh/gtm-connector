package postgres_test

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

func TestViewAccountsListsLiveGrants(t *testing.T) {
	f := postgrestest.New(t)
	reader := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID}
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-001", "First account"))
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Ungranted account"))
	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, f.Reader.ID, "acc-001"))
	accounts, err := f.Store.ViewAccounts(ctx, reader)
	ok(t, err)
	if len(accounts) != 2 || accounts[0] != (postgres.AccountView{Ref: "acc-001", DisplayName: "First account"}) ||
		accounts[1].Ref != postgrestest.Account {
		t.Fatalf("accounts %+v", accounts)
	}
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, "acc-001"))
	if accounts, err = f.Store.ViewAccounts(ctx, reader); err != nil || len(accounts) != 1 {
		t.Fatalf("after a revocation %+v %v", accounts, err)
	}
	if accounts, err = f.Store.ViewAccounts(ctx, postgres.Viewer{WorkspaceID: f.Workspace.ID,
		PrincipalID: f.Outsider.ID}); err != nil || len(accounts) != 0 {
		t.Fatalf("a principal with no grant %+v %v", accounts, err)
	}
	other, err := f.Store.EnsureWorkspace(ctx, "other", "demo", "Other workspace")
	ok(t, err)
	_, err = f.Store.ViewAccounts(ctx, postgres.Viewer{WorkspaceID: other.ID, PrincipalID: f.Reader.ID})
	is(t, err, postgres.ErrNotPermitted)
	ok(t, sql(t, f, "UPDATE principals SET disabled_at = now() WHERE id = $1", f.Reader.ID))
	_, err = f.Store.ViewAccounts(ctx, reader)
	is(t, err, postgres.ErrNotPermitted)
}
