package receipts

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

const workspace = "ws-1"

func fixed() *Memory {
	n, second := 0, 0
	epoch := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC)
	m := NewMemory(func() time.Time {
		second++
		return epoch.Add(time.Duration(second-1) * time.Second)
	}, func() string {
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
	})
	m.SetGeneration("crm", 1)
	return m
}

var token = Fingerprint("a bearer")

func brief() *string {
	b := "brief-0123456789abcdef0123456789abcdef"
	return &b
}

func start(t *testing.T, m *Memory, operation, tool string, id RequestID) Receipt {
	t.Helper()
	r, err := m.Start(context.Background(), Start{WorkspaceID: workspace, OperationID: operation, PrincipalID: "p1",
		TokenID: token, Tool: tool, Scope: "crm:read", AccountRef: "acc-001", ConnectionID: "crm",
		Arguments: strictjson.NewObject("account_id", "acc-001"), RequestID: id, BriefID: brief()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func encode(t *testing.T, r Receipt) string {
	t.Helper()
	text, err := strictjson.Dumps(r.Value(), strictjson.Quoted)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func version(id, body string) RecordVersion {
	return RecordVersion{Source: "crm", Table: "accounts", RecordID: id, Body: []byte(body)}
}

func TestPendingThenFinishedOnce(t *testing.T) {
	m, ctx := fixed(), context.Background()
	r := start(t, m, "op-1", "get_account", RPC(1))
	want := `{"audit_id": "00000000-0000-4000-8000-000000000001", "started_at": "2027-01-15T15:00:00Z", ` +
		`"token_id": "` + token + `", "tool": "get_account", "arguments": {"account_id": "acc-001"}, ` +
		`"request_id": 1, "brief_id": "brief-0123456789abcdef0123456789abcdef", "status": "pending", ` +
		`"http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`
	if got := encode(t, r); got != want {
		t.Fatalf("pending receipt:\n got %s\nwant %s", got, want)
	}
	if r.Generation != 1 || r.WorkspaceID != workspace || r.PrincipalID != "p1" || r.Lease == "" {
		t.Fatalf("start recorded %+v", r)
	}
	if _, err := m.Finish(ctx, Ticket{r.AuditID, "another lease"}, Denied()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a finish without the lease: %v", err)
	}
	done, err := m.FinishRead(ctx, r.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0)
	if err != nil || done.Status != "ok" || len(done.Rows) != 1 || done.Rows[0] != "crm/accounts/acc-001" {
		t.Fatalf("finish: %+v %v", done, err)
	}
	// A retry after a lost acknowledgement returns the finished receipt.
	again, err := m.FinishRead(ctx, r.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0)
	if err != nil || !again.Same(done) {
		t.Fatalf("the identical retry: %+v %v", again, err)
	}
	for _, other := range [][]RecordVersion{{version("acc-001", `{"a": 1}`)}, {version("acc-002", "{}")}} {
		if _, err := m.FinishRead(ctx, r.Ticket(), other, 0); !errors.Is(err, ErrFinished) {
			t.Fatalf("a retry with other records: %v", err)
		}
	}
	if _, err := m.Finish(ctx, r.Ticket(), Denied()); !errors.Is(err, ErrFinished) {
		t.Fatalf("a second finish with another outcome: %v", err)
	}
	if _, err := m.Finish(ctx, Ticket{"missing", ""}, Denied()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finishing an unknown receipt: %v", err)
	}
	if _, err := m.Finish(ctx, r.Ticket(), Outcome{Status: "pending", HTTPStatus: 200}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pending as a final status: %v", err)
	}
	read, err := m.Read(ctx, workspace, []string{r.AuditID})
	if err != nil || read[0].Status != "ok" || read[0].Lease != "" {
		t.Fatalf("read: %+v %v", read, err)
	}
	if _, err := m.Read(ctx, "ws-2", []string{r.AuditID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading another workspace's receipt: %v", err)
	}
	bound, _ := m.Records(workspace, r.AuditID)
	if len(bound) != 1 || bound[0].Version() == "" || string(bound[0].Body) != "{}" {
		t.Fatalf("bound versions: %+v", bound)
	}
}

func TestOperationRetry(t *testing.T) {
	m, ctx := fixed(), context.Background()
	first := start(t, m, "op-1", "get_account", RPC(1))
	again := start(t, m, "op-1", "get_account", RPC(1))
	if again.AuditID != first.AuditID || again.Lease != first.Lease || len(m.All()) != 1 {
		t.Fatalf("a retried start made %+v", again)
	}
	if _, err := m.Start(ctx, Start{WorkspaceID: workspace, OperationID: "op-1", TokenID: token, Tool: "list_opportunities",
		Arguments: strictjson.NewObject()}); !errors.Is(err, ErrOperationReused) {
		t.Fatalf("another request under the operation: %v", err)
	}
	found, err := m.ByOperation(ctx, "op-1")
	if err != nil || found.AuditID != first.AuditID || found.Lease != first.Lease {
		t.Fatalf("by operation: %+v %v", found, err)
	}
}

func TestGenerationFence(t *testing.T) {
	m, ctx := fixed(), context.Background()
	r := start(t, m, "op-1", "get_account", RPC(1))
	m.SetGeneration("crm", 2)
	if _, err := m.FinishRead(ctx, r.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("a read across a source change: %v", err)
	}
	read, _ := m.Read(ctx, workspace, []string{r.AuditID})
	if read[0].Status != "error" || *read[0].HTTPStatus != 409 || *read[0].Error != "source_changed" || len(read[0].Rows) != 0 {
		t.Fatalf("the fenced receipt: %s", encode(t, read[0]))
	}
	if bound, _ := m.Records(workspace, r.AuditID); len(bound) != 0 {
		t.Fatalf("a fenced read bound %v", bound)
	}
	if _, err := m.FinishRead(ctx, r.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("retrying the fenced read: %v", err)
	}
	other := start(t, m, "op-2", "get_account", RPC(2))
	if _, err := m.FinishRead(ctx, other.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0); err != nil {
		t.Fatal(err)
	}
	third := start(t, m, "op-3", "get_account", RPC(3))
	if _, err := m.FinishRead(ctx, third.Ticket(), []RecordVersion{version("acc-001", "{}"), {Source: "crm",
		Table: "accounts", RecordID: "acc-001", ContentVersion: version("acc-001", "{}").Version(), Body: []byte("[]")}},
		0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("one version with two bodies: %v", err)
	}
}

func TestRacingFinishersOneWins(t *testing.T) {
	m := fixed()
	r := start(t, m, "op-1", "get_account", RPC(1))
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := Failed(500, fmt.Sprintf("worker %d", i))
			if _, err := m.Finish(context.Background(), r.Ticket(), o); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d finishers won", wins.Load())
	}
}

func TestRequestIDForms(t *testing.T) {
	var zero RequestID
	var typedNil *big.Int
	for _, c := range []struct {
		id   RequestID
		want string
	}{
		{zero, `null`},
		{RPC(typedNil), `null`},
		{RPC(3), `3`},
		{RPC("abc"), `"abc"`},
		{RPC(nil), `null`},
		{Null(), `null`},
		{RPC(1.5), `"<invalid>"`},
		{RPC(true), `"<invalid>"`},
		{RPC([]any{1}), `"<invalid>"`},
		{Invalid(), `"<invalid>"`},
		{Child("p", 1), `{"kind": "brief_child", "parent_audit_id": "p", "ordinal": 1}`},
	} {
		got, err := strictjson.Dumps(c.id.Value(), strictjson.Quoted)
		if err != nil || got != c.want {
			t.Errorf("request_id %s: got %s, %v", c.want, got, err)
		}
		parsed, err := strictjson.LoadsText(got)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseRequestID(parsed)
		if again, _ := strictjson.Dumps(back.Value(), strictjson.Quoted); err != nil || again != c.want {
			t.Errorf("request_id %s read back as %s, %v", c.want, again, err)
		}
	}
	for _, bad := range []*strictjson.Object{
		strictjson.NewObject("kind", "brief_child", "parent_audit_id", "x"),
		strictjson.NewObject("kind", "brief_child", "parent_audit_id", "", "ordinal", 1),
		strictjson.NewObject("kind", "brief_child", "parent_audit_id", "x", "ordinal", 0),
		strictjson.NewObject("kind", "brief_child", "parent_audit_id", "x", "ordinal", true),
	} {
		if _, err := ParseRequestID(bad); err == nil {
			t.Errorf("read %v", bad.Keys)
		}
	}
}

func TestChildReads(t *testing.T) {
	m, ctx := fixed(), context.Background()
	parent := start(t, m, "op-1", Composite, RPC(1))
	child := func(parent string, ordinal int) (Receipt, error) {
		return m.StartChild(ctx, ChildStart{Parent: parent, Ordinal: ordinal, Tool: "get_account", Scope: "crm:read",
			ConnectionID: "crm", Arguments: strictjson.NewObject("account_id", "acc-001"), BriefID: brief()})
	}
	first, err := child(parent.AuditID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, ordinal, ok := first.RequestID.Parent(); !ok || got != parent.AuditID || ordinal != 1 ||
		first.TokenID != parent.TokenID || first.PrincipalID != parent.PrincipalID || first.WorkspaceID != workspace {
		t.Fatalf("child: %+v", first)
	}
	if again, err := child(parent.AuditID, 1); err != nil || again.AuditID != first.AuditID {
		t.Fatalf("a retried child start: %+v %v", again, err)
	}
	if _, err := m.StartChild(ctx, ChildStart{Parent: parent.AuditID, Ordinal: 1, Tool: "list_opportunities",
		Arguments: strictjson.NewObject()}); !errors.Is(err, ErrDuplicateChild) {
		t.Fatalf("another child with ordinal 1: %v", err)
	}
	if _, err := child("missing", 1); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a missing parent: %v", err)
	}
	for _, ordinal := range []int{0, -1} {
		if _, err := child(parent.AuditID, ordinal); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ordinal %d: %v", ordinal, err)
		}
	}
	if _, err := child("", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty parent: %v", err)
	}
	if _, err := child(first.AuditID, 1); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a child: %v", err)
	}
	composite, err := m.StartChild(ctx, ChildStart{Parent: parent.AuditID, Ordinal: 2, Tool: Composite,
		Arguments: strictjson.NewObject()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child(composite.AuditID, 1); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a composite child: %v", err)
	}
	seal := start(t, m, "op-seal", "save_brief", Null())
	if _, err := child(seal.AuditID, 1); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a seal: %v", err)
	}
	if _, err := m.Start(ctx, Start{WorkspaceID: workspace, OperationID: "op-x", TokenID: token, Tool: "get_account",
		Arguments: strictjson.NewObject(), RequestID: Child(parent.AuditID, 9)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a child request ID through Start: %v", err)
	}
	if _, err := m.Finish(ctx, parent.Ticket(), Failed(200, "request_failed")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finishing a parent with a pending child: %v", err)
	}
	for _, r := range []Receipt{first, composite} {
		if _, err := m.Finish(ctx, r.Ticket(), Denied()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Finish(ctx, parent.Ticket(), Failed(200, "request_failed")); err != nil {
		t.Fatal(err)
	}
	if _, err := child(parent.AuditID, 3); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a finished parent: %v", err)
	}
}

func TestDenialAndNotSelected(t *testing.T) {
	m, ctx := fixed(), context.Background()
	for i, c := range []struct {
		outcome Outcome
		status  int
	}{{Denied(), 403}, {NotSelected(), 200}} {
		r := start(t, m, fmt.Sprint("op-", i), "read_slack_messages", RPC(4))
		done, err := m.Finish(ctx, r.Ticket(), c.outcome)
		if err != nil || done.Status != "error" || *done.HTTPStatus != c.status || len(done.Rows) != 0 ||
			*done.Error != "request_failed" {
			t.Fatalf("%d: %+v %v", c.status, done, err)
		}
	}
	r := start(t, m, "op-rows", "read_slack_messages", RPC(5))
	refused := Denied()
	refused.Rows = []string{"slack/messages/msg-001"}
	if _, err := m.Finish(ctx, r.Ticket(), refused); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an error receipt with rows: %v", err)
	}
}

func TestStartRefusesBadReceipts(t *testing.T) {
	m := fixed()
	for _, s := range []Start{
		{WorkspaceID: workspace, OperationID: "a", TokenID: "raw-token", Tool: "get_account", Arguments: strictjson.NewObject()},
		{WorkspaceID: workspace, OperationID: "b", TokenID: Anonymous, Tool: "", Arguments: strictjson.NewObject()},
		{WorkspaceID: workspace, OperationID: "c", TokenID: Anonymous, Tool: "get_account", Arguments: []any{func() {}}},
		{WorkspaceID: "", OperationID: "d", TokenID: Anonymous, Tool: "get_account", Arguments: strictjson.NewObject()},
		{WorkspaceID: workspace, OperationID: "", TokenID: Anonymous, Tool: "get_account", Arguments: strictjson.NewObject()},
	} {
		if _, err := m.Start(context.Background(), s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if _, err := m.Start(context.Background(), Start{WorkspaceID: workspace, OperationID: "e", TokenID: Anonymous,
		Tool: "get_account", ConnectionID: "unknown"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown connection: %v", err)
	}
	if len(m.All()) != 0 {
		t.Fatal("a refused start left a receipt")
	}
}

// TestReceiptsAreDetached changes everything a caller handed in or was handed back, and checks that the
// committed receipt did not change.
func TestReceiptsAreDetached(t *testing.T) {
	m, ctx := fixed(), context.Background()
	arguments := strictjson.NewObject("account_id", "acc-001", "nested", []any{big.NewInt(1)})
	id := big.NewInt(7)
	b := "brief-x"
	started, err := m.Start(ctx, Start{WorkspaceID: workspace, OperationID: "op", TokenID: token, Tool: "get_account",
		Arguments: arguments, RequestID: RPC(id), BriefID: &b})
	if err != nil {
		t.Fatal(err)
	}
	before := encode(t, started)
	arguments.Set("account_id", "acc-999")
	arguments.Values[1].([]any)[0].(*big.Int).SetInt64(99)
	id.SetInt64(8)
	b = "brief-y"
	started.Arguments.(*strictjson.Object).Set("account_id", "acc-998")
	*started.BriefID = "brief-z"
	started.RequestID.Value().(*big.Int).SetInt64(9)
	value := started.Value()
	value.Set("tool", "changed")
	value.Values[4].(*strictjson.Object).Set("account_id", "acc-997")
	code := "worker"
	finished, err := m.Finish(ctx, started.Ticket(), Outcome{Status: "error", HTTPStatus: 500, Rows: []string{}, Error: &code})
	if err != nil {
		t.Fatal(err)
	}
	code = "changed"
	*finished.HTTPStatus = 200
	*finished.Error = "changed"
	*finished.ElapsedMS = 5
	for _, read := range [][]Receipt{m.All(), func() []Receipt { r, _ := m.Read(ctx, workspace, []string{started.AuditID}); return r }()} {
		read[0].Arguments.(*strictjson.Object).Set("account_id", "acc-996")
		read[0].Rows = append(read[0].Rows, "x")
	}
	again, _ := m.Read(ctx, workspace, []string{started.AuditID})
	want := before[:len(before)-len(`"status": "pending", "http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`)] +
		`"status": "error", "http_status": 500, "row_count": 0, "rows": [], "elapsed_ms": 0.0, "error": "worker"}`
	if got := encode(t, again[0]); got != want {
		t.Fatalf("the committed receipt changed:\n got %s\nwant %s", got, want)
	}
}

func TestPublish(t *testing.T) {
	m, ctx := fixed(), context.Background()
	read := start(t, m, "op-read", "get_account", RPC(1))
	if _, err := m.FinishRead(ctx, read.Ticket(), []RecordVersion{version("acc-001", "{}")}, 0); err != nil {
		t.Fatal(err)
	}
	publication := func(seal Receipt) Publication {
		return Publication{WorkspaceID: workspace, AccountRef: "acc-001", BriefID: "brief-1", PrincipalID: "p1",
			Seal: seal.Ticket(), Kind: "card", SchemaVersion: 3, Bytes: []byte("card"), Reads: []string{read.AuditID},
			Units: []UnitRow{{N: 1, Kind: "record", Value: []byte(`{}`)}}}
	}
	sealStart := func(operation string) Receipt {
		r, err := m.Start(ctx, Start{WorkspaceID: workspace, OperationID: operation, PrincipalID: "p1", TokenID: token,
			Tool: "save_brief", AccountRef: "acc-001", Arguments: strictjson.NewObject()})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// No grant: nothing is written and the seal finishes as not permitted.
	refused := sealStart("seal-1")
	if _, err := m.Publish(ctx, publication(refused)); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("publishing without a grant: %v", err)
	}
	if _, found := m.Published("brief-1"); found {
		t.Fatal("a refused publication wrote a card")
	}
	if r, _ := m.Read(ctx, workspace, []string{refused.AuditID}); *r[0].HTTPStatus != 403 || *r[0].Error != "not_permitted" {
		t.Fatalf("the refused seal: %s", encode(t, r[0]))
	}
	m.Grant(workspace, "p1", "acc-001")
	m.SetGeneration("crm", 2)
	changed := sealStart("seal-2")
	if _, err := m.Publish(ctx, publication(changed)); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("publishing across a source change: %v", err)
	}
	m.SetGeneration("crm", 1)
	seal := sealStart("seal-3")
	p := publication(seal)
	card, err := m.Publish(ctx, p)
	if err != nil || card.Seal.Status != "ok" || card.Seal.Rows[0] != Reference("brief-1", []byte("card")) {
		t.Fatalf("publish: %+v %v", card, err)
	}
	p.Bytes[0] = 'X'
	p.Units[0].Value[0] = 'X'
	card.Bytes[0] = 'Y'
	again, err := m.Publish(ctx, publication(seal))
	if err != nil || string(again.Bytes) != "card" || string(again.Units[0].Value) != "{}" {
		t.Fatalf("a retried publication: %+v %v", again, err)
	}
	other := publication(seal)
	other.Bytes = []byte("other")
	if _, err := m.Publish(ctx, other); !errors.Is(err, ErrFinished) {
		t.Fatalf("publishing other bytes under a finished seal: %v", err)
	}
}

func TestDecode(t *testing.T) {
	row := func() *strictjson.Object {
		return strictjson.NewObject("audit_id", "a", "started_at", "2027-01-15T15:00:00Z", "token_id", Anonymous,
			"tool", "get_account", "arguments", strictjson.NewObject("account_id", "acc-001"), "request_id", big.NewInt(1),
			"brief_id", nil, "status", "ok", "http_status", big.NewInt(200), "row_count", big.NewInt(1),
			"rows", []any{"crm/accounts/acc-001"}, "elapsed_ms", 0.0, "error", nil)
	}
	r, err := Decode(row())
	if err != nil || r.Status != "ok" || *r.HTTPStatus != 200 {
		t.Fatalf("decode: %+v %v", r, err)
	}
	for name, edit := range map[string]func(o *strictjson.Object){
		"row_count true":    func(o *strictjson.Object) { o.Set("row_count", true) },
		"row_count 2":       func(o *strictjson.Object) { o.Set("row_count", big.NewInt(2)) },
		"http_status true":  func(o *strictjson.Object) { o.Set("http_status", true) },
		"http_status text":  func(o *strictjson.Object) { o.Set("http_status", "200") },
		"elapsed true":      func(o *strictjson.Object) { o.Set("elapsed_ms", true) },
		"another started":   func(o *strictjson.Object) { o.Set("started_at", "2027-01-15T15:00:00+00:00") },
		"a raw token":       func(o *strictjson.Object) { o.Set("token_id", "secret") },
		"rows on an error":  func(o *strictjson.Object) { o.Set("status", "error") },
		"an extra column":   func(o *strictjson.Object) { o.Set("extra", nil) },
		"a missing column":  func(o *strictjson.Object) { o.Delete("error") },
		"an unknown status": func(o *strictjson.Object) { o.Set("status", "done") },
		"a numeric brief":   func(o *strictjson.Object) { o.Set("brief_id", big.NewInt(1)) },
	} {
		o := row()
		edit(o)
		if _, err := Decode(o); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	a, _ := Decode(row())
	float := row()
	float.Set("elapsed_ms", big.NewInt(0))
	b, _ := Decode(float)
	if !a.Same(b) {
		t.Error("elapsed_ms 0 and 0.0 differ")
	}
}

func TestStartedAtText(t *testing.T) {
	at := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC)
	if got := StartedAtText(at); got != "2027-01-15T15:00:00Z" {
		t.Error(got)
	}
	if got := StartedAtText(at.Add(1500 * time.Microsecond)); got != "2027-01-15T15:00:00.001500Z" {
		t.Error(got)
	}
	if id := RandomID(); len(id) != 36 || id[14] != '4' {
		t.Error(id)
	}
}
