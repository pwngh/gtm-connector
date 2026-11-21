package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Each vector here was sent to the Python gateway (Connector under mcp 2.3.0) and its status, body and receipt
// recorded; the Go gateway must give the same. Bodies compare as JSON values.

const vectorMeta = `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

func vectorHeaders(method, name string, extra ...[2]string) [][2]string {
	h := [][2]string{{"Content-Type", "application/json"}, {"Accept", "application/json, text/event-stream"},
		{"Mcp-Protocol-Version", testVersion}, {"Mcp-Method", method}, {"Authorization", "Bearer " + tokenValue("full")}}
	if name != "" {
		h = append(h, [2]string{"Mcp-Name", name})
	}
	return append(h, extra...)
}

// replace swaps one header for another value, or drops it when value is unset.
func replace(headers [][2]string, name, value string) [][2]string {
	var out [][2]string
	for _, h := range headers {
		if !strings.EqualFold(h[0], name) {
			out = append(out, h)
		}
	}
	if value != unset {
		out = append(out, [2]string{name, value})
	}
	return out
}

func callBody(id, name, arguments, meta string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":%q,"arguments":%s,"_meta":%s}}`, id, name, arguments, meta))
}

func methodBody(id, method, params string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":{%s"_meta":%s}}`, id, method, params, vectorMeta))
}

type vector struct {
	label   string
	body    []byte
	headers [][2]string
	status  int
	reply   string // the exact reply as a JSON value; "" for an empty body; "released" for a released tool result
	receipt string // "" for none, else "tool status http"
}

func checkVectors(t *testing.T, h *harness, vectors []vector) {
	t.Helper()
	for _, v := range vectors {
		before := len(h.receipts.Read())
		response := h.post(v.body, v.headers)
		problem := ""
		switch {
		case response.Code != v.status:
			problem = fmt.Sprintf("status %d, want %d", response.Code, v.status)
		case v.reply == "" && response.Body.Len() != 0:
			problem = "a body where Python sends none"
		case v.reply == "released":
			if path(decode(t, response.Body.Bytes()), "result", "isError") != false {
				problem = "not a released result"
			}
		case v.reply != "" && !reflect.DeepEqual(withoutAuditID(decode(t, response.Body.Bytes())), withoutAuditID(decode(t, []byte(v.reply)))):
			problem = "reply differs"
		case strings.Contains(response.Header().Get("Content-Type"), "event-stream"):
			problem = "an SSE reply"
		}
		rows := h.receipts.Read()[before:]
		got := ""
		if len(rows) > 1 {
			problem = fmt.Sprintf("%d receipts", len(rows))
		} else if len(rows) == 1 {
			got = fmt.Sprintf("%s %s %d", rows[0].Tool, rows[0].Status, *rows[0].HTTPStatus)
		}
		if got != v.receipt {
			problem += fmt.Sprintf(" receipt %q, want %q", got, v.receipt)
		}
		if problem != "" {
			t.Errorf("%s: %s\n  got %d %s", v.label, problem, response.Code, response.Body)
		}
	}
}

// The Python SDK's Accept and Content-Type rules: a missing or empty Accept is 406; JSON alone is enough;
// a Content-Type parameter that fails MIME parsing is still JSON to Python.
func TestAcceptAndContentTypeAsPython(t *testing.T) {
	h := newHarness(t)
	call := func(id string) []byte { return callBody(id, "get_account", `{"account_id":"acc-001"}`, vectorMeta) }
	headers := vectorHeaders("tools/call", "get_account")
	checkVectors(t, h, []vector{
		{"Accept missing", call("1"), replace(headers, "Accept", unset), 406, "", "get_account error 406"},
		{"Accept empty", call("2"), replace(headers, "Accept", ""), 406, "", "get_account error 406"},
		{"Accept JSON alone", call("3"), replace(headers, "Accept", "application/json"), 200, "released", "get_account ok 200"},
		{"Content-Type with a bare parameter", call("4"), replace(headers, "Content-Type", "application/json; charset"), 200, "released", "get_account ok 200"},
		{"Content-Type with empty parameters", call("5"), replace(headers, "Content-Type", "application/json;;=x"), 200, "released", "get_account ok 200"},
		{"subscriptions/listen with JSON alone", methodBody("6", "subscriptions/listen", `"notifications":{},`),
			replace(vectorHeaders("subscriptions/listen", ""), "Accept", "application/json"), 406, "", ""},
	})
}

