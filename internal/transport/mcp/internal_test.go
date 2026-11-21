package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

func testCore(t *testing.T, receipts *MemoryRecorder, run ToolFunc) *core {
	t.Helper()
	c, err := newCore(settings{resource: testResource, rateLimit: 2, windowSeconds: 60, verify: verifierFor(fixtureTokens(testWall)),
		receipts: receipts, run: run, clock: func() float64 { return 100 }, wall: func() float64 { return testWall }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The tool handler checks the admitted scopes itself, so a tool never runs outside its grant even if a
// gateway path were to let a call through.
func TestHandlerRefusesAnUncoveredScope(t *testing.T) {
	ran := false
	run := func(context.Context, Call) (json.RawMessage, error) { ran = true; return json.RawMessage(`{}`), nil }
	inv := &Invocation{AuditID: "fixture-receipt", Tool: "read_call_notes", Scopes: []string{"crm:read"}}
	lookup := func(id string) *Invocation {
		if id == "gtm-1" {
			return inv
		}
		return nil
	}
	handler := toolHandler("read_call_notes", run, lookup)
	for _, meta := range []sdk.Meta{{dispatchKey: "gtm-1"}, {dispatchKey: "gtm-unknown"}, {}} {
		result, err := handler(context.Background(), &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Name: "read_call_notes",
			Arguments: json.RawMessage(`{"account_id":"acc-001"}`), Meta: meta}})
		if err != nil || !result.IsError || ran {
			t.Fatalf("%v: %+v %v ran=%v", meta, result, err, ran)
		}
	}
	result, _ := handler(context.Background(), &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Name: "read_call_notes",
		Arguments: json.RawMessage(`{}`), Meta: sdk.Meta{dispatchKey: "gtm-1"}}})
	if text := result.Content[0].(*sdk.TextContent).Text; text != "Error executing tool read_call_notes: Required scope: calls:read" {
		t.Fatalf("%q", text)
	}
}

// A reply for no pending request, or for one already answered, is never written.
func TestStdioNeverForwardsAnUnmatchedReply(t *testing.T) {
	receipts := &MemoryRecorder{}
	c := testCore(t, receipts, func(context.Context, Call) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	var out bytes.Buffer
	conn := newStdioConn(c, tokenValue("crm"), &out)
	for _, id := range []any{"gtm-99", int64(7)} {
		jid, _ := jsonrpc.MakeID(id)
		if err := conn.Write(context.Background(), &jsonrpc.Response{ID: jid, Result: json.RawMessage(`{"content":[]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q", out.String())
	}
}

// The seam composite reads need: a child read rechecks the caller's own bearer and charges the same window.
func TestRecheckChargesTheSameWindow(t *testing.T) {
	var h *harness
	var rechecked []string
	revoked := false
	h = newHarness(t, func(c *GatewayConfig) {
		c.RateLimit = 2
		verify := c.Verify
		c.Verify = func(raw string) *Token {
			if revoked && raw == tokenValue("calls") {
				return nil
			}
			return verify(raw)
		}
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			revoked = call.Tool == "read_call_notes" // revoked between admission and the child read
			if _, refused := call.Invocation.Recheck(); refused != nil {
				rechecked = append(rechecked, refused.Reason)
			} else {
				rechecked = append(rechecked, "ok")
			}
			return h.fx.Run(ctx, call)
		}
	})
	h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil), "crm")
	if limited := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil); limited.Code != 429 {
		t.Fatalf("the child read did not count: %d", limited.Code)
	}
	h.records(h.tool("read_call_notes", map[string]any{"account_id": "acc-001"}, "calls", nil), "calls")
	if strings.Join(rechecked, " ") != "ok invalid_token" {
		t.Fatalf("rechecks %v", rechecked)
	}
}

// A child read refuses a bearer that has come to carry a scope outside the read set, as admission refuses
// it, and charges nothing for it. Current says the same without a charge, and gives the scopes the bearer
// carries now.
func TestRecheckRefusesAScopeOutsideTheReadSet(t *testing.T) {
	var h *harness
	var seen []string
	widened := false
	h = newHarness(t, func(c *GatewayConfig) {
		verify := c.Verify
		c.Verify = func(raw string) *Token {
			token := verify(raw)
			if token != nil && widened {
				token.Scopes = append(token.Scopes, "crm:write")
			}
			return token
		}
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			seen = append(seen, fmt.Sprint(call.Invocation.Current()))
			widened = true // the bearer gains crm:write between admission and the child read
			if _, refused := call.Invocation.Recheck(); refused != nil {
				seen = append(seen, refused.Reason)
			}
			seen = append(seen, fmt.Sprint(call.Invocation.Current()))
			return h.fx.Run(ctx, call)
		}
	})
	h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil), "crm")
	if strings.Join(seen, " ") != "[crm:read] true invalid_token [] false" {
		t.Fatalf("rechecks %v", seen)
	}
	if used := h.gateway.Windows()[fingerprint(tokenValue("crm"))].Used; used != 1 {
		t.Fatalf("the refused recheck was charged: %d", used)
	}
}

// A reply is accepted only as one complete JSON-RPC object for the expected dispatch id.
func TestParseReply(t *testing.T) {
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{`{"jsonrpc":"2.0","id":"gtm-1","result":{"content":[]}}`, true},
		{`{"jsonrpc":"2.0","id":"gtm-1","error":{"code":-32603,"message":"x"}}`, true},
		{`{"jsonrpc":"2.0","id":"gtm-1","result":{"content":[]}} {}`, false},
		{`{"jsonrpc":"2.0","id":"gtm-2","result":{"content":[]}}`, false},
		{`{"jsonrpc":"2.0","id":"gtm-1","result":{},"error":{"code":1,"message":"x"}}`, false},
		{`{"jsonrpc":"2.0","id":"gtm-1","result":[]}`, false},
		{`{"jsonrpc":"2.0","id":"gtm-1"}`, false},
		{`{"id":"gtm-1","result":{}}`, false},
	} {
		if _, _, ok := parseReply([]byte(c.body), "gtm-1"); ok != c.ok {
			t.Errorf("%s: %v", c.body, ok)
		}
	}
}

// Only a result whose isError is a boolean, or absent, can be released.
func TestSettleNeedsABooleanIsError(t *testing.T) {
	receipts := &MemoryRecorder{}
	c := testCore(t, receipts, func(context.Context, Call) (json.RawMessage, error) { return nil, nil })
	for _, isError := range []string{`"false"`, `0`, `null`} {
		id, _ := c.start(context.Background(), Start{Tool: "get_account"})
		result, _ := strictjson.Loads([]byte(`{"content":[],"isError":` + isError + `}`))
		if got := c.settle(context.Background(), &Invocation{AuditID: id}, 0, result.(*strictjson.Object), nil, false); !got.withheld {
			t.Errorf("isError %s was released", isError)
		}
	}
	for _, row := range receipts.Read() {
		if row.Status != "error" || *row.HTTPStatus != 503 {
			t.Errorf("%+v", row)
		}
	}
}
