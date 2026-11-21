package mcp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// These transcribe tests/test_gateway.py and the prepared folder's read rows; each test names its source.

type harness struct {
	t        *testing.T
	gateway  *Gateway
	receipts *MemoryRecorder
	fx       *fixture
	tokens   map[string]*Token
	clock    float64
	wall     float64
	number   int
}

type option func(*GatewayConfig)

func newHarness(t *testing.T, options ...option) *harness {
	h := &harness{t: t, receipts: &MemoryRecorder{}, clock: 100, wall: testWall}
	h.fx = newFixture(t, h.receipts)
	h.tokens = fixtureTokens(h.wall)
	config := GatewayConfig{Resource: testResource, Issuer: testIssuer, RateLimit: 60, WindowSeconds: 60,
		Verify: verifierFor(h.tokens), Receipts: h.receipts, Run: h.fx.Run,
		Clock: func() float64 { return h.clock }, Wall: func() float64 { return h.wall }}
	for _, o := range options {
		o(&config)
	}
	gateway, err := NewGateway(config)
	if err != nil {
		t.Fatal(err)
	}
	h.gateway = gateway
	return h
}

// unset removes a default header, as None does in the Python helper.
const unset = "\x00unset"

func (h *harness) post(body []byte, headers [][2]string) *httptest.ResponseRecorder {
	request := newRequest(http.MethodPost, testResource, bytes.NewReader(body))
	for _, pair := range headers {
		if pair[1] == unset {
			request.Header.Del(pair[0])
		} else {
			request.Header.Add(pair[0], pair[1])
		}
	}
	recorder := httptest.NewRecorder()
	h.gateway.ServeHTTP(recorder, request)
	return recorder
}

// rpc is GatewayTests.rpc: the 2026-07-28 metadata and headers, a numbered id, and the named bearer.
func (h *harness) rpc(method string, params map[string]any, token string, overrides map[string]string, brief bool) *httptest.ResponseRecorder {
	h.number++
	meta := map[string]any{"io.modelcontextprotocol/protocolVersion": testVersion,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "gateway-tests", "version": "0.2.0"}}
	if brief {
		meta["gtm.demo/briefId"] = "brief-fixture-001"
	}
	supplied := map[string]any{"_meta": meta}
	for k, v := range params {
		supplied[k] = v
	}
	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"Mcp-Protocol-Version": testVersion, "Mcp-Method": method}
	if method == "tools/call" {
		headers["Mcp-Name"] = fmt.Sprint(supplied["name"])
	}
	if token != "" {
		value := token
		if _, named := h.tokens[tokenValue(token)]; named {
			value = tokenValue(token)
		}
		headers["Authorization"] = "Bearer " + value
	}
	for k, v := range overrides {
		headers[k] = v
	}
	var pairs [][2]string
	for k, v := range headers {
		if v != unset {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": h.number, "method": method, "params": supplied})
	return h.post(body, pairs)
}

func (h *harness) tool(name string, arguments map[string]any, token string, overrides map[string]string) *httptest.ResponseRecorder {
	if arguments == nil {
		arguments = map[string]any{}
	}
	return h.rpc("tools/call", map[string]any{"name": name, "arguments": arguments}, token, overrides, true)
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("not JSON: %q", data)
	}
	return document
}

func path(value any, keys ...string) any {
	for _, k := range keys {
		m, _ := value.(map[string]any)
		value = m[k]
	}
	return value
}

