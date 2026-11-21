package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

var ctx = context.Background()

// sql runs one statement on its own connection, as another writer would.
func sql(t *testing.T, f *postgrestest.Fixture, statement string, args ...any) error {
	t.Helper()
	conn, err := pgx.Connect(ctx, f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, statement, args...)
	return err
}

func count(t *testing.T, f *postgrestest.Fixture, query string, args ...any) int {
	t.Helper()
	conn, err := pgx.Connect(ctx, f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func code(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

// parent starts a pending prepare_renewal_brief receipt for the fixture's account.
func parent(t *testing.T, f *postgrestest.Fixture, operation string, lease time.Duration) postgres.Receipt {
	t.Helper()
	r, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: operation, PrincipalID: f.Reader.ID, TokenID: postgres.TokenID("raw-token"),
		Tool: postgres.Composite, AccountRef: postgrestest.Account, Arguments: json.RawMessage(`{"account_id":"acc-003"}`),
		Lease: lease})
	ok(t, err)
	return r
}

func child(t *testing.T, f *postgrestest.Fixture, p postgres.Receipt, ordinal int, lease time.Duration) postgres.Receipt {
	t.Helper()
	r, err := f.Store.StartChildRead(ctx, childStart(f, p, ordinal, lease))
	ok(t, err)
	return r
}

func childStart(f *postgrestest.Fixture, p postgres.Receipt, ordinal int, lease time.Duration) postgres.ChildReadStart {
	return postgres.ChildReadStart{ParentID: p.ID, Ordinal: ordinal, Tool: "get_account", Scope: "crm:read",
		ConnectionID: f.Connection.ID, Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: lease}
}

func records(tag string) []postgres.RecordVersion {
	return []postgres.RecordVersion{
		{Source: "crm", Table: "accounts", RecordID: "acc-003", Body: []byte(`{"v":"` + tag + `"}`)},
		{Source: "crm", Table: "contacts", RecordID: "con-001", Body: []byte(`{"c":"` + tag + `"}`)},
	}
}

func finishOK(r postgres.Receipt, rows ...string) postgres.ReceiptFinish {
	return postgres.ReceiptFinish{ReceiptID: r.ID, LeaseToken: r.LeaseToken, Status: "ok", HTTPStatus: 200, Rows: rows}
}

func TestMigrateIsRepeatableAndRefusesAChangedFile(t *testing.T) {
	f := postgrestest.New(t)
	ok(t, f.Store.Migrate(ctx))
	version, err := f.Store.SchemaVersion(ctx)
	ok(t, err)
	if version != postgres.LatestSchemaVersion() {
		t.Fatalf("schema %d", version)
	}
	ok(t, sql(t, f, "UPDATE schema_migrations SET sha256 = 'changed' WHERE version = 1"))
	if err := f.Store.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a changed migration was accepted: %v", err)
	}
}

func TestReceiptFinishesOnce(t *testing.T) {
	f := postgrestest.New(t)
	r := parent(t, f, "once", time.Minute)
	if r.Status != "pending" || r.TokenID == "anonymous" || r.PrincipalID != f.Reader.ID {
		t.Fatalf("started %+v", r)
	}
	done, err := f.Store.FinishReceipt(ctx, finishOK(r, "x"))
	ok(t, err)
	if done.Status != "ok" || len(done.Rows) != 1 || done.FinishedAt == nil {
		t.Fatalf("finished %+v", done)
	}
	// The same outcome again is a retry after a lost acknowledgement.
	again, err := f.Store.FinishReceipt(ctx, finishOK(r, "x"))
	ok(t, err)
	if again.ID != r.ID || *again.FinishedAt != *done.FinishedAt {
		t.Fatal("a repeated finish changed the receipt")
	}
	_, err = f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: r.ID, LeaseToken: r.LeaseToken,
		Status: "error", HTTPStatus: 403, Error: "denied"})
	is(t, err, postgres.ErrAlreadyFinished)

	// A writer that skips the guarded UPDATE meets the trigger.
	err = sql(t, f, "UPDATE receipts SET status = 'error', rows = '[]', row_count = 0 WHERE id = $1", r.ID)
	if code(err) != "GT001" {
		t.Fatalf("direct update of a finished receipt: %v", err)
	}
	if code(sql(t, f, "DELETE FROM receipts WHERE id = $1", r.ID)) != "GT010" {
		t.Fatal("a receipt was deleted")
	}
	if code(sql(t, f, "TRUNCATE receipts CASCADE")) != "GT010" {
		t.Fatal("receipts were truncated")
	}
	// An error finishes with no rows.
	bad := parent(t, f, "error-rows", time.Minute)
	if _, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: bad.ID, LeaseToken: bad.LeaseToken,
		Status: "error", HTTPStatus: 500, Rows: []string{"x"}}); err == nil {
		t.Fatal("an error receipt took rows")
	}
	if code(sql(t, f, `UPDATE receipts SET status = 'error', http_status = 500, rows = '["x"]', row_count = 1,
		finished_at = now(), finishing_lease = lease_token WHERE id = $1`, bad.ID)) != "23514" {
		t.Fatal("the table accepted an error receipt with rows")
	}
}

