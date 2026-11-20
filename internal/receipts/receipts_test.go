package receipts

import (
	"context"
	"errors"
	"fmt"
	"math/big"
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

// TestMemoryFixesIDsAndClock shows the IDs and times a Memory gives when they are fixed, as the oracle fixes
// them. The rest of the contract is in receiptstest, run on every store.
func TestMemoryFixesIDsAndClock(t *testing.T) {
	m := fixed()
	r := start(t, m, "op-1", "get_account", RPC(1))
	want := `{"audit_id": "00000000-0000-4000-8000-000000000001", "started_at": "2027-01-15T15:00:00Z", ` +
		`"token_id": "` + token + `", "tool": "get_account", "arguments": {"account_id": "acc-001"}, ` +
		`"request_id": 1, "brief_id": "brief-0123456789abcdef0123456789abcdef", "status": "pending", ` +
		`"http_status": null, "row_count": 0, "rows": [], "elapsed_ms": null, "error": null}`
	if got := encode(t, r); got != want {
		t.Fatalf("pending receipt:\n got %s\nwant %s", got, want)
	}
	if second := start(t, m, "op-2", "get_account", RPC(2)); second.AuditID != "00000000-0000-4000-8000-000000000002" ||
		StartedAtText(second.StartedAt) != "2027-01-15T15:00:01Z" {
		t.Fatalf("the second receipt: %s", encode(t, second))
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
