package mcp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// These tests send requests to the gate where serve mounts it: at /mcp behind the browser surface's handler,
// with its receipts, its tokens and its tools on PostgreSQL and the demo book as its source. Every request
// goes through that handler, never to the gate alone.

const (
	mountedOrigin = "https://127.0.0.1:8765"
	mountedHost   = "127.0.0.1:8765"
	requestMeta   = `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
)

var ctx = context.Background()

// mounted is one database with a demo workspace, a machine principal, and the surface serving both.
type mounted struct {
	t         *testing.T
	url       string
	store     *postgres.Store
	workspace postgres.Workspace
	machine   postgres.Principal
	surface   http.Handler
	gateway   *mcp.Gateway
	book      *watched
	selection cards.Selection // what the surface serves now
	wall      func() float64  // nil reads the real clock
	said      []string        // every status line, header and body the surface answered
}

// watched is the demo book. It notes every read made of it, and each read made while no committed pending
// receipt named its tool. before, when set, runs as a read starts.
type watched struct {
	*demo.Store
	url    string
	before func(tool string)

	mu          sync.Mutex
	reads       []string
	unreceipted []string
}

func (w *watched) note(tool string) {
	// The receipt is looked for first. A caller who leaves in before has its read's receipt finished at
	// once, while the read it no longer waits for is still returning.
	w.receipted(tool)
	if w.before != nil {
		w.before(tool)
	}
}

// receipted keeps the read, and whether its pending receipt is committed as it starts.
func (w *watched) receipted(tool string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads = append(w.reads, tool)
	// Another connection sees only what is committed.
	conn, err := pgx.Connect(ctx, w.url)
	if err != nil {
		w.unreceipted = append(w.unreceipted, tool+": "+err.Error())
		return
	}
	defer conn.Close(ctx)
	var pending int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM receipts WHERE tool = $1 AND status = 'pending'", tool).
		Scan(&pending); err != nil || pending != 1 {
		w.unreceipted = append(w.unreceipted, fmt.Sprintf("%s: %d pending receipts (%v)", tool, pending, err))
	}
}

func (w *watched) Read(ctx context.Context, tool, account string) (string, []*strictjson.Object, error) {
	w.note(tool)
	return w.Store.Read(ctx, tool, account)
}

func (w *watched) Search(_ context.Context, query string, number *string, accounts []string) ([]*strictjson.Object, bool, error) {
	if number != nil {
		return nil, false, mcp.ToolError("account_number_requires_salesforce")
	}
	w.note("search_accounts")
	records, more := w.Store.Search(query, accounts)
	return records, more, nil
}

func (w *watched) Opportunities(_ context.Context, accounts []string) ([]*strictjson.Object, error) {
	w.note("list_opportunities")
	return w.Store.Opportunities(accounts), nil
}

func (w *watched) made() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.reads...)
}

var demoSources = evidence.Sources{CRMBackend: "sqlite", CRMBinding: evidence.FixtureCRMBinding(),
	CallsBackend: "fixture", SlackBackend: "fixture"}

// mount builds a fresh database and serves it. change adjusts the tools' configuration before they are built.
func mount(t *testing.T, selection cards.Selection, change func(*mcptools.Config)) *mounted {
	t.Helper()
	m := &mounted{t: t, url: pgtest.URL(t)}
	var err error
	if m.store, err = postgres.Open(ctx, m.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.store.Close)
	must(t, m.store.Migrate(ctx))
	m.workspace, err = m.store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	must(t, err)
	m.machine, err = m.store.CreatePrincipal(ctx, m.workspace, "machine", "Test assistant")
	must(t, err)
	book, err := demo.Seeded()
	must(t, err)
	m.book = &watched{Store: book, url: m.url}
	for _, account := range book.Accounts() {
		must(t, m.store.EnsureAccount(ctx, m.workspace.ID, account.ID, account.Name))
		must(t, m.store.GrantAccount(ctx, m.workspace.ID, m.machine.ID, account.ID))
	}
	m.serve(selection, change)
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		if pending := m.count("SELECT count(*) FROM receipts WHERE status = 'pending'"); pending != 0 {
			t.Errorf("%d receipts were left pending", pending)
		}
		if len(m.book.unreceipted) != 0 {
			t.Errorf("sources were read without a committed pending receipt: %v", m.book.unreceipted)
		}
	})
	return m
}

// serve builds the tools and the surface over the database as it stands, as serve does at start.
func (m *mounted) serve(selection cards.Selection, change func(*mcptools.Config)) {
	m.t.Helper()
	m.selection = selection
	gateway, err := mcptools.New(m.tools(change))
	must(m.t, err)
	surface, err := httptransport.New(httptransport.Config{PublicOrigin: mountedOrigin,
		AllowedHosts: []string{mountedHost, "localhost:8765"}, SessionKey: bytes.Repeat([]byte{7}, 32), MCP: gateway},
		m.store, authz.Evidence{Store: m.store})
	must(m.t, err)
	m.t.Cleanup(func() { surface.Close() })
	m.surface, m.gateway = surface, gateway
}

// tools is the configuration the tools are built from: the database as it stands and the selection served.
func (m *mounted) tools(change func(*mcptools.Config)) mcptools.Config {
	m.t.Helper()
	connections := map[string]string{}
	for _, source := range []string{"crm", "calls", "slack"} {
		connection, err := m.store.EnsureConnection(ctx, m.workspace, source, "demo")
		must(m.t, err)
		connections[source] = connection.ID
	}
	config := mcptools.Config{Resource: mountedOrigin + "/mcp", Issuer: mountedOrigin, WorkspaceID: m.workspace.ID,
		Store: m.store, Receipts: &postgres.ReceiptStore{Store: m.store}, Source: m.book, Sources: demoSources,
		Connections: connections, Selection: m.selection, RateLimit: 1000, WindowSeconds: 60,
		Wall: func() float64 {
			if m.wall != nil {
				return m.wall()
			}
			return float64(time.Now().UnixMicro()) / 1e6
		}}
	if change != nil {
		change(&config)
	}
	return config
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// sql runs one statement on a connection of the test's own.
func (m *mounted) sql(statement string, args ...any) error {
	conn, err := pgx.Connect(ctx, m.url)
	must(m.t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, statement, args...)
	return err
}

func (m *mounted) count(query string, args ...any) int {
	m.t.Helper()
	conn, err := pgx.Connect(ctx, m.url)
	must(m.t, err)
	defer conn.Close(ctx)
	var n int
	must(m.t, conn.QueryRow(ctx, query, args...).Scan(&n))
	return n
}

// dump is one text value read from the database.
func (m *mounted) dump(query string) string {
	m.t.Helper()
	conn, err := pgx.Connect(ctx, m.url)
	must(m.t, err)
	defer conn.Close(ctx)
	var text string
	must(m.t, conn.QueryRow(ctx, query).Scan(&text))
	return text
}

// expiry is when a stored bearer expires, in seconds since the epoch, as the verifier reads it.
func (m *mounted) expiry(raw string) float64 {
	m.t.Helper()
	digest := sha256.Sum256([]byte(raw))
	micros := m.count("SELECT (extract(epoch FROM expires_at) * 1000000)::bigint FROM machine_tokens WHERE token_sha256 = $1", digest[:])
	return float64(micros) / 1e6
}

// issue stores a bearer of the machine principal, which is granted every account, that expires in an hour.
func (m *mounted) issue(raw string, scopes ...string) { m.issueFor(m.machine, raw, scopes...) }

// issueFor stores a bearer of a principal that expires in an hour.
func (m *mounted) issueFor(principal postgres.Principal, raw string, scopes ...string) {
	m.t.Helper()
	digest := sha256.Sum256([]byte(raw))
	must(m.t, m.sql(`INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, expires_at)
		VALUES ($1, $2, $3, $4, now() + interval '1 hour')`, m.workspace.ID, principal.ID, digest[:], scopes))
}

// grantee is a new machine principal granted exactly these accounts.
func (m *mounted) grantee(accounts ...string) postgres.Principal {
	m.t.Helper()
	principal, err := m.store.CreatePrincipal(ctx, m.workspace, "machine", "Narrow assistant")
	must(m.t, err)
	for _, account := range accounts {
		must(m.t, m.store.GrantAccount(ctx, m.workspace.ID, principal.ID, account))
	}
	return principal
}

// rescope gives a stored bearer other scopes, as an operator who narrows a grant does.
func (m *mounted) rescope(raw string, scopes ...string) {
	m.t.Helper()
	digest := sha256.Sum256([]byte(raw))
	must(m.t, m.sql("UPDATE machine_tokens SET scopes = $2 WHERE token_sha256 = $1", digest[:], scopes))
}

// fixture stores the gate fixture's bearers that the store can hold: each read grant, by its fixture value.
func (m *mounted) fixture() {
	m.t.Helper()
	scopes := mcp.FixtureScopes()
	for _, name := range []string{"crm", "calls", "slack", "read"} {
		m.issue(mcp.FixtureBearer(name), scopes[name]...)
	}
}

// receipt is one row of the receipts table, as the tests read it.
type receipt struct {
	ID, Tool, Status, Arguments, RequestID, TokenID string
	HTTP                                            int
	Rows                                            []string
	Principal, Parent, Brief, Error, Scope          *string
}

func (r receipt) label() string { return fmt.Sprintf("%s %s %d", r.Tool, r.Status, r.HTTP) }

// receipts are the rows that match where, oldest first.
func (m *mounted) receipts(where string, args ...any) []receipt {
	m.t.Helper()
	conn, err := pgx.Connect(ctx, m.url)
	must(m.t, err)
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT id::text, tool, status, arguments::text, request_id::text, token_id,
		coalesce(http_status, 0), ARRAY(SELECT jsonb_array_elements_text(rows)), principal_id::text, parent_id::text,
		brief_id, error, scope FROM receipts WHERE `+where+` ORDER BY started_at, ordinal NULLS FIRST`, args...)
	must(m.t, err)
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (receipt, error) {
		var r receipt
		err := row.Scan(&r.ID, &r.Tool, &r.Status, &r.Arguments, &r.RequestID, &r.TokenID, &r.HTTP, &r.Rows,
			&r.Principal, &r.Parent, &r.Brief, &r.Error, &r.Scope)
		return r, err
	})
	must(m.t, err)
	return found
}