func TestTwoFinishersRace(t *testing.T) {
	f := postgrestest.New(t)
	for round := range 20 {
		r := parent(t, f, fmt.Sprintf("race-%d", round), time.Minute)
		var wins, refusals atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := f.Store.FinishReceipt(ctx, finishOK(r, fmt.Sprintf("finisher-%d", i)))
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, postgres.ErrAlreadyFinished):
					refusals.Add(1)
				default:
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 || refusals.Load() != 5 {
			t.Fatalf("round %d: %d finishers won, %d refused", round, wins.Load(), refusals.Load())
		}
	}
	// Two FinishRead calls racing bind one set of versions.
	p := parent(t, f, "race-read", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records(fmt.Sprint(i)), 1); err == nil {
				wins.Add(1)
			} else if !errors.Is(err, postgres.ErrAlreadyFinished) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || count(t, f, "SELECT count(*) FROM receipt_records WHERE receipt_id = $1", c.ID) != 2 {
		t.Fatalf("%d readers won", wins.Load())
	}
}

func TestExpiredLeaseCannotFinish(t *testing.T) {
	f := postgrestest.New(t)
	r := parent(t, f, "lease", 300*time.Millisecond)
	_, err := f.Store.RenewLease(ctx, r.ID, r.LeaseToken, 400*time.Millisecond)
	ok(t, err)
	time.Sleep(600 * time.Millisecond)
	_, err = f.Store.FinishReceipt(ctx, finishOK(r))
	is(t, err, postgres.ErrLeaseLost)
	_, err = f.Store.RenewLease(ctx, r.ID, r.LeaseToken, time.Minute)
	is(t, err, postgres.ErrLeaseLost)
	// Skipping the guard in SQL does not help: the trigger checks the lease.
	err = sql(t, f, `UPDATE receipts SET status = 'ok', http_status = 200, finished_at = now() WHERE id = $1`, r.ID)
	if code(err) != "GT003" {
		t.Fatalf("an expired lease finished a receipt: %v", err)
	}
	// Taking over a live lease is refused too.
	live := parent(t, f, "lease-live", time.Minute)
	if code(sql(t, f, `UPDATE receipts SET lease_token = gen_random_uuid(), lease_fence = lease_fence + 1
		WHERE id = $1`, live.ID)) != "GT004" {
		t.Fatal("a held lease was taken over")
	}

	n, err := f.Store.AbandonExpired(ctx, 10)
	ok(t, err)
	if n != 1 {
		t.Fatalf("abandoned %d", n)
	}
	gone, err := f.Store.Receipt(ctx, f.Workspace.ID, r.ID)
	ok(t, err)
	if gone.Status != "error" || *gone.HTTPStatus != 503 || *gone.Error != "interrupted" || gone.LeaseFence != 2 {
		t.Fatalf("abandoned receipt %+v", gone)
	}
	// The old worker comes back and still cannot finish it: the lease was
	// taken over, so it is told nothing about the outcome.
	_, err = f.Store.FinishReceipt(ctx, finishOK(r))
	is(t, err, postgres.ErrLeaseLost)
}