// records is GatewayTests.records: a released tool result from the named source.
func (h *harness) records(response *httptest.ResponseRecorder, source string) []any {
	h.t.Helper()
	if response.Code != 200 {
		h.t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	result := path(decode(h.t, response.Body.Bytes()), "result").(map[string]any)
	if result["resultType"] != "complete" || result["isError"] == true {
		h.t.Fatalf("not a released result: %s", response.Body)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["source"] != source || structured["untrusted_data"] != true {
		h.t.Fatalf("wrong envelope: %v", structured)
	}
	records := structured["records"].([]any)
	for _, record := range records {
		if !strings.HasPrefix(record.(map[string]any)["record_ref"].(string), source+"/") {
			h.t.Fatalf("record outside %s: %v", source, record)
		}
	}
	return records
}

func refs(records []any) []string {
	out := []string{}
	for _, r := range records {
		out = append(out, r.(map[string]any)["record_ref"].(string))
	}
	return out
}

func reasonOf(t *testing.T, response *httptest.ResponseRecorder) (string, string) {
	document := decode(t, response.Body.Bytes())
	reason, _ := path(document, "error", "data", "reason").(string)
	return reason, fmt.Sprint(path(document, "error", "code"))
}

// test_current_discovery_and_tool_scope_declarations, with the result shapes the Python SDK sends.
func TestCurrentDiscoveryAndToolScopeDeclarations(t *testing.T) {
	h := newHarness(t)
	discovery := h.rpc("server/discover", nil, "full", nil, true)
	if discovery.Code != 200 {
		t.Fatalf("discover %d %s", discovery.Code, discovery.Body)
	}
	result := path(decode(t, discovery.Body.Bytes()), "result").(map[string]any)
	if result["resultType"] != "complete" || !reflect.DeepEqual(result["supportedVersions"], []any{testVersion}) ||
		result["cacheScope"] != "private" || fmt.Sprint(result["ttlMs"]) != "0" ||
		path(result, "capabilities", "tools") == nil ||
		!reflect.DeepEqual(path(result, "_meta", "io.modelcontextprotocol/serverInfo"), map[string]any{"name": "gtm-connector", "version": "0.2.0"}) {
		t.Fatalf("discover result %v", result)
	}
	listing := h.rpc("tools/list", nil, "full", nil, true)
	result = path(decode(t, listing.Body.Bytes()), "result").(map[string]any)
	if listing.Code != 200 || result["resultType"] != "complete" || result["cacheScope"] != "private" || fmt.Sprint(result["ttlMs"]) != "0" {
		t.Fatalf("tools/list %d %v", listing.Code, result)
	}
	scopes := map[string]any{}
	for _, tool := range result["tools"].([]any) {
		m := tool.(map[string]any)
		scopes[m["name"].(string)] = path(m, "_meta", "gtm.demo/requiredScope")
		// The schema and annotations the Python SDK publishes for the same signature.
		if m["name"] == "get_account" {
			want := decode(t, []byte(`{"type":"object","properties":{"account_id":{"title":"Account Id","type":"string"}},"required":["account_id"],"title":"get_accountArguments"}`))
			if !reflect.DeepEqual(m["inputSchema"], want) {
				t.Errorf("get_account inputSchema %v", m["inputSchema"])
			}
			annotations := decode(t, []byte(`{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true}`))
			if !reflect.DeepEqual(m["annotations"], annotations) {
				t.Errorf("annotations %v", m["annotations"])
			}
		}
	}
	want := map[string]any{"search_accounts": "crm:read", "get_account": "crm:read", "list_opportunities": "crm:read",
		"read_call_notes": "calls:read", "read_slack_messages": "slack:read", "log_activity": "crm:write"}
	replay(t, "test_current_discovery_and_tool_scope_declarations: tools/list", wantUnlisted)
	delete(want, writeTool)
	if !reflect.DeepEqual(scopes, want) {
		t.Fatalf("scopes %v", scopes)
	}
	if len(h.receipts.Read()) != 0 {
		t.Fatalf("discovery left receipts")
	}
}

// test_scopes_separate_both_sources_and_writes
func TestScopesSeparateBothSourcesAndWrites(t *testing.T) {
	h := newHarness(t)
	account := h.records(h.tool("get_account", map[string]any{"account_id": "acc-007"}, "crm", nil), "crm")
	if !reflect.DeepEqual(refs(account), []string{"crm/accounts/acc-007", "crm/contacts/con-007"}) {
		t.Fatalf("refs %v", refs(account))
	}
	denied := []struct {
		name      string
		arguments map[string]any
		token     string
		scope     string
	}{
		{"read_call_notes", map[string]any{"account_id": "acc-007"}, "crm", "calls:read"},
		{"get_account", map[string]any{"account_id": "acc-001"}, "calls", "crm:read"},
		{"search_accounts", nil, "calls", "crm:read"},
		{"list_opportunities", nil, "calls", "crm:read"},
		{"log_activity", fixtureActivity, "crm", "crm:write"},
		{"log_activity", fixtureActivity, "calls", "crm:write"},
	}
	for _, d := range denied {
		if d.name == writeTool {
			h.replayWrite("test_scopes_separate_both_sources_and_writes: log_activity with "+d.token+":read", d.arguments, d.token)
			continue
		}
		response := h.tool(d.name, d.arguments, d.token, nil)
		reason, code := reasonOf(t, response)
		challenge := response.Header().Get("WWW-Authenticate")
		if response.Code != 403 || reason != "insufficient_scope" || code != "1004" ||
			!strings.Contains(challenge, `error="insufficient_scope"`) || !strings.Contains(challenge, `scope="`+d.scope+`"`) ||
			!strings.Contains(challenge, "resource_metadata=") || strings.Contains(response.Body.String(), "IGNORE PREVIOUS") {
			t.Fatalf("%s with %s: %d %s %q", d.name, d.token, response.Code, response.Body, challenge)
		}
	}
	h.noWrite()
	notes := h.records(h.tool("read_call_notes", map[string]any{"account_id": "acc-001"}, "calls", nil), "calls")
	if !reflect.DeepEqual(refs(notes), []string{"calls/call_notes/note-001"}) {
		t.Fatalf("notes %v", refs(notes))
	}
	if empty := h.records(h.tool("read_call_notes", map[string]any{"account_id": "acc-004"}, "calls", nil), "calls"); len(empty) != 0 {
		t.Fatalf("empty %v", empty)
	}
}

// test_routing_header_cannot_disguise_a_call_note_request
func TestRoutingHeaderCannotDisguiseACallNoteRequest(t *testing.T) {
	h := newHarness(t)
	response := h.tool("read_call_notes", map[string]any{"account_id": "acc-007"}, "crm", map[string]string{"Mcp-Name": "get_account"})
	// The scope check comes before the SDK's header checks, so the scope refusal wins.
	if reason, _ := reasonOf(t, response); response.Code != 403 || reason != "insufficient_scope" ||
		!strings.Contains(response.Header().Get("WWW-Authenticate"), `scope="calls:read"`) || strings.Contains(response.Body.String(), "IGNORE PREVIOUS") {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	row := h.receipts.Read()[0]
	if row.RowCount != 0 || len(row.Rows) != 0 || len(h.fx.ran()) != 0 {
		t.Fatalf("receipt %+v ran %v", row, h.fx.ran())
	}
}

// test_expired_wrong_audience_missing_and_malformed_tokens_are_unauthorized
func TestInvalidTokensAreUnauthorized(t *testing.T) {
	h := newHarness(t)
	tokens := []string{"expired", "wrong_audience", "", "unknown-fixture-token", "not.a.valid.token"}
	for _, token := range tokens {
		response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, token, nil)
		reason, code := reasonOf(t, response)
		challenge := response.Header().Get("WWW-Authenticate")
		if response.Code != 401 || reason != "invalid_token" || code != "1001" ||
			!strings.Contains(challenge, `error="invalid_token"`) || !strings.Contains(challenge, "resource_metadata=") ||
			strings.Contains(response.Body.String(), "Alder Vale") || (token != "" && strings.Contains(response.Body.String(), tokenValue(token))) {
			t.Fatalf("token %q: %d %s", token, response.Code, response.Body)
		}
	}
	rows := h.receipts.Read()
	if len(rows) != len(tokens) {
		t.Fatalf("%d receipts", len(rows))
	}
	for _, row := range rows {
		if row.RowCount != 0 {
			t.Fatalf("row %+v", row)
		}
	}
	if len(h.gateway.Windows()) != 0 {
		t.Fatalf("an invalid token was charged: %v", h.gateway.Windows())
	}
}

// test_duplicate_authorization_headers_have_the_same_invalid_token_reason
func TestDuplicateAuthorizationHeaders(t *testing.T) {
	h := newHarness(t)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`)
	response := h.post(body, [][2]string{{"Content-Type", "application/json"}, {"Mcp-Method", "tools/call"},
		{"Authorization", "Bearer " + tokenValue("crm")}, {"Authorization", "Bearer " + tokenValue("full")}})
	reason, code := reasonOf(t, response)
	if response.Code != 401 || reason != "invalid_token" || code != "1001" || !strings.Contains(response.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	rows := h.receipts.Read()
	if len(rows) != 1 || *rows[0].HTTPStatus != 401 || len(rows[0].Rows) != 0 {
		t.Fatalf("receipts %+v", rows)
	}
}

// test_every_tool_attempt_has_one_complete_audit_row
func TestEveryToolAttemptHasOneCompleteReceipt(t *testing.T) {
	h := newHarness(t)
	attempts := []struct {
		name      string
		arguments map[string]any
		token     string
		headers   map[string]string
	}{
		{"search_accounts", map[string]any{"query": "Alder Vale Packaging"}, "crm", nil},
		{"get_account", map[string]any{"account_id": "acc-001"}, "crm", nil},
		{"list_opportunities", map[string]any{"account_id": "acc-003"}, "crm", nil},
		{"read_call_notes", map[string]any{"account_id": "acc-004"}, "calls", nil},
		{"log_activity", fixtureActivity, "write", nil},
		{"read_call_notes", map[string]any{"account_id": "acc-007"}, "crm", nil},
		{"get_account", map[string]any{"account_id": "acc-001"}, "expired", nil},
		{"unknown_fixture_tool", map[string]any{}, "full", nil},
		{"get_account", map[string]any{}, "crm", nil},
		{"get_account", map[string]any{"account_id": "acc-001"}, "full", map[string]string{"Mcp-Name": "search_accounts"}},
	}
	expected := [][]string{{"crm/accounts/acc-001"}, {"crm/accounts/acc-001", "crm/contacts/con-001"}, {"crm/opportunities/opp-003"}}
	for i, a := range attempts {
		number := i + 1
		var response *httptest.ResponseRecorder
		if a.name == writeTool {
			response = h.replayWrite("test_every_tool_attempt_has_one_complete_audit_row: attempt 5", a.arguments, a.token)
		} else {
			response = h.tool(a.name, a.arguments, a.token, a.headers)
		}
		document := decode(t, response.Body.Bytes())
		switch {
		case a.name == writeTool:
			// replayWrite has checked the refusal.
		case number <= 5:
			source := "crm"
			if a.name == "read_call_notes" {
				source = "calls"
			}
			records := h.records(response, source)
			if number <= 3 && !reflect.DeepEqual(refs(records), expected[number-1]) {
				t.Fatalf("attempt %d refs %v", number, refs(records))
			}
		case number == 6 || number == 7 || number == 9 || number == 10:
			if want := map[int]int{6: 403, 7: 401, 9: 400, 10: 400}[number]; response.Code != want {
				t.Fatalf("attempt %d: %d, want %d", number, response.Code, want)
			}
		default:
			if document["error"] == nil && path(document, "result", "isError") != true {
				t.Fatalf("attempt %d released: %s", number, response.Body)
			}
		}
		rows := h.receipts.Read()
		row := rows[len(rows)-1]
		var wantRows []string
		if records, ok := path(document, "result", "structuredContent", "records").([]any); ok {
			wantRows = refs(records)
		}
		if len(rows) != number || row.Tool != a.name || !reflect.DeepEqual(row.Arguments, plainJSON(t, a.arguments)) ||
			fmt.Sprint(row.RequestID) != fmt.Sprint(number) || row.BriefID == nil || *row.BriefID != "brief-fixture-001" ||
			*row.HTTPStatus != response.Code || row.Status == "pending" || row.RowCount != len(wantRows) ||
			(len(wantRows) > 0 && !reflect.DeepEqual(row.Rows, wantRows)) {
			t.Fatalf("attempt %d receipt %+v (response %d %s)", number, row, response.Code, response.Body)
		}
		if result, ok := document["result"]; ok && path(result, "_meta", "gtm.demo/auditId") != row.AuditID {
			t.Fatalf("attempt %d auditId %v", number, path(result, "_meta"))
		}
	}
	rows := h.receipts.Read()
	if rows[0].TokenID != rows[1].TokenID || rows[0].TokenID == rows[3].TokenID {
		t.Fatal("token fingerprints")
	}
	for _, row := range rows {
		for name := range h.tokens {
			if strings.Contains(fmt.Sprint(row), name) {
				t.Fatal("a bearer reached a receipt")
			}
		}
	}
}

func plainJSON(t *testing.T, value any) any {
	data, _ := json.Marshal(value)
	return path(decode(t, []byte(`{"v":`+string(data)+`}`)), "v")
}

// test_idempotent_write_returns_one_activity_and_rejects_changed_payload
func TestIdempotentWriteRejectsChangedPayload(t *testing.T) {
	h := newHarness(t)
	arguments := map[string]any{"account_id": "acc-001", "kind": "renewal_call", "body": "Fixture owner requested a renewal call.", "idempotency_key": "fixture-write-001"}
	const vector = "test_idempotent_write_returns_one_activity_and_rejects_changed_payload: "
	h.replayWrite(vector+"first", arguments, "write")
	h.replayWrite(vector+"retry", arguments, "write")
	changed := map[string]any{}
	for k, v := range arguments {
		changed[k] = v
	}
	changed["body"] = "Changed fixture payload."
	h.replayWrite(vector+"changed payload", changed, "write")
	if rows := h.receipts.Read(); len(rows) != 3 || rows[2].Status != "error" {
		t.Fatalf("receipts %+v", rows)
	}
}

// test_rate_limit_counts_scope_denials_is_per_token_and_resets
func TestRateLimitCountsScopeDenials(t *testing.T) {
	h := newHarness(t, func(c *GatewayConfig) { c.RateLimit = 2 })
	if denied := h.tool("read_call_notes", map[string]any{"account_id": "acc-001"}, "crm", nil); denied.Code != 403 {
		t.Fatalf("denied %d", denied.Code)
	}
	h.records(h.tool("search_accounts", nil, "crm", nil), "crm")
	limited := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
	reason, code := reasonOf(t, limited)
	if limited.Code != 429 || reason != "rate_limited" || code != "1002" || limited.Header().Get("Retry-After") <= "0" ||
		!strings.Contains(strings.ToLower(limited.Body.String()), "rate_limit") {
		t.Fatalf("limited %d %s", limited.Code, limited.Body)
	}
	h.records(h.tool("read_call_notes", map[string]any{"account_id": "acc-001"}, "calls", nil), "calls")
	h.clock += 61
	h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil), "crm")
	var statuses []int
	for _, row := range h.receipts.Read() {
		statuses = append(statuses, *row.HTTPStatus)
	}
	if !reflect.DeepEqual(statuses, []int{403, 200, 429, 200, 200}) || len(h.receipts.Read()[2].Rows) != 0 {
		t.Fatalf("statuses %v", statuses)
	}
}

// test_duplicate_write_attempts_also_consume_rate_limit
func TestDuplicateWriteAttemptsConsumeRateLimit(t *testing.T) {
	h := newHarness(t, func(c *GatewayConfig) { c.RateLimit = 1 })
	arguments := map[string]any{"account_id": "acc-001", "kind": "renewal_call", "body": "x", "idempotency_key": "fixture-write-001"}
	h.replayWrite("test_duplicate_write_attempts_also_consume_rate_limit: first", arguments, "write")
	if retry := h.tool("log_activity", arguments, "write", nil); retry.Code != 429 {
		t.Fatalf("retry %d", retry.Code)
	}
	h.noWrite()
	if len(h.receipts.Read()) != 2 {
		t.Fatal("counts")
	}
}

// test_header_mismatches_are_rejected_and_audited
func TestHeaderMismatchesAreRejectedAndAudited(t *testing.T) {
	h := newHarness(t)
	cases := []map[string]string{
		{"Mcp-Method": "tools/list"}, {"Mcp-Name": "search_accounts"}, {"Mcp-Name": unset},
		{"Mcp-Protocol-Version": unset}, {"Mcp-Protocol-Version": "2025-11-25"},
	}
	for _, headers := range cases {
		response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "full", headers)
		reason, code := reasonOf(t, response)
		_, version := headers["Mcp-Protocol-Version"]
		if response.Code != 400 || code != "-32020" || (version && reason != "protocol_version_mismatch") ||
			strings.Contains(response.Body.String(), "Alder Vale") {
			t.Fatalf("%v: %d %s", headers, response.Code, response.Body)
		}
	}
	if len(h.receipts.Read()) != len(cases) || len(h.fx.ran()) != 0 {
		t.Fatal("receipts or runs")
	}
	encoded := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("get_account")) + "?="
	h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "full", map[string]string{"Mcp-Name": encoded}), "crm")
}

// test_hostile_browser_origin_is_rejected_without_data, and a wrong Host or plain HTTP likewise.
func TestHostileOriginOrHostIsRejected(t *testing.T) {
	h := newHarness(t)
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "full", map[string]string{"Origin": "https://untrusted.example"})
	reason, code := reasonOf(t, response)
	if response.Code != 403 || reason != "untrusted_origin_or_host" || code != "1003" || strings.Contains(response.Body.String(), "Alder Vale") {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	if rows := h.receipts.Read(); len(rows) != 1 || len(rows[0].Rows) != 0 {
		t.Fatalf("receipts %+v", rows)
	}
	for _, target := range []string{"https://localhost:8765/mcp", "http://127.0.0.1:8765/mcp"} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
		request := newRequest(http.MethodPost, target, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+tokenValue("full"))
		recorder := httptest.NewRecorder()
		h.gateway.ServeHTTP(recorder, request)
		if reason, _ := reasonOf(t, recorder); recorder.Code != 403 || reason != "untrusted_origin_or_host" {
			t.Fatalf("%s: %d %s", target, recorder.Code, recorder.Body)
		}
	}
}

// test_audit_start_failure_prevents_writes
func TestReceiptStartFailurePreventsExecution(t *testing.T) {
	h := newHarness(t)
	h.receipts.FailStart = true
	response := h.tool("log_activity", map[string]any{"account_id": "acc-001", "kind": "k", "body": "b", "idempotency_key": "i"}, "write", nil)
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"audit_unavailable; call was not executed"}` {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	if h.fx.activityCount() != 0 || len(h.fx.ran()) != 0 {
		t.Fatal("the call ran")
	}
}

// test_deeply_nested_marked_request_is_rejected_and_audited
func TestDeeplyNestedMarkedRequestIsAudited(t *testing.T) {
	h := newHarness(t)
	body := []byte(strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000))
	response := h.post(body, [][2]string{{"Mcp-Method", "tools/call"}, {"Mcp-Name", "get_account"}})
	rows := h.receipts.Read()
	if response.Code != 400 || len(rows) != 1 || rows[0].Tool != "get_account" || rows[0].Status != "error" ||
		*rows[0].HTTPStatus != 400 || rows[0].RowCount != 0 {
		t.Fatalf("%d %+v", response.Code, rows)
	}
}

// nestedArgument is a value with this many objects open around a 1.
func nestedArgument(levels int) any {
	var value any = 1
	for i := 0; i < levels; i++ {
		value = map[string]any{"a": value}
	}
	return value
}

// A tool call nested past strictjson.MaxDepth is refused as invalid JSON and runs nothing. The request,
// its params and its arguments are three levels, so the argument's own levels decide.
func TestCallNestedPastTheLimitIsInvalidJSON(t *testing.T) {
	h := newHarness(t)
	atLimit := h.tool("get_account", map[string]any{"account_id": "acc-001", "extra": nestedArgument(strictjson.MaxDepth - 3)}, "crm", nil)
	// A call at the limit is read: it is refused for its unknown argument, not as JSON.
	if reason, _ := reasonOf(t, atLimit); reason != "invalid_arguments" {
		t.Fatalf("a call at the limit: %d %s", atLimit.Code, atLimit.Body)
	}
	before, ran := len(h.receipts.Read()), len(h.fx.ran())
	response := h.tool("get_account", map[string]any{"account_id": "acc-001", "extra": nestedArgument(strictjson.MaxDepth - 2)}, "crm", nil)
	reason, code := reasonOf(t, response)
	rows := h.receipts.Read()
	if response.Code != 400 || reason != "invalid_json" || code != "-32700" || len(rows) != before+1 {
		t.Fatalf("%d %s, %d receipts", response.Code, response.Body, len(rows)-before)
	}
	// The Mcp-Method header says it is a call, so it keeps a receipt under the name the Mcp-Name header gives.
	if last := rows[len(rows)-1]; last.Tool != "get_account" || last.Status != "error" || *last.HTTPStatus != 400 || last.RowCount != 0 {
		t.Fatalf("%+v", last)
	}
	// Without the header nothing readable says it is a call, as for any body no parser reads.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001","extra":` +
		strings.Repeat(`{"a":`, strictjson.MaxDepth-2) + "1" + strings.Repeat("}", strictjson.MaxDepth-2) + "}}}"
	unmarked := h.post([]byte(body), [][2]string{{"Authorization", "Bearer unused"}})
	if reason, _ := reasonOf(t, unmarked); unmarked.Code != 400 || reason != "invalid_json" || len(h.receipts.Read()) != before+1 {
		t.Fatalf("%d %s, %d receipts", unmarked.Code, unmarked.Body, len(h.receipts.Read())-before)
	}
	if len(h.fx.ran()) != ran {
		t.Fatalf("a call ran: %v", h.fx.ran())
	}
}

// test_a_tool_call_the_strict_parser_refuses_keeps_one_receipt
func TestStrictParserRefusalKeepsOneReceipt(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001","weight":1e999}}}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call","params":{}}`,
		// json.loads reads these, so Python still sees a tool call and keeps a receipt.
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":NaN}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"n":Infinity}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"n":-Infinity}}}`,
		utf16(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`, false, false),
		utf16(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`, true, false),
		utf16(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`, false, true),
		utf32(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}}`),
	} {
		before := len(h.receipts.Read())
		response := h.post([]byte(body), [][2]string{{"Authorization", "Bearer unused"}})
		reason, _ := reasonOf(t, response)
		rows := h.receipts.Read()
		last := rows[len(rows)-1]
		if response.Code != 400 || reason != "invalid_json" || len(rows) != before+1 || last.Status != "error" || last.Tool != "<unparsed>" ||
			*last.HTTPStatus != 400 || last.RowCount != 0 || path(last.Arguments, "unparsed_sha256") == nil {
			t.Fatalf("%s: %d %s %+v", body[:40], response.Code, response.Body, last)
		}
	}
}

