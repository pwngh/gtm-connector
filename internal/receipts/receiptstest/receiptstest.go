// Package receiptstest is the contract every receipts.Store keeps, written once and run against each store.
// Each case changes one thing, so a store that skips one rule fails one case.
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
	// Workspace holds Principal, Stranger (a second principal), Account, OtherAccount and Connection, a CRM
	// connection; Other is a second workspace, with OtherConnection, a CRM connection of its own.
	Workspace, Other            string
	Principal, Stranger         string
	Account, OtherAccount       string
	Connection, OtherConnection string
	// Bump changes Connection's source: its generation goes up.
	Bump func(t *testing.T)
	// Grant gives Principal a grant on Account; it starts without one, and Stranger never holds one.
	Grant func(t *testing.T)
	// Committed counts the receipts the store holds.
	Committed func(t *testing.T) int
	// Published is whether a card of this brief was written in Workspace.
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
		{"OperationsBelongToTheirWorkspace", operationsBelongToTheirWorkspace},
		{"GenerationFence", generationFence},
		{"ChildReadsAreFenced", childReadsAreFenced},
		{"VersionConflicts", versionConflicts},
		{"RacingFinishersOneWins", racingFinishersOneWins},
		{"ChildReads", childReads},
		{"DenialAndNotSelected", denialAndNotSelected},
		{"OnlyReadsClaimRecordRows", onlyReadsClaimRecordRows},
		{"StartRefusesBadReceipts", startRefusesBadReceipts},
		{"ReceiptsAreDetached", receiptsAreDetached},
		{"PublishACard", publishACard},
		{"PublishAPreparedBrief", publishAPreparedBrief},
		{"PublishRefusals", publishRefusals},
		{"PublishRetries", publishRetries},
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
	b := brief
	r, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: operation,
		PrincipalID: f.Principal, TokenID: token, Tool: tool, Scope: "crm:read", AccountRef: f.Account,
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

// must is the value of a call that cannot fail here; a failure panics, which fails the test with its stack.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func readOne(t *testing.T, f *Fixture, id string) receipts.Receipt {
	t.Helper()
	return must(f.Store.Read(ctx, f.Workspace, []string{id}))[0]
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
		r.ConnectionID != f.Connection || r.OperationID != "op-1" || r.Replay {
		t.Fatalf("start recorded %+v", r)
	}
	_, err := f.Store.Finish(ctx, receipts.Ticket{AuditID: r.AuditID, Lease: receipts.RandomID()}, receipts.Denied())
	is(t, err, receipts.ErrLeaseLost, "a finish without the lease")
	// Three versions, bound and claimed in the order given.
	three := []receipts.RecordVersion{version(f.Account, "{}"),
		{Source: "crm", Table: "contacts", RecordID: "con-2", Body: []byte(`{"c": 2}`)},
		{Source: "crm", Table: "contacts", RecordID: "con-1", Body: []byte(`{"c": 1}`)}}
	done, err := f.Store.FinishRead(ctx, r.Ticket(), three, 0)
	if err != nil || done.Status != "ok" ||
		strings.Join(done.Rows, " ") != "crm/accounts/"+f.Account+" crm/contacts/con-2 crm/contacts/con-1" {
		t.Fatalf("finish: %+v %v", done, err)
	}
	// A retry after a lost acknowledgement returns the finished receipt.
	again, err := f.Store.FinishRead(ctx, r.Ticket(), three, 0)
	if err != nil || !again.Same(done) {
		t.Fatalf("the identical retry: %+v %v", again, err)
	}
	for _, other := range [][]receipts.RecordVersion{{version(f.Account, `{"a": 1}`)}, {version("acc-002", "{}")},
		{three[0], three[2], three[1]}} {
		_, err := f.Store.FinishRead(ctx, r.Ticket(), other, 0)
		is(t, err, receipts.ErrFinished, "a retry with other records")
	}
	// After the finish, only the lease holder hears back.
	_, err = f.Store.FinishRead(ctx, receipts.Ticket{AuditID: r.AuditID, Lease: receipts.RandomID()}, three, 0)
	is(t, err, receipts.ErrLeaseLost, "the identical retry without the lease")
	_, err = f.Store.Finish(ctx, receipts.Ticket{AuditID: r.AuditID, Lease: receipts.RandomID()}, receipts.Denied())
	is(t, err, receipts.ErrLeaseLost, "another outcome without the lease")
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
	bound := must(f.Store.Records(ctx, f.Workspace, r.AuditID))
	if len(bound) != 3 {
		t.Fatalf("bound %d versions", len(bound))
	}
	for i, v := range bound {
		if v.Ref() != three[i].Ref() || v.Version() != three[i].Version() || string(v.Body) != string(three[i].Body) {
			t.Fatalf("bound version %d: %+v", i, v)
		}
	}
	_, err = f.Store.Records(ctx, f.Other, r.AuditID)
	is(t, err, receipts.ErrNotFound, "another workspace's bindings")
}

