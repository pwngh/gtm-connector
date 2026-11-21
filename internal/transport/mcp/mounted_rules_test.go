package mcp_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The rules of AGENTS.md that a mounted endpoint can break, each tested through the surface's handler on
// PostgreSQL: one outer receipt for every call (rule one), a scope and a selection that open only their own
// (rule two), a bad token refused (rule three), and no credential in anything written or answered (rule seven).

var bearer = mcp.FixtureBearer

// Rule one. Every tool call leaves exactly one outer receipt, committed before the tool reads anything and
// finished before the reply, whether the call is released, refused or fails. A read inside a prepared brief
// leaves a receipt of its own under it. A call whose receipt cannot be committed runs nothing.
func TestMountedCallsLeaveOneOuterReceipt(t *testing.T) {
	var store *failing
	m := mount(t, cards.Selection{"acc-003": true, "acc-007": false}, func(c *mcptools.Config) {
		store = &failing{Store: c.Receipts}
		c.Receipts = store
	})
	m.fixture()
	account := map[string]any{"account_id": "acc-003"}

	// The five direct reads: the receipt names exactly the records the reply holds, bound as it finished.
	for id, tool := range []string{"search_accounts", "get_account", "list_opportunities", "read_call_notes", "read_slack_messages"} {
		arguments := account
		if tool == "search_accounts" {
			arguments = map[string]any{"query": "thornfield"}
		}
		response, row := m.call(id+1, tool, arguments, bearer("read"))
		released := records(t, response)
		bound := m.count("SELECT count(*) FROM receipt_records WHERE receipt_id = $1", row.ID)
		if row.Status != "ok" || len(released) == 0 || !reflect.DeepEqual(row.Rows, released) || bound != len(released) ||
			row.Principal == nil || *row.Principal != m.machine.ID || row.TokenID != receipts.Fingerprint(bearer("read")) ||
			row.Scope == nil || *row.Scope != mcp.Definitions[tool].Scope || row.Parent != nil ||
			at(decode(t, response.Body.Bytes()), "result", "_meta", "gtm.demo/auditId") != row.ID {
			t.Fatalf("%s: receipt %+v binds %d records for the reply's %v", tool, row, bound, released)
		}
	}
	if made := m.book.made(); !reflect.DeepEqual(made, []string{"search_accounts", "get_account", "list_opportunities",
		"read_call_notes", "read_slack_messages"}) {
		t.Fatalf("the direct reads read %v", made)
	}

	// A prepared brief: the call's one outer receipt is the brief's seal, and each read inside it has its own.
	response, outer := m.call("prepare", "prepare_renewal_brief", account, bearer("read"))
	brief := records(t, response)
	inside := m.receipts("parent_id = $1", outer.ID)
	var tools []string
	for i, child := range inside {
		tools = append(tools, child.label())
		want := fmt.Sprintf(`{"kind": "brief_child", "ordinal": %d, "parent_audit_id": "%s"}`, i+1, outer.ID)
		if !sameJSON(t, []byte(child.RequestID), []byte(want)) || child.TokenID != outer.TokenID ||
			child.Principal == nil || *child.Principal != m.machine.ID || child.Brief == nil || !strings.Contains(brief[0], *child.Brief) {
			t.Fatalf("child %d: %+v", i+1, child)
		}
	}
	if !reflect.DeepEqual(tools, []string{"get_account ok 200", "list_opportunities ok 200", "read_call_notes ok 200",
		"read_slack_messages ok 200"}) || !reflect.DeepEqual(outer.Rows, brief) || outer.Status != "ok" ||
		m.count("SELECT count(*) FROM receipts WHERE tool = 'prepare_renewal_brief'") != 1 ||
		m.count("SELECT count(*) FROM cards WHERE seal_receipt_id = $1", outer.ID) != 1 {
		t.Fatalf("the prepared brief: outer %+v, inside %v", outer, tools)
	}
	briefID := strings.Split(brief[0], "/")[1]
	response, row := m.call("read", "read_renewal_brief", map[string]any{"brief_id": briefID}, bearer("read"))
	if again := records(t, response); !reflect.DeepEqual(again, brief) || !reflect.DeepEqual(row.Rows, brief) ||
		len(m.receipts("parent_id = $1", row.ID)) != 0 {
		t.Fatalf("the brief read again: %v, receipt %+v", again, row)
	}
	sourceReads := len(m.book.made())

	// Calls that release nothing: each still leaves its one finished receipt, with no rows, and reads nothing.
	for _, c := range []struct {
		label, tool string
		arguments   any
		bearer      string
		status      int
		answer      string // a gateway reason, or a tool error's code
	}{
		{"no arguments", "get_account", map[string]any{}, bearer("read"), 400, "invalid_arguments"},
		{"an unknown tool", "nope", map[string]any{}, bearer("read"), 200, "nope"},
		{"another source's scope", "read_call_notes", account, bearer("crm"), 403, "insufficient_scope"},
		{"no bearer", "get_account", account, "", 401, "invalid_token"},
		{"an unknown bearer", "get_account", account, "never-issued", 401, "invalid_token"},
		{"the write tool", mcp.WriteTool, map[string]any{"account_id": "acc-003"}, bearer("read"), 403, "crm_write_disabled"},
		{"an account outside the selection", "get_account", map[string]any{"account_id": "acc-001"}, bearer("read"), 200, "account_not_selected"},
		{"a brief as of no date", "prepare_renewal_brief", map[string]any{"account_id": "acc-003", "as_of": "tomorrow"}, bearer("read"), 200, "invalid_as_of"},
		{"an unknown brief", "read_renewal_brief", map[string]any{"brief_id": "brief-" + strings.Repeat("0", 32)}, bearer("read"), 200, "brief_not_found"},
	} {
		response, row := m.call(c.label, c.tool, c.arguments, c.bearer)
		answer := refusal(t, response)
		if answer == "" {
			answer = toolError(t, response)
		}
		if response.Code != c.status || answer != c.answer || row.Status != "error" || len(row.Rows) != 0 ||
			len(m.receipts("parent_id = $1", row.ID)) != 0 {
			t.Errorf("%s: %d %s, receipt %+v", c.label, response.Code, response.Body, row)
		}
	}
	if made := m.book.made(); len(made) != sourceReads {
		t.Fatalf("calls that released nothing read the book: %v", made[sourceReads:])
	}

	// A request that is no call leaves no receipt.
	calls := len(m.outer())
	if listing := m.list(bearer("read")); listing.Code != 200 || len(m.outer()) != calls {
		t.Fatalf("tools/list: %d, %d receipts", listing.Code, len(m.outer())-calls)
	}

	// With the receipt store down, the call gets no receipt, so nothing runs and nothing is read.
	store.downAtStart = true
	down := m.post(callBody(1, "get_account", account), requestHeaders("tools/call", "get_account", bearer("read")))
	store.downAtStart = false
	if down.Code != 503 || strings.TrimSpace(down.Body.String()) != `{"error":"audit_unavailable; call was not executed"}` ||
		len(m.outer()) != calls || len(m.book.made()) != sourceReads {
		t.Fatalf("a call without a receipt: %d %s, read %v", down.Code, down.Body, m.book.made()[sourceReads:])
	}
	// With it down when a receipt should finish, nothing is released: not the records a read returned, and
	// not a refusal's own words. The receipt stays pending, for its lease to end.
	for id, arguments := range []map[string]any{account, {}} {
		store.down = true
		withheld := m.post(callBody(id, "get_account", arguments), requestHeaders("tools/call", "get_account", bearer("read")))
		store.down = false
		rows := m.outer()[calls+id:]
		if withheld.Code != 503 || strings.TrimSpace(withheld.Body.String()) != `{"error":"execution_or_audit_unavailable; no result released"}` ||
			len(rows) != 1 || rows[0].Status != "pending" {
			t.Fatalf("a call whose receipt cannot finish: %d %s, receipts %+v", withheld.Code, withheld.Body, rows)
		}
		finishInterrupted(t, m, rows[0].ID)
	}
}