// Every method the Python gateway does not serve is its 404/-32601 with the method as data; the three empty
// lists it does serve are answered in its shape; subscriptions/listen never reaches SDK streaming.
func TestMethodsAsPython(t *testing.T) {
	h := newHarness(t)
	notFound := func(id, method string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found","data":%q}}`, id, method)
	}
	empty := func(id, member string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"cacheScope":"private","%s":[],"resultType":"complete","ttlMs":0,`+
			`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"gtm-connector","version":"0.2.0"}}}}`, id, member)
	}
	checkVectors(t, h, []vector{
		{"subscriptions/listen", methodBody("5", "subscriptions/listen", `"notifications":{"toolsListChanged":true},`),
			vectorHeaders("subscriptions/listen", ""), 404, notFound("5", "subscriptions/listen"), ""},
		{"logging/setLevel", methodBody("20", "logging/setLevel", `"level":"debug",`), vectorHeaders("logging/setLevel", ""), 404, notFound("20", "logging/setLevel"), ""},
		{"completion/complete", methodBody("21", "completion/complete", `"ref":{"type":"ref/prompt","name":"x"},"argument":{"name":"a","value":"b"},`),
			vectorHeaders("completion/complete", ""), 404, notFound("21", "completion/complete"), ""},
		{"resources/list", methodBody("22", "resources/list", ""), vectorHeaders("resources/list", ""), 200, empty("22", "resources"), ""},
		{"prompts/list", methodBody("23", "prompts/list", ""), vectorHeaders("prompts/list", ""), 200, empty("23", "prompts"), ""},
		{"resources/templates/list", methodBody("24", "resources/templates/list", ""), vectorHeaders("resources/templates/list", ""), 200, empty("24", "resourceTemplates"), ""},
		{"resources/subscribe", methodBody("25", "resources/subscribe", `"uri":"file:///x",`), vectorHeaders("resources/subscribe", "file:///x"), 404, notFound("25", "resources/subscribe"), ""},
		{"notifications/initialized with an id", methodBody("34", "notifications/initialized", ""), vectorHeaders("notifications/initialized", ""), 404, notFound("34", "notifications/initialized"), ""},
		{"ping", methodBody("13", "ping", ""), vectorHeaders("ping", ""), 404, notFound("13", "ping"), ""},
		{"a custom method", methodBody("14", "gtm/anything", ""), vectorHeaders("gtm/anything", ""), 404, notFound("14", "gtm/anything"), ""},
		{"tools/list with a number cursor", methodBody("9", "tools/list", `"cursor":5,`), vectorHeaders("tools/list", ""), 400,
			`{"jsonrpc":"2.0","id":9,"error":{"code":-32602,"message":"Invalid request parameters","data":""}}`, ""},
	})
	// A string cursor is ignored and the whole list comes back.
	whole := unlistedTools(t, "tools/list with a string cursor")
	response := h.post(methodBody("8", "tools/list", `"cursor":"abc",`), vectorHeaders("tools/list", ""))
	if response.Code != 200 || len(path(decode(t, response.Body.Bytes()), "result", "tools").([]any)) != len(whole) {
		t.Errorf("string cursor: %d %s", response.Code, response.Body)
	}
}

// Python validates the 2026-07-28 request schema, including the reserved _meta keys, and answers a
// mis-shaped one with -32602 "Invalid request parameters". Well-formed extras pass.
func TestRequestSchemaAsPython(t *testing.T) {
	h := newHarness(t)
	invalid := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32602,"message":"Invalid request parameters","data":""}}`
	}
	meta := func(extra string) string {
		return `{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` + extra + `}`
	}
	cc := `"io.modelcontextprotocol/clientCapabilities":`
	cases := []struct {
		label string
		meta  string
		ok    bool
	}{
		{"capabilities a string", meta(cc + `"x"`), false},
		{"capabilities a list", meta(cc + `[]`), false},
		{"capabilities null", meta(cc + `null`), false},
		{"roots a number", meta(cc + `{"roots":5}`), false},
		{"roots an object", meta(cc + `{"roots":{"listChanged":true}}`), true},
		{"an unknown capability", meta(cc + `{"whatever":5}`), true},
		{"experimental not objects", meta(cc + `{"experimental":{"a":5}}`), false},
		{"clientInfo a number", meta(cc + `{},"io.modelcontextprotocol/clientInfo":5`), false},
		{"clientInfo without version", meta(cc + `{},"io.modelcontextprotocol/clientInfo":{"name":"x"}`), false},
		{"clientInfo a number version", meta(cc + `{},"io.modelcontextprotocol/clientInfo":{"name":"x","version":1}`), false},
		{"clientInfo complete", meta(cc + `{},"io.modelcontextprotocol/clientInfo":{"name":"x","version":"1"}`), true},
		{"clientInfo null", meta(cc + `{},"io.modelcontextprotocol/clientInfo":null`), true},
		{"progressToken a string", meta(cc + `{},"progressToken":"a"`), true},
		{"progressToken a boolean", meta(cc + `{},"progressToken":true`), true},
		{"progressToken a fraction", meta(cc + `{},"progressToken":1.5`), false},
		{"progressToken an object", meta(cc + `{},"progressToken":{}`), false},
		{"logLevel unknown", meta(cc + `{},"io.modelcontextprotocol/logLevel":"loud"`), false},
	}
	for i, c := range cases {
		id := fmt.Sprint(100 + i)
		status, reply, receipt := 200, "released", "get_account ok 200"
		if !c.ok {
			status, reply, receipt = 400, invalid(id), "get_account error 400"
		}
		checkVectors(t, h, []vector{{c.label + " (tools/call)", callBody(id, "get_account", `{"account_id":"acc-001"}`, c.meta),
			vectorHeaders("tools/call", "get_account"), status, reply, receipt}})
		listStatus, listReply := 200, ""
		if !c.ok {
			listStatus, listReply = 400, invalid(id)
		}
		response := h.post([]byte(`{"jsonrpc":"2.0","id":`+id+`,"method":"tools/list","params":{"_meta":`+c.meta+`}}`), vectorHeaders("tools/list", ""))
		if response.Code != listStatus || (listReply != "" && !reflect.DeepEqual(decode(t, response.Body.Bytes()), decode(t, []byte(listReply)))) {
			t.Errorf("%s (tools/list): %d %s", c.label, response.Code, response.Body)
		}
	}
}