func TestLostAcknowledgement(t *testing.T) {
	f := postgrestest.New(t)
	first := parent(t, f, "op-lost", time.Minute)
	// The caller never saw first; it retries with the same operation ID.
	retry := parent(t, f, "op-lost", time.Minute)
	if retry.ID != first.ID || retry.LeaseToken != first.LeaseToken {
		t.Fatal("a retried start made a second receipt")
	}
	if count(t, f, "SELECT count(*) FROM receipts WHERE operation_id = 'op-lost'") != 1 {
		t.Fatal("two receipts for one operation")
	}
	_, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "op-lost",
		TokenID: "anonymous", Tool: "other", Arguments: json.RawMessage(`{}`), Lease: time.Minute})
	is(t, err, postgres.ErrOperationReused)

	// A read whose acknowledgement was lost: retried, it gives the same
	// receipt and the versions it bound, without reading again.
	reads := 0
	read := func(context.Context) ([]postgres.RecordVersion, error) { reads++; return records("a"), nil }
	got, versions, err := f.Store.RunChildRead(ctx, childStart(f, first, 1, time.Minute), read)
	ok(t, err)
	again, versionsAgain, err := f.Store.RunChildRead(ctx, childStart(f, first, 1, time.Minute), read)
	ok(t, err)
	if reads != 1 || again.ID != got.ID || len(versionsAgain) != len(versions) ||
		string(versionsAgain[0].Body) != string(versions[0].Body) {
		t.Fatalf("retry read %d times", reads)
	}
	// FinishRead repeated with the same records is the same result.
	held := child(t, f, first, 2, time.Minute)
	done, err := f.Store.FinishRead(ctx, held.ID, held.LeaseToken, records("a"), 1)
	ok(t, err)
	same, err := f.Store.FinishRead(ctx, held.ID, held.LeaseToken, records("a"), 1)
	ok(t, err)
	if same.ID != done.ID || *same.FinishedAt != *done.FinishedAt {
		t.Fatal("a repeated FinishRead changed the receipt")
	}
	_, err = f.Store.FinishRead(ctx, held.ID, held.LeaseToken, records("b")[:1], 1)
	is(t, err, postgres.ErrAlreadyFinished)

	// Committed and delivered are recorded apart.
	byOperation, err := f.Store.ReceiptByOperation(ctx, f.Workspace.ID, got.OperationID)
	ok(t, err)
	if byOperation.Status != "ok" || byOperation.DeliveredAt != nil {
		t.Fatalf("before delivery %+v", byOperation)
	}
	ok(t, f.Store.MarkDelivered(ctx, got.ID))
	ok(t, f.Store.MarkDelivered(ctx, got.ID))
	byOperation, err = f.Store.ReceiptByOperation(ctx, f.Workspace.ID, got.OperationID)
	ok(t, err)
	if byOperation.DeliveredAt == nil {
		t.Fatal("delivery not recorded")
	}
	if err := f.Store.MarkDelivered(ctx, first.ID); err == nil {
		t.Fatal("a pending receipt was marked delivered")
	}
}