// finishInterrupted finishes a receipt a test left pending on purpose, as the store finishes one whose lease
// ended, so the test's last check finds none pending.
func finishInterrupted(t *testing.T, m *mounted, id string) {
	t.Helper()
	must(t, m.sql(`UPDATE receipts SET status = 'error', http_status = 503, error = 'interrupted', finished_at = clock_timestamp(),
		finishing_lease = lease_token WHERE id = $1`, id))
}

// Rule two, scopes. A token for one source reads that source and no other: every other tool refuses it with
// 403 and names its own scope, leaves a receipt with no rows, and never reaches the source. Inside a prepared
// brief the same holds for each read: a read the grant does not cover is refused under its own receipt, the
// source is not read, and the card says so.
func TestMountedScopeOpensOnlyItsSource(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	account := map[string]any{"account_id": "acc-003"}
	tools := []string{"search_accounts", "get_account", "list_opportunities", "read_call_notes", "read_slack_messages"}
	source := map[string]string{"crm:read": "crm/", "calls:read": "calls/", "slack:read": "slack/"}
	id := 0
	for _, grant := range []string{"crm", "calls", "slack"} {
		held := mcp.FixtureScopes()[grant][0]
		for _, tool := range tools {
			id++
			needs := mcp.Definitions[tool].Scope
			arguments := account
			if tool == "search_accounts" {
				arguments = map[string]any{}
			}
			read := len(m.book.made())
			response, row := m.call(id, tool, arguments, bearer(grant))
			if needs == held {
				released := records(t, response)
				for _, ref := range released {
					if !strings.HasPrefix(ref, source[held]) {
						t.Errorf("%s with %s released %s", tool, held, ref)
					}
				}
				if len(released) == 0 || len(m.book.made()) != read+1 {
					t.Errorf("%s with %s released %v", tool, held, released)
				}
				continue
			}
			challenge := response.Header().Get("WWW-Authenticate")
			if response.Code != 403 || refusal(t, response) != "insufficient_scope" || !strings.Contains(challenge, `scope="`+needs+`"`) ||
				row.label() != tool+" error 403" || len(row.Rows) != 0 || len(m.book.made()) != read ||
				strings.Contains(response.Body.String(), "Thornfield") || strings.Contains(response.Body.String(), "record_ref") {
				t.Errorf("%s with %s: %d %s %q, receipt %+v", tool, held, response.Code, response.Body, challenge, row)
			}
		}
	}

	// A brief prepared with crm:read alone, for an account whose Slack is selected.
	m.serve(cards.Selection{"acc-003": true}, nil)
	read := len(m.book.made())
	response, outer := m.call("brief", "prepare_renewal_brief", account, bearer("crm"))
	records(t, response)
	briefed := at(decode(t, response.Body.Bytes()), "result", "structuredContent", "brief")
	var inside []string
	for _, child := range m.receipts("parent_id = $1", outer.ID) {
		inside = append(inside, child.label())
	}
	if !reflect.DeepEqual(inside, []string{"get_account ok 200", "list_opportunities ok 200", "read_call_notes error 403",
		"read_slack_messages error 403"}) || !reflect.DeepEqual(m.book.made()[read:], []string{"get_account", "list_opportunities"}) ||
		at(briefed, "notes_status") != "denied" || at(briefed, "slack_status") != "denied" {
		t.Fatalf("a brief under crm:read alone: reads %v, sources read %v, statuses %v %v", inside, m.book.made()[read:],
			at(briefed, "notes_status"), at(briefed, "slack_status"))
	}
	for _, unit := range at(briefed, "units").([]any) {
		if ref, _ := at(unit, "record_ref").(string); !strings.HasPrefix(ref, "crm/") {
			t.Errorf("the brief holds a unit of %s", ref)
		}
	}
	for _, text := range []string{"note-003", "msg-003", "calls/call_notes", "slack/messages"} {
		if strings.Contains(response.Body.String(), text) {
			t.Errorf("the reply holds %q, which crm:read does not open", text)
		}
	}
}