func operationRetry(t *testing.T, f *Fixture) {
	first := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	again := start(t, f, "op-1", "get_account", receipts.RPC(1), f.Connection)
	if again.AuditID != first.AuditID || again.Lease != first.Lease || !again.Replay || f.Committed(t) != 1 {
		t.Fatalf("a retried start made %+v", again)
	}
	b, other := brief, "brief-other"
	same := receipts.Start{WorkspaceID: f.Workspace, OperationID: "op-1", PrincipalID: f.Principal, TokenID: token,
		Tool: "get_account", Scope: "crm:read", AccountRef: f.Account, ConnectionID: f.Connection,
		Arguments: strictjson.NewObject("account_id", f.Account), RequestID: receipts.RPC(1), BriefID: &b}
	for name, change := range map[string]func(s *receipts.Start){
		"tool":       func(s *receipts.Start) { s.Tool = "list_opportunities" },
		"principal":  func(s *receipts.Start) { s.PrincipalID = f.Stranger },
		"token":      func(s *receipts.Start) { s.TokenID = receipts.Anonymous },
		"scope":      func(s *receipts.Start) { s.Scope = "calls:read" },
		"account":    func(s *receipts.Start) { s.AccountRef = f.OtherAccount },
		"connection": func(s *receipts.Start) { s.ConnectionID = "" },
		"arguments":  func(s *receipts.Start) { s.Arguments = strictjson.NewObject("account_id", f.OtherAccount) },
		"request ID": func(s *receipts.Start) { s.RequestID = receipts.RPC(2) },
		"brief ID":   func(s *receipts.Start) { s.BriefID = &other },
		"no brief":   func(s *receipts.Start) { s.BriefID = nil },
	} {
		changed := same
		change(&changed)
		_, err := f.Store.Start(ctx, changed)
		is(t, err, receipts.ErrOperationReused, "another "+name+" under the operation")
	}
	found, err := f.Store.ByOperation(ctx, f.Workspace, "op-1")
	if err != nil || found.AuditID != first.AuditID || found.Lease != "" {
		t.Fatalf("by operation: %+v %v", found, err)
	}
	_, err = f.Store.ByOperation(ctx, f.Workspace, "op-unknown")
	is(t, err, receipts.ErrNotFound, "an operation never started")
}

