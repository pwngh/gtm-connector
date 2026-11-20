package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// Regression tests for the G1c review: each names the finding it covers.

// Item 1: every unit's receipt is checked, not only ReadReceiptIDs.
func TestPublishChecksEveryUnitsProvenance(t *testing.T) {
	f := postgrestest.New(t)
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Other account"))

	t.Run("a pending receipt", func(t *testing.T) {
		pub, _ := publication(t, f, "prov-pending")
		pending := child(t, f, parentOf(t, f, pub), 2, time.Minute)
		pub.Units[0].ReceiptID, pub.Units[0].SourceRecordID = pending.ID, ""
		pub.Kind, pub.ReadReceiptIDs = "cli", nil
		refuse(t, f, pub)
	})
	t.Run("a read of another account", func(t *testing.T) {
		pub, _ := publication(t, f, "prov-account")
		start := childStart(f, parentOf(t, f, pub), 2, time.Minute)
		start.AccountRef = "acc-004"
		other, _, err := f.Store.RunChildRead(ctx, start, func(context.Context) ([]postgres.RecordVersion, error) {
			return records("acc-004"), nil
		})
		ok(t, err)
		pub.Units[0].ReceiptID, pub.Units[0].SourceRecordID = other.ID, ""
		pub.Kind, pub.ReadReceiptIDs = "cli", nil
		refuse(t, f, pub)
	})
	t.Run("a read under another brief", func(t *testing.T) {
		pub, _ := publication(t, f, "prov-parent")
		elsewhere := parent(t, f, "prov-parent-elsewhere", time.Minute)
		read, _, err := f.Store.RunChildRead(ctx, childStart(f, elsewhere, 1, time.Minute),
			func(context.Context) ([]postgres.RecordVersion, error) { return records("elsewhere"), nil })
		ok(t, err)
		pub.Units[0].ReceiptID, pub.Units[0].SourceRecordID = read.ID, ""
		pub.Kind, pub.ReadReceiptIDs = "cli", nil
		refuse(t, f, pub)
	})
	t.Run("a record version its receipt never returned", func(t *testing.T) {
		pub, _ := publication(t, f, "prov-record")
		elsewhere := parent(t, f, "prov-record-elsewhere", time.Minute)
		read, _, err := f.Store.RunChildRead(ctx, childStart(f, elsewhere, 1, time.Minute),
			func(context.Context) ([]postgres.RecordVersion, error) { return records("never"), nil })
		ok(t, err)
		var stranger string
		ok(t, one(t, f, "SELECT source_record_id::text FROM receipt_records WHERE receipt_id = $1 AND ordinal = 0",
			&stranger, read.ID))
		pub.Units[0].SourceRecordID = stranger
		refuse(t, f, pub)
	})
	t.Run("a stale generation on a unit's receipt", func(t *testing.T) {
		pub, _ := publication(t, f, "prov-stale")
		pub.ReadReceiptIDs = nil
		_, err := f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
		ok(t, err)
		refuse(t, f, pub)
	})
}

func refuse(t *testing.T, f *postgrestest.Fixture, pub postgres.CardPublication) {
	t.Helper()
	before := count(t, f, "SELECT count(*) FROM cards")
	if card, err := f.Store.PublishCard(ctx, pub); err == nil {
		t.Fatalf("published card %s", card.ID)
	}
	if count(t, f, "SELECT count(*) FROM cards") != before {
		t.Fatal("a card was stored")
	}
}

// parentOf gives the brief receipt a publication's reads hang under.
func parentOf(t *testing.T, f *postgrestest.Fixture, pub postgres.CardPublication) postgres.Receipt {
	t.Helper()
	read, err := f.Store.Receipt(ctx, f.Workspace.ID, pub.Units[0].ReceiptID)
	ok(t, err)
	p, err := f.Store.Receipt(ctx, f.Workspace.ID, read.ParentID)
	ok(t, err)
	return p
}