// test_unknown_account_write_returns_an_explicit_audited_tool_error
func TestUnknownAccountWriteIsAnAuditedToolError(t *testing.T) {
	h := newHarness(t)
	h.replayWrite("test_unknown_account_write_returns_an_explicit_audited_tool_error",
		map[string]any{"account_id": "acc-missing", "kind": "k", "body": "b", "idempotency_key": "i"}, "write")
	if rows := h.receipts.Read(); len(rows) != 1 || rows[0].Status != "error" || rows[0].RowCount != 0 {
		t.Fatalf("%+v", rows)
	}
}

// test_audit_finish_failure_withholds_data
func TestReceiptFinishFailureWithholdsData(t *testing.T) {
	h := newHarness(t)
	h.receipts.FailFinish = true
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"execution_or_audit_unavailable; no result released"}` {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

// test_write_committed_before_audit_failure_is_reused_on_retry
func TestWriteCommittedBeforeReceiptFailureIsReused(t *testing.T) {
	h := newHarness(t)
	arguments := map[string]any{"account_id": "acc-001", "kind": "renewal_call", "body": "b", "idempotency_key": "fixture-write-001"}
	const vector = "test_write_committed_before_audit_failure_is_reused_on_retry: "
	h.receipts.FailFinish = true
	replay(t, vector+"first", wantWithheld)
	response := h.tool("log_activity", arguments, "write", nil)
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"execution_or_audit_unavailable; no result released"}` {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	h.receipts.FailFinish = false
	if rows := h.receipts.Read(); len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("%+v", rows)
	}
	h.noWrite()
	h.replayWrite(vector+"retry", arguments, "write")
	if rows := h.receipts.Read(); len(rows) != 2 || rows[0].Status != "pending" {
		t.Fatalf("%+v", rows)
	}
}