// Rule two, the selection. With a selection, an account outside it is refused before any source is read, a
// Slack read needs the account's Slack selected, a search and an account-wide list hold only selected
// accounts, and the two brief tools are offered. Without one they are not.
func TestMountedSelectionLimitsAccounts(t *testing.T) {
	m := mount(t, cards.Selection{"acc-003": true, "acc-007": false}, nil)
	m.fixture()
	read := bearer("read")
	for id, c := range []struct {
		tool, account, code string
	}{
		{"get_account", "acc-001", "account_not_selected"},
		{"list_opportunities", "acc-001", "account_not_selected"},
		{"read_call_notes", "acc-001", "account_not_selected"},
		{"read_slack_messages", "acc-001", "account_not_selected"},
		{"read_slack_messages", "acc-007", "slack_not_selected"},
		{"prepare_renewal_brief", "acc-001", "account_not_selected"},
		{"get_account", "acc-999", "account_not_selected"},
	} {
		response, row := m.call(id, c.tool, map[string]any{"account_id": c.account}, read)
		if toolError(t, response) != c.code || row.label() != c.tool+" error 200" || len(row.Rows) != 0 ||
			len(m.receipts("parent_id = $1", row.ID)) != 0 || strings.Contains(response.Body.String(), "record_ref") {
			t.Errorf("%s of %s: %d %s, receipt %+v", c.tool, c.account, response.Code, response.Body, row)
		}
	}
	if made := m.book.made(); len(made) != 0 || m.count("SELECT count(*) FROM cards") != 0 {
		t.Fatalf("refused reads reached the book: %v", made)
	}
	response, _ := m.call("search", "search_accounts", map[string]any{}, read)
	if found := records(t, response); !reflect.DeepEqual(found, []string{"crm/accounts/acc-007", "crm/accounts/acc-003"}) {
		t.Errorf("a search under the selection found %v", found)
	}
	response, _ = m.call("search one", "search_accounts", map[string]any{"query": "alder"}, read)
	if found := records(t, response); len(found) != 0 {
		t.Errorf("a search for an unselected account found %v", found)
	}
	response, _ = m.call("list", "list_opportunities", map[string]any{}, read)
	if found := records(t, response); !reflect.DeepEqual(found, []string{"crm/opportunities/opp-003", "crm/opportunities/opp-007"}) {
		t.Errorf("the opportunities under the selection are %v", found)
	}
	response, _ = m.call("slack", "read_slack_messages", map[string]any{"account_id": "acc-003"}, read)
	if found := records(t, response); !reflect.DeepEqual(found, []string{"slack/messages/msg-003"}) {
		t.Errorf("the selected Slack thread: %v", found)
	}
	// An account selected without Slack is briefed without a Slack read, whatever the grant holds.
	response, outer := m.call("brief", "prepare_renewal_brief", map[string]any{"account_id": "acc-007"}, read)
	records(t, response)
	for _, child := range m.receipts("parent_id = $1", outer.ID) {
		if child.Tool == "read_slack_messages" {
			t.Errorf("a brief of an account selected without Slack read Slack: %+v", child)
		}
	}
	if made := m.book.made(); !reflect.DeepEqual(made, []string{"search_accounts", "search_accounts", "list_opportunities",
		"read_slack_messages", "get_account", "list_opportunities", "read_call_notes"}) {
		t.Errorf("the book was read %v", made)
	}

	names := func(m *mounted) []string {
		var listed []string
		tools, _ := at(decode(t, m.list(read).Body.Bytes()), "result", "tools").([]any)
		for _, tool := range tools {
			listed = append(listed, at(tool, "name").(string))
		}
		slices.Sort(listed)
		return listed
	}
	selected := slices.Clone(mcp.SelectedTools)
	slices.Sort(selected)
	if got := names(m); !reflect.DeepEqual(got, selected) {
		t.Errorf("with a selection the tools are %v", got)
	}
	m.serve(nil, nil)
	unselected := slices.Clone(mcp.DefaultTools)
	slices.Sort(unselected)
	if got := names(m); !reflect.DeepEqual(got, unselected) {
		t.Errorf("without a selection the tools are %v", got)
	}
	if response, _ := m.call("no brief", "prepare_renewal_brief", map[string]any{"account_id": "acc-003"}, read); response.Code != 200 ||
		at(decode(t, response.Body.Bytes()), "result", "isError") != true {
		t.Errorf("a brief without a selection: %d %s", response.Code, response.Body)
	}
}