// Tool-call shapes Python's schema refuses, and those it serves.
func TestToolCallShapesAsPython(t *testing.T) {
	h := newHarness(t)
	invalid := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32602,"message":"Invalid request parameters","data":""}}`
	}
	checkVectors(t, h, []vector{
		{"no params.name", []byte(`{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"arguments":{"account_id":"acc-001"},"_meta":` + vectorMeta + `}}`),
			vectorHeaders("tools/call", "get_account"), 400, invalid("32"), "get_account error 400"},
		{"unknown tool, string arguments", callBody("36", "nope", `"x"`, vectorMeta), vectorHeaders("tools/call", "nope"), 400, invalid("36"), "nope error 400"},
		{"unknown tool, null arguments", callBody("11", "nope", `null`, vectorMeta), vectorHeaders("tools/call", "nope"), 200,
			`{"jsonrpc":"2.0","id":11,"result":{"content":[{"text":"Unknown tool: nope","type":"text"}],"isError":true,"resultType":"complete",` +
				`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"gtm-connector","version":"0.2.0"}}}}`,
			"nope error 200"},
		{"an integer name", []byte(`{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":7,"arguments":{},"_meta":` + vectorMeta + `}}`),
			vectorHeaders("tools/call", "7"), 400,
			`{"jsonrpc":"2.0","id":20,"error":{"code":-32020,"message":"mcp-name header does not match the request body's 'name' parameter"}}`, "<invalid> error 400"},
		{"non-canonical base64 name", callBody("33", "get_account", `{"account_id":"acc-001"}`, vectorMeta),
			vectorHeaders("tools/call", "=?base64?Z2V0X2FjY291bnQ?="), 400,
			`{"jsonrpc":"2.0","id":33,"error":{"code":-32020,"message":"mcp-name header does not match the request body's 'name' parameter"}}`, "get_account error 400"},
		{"no arguments member", []byte(`{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"search_accounts","_meta":` + vectorMeta + `}}`),
			vectorHeaders("tools/call", "search_accounts"), 200, "released", "search_accounts ok 200"},
		{"a fractional id on a call", callBody("1.5", "get_account", `{"account_id":"acc-001"}`, vectorMeta), vectorHeaders("tools/call", "get_account"), 400,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"a tool call requires a string or integer id","data":{"reason":"call_id_required"}}}`,
			"get_account error 400"},
		{"a fractional id on a list", []byte(`{"jsonrpc":"2.0","id":1.0,"method":"tools/list","params":{"_meta":` + vectorMeta + `}}`), vectorHeaders("tools/list", ""), 400,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Body must be a single JSON-RPC request or notification object"}}`, ""},
		{"jsonrpc 1.0", []byte(`{"jsonrpc":"1.0","id":17,"method":"tools/list","params":{"_meta":` + vectorMeta + `}}`), vectorHeaders("tools/list", ""), 400,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Body must be a single JSON-RPC request or notification object"}}`, ""},
		{"Last-Event-ID is ignored", callBody("6", "get_account", `{"account_id":"acc-001"}`, vectorMeta),
			vectorHeaders("tools/call", "get_account", [2]string{"Last-Event-ID", "x_1"}), 200, "released", "get_account ok 200"},
		{"Mcp-Session-Id is ignored", callBody("31", "get_account", `{"account_id":"acc-001"}`, vectorMeta),
			vectorHeaders("tools/call", "get_account", [2]string{"Mcp-Session-Id", "abc"}), 200, "released", "get_account ok 200"},
		{"a progress token is accepted", callBody("37", "get_account", `{"account_id":"acc-001"}`,
			`{"progressToken":7,"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`),
			vectorHeaders("tools/call", "get_account"), 200, "released", "get_account ok 200"},
	})
}

