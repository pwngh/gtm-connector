// Package receiptstest is the contract every receipts.Store keeps, written once and run against each store.
package receiptstest

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Fixture is one empty store and what the contract needs around it.
type Fixture struct {
	Store receipts.Store
	// Workspace holds Principal, Account and Connection, a CRM connection; Other is a second workspace.
	Workspace, Other               string
	Principal, Account, Connection string
	// Bump changes Connection's source: its generation goes up.
	Bump func(t *testing.T)
	// Grant gives Principal a grant on Account; it starts without one.
	Grant func(t *testing.T)
	// Records gives the record versions a read bound, in order.
	Records func(t *testing.T, auditID string) []receipts.RecordVersion
	// Committed counts the receipts the store holds.
	Committed func(t *testing.T) int
	// Published is whether a card of this brief was written.
	Published func(t *testing.T, briefID string) bool
}

// Run runs every contract test, each on a fresh fixture.
func Run(t *testing.T, fresh func(t *testing.T) *Fixture) {
	for _, c := range []struct {
		name string
		test func(t *testing.T, f *Fixture)
	}{
		{"PendingThenFinishedOnce", pendingThenFinishedOnce},
		{"OperationRetry", operationRetry},
		{"GenerationFence", generationFence},
		{"RacingFinishersOneWins", racingFinishersOneWins},
		{"ChildReads", childReads},
		{"DenialAndNotSelected", denialAndNotSelected},
		{"StartRefusesBadReceipts", startRefusesBadReceipts},
		{"ReceiptsAreDetached", receiptsAreDetached},
		{"Publish", publish},
	} {
		t.Run(c.name, func(t *testing.T) { c.test(t, fresh(t)) })
	}
}

var (
	ctx   = context.Background()
	token = receipts.Fingerprint("a bearer")
	brief = "brief-0123456789abcdef0123456789abcdef"
)

// start commits a receipt for the fixture's account; a non-empty connection makes it a source read.
func start(t *testing.T, f *Fixture, operation, tool string, id receipts.RequestID, connection string) receipts.Receipt {
	t.Helper()
	scope := "crm:read"
	b := brief
	r, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: operation,
		PrincipalID: f.Principal, TokenID: token, Tool: tool, Scope: scope, AccountRef: f.Account,
		ConnectionID: connection, Arguments: strictjson.NewObject("account_id", f.Account), RequestID: id, BriefID: &b})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func encode(t *testing.T, value any) string {
	t.Helper()
	text, err := strictjson.Dumps(value, strictjson.Quoted)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// shape is a receipt's JSON with the two columns the store assigns, its ID and its start time, left out.
func shape(t *testing.T, r receipts.Receipt) string {
	t.Helper()
	value := r.Value()
	value.Set("audit_id", "-")
	value.Set("started_at", "-")
	return encode(t, value)
}

func version(id, body string) receipts.RecordVersion {
	return receipts.RecordVersion{Source: "crm", Table: "accounts", RecordID: id, Body: []byte(body)}
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, want %v", what, err, want)
	}
}