// Rule three. A bearer that is missing, malformed, unknown, expired, revoked, of a disabled principal, of
// another workspace, limited to a selection, doubled, or replaced by a signed-in browser session is refused
// with 401 and the same words, whichever it is. The call leaves one receipt with no rows, reads nothing and
// charges no rate window, and nothing else the request carries is tried in the bearer's place.
func TestMountedBadTokensAreRefused(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	insert := func(raw, workspace, principal, columns, values string) {
		t.Helper()
		digest := sha256.Sum256([]byte(raw))
		must(t, m.sql(`INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, `+columns+`)
			VALUES ($1, $2, $3, ARRAY['crm:read'], `+values+`)`, workspace, principal, digest[:]))
	}
	insert("expired-bearer", m.workspace.ID, m.machine.ID, "created_at, expires_at", "now() - interval '2 hours', now() - interval '1 hour'")
	insert("revoked-bearer", m.workspace.ID, m.machine.ID, "expires_at, revoked_at", "now() + interval '1 hour', now()")
	insert("selection-bearer", m.workspace.ID, m.machine.ID, "expires_at, selection", "now() + interval '1 hour', ARRAY['acc-003']")
	disabled, err := m.store.CreatePrincipal(ctx, m.workspace, "machine", "Disabled assistant")
	must(t, err)
	insert("disabled-bearer", m.workspace.ID, disabled.ID, "expires_at", "now() + interval '1 hour'")
	must(t, m.store.DisablePrincipal(ctx, m.workspace.ID, disabled.ID))
	elsewhere, err := m.store.EnsureWorkspace(ctx, "elsewhere", "demo", "Another workspace")
	must(t, err)
	stranger, err := m.store.CreatePrincipal(ctx, elsewhere, "machine", "Another assistant")
	must(t, err)
	insert("elsewhere-bearer", elsewhere.ID, stranger.ID, "expires_at", "now() + interval '1 hour'")

	// A browser session that is live: the API answers it, and the MCP endpoint must not.
	person, err := m.store.CreatePrincipal(ctx, m.workspace, "person", "Reader")
	must(t, err)
	session := strings.Repeat("s", 43)
	digest := sha256.Sum256([]byte(session))
	_, err = m.store.CreateSession(ctx, digest[:], person, time.Hour)
	must(t, err)
	cookie := [2]string{"Cookie", "__Host-gtm-session=" + session}
	if signedIn := m.send(http.MethodGet, "/api/v1/session", mountedHost, nil, [][2]string{cookie}); signedIn.Code != 200 ||
		at(decode(t, signedIn.Body.Bytes()), "signed_in") != true {
		t.Fatalf("the session is not live: %d %s", signedIn.Code, signedIn.Body)
	}

	account := map[string]any{"account_id": "acc-003"}
	authorization := func(values ...string) [][2]string {
		headers := requestHeaders("tools/call", "get_account", "")
		for _, value := range values {
			headers = append(headers, [2]string{"Authorization", value})
		}
		return headers
	}
	cases := []struct {
		label     string
		headers   [][2]string
		principal string // who the receipt names, "" for nobody
	}{
		{"no bearer", authorization(), ""},
		{"an unknown bearer", authorization("Bearer never-issued-bearer"), ""},
		{"another scheme", authorization("Basic " + bearer("crm")), ""},
		{"a bearer in two parts", authorization("Bearer " + bearer("crm") + " again"), ""},
		{"an empty bearer", authorization("Bearer "), ""},
		{"two Authorization headers", authorization("Bearer "+bearer("crm"), "Bearer "+bearer("read")), m.machine.ID},
		{"an expired bearer", authorization("Bearer expired-bearer"), m.machine.ID},
		{"a revoked bearer", authorization("Bearer revoked-bearer"), m.machine.ID},
		{"a bearer limited to a selection", authorization("Bearer selection-bearer"), m.machine.ID},
		{"a disabled principal's bearer", authorization("Bearer disabled-bearer"), disabled.ID},
		{"another workspace's bearer", authorization("Bearer elsewhere-bearer"), ""},
		{"a session cookie alone", append(authorization(), cookie), ""},
		{"a session cookie beside an unknown bearer", append(authorization("Bearer never-issued-bearer"), cookie), ""},
		{"a session cookie as the bearer", authorization("Bearer " + session), ""},
	}
	for id, c := range cases {
		before := len(m.outer())
		response := m.post(callBody(id, "get_account", account), c.headers)
		rows := m.outer()[before:]
		challenge := response.Header().Get("WWW-Authenticate")
		want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":1001,"message":"invalid or expired access token","data":{"reason":"invalid_token"}}}`, id)
		if response.Code != 401 || !sameJSON(t, response.Body.Bytes(), []byte(want)) || !strings.Contains(challenge, `error="invalid_token"`) ||
			strings.Contains(challenge, "scope=") {
			t.Errorf("%s: %d %s %q", c.label, response.Code, response.Body, challenge)
		}
		if len(rows) != 1 || rows[0].label() != "get_account error 401" || len(rows[0].Rows) != 0 {
			t.Fatalf("%s: receipts %+v", c.label, rows)
		}
		if named := rows[0].Principal; (named == nil) != (c.principal == "") || named != nil && *named != c.principal {
			t.Errorf("%s: the receipt names %v, want %q", c.label, named, c.principal)
		}
		// A listing with the same credentials is refused too, and a request that is no call leaves no receipt.
		listing := m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":`+requestMeta+`}}`),
			append(requestHeaders("tools/list", "", ""), c.headers[len(requestHeaders("tools/call", "get_account", "")):]...))
		if listing.Code != 401 || refusal(t, listing) != "invalid_token" || len(m.outer()) != before+1 {
			t.Errorf("%s: tools/list answered %d %s", c.label, listing.Code, listing.Body)
		}
	}
	if made, charged := m.book.made(), m.gateway.Windows(); len(made) != 0 || len(charged) != 0 {
		t.Fatalf("refused bearers read %v and were charged %v", made, charged)
	}
	// The grants that were named beside a refused credential are as they were.
	for id, name := range []string{"crm", "read"} {
		response, _ := m.call(id, "get_account", account, bearer(name))
		records(t, response)
	}
	// A bearer that still verifies does not open the browser API, which takes its session cookie only.
	api := m.send(http.MethodGet, "/api/v1/accounts", mountedHost, nil, [][2]string{{"Authorization", "Bearer " + bearer("read")}})
	if api.Code != 401 || !strings.Contains(api.Body.String(), "authorization_refused") {
		t.Errorf("the API with a bearer: %d %s", api.Code, api.Body)
	}
}