// The client's id comes back exactly, however large: Python ints are exact.
func TestLargeIdsComeBackExactly(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ id, method string }{
		{"9007199254740993", "tools/list"}, {"9007199254740993", "tools/call"}, {"100000000000000000000", "tools/call"},
	} {
		body := methodBody(c.id, c.method, "")
		headers := vectorHeaders(c.method, "")
		if c.method == "tools/call" {
			body, headers = callBody(c.id, "get_account", `{"account_id":"acc-001"}`, vectorMeta), vectorHeaders("tools/call", "get_account")
		}
		response := h.post(body, headers)
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":`+c.id+`,`) {
			t.Errorf("%s %s: %d %.200s", c.method, c.id, response.Code, response.Body)
		}
		if c.method == "tools/call" {
			rows := h.receipts.Read()
			if got := fmt.Sprint(rows[len(rows)-1].RequestID); got != c.id {
				t.Errorf("receipt request id %s, want %s", got, c.id)
			}
		}
	}
}

// Connector.__call__ hands every other HTTP method on /mcp to the SDK app, which answers 405 with Allow, and
// any other path is Starlette's 404.
func TestOtherHTTPMethodsAsPython(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{http.MethodPut, http.MethodOptions, http.MethodPatch, http.MethodHead} {
		request := newRequest(method, testResource, nil)
		request.Header.Set("Authorization", "Bearer "+tokenValue("full"))
		recorder := httptest.NewRecorder()
		h.gateway.ServeHTTP(recorder, request)
		want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Method Not Allowed"}}`
		if recorder.Code != 405 || recorder.Header().Get("Allow") != "GET, POST, DELETE" ||
			(method != http.MethodHead && !reflect.DeepEqual(decode(t, recorder.Body.Bytes()), decode(t, []byte(want)))) {
			t.Errorf("%s: %d %q %s", method, recorder.Code, recorder.Header().Get("Allow"), recorder.Body)
		}
	}
	recorder := httptest.NewRecorder()
	h.gateway.ServeHTTP(recorder, newRequest(http.MethodPost, "https://127.0.0.1:8765/other", strings.NewReader("{}")))
	if recorder.Code != 404 || recorder.Body.String() != "Not Found" {
		t.Errorf("POST /other: %d %q", recorder.Code, recorder.Body)
	}
}

// The tool list is pinned in the SDK's order (by name), and discovery in the shape it sends, so an SDK
// upgrade that changes either fails here and is decided deliberately.
func TestToolOrderAndDiscoveryArePinned(t *testing.T) {
	h := newHarness(t)
	listing := h.rpc("tools/list", nil, "full", nil, true)
	var names []string
	for _, tool := range path(decode(t, listing.Body.Bytes()), "result", "tools").([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if want := unlistedTools(t, "tools/list order"); !reflect.DeepEqual(names, want) {
		t.Errorf("tool order %v", names)
	}
	discovery := h.rpc("server/discover", nil, "full", nil, true)
	want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"gtm-connector","version":"0.2.0"}},`+
		`"cacheScope":"private","capabilities":{"prompts":{},"resources":{},"tools":{}},"resultType":"complete","supportedVersions":["2026-07-28"],"ttlMs":0}}`, h.number)
	if !reflect.DeepEqual(decode(t, discovery.Body.Bytes()), decode(t, []byte(want))) {
		t.Errorf("discovery %s", discovery.Body)
	}
}

// withoutAuditID drops a result's receipt id, which differs between the two implementations by design.
func withoutAuditID(document map[string]any) map[string]any {
	if meta, ok := path(document, "result", "_meta").(map[string]any); ok {
		delete(meta, "gtm.demo/auditId")
	}
	return document
}

// A bearer with no stated expiry is a 401 on the wire too.
func TestBearerWithoutExpiryIsUnauthorized(t *testing.T) {
	h := newHarness(t)
	h.tokens["fixture-no-expiry-bearer"] = &Token{Scopes: []string{"crm:read"}, Resource: testResource}
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "fixture-no-expiry-bearer", nil)
	if reason, _ := reasonOf(t, response); response.Code != 401 || reason != "invalid_token" || len(h.fx.ran()) != 0 {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}
