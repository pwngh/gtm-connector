package receipts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

func fixed() *Memory {
	n, second := 0, 0
	epoch := time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC)
	return NewMemory(func() time.Time {
		second++
		return epoch.Add(time.Duration(second-1) * time.Second)
	}, func() string {
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
	})
}

var token = Fingerprint("a bearer")

func start(t *testing.T, m *Memory, tool string, id RequestID) Receipt {
	t.Helper()
	brief := "brief-0123456789abcdef0123456789abcdef"
	r, err := m.Start(context.Background(), Start{TokenID: token, Tool: tool,
		Arguments: strictjson.NewObject("account_id", "acc-001"), RequestID: id, BriefID: &brief})
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

func TestPendingThenFinishedOnce(t *testing.T) {
	m, ctx := fixed(), context.Background()
	r := start(t, m, "get_account", RPC(1))
	want := `{"audit_id": "00000000-0000-4000-8000-000000000001", "started_at": "2027-01-15T15:00:00Z", ` +
		`"token_id": "` + token + `", "tool": "get_account", "arguments": {"account_id": "acc-001"}, ` +
		`"request_id": 1, "brief_id": "brief-0123456789abcdef0123456789abcdef", "status": "pending", ` +
		`"http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`
	if got := encode(t, r); got != want {
		t.Fatalf("pending receipt:\n got %s\nwant %s", got, want)
	}
	done, err := m.Finish(ctx, r.AuditID, Outcome{Status: "ok", HTTPStatus: 200, Rows: []string{"crm/accounts/acc-001"}})
	if err != nil || done.Status != "ok" || len(done.Rows) != 1 {
		t.Fatalf("finish: %+v %v", done, err)
	}
	for _, o := range []Outcome{{Status: "ok", HTTPStatus: 200}, Denied()} {
		if _, err := m.Finish(ctx, r.AuditID, o); !errors.Is(err, ErrFinished) {
			t.Fatalf("a second finish: %v", err)
		}
	}
	if _, err := m.Finish(ctx, "missing", Denied()); !errors.Is(err, ErrFinished) {
		t.Fatalf("finishing an unknown receipt: %v", err)
	}
	if _, err := m.Finish(ctx, r.AuditID, Outcome{Status: "pending", HTTPStatus: 200}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pending as a final status: %v", err)
	}
	read, err := m.Read(ctx, []string{r.AuditID})
	if err != nil || read[0].Status != "ok" || *read[0].HTTPStatus != 200 {
		t.Fatalf("read: %+v %v", read, err)
	}
	if _, err := m.Read(ctx, []string{r.AuditID, "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a missing receipt: %v", err)
	}
}

func TestRacingFinishersOneWins(t *testing.T) {
	m := fixed()
	r := start(t, m, "get_account", RPC(1))
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := Outcome{Status: "ok", HTTPStatus: 200}
			if i%2 == 1 {
				o = Denied()
			}
			if _, err := m.Finish(context.Background(), r.AuditID, o); err == nil {
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
	m := fixed()
	parent := start(t, m, "prepare_renewal_brief", RPC(7))
	for _, c := range []struct {
		id   RequestID
		want string
	}{
		{RPC(3), `3`},
		{RPC("abc"), `"abc"`},
		{RPC(nil), `null`},
		{Null(), `null`},
		{RPC(1.5), `"<invalid>"`},
		{RPC(true), `"<invalid>"`},
		{RPC([]any{1}), `"<invalid>"`},
		{Invalid(), `"<invalid>"`},
		{Child(parent.AuditID, 1), `{"kind": "brief_child", "parent_audit_id": "` + parent.AuditID + `", "ordinal": 1}`},
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
	if _, err := ParseRequestID(strictjson.NewObject("kind", "brief_child", "parent_audit_id", "x")); err == nil {
		t.Error("a child request_id without its ordinal was read")
	}
}

func TestChildReads(t *testing.T) {
	m, ctx := fixed(), context.Background()
	parent := start(t, m, "prepare_renewal_brief", RPC(1))
	first := start(t, m, "get_account", Child(parent.AuditID, 1))
	if got, ordinal, ok := first.RequestID.Parent(); !ok || got != parent.AuditID || ordinal != 1 {
		t.Fatalf("child parent: %v %v %v", got, ordinal, ok)
	}
	start(t, m, "list_opportunities", Child(parent.AuditID, 2))
	try := func(id RequestID) error {
		_, err := m.Start(ctx, Start{TokenID: Anonymous, Tool: "read_call_notes", Arguments: strictjson.NewObject(),
			RequestID: id})
		return err
	}
	if err := try(Child(parent.AuditID, 2)); !errors.Is(err, ErrDuplicateChild) {
		t.Fatalf("a second child with ordinal 2: %v", err)
	}
	if err := try(Child("missing", 1)); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a missing parent: %v", err)
	}
	if err := try(Child(parent.AuditID, 0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ordinal 0: %v", err)
	}
	if _, err := m.Finish(ctx, parent.AuditID, Outcome{Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/x/y"}}); err != nil {
		t.Fatal(err)
	}
	if err := try(Child(parent.AuditID, 3)); !errors.Is(err, ErrParent) {
		t.Fatalf("a child of a finished parent: %v", err)
	}
}

func TestDenialAndNotSelected(t *testing.T) {
	m, ctx := fixed(), context.Background()
	for _, c := range []struct {
		outcome Outcome
		status  int
	}{{Denied(), 403}, {NotSelected(), 200}} {
		r := start(t, m, "read_slack_messages", RPC(4))
		done, err := m.Finish(ctx, r.AuditID, c.outcome)
		if err != nil || done.Status != "error" || *done.HTTPStatus != c.status || len(done.Rows) != 0 ||
			*done.Error != "request_failed" {
			t.Fatalf("%d: %+v %v", c.status, done, err)
		}
	}
	r := start(t, m, "read_slack_messages", RPC(5))
	refused := Denied()
	refused.Rows = []string{"slack/messages/msg-001"}
	if _, err := m.Finish(ctx, r.AuditID, refused); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an error receipt with rows: %v", err)
	}
}

func TestStartRefusesBadReceipts(t *testing.T) {
	m := fixed()
	for _, s := range []Start{
		{TokenID: "raw-token", Tool: "get_account", Arguments: strictjson.NewObject()},
		{TokenID: Anonymous, Tool: "", Arguments: strictjson.NewObject()},
		{TokenID: Anonymous, Tool: "get_account", Arguments: []any{func() {}}},
	} {
		if _, err := m.Start(context.Background(), s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if len(m.All()) != 0 {
		t.Fatal("a refused start left a receipt")
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