func readOne(t *testing.T, f *Fixture, id string) receipts.Receipt {
	t.Helper()
	read, err := f.Store.Read(ctx, f.Workspace, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return read[0]
}

func pendingThenFinishedOnce(t *testing.T, f *Fixture) {
	r := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	want := `{"audit_id": "-", "started_at": "-", "token_id": "` + token + `", "tool": "get_account", ` +
		`"arguments": {"account_id": "` + f.Account + `"}, "request_id": 1, "brief_id": "` + brief + `", ` +
		`"status": "pending", "http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`
	if got := shape(t, r); got != want {
		t.Fatalf("pending receipt:\n got %s\nwant %s", got, want)
	}
	if r.Generation != 1 || r.WorkspaceID != f.Workspace || r.PrincipalID != f.Principal || r.Lease == "" ||
		r.ConnectionID != f.Connection || r.OperationID != "op-1" {
		t.Fatalf("start recorded %+v", r)
	}
	_, err := f.Store.Finish(ctx, receipts.Ticket{AuditID: r.AuditID, Lease: receipts.RandomID()}, receipts.Denied())
	is(t, err, receipts.ErrLeaseLost, "a finish without the lease")
	done, err := f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	if err != nil || done.Status != "ok" || len(done.Rows) != 1 || done.Rows[0] != "crm/accounts/"+f.Account {
		t.Fatalf("finish: %+v %v", done, err)
	}
	// A retry after a lost acknowledgement returns the finished receipt.
	again, err := f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	if err != nil || !again.Same(done) {
		t.Fatalf("the identical retry: %+v %v", again, err)
	}
	for _, other := range [][]receipts.RecordVersion{{version(f.Account, `{"a": 1}`)}, {version("acc-002", "{}")}} {
		_, err := f.Store.FinishRead(ctx, r.Ticket(), other, 0)
		is(t, err, receipts.ErrFinished, "a retry with other records")
	}
	_, err = f.Store.Finish(ctx, r.Ticket(), receipts.Denied())
	is(t, err, receipts.ErrFinished, "a second finish with another outcome")
	_, err = f.Store.Finish(ctx, receipts.Ticket{AuditID: receipts.RandomID(), Lease: receipts.RandomID()},
		receipts.Denied())
	is(t, err, receipts.ErrNotFound, "finishing an unknown receipt")
	_, err = f.Store.Finish(ctx, r.Ticket(), receipts.Outcome{Status: "pending", HTTPStatus: 200})
	is(t, err, receipts.ErrInvalid, "pending as a final status")
	read := readOne(t, f, r.AuditID)
	if read.Status != "ok" || read.Lease != "" || !read.Same(done) {
		t.Fatalf("read: %+v", read)
	}
	_, err = f.Store.Read(ctx, f.Other, []string{r.AuditID})
	is(t, err, receipts.ErrNotFound, "reading another workspace's receipt")
	bound := f.Records(t, r.AuditID)
	if len(bound) != 1 || bound[0].Version() != version(f.Account, "{}").Version() || string(bound[0].Body) != "{}" {
		t.Fatalf("bound versions: %+v", bound)
	}
}

func operationRetry(t *testing.T, f *Fixture) {
	first := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	again := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	if again.AuditID != first.AuditID || again.Lease != first.Lease || f.Committed(t) != 1 {
		t.Fatalf("a retried start made %+v", again)
	}
	_, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "op-1", TokenID: token,
		Tool: "list_opportunities", Arguments: strictjson.NewObject()})
	is(t, err, receipts.ErrOperationReused, "another request under the operation")
	found, err := f.Store.ByOperation(ctx, "op-1")
	if err != nil || found.AuditID != first.AuditID || found.Lease != first.Lease {
		t.Fatalf("by operation: %+v %v", found, err)
	}
	_, err = f.Store.ByOperation(ctx, "op-unknown")
	is(t, err, receipts.ErrNotFound, "an operation never started")
}

func generationFence(t *testing.T, f *Fixture) {
	r := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	f.Bump(t)
	_, err := f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	is(t, err, receipts.ErrSourceChanged, "a read across a source change")
	read := readOne(t, f, r.AuditID)
	if read.Status != "error" || *read.HTTPStatus != 409 || *read.Error != "source_changed" || len(read.Rows) != 0 {
		t.Fatalf("the fenced receipt: %s", encode(t, read.Value()))
	}
	if bound := f.Records(t, r.AuditID); len(bound) != 0 {
		t.Fatalf("a fenced read bound %v", bound)
	}
	_, err = f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	is(t, err, receipts.ErrSourceChanged, "retrying the fenced read")
	other := start(t, f, "op-2", "get_account", receipts.RPC(2), f.Connection)
	if other.Generation != r.Generation+1 {
		t.Fatalf("a read after the change records generation %d", other.Generation)
	}
	if _, err := f.Store.FinishRead(ctx, other.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0); err != nil {
		t.Fatal(err)
	}
	third := start(t, f, "op-3", "get_account", receipts.RPC(3), f.Connection)
	conflicting := version(f.Account, "[]")
	conflicting.ContentVersion = version(f.Account, "{}").Version()
	_, err = f.Store.FinishRead(ctx, third.Ticket(), []receipts.RecordVersion{conflicting}, 0)
	is(t, err, receipts.ErrVersionConflict, "one version with two bodies")
}

