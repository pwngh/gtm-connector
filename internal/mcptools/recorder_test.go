package mcptools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The recorder on the in-memory receipt store, which keeps the same contract as PostgreSQL.

const (
	testWorkspace = "workspace-1"
	testCRM       = "connection-crm"
	knownBearer   = "a-known-bearer-of-the-workspace"
)

// oneToken is a token table that holds one bearer.
type oneToken struct{ Storage }

func (oneToken) MachineToken(_ context.Context, workspaceID string, digest []byte) (postgres.MachineToken, error) {
	if known := sha256.Sum256([]byte(knownBearer)); workspaceID != testWorkspace || string(digest) != string(known[:]) {
		return postgres.MachineToken{}, postgres.ErrNotFound
	}
	// Not usable: a receipt names who presented a bearer, whether or not it admits anything.
	return postgres.MachineToken{WorkspaceID: testWorkspace, PrincipalID: "principal-1"}, nil
}

func newRecorder(t *testing.T) (*recorder, *receipts.Memory) {
	t.Helper()
	store := receipts.NewMemory(nil, nil)
	store.SetGeneration(testWorkspace, testCRM, 1)
	return &recorder{store: store, workspace: testWorkspace, tokens: oneToken{}, connections: map[string]string{"crm": testCRM},
		open: map[string]*pending{}}, store
}

func stored(t *testing.T, store *receipts.Memory, id string) receipts.Receipt {
	t.Helper()
	found, err := store.Read(context.Background(), testWorkspace, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return found[0]
}

func version(id string) receipts.RecordVersion {
	return receipts.RecordVersion{Source: "crm", Table: "accounts", RecordID: id, Body: []byte(`{"id":"` + id + `"}`)}
}

// What a call's receipt records: who presented the bearer when the workspace knows it, the tool's scope and
// source, the account it names, and the arguments, the request ID and the brief ID as given.
func TestStartRecordsTheCallAsGiven(t *testing.T) {
	ctx := context.Background()
	r, store := newRecorder(t)
	known, unknown, brief := knownBearer, "never-issued", "brief-of-the-caller"
	arguments := strictjson.NewObject("account_id", "acc-003", "as_of", nil)
	for _, c := range []struct {
		label     string
		start     mcp.Start
		tool      string
		principal string
		token     string
		scope     string
		source    bool
		account   string
		request   any
		brief     *string
	}{
		{"a direct read", mcp.Start{Token: &known, Tool: "get_account", Arguments: arguments, RequestID: json.Number("7"), BriefID: &brief},
			"get_account", "principal-1", receipts.Fingerprint(known), "crm:read", true, "acc-003", "7", &brief},
		{"an unknown bearer", mcp.Start{Token: &unknown, Tool: "get_account", Arguments: arguments, RequestID: "seven"},
			"get_account", "", receipts.Fingerprint(unknown), "crm:read", true, "acc-003", `"seven"`, nil},
		{"no bearer", mcp.Start{Tool: "read_call_notes", Arguments: arguments},
			"read_call_notes", "", receipts.Anonymous, "calls:read", false, "acc-003", "null", nil},
		{"a prepared brief, whose receipt is its seal", mcp.Start{Token: &known, Tool: receipts.Composite, Arguments: arguments,
			RequestID: json.Number("100000000000000000000"), BriefID: &brief},
			receipts.Composite, "principal-1", receipts.Fingerprint(known), "crm:read", false, "acc-003", "100000000000000000000", nil},
		{"the write tool", mcp.Start{Token: &known, Tool: "log_activity", Arguments: arguments, RequestID: "<invalid>"},
			"log_activity", "principal-1", receipts.Fingerprint(known), "", false, "", `"<invalid>"`, nil},
		{"an id that is no id", mcp.Start{Tool: "nope", Arguments: "x", RequestID: 1.5}, "nope", "", receipts.Anonymous, "", false, "", `"<invalid>"`, nil},
		{"a tool name no receipt holds", mcp.Start{Tool: strings.Repeat("x", 101), Arguments: nil, RequestID: "a\x00b"},
			"<invalid>", "", receipts.Anonymous, "", false, "", `"<invalid>"`, nil},
		{"an empty tool name", mcp.Start{Tool: ""}, "<invalid>", "", receipts.Anonymous, "", false, "", "null", nil},
	} {
		id, err := r.Start(ctx, c.start)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		got := stored(t, store, id)
		request, _ := strictjson.Dumps(got.RequestID.Value(), strictjson.Quoted)
		if got.Tool != c.tool || got.PrincipalID != c.principal || got.TokenID != c.token || got.Scope != c.scope ||
			(got.ConnectionID == testCRM) != c.source || got.AccountRef != c.account || request != c.request ||
			!reflect.DeepEqual(got.BriefID, c.brief) || got.Status != "pending" || !strings.HasPrefix(got.OperationID, "mcp-") {
			t.Errorf("%s: %+v (request_id %s)", c.label, got, request)
		}
		if given, _ := strictjson.Dumps(got.Arguments, strictjson.Quoted); c.start.Arguments == arguments && given != `{"account_id": "acc-003", "as_of": null}` {
			t.Errorf("%s: arguments %s", c.label, given)
		}
	}
}

// A released direct read finishes only by binding the records its body returned, and only when those are the
// rows the reply holds. Anything else releases nothing, and the receipt can still finish as the failure.
func TestFinishBindsWhatTheReadReturned(t *testing.T) {
	ctx := context.Background()
	r, store := newRecorder(t)
	failure := "execution_or_audit_failure"
	start := func() string {
		id, err := r.Start(ctx, mcp.Start{Tool: "get_account", Arguments: strictjson.NewObject("account_id", "acc-003")})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	id := start()
	if err := r.bind(id, []receipts.RecordVersion{version("acc-003"), version("acc-004")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200, Rows: []string{"crm/accounts/acc-003", "crm/accounts/acc-004"}}); err != nil {
		t.Fatal(err)
	}
	bound, _ := store.Records(ctx, testWorkspace, id)
	if got := stored(t, store, id); got.Status != "ok" || len(got.Rows) != 2 || len(bound) != 2 {
		t.Fatalf("a released read: %+v, %d records bound", got, len(bound))
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200}); err == nil {
		t.Fatal("a finished receipt finished again")
	}

	for label, rows := range map[string][]string{
		"a row the read did not return": {"crm/accounts/acc-003", "crm/accounts/acc-009"},
		"a row short":                   {},
		"the rows in another order":     {"crm/accounts/acc-004", "crm/accounts/acc-003"},
	} {
		id := start()
		if err := r.bind(id, []receipts.RecordVersion{version("acc-003"), version("acc-004")}); err != nil {
			t.Fatal(err)
		}
		if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200, Rows: rows}); err == nil {
			t.Fatalf("%s: the receipt finished ok", label)
		}
		if err := r.Finish(ctx, id, mcp.Finish{Status: "error", HTTPStatus: 503, Rows: []string{}, Error: &failure}); err != nil {
			t.Fatalf("%s: the failure did not finish: %v", label, err)
		}
		bound, _ := store.Records(ctx, testWorkspace, id)
		if got := stored(t, store, id); got.Status != "error" || *got.HTTPStatus != 503 || len(got.Rows) != 0 || len(bound) != 0 {
			t.Fatalf("%s: %+v, %d records bound", label, got, len(bound))
		}
	}

	// A call that is no source read has nothing to bind, and no receipt but the gateway's own is finished.
	composite, err := r.Start(ctx, mcp.Start{Tool: receipts.Composite, Arguments: strictjson.NewObject("account_id", "acc-003")})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bind(composite, nil); err == nil {
		t.Fatal("records were bound to a receipt that reads no source")
	}
	if err := r.Finish(ctx, "not-a-receipt", mcp.Finish{Status: "error", HTTPStatus: 400}); err == nil {
		t.Fatal("a receipt the gateway never started was finished")
	}
}