// The Host and Origin checks at the mounted endpoint are the gateway's: only the public origin's own Host,
// over TLS, with no Origin header. A call refused for any of them still leaves its receipt, where the browser
// surface's own Host check would have answered first and left none. Every other path keeps the surface's check.
func TestMountedHostAndOriginChecks(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	body := callBody(1, "get_account", map[string]any{"account_id": "acc-003"})
	headers := requestHeaders("tools/call", "get_account", bearer("read"))
	plain := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		request.Host = mountedHost
		for _, header := range headers {
			request.Header.Add(header[0], header[1])
		}
		recorder := httptest.NewRecorder()
		m.surface.ServeHTTP(recorder, request)
		return recorder
	}
	for label, send := range map[string]func() *httptest.ResponseRecorder{
		"another Host": func() *httptest.ResponseRecorder {
			return m.send(http.MethodPost, "/mcp", "evil.example", body, headers)
		},
		"an allowed alias": func() *httptest.ResponseRecorder {
			return m.send(http.MethodPost, "/mcp", "localhost:8765", body, headers)
		},
		"an Origin header": func() *httptest.ResponseRecorder {
			return m.post(body, append(slices.Clone(headers), [2]string{"Origin", mountedOrigin}))
		},
		"another Origin": func() *httptest.ResponseRecorder {
			return m.post(body, append(slices.Clone(headers), [2]string{"Origin", "https://evil.example"}))
		},
		"no TLS": plain,
	} {
		before := len(m.outer())
		response := send()
		rows := m.outer()[before:]
		if response.Code != 403 || refusal(t, response) != "untrusted_origin_or_host" || len(rows) != 1 ||
			rows[0].label() != "get_account error 403" || len(rows[0].Rows) != 0 {
			t.Errorf("%s: %d %s, receipts %+v", label, response.Code, response.Body, rows)
		}
	}
	if made := m.book.made(); len(made) != 0 {
		t.Fatalf("a refused Host or Origin read %v", made)
	}
	calls := len(m.outer())
	// Not a call: refused the same way, with no receipt.
	listing := m.send(http.MethodPost, "/mcp", "evil.example", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":`+requestMeta+`}}`),
		requestHeaders("tools/list", "", bearer("read")))
	if listing.Code != 403 || refusal(t, listing) != "untrusted_origin_or_host" {
		t.Errorf("tools/list on another Host: %d %s", listing.Code, listing.Body)
	}
	if get := m.send(http.MethodGet, "/mcp", mountedHost, nil, nil); get.Code != 405 || get.Header().Get("Allow") != "POST" {
		t.Errorf("GET /mcp: %d %q", get.Code, get.Header().Get("Allow"))
	}
	if remove := m.send(http.MethodDelete, "/mcp", mountedHost, nil, nil); remove.Code != 405 {
		t.Errorf("DELETE /mcp: %d", remove.Code)
	}
	// Every other method meets the same Host and Origin checks before it is told the method is not allowed.
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut, http.MethodPatch, http.MethodOptions, http.MethodHead} {
		for label, send := range map[string]func() *httptest.ResponseRecorder{
			"another Host":     func() *httptest.ResponseRecorder { return m.send(method, "/mcp", "evil.example", nil, nil) },
			"an allowed alias": func() *httptest.ResponseRecorder { return m.send(method, "/mcp", "localhost:8765", nil, nil) },
			"an Origin header": func() *httptest.ResponseRecorder {
				return m.send(method, "/mcp", mountedHost, nil, [][2]string{{"Origin", mountedOrigin}})
			},
		} {
			response := send()
			if response.Code != 403 || response.Header().Get("Allow") != "" ||
				method != http.MethodHead && refusal(t, response) != "untrusted_origin_or_host" {
				t.Errorf("%s /mcp from %s: %d %q %s", method, label, response.Code, response.Header().Get("Allow"), response.Body)
			}
		}
		if allowed := m.send(method, "/mcp", mountedHost, nil, nil); allowed.Code != 405 {
			t.Errorf("%s /mcp: %d", method, allowed.Code)
		}
	}
	if below := m.send(http.MethodPost, "/mcp/x", mountedHost, body, headers); below.Code != 404 {
		t.Errorf("POST /mcp/x: %d %s", below.Code, below.Body)
	}
	// The metadata that describes the endpoint, and the surface's own Host check on everything but /mcp.
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		metadata := m.send(http.MethodGet, path, mountedHost, nil, nil)
		document := decode(t, metadata.Body.Bytes())
		if metadata.Code != 200 || document["resource"] != mountedOrigin+"/mcp" ||
			!reflect.DeepEqual(document["scopes_supported"], []any{"crm:read"}) || strings.Contains(metadata.Body.String(), "write") {
			t.Errorf("%s: %d %s", path, metadata.Code, metadata.Body)
		}
		if other := m.send(http.MethodGet, path, "evil.example", nil, nil); other.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s on another Host: %d %s", path, other.Code, other.Body)
		}
	}
	if below := m.send(http.MethodPost, "/mcp/x", "evil.example", body, headers); below.Code != http.StatusMisdirectedRequest {
		t.Errorf("POST /mcp/x on another Host: %d %s", below.Code, below.Body)
	}
	if len(m.outer()) != calls {
		t.Fatalf("requests that are no call left %d receipts", len(m.outer())-calls)
	}
}