func racingFinishersOneWins(t *testing.T, f *Fixture) {
	for _, connection := range []string{"", f.Connection} {
		r := start(t, f, "op-race-"+connection, "get_account", receipts.RPC(1), connection)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.Store.Finish(ctx, r.Ticket(), receipts.Failed(500, fmt.Sprintf("worker %d", i))); err == nil {
					wins.Add(1)
				} else if !errors.Is(err, receipts.ErrFinished) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d finishers won", wins.Load())
		}
	}
}

func childReads(t *testing.T, f *Fixture) {
	parent := start(t, f, "op-1", receipts.Composite, receipts.RPC(1), "")
	child := func(parent string, ordinal int) (receipts.Receipt, error) {
		b := brief
		return f.Store.StartChild(ctx, receipts.ChildStart{Parent: parent, Ordinal: ordinal, Tool: "get_account",
			Scope: "crm:read", ConnectionID: f.Connection, Arguments: strictjson.NewObject("account_id", f.Account),
			BriefID: &b})
	}
	first, err := child(parent.AuditID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, ordinal, ok := first.RequestID.Parent(); !ok || got != parent.AuditID || ordinal != 1 ||
		first.TokenID != parent.TokenID || first.PrincipalID != parent.PrincipalID || first.WorkspaceID != f.Workspace ||
		first.OperationID != "op-1#1" || first.Lease == "" || first.BriefID == nil || *first.BriefID != brief {
		t.Fatalf("child: %+v", first)
	}
	// A read runs at most once: a pending child is not handed out again.
	_, err = child(parent.AuditID, 1)
	is(t, err, receipts.ErrInProgress, "a retried start of a pending child")
	_, err = f.Store.StartChild(ctx, receipts.ChildStart{Parent: parent.AuditID, Ordinal: 1, Tool: "list_opportunities",
		Scope: "crm:read", ConnectionID: f.Connection, Arguments: strictjson.NewObject()})
	is(t, err, receipts.ErrDuplicateChild, "another child with ordinal 1")
	_, err = child(receipts.RandomID(), 1)
	is(t, err, receipts.ErrParent, "a child of a missing parent")
	for _, ordinal := range []int{0, -1} {
		_, err := child(parent.AuditID, ordinal)
		is(t, err, receipts.ErrInvalid, fmt.Sprintf("ordinal %d", ordinal))
	}
	_, err = child("", 1)
	is(t, err, receipts.ErrInvalid, "an empty parent")
	_, err = f.Store.StartChild(ctx, receipts.ChildStart{Parent: parent.AuditID, Ordinal: 2, Tool: "get_account",
		Arguments: strictjson.NewObject()})
	is(t, err, receipts.ErrInvalid, "a child that names no connection")
	_, err = child(first.AuditID, 1)
	is(t, err, receipts.ErrParent, "a child of a child")
	seal := start(t, f, "op-seal", "save_brief", receipts.Null(), "")
	_, err = child(seal.AuditID, 1)
	is(t, err, receipts.ErrParent, "a child of a seal")
	_, err = f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "op-x", TokenID: token,
		Tool: "get_account", Arguments: strictjson.NewObject(), RequestID: receipts.Child(parent.AuditID, 9)})
	is(t, err, receipts.ErrInvalid, "a child request ID through Start")
	_, err = f.Store.Finish(ctx, parent.Ticket(), receipts.Failed(200, "request_failed"))
	is(t, err, receipts.ErrInvalid, "finishing a parent with a pending child")
	denied, err := f.Store.Finish(ctx, first.Ticket(), receipts.Denied())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Store.Finish(ctx, parent.Ticket(), receipts.Failed(200, "request_failed")); err != nil {
		t.Fatal(err)
	}
	_, err = child(parent.AuditID, 3)
	is(t, err, receipts.ErrParent, "a child of a finished parent")
	// A finished child's outcome stands, after its parent finished too.
	again, err := child(parent.AuditID, 1)
	if err != nil || again.AuditID != first.AuditID || again.Lease != "" || !again.Same(denied) {
		t.Fatalf("a retried start of a finished child: %+v %v", again, err)
	}
}