// Each step boundary of a source read and a publication, with the process
// stopping there. Nothing half-written survives, and the work can finish.
func TestCrashAtEachStepBoundary(t *testing.T) {
	f := postgrestest.New(t)
	versionsBefore := count(t, f, "SELECT count(*) FROM source_records")

	t.Run("after the child receipt commits, before the read", func(t *testing.T) {
		p := parent(t, f, "crash-1", time.Minute)
		c := child(t, f, p, 1, 200*time.Millisecond)
		time.Sleep(300 * time.Millisecond)
		n, err := f.Store.AbandonExpired(ctx, 10)
		ok(t, err)
		gone, err := f.Store.Receipt(ctx, f.Workspace.ID, c.ID)
		ok(t, err)
		if n < 1 || gone.Status != "error" || *gone.Error != "interrupted" || len(gone.Rows) != 0 {
			t.Fatalf("interrupted read %+v", gone)
		}
		if count(t, f, "SELECT count(*) FROM receipt_records WHERE receipt_id = $1", c.ID) != 0 {
			t.Fatal("an interrupted read bound records")
		}
	})

	for _, point := range []string{"read:locked", "read:record", "read:bound", "read:finished"} {
		t.Run("inside the read's finishing transaction at "+point, func(t *testing.T) {
			p := parent(t, f, "crash-"+point, time.Minute)
			c := child(t, f, p, 1, time.Minute)
			postgres.SetCrash(f.Store, func(at string) error {
				if at == point {
					return errors.New("stopped at " + at)
				}
				return nil
			})
			_, err := f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records(point), 1)
			postgres.SetCrash(f.Store, nil)
			if err == nil {
				t.Fatal("no crash")
			}
			still, err := f.Store.Receipt(ctx, f.Workspace.ID, c.ID)
			ok(t, err)
			if still.Status != "pending" ||
				count(t, f, "SELECT count(*) FROM receipt_records WHERE receipt_id = $1", c.ID) != 0 ||
				count(t, f, "SELECT count(*) FROM source_records") != versionsBefore {
				t.Fatal("a crash left part of a read behind")
			}
			done, err := f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records(point), 1)
			ok(t, err)
			if done.Status != "ok" || len(done.Rows) != 2 {
				t.Fatalf("finished after the crash %+v", done)
			}
			versionsBefore += 2
		})
	}

	t.Run("after the read commits, before the reply is delivered", func(t *testing.T) {
		p := parent(t, f, "crash-delivery", time.Minute)
		reads := 0
		read := func(context.Context) ([]postgres.RecordVersion, error) {
			reads++
			return records("delivery"), nil
		}
		r, _, err := f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute), read)
		ok(t, err)
		if r.DeliveredAt != nil {
			t.Fatal("delivered before anyone said so")
		}
		again, versions, err := f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute), read)
		ok(t, err)
		if reads != 1 || again.ID != r.ID || len(versions) != 2 {
			t.Fatal("recovery read the source again")
		}
	})

	for _, point := range []string{"publish:checked", "publish:written", "publish:sealed"} {
		t.Run("inside the publishing transaction at "+point, func(t *testing.T) {
			publication, seal := publication(t, f, "pub-"+strings.ReplaceAll(point, ":", "-"))
			cards := count(t, f, "SELECT count(*) FROM cards")
			postgres.SetCrash(f.Store, func(at string) error {
				if at == point {
					return errors.New("stopped at " + at)
				}
				return nil
			})
			_, err := f.Store.PublishCard(ctx, publication)
			postgres.SetCrash(f.Store, nil)
			if err == nil {
				t.Fatal("no crash")
			}
			pending, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
			ok(t, err)
			if pending.Status != "pending" || count(t, f, "SELECT count(*) FROM cards") != cards ||
				count(t, f, "SELECT count(*) FROM evidence_units WHERE card_id NOT IN (SELECT id FROM cards)") != 0 {
				t.Fatal("a crash left part of a card behind")
			}
			card, err := f.Store.PublishCard(ctx, publication)
			ok(t, err)
			sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
			ok(t, err)
			if sealed.Status != "ok" || card.ID == "" ||
				count(t, f, "SELECT count(*) FROM evidence_units WHERE card_id = $1", card.ID) != 1 {
				t.Fatalf("published after the crash: %+v", sealed)
			}
		})
	}
}

// publication starts a prepare_renewal_brief call for brief, which seals
// it, and a read under it, and describes a one-unit card resting on that read.
func publication(t *testing.T, f *postgrestest.Fixture, brief string) (postgres.CardPublication, postgres.Receipt) {
	t.Helper()
	pub, seal, _ := postgrestest.Prepared(t, f.Store, f, brief+"-call", brief, f.Reader.ID,
		postgres.TokenID("raw-token"), postgrestest.Record("accounts", "acc-003", "id", "acc-003", "v", brief))
	return pub, seal
}