// Rule seven. No bearer reaches anything the endpoint answers or the store keeps: not a reply, a header, a
// challenge or an error, and not a receipt, a saved brief or a stored record, whether the bearer was admitted,
// refused, or presented while the store failed. Receipts and briefs hold its SHA-256 fingerprint only.
func TestMountedCredentialsReachNothing(t *testing.T) {
	var store *failing
	m := mount(t, cards.Selection{"acc-003": true}, func(c *mcptools.Config) {
		store = &failing{Store: c.Receipts}
		c.Receipts = store
	})
	m.fixture()
	secrets := []string{bearer("read"), bearer("crm"), "never-issued-secret-bearer-000000000000000000",
		"expired-secret-bearer-00000000000000000000000", "Basic c2VjcmV0LXVzZXI6c2VjcmV0LXBhc3M="}
	digest := sha256.Sum256([]byte(secrets[3]))
	must(t, m.sql(`INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, created_at, expires_at)
		VALUES ($1, $2, $3, ARRAY['crm:read'], now() - interval '2 hours', now() - interval '1 hour')`,
		m.workspace.ID, m.machine.ID, digest[:]))
	account := map[string]any{"account_id": "acc-003"}
	id := 0
	next := func() int { id++; return id }
	for _, secret := range secrets[:4] {
		m.call(next(), "get_account", account, secret)
		m.call(next(), "read_call_notes", account, secret)
		m.call(next(), "get_account", map[string]any{"account_id": 7}, secret)
		m.call(next(), "get_account", map[string]any{"account_id": "acc-001"}, secret)
		m.call(next(), mcp.WriteTool, account, secret)
		m.call(next(), "prepare_renewal_brief", account, secret)
		m.call(next(), "read_renewal_brief", map[string]any{"brief_id": "brief-" + strings.Repeat("0", 32)}, secret)
		m.list(secret)
		m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`), requestHeaders("tools/call", "get_account", secret))
		m.send(http.MethodPost, "/mcp", "evil.example", callBody(next(), "get_account", account), requestHeaders("tools/call", "get_account", secret))
		m.send(http.MethodGet, "/mcp", mountedHost, nil, [][2]string{{"Authorization", "Bearer " + secret}})
		m.send(http.MethodGet, "/api/v1/accounts", mountedHost, nil, [][2]string{{"Authorization", "Bearer " + secret}})
		store.downAtStart = true
		m.post(callBody(next(), "get_account", account), requestHeaders("tools/call", "get_account", secret))
		store.downAtStart = false
	}
	// A credential that is no bearer at all is recorded by its fingerprint too.
	m.post(callBody(next(), "get_account", account), append(requestHeaders("tools/call", "get_account", ""), [2]string{"Authorization", secrets[4]}))
	store.down = true
	interrupted := m.post(callBody(next(), "get_account", map[string]any{}), requestHeaders("tools/call", "get_account", secrets[0]))
	store.down = false
	if interrupted.Code != 503 {
		t.Fatalf("a refusal with the store down: %d %s", interrupted.Code, interrupted.Body)
	}
	for _, row := range m.receipts("status = 'pending'") {
		finishInterrupted(t, m, row.ID)
	}

	kept := m.dump(`SELECT coalesce(string_agg(r::text, E'\n'), '') FROM receipts r`) +
		m.dump(`SELECT coalesce(string_agg(convert_from(card_bytes, 'UTF8'), E'\n'), '') FROM cards`) +
		m.dump(`SELECT coalesce(string_agg(convert_from(raw_body, 'UTF8'), E'\n'), '') FROM source_records`) +
		m.dump(`SELECT coalesce(string_agg(u::text, E'\n'), '') FROM evidence_units u`) +
		m.dump(`SELECT coalesce(string_agg(t.scopes::text || encode(t.token_sha256, 'hex'), E'\n'), '') FROM machine_tokens t`)
	answered := strings.Join(m.said, "\n")
	if m.count("SELECT count(*) FROM cards") == 0 || !strings.Contains(kept, "prepared_by") || len(m.said) < 50 {
		t.Fatalf("the test did not prepare a brief or made too few requests: %d answers", len(m.said))
	}
	for _, secret := range secrets {
		raw := strings.TrimPrefix(secret, "Basic ")
		if strings.Contains(answered, raw) {
			t.Errorf("an answer holds the credential %.12s...", raw)
		}
		if strings.Contains(kept, raw) {
			t.Errorf("the store holds the credential %.12s...", raw)
		}
		if !strings.Contains(kept, receipts.Fingerprint(secret)) {
			t.Errorf("no receipt holds the fingerprint of the credential %.12s...", raw)
		}
		if strings.Contains(answered, receipts.Fingerprint(secret)) {
			t.Errorf("an answer holds the fingerprint of the credential %.12s...", raw)
		}
	}
}

// Each read inside a prepared brief checks the caller's own bearer again and counts one attempt against its
// rate window, under a receipt of its own. A bearer that expires, or a window that fills, part way through
// leaves no brief: the read is refused under its receipt, the source is not read, and the call is a tool
// error.
func TestMountedReadsInsideABriefCheckTheBearerAgain(t *testing.T) {
	account := map[string]any{"account_id": "acc-003"}
	labels := func(m *mounted, outer receipt) []string {
		var inside []string
		for _, child := range m.receipts("parent_id = $1", outer.ID) {
			inside = append(inside, child.label())
		}
		return inside
	}

	// One charge for the call and one for each of its four reads: five fit a window of five exactly.
	m := mount(t, cards.Selection{"acc-003": true}, func(c *mcptools.Config) { c.RateLimit = 5 })
	m.fixture()
	response, outer := m.call(1, "prepare_renewal_brief", account, bearer("read"))
	records(t, response)
	if used := m.gateway.Windows()[receipts.Fingerprint(bearer("read"))].Used; used != 5 || len(labels(m, outer)) != 4 {
		t.Fatalf("a brief of four reads was charged %d times", used)
	}
	if limited := m.post(callBody(2, "get_account", account), requestHeaders("tools/call", "get_account", bearer("read"))); limited.Code != 429 {
		t.Fatalf("a sixth attempt in a window of five: %d %s", limited.Code, limited.Body)
	}

	// A window of four fills at the fourth read.
	m = mount(t, cards.Selection{"acc-003": true}, func(c *mcptools.Config) { c.RateLimit = 4 })
	m.fixture()
	response, outer = m.call(1, "prepare_renewal_brief", account, bearer("read"))
	if got := labels(m, outer); toolError(t, response) != "rate_limited" || outer.label() != "prepare_renewal_brief error 200" ||
		!reflect.DeepEqual(got, []string{"get_account ok 200", "list_opportunities ok 200", "read_call_notes ok 200", "read_slack_messages error 429"}) ||
		slices.Contains(m.book.made(), "read_slack_messages") || m.count("SELECT count(*) FROM cards") != 0 {
		t.Fatalf("a brief past its rate window: %d %s, reads %v, book %v", response.Code, response.Body, got, m.book.made())
	}

	// The bearer expires while the opportunities are read: the next read is refused, and so is the brief.
	m = mount(t, cards.Selection{"acc-003": true}, nil)
	m.fixture()
	expires := m.expiry(bearer("read"))
	m.book.before = func(tool string) {
		if tool == "list_opportunities" {
			m.wall = func() float64 { return expires }
		}
	}
	response, outer = m.call(1, "prepare_renewal_brief", account, bearer("read"))
	if got := labels(m, outer); toolError(t, response) != "invalid_token" || outer.label() != "prepare_renewal_brief error 200" ||
		!reflect.DeepEqual(got, []string{"get_account ok 200", "list_opportunities ok 200", "read_call_notes error 401"}) ||
		!reflect.DeepEqual(m.book.made(), []string{"get_account", "list_opportunities"}) || m.count("SELECT count(*) FROM cards") != 0 {
		t.Fatalf("a brief past its bearer's expiry: %d %s, reads %v, book %v", response.Code, response.Body, got, m.book.made())
	}

	// The bearer expires after the last read, before the brief is saved: no brief is saved or released.
	m = mount(t, cards.Selection{"acc-003": true}, nil)
	m.fixture()
	expires = m.expiry(bearer("read"))
	m.book.before = func(tool string) {
		if tool == "read_slack_messages" {
			m.wall = func() float64 { return expires }
		}
	}
	response, outer = m.call(1, "prepare_renewal_brief", account, bearer("read"))
	if toolError(t, response) != "invalid_token" || outer.label() != "prepare_renewal_brief error 200" || len(labels(m, outer)) != 4 ||
		m.count("SELECT count(*) FROM cards") != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
		t.Fatalf("a brief whose bearer expired before it was saved: %d %s", response.Code, response.Body)
	}
}