// outer are the receipts of the calls themselves: every receipt that is no read inside another call.
func (m *mounted) outer() []receipt { return m.receipts("parent_id IS NULL") }

func (m *mounted) outerLabels() []string {
	var labels []string
	for _, r := range m.outer() {
		labels = append(labels, r.label())
	}
	return labels
}

// send gives the surface one request as the wire carries it: an origin-form target, a Host line and TLS.
func (m *mounted) send(method, target, host string, body []byte, headers [][2]string) *httptest.ResponseRecorder {
	return m.sendFrom(ctx, method, target, host, bytes.NewReader(body), headers)
}

// sendFrom is send for a caller who may leave, which ends its context, and whose body may fail as it is read.
func (m *mounted) sendFrom(caller context.Context, method, target, host string, body io.Reader, headers [][2]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body).WithContext(caller)
	request.Host = host
	request.TLS = &tls.ConnectionState{}
	for _, header := range headers {
		request.Header.Add(header[0], header[1])
	}
	recorder := httptest.NewRecorder()
	m.surface.ServeHTTP(recorder, request)
	m.said = append(m.said, fmt.Sprintf("%d %v %s", recorder.Code, recorder.Header(), recorder.Body))
	return recorder
}

func (m *mounted) post(body []byte, headers [][2]string) *httptest.ResponseRecorder {
	return m.send(http.MethodPost, "/mcp", mountedHost, body, headers)
}