func TestUnavailableReceiptStore(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "unavailable", time.Minute)

	down, err := postgres.Open(ctx, f.URL)
	ok(t, err)
	down.Close()
	reads := 0
	_, _, err = down.RunChildRead(ctx, childStart(f, p, 1, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { reads++; return records("x"), nil })
	if err == nil || reads != 0 {
		t.Fatalf("the read ran without a receipt (reads %d, err %v)", reads, err)
	}
	if count(t, f, "SELECT count(*) FROM receipts WHERE parent_id = $1", p.ID) != 0 {
		t.Fatal("a receipt appeared")
	}

	// The store goes away after the receipt commits: the records are not
	// released, and the receipt stays pending until it is abandoned.
	failing, err := postgres.Open(ctx, f.URL)
	ok(t, err)
	_, released, err := failing.RunChildRead(ctx, childStart(f, p, 2, 200*time.Millisecond),
		func(context.Context) ([]postgres.RecordVersion, error) { failing.Close(); return records("y"), nil })
	if err == nil || released != nil {
		t.Fatal("records were released without a finished receipt")
	}
	c, err := f.Store.ReceiptByOperation(ctx, f.Workspace.ID, p.OperationID+"#2")
	ok(t, err)
	if c.Status != "pending" {
		t.Fatalf("receipt %s", c.Status)
	}
	time.Sleep(300 * time.Millisecond)
	_, err = f.Store.AbandonExpired(ctx, 10)
	ok(t, err)
	c, err = f.Store.Receipt(ctx, f.Workspace.ID, c.ID)
	ok(t, err)
	if c.Status != "error" || *c.Error != "interrupted" {
		t.Fatalf("receipt %+v", c)
	}
}

func TestForeignKeysStayInsideOneWorkspace(t *testing.T) {
	f := postgrestest.New(t)
	other, err := f.Store.EnsureWorkspace(ctx, "other", "demo", "Other workspace")
	ok(t, err)
	stranger, err := f.Store.CreatePrincipal(ctx, other, "person", "Other person")
	ok(t, err)
	otherConnection, err := f.Store.EnsureConnection(ctx, other, "crm", "demo")
	ok(t, err)

	if code(f.Store.GrantAccount(ctx, other.ID, stranger.ID, postgrestest.Account)) != "23503" {
		t.Fatal("a grant reached another workspace's account")
	}
	if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, principal_id, token_id, tool, arguments,
		request_id, lease_token, lease_expires_at) VALUES ($1, 'x', $2, 'anonymous', 't', '{}', 'null',
		gen_random_uuid(), now() + interval '1 minute')`, f.Workspace.ID, stranger.ID)) != "23503" {
		t.Fatal("a receipt named another workspace's principal")
	}
	p := parent(t, f, "fk-parent", time.Minute)
	start := childStart(f, p, 1, time.Minute)
	start.ConnectionID = otherConnection.ID
	_, err = f.Store.StartChildRead(ctx, start)
	is(t, err, postgres.ErrNotFound)
	if code(sql(t, f, `INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, receipt_id, body)
		VALUES ($1, $2, 9, 'record', 'crm/accounts/acc-003', 't', $3, '{}')`,
		other.ID, f.Card.ID, f.Read.ID)) != "23503" {
		t.Fatal("a unit joined another workspace's card")
	}
	// Demo data and a real source never share a workspace.
	if code(sql(t, f, `INSERT INTO connections (workspace_id, workspace_kind, source, mode)
		VALUES ($1, 'demo', 'slack', 'slack')`, f.Workspace.ID)) != "23514" {
		t.Fatal("a demo workspace took a real source")
	}
}

func TestOrdinalsAndOperationsAreUnique(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "unique", time.Minute)
	child(t, f, p, 1, time.Minute)
	other := childStart(f, p, 1, time.Minute)
	other.Tool = "get_contacts"
	_, err := f.Store.StartChildRead(ctx, other)
	is(t, err, postgres.ErrOperationReused)
	if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, parent_id, ordinal, principal_id, token_id,
		tool, scope, connection_id, connection_generation, arguments, request_id, lease_token, lease_expires_at)
		VALUES ($1, 'another-op', $2, 1, $4, $5, 't', 'crm:read', $3, 1, '{}', 'null', gen_random_uuid(),
		now() + interval '1 minute')`, f.Workspace.ID, p.ID, f.Connection.ID, p.PrincipalID, p.TokenID)) != "23505" {
		t.Fatal("two children share (parent, ordinal)")
	}
	// An operation ID belongs to its workspace: another workspace's is
	// another receipt, and hands over nothing.
	otherWorkspace, err := f.Store.EnsureWorkspace(ctx, "other", "demo", "Other")
	ok(t, err)
	theirs, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: otherWorkspace.ID,
		OperationID: "unique", TokenID: "anonymous", Tool: "brief", Arguments: json.RawMessage(`{}`),
		Lease: time.Minute})
	ok(t, err)
	if theirs.ID == p.ID || theirs.LeaseToken == p.LeaseToken || theirs.Replay {
		t.Fatalf("another workspace's start under the operation: %+v", theirs)
	}
	if _, err := f.Store.ReceiptByOperation(ctx, otherWorkspace.ID, "unique-never"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal(err)
	}
	mine, err := f.Store.ReceiptByOperation(ctx, f.Workspace.ID, "unique")
	ok(t, err)
	if mine.ID != p.ID || mine.LeaseToken != "" {
		t.Fatalf("by operation: %+v", mine)
	}
	// Only the start's replay gives back the lease.
	again := parent(t, f, "unique", time.Minute)
	if again.ID != p.ID || again.LeaseToken != p.LeaseToken || !again.Replay {
		t.Fatalf("the replay: %+v", again)
	}
	if read, err := f.Store.Receipt(ctx, f.Workspace.ID, p.ID); err != nil || read.LeaseToken != "" {
		t.Fatalf("an owner read handed out the lease: %v", err)
	}
}