// operationsBelongToTheirWorkspace: one operation ID in two workspaces is two receipts, and neither
// workspace learns about, or gets the lease of, the other's.
func operationsBelongToTheirWorkspace(t *testing.T, f *Fixture) {
	mine := start(t, f, "op-shared", "get_account", receipts.RPC(1), f.Connection)
	theirs, err := f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Other, OperationID: "op-shared",
		TokenID: token, Tool: "get_account", Scope: "crm:read", ConnectionID: f.OtherConnection,
		Arguments: strictjson.NewObject(), RequestID: receipts.RPC(1)})
	if err != nil || theirs.AuditID == mine.AuditID || theirs.Lease == mine.Lease || theirs.Replay {
		t.Fatalf("the other workspace's start: %+v %v", theirs, err)
	}
	found, err := f.Store.ByOperation(ctx, f.Other, "op-shared")
	if err != nil || found.AuditID != theirs.AuditID {
		t.Fatalf("by operation in the other workspace: %+v %v", found, err)
	}
	_, err = f.Store.ByOperation(ctx, f.Other, "op-1-never")
	is(t, err, receipts.ErrNotFound, "an operation of neither workspace")
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
	if bound := must(f.Store.Records(ctx, f.Workspace, r.AuditID)); len(bound) != 0 {
		t.Fatalf("a fenced read bound %v", bound)
	}
	_, err = f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	is(t, err, receipts.ErrSourceChanged, "retrying the fenced read")
	other := start(t, f, "op-2", "get_account", receipts.RPC(2), f.Connection)
	if other.Generation != r.Generation+1 {
		t.Fatalf("a read after the change records generation %d", other.Generation)
	}
	must(f.Store.FinishRead(ctx, other.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0))
}

