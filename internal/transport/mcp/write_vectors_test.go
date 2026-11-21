package mcp

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The Go gate serves read tools only. Each vector named here was recorded from the Python gateway while it
// served log_activity. The vector stays in its test as recorded, and this list gives the answer the Go gate
// gives in its place. A vector that is not named here must match its recording.

// The answers a write vector can have.
const (
	// wantRefused is HTTP 403 with code 1005 and reason crm_write_disabled, one finished receipt, and no effect.
	wantRefused = "refused"
	// wantWithheld is the 503 a refusal becomes when its receipt cannot finish: the receipt stays pending.
	wantWithheld = "withheld"
	// wantUnlisted is a tools/list answer with the recorded tools except log_activity.
	wantUnlisted = "unlisted"
)

type writeVector struct {
	recorded string // what the Python gateway answered
	want     string // what the Go gate answers
}

const inspectorListing = "scripts/check-inspector.py: tools/list"

var writeVectors = map[string]writeVector{
	"test_current_discovery_and_tool_scope_declarations: tools/list": {
		"six tools, log_activity requiring crm:write", wantUnlisted},
	"test_scopes_separate_both_sources_and_writes: log_activity with crm:read": {
		`403 insufficient_scope asking for scope="crm:write"`, wantRefused},
	"test_scopes_separate_both_sources_and_writes: log_activity with calls:read": {
		`403 insufficient_scope asking for scope="crm:write"`, wantRefused},
	"test_every_tool_attempt_has_one_complete_audit_row: attempt 5": {
		"200 with one crm/activities record", wantRefused},
	"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: first": {
		"200 with one crm/activities record", wantRefused},
	"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: retry": {
		"200 with the same record", wantRefused},
	"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: changed payload": {
		"200 with the tool error idempotency_conflict", wantRefused},
	"test_duplicate_write_attempts_also_consume_rate_limit: first": {
		"200 with one crm/activities record", wantRefused},
	"test_unknown_account_write_returns_an_explicit_audited_tool_error": {
		"200 with the tool error account not found", wantRefused},
	"test_write_committed_before_audit_failure_is_reused_on_retry: first": {
		"503 with the receipt pending and the activity written", wantWithheld},
	"test_write_committed_before_audit_failure_is_reused_on_retry: retry": {
		"200 with the activity written before", wantRefused},
	"tools/list with a string cursor": {
		"six tools", wantUnlisted},
	"tools/list order": {
		"six tools, log_activity third", wantUnlisted},
	"test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens: tools/list": {
		"six tools", wantUnlisted},
	"test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input: tools/list": {
		"six tools", wantUnlisted},
	inspectorListing: {
		"six tools, log_activity requiring crm:write", wantUnlisted},
}

// recordedTools are the default server's tools as the Python gateway lists them, in the SDK's order.
var recordedTools = []string{"get_account", "list_opportunities", "log_activity", "read_call_notes", "read_slack_messages", "search_accounts"}

var replayed sync.Map

// replay marks a named write vector as replayed. Only a vector on the list, with this answer, may take it.
func replay(t *testing.T, name, want string) {
	t.Helper()
	vector, ok := writeVectors[name]
	if !ok || vector.want != want {
		t.Fatalf("%q is not a write vector answered as %s", name, want)
	}
	t.Logf("write vector %q: Python answered %s; the Go gate must answer %s", name, vector.recorded, want)
	replayed.Store(name, true)
}

// unlistedTools replays a recorded tools/list vector and returns the names the Go gate lists.
func unlistedTools(t *testing.T, name string) []string {
	t.Helper()
	replay(t, name, wantUnlisted)
	return slices.DeleteFunc(slices.Clone(recordedTools), func(tool string) bool { return tool == writeTool })
}

// TestMain fails a whole run of the package that left a named write vector out. The Inspector's vector runs
// only with GTM_INSPECTOR=1.
func TestMain(m *testing.M) {
	code := m.Run()
	whole := true
	for _, filter := range []string{"test.run", "test.skip", "test.list"} {
		whole = whole && flag.Lookup(filter).Value.String() == ""
	}
	if code == 0 && whole {
		for name := range writeVectors {
			if _, ok := replayed.Load(name); !ok && (name != inspectorListing || os.Getenv("GTM_INSPECTOR") == "1") {
				fmt.Fprintln(os.Stderr, "write vector not replayed:", name)
				code = 1
			}
		}
	}
	os.Exit(code)
}

