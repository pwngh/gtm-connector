package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
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

// A grant on one account opens no other. The outsider holds acc-004, with a
// card published there, and asks for acc-003's card, units, receipts and
// card list by their IDs: each is refused, with one grant and again after
// the second of two grants is revoked.
func TestAGrantOnOneAccountOpensNoOther(t *testing.T) {
	f := postgrestest.New(t)
	own := postgrestest.Publish(t, f, "acc-004", f.Outsider.ID, "brief-own")
	outsider := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Outsider.ID}
	refused := func(t *testing.T) {
		t.Helper()
		for name, call := range map[string]func() error{
			"card": func() error { _, err := f.Store.ViewCard(ctx, outsider, f.Card.ID); return err },
			"unit 1": func() error {
				_, _, _, err := f.Store.ViewUnit(ctx, outsider, f.Card.ID, 1)
				return err
			},
			"unit 2": func() error {
				_, _, _, err := f.Store.ViewUnit(ctx, outsider, f.Card.ID, 2)
				return err
			},
			"read receipt": func() error { _, err := f.Store.ViewReceipt(ctx, outsider, f.Read.ID); return err },
			"seal receipt": func() error { _, err := f.Store.ViewReceipt(ctx, outsider, f.Seal.ID); return err },
			"card list":    func() error { _, err := f.Store.ViewAccountCards(ctx, outsider, postgrestest.Account); return err },
		} {
			if err := call(); !errors.Is(err, postgres.ErrNotPermitted) {
				t.Errorf("acc-003's %s: %v", name, err)
			}
		}
		// Its own account still reads, and lists only its own card.
		if _, err := f.Store.ViewCard(ctx, outsider, own.Card.ID); err != nil {
			t.Errorf("its own card: %v", err)
		}
		if _, err := f.Store.ViewReceipt(ctx, outsider, own.Read.ID); err != nil {
			t.Errorf("its own read: %v", err)
		}
		cards, err := f.Store.ViewAccountCards(ctx, outsider, "acc-004")
		if err != nil || len(cards) != 1 || cards[0].ID != own.Card.ID {
			t.Errorf("its own card list: %+v %v", cards, err)
		}
		accounts, err := f.Store.ViewAccounts(ctx, outsider)
		if err != nil || len(accounts) != 1 || accounts[0].Ref != "acc-004" {
			t.Errorf("its accounts: %+v %v", accounts, err)
		}
	}
	t.Run("one grant", refused)

	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, f.Outsider.ID, postgrestest.Account))
	if _, err := f.Store.ViewCard(ctx, outsider, f.Card.ID); err != nil {
		t.Fatalf("with both grants: %v", err)
	}
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Outsider.ID, postgrestest.Account))
	t.Run("one of two grants revoked", refused)
}

// A disabled principal reads nothing, has no live session and cannot be
// found for sign-in, though its grants and sessions are still there.
func TestADisabledPrincipalReadsNothing(t *testing.T) {
	f := postgrestest.New(t)
	digest := sha256.Sum256([]byte("demo-password"))
	p, err := f.Store.EnsureDemoPrincipal(ctx, f.Workspace, "demo-disabled", "Demo", digest[:])
	ok(t, err)
	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, p.ID, postgrestest.Account))
	_, err = f.Store.CreateSession(ctx, id(7), p, time.Hour)
	ok(t, err)
	viewer := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: p.ID}
	reads := map[string]func() error{
		"card":    func() error { _, err := f.Store.ViewCard(ctx, viewer, f.Card.ID); return err },
		"unit":    func() error { _, _, _, err := f.Store.ViewUnit(ctx, viewer, f.Card.ID, 1); return err },
		"receipt": func() error { _, err := f.Store.ViewReceipt(ctx, viewer, f.Read.ID); return err },
		"card list": func() error {
			_, err := f.Store.ViewAccountCards(ctx, viewer, postgrestest.Account)
			return err
		},
		"accounts":   func() error { _, err := f.Store.ViewAccounts(ctx, viewer); return err },
		"session":    func() error { _, err := f.Store.LiveSession(ctx, id(7)); return err },
		"sign-in":    func() error { _, _, err := f.Store.DemoPrincipalByLogin(ctx, "demo-disabled"); return err },
		"its record": func() error { _, err := f.Store.DemoPrincipal(ctx, "demo-disabled"); return err },
	}
	for name, read := range reads {
		if err := read(); err != nil {
			t.Fatalf("%s before disabling: %v", name, err)
		}
	}
	ok(t, f.Store.DisablePrincipal(ctx, f.Workspace.ID, p.ID))
	for name, want := range map[string]error{"card": postgres.ErrNotPermitted, "unit": postgres.ErrNotPermitted,
		"receipt": postgres.ErrNotPermitted, "card list": postgres.ErrNotPermitted,
		"accounts": postgres.ErrNotPermitted, "session": postgres.ErrNotFound, "sign-in": postgres.ErrNotFound,
		"its record": postgres.ErrDisabled} {
		if err := reads[name](); !errors.Is(err, want) {
			t.Errorf("%s after disabling: %v, want %v", name, err, want)
		}
	}
	if count(t, f, "SELECT count(*) FROM account_grants WHERE principal_id = $1 AND revoked_at IS NULL", p.ID) != 1 ||
		count(t, f, "SELECT count(*) FROM sessions WHERE principal_id = $1 AND revoked_at IS NULL", p.ID) != 1 {
		t.Fatal("disabling changed the grant or the session; the checks above must hold on their own")
	}
	ok(t, f.Store.DisablePrincipal(ctx, f.Workspace.ID, p.ID))
	is(t, f.Store.DisablePrincipal(ctx, f.Workspace.ID, "00000000-0000-4000-8000-000000000000"), postgres.ErrNotFound)
}

// RF-05: a source that was not read is stored and read back as not read,
// with an unknown count, never a zero, and the oracle's text.
func TestAGapNotReadKeepsItsUnknownCount(t *testing.T) {
	f := postgrestest.New(t)
	want := postgrestest.OracleGaps(t, "calls_denied", "slack_not_requested")
	published := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-not-read", want...)
	view, err := f.Store.ViewCard(ctx, postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID},
		published.Card.ID)
	ok(t, err)
	if len(view.Gaps) != len(want) {
		t.Fatalf("gaps %+v", view.Gaps)
	}
	for i, g := range view.Gaps {
		if g.Code != want[i].Code || g.Disposition != "not_read" || g.Count != nil || g.ByRole != nil ||
			g.Text != want[i].Text || want[i].Count != nil {
			t.Errorf("gap %d read back as %+v, want %+v", i, g, want[i])
		}
	}
	if n := count(t, f, `SELECT count(*) FROM gaps WHERE card_id = $1 AND disposition = 'not_read'
		AND count IS NULL AND by_role IS NULL`, published.Card.ID); n != 2 {
		t.Fatalf("%d gaps stored as not read with no count", n)
	}
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
