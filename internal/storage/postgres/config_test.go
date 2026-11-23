package postgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

func revisionOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// The database keeps the file's rules for any writer: a revision is the SHA-256 of its bytes and is written
// once; an activation needs an activate_config receipt that finished ok and names the revision and nothing
// else, takes the database's time, comes after every activation before it, and is never changed.
func TestConfigRulesHoldForAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	body := "sources:\n  crm: {kind: demo}\n"
	revision := revisionOf(body)
	settings := json.RawMessage(`{"sources":{"crm":{"kind":"demo"}}}`)
	if stored, err := f.Store.SaveConfigRevision(ctx, f.Workspace.ID, revision, []byte(body), settings); err != nil || !stored {
		t.Fatalf("save: %v %v", stored, err)
	}
	if stored, err := f.Store.SaveConfigRevision(ctx, f.Workspace.ID, revision, []byte(body), settings); err != nil || stored {
		t.Errorf("the same bytes stored again: %v %v", stored, err)
	}
	receipt := func(tool, arguments, status string) postgres.Receipt {
		started, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
			OperationID: "config-" + time.Now().Format("150405.000000000"), TokenID: postgres.TokenID(""), Tool: tool,
			Arguments: json.RawMessage(arguments), RequestID: json.RawMessage(`1`), Lease: time.Minute})
		ok(t, err)
		if status == "pending" {
			return started
		}
		finish := postgres.ReceiptFinish{ReceiptID: started.ID, LeaseToken: started.LeaseToken, Status: status, HTTPStatus: 200}
		if status == "error" {
			finish.HTTPStatus, finish.Error = 404, "revision_not_found"
		}
		finished, err := f.Store.FinishReceipt(ctx, finish)
		ok(t, err)
		return finished
	}
	activate := func(r postgres.Receipt) error {
		return sql(t, f, `INSERT INTO config_activations (workspace_id, revision, receipt_id, activated_at)
			VALUES ($1, $2, $3, '2020-01-01')`, f.Workspace.ID, revision, r.ID)
	}
	named := `{"revision":"` + revision + `"}`
	for label, refused := range map[string]error{
		"under a receipt of another tool":         activate(receipt("close_deal", named, "ok")),
		"under a receipt still pending":           activate(receipt("activate_config", named, "pending")),
		"under a receipt that finished refused":   activate(receipt("activate_config", named, "error")),
		"under a receipt naming another revision": activate(receipt("activate_config", `{"revision":"`+strings.Repeat("b", 64)+`"}`, "ok")),
		"under a receipt with a second member":    activate(receipt("activate_config", `{"revision":"`+revision+`","force":true}`, "ok")),
		"with bytes that are not the revision's": sql(t, f, `INSERT INTO config_revisions (workspace_id, revision, body, settings)
			VALUES ($1, $2, 'other bytes', '{}')`, f.Workspace.ID, strings.Repeat("c", 64)),
		"of a revision the workspace never stored": sql(t, f, `INSERT INTO config_activations (workspace_id, revision, receipt_id)
			VALUES ($1, $2, $3)`, f.Workspace.ID, strings.Repeat("d", 64), receipt("activate_config", `{"revision":"`+strings.Repeat("d", 64)+`"}`, "ok").ID),
	} {
		if refused == nil {
			t.Errorf("a file was stored or activated %s", label)
		}
	}
	// In a transaction whose snapshot is pinned, an activation could not see the one before it.
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	good := receipt("activate_config", named, "ok")
	_, err = conn.Exec(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")
	ok(t, err)
	if _, err := conn.Exec(ctx, `INSERT INTO config_activations (workspace_id, revision, receipt_id) VALUES ($1, $2, $3)`,
		f.Workspace.ID, revision, good.ID); err == nil {
		t.Errorf("an activation in a pinned snapshot was taken")
	}
	_, _ = conn.Exec(ctx, "ROLLBACK")
	// Read uncommitted is not read committed either, though PostgreSQL runs it as such.
	_, err = conn.Exec(ctx, "BEGIN ISOLATION LEVEL READ UNCOMMITTED")
	ok(t, err)
	if _, err := conn.Exec(ctx, `INSERT INTO config_activations (workspace_id, revision, receipt_id) VALUES ($1, $2, $3)`,
		f.Workspace.ID, revision, good.ID); err == nil {
		t.Errorf("an activation under read uncommitted was taken")
	}
	_, _ = conn.Exec(ctx, "ROLLBACK")
	// A temporary table named receipts does not stand in for the receipts: the guard reads its own schema's.
	// The real receipt here is a read's; the temporary row with its ID says it is an activation.
	read := postgrestest.Read(t, f, "get_account", postgrestest.Account, "")
	for _, statement := range []string{
		`CREATE TEMP TABLE receipts (LIKE public.receipts)`,
		`INSERT INTO receipts SELECT * FROM public.receipts WHERE id = '` + good.ID + `'`,
		`UPDATE receipts SET id = '` + read.ID + `'`,
	} {
		_, err = conn.Exec(ctx, statement)
		ok(t, err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO config_activations (workspace_id, revision, receipt_id) VALUES ($1, $2, $3)`,
		f.Workspace.ID, revision, read.ID); err == nil {
		t.Errorf("an activation was taken on a temporary table's word")
	}
	_, err = conn.Exec(ctx, `DROP TABLE pg_temp.receipts`)
	ok(t, err)
	if n := count(t, f, `SELECT count(*) FROM config_activations`); n != 0 {
		t.Fatalf("%d activations after every refusal", n)
	}

	before := time.Now().Add(-time.Minute)
	ok(t, activate(good))
	history, err := f.Store.ConfigHistory(ctx, f.Workspace.ID, 0)
	ok(t, err)
	if len(history) != 1 || history[0].ActivatedAt.Before(before) {
		t.Errorf("the activation took the writer's time: %+v", history)
	}
	if err := activate(good); err == nil {
		t.Errorf("one receipt activated twice")
	}
	// The file that is active is not activated again, also under a receipt of its own.
	if err := activate(receipt("activate_config", named, "ok")); err == nil || !strings.Contains(err.Error(), "active already") {
		t.Errorf("the active file activated again: %v", err)
	}
	for _, statement := range []string{
		`UPDATE config_activations SET revision = revision`, `DELETE FROM config_activations`, `TRUNCATE config_activations`,
		`UPDATE config_revisions SET settings = '{}'`, `DELETE FROM config_revisions`,
	} {
		if err := sql(t, f, statement); err == nil {
			t.Errorf("%s was allowed", statement)
		}
	}
}

// ActivateConfig makes a stored revision the active file under its receipt, gives an unknown one
// ErrNoRevision under a refused receipt, and leaves the active file as it is when asked for it again.
func TestActivateConfig(t *testing.T) {
	f := postgrestest.New(t)
	first, second := "sources:\n  crm: {kind: demo}\n", "sources:\n  book: {kind: demo}\n"
	for _, body := range []string{first, second} {
		if _, err := f.Store.SaveConfigRevision(ctx, f.Workspace.ID, revisionOf(body), []byte(body), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, found, err := f.Store.ActiveConfig(ctx, f.Workspace.ID); err != nil || found {
		t.Fatalf("a workspace with no activation: %v %v", found, err)
	}
	if _, _, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, strings.Repeat("e", 64)); !errors.Is(err, postgres.ErrNoRevision) {
		t.Errorf("an unknown revision: %v", err)
	}
	a, changed, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(first))
	if err != nil || !changed || a.Revision != revisionOf(first) {
		t.Fatalf("activate: %+v %v %v", a, changed, err)
	}
	again, changed, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(first))
	if err != nil || changed || again.ReceiptID != a.ReceiptID {
		t.Errorf("the active file again: %+v %v %v", again, changed, err)
	}
	b, _, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(second))
	ok(t, err)
	file, active, found, err := f.Store.ActiveConfig(ctx, f.Workspace.ID)
	if err != nil || !found || active.Revision != revisionOf(second) || string(file.Body) != second || !b.ActivatedAt.After(a.ActivatedAt) {
		t.Errorf("the active file: %+v %+v %v %v", file, active, found, err)
	}
	// Of several activations of one revision at once, one activates it. The others are given that activation,
	// each under a receipt finished as refused, and one activation is on record.
	third := "sources:\n  third: {kind: demo}\n"
	if _, err := f.Store.SaveConfigRevision(ctx, f.Workspace.ID, revisionOf(third), []byte(third), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		changed bool
		err     error
	}
	outcomes := make(chan outcome, 6)
	for range 6 {
		go func() {
			_, changed, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(third))
			outcomes <- outcome{changed, err}
		}()
	}
	won := 0
	for range 6 {
		o := <-outcomes
		if o.err != nil {
			t.Errorf("an activation at once with others: %v", o.err)
		}
		if o.changed {
			won++
		}
	}
	if n := count(t, f, `SELECT count(*) FROM config_activations WHERE revision = $1`, revisionOf(third)); won != 1 || n != 1 {
		t.Errorf("%d of six activations of one revision say they activated it, and %d are on record", won, n)
	}
	if n := count(t, f, `SELECT count(*) FROM receipts WHERE tool = 'activate_config' AND status = 'ok'
		AND arguments ->> 'revision' = $1`, revisionOf(third)); n != 1 {
		t.Errorf("%d receipts finished ok for that revision, of 1", n)
	}
	if n := count(t, f, `SELECT count(*) FROM receipts WHERE status = 'pending'`); n != 0 {
		t.Errorf("%d receipts are left pending", n)
	}
	b, _, err = f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(second))
	ok(t, err)
	// The first may be activated again later: it is then the latest.
	back, changed, err := f.Store.ActivateConfig(ctx, f.Workspace.ID, revisionOf(first))
	if err != nil || !changed || !back.ActivatedAt.After(b.ActivatedAt) {
		t.Errorf("an earlier revision activated again: %+v %v %v", back, changed, err)
	}
	if n := count(t, f, `SELECT count(*) FROM gtm_integrity_violations()`); n != 0 {
		t.Errorf("the integrity check finds %d violations", n)
	}
}

// A restore loads activations with their triggers off, so its check refuses one its receipt does not name.
func TestARestoreRefusesAnActivationItsReceiptDoesNotName(t *testing.T) {
	f := postgrestest.New(t)
	body := "sources:\n  crm: {kind: demo}\n"
	if _, err := f.Store.SaveConfigRevision(ctx, f.Workspace.ID, revisionOf(body), []byte(body), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// An activation's own kind of receipt, finished ok, that names another revision.
	started, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "config-other",
		TokenID: postgres.TokenID(""), Tool: "activate_config", Arguments: json.RawMessage(`{"revision":"` + strings.Repeat("b", 64) + `"}`),
		RequestID: json.RawMessage(`1`), Lease: time.Minute})
	ok(t, err)
	read, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: started.ID, LeaseToken: started.LeaseToken,
		Status: "ok", HTTPStatus: 200})
	ok(t, err)
	ok(t, sql(t, f, `ALTER TABLE config_activations DISABLE TRIGGER USER`))
	ok(t, sql(t, f, `INSERT INTO config_activations (workspace_id, revision, receipt_id) VALUES ($1, $2, $3)`,
		f.Workspace.ID, revisionOf(body), read.ID))
	ok(t, sql(t, f, `ALTER TABLE config_activations ENABLE TRIGGER USER`))
	files := dump(t, f)
	fresh, _ := freshStoreURL(t)
	err = fresh.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data))
	if err == nil || !strings.Contains(err.Error(), "is not the one its receipt names") {
		t.Errorf("the restore: %v", err)
	}
}