// childReadsAreFenced: a read inside a composite call is fenced like any other.
func childReadsAreFenced(t *testing.T, f *Fixture) {
	parent := start(t, f, "op-1", receipts.Composite, receipts.RPC(1), "")
	b := brief
	child := must(f.Store.StartChild(ctx, receipts.ChildStart{Parent: parent.AuditID, Ordinal: 1,
		Tool: "get_account", Scope: "crm:read", ConnectionID: f.Connection,
		Arguments: strictjson.NewObject("account_id", f.Account), BriefID: &b}))
	if child.Generation != 1 || child.ConnectionID != f.Connection {
		t.Fatalf("child: %+v", child)
	}
	f.Bump(t)
	_, err := f.Store.FinishRead(ctx, child.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	is(t, err, receipts.ErrSourceChanged, "a child read across a source change")
	read := readOne(t, f, child.AuditID)
	if read.Status != "error" || *read.HTTPStatus != 409 || *read.Error != "source_changed" ||
		len(must(f.Store.Records(ctx, f.Workspace, child.AuditID))) != 0 {
		t.Fatalf("the fenced child: %s", encode(t, read.Value()))
	}
}

// versionConflicts: a version that arrives with two bodies stores nothing and finishes the read 409.
func versionConflicts(t *testing.T, f *Fixture) {
	for i, versions := range [][][]receipts.RecordVersion{
		// within one read
		{{version(f.Account, "{}"), {Source: "crm", Table: "accounts", RecordID: f.Account,
			ContentVersion: version(f.Account, "{}").Version(), Body: []byte("[]")}}},
		// against an earlier read
		{{version("acc-009", "{}")}, {{Source: "crm", Table: "accounts", RecordID: "acc-009",
			ContentVersion: version("acc-009", "{}").Version(), Body: []byte("[]")}}},
	} {
		var last receipts.Receipt
		var err error
		for j, read := range versions {
			last = start(t, f, fmt.Sprintf("op-%d-%d", i, j), "get_account", receipts.RPC(j+1), f.Connection)
			_, err = f.Store.FinishRead(ctx, last.Ticket(), read, 0)
		}
		is(t, err, receipts.ErrVersionConflict, "one version with two bodies")
		conflicted := readOne(t, f, last.AuditID)
		if conflicted.Status != "error" || *conflicted.HTTPStatus != 409 || *conflicted.Error != "version_conflict" ||
			len(must(f.Store.Records(ctx, f.Workspace, last.AuditID))) != 0 {
			t.Fatalf("the conflicted read: %s", encode(t, conflicted.Value()))
		}
	}
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
		first.OperationID != "op-1#1" || first.Lease == "" || first.BriefID == nil || *first.BriefID != brief ||
		first.Generation != 1 || first.ConnectionID != f.Connection || first.AccountRef != f.Account || first.Replay {
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
	// A child whose own tool is the composite one is still a child, and takes no child of its own.
	composite := must(f.Store.StartChild(ctx, receipts.ChildStart{Parent: parent.AuditID, Ordinal: 2,
		Tool: receipts.Composite, Scope: "crm:read", ConnectionID: f.Connection, Arguments: strictjson.NewObject()}))
	_, err = child(composite.AuditID, 1)
	is(t, err, receipts.ErrParent, "a child of a composite child")
	seal := start(t, f, "op-seal", receipts.SaveBrief, receipts.Null(), "")
	_, err = child(seal.AuditID, 1)
	is(t, err, receipts.ErrParent, "a child of a seal")
	_, err = f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "op-x", TokenID: token,
		Tool: "get_account", Arguments: strictjson.NewObject(), RequestID: receipts.Child(parent.AuditID, 9)})
	is(t, err, receipts.ErrInvalid, "a child request ID through Start")
	_, err = f.Store.Finish(ctx, parent.Ticket(), receipts.Failed(200, "request_failed"))
	is(t, err, receipts.ErrInvalid, "finishing a parent with a pending child")
	denied := must(f.Store.Finish(ctx, first.Ticket(), receipts.Denied()))
	must(f.Store.Finish(ctx, composite.Ticket(), receipts.Denied()))
	must(f.Store.Finish(ctx, parent.Ticket(), receipts.Failed(200, "request_failed")))
	_, err = child(parent.AuditID, 3)
	is(t, err, receipts.ErrParent, "a child of a finished parent")
	// A finished child's outcome stands, after its parent finished too, without its lease.
	again, err := child(parent.AuditID, 1)
	if err != nil || again.AuditID != first.AuditID || again.Lease != "" || !again.Replay || !again.Same(denied) {
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

// onlyReadsClaimRecordRows: a receipt that reads no source cannot claim a record row, and a read claims its
// rows only through FinishRead.
func onlyReadsClaimRecordRows(t *testing.T, f *Fixture) {
	call := start(t, f, "op-call", "get_account", receipts.RPC(1), "")
	for _, row := range []string{"crm/accounts/" + f.Account, "calls/call_notes/note-1", "slack/messages/m-1"} {
		_, err := f.Store.Finish(ctx, call.Ticket(), receipts.Outcome{Status: "ok", HTTPStatus: 200,
			Rows: []string{row}})
		is(t, err, receipts.ErrInvalid, "a call with no connection claiming "+row)
	}
	read := start(t, f, "op-read", "get_account", receipts.RPC(2), f.Connection)
	for _, rows := range [][]string{{"crm/accounts/" + f.Account}, {}} {
		_, err := f.Store.Finish(ctx, read.Ticket(), receipts.Outcome{Status: "ok", HTTPStatus: 200, Rows: rows})
		is(t, err, receipts.ErrInvalid, "a read finished ok without FinishRead")
	}
	_, err := f.Store.FinishRead(ctx, call.Ticket(), []receipts.RecordVersion{version(f.Account, "{}")}, 0)
	is(t, err, receipts.ErrInvalid, "FinishRead on a call that reads no source")
	// What a seal names is not a record.
	must(f.Store.Finish(ctx, call.Ticket(), receipts.Outcome{Status: "ok", HTTPStatus: 200,
		Rows: []string{"briefs/" + brief + "/" + strings.Repeat("0", 64)}}))
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
	_, err = f.Store.Start(ctx, receipts.Start{WorkspaceID: w, OperationID: "g", TokenID: a, Tool: "get_account",
		Scope: "crm:read", ConnectionID: f.OtherConnection, Arguments: strictjson.NewObject()})
	is(t, err, receipts.ErrNotFound, "another workspace's connection")
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
	started := must(f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "op",
		TokenID: token, Tool: "get_account", Arguments: arguments, RequestID: receipts.RPC(id), BriefID: &b}))
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
	finished := must(f.Store.Finish(ctx, started.Ticket(), receipts.Outcome{Status: "error", HTTPStatus: 500,
		Rows: []string{}, Error: &code}))
	code = "changed"
	*finished.HTTPStatus = 200
	*finished.Error = "changed"
	*finished.ElapsedMS = 5
	read := must(f.Store.Read(ctx, f.Workspace, []string{started.AuditID}))
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
