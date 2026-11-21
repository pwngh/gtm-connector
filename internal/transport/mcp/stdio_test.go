package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// These transcribe the removed Python suite's stdio bridge tests. In Go, stdio is served in process as a machine principal,
// so each request passes the admission the bridge's forwarded HTTP request passes today.

type stdioHarness struct {
	t        *testing.T
	receipts *MemoryRecorder
	fx       *fixture
	in       *os.File
	lines    chan []byte // every stdout line, closed when stdout ends
	done     chan error
	wall     float64
	held     map[string]map[string]any
}

func newStdio(t *testing.T, scopes []string, change func(*StdioConfig)) *stdioHarness {
	h := &stdioHarness{t: t, receipts: &MemoryRecorder{}, done: make(chan error, 1), wall: testWall,
		held: map[string]map[string]any{}}
	h.fx = newFixture(t, h.receipts)
	expires := h.wall + 3600
	machine := "fixture-machine-principal-bearer-value"
	tokens := map[string]*Token{machine: {Scopes: scopes, ExpiresAt: &expires, Resource: testResource}}
	config := StdioConfig{Token: machine, Resource: testResource, RateLimit: 60, WindowSeconds: 60,
		Verify: verifierFor(tokens), Receipts: h.receipts, Run: h.fx.Run,
		Clock: func() float64 { return 100 }, Wall: func() float64 { return h.wall }}
	if change != nil {
		change(&config)
	}
	// Real OS pipes, as an assistant app gives a stdio server.
	clientIn, serverIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	serverOut, clientOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	h.in = serverIn
	h.lines = make(chan []byte, 64)
	go func() {
		scanner := bufio.NewScanner(serverOut)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			h.lines <- append([]byte{}, scanner.Bytes()...)
		}
		close(h.lines)
	}()
	go func() {
		h.done <- ServeStdio(context.Background(), config, clientIn, clientOut)
		clientOut.Close()
	}()
	t.Cleanup(func() { serverIn.Close() })
	return h
}

func (h *stdioHarness) send(messages ...string) {
	for _, m := range messages {
		if _, err := io.WriteString(h.in, m+"\n"); err != nil {
			h.t.Fatal(err)
		}
	}
}

// next reads one line from stdout.
func (h *stdioHarness) next() map[string]any {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("stdout closed early")
		}
		return decode(h.t, line)
	case <-time.After(10 * time.Second):
		h.t.Fatal("no answer")
	}
	return nil
}

// answer returns the reply to one request id. The SDK runs calls concurrently, so replies may come in any
// order; others are kept for later.
func (h *stdioHarness) answer(id int) map[string]any {
	h.t.Helper()
	key := fmt.Sprint(id)
	for {
		if reply, ok := h.held[key]; ok {
			delete(h.held, key)
			return reply
		}
		reply := h.next()
		h.held[fmt.Sprint(reply["id"])] = reply
	}
}

const legacyStart = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
const legacyReady = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

func call(id int, name string, arguments string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, arguments)
}

func toolNames(t *testing.T, listing map[string]any) []string {
	var names []string
	for _, tool := range path(listing, "result", "tools").([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	return names
}

// test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens
func TestStdioCRMOnlyPrincipalReadsCRMAndNeverWidens(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "get_account", `{"account_id":"acc-007"}`), call(4, "read_call_notes", `{"account_id":"acc-007"}`))
	h.answer(1)
	want := unlistedTools(t, "test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens: tools/list")
	if names := toolNames(t, h.answer(2)); !reflect.DeepEqual(names, want) {
		t.Fatalf("tools %v", names)
	}
	account := h.answer(3)
	if path(account, "result", "isError") != false || !strings.Contains(fmt.Sprint(path(account, "result", "structuredContent")), "crm/accounts/acc-007") {
		t.Fatalf("account %v", account)
	}
	denied := h.answer(4)
	if path(denied, "error", "data", "reason") != "insufficient_scope" || fmt.Sprint(path(denied, "error", "code")) != "1004" ||
		strings.Contains(fmt.Sprint(denied), "IGNORE PREVIOUS") {
		t.Fatalf("denied %v", denied)
	}
	rows := h.receipts.Read()
	got := [][3]any{}
	for _, row := range rows {
		got = append(got, [3]any{row.Tool, row.Status, *row.HTTPStatus})
	}
	if !reflect.DeepEqual(got, [][3]any{{"get_account", "ok", 200}, {"read_call_notes", "error", 403}}) || rows[0].TokenID != rows[1].TokenID {
		t.Fatalf("receipts %v", got)
	}
}