func one(t *testing.T, f *postgrestest.Fixture, query string, out any, args ...any) error {
	t.Helper()
	conn, err := pgx.Connect(ctx, f.URL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, query, args...).Scan(out)
}

// Item 1: the actor is the seal's principal, never the caller's word.
func TestPublishTakesTheActorFromTheSeal(t *testing.T) {
	f := postgrestest.New(t)
	pub, _ := publication(t, f, "actor-revoked")
	ok(t, f.Store.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account))
	pub.PrincipalID = ""
	_, err := f.Store.PublishCard(ctx, pub)
	is(t, err, postgres.ErrNotPermitted)
	ok(t, f.Store.GrantAccount(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account))

	disabled, _ := publication(t, f, "actor-disabled")
	ok(t, sql(t, f, "UPDATE principals SET disabled_at = now() WHERE id = $1", f.Reader.ID))
	_, err = f.Store.PublishCard(ctx, disabled)
	is(t, err, postgres.ErrNotPermitted)
}

// Item 1: nothing is added to a card's projections after publication.
func TestProjectionsNeverChangeAfterPublication(t *testing.T) {
	f := postgrestest.New(t)
	for _, statement := range []string{
		`INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, body)
		 VALUES ($1, $2, 3, 'record', 'crm/accounts/acc-003', 'get_account', '{}')`,
		`INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, text)
		 VALUES ($1, $2, 1, 'speakers_unknown', 'note', 'added later')`,
	} {
		if err := sql(t, f, statement, f.Workspace.ID, f.Card.ID); err == nil {
			t.Errorf("allowed after publication: %s", statement)
		}
	}
	for _, statement := range []string{"TRUNCATE evidence_units", "TRUNCATE gaps", "TRUNCATE receipt_records CASCADE"} {
		if err := sql(t, f, statement); err == nil {
			t.Errorf("allowed: %s", statement)
		}
	}
}