// writeRefusal is the gate's whole answer to a log_activity call with this id.
func writeRefusal(t *testing.T, id int) map[string]any {
	t.Helper()
	return decode(t, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%d,"error":{"code":1005,"message":"crm_write_disabled","data":{"reason":"crm_write_disabled"}}}`, id)))
}

// refusedReceipts checks that each of a transport's receipts finished as a 403 error with no rows.
func refusedReceipts(t *testing.T, rows []Receipt, count int) {
	t.Helper()
	if len(rows) != count {
		t.Fatalf("%d receipts, want %d", len(rows), count)
	}
	for _, row := range rows {
		if row.Status != "error" || *row.HTTPStatus != 403 || row.RowCount != 0 || len(row.Rows) != 0 ||
			row.Error == nil || *row.Error != "request_failed" {
			t.Fatalf("receipt %+v", row)
		}
	}
}

// writeRefused calls log_activity and checks the one refusal the gate gives it: HTTP 403, code 1005 and
// reason crm_write_disabled with no challenge, one receipt finished as an error, no tool run and no activity.
func (h *harness) writeRefused(arguments map[string]any, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	before := len(h.receipts.Read())
	response := h.tool(writeTool, arguments, token, nil)
	if response.Code != 403 || !reflect.DeepEqual(decode(h.t, response.Body.Bytes()), writeRefusal(h.t, h.number)) ||
		response.Header().Get("WWW-Authenticate") != "" {
		h.t.Fatalf("write with %s: %d %s %q", token, response.Code, response.Body, response.Header().Get("WWW-Authenticate"))
	}
	rows := h.receipts.Read()[before:]
	refusedReceipts(h.t, rows, 1)
	if rows[0].Tool != writeTool {
		h.t.Fatalf("receipt %+v", rows[0])
	}
	h.noWrite()
	return response
}

// noWrite checks that log_activity never ran and no activity exists.
func (h *harness) noWrite() {
	h.t.Helper()
	if slices.Contains(h.fx.ran(), writeTool) || h.fx.activityCount() != 0 {
		h.t.Fatalf("a write ran: %v, %d activities", h.fx.ran(), h.fx.activityCount())
	}
}

// replayWrite replays a named write vector that the gate refuses.
func (h *harness) replayWrite(name string, arguments map[string]any, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	replay(h.t, name, wantRefused)
	return h.writeRefused(arguments, token)
}

var fixtureActivity = map[string]any{"account_id": "acc-001", "kind": "renewal_call",
	"body": "Fixture owner requested a renewal call.", "idempotency_key": "fixture-write-001"}

// tools/list shows the read tools only and names neither log_activity nor crm:write, on both transports,
// with and without a selection, even to a token that carries crm:write. No tool is defined for a write
// scope, and the resource metadata offers none.
func TestToolsListHasNoWriteTool(t *testing.T) {
	check := func(label string, listing map[string]any, tools []string) {
		t.Helper()
		want := slices.Clone(tools)
		sort.Strings(want)
		text := fmt.Sprint(listing)
		if !reflect.DeepEqual(toolNames(t, listing), want) || strings.Contains(text, writeTool) || strings.Contains(text, "crm:write") {
			t.Errorf("%s: %s", label, text)
		}
	}
	for _, selected := range []bool{false, true} {
		tools, label := DefaultTools, "default"
		if selected {
			tools, label = SelectedTools, "selected"
		}
		h := newHarness(t, func(c *GatewayConfig) { c.Selected = selected })
		for _, token := range []string{"crm", "write", "full"} {
			listing := h.rpc("tools/list", nil, token, nil, false)
			if listing.Code != 200 {
				t.Fatalf("%s tools/list with %s: %d %s", label, token, listing.Code, listing.Body)
			}
			check(label+" over HTTP with "+token, decode(t, listing.Body.Bytes()), tools)
		}
		s := newStdio(t, []string{"crm:read", "crm:write"}, func(c *StdioConfig) { c.Selected = selected })
		s.send(legacyStart, legacyReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		s.answer(1)
		check(label+" over stdio", s.answer(2), tools)
		if len(h.receipts.Read())+len(s.receipts.Read()) != 0 {
			t.Fatalf("%s: listing left receipts", label)
		}
	}
	for name, definition := range Definitions {
		if name == writeTool || !slices.Contains([]string{"crm:read", "calls:read", "slack:read"}, definition.Scope) {
			t.Errorf("the gate defines %s requiring %q", name, definition.Scope)
		}
	}
	metadata := httptest.NewRecorder()
	newHarness(t).gateway.ServeHTTP(metadata, newRequest(http.MethodGet, "https://127.0.0.1:8765/.well-known/oauth-protected-resource/mcp", nil))
	if metadata.Code != 200 || strings.Contains(metadata.Body.String(), "write") {
		t.Fatalf("metadata %d %s", metadata.Code, metadata.Body)
	}
}

// A log_activity call under a valid read grant is refused with its receipt and writes nothing, on both
// transports, with and without a selection, and whatever its arguments are. The refusal asks for no scope,
// because no scope opens the tool. The grant is charged for the attempt and still reads afterwards.
func TestWriteWithAReadGrantIsRefused(t *testing.T) {
	for _, selected := range []bool{false, true} {
		h := newHarness(t, func(c *GatewayConfig) { c.Selected = selected })
		for _, token := range []string{"crm", "calls", "slack", "full"} {
			h.writeRefused(fixtureActivity, token)
			h.writeRefused(map[string]any{}, token)
		}
		h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil), "crm")
		if used := h.gateway.Windows()[fingerprint(tokenValue("crm"))].Used; used != 3 {
			t.Fatalf("the crm grant was charged %d times", used)
		}
		// Nothing issues crm:write. A token that carries it beside crm:read still reads the CRM, because the
		// read scope opens that source and the extra scope opens nothing.
		h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "full", nil), "crm")

		s := newStdio(t, []string{"crm:read", "calls:read", "slack:read"}, func(c *StdioConfig) { c.Selected = selected })
		s.send(legacyStart, legacyReady, call(2, writeTool, jsonOf(fixtureActivity)), string(callBody("3", writeTool, "{}", vectorMeta)))
		s.answer(1)
		for id := 2; id <= 3; id++ {
			if answer := s.answer(id); !reflect.DeepEqual(answer, writeRefusal(t, id)) {
				t.Fatalf("stdio write %d: %v", id, answer)
			}
		}
		rows := s.receipts.Read()
		refusedReceipts(t, rows, 2)
		if rows[0].Tool != writeTool || rows[1].Tool != writeTool || s.fx.activityCount() != 0 || len(s.fx.ran()) != 0 {
			t.Fatalf("stdio: receipts %+v, ran %v", rows, s.fx.ran())
		}
	}
}

// A token that carries only crm:write gets nothing: every tool refuses it with a receipt, no refusal asks
// for crm:write, and no tool body runs. It may still list the tools, as any valid token may.
func TestWriteScopeOpensNothing(t *testing.T) {
	arguments := map[string]map[string]any{
		"search_accounts": {}, "get_account": {"account_id": "acc-001"}, "list_opportunities": {},
		"read_call_notes": {"account_id": "acc-001"}, "read_slack_messages": {"account_id": "acc-003"},
		"prepare_renewal_brief": {"account_id": "acc-001"}, "read_renewal_brief": {"brief_id": "brief-fixture-001"},
	}
	for _, selected := range []bool{false, true} {
		tools := DefaultTools
		if selected {
			tools = SelectedTools
		}
		h := newHarness(t, func(c *GatewayConfig) { c.Selected = selected })
		s := newStdio(t, []string{"crm:write"}, func(c *StdioConfig) { c.Selected = selected })
		s.send(legacyStart, legacyReady)
		s.answer(1)
		for i, tool := range tools {
			scope := Definitions[tool].Scope
			response := h.tool(tool, arguments[tool], "write", nil)
			reason, code := reasonOf(t, response)
			challenge := response.Header().Get("WWW-Authenticate")
			if response.Code != 403 || reason != "insufficient_scope" || code != "1004" || !strings.Contains(challenge, `scope="`+scope+`"`) ||
				strings.Contains(response.Body.String()+challenge, "crm:write") {
				t.Fatalf("%s with crm:write alone: %d %s %q", tool, response.Code, response.Body, challenge)
			}
			s.send(call(i+2, tool, jsonOf(arguments[tool])))
			if answer := s.answer(i + 2); path(answer, "error", "data", "reason") != "insufficient_scope" ||
				path(answer, "error", "message") != "insufficient_scope: "+scope+" required" {
				t.Fatalf("stdio %s with crm:write alone: %v", tool, answer)
			}
		}
		h.writeRefused(fixtureActivity, "write")
		s.send(call(99, writeTool, jsonOf(fixtureActivity)))
		if answer := s.answer(99); !reflect.DeepEqual(answer, writeRefusal(t, 99)) {
			t.Fatalf("stdio write with crm:write alone: %v", answer)
		}
		refusedReceipts(t, h.receipts.Read(), len(tools)+1)
		refusedReceipts(t, s.receipts.Read(), len(tools)+1)
		if len(h.fx.ran())+len(s.fx.ran()) != 0 || h.fx.activityCount()+s.fx.activityCount() != 0 {
			t.Fatalf("a tool ran: %v %v", h.fx.ran(), s.fx.ran())
		}
	}
}
