package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// rawLine returns the next stdout line as written.
func (h *stdioHarness) rawLine() string {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("stdout closed early")
		}
		return string(line)
	case <-time.After(10 * time.Second):
		h.t.Fatal("no answer")
	}
	return ""
}

// quiet reports whether stdout stays silent for a moment.
func (h *stdioHarness) quiet() bool {
	select {
	case line := <-h.lines:
		h.t.Logf("unexpected line: %.200s", line)
		return false
	case <-time.After(300 * time.Millisecond):
		return true
	}
}

func (h *stdioHarness) receiptFor(auditID string) *Receipt {
	for _, row := range h.receipts.Read() {
		if row.AuditID == auditID {
			row := row
			return &row
		}
	}
	return nil
}

// A second call with an id still in flight is refused with its own finished receipt; the first call's reply
// carries its own receipt, finished before release.
func TestStdioDuplicateInFlightIDIsRefused(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 4)
	var h *stdioHarness
	h = newStdio(t, []string{"crm:read"}, func(c *StdioConfig) {
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			started <- call.Tool
			if call.Tool == "get_account" {
				<-release
			}
			return h.fx.Run(ctx, call)
		}
	})
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(call(7, "get_account", `{"account_id":"acc-001"}`))
	<-started
	h.send(call(7, "search_accounts", `{"query":"Fixture"}`))
	duplicate := h.next()
	if fmt.Sprint(path(duplicate, "error", "code")) != "-32600" || path(duplicate, "error", "message") != "request ID already in use" ||
		fmt.Sprint(duplicate["id"]) != "7" {
		t.Fatalf("duplicate: %v", duplicate)
	}
	rows := h.receipts.Read()
	if len(rows) != 2 || rows[1].Tool != "search_accounts" || rows[1].Status != "error" || *rows[1].HTTPStatus != 400 {
		t.Fatalf("receipts %+v", rows)
	}
	close(release)
	first := h.next()
	audit, _ := path(first, "result", "_meta", "gtm.demo/auditId").(string)
	if own := h.receiptFor(audit); own == nil || own.Tool != "get_account" || own.Status != "ok" {
		t.Fatalf("first reply %v names receipt %+v", first, own)
	}
	if len(h.fx.ran()) != 1 {
		t.Fatalf("ran %v", h.fx.ran())
	}
}

// Overlapping calls with different ids, finished in the reverse order, each name and finish their own receipt.
func TestStdioOverlappingCallsKeepTheirOwnReceipts(t *testing.T) {
	gates := map[string]chan struct{}{"get_account": make(chan struct{}), "read_call_notes": make(chan struct{})}
	started := make(chan string, 4)
	var h *stdioHarness
	h = newStdio(t, []string{"crm:read", "calls:read"}, func(c *StdioConfig) {
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			started <- call.Tool
			<-gates[call.Tool]
			return h.fx.Run(ctx, call)
		}
	})
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(call(2, "get_account", `{"account_id":"acc-001"}`), call(3, "read_call_notes", `{"account_id":"acc-001"}`))
	<-started
	<-started
	close(gates["read_call_notes"])
	notes := h.answer(3)
	close(gates["get_account"])
	account := h.answer(2)
	for _, c := range []struct {
		reply map[string]any
		tool  string
	}{{account, "get_account"}, {notes, "read_call_notes"}} {
		audit, _ := path(c.reply, "result", "_meta", "gtm.demo/auditId").(string)
		own := h.receiptFor(audit)
		if own == nil || own.Tool != c.tool || own.Status != "ok" || own.RowCount == 0 {
			t.Fatalf("%s reply names receipt %+v", c.tool, own)
		}
	}
}

// The client's id comes back exactly on stdio too, and is recorded exactly.
func TestStdioIdsComeBackExactly(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	for _, id := range []string{"9007199254740993", "100000000000000000000", `"café-1"`} {
		h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`, id))
		line := h.rawLine()
		var reply struct {
			ID json.RawMessage `json:"id"`
		}
		json.Unmarshal([]byte(line), &reply)
		if string(reply.ID) != id && !(strings.HasPrefix(id, `"`) && string(reply.ID) == `"café-1"`) {
			t.Errorf("id %s came back as %s", id, reply.ID)
		}
		rows := h.receipts.Read()
		if got := jsonOf(rows[len(rows)-1].RequestID); got != id && !(strings.HasPrefix(id, `"`) && got == `"café-1"`) {
			t.Errorf("id %s recorded as %s", id, got)
		}
	}
	h.send(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/list"}`)
	if line := h.rawLine(); !strings.Contains(line, `"id":9007199254740993`) {
		t.Errorf("tools/list id: %.120s", line)
	}
}

