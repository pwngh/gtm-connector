package postgres_test

import (
	"context"
	"testing"
	"time"

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

// Listing the accounts holds a revocation of a listed grant off until the
// list is read.
func TestViewAccountsHoldsOffARevocation(t *testing.T) {
	f := postgrestest.New(t)
	paused, release := make(chan struct{}), make(chan struct{})
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "view:accounts" {
			close(paused)
			<-release
		}
		return nil
	})
	defer postgres.SetCrash(f.Store, nil)
	listed := make(chan error, 1)
	go func() {
		_, err := f.Store.ViewAccounts(context.Background(),
			postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID})
		listed <- err
	}()
	<-paused
	other, err := postgres.Open(ctx, f.URL)
	ok(t, err)
	defer other.Close()
	revoked := make(chan error, 1)
	go func() {
		revoked <- other.RevokeAccountGrant(context.Background(), f.Workspace.ID, f.Reader.ID, postgrestest.Account)
	}()
	select {
	case err := <-revoked:
		close(release)
		t.Fatalf("the revocation committed while the list was read: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	ok(t, <-listed)
	ok(t, <-revoked)
}