// Connector.__call__'s GET and DELETE answers, which no Python test pins but scripts/check-inspector.py
// and the SDK's session rules depend on.
func TestGetAndDeleteAreNotAllowed(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		method, allow, message string
	}{{http.MethodGet, "POST", "Method Not Allowed"}, {http.MethodDelete, "", "Method Not Allowed: Session termination not supported"}} {
		request := newRequest(c.method, testResource, nil)
		request.Header.Set("Authorization", "Bearer "+tokenValue("full"))
		recorder := httptest.NewRecorder()
		h.gateway.ServeHTTP(recorder, request)
		want := decode(t, []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"`+c.message+`"}}`))
		if recorder.Code != 405 || recorder.Header().Get("Allow") != c.allow || !reflect.DeepEqual(decode(t, recorder.Body.Bytes()), want) {
			t.Fatalf("%s: %d %q %s", c.method, recorder.Code, recorder.Header().Get("Allow"), recorder.Body)
		}
	}
}

// The checks the Python SDK makes after check_request admits a request, with the replies it sends
// (captured from Connector under mcp 2.3.0).
func TestSDKLadderAfterAdmission(t *testing.T) {
	h := newHarness(t)
	full := "Bearer " + tokenValue("full")
	headers := func(method string, extra ...[2]string) [][2]string {
		return append([][2]string{{"Content-Type", "application/json"}, {"Accept", "application/json, text/event-stream"},
			{"Mcp-Protocol-Version", testVersion}, {"Mcp-Method", method}, {"Authorization", full}}, extra...)
	}
	cases := []struct {
		label   string
		body    string
		headers [][2]string
		status  int
		reply   string
	}{
		{"missing client capabilities", `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			headers("tools/list"), 400, `{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"params._meta is missing the required envelope key(s): io.modelcontextprotocol/clientCapabilities"}}`},
		{"Mcp-Method missing on a list", `{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			[][2]string{{"Content-Type", "application/json"}, {"Accept", "application/json"}, {"Mcp-Protocol-Version", testVersion}, {"Authorization", full}},
			400, `{"jsonrpc":"2.0","id":8,"error":{"code":-32020,"message":"mcp-method header does not match the request body's method"}}`},
		{"Mcp-Method twice", `{"jsonrpc":"2.0","id":11,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			headers("tools/list", [2]string{"Mcp-Method", "tools/list"}),
			400, `{"jsonrpc":"2.0","id":11,"error":{"code":-32020,"message":"mcp-method header appears more than once"}}`},
		{"boolean id", `{"jsonrpc":"2.0","id":true,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			headers("tools/list"), 400, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Body must be a single JSON-RPC request or notification object"}}`},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			headers("notifications/initialized"), 202, ""},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}]`,
			headers("tools/list"), 400, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"expected one valid JSON-RPC object","data":{"reason":"invalid_json"}}}`},
		{"prompts/get", `{"jsonrpc":"2.0","id":9,"method":"prompts/get","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			headers("prompts/get", [2]string{"Mcp-Name", "x"}), 404, `{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"Method not found","data":{"reason":"method_not_found"}}}`},
		{"Accept without JSON", `{"jsonrpc":"2.0","id":10,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			[][2]string{{"Content-Type", "application/json"}, {"Accept", "text/plain"}, {"Mcp-Protocol-Version", testVersion}, {"Mcp-Method", "tools/list"}, {"Authorization", full}},
			406, ""},
		{"missing metadata", `{"jsonrpc":"2.0","id":99,"method":"tools/list","params":{}}`,
			headers("tools/list"), 400, `{"jsonrpc":"2.0","id":99,"error":{"code":-32602,"message":"request metadata requires protocolVersion","data":{"reason":"missing_protocol_version"}}}`},
	}
	for _, c := range cases {
		status, reply := c.status, c.reply
		response := h.post([]byte(c.body), c.headers)
		if response.Code != status || (reply != "" && !reflect.DeepEqual(decode(t, response.Body.Bytes()), decode(t, []byte(reply)))) ||
			(reply == "" && response.Body.Len() != 0) {
			t.Errorf("%s: %d %s", c.label, response.Code, response.Body)
		}
	}
	// Methods the 2026-07-28 revision removed are answered as the Python SDK answers them.
	for _, method := range []string{"ping", "initialize"} {
		response := h.rpc(method, nil, "full", nil, false)
		want := decode(t, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found","data":%q}}`, h.number, method)))
		if response.Code != 404 || !reflect.DeepEqual(decode(t, response.Body.Bytes()), want) {
			t.Errorf("%s: %d %s", method, response.Code, response.Body)
		}
	}
	// An unknown tool is a tool error with HTTP 200 and the Python SDK's text.
	unknown := h.tool("unknown_fixture_tool", nil, "full", nil)
	result := path(decode(t, unknown.Body.Bytes()), "result")
	if unknown.Code != 200 || path(result, "isError") != true || path(result, "resultType") != "complete" ||
		!strings.Contains(fmt.Sprint(path(result, "content")), "Unknown tool: unknown_fixture_tool") {
		t.Errorf("unknown tool: %d %s", unknown.Code, unknown.Body)
	}
}