// Item 2: a source read finishes only through FinishRead, with its bindings.
func TestSourceReadsClaimOnlyWhatTheyBound(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "claims", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	if _, err := f.Store.FinishReceipt(ctx, finishOK(c, "crm/accounts/acc-003")); err == nil {
		t.Fatal("FinishReceipt finished a source read ok without bindings")
	}
	c2 := child(t, f, p, 2, time.Minute)
	err := sql(t, f, `UPDATE receipts SET status = 'ok', http_status = 200, rows = '["crm/accounts/acc-003"]',
		row_count = 1, finished_at = now(), finishing_lease = lease_token WHERE id = $1`, c2.ID)
	if err == nil {
		t.Fatal("the database let a source read claim a row it never bound")
	}
	// Bindings committed apart from the finish are refused at commit.
	var version string
	ok(t, one(t, f, "SELECT id::text FROM source_records LIMIT 1", &version))
	if err := sql(t, f, `INSERT INTO receipt_records (workspace_id, receipt_id, ordinal, source_record_id)
		VALUES ($1, $2, 0, $3)`, f.Workspace.ID, c2.ID, version); err == nil {
		t.Fatal("a binding committed without its finish")
	}
	// A receipt is born pending.
	if err := sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, token_id, tool, arguments, request_id,
		status, http_status, finished_at, lease_token, lease_expires_at)
		VALUES ($1, 'born-finished', 'anonymous', 't', '{}', 'null', 'ok', 200, now(), gen_random_uuid(),
		now() + interval '1 minute')`, f.Workspace.ID); err == nil {
		t.Fatal("a receipt was inserted finished")
	}
	// A CRM read cannot return a calls record.
	c3 := child(t, f, p, 3, time.Minute)
	wrong := []postgres.RecordVersion{{Source: "calls", Table: "call_notes", RecordID: "note-1", Body: []byte("x")}}
	if _, err := f.Store.FinishRead(ctx, c3.ID, c3.LeaseToken, wrong, 1); err == nil {
		t.Fatal("a crm:read receipt bound a calls record")
	}
}

// Item 2: the database checks the finisher's lease token.
func TestTheLeaseHolderIsEnforcedByTheDatabase(t *testing.T) {
	f := postgrestest.New(t)
	r := parent(t, f, "holder", time.Minute)
	wrong := finishOK(r)
	wrong.LeaseToken = "00000000-0000-4000-8000-000000000000"
	if _, err := f.Store.FinishReceipt(ctx, wrong); err != postgres.ErrLeaseLost {
		t.Fatalf("wrong token on a live lease: %v", err)
	}
	for _, presented := range []string{"NULL", "gen_random_uuid()"} {
		err := sql(t, f, `UPDATE receipts SET status = 'ok', http_status = 200, finished_at = now(),
			finishing_lease = `+presented+` WHERE id = $1`, r.ID)
		if code(err) != "GT003" {
			t.Fatalf("presented %s: %v", presented, err)
		}
	}
}

// Item 2 (Opus m2): a generation bump waits for a read that is finishing.
func TestAGenerationBumpWaitsForAFinishingRead(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "bump-wait", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	paused, release := make(chan struct{}), make(chan struct{})
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "read:bound" {
			close(paused)
			<-release
		}
		return nil
	})
	defer postgres.SetCrash(f.Store, nil)
	finished := make(chan error, 1)
	go func() {
		_, err := f.Store.FinishRead(ctx, c.ID, c.LeaseToken, records("bump"), 1)
		finished <- err
	}()
	<-paused
	bumped := make(chan error, 1)
	go func() {
		other, err := postgres.Open(ctx, f.URL)
		if err != nil {
			bumped <- err
			return
		}
		defer other.Close()
		_, err = other.BumpConnectionGeneration(ctx, f.Connection.ID)
		bumped <- err
	}()
	select {
	case err := <-bumped:
		close(release)
		t.Fatalf("the bump committed while the read was finishing (%v)", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	ok(t, <-finished)
	ok(t, <-bumped)
}

// Item 3, storage side: restore checks the rows it loaded.
func TestRestoreRefusesRowsTheWritePathRefuses(t *testing.T) {
	f := postgrestest.New(t)
	files := dump(t, f)
	for name, edit := range map[string]func(map[string]string){
		"a seal pointed at a pending receipt": func(m map[string]string) {
			m["cards"] = strings.ReplaceAll(m["cards"], f.Seal.ID, f.Read.ID)
		},
		"changed unit text": func(m map[string]string) {
			m["evidence_units"] = strings.ReplaceAll(m["evidence_units"], "We agree.", "We refuse.")
		},
		"a read that lost its bindings": func(m map[string]string) {
			m["receipt_records"] = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			copied := map[string]string{}
			for k, v := range files.data {
				copied[k] = v
			}
			edit(copied)
			counts := files.copies
			fresh := freshStore(t)
			err := fresh.Restore(ctx, strings.Repeat("a", 64), files.version, recount(counts, copied), opener(copied))
			if err == nil {
				t.Fatal("restored")
			}
			if v, _ := fresh.SchemaVersion(ctx); v != 0 {
				t.Fatal("a refused restore left a schema")
			}
		})
	}
	t.Run("a target with an unrelated table", func(t *testing.T) {
		fresh, url := freshStoreURL(t)
		conn, err := pgx.Connect(ctx, url)
		ok(t, err)
		_, err = conn.Exec(ctx, "CREATE TABLE unrelated (x int)")
		conn.Close(ctx)
		ok(t, err)
		if err := fresh.Restore(ctx, strings.Repeat("a", 64), files.version, files.copies, opener(files.data)); err == nil {
			t.Fatal("restored into a database with other tables")
		}
	})
}

// Item 4: a source read runs at most once per receipt.
func TestConcurrentCallbacksReadOnce(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "callbacks", time.Minute)
	var reads atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute),
				func(context.Context) ([]postgres.RecordVersion, error) {
					reads.Add(1)
					time.Sleep(100 * time.Millisecond)
					return records("once"), nil
				})
		}()
	}
	close(start)
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("the source was read %d times", reads.Load())
	}
}

func TestAnExpiredPendingRetryDoesNotReadAgain(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "expired-retry", time.Minute)
	child(t, f, p, 1, 200*time.Millisecond) // the worker that stopped
	time.Sleep(300 * time.Millisecond)
	reads := 0
	r, _, err := f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { reads++; return records("again"), nil })
	if reads != 0 || err == nil {
		t.Fatalf("the retry read again (reads %d, err %v, receipt %+v)", reads, err, r)
	}
}

func TestRecoveryAfterPublicationCommits(t *testing.T) {
	f := postgrestest.New(t)
	// The fixture's brief has finished. Its read, retried, is the committed one.
	reads := 0
	r, versions, err := f.Store.RunChildRead(ctx, postgres.ChildReadStart{ParentID: f.Parent.ID, Ordinal: 1,
		Tool: "get_account", Scope: "crm:read", ConnectionID: f.Connection.ID,
		Arguments: json.RawMessage(`{"account_id":"acc-003"}`), Lease: time.Minute},
		func(context.Context) ([]postgres.RecordVersion, error) { reads++; return nil, nil })
	ok(t, err)
	if reads != 0 || r.ID != f.Read.ID || len(versions) != 2 {
		t.Fatalf("recovery read %d times, receipt %s", reads, r.ID)
	}
}

// Item 4: replay matches the whole request.
func TestReplayMatchesTheWholeRequest(t *testing.T) {
	f := postgrestest.New(t)
	base := postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "identity", PrincipalID: f.Reader.ID,
		TokenID: "anonymous", Tool: "brief", Scope: "crm:read", AccountRef: postgrestest.Account,
		Arguments: json.RawMessage(`{}`), RequestID: json.RawMessage(`7`), BriefID: "b1", Lease: time.Minute}
	_, err := f.Store.StartReceipt(ctx, base)
	ok(t, err)
	for name, change := range map[string]func(*postgres.ReceiptStart){
		"scope":      func(s *postgres.ReceiptStart) { s.Scope = "calls:read" },
		"request ID": func(s *postgres.ReceiptStart) { s.RequestID = json.RawMessage(`8`) },
		"brief ID":   func(s *postgres.ReceiptStart) { s.BriefID = "b2" },
	} {
		changed := base
		change(&changed)
		if _, err := f.Store.StartReceipt(ctx, changed); !errors.Is(err, postgres.ErrOperationReused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := parent(t, f, "child-identity", time.Minute)
	child(t, f, p, 1, time.Minute)
	scoped := childStart(f, p, 1, time.Minute)
	scoped.Scope = "calls:read"
	if _, err := f.Store.StartChildRead(ctx, scoped); err == nil {
		t.Error("a child replay with another scope was accepted")
	}
	accounted := childStart(f, p, 1, time.Minute)
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Other"))
	accounted.AccountRef = "acc-004"
	if _, err := f.Store.StartChildRead(ctx, accounted); err == nil {
		t.Error("a child replay with another account was accepted")
	}
	// A top-level operation ID never squats a child's.
	if _, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "victim#0",
		TokenID: "anonymous", Tool: "t", Arguments: json.RawMessage(`{}`), Lease: time.Minute}); err == nil {
		t.Error("an operation ID with # was accepted")
	}
}

// Item 5: a request naming an unknown account still gets its receipt.
func TestARejectionOfAnUnknownAccountGetsAReceipt(t *testing.T) {
	f := postgrestest.New(t)
	r, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "unknown",
		TokenID: "anonymous", Tool: "get_account", AccountRef: "acc-999",
		Arguments: json.RawMessage(`{"account_id":"acc-999"}`), Lease: time.Minute})
	ok(t, err)
	_, err = f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: r.ID, LeaseToken: r.LeaseToken,
		Status: "error", HTTPStatus: 403, Error: "unknown_account"})
	ok(t, err)
	var requested, status string
	ok(t, one(t, f, "SELECT requested_account || ' ' || status FROM receipts WHERE id = $1", &requested, r.ID))
	if requested != "acc-999 error" {
		t.Fatalf("committed %q", requested)
	}
	_ = status
}

// Item 6: errors a caller can act on.
func TestVersionConflictsAndLeaseErrors(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "conflict", time.Minute)
	v := []postgres.RecordVersion{{Source: "crm", Table: "accounts", RecordID: "acc-003", ContentVersion: "1",
		Body: []byte("a")}}
	_, _, err := f.Store.RunChildRead(ctx, childStart(f, p, 1, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { return v, nil })
	ok(t, err)
	v[0].Body = []byte("b")
	_, _, err = f.Store.RunChildRead(ctx, childStart(f, p, 2, time.Minute),
		func(context.Context) ([]postgres.RecordVersion, error) { return v, nil })
	is(t, err, postgres.ErrVersionConflict)
	c, err := f.Store.ReceiptByOperation(ctx, p.OperationID+"#2")
	ok(t, err)
	if c.Status != "error" || c.HTTPStatus == nil || *c.HTTPStatus != 409 || *c.Error != "version_conflict" {
		t.Fatalf("conflicted read %+v", c)
	}

	pub, _ := publication(t, f, "expired-seal")
	ok(t, sql(t, f, `UPDATE receipts SET lease_expires_at = clock_timestamp() + interval '100 milliseconds',
		finishing_lease = lease_token WHERE id = $1`, pub.SealReceiptID))
	time.Sleep(200 * time.Millisecond)
	_, err = f.Store.PublishCard(ctx, pub)
	is(t, err, postgres.ErrLeaseLost)

	again, _ := publication(t, f, "published-twice")
	card, err := f.Store.PublishCard(ctx, again)
	ok(t, err)
	retried, err := f.Store.PublishCard(ctx, again)
	ok(t, err)
	if retried.ID != card.ID {
		t.Fatal("a publish retry did not return the card")
	}
}

type dumped struct {
	version int
	copies  []postgres.TableCopy
	data    map[string]string
}

func dump(t *testing.T, f *postgrestest.Fixture) dumped {
	t.Helper()
	d := dumped{data: map[string]string{}}
	buffers := map[string]*strings.Builder{}
	var err error
	d.version, d.copies, err = f.Store.Dump(ctx, func(table string) (io.WriteCloser, error) {
		buffers[table] = &strings.Builder{}
		return nopWriteCloser{buffers[table]}, nil
	})
	ok(t, err)
	for table, b := range buffers {
		d.data[table] = b.String()
	}
	return d
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// recount sets each table's row count to its edited data, as an editor
// recomputing the manifest would.
func recount(copies []postgres.TableCopy, data map[string]string) []postgres.TableCopy {
	out := make([]postgres.TableCopy, len(copies))
	for i, c := range copies {
		c.Rows = int64(strings.Count(data[c.Name], "\n"))
		out[i] = c
	}
	return out
}

func opener(data map[string]string) func(string) (io.ReadCloser, error) {
	return func(table string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(data[table])), nil
	}
}

func freshStore(t *testing.T) *postgres.Store {
	s, _ := freshStoreURL(t)
	return s
}

func freshStoreURL(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	url := pgtest.URL(t)
	s, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(s.Close)
	return s, url
}

// Item 7: a revocation cannot commit between the grant check and the read.
func TestARevocationWaitsForARead(t *testing.T) {
	f := postgrestest.New(t)
	reader := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID}
	paused, release := make(chan struct{}), make(chan struct{})
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "view:granted" {
			close(paused)
			<-release
		}
		return nil
	})
	read := make(chan error, 1)
	go func() {
		_, err := f.Store.ViewCard(ctx, reader, f.Card.ID)
		read <- err
	}()
	<-paused
	revoked := make(chan error, 1)
	go func() {
		other, err := postgres.Open(ctx, f.URL)
		if err != nil {
			revoked <- err
			return
		}
		defer other.Close()
		revoked <- other.RevokeAccountGrant(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account)
	}()
	select {
	case err := <-revoked:
		close(release)
		t.Fatalf("the revocation committed during the read (%v)", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	ok(t, <-read)
	ok(t, <-revoked)
	postgres.SetCrash(f.Store, nil)
	_, err := f.Store.ViewCard(ctx, reader, f.Card.ID)
	is(t, err, postgres.ErrNotPermitted)
}

// Item 7: unknown, foreign and ungranted things all answer ErrNotPermitted.
func TestRefusalsAreUniform(t *testing.T) {
	f := postgrestest.New(t)
	outsider := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Outsider.ID}
	reader := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID}
	unknown := "00000000-0000-4000-8000-000000000000"
	for name, call := range map[string]func() error{
		"ungranted card":    func() error { _, err := f.Store.ViewCard(ctx, outsider, f.Card.ID); return err },
		"unknown card":      func() error { _, err := f.Store.ViewCard(ctx, reader, unknown); return err },
		"malformed card":    func() error { _, err := f.Store.ViewCard(ctx, reader, "x"); return err },
		"ungranted receipt": func() error { _, err := f.Store.ViewReceipt(ctx, outsider, f.Read.ID); return err },
		"unknown receipt":   func() error { _, err := f.Store.ViewReceipt(ctx, reader, unknown); return err },
		"ungranted account": func() error { _, err := f.Store.ViewAccountCards(ctx, outsider, "acc-003"); return err },
		"unknown account":   func() error { _, err := f.Store.ViewAccountCards(ctx, reader, "acc-999"); return err },
		"unknown unit": func() error {
			_, _, _, err := f.Store.ViewUnit(ctx, reader, f.Card.ID, 99)
			return err
		},
	} {
		if err := call(); !errors.Is(err, postgres.ErrNotPermitted) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Item 11: the server's role reads and writes rows and nothing else.
func TestTheAppRoleOnlyTouchesRows(t *testing.T) {
	f := postgrestest.New(t)
	name := "gtm_app_" + strings.ReplaceAll(f.Workspace.ID[:8], "-", "")
	ok(t, f.Store.EnsureAppRole(ctx, name, "app-password-for-this-test"))
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), f.URL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		conn.Exec(context.Background(), "DROP OWNED BY "+name)
		conn.Exec(context.Background(), "DROP ROLE "+name)
	})
	parsed, err := pgx.ParseConfig(f.URL)
	ok(t, err)
	appURL := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", name, "app-password-for-this-test",
		parsed.Host, parsed.Port, parsed.Database)
	app, err := postgres.Open(ctx, appURL)
	ok(t, err)
	defer app.Close()
	ok(t, app.CheckAppRole(ctx))
	if f.Store.CheckAppRole(ctx) == nil {
		t.Fatal("the owner passed as the app role")
	}
	if _, err := app.ViewCard(ctx, postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID},
		f.Card.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, appURL)
	ok(t, err)
	defer conn.Close(ctx)
	for _, statement := range []string{
		"ALTER TABLE receipts DISABLE TRIGGER USER",
		"TRUNCATE receipts CASCADE",
		"DELETE FROM accounts",
		"DROP TABLE gaps",
		"CREATE TABLE extra (x int)",
		"UPDATE schema_migrations SET sha256 = 'x'",
		"INSERT INTO restores (archive_sha256) VALUES (repeat('a', 64))",
	} {
		if _, err := conn.Exec(ctx, statement); code(err) != "42501" {
			t.Errorf("the app role ran %q (%v)", statement, err)
		}
	}
}