// A receipt its own work finished is confirmed, never rewritten: the same outcome again, or an error where
// the store recorded a refusal in its own words. A reply that would release more than the receipt says is
// refused.
func TestFinishConfirmsAReceiptItsWorkFinished(t *testing.T) {
	ctx := context.Background()
	r, store := newRecorder(t)
	failed, refused := "request_failed", "not_permitted"
	start := func() (string, receipts.Ticket) {
		id, err := r.Start(ctx, mcp.Start{Tool: receipts.Composite, Arguments: strictjson.NewObject("account_id", "acc-003")})
		if err != nil {
			t.Fatal(err)
		}
		outer, _ := r.outer(id)
		return id, outer.Ticket()
	}
	rows := []string{"briefs/brief-1/" + strings.Repeat("a", 64)}

	id, ticket := start()
	if _, err := store.Finish(ctx, ticket, receipts.Outcome{Status: "ok", HTTPStatus: 200, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "error", HTTPStatus: 200, Rows: []string{}, Error: &failed}); err == nil {
		t.Fatal("an error was confirmed over a receipt that finished ok")
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/brief-2/" + strings.Repeat("b", 64)}}); err == nil {
		t.Fatal("another brief was confirmed over the sealed one")
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200, Rows: rows, ElapsedMs: 5}); err != nil {
		t.Fatalf("the seal's own outcome was not confirmed: %v", err)
	}

	id, ticket = start()
	if _, err := store.Finish(ctx, ticket, receipts.Outcome{Status: "error", HTTPStatus: 403, Rows: []string{}, Error: &refused}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "ok", HTTPStatus: 200, Rows: rows}); err == nil {
		t.Fatal("a release was confirmed over a refusal")
	}
	if err := r.Finish(ctx, id, mcp.Finish{Status: "error", HTTPStatus: 200, Rows: []string{}, Error: &failed}); err != nil {
		t.Fatalf("an error reply over the store's refusal: %v", err)
	}
	if got := stored(t, store, id); *got.HTTPStatus != 403 || *got.Error != refused {
		t.Fatalf("the refusal was rewritten: %+v", got)
	}
}