func requestHeaders(method, name, bearer string) [][2]string {
	headers := [][2]string{{"Content-Type", "application/json"}, {"Accept", "application/json, text/event-stream"},
		{"Mcp-Protocol-Version", mcp.FixtureVersion}, {"Mcp-Method", method}}
	if name != "" {
		headers = append(headers, [2]string{"Mcp-Name", name})
	}
	if bearer != "" {
		headers = append(headers, [2]string{"Authorization", "Bearer " + bearer})
	}
	return headers
}

func callBody(id any, tool string, arguments any) []byte {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments, "_meta": json.RawMessage(requestMeta)}})
	return body
}

// call makes one tool call and checks that it left exactly one outer receipt, finished.
func (m *mounted) call(id any, tool string, arguments any, bearer string, extra ...[2]string) (*httptest.ResponseRecorder, receipt) {
	m.t.Helper()
	before := len(m.outer())
	response := m.post(callBody(id, tool, arguments), append(requestHeaders("tools/call", tool, bearer), extra...))
	rows := m.outer()[before:]
	if len(rows) != 1 || rows[0].Status == "pending" || rows[0].Tool != tool || rows[0].HTTP != response.Code {
		m.t.Fatalf("%s left %d outer receipts %+v for the answer %d %s", tool, len(rows), rows, response.Code, response.Body)
	}
	return response, rows[0]
}

func (m *mounted) list(bearer string) *httptest.ResponseRecorder {
	return m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":`+requestMeta+`}}`),
		requestHeaders("tools/list", "", bearer))
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

func at(value any, keys ...string) any {
	for _, key := range keys {
		object, _ := value.(map[string]any)
		value = object[key]
	}
	return value
}

// refusal is a gateway refusal's reason, or "" for any other answer.
func refusal(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	if response.Body.Len() == 0 {
		return ""
	}
	reason, _ := at(decode(t, response.Body.Bytes()), "error", "data", "reason").(string)
	return reason
}

// records are the record references a released result holds, in order. An answer that released nothing
// fails the test.
func records(t *testing.T, response *httptest.ResponseRecorder) []string {
	t.Helper()
	result := at(decode(t, response.Body.Bytes()), "result")
	list, isList := at(result, "structuredContent", "records").([]any)
	if response.Code != 200 || at(result, "isError") != false || !isList {
		t.Fatalf("nothing was released: %d %s", response.Code, response.Body)
	}
	refs := []string{}
	for _, record := range list {
		refs = append(refs, at(record, "record_ref").(string))
	}
	return refs
}

// toolError is the code of a tool error, "" for any other answer.
func toolError(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	result := at(decode(t, response.Body.Bytes()), "result")
	if response.Code != 200 || at(result, "isError") != true {
		return ""
	}
	content, _ := at(result, "content").([]any)
	if len(content) != 1 {
		return ""
	}
	text, _ := at(content[0], "text").(string)
	_, code, _ := strings.Cut(text, ": ")
	return code
}