func denialAndNotSelected(t *testing.T, f *Fixture) {
	for i, c := range []struct {
		outcome receipts.Outcome
		status  int
	}{{receipts.Denied(), 403}, {receipts.NotSelected(), 200}} {
		r := start(t, f, fmt.Sprint("op-", i), "read_slack_messages", receipts.RPC(4), f.Connection)
		done, err := f.Store.Finish(ctx, r.Ticket(), c.outcome)
		if err != nil || done.Status != "error" || *done.HTTPStatus != c.status || len(done.Rows) != 0 ||
			*done.Error != "request_failed" {
			t.Fatalf("%d: %+v %v", c.status, done, err)
		}
	}
	r := start(t, f, "op-rows", "read_slack_messages", receipts.RPC(5), f.Connection)
	refused := receipts.Denied()
	refused.Rows = []string{"slack/messages/msg-001"}
	_, err := f.Store.Finish(ctx, r.Ticket(), refused)
	is(t, err, receipts.ErrInvalid, "an error receipt with rows")
}

func startRefusesBadReceipts(t *testing.T, f *Fixture) {
	w, a := f.Workspace, receipts.Anonymous
	for _, s := range []receipts.Start{
		{WorkspaceID: w, OperationID: "a", TokenID: "raw-token", Tool: "get_account", Arguments: strictjson.NewObject()},
		{WorkspaceID: w, OperationID: "b", TokenID: a, Tool: "", Arguments: strictjson.NewObject()},
		{WorkspaceID: w, OperationID: "c", TokenID: a, Tool: "get_account", Arguments: []any{func() {}}},
		{WorkspaceID: "", OperationID: "d", TokenID: a, Tool: "get_account", Arguments: strictjson.NewObject()},
		{WorkspaceID: w, OperationID: "", TokenID: a, Tool: "get_account", Arguments: strictjson.NewObject()},
		{WorkspaceID: w, OperationID: "e#1", TokenID: a, Tool: "get_account", Arguments: strictjson.NewObject()},
	} {
		_, err := f.Store.Start(ctx, s)
		is(t, err, receipts.ErrInvalid, fmt.Sprintf("%+v", s))
	}
	_, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: w, OperationID: "f", TokenID: a, Tool: "get_account",
		Scope: "crm:read", ConnectionID: receipts.RandomID(), Arguments: strictjson.NewObject()})
	is(t, err, receipts.ErrNotFound, "an unknown connection")
	if f.Committed(t) != 0 {
		t.Fatal("a refused start left a receipt")
	}
}

// receiptsAreDetached changes everything a caller handed in or was handed back, and checks that the
// committed receipt did not change.
func receiptsAreDetached(t *testing.T, f *Fixture) {
	arguments := strictjson.NewObject("account_id", f.Account, "nested", []any{big.NewInt(1)})
	id := big.NewInt(7)
	b := "brief-x"
	started, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "op", TokenID: token,
		Tool: "get_account", Arguments: arguments, RequestID: receipts.RPC(id), BriefID: &b})
	if err != nil {
		t.Fatal(err)
	}
	before := encode(t, started.Value())
	arguments.Set("account_id", "acc-999")
	arguments.Values[1].([]any)[0].(*big.Int).SetInt64(99)
	id.SetInt64(8)
	b = "brief-y"
	started.Arguments.(*strictjson.Object).Set("account_id", "acc-998")
	*started.BriefID = "brief-z"
	value := started.Value()
	value.Set("tool", "changed")
	value.Values[4].(*strictjson.Object).Set("account_id", "acc-997")
	code := "worker"
	finished, err := f.Store.Finish(ctx, started.Ticket(), receipts.Outcome{Status: "error", HTTPStatus: 500,
		Rows: []string{}, Error: &code})
	if err != nil {
		t.Fatal(err)
	}
	code = "changed"
	*finished.HTTPStatus = 200
	*finished.Error = "changed"
	*finished.ElapsedMS = 5
	read, err := f.Store.Read(ctx, f.Workspace, []string{started.AuditID})
	if err != nil {
		t.Fatal(err)
	}
	read[0].Arguments.(*strictjson.Object).Set("account_id", "acc-996")
	read[0].Rows = append(read[0].Rows, "x")
	pending := `"status": "pending", "http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`
	if !strings.HasSuffix(before, pending) {
		t.Fatalf("started as %s", before)
	}
	want := strings.TrimSuffix(before, pending) +
		`"status": "error", "http_status": 500, "row_count": 0, "rows": [], "elapsed_ms": 0.0, "error": "worker"}`
	if got := encode(t, readOne(t, f, started.AuditID).Value()); got != want {
		t.Fatalf("the committed receipt changed:\n got %s\nwant %s", got, want)
	}
}

