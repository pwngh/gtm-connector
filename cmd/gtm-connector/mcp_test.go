package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// lockedBuffer is a log sink the server and the test use at once.
type lockedBuffer struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

const mcpMeta = `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

// served is serve as it serves: the surface with MCP mounted, connected as the app role, on a TLS listener
// with the install's certificate, and a client that calls its tools.
type served struct {
	address string
	server  *http.Server
	logs    *lockedBuffer
	key     []byte
	send    func(host, method, name, token string, params map[string]any) (int, map[string]any)
	call    func(host, tool, token string, arguments map[string]any) (int, map[string]any)
	result  func(document map[string]any) map[string]any
	reason  func(document map[string]any) any
}

// startServe starts what serve itself builds, until the test ends. It fails the test when serve cannot
// start.
func startServe(t *testing.T, d *demoDatabase, selection cards.Selection) *served {
	t.Helper()
	store, err := openAppStore(ctx, config{databaseURL: d.appURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	creds, err := openCredentials(privateDir(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts(nil)); err != nil {
		t.Fatal(err)
	}
	pair, key, err := serving(creds)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	surface, err := newSurface(ctx, store, config{publicOrigin: "https://" + address, allowedHosts: []string{address}}, key,
		selection, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { surface.Close() })
	logs := &lockedBuffer{}
	server := newServer(surface, pair, slog.New(slog.NewTextHandler(logs, nil)))
	go server.ServeTLS(listener, "", "")
	t.Cleanup(func() { server.Close() })

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	s := &served{address: address, server: server, logs: logs, key: key}
	s.send = func(host, method, name, token string, params map[string]any) (int, map[string]any) {
		t.Helper()
		params["_meta"] = json.RawMessage(mcpMeta)
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		request, err := http.NewRequest(http.MethodPost, "https://"+address+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		for header, value := range map[string]string{"Content-Type": "application/json", "Accept": "application/json",
			"Mcp-Protocol-Version": "2026-07-28", "Mcp-Method": method, "Mcp-Name": name, "Authorization": "Bearer " + token} {
			if value != "" {
				request.Header.Set(header, value)
			}
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if strings.Contains(string(raw)+fmt.Sprint(response.Header), token) {
			t.Errorf("%s %s answered with the bearer", method, name)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s %s: %d %q", method, name, response.StatusCode, raw)
		}
		return response.StatusCode, document
	}
	s.call = func(host, tool, token string, arguments map[string]any) (int, map[string]any) {
		return s.send(host, "tools/call", tool, token, map[string]any{"name": tool, "arguments": arguments})
	}
	s.result = func(document map[string]any) map[string]any {
		found, _ := document["result"].(map[string]any)
		return found
	}
	s.reason = func(document map[string]any) any {
		failure, _ := document["error"].(map[string]any)
		data, _ := failure["data"].(map[string]any)
		return data["reason"]
	}
	return s
}

// machineBearer issues a bearer for CRM and calls to a machine principal granted acc-003, as an operator's
// command will issue one.
func machineBearer(t *testing.T, d *demoDatabase, bearer string) string {
	t.Helper()
	machine, err := d.owner.CreatePrincipal(ctx, d.workspace, "machine", "Test assistant")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, machine.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(bearer))
	if _, err := conn(t, d.ownerURL).Exec(ctx, `INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes,
		expires_at) VALUES ($1, $2, $3, ARRAY['crm:read', 'calls:read'], now() + interval '5 minutes')`,
		d.workspace.ID, machine.ID, digest[:]); err != nil {
		t.Fatal(err)
	}
	return bearer
}

// What serve itself builds, served as serve serves it: the surface with MCP mounted, connected as the app
// role, on a TLS listener with the install's certificate. An assistant with a machine token lists the tools,
// reads its own source and no other, and prepares a brief, each call under one receipt; a bearer the store
// does not know and a request for another Host are refused; and no bearer reaches the server's log.
func TestServeAnswersMCPOverTLS(t *testing.T) {
	d := setUpDemo(t)
	file := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(file, []byte(`{"schema_version": 1, "accounts": [{"account_id": "acc-003", "slack": true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	selection, err := loadSelection(file)
	if err != nil {
		t.Fatal(err)
	}
	s := startServe(t, d, selection)
	address, server, logs, key, send, call, result, reason := s.address, s.server, s.logs, s.key, s.send, s.call, s.result, s.reason
	bearer, unknown := machineBearer(t, d, "serve-test-bearer-0000000000000000000000000000"), "serve-test-unknown-bearer-000000000000000000"
	account := map[string]any{"account_id": "acc-003"}

	status, listing := send(address, "tools/list", "", bearer, map[string]any{})
	if tools, _ := result(listing)["tools"].([]any); status != 200 || len(tools) != 7 {
		t.Fatalf("tools/list: %d %v", status, listing)
	}
	status, read := call(address, "get_account", bearer, account)
	if structured, _ := result(read)["structuredContent"].(map[string]any); status != 200 || structured["source"] != "crm" ||
		len(structured["records"].([]any)) != 2 {
		t.Fatalf("get_account: %d %v", status, read)
	}
	if status, denied := call(address, "read_slack_messages", bearer, account); status != 403 || reason(denied) != "insufficient_scope" {
		t.Fatalf("read_slack_messages without slack:read: %d %v", status, denied)
	}
	status, prepared := call(address, "prepare_renewal_brief", bearer, account)
	brief, _ := result(prepared)["structuredContent"].(map[string]any)
	if briefed, _ := brief["brief"].(map[string]any); status != 200 || result(prepared)["isError"] != false ||
		briefed["notes_status"] != "available" || briefed["slack_status"] != "denied" {
		t.Fatalf("prepare_renewal_brief: %d %v", status, prepared)
	}
	if status, refused := call(address, "get_account", unknown, account); status != 401 || reason(refused) != "invalid_token" {
		t.Fatalf("an unknown bearer: %d %v", status, refused)
	}
	if status, refused := call("evil.example", "get_account", bearer, account); status != 403 || reason(refused) != "untrusted_origin_or_host" {
		t.Fatalf("another Host: %d %v", status, refused)
	}

	// Five calls, five outer receipts, each finished, and one receipt for each read inside the brief.
	for query, want := range map[string]int{
		"SELECT count(*) FROM receipts WHERE parent_id IS NULL":                                                5,
		"SELECT count(*) FROM receipts WHERE parent_id IS NOT NULL":                                            4,
		"SELECT count(*) FROM receipts WHERE status = 'pending'":                                               0,
		"SELECT count(*) FROM receipts WHERE tool = 'prepare_renewal_brief'":                                   1,
		"SELECT count(*) FROM receipts WHERE parent_id IS NOT NULL AND status = 'error' AND http_status = 403": 1,
		"SELECT count(*) FROM cards":                                                                           1,
	} {
		if got := d.count(t, query); got != want {
			t.Errorf("%s: %d, want %d", query, got, want)
		}
	}

	// A bearer sent to the port without TLS fails the handshake, which the server logs.
	plain, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(plain, "POST /mcp HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", address, bearer)
	io.Copy(io.Discard, plain)
	plain.Close()
	server.Close()
	deadline := time.Now().Add(5 * time.Second)
	for logs.String() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	logged := logs.String()
	if logged == "" {
		t.Fatal("the server logged nothing")
	}
	for _, secret := range []string{bearer, unknown, hexDigest(bearer), string(key)} {
		if strings.Contains(logged, secret) {
			t.Errorf("the server's log holds a credential: %.200s", logged)
		}
	}
}

func hexDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum)
}