// A fractional id is not an id: a call gets call_id_required with an "<invalid>" receipt, any other request
// the invalid-body answer.
func TestStdioFractionalIDs(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(`{"jsonrpc":"2.0","id":1.5,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`)
	refused := h.next()
	if path(refused, "error", "data", "reason") != "call_id_required" || refused["id"] != nil {
		t.Fatalf("call: %v", refused)
	}
	if rows := h.receipts.Read(); len(rows) != 1 || rows[0].RequestID != "<invalid>" || rows[0].Status != "error" {
		t.Fatalf("receipts %+v", rows)
	}
	h.send(`{"jsonrpc":"2.0","id":1.0,"method":"tools/list"}`)
	if listed := h.next(); fmt.Sprint(path(listed, "error", "code")) != "-32600" || listed["id"] != nil {
		t.Fatalf("list: %v", listed)
	}
	if len(h.fx.ran()) != 0 {
		t.Fatal("a call ran")
	}
}

// One message per line: a batch is refused whole, as the HTTP gateway refuses it, and nothing in it runs.
func TestStdioBatchIsRefused(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send("[" + call(2, "get_account", `{"account_id":"acc-001"}`) + "," + call(3, "read_call_notes", `{"account_id":"acc-001"}`) + "]")
	refused := h.next()
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"expected one valid JSON-RPC object","data":{"reason":"invalid_json"}}}`
	if jsonOf(refused) != jsonOf(decode(t, []byte(want))) {
		t.Fatalf("batch: %v", refused)
	}
	if !h.quiet() || len(h.fx.ran()) != 0 || len(h.receipts.Read()) != 0 {
		t.Fatalf("ran %v receipts %d", h.fx.ran(), len(h.receipts.Read()))
	}
}

// A line the strict parser refuses but that still names a tool call keeps its receipt, as on HTTP.
func TestStdioUnparsableCallKeepsAReceipt(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":NaN}}}`)
	refused := h.next()
	if path(refused, "error", "data", "reason") != "invalid_json" {
		t.Fatalf("%v", refused)
	}
	rows := h.receipts.Read()
	if len(rows) != 1 || rows[0].Tool != "<unparsed>" || rows[0].Status != "error" || *rows[0].HTTPStatus != 400 ||
		path(rows[0].Arguments, "unparsed_sha256") == nil {
		t.Fatalf("receipts %+v", rows)
	}
}

// A line nested past strictjson.MaxDepth is refused as invalid JSON, however long it is. No parser reads
// it, so nothing says it is a call: it runs nothing and leaves no receipt, and the next line is served.
func TestStdioLineNestedPastTheLimitIsInvalidJSON(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	levels := strictjson.MaxDepth - 2 // The request, its params and its arguments are three more.
	h.send(call(2, "get_account", `{"account_id":"acc-001","extra":`+strings.Repeat(`{"a":`, levels)+"1"+strings.Repeat("}", levels)+"}"),
		strings.Repeat("[", 4_000_000))
	for range 2 {
		if refused := h.next(); path(refused, "error", "data", "reason") != "invalid_json" || refused["id"] != nil {
			t.Fatalf("%.200v", refused)
		}
	}
	if len(h.fx.ran()) != 0 || len(h.receipts.Read()) != 0 {
		t.Fatalf("ran %v receipts %d", h.fx.ran(), len(h.receipts.Read()))
	}
	h.send(call(3, "get_account", `{"account_id":"acc-001"}`))
	if served := h.answer(3); path(served, "result", "isError") != false {
		t.Fatalf("after a nested line: %v", served)
	}
}

// Frames are bounded before anything decodes them. A call over 65,536 bytes is refused with its receipt; a
// frame over the 4 MiB frame cap is refused unread, and the next line is still served.
func TestStdioFramesAreBounded(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(call(2, "search_accounts", `{"query":"`+strings.Repeat("a", 70000)+`"}`))
	large := h.next()
	if path(large, "error", "data", "reason") != "request_too_large" || fmt.Sprint(large["id"]) != "2" {
		t.Fatalf("large: %.200v", large)
	}
	if rows := h.receipts.Read(); len(rows) != 1 || *rows[0].HTTPStatus != 413 {
		t.Fatalf("receipts %+v", rows)
	}
	h.send(call(3, "search_accounts", `{"query":"`+strings.Repeat("a", 5<<20)+`"}`))
	huge := h.next()
	if path(huge, "error", "data", "reason") != "request_too_large" || huge["id"] != nil {
		t.Fatalf("huge: %.200v", huge)
	}
	h.send(call(4, "get_account", `{"account_id":"acc-001"}`))
	if served := h.answer(4); path(served, "result", "isError") != false {
		t.Fatalf("after a huge frame: %v", served)
	}
}

// Nothing is written while a call's receipt is still being finished.
func TestStdioBlockedFinishReleasesNothing(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	blocked, release := make(chan struct{}), make(chan struct{})
	h.receipts.BeforeFinish = func(string) {
		close(blocked)
		<-release
	}
	h.send(call(2, "get_account", `{"account_id":"acc-001"}`))
	<-blocked
	if !h.quiet() {
		t.Fatal("a reply left before its receipt finished")
	}
	close(release)
	if reply := h.answer(2); path(reply, "result", "isError") != false {
		t.Fatalf("%v", reply)
	}
}