func publish(t *testing.T, f *Fixture) {
	unit := `{"kind": "record", "n": 1, "record_ref": "crm/accounts/` + f.Account + `", "tool": "get_account"}`
	payload := []byte(`{"units": [` + unit + `], "gaps": []}`)
	read := func(operation string) receipts.Receipt {
		r := start(t, f, operation, "get_account", receipts.RPC(1), f.Connection)
		if _, err := f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0); err != nil {
			t.Fatal(err)
		}
		return r
	}
	publication := func(seal, read receipts.Receipt, bytes []byte) receipts.Publication {
		return receipts.Publication{WorkspaceID: f.Workspace, AccountRef: f.Account, BriefID: brief,
			PrincipalID: f.Principal, Seal: seal.Ticket(), Kind: "card", SchemaVersion: 3, RulesVersion: "3",
			Bytes: append([]byte(nil), bytes...), Reads: []string{read.AuditID},
			Units: []receipts.UnitRow{{N: 1, Kind: "record", RecordRef: "crm/accounts/" + f.Account, Tool: "get_account",
				ReceiptID: read.AuditID, ContentVersion: version(f.Account, "{}").Version(), Value: []byte(unit)}}}
	}
	sealStart := func(operation string) receipts.Receipt {
		r, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: operation,
			PrincipalID: f.Principal, TokenID: token, Tool: "save_brief", AccountRef: f.Account,
			Arguments: strictjson.NewObject("account_id", f.Account, "brief_id", brief)})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := read("op-read-1")
	// No grant: nothing is written and the seal finishes as not permitted.
	refused := sealStart("seal-1")
	_, err := f.Store.Publish(ctx, publication(refused, first, payload))
	is(t, err, receipts.ErrNotPermitted, "publishing without a grant")
	if f.Published(t, brief) {
		t.Fatal("a refused publication wrote a card")
	}
	if r := readOne(t, f, refused.AuditID); *r.HTTPStatus != 403 || *r.Error != "not_permitted" {
		t.Fatalf("the refused seal: %s", encode(t, r.Value()))
	}
	f.Grant(t)
	f.Bump(t)
	changed := sealStart("seal-2")
	_, err = f.Store.Publish(ctx, publication(changed, first, payload))
	is(t, err, receipts.ErrSourceChanged, "publishing across a source change")
	if r := readOne(t, f, changed.AuditID); *r.HTTPStatus != 409 || *r.Error != "source_changed" || f.Published(t, brief) {
		t.Fatalf("the fenced seal: %s", encode(t, r.Value()))
	}
	current := read("op-read-2")
	seal := sealStart("seal-3")
	p := publication(seal, current, payload)
	card, err := f.Store.Publish(ctx, p)
	if err != nil || card.Seal.Status != "ok" || card.Seal.Rows[0] != receipts.Reference(brief, payload) ||
		card.SHA256 != card.Seal.Rows[0][len(card.Seal.Rows[0])-64:] || string(card.Bytes) != string(payload) ||
		!f.Published(t, brief) {
		t.Fatalf("publish: %+v %v", card, err)
	}
	p.Bytes[0] = 'X'
	p.Units[0].Value[0] = 'X'
	card.Bytes[0] = 'Y'
	again, err := f.Store.Publish(ctx, publication(seal, current, payload))
	if err != nil || string(again.Bytes) != string(payload) || string(again.Units[0].Value) != unit ||
		!again.Seal.Same(card.Seal) {
		t.Fatalf("a retried publication: %+v %v", again, err)
	}
	other := publication(seal, current, []byte(`{"units": [`+unit+`], "gaps": [], "other": 1}`))
	_, err = f.Store.Publish(ctx, other)
	is(t, err, receipts.ErrFinished, "publishing other bytes under a finished seal")
	_, err = f.Store.Publish(ctx, publication(sealStart("seal-4"), current, payload))
	is(t, err, receipts.ErrInvalid, "publishing a brief twice")
}