// The prepared folder's read rows (testdata/oracle/folders/prepared/reads.jsonl): a brief read again, refused
// for a missing scope, a changed seal, a token one second before and at expiry, and a changed selection.
// Each response must equal the oracle's as a value, audit id included, and each receipt its row.
func TestPreparedFolderReads(t *testing.T) {
	type row struct {
		Label     string            `json:"label"`
		Scopes    []string          `json:"scopes"`
		Arguments map[string]string `json:"arguments"`
		Status    int               `json:"http_status"`
		Response  json.RawMessage   `json:"response"`
		Receipts  []map[string]any  `json:"receipts"`
	}
	var rows []row
	for _, line := range oracletest.Lines(t, "folders/prepared/reads.jsonl") {
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	var first struct {
		Result struct {
			Structured json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	json.Unmarshal(rows[0].Response, &first)
	for _, r := range rows {
		t.Run(r.Label, func(t *testing.T) {
			h := newHarness(t, func(c *GatewayConfig) { c.Selected = true })
			h.fx.brief = first.Result.Structured
			wantReceipt := r.Receipts[0]
			h.receipts.NewID = func(int) string { return wantReceipt["audit_id"].(string) }
			switch r.Label {
			case "changed seal", "changed selection":
				h.fx.briefState = r.Label
			}
			// The oracle issues a 300-second token and reads it 299 and 300 seconds later.
			issued := h.wall
			expires := issued + 300
			h.tokens["fixture-oracle-grant-value-0000000000000000000000"] = &Token{Scopes: r.Scopes, ExpiresAt: &expires, Resource: testResource}
			switch r.Label {
			case "token one second before expiry":
				h.wall = issued + 299
			case "token at expiry":
				h.wall = issued + 300
			}
			expected := decode(t, r.Response)
			number, _ := expected["id"].(json.Number).Int64()
			h.number = int(number) - 1
			arguments := map[string]any{}
			for k, v := range r.Arguments {
				arguments[k] = v
			}
			response := h.rpc("tools/call", map[string]any{"name": "read_renewal_brief", "arguments": arguments},
				"fixture-oracle-grant-value-0000000000000000000000", nil, false)
			if response.Code != r.Status {
				t.Fatalf("status %d, want %d: %s", response.Code, r.Status, response.Body)
			}
			if got := decode(t, response.Body.Bytes()); !reflect.DeepEqual(got, expected) {
				gotJSON, _ := json.Marshal(got)
				if dump := os.Getenv("GTM_DUMP"); dump != "" {
					os.WriteFile(dump, gotJSON, 0o600)
				}
				t.Fatalf("response differs:\n got %.600s\nwant %.600s", gotJSON, r.Response)
			}
			receipts := h.receipts.Read()
			if len(receipts) != 1 {
				t.Fatalf("%d receipts", len(receipts))
			}
			got := receipts[0]
			var wantRows []string
			for _, ref := range wantReceipt["rows"].([]any) {
				wantRows = append(wantRows, ref.(string))
			}
			errorText := ""
			if got.Error != nil {
				errorText = *got.Error
			}
			if got.AuditID != wantReceipt["audit_id"] || got.Tool != wantReceipt["tool"] || got.Status != wantReceipt["status"] ||
				float64(*got.HTTPStatus) != wantReceipt["http_status"].(float64) || float64(got.RowCount) != wantReceipt["row_count"].(float64) ||
				!reflect.DeepEqual(append([]string{}, got.Rows...), append([]string{}, wantRows...)) ||
				fmt.Sprint(got.RequestID) != fmt.Sprint(wantReceipt["request_id"]) || got.BriefID != nil ||
				fmt.Sprint(got.Arguments) != fmt.Sprint(wantReceipt["arguments"]) || fmt.Sprint(wantReceipt["error"]) != map[bool]string{true: "<nil>", false: errorText}[got.Error == nil] {
				t.Fatalf("receipt %+v\nwant %v", got, wantReceipt)
			}
		})
	}
}

// The same gateway behind a real TLS listener: one call on the wire, and a request with two Host lines,
// which net/http refuses with 400 before any handler runs, so it gets neither the oracle's 403 nor a receipt.
func TestOverRealTLS(t *testing.T) {
	receipts := &MemoryRecorder{}
	fx := newFixture(t, receipts)
	server := httptest.NewUnstartedServer(nil)
	server.StartTLS()
	defer server.Close()
	resource := server.URL + "/mcp"
	tokens := fixtureTokens(testWall)
	for _, token := range tokens {
		token.Resource = resource
	}
	server.Config.Handler = mustGateway(t, GatewayConfig{Resource: resource, Issuer: testIssuer, RateLimit: 60, WindowSeconds: 60,
		Verify: verifierFor(tokens), Receipts: receipts, Run: fx.Run, Wall: func() float64 { return testWall }})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"},` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	request, _ := http.NewRequest(http.MethodPost, resource, strings.NewReader(body))
	for k, v := range map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"Mcp-Protocol-Version": testVersion, "Mcp-Method": "tools/call", "Mcp-Name": "get_account", "Authorization": "Bearer " + tokenValue("crm")} {
		request.Header.Set(k, v)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(payload), "crm/accounts/acc-001") {
		t.Fatalf("%d %s", response.StatusCode, payload)
	}
	connection, err := tls.Dial("tcp", strings.TrimPrefix(server.URL, "https://"), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	fmt.Fprintf(connection, "POST /mcp HTTP/1.1\r\nHost: %s\r\nHost: %s\r\nMcp-Method: tools/call\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		host, host, len(body), body)
	raw, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw.StatusCode != 400 || len(receipts.Read()) != 1 {
		t.Fatalf("duplicate Host: %d, %d receipts", raw.StatusCode, len(receipts.Read()))
	}
	if !slices.Equal(fx.ran(), []string{"get_account"}) {
		t.Fatalf("ran %v", fx.ran())
	}
}

// utf16 encodes ASCII text as UTF-16, little- or big-endian, with or without a byte-order mark.
func utf16(text string, bigEndian, mark bool) string {
	var out []byte
	if mark {
		out = []byte{0xFF, 0xFE}
		if bigEndian {
			out = []byte{0xFE, 0xFF}
		}
	}
	for _, c := range []byte(text) {
		if bigEndian {
			out = append(out, 0, c)
		} else {
			out = append(out, c, 0)
		}
	}
	return string(out)
}

// utf32 encodes ASCII text as UTF-32 little-endian with no mark.
func utf32(text string) string {
	var out []byte
	for _, c := range []byte(text) {
		out = append(out, c, 0, 0, 0)
	}
	return string(out)
}