func TestParentsAndChildren(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "family", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	if c.PrincipalID != p.PrincipalID || c.TokenID != p.TokenID || c.ConnectionGeneration != f.Connection.Generation ||
		!strings.Contains(string(c.RequestID), `"brief_child"`) {
		t.Fatalf("child %+v", c)
	}
	_, err := f.Store.FinishReceipt(ctx, finishOK(p))
	is(t, err, postgres.ErrPendingChild)
	_, err = f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records("family"), 1)
	ok(t, err)
	_, err = f.Store.FinishReceipt(ctx, finishOK(p))
	ok(t, err)
	_, err = f.Store.StartChildRead(ctx, childStart(f, p, 2, time.Minute))
	is(t, err, postgres.ErrParent)
}

func TestSourceChangeFencesReadsAndPublication(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "fence", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	before := count(t, f, "SELECT count(*) FROM source_records")
	_, err := f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
	ok(t, err)
	r, err := f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records("fence"), 1)
	is(t, err, postgres.ErrSourceChanged)
	if r.Status != "error" || *r.HTTPStatus != 409 || count(t, f, "SELECT count(*) FROM source_records") != before {
		t.Fatalf("fenced read %+v", r)
	}
	_, err = f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records("fence"), 1)
	is(t, err, postgres.ErrSourceChanged)

	publication, seal := publication(t, f, "fenced-card")
	_, err = f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
	ok(t, err)
	_, err = f.Store.PublishCard(ctx, publication)
	is(t, err, postgres.ErrSourceChanged)
	sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
	ok(t, err)
	if sealed.Status != "error" || *sealed.Error != "source_changed" ||
		count(t, f, "SELECT count(*) FROM cards WHERE brief_id = 'fenced-card'") != 0 {
		t.Fatal("a card was published on a changed source")
	}
}

func TestPublicationRechecksTheGrant(t *testing.T) {
	f := postgrestest.New(t)
	publication, seal := publication(t, f, "revoked")
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account))
	_, err := f.Store.PublishCard(ctx, publication)
	is(t, err, postgres.ErrNotPermitted)
	sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
	ok(t, err)
	if sealed.Status != "error" || *sealed.HTTPStatus != 403 {
		t.Fatalf("seal %+v", sealed)
	}
}