// test_a_refused_grant_is_renewed_once_for_the_same_scopes has no Go counterpart: there is no bridge grant
// to renew. What stays is rule 3: an expired principal is refused, with a receipt, and never retried.
func TestStdioExpiredPrincipalIsRefusedNotRetried(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady, call(2, "get_account", `{"account_id":"acc-007"}`))
	h.answer(1)
	h.answer(2)
	h.wall += 3601
	h.send(call(3, "get_account", `{"account_id":"acc-007"}`), `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	refused, listing := h.answer(3), h.answer(4)
	if path(refused, "error", "data", "reason") != "invalid_token" || path(listing, "error", "data", "reason") != "invalid_token" {
		t.Fatalf("refused %v listing %v", refused, listing)
	}
	got := [][2]any{}
	for _, row := range h.receipts.Read() {
		got = append(got, [2]any{row.Tool, *row.HTTPStatus})
	}
	if !reflect.DeepEqual(got, [][2]any{{"get_account", 200}, {"get_account", 401}}) {
		t.Fatalf("receipts %v", got)
	}
}

// test_a_selected_server_prepares_a_brief_through_the_bridge: a principal holding every read scope cannot
// write to a selected server. Preparing the brief itself is E3's composite read, not this spike's.
func TestStdioSelectedServerRefusesWrites(t *testing.T) {
	h := newStdio(t, []string{"crm:read", "calls:read", "slack:read"}, func(c *StdioConfig) { c.Selected = true })
	h.send(legacyStart, legacyReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "log_activity", `{"account_id":"acc-007","kind":"note","body":"x","idempotency_key":"k"}`))
	h.answer(1)
	if names := toolNames(t, h.answer(2)); !slices.Contains(names, "prepare_renewal_brief") || slices.Contains(names, "log_activity") {
		t.Fatalf("tools %v", names)
	}
	if write := h.answer(3); path(write, "error", "data", "reason") != "crm_write_disabled" {
		t.Fatalf("write %v", write)
	}
	if h.fx.activityCount() != 0 {
		t.Fatal("a write ran")
	}
}

// test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input
func TestStdioStdoutCarriesOnlyProtocolAndStopsAtEndOfInput(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	first, second := h.answer(1), h.answer(2)
	want := unlistedTools(t, "test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input: tools/list")
	if first["jsonrpc"] != "2.0" || second["jsonrpc"] != "2.0" || !reflect.DeepEqual(toolNames(t, second), want) {
		t.Fatalf("%v %v", first, second)
	}
	h.in.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop at end of input")
	}
	if line, ok := <-h.lines; ok {
		t.Fatalf("more on stdout: %s", line)
	}
}

// test_a_call_the_bridge_itself_refuses_still_leaves_a_receipt
func TestStdioMalformedCallsStillLeaveReceipts(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_account","arguments":["acc-001"]}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":7,"arguments":{}}}`,
		call(4, "get_accøunt", `{"account_id":"acc-001"}`))
	h.answer(1)
	for i := 0; i < 3; i++ {
		answer := h.answer(i + 2)
		if answer["error"] == nil && path(answer, "result", "isError") != true {
			t.Fatalf("released %v", answer)
		}
	}
	rows := h.receipts.Read()
	if len(rows) != 3 {
		t.Fatalf("%d receipts", len(rows))
	}
	for _, row := range rows {
		if row.Status != "error" || len(row.Rows) != 0 {
			t.Fatalf("receipt %+v", row)
		}
	}
	if len(h.fx.ran()) != 0 {
		t.Fatalf("ran %v", h.fx.ran())
	}
}

// The 2026-07-28 revision on stdio: per-request metadata with no initialize, and the same scope and rate
// charging as HTTP (test_rate_limit_counts_scope_denials_is_per_token_and_resets).
func TestStdioCurrentRevisionChargesScopeDenials(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, func(c *StdioConfig) { c.RateLimit = 2 })
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	h.send(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_call_notes","arguments":{"account_id":"acc-001"},`+meta+`}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_accounts","arguments":{},`+meta+`}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"},`+meta+`}}`)
	discovered := h.answer(1)
	if !slices.Contains(path(discovered, "result", "supportedVersions").([]any), any(testVersion)) {
		t.Fatalf("discover %v", discovered)
	}
	denied, searched, limited := h.answer(2), h.answer(3), h.answer(4)
	if path(denied, "error", "data", "reason") != "insufficient_scope" || path(searched, "result", "isError") != false ||
		path(limited, "error", "data", "reason") != "rate_limited" {
		t.Fatalf("%v\n%v\n%v", denied, searched, limited)
	}
	var statuses []int
	for _, row := range h.receipts.Read() {
		statuses = append(statuses, *row.HTTPStatus)
	}
	if !reflect.DeepEqual(statuses, []int{403, 200, 429}) {
		t.Fatalf("statuses %v", statuses)
	}
}

// test_audit_start_failure_prevents_writes and test_audit_finish_failure_withholds_data, on stdio.
func TestStdioReceiptFailures(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.receipts.FailStart = true
	h.send(call(2, "log_activity", `{"account_id":"acc-001","kind":"k","body":"b","idempotency_key":"i"}`))
	if refused := h.answer(2); path(refused, "error", "message") != "audit_unavailable; call was not executed" || h.fx.activityCount() != 0 {
		t.Fatalf("start failure %v", refused)
	}
	h.receipts.FailStart, h.receipts.FailFinish = false, true
	h.send(call(3, "get_account", `{"account_id":"acc-001"}`))
	withheld := h.answer(3)
	if path(withheld, "error", "message") != "execution_or_audit_unavailable; no result released" || strings.Contains(fmt.Sprint(withheld), "Alder Vale") {
		t.Fatalf("finish failure %v", withheld)
	}
	if rows := h.receipts.Read(); len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("receipts %+v", rows)
	}
	_ = json.Valid
}

// Arguments with a NUL character, in a name or in a value, are refused over stdio too, under a receipt that
// records a fixed note in their place: no store holds them as given.
func TestStdioArgumentsWithANULAreRefused(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady,
		call(2, "get_account", `{"account_id":"acc-001","a\u0000b":1}`),
		call(3, "search_accounts", `{"query":"a\u0000b"}`),
		call(4, "nope", `{"list":[{"\u0000":1}]}`))
	h.answer(1)
	for id := 2; id <= 4; id++ {
		if refused := h.answer(id); path(refused, "error", "data", "reason") != "invalid_arguments" {
			t.Fatalf("call %d: %v", id, refused)
		}
	}
	rows := h.receipts.Read()
	if len(rows) != 3 || len(h.fx.ran()) != 0 {
		t.Fatalf("%d receipts, ran %v", len(rows), h.fx.ran())
	}
	for _, row := range rows {
		if row.Status != "error" || *row.HTTPStatus != 400 || path(row.Arguments, "unrecorded") != "the arguments hold a NUL character" {
			t.Fatalf("%+v", row)
		}
	}
}