// The selection file serve reads: one regular file its reader owns and nobody else can write, never a
// symbolic link, at most 64 KiB, in the documented shape.
func TestLoadSelection(t *testing.T) {
	dir := t.TempDir()
	const document = `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}, {"account_id": "acc-012", "slack": false}]}`
	write := func(name, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := write("selection.json", document, 0o600)
	selection, err := loadSelection(good)
	if err != nil || len(selection) != 2 || !selection["acc-007"] || selection["acc-012"] {
		t.Fatalf("a selection file: %v (%v)", selection, err)
	}
	if readable, err := loadSelection(write("readable.json", document, 0o644)); err != nil || len(readable) != 2 {
		t.Fatalf("a file others can read: %v (%v)", readable, err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	const unsafe = "The selection file must be a regular file you own that others cannot write."
	for label, c := range map[string]struct{ path, refusal string }{
		"a missing file":          {filepath.Join(dir, "none.json"), "The selection file was not found."},
		"a symbolic link":         {link, "The selection file cannot be opened. It must be a regular file, not a symbolic link."},
		"a folder":                {dir, unsafe},
		"a file its group writes": {write("group.json", document, 0o620), unsafe},
		"a file anyone writes":    {write("world.json", document, 0o602), unsafe},
		"a file past 64 KiB":      {write("large.json", document+strings.Repeat(" ", maxSelectionBytes), 0o600), "The selection file is larger than 65536 bytes."},
		"a file in another shape": {write("shape.json", `{"schema_version": 2, "accounts": []}`, 0o600), "The selection file has an unsupported shape."},
	} {
		selection, err := loadSelection(c.path)
		if err == nil || !strings.HasPrefix(err.Error(), c.refusal) || selection != nil {
			t.Errorf("%s: %v (%v)", label, selection, err)
		}
	}
}

// serve's command line. No --selection serves no selection. A --selection with an empty name stops serve
// before it opens anything: it is never read as "no selection", which would serve every account. So does a
// file name given without the flag, and a file that does not load.
func TestServeSelectionFlag(t *testing.T) {
	file := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(file, []byte(`{"schema_version": 1, "accounts": [{"account_id": "acc-003", "slack": true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if selection, err := serveSelection(nil); err != nil || selection != nil {
		t.Fatalf("no flag: %v (%v)", selection, err)
	}
	for _, args := range [][]string{{"--selection", file}, {"--selection=" + file}, {"-selection", file}} {
		if selection, err := serveSelection(args); err != nil || len(selection) != 1 || !selection["acc-003"] {
			t.Fatalf("%v: %v (%v)", args, selection, err)
		}
	}
	const empty = `--selection needs the name of a selection file; an empty name is not "no selection"`
	for label, c := range map[string]struct {
		args    []string
		refusal string
	}{
		"an empty name":            {[]string{"--selection", ""}, empty},
		"an empty name after =":    {[]string{"--selection="}, empty},
		"the flag alone":           {[]string{"--selection"}, "flag needs an argument"},
		"a file without the flag":  {[]string{file}, "serve takes no argument but --selection FILE"},
		"a file after an empty":    {[]string{"--selection=", file}, "serve takes no argument but --selection FILE"},
		"a file that is not there": {[]string{"--selection", file + ".missing"}, "The selection file was not found."},
	} {
		selection, err := serveSelection(c.args)
		if err == nil || !strings.Contains(err.Error(), c.refusal) || selection != nil {
			t.Errorf("%s: %v (%v)", label, selection, err)
		}
		// serve itself stops there, before it reads its settings or opens the database.
		t.Setenv("GTM_LISTEN", "not an address")
		if err := serve(ctx, c.args); err == nil || !strings.Contains(err.Error(), c.refusal) {
			t.Errorf("%s: serve answered %v", label, err)
		}
	}
	// The help says when the file is read.
	flags, _ := serveFlags()
	if usage := flags.Lookup("selection").Usage; !strings.Contains(usage, "read once, at start") || !strings.Contains(usage, "restart serve") {
		t.Errorf("the flag's help: %s", usage)
	}
}

// failingFile is a selection file whose read fails.
type failingFile struct{}

func (failingFile) Read([]byte) (int, error) { return 0, errors.New("input/output error on a device") }

// A selection file that cannot be read says so. Only one that is too large is told it is too large.
func TestSelectionReadErrors(t *testing.T) {
	if _, err := readSelection(failingFile{}); err == nil || err.Error() != "The selection file could not be read." {
		t.Fatalf("a read that fails: %v", err)
	}
	if _, err := readSelection(strings.NewReader(strings.Repeat(" ", maxSelectionBytes+1))); err == nil ||
		err.Error() != "The selection file is larger than 65536 bytes." {
		t.Fatalf("a file past the limit: %v", err)
	}
	if selection, err := readSelection(strings.NewReader(`{"schema_version": 1, "accounts": [{"account_id": "acc-003", "slack": false}]}`)); err != nil || len(selection) != 1 {
		t.Fatalf("a file that reads: %v (%v)", selection, err)
	}
}

// When PostgreSQL refuses what setting up the MCP endpoint writes, serve says so in one fixed line with the
// SQLSTATE and nothing of PostgreSQL's own text, which names the table and can name a role or a host.
func TestMCPStartupErrorsAreFixedLines(t *testing.T) {
	d := setUpDemo(t)
	app, err := url.Parse(d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, "REVOKE INSERT, UPDATE ON ALL TABLES IN SCHEMA public FROM "+app.User.Username()); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = mcpGateway(ctx, store, "https://127.0.0.1:8443", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || err.Error() != "the MCP endpoint could not be set up: PostgreSQL error 42501" {
		t.Fatalf("a role that may not write: %v", err)
	}
	_, err = newSurface(ctx, store, config{publicOrigin: "https://127.0.0.1:8443", allowedHosts: []string{"127.0.0.1:8443"}},
		bytes.Repeat([]byte("k"), 32), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, leaked := range []string{"permission denied", "connections", "workspaces", app.User.Username(), "SQLSTATE"} {
		if err == nil || strings.Contains(err.Error(), leaked) {
			t.Errorf("serve's own error holds %q: %v", leaked, err)
		}
	}
	if got := mountError(errors.New("dial tcp 10.0.0.7:5432: connection refused")).Error(); got != "the MCP endpoint could not be set up" {
		t.Errorf("an error that is not PostgreSQL's: %s", got)
	}
}

// serve's MCP tools read what the workspace is connected to when each call starts. A source removed while
// the server runs is refused at once, under the call's receipt, which names no connection; connected again,
// it is read again. A server started with no CRM serves the browser surface and refuses every tool by name.
func TestServeFollowsWhatTheWorkspaceIsConnectedTo(t *testing.T) {
	d := setUpDemo(t)
	s := startServe(t, d, nil)
	bearer := machineBearer(t, d, "serve-test-bearer-1111111111111111111111111111")
	account := map[string]any{"account_id": "acc-003"}
	read := func(s *served) (records int, refusal string) {
		t.Helper()
		status, document := s.call(s.address, "get_account", bearer, account)
		result := s.result(document)
		if status != 200 || result == nil {
			t.Fatalf("get_account: %d %v", status, document)
		}
		if result["isError"] == true {
			content, _ := result["content"].([]any)
			text, _ := content[0].(map[string]any)["text"].(string)
			return 0, text
		}
		structured, _ := result["structuredContent"].(map[string]any)
		found, _ := structured["records"].([]any)
		return len(found), ""
	}
	last := func() string {
		t.Helper()
		var status string
		var named bool
		if err := conn(t, d.ownerURL).QueryRow(ctx, `SELECT status, connection_id IS NOT NULL FROM receipts
			WHERE tool = 'get_account' AND parent_id IS NULL ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&status, &named); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s %v", status, named)
	}
	const notConnected = "Error executing tool get_account: crm_not_connected"

	if records, refusal := read(s); records != 2 || refusal != "" || last() != "ok true" {
		t.Fatalf("before the removal: %d records, %q, receipt %s", records, refusal, last())
	}
	if code, _, stderr := d.read(t, "connect", "demo", "--remove"); code != 0 {
		t.Fatalf("connect demo --remove: exit %d\n%s", code, stderr)
	}
	if records, refusal := read(s); records != 0 || refusal != notConnected || last() != "error false" {
		t.Errorf("after the removal: %d records, %q, receipt %s", records, refusal, last())
	}
	if code, _, stderr := d.read(t, "connect", "demo"); code != 0 {
		t.Fatalf("connect demo: exit %d\n%s", code, stderr)
	}
	if records, refusal := read(s); records != 2 || refusal != "" || last() != "ok true" {
		t.Errorf("connected again: %d records, %q, receipt %s", records, refusal, last())
	}

	// A restart with no CRM: the whole surface starts, the MCP endpoint with it.
	if code, _, stderr := d.read(t, "connect", "demo", "--remove"); code != 0 {
		t.Fatalf("connect demo --remove: exit %d\n%s", code, stderr)
	}
	restarted := startServe(t, d, nil)
	if records, refusal := read(restarted); records != 0 || refusal != notConnected || last() != "error false" {
		t.Errorf("after a restart with no CRM: %d records, %q, receipt %s", records, refusal, last())
	}
	if status, _ := restarted.send(restarted.address, "tools/list", "", bearer, map[string]any{}); status != 200 {
		t.Errorf("tools/list with no CRM: %d", status)
	}
}

// When serve cannot read which workspace a bearer was issued in, no gate can keep the call's receipt, and none
// is kept: this is the one refusal of a call with no receipt. The call is answered as a gate answers a call
// whose receipt could not be committed, 503 audit_unavailable in JSON, and serve logs one fixed line that
// holds no bearer and no text of PostgreSQL's.
func TestABearerWhoseWorkspaceCannotBeReadIsRefusedInTheGatesWords(t *testing.T) {
	d := setUpDemo(t)
	role := d.appRole(t)
	store, err := postgres.Open(ctx, d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	logs := &lockedBuffer{}
	handler, err := mcpGateway(ctx, store, "https://127.0.0.1:8443", nil, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn(t, d.ownerURL).Exec(ctx, "REVOKE SELECT ON machine_tokens FROM "+role); err != nil {
		t.Fatal(err)
	}
	const bearer = "invented-bearer-whose-workspace-cannot-be-read"
	request, err := http.NewRequest(http.MethodPost, "https://127.0.0.1:8443/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-003"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	answer := httptest.NewRecorder()
	handler.ServeHTTP(answer, request)
	if answer.Code != http.StatusServiceUnavailable || answer.Header().Get("Content-Type") != "application/json" ||
		answer.Body.String() != `{"error":"audit_unavailable; call was not executed"}` {
		t.Errorf("the answer: %d %s %s", answer.Code, answer.Header().Get("Content-Type"), answer.Body.String())
	}
	logged := logs.String()
	if strings.Count(logged, "\n") != 1 || !strings.Contains(logged, `level=WARN msg="an MCP request was refused with `+
		`no receipt: the workspace of its bearer could not be read" error="PostgreSQL error 42501"`) {
		t.Errorf("the log:\n%s", logged)
	}
	for _, leaked := range []string{bearer, "permission denied", role} {
		if strings.Contains(logged, leaked) || strings.Contains(answer.Body.String(), leaked) {
			t.Errorf("the log or the answer holds %q", leaked)
		}
	}
	if n := d.count(t, "SELECT count(*) FROM receipts"); n != 0 {
		t.Errorf("%d receipts were kept", n)
	}
}