// The recheck is of the grant on the card's own account: the actor's live
// grant on another account does not stand in for a revoked one.
func TestPublicationRechecksTheCardsOwnGrant(t *testing.T) {
	f := postgrestest.New(t)
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Other account"))
	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, f.Reader.ID, "acc-004"))
	pub, seal := publication(t, f, "revoked-one-of-two")
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account))
	_, err := f.Store.PublishCard(ctx, pub)
	is(t, err, postgres.ErrNotPermitted)
	sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
	ok(t, err)
	if sealed.Status != "error" || *sealed.HTTPStatus != 403 || *sealed.Error != "not_permitted" {
		t.Fatalf("seal %+v", sealed)
	}
	if count(t, f, "SELECT count(*) FROM cards WHERE brief_id = 'revoked-one-of-two'") != 0 {
		t.Fatal("a card was stored")
	}
}

func TestDurableRowsRefuseChange(t *testing.T) {
	f := postgrestest.New(t)
	for _, statement := range []string{
		"UPDATE source_records SET raw_body = 'x'",
		"DELETE FROM source_records",
		"UPDATE cards SET card_bytes = 'x', card_sha256 = sha256('x')",
		"DELETE FROM cards",
		"UPDATE evidence_units SET body = '{}'",
		"DELETE FROM gaps",
		"UPDATE card_lines SET body = '{}'",
		"DELETE FROM card_lines",
		"UPDATE cards SET line_count = 0",
		"DELETE FROM receipt_records",
	} {
		if code(sql(t, f, statement)) != "GT010" {
			t.Errorf("%s was allowed", statement)
		}
	}
	if code(sql(t, f, "UPDATE source_records SET body_sha256 = sha256('other')")) == "" {
		t.Error("a record's digest was changed")
	}
}

func TestCardSealMustNameItsBytes(t *testing.T) {
	f := postgrestest.New(t)
	pub, seal := publication(t, f, "forged")
	_, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: seal.ID, LeaseToken: seal.LeaseToken,
		Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/forged/" + strings.Repeat("0", 64)}})
	ok(t, err)
	err = sql(t, f, `INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
		card_bytes, card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count, line_count)
		VALUES ($1, 'acc-003', 'forged', 'go', 3, '3', $3, sha256($3), $4, $2, 0, 0, 0, 0)`,
		f.Workspace.ID, seal.ID, []byte(`{"units":[],"gaps":[]}`), pub.ParentReceiptID)
	if code(err) != "GT009" {
		t.Fatalf("a card was stored under a seal for other bytes: %v", err)
	}
	if code(sql(t, f, `INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
		card_bytes, card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count, line_count)
		VALUES ($1, 'acc-003', 'x', 'go', 3, '3', 'x', sha256('y'), $3, $2, 0, 0, 0, 0)`,
		f.Workspace.ID, seal.ID, pub.ParentReceiptID)) != "23514" {
		t.Fatal("a card's digest need not match its bytes")
	}
}

func TestRecordVersions(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "versions", time.Minute)
	versioned := []postgres.RecordVersion{{Source: "crm", Table: "accounts", RecordID: "acc-010",
		ContentVersion: "1700000000.000100", Body: []byte("first")}}
	_, _, err := f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { return versioned, nil })
	ok(t, err)
	versioned[0].Body = []byte("second")
	_, _, err = f.Store.RunChildRead(ctx, childStart(f, p, 2, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { return versioned, nil })
	is(t, err, postgres.ErrVersionConflict)
	// The same version read again binds the stored row; no duplicate.
	versioned[0].Body = []byte("first")
	_, _, err = f.Store.RunChildRead(ctx, childStart(f, p, 3, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { return versioned, nil })
	ok(t, err)
	if count(t, f, "SELECT count(*) FROM source_records WHERE record_id = 'acc-010'") != 1 {
		t.Fatal("one version stored twice")
	}
	// A failed read finishes as an error with no rows.
	r, _, err := f.Store.RunChildRead(ctx, childStart(f, p, 4, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) {
			return nil, &postgres.ReadFailure{HTTPStatus: 403, Code: "calls_denied"}
		})
	if err == nil || r.Status != "error" || *r.HTTPStatus != 403 || len(r.Rows) != 0 {
		t.Fatalf("denied read %+v %v", r, err)
	}
}
