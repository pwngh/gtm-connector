package postgres_test

import (
	"bytes"
	"crypto/sha256"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

func id(b byte) []byte {
	sum := sha256.Sum256([]byte{b})
	return sum[:]
}

func TestSessionsRotateExpireAndRevoke(t *testing.T) {
	f := postgrestest.New(t)
	digest := sha256.Sum256([]byte("password"))
	demo, err := f.Store.EnsureDemoPrincipal(ctx, f.Workspace, "demo-reviewer", "Demo reviewer", digest[:])
	ok(t, err)
	other := sha256.Sum256([]byte("other"))
	_, err = f.Store.EnsureDemoPrincipal(ctx, f.Workspace, "demo-reviewer", "Demo reviewer", other[:])
	is(t, err, postgres.ErrCredentialMismatch)

	// A pre-session signs in once.
	pre := postgres.Admission{PreSession: true, IDSHA256: id(1), ExpiresAt: time.Now().Add(time.Hour)}
	_, err = f.Store.SignIn(ctx, pre, id(2), demo, time.Hour)
	ok(t, err)
	_, err = f.Store.SignIn(ctx, pre, id(9), demo, time.Hour)
	is(t, err, postgres.ErrSessionUsed)
	expired := postgres.Admission{PreSession: true, IDSHA256: id(8), ExpiresAt: time.Now().Add(-time.Second)}
	_, err = f.Store.SignIn(ctx, expired, id(9), demo, time.Hour)
	is(t, err, postgres.ErrSessionUsed)
	signed, err := f.Store.LiveSession(ctx, id(2))
	ok(t, err)
	if signed.Principal == nil || signed.Principal.ID != demo.ID || signed.Principal.WorkspaceKind != "demo" {
		t.Fatalf("session %+v", signed)
	}
	// Signing in again from a live session consumes it.
	_, err = f.Store.SignIn(ctx, postgres.Admission{IDSHA256: id(2)}, id(4), demo, time.Hour)
	ok(t, err)
	_, err = f.Store.LiveSession(ctx, id(2))
	is(t, err, postgres.ErrNotFound)
	_, err = f.Store.SignIn(ctx, postgres.Admission{IDSHA256: id(2)}, id(5), demo, time.Hour)
	is(t, err, postgres.ErrSessionUsed)

	_, err = f.Store.CreateSession(ctx, id(3), demo, 200*time.Millisecond)
	ok(t, err)
	time.Sleep(300 * time.Millisecond)
	_, err = f.Store.LiveSession(ctx, id(3))
	is(t, err, postgres.ErrNotFound)

	n, err := f.Store.RevokePrincipalSessions(ctx, f.Workspace.ID, demo.ID)
	ok(t, err)
	if n != 2 {
		t.Fatalf("revoked %d", n)
	}
	_, err = f.Store.LiveSession(ctx, id(4))
	is(t, err, postgres.ErrNotFound)

	// Only demo people in a demo workspace may sign in with a password.
	live, err := f.Store.EnsureWorkspace(ctx, "live", "live", "Live workspace")
	ok(t, err)
	if _, err := f.Store.EnsureDemoPrincipal(ctx, live, "live-person", "Person", digest[:]); code(err) != "23514" {
		t.Fatalf("a live workspace got a password principal: %v", err)
	}
	if _, err := f.Store.EnsureWorkspace(ctx, "live", "demo", "Live workspace"); err == nil {
		t.Fatal("a workspace changed kind")
	}
}

type memoryFile struct{ *bytes.Buffer }

func (memoryFile) Close() error { return nil }

func TestDumpAndRestoreKeepState(t *testing.T) {
	f := postgrestest.New(t)
	files := map[string]*bytes.Buffer{}
	archive := strings.Repeat("b", 64)
	applied, copies, err := f.Store.Dump(ctx, func(table string) (io.WriteCloser, error) {
		files[table] = &bytes.Buffer{}
		return memoryFile{files[table]}, nil
	})
	ok(t, err)

	fresh, err := postgres.Open(ctx, pgtest.URL(t))
	ok(t, err)
	defer fresh.Close()
	open := func(table string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(files[table].Bytes())), nil
	}
	ok(t, fresh.Restore(ctx, archive, applied, copies, open))

	card, err := fresh.Card(ctx, f.Workspace.ID, f.Card.ID)
	ok(t, err)
	if !bytes.Equal(card.Bytes, f.Card.Bytes) || !bytes.Equal(card.SHA256, f.Card.SHA256) {
		t.Fatal("the card changed")
	}
	seal, err := fresh.Receipt(ctx, f.Workspace.ID, f.Seal.ID)
	ok(t, err)
	if seal.Status != "ok" || !seal.StartedAt.Equal(f.Seal.StartedAt) || seal.Rows[0] != f.Seal.Rows[0] {
		t.Fatalf("seal %+v", seal)
	}
	units, err := fresh.Units(ctx, f.Workspace.ID, f.Card.ID)
	ok(t, err)
	versions, err := fresh.ReceiptRecords(ctx, f.Workspace.ID, f.Read.ID)
	ok(t, err)
	granted, err := fresh.HasAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account)
	ok(t, err)
	if len(units) != 2 || len(versions) != 2 || !granted {
		t.Fatal("state was lost")
	}
	// The triggers are back on after the restore.
	_, err = fresh.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: f.Seal.ID, LeaseToken: f.SealLease,
		Status: "error", HTTPStatus: 500})
	is(t, err, postgres.ErrAlreadyFinished)

	// A database that is not empty is refused.
	if err := fresh.Restore(ctx, archive, applied, copies, open); err == nil {
		t.Fatal("restored over existing data")
	}
	// So is a dump whose row counts do not match.
	other, err := postgres.Open(ctx, pgtest.URL(t))
	ok(t, err)
	defer other.Close()
	copies[0].Rows++
	if err := other.Restore(ctx, archive, applied, copies, open); err == nil {
		t.Fatal("a short table was accepted")
	}
	// A failed restore leaves the database empty, so it can be run again.
	if v, err := other.AppliedMigrations(ctx); err != nil || len(v) != 0 {
		t.Fatalf("migrations after a failed restore: %v %v", v, err)
	}
	copies[0].Rows--
	ok(t, other.Restore(ctx, archive, applied, copies, open))
}
