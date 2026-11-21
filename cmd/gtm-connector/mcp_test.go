package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// What serve itself builds, served as serve serves it: the surface with MCP mounted, connected as the app
// role, on a TLS listener with the install's certificate. An assistant with a machine token lists the tools,
// reads its own source and no other, and prepares a brief, each call under one receipt; a bearer the store
// does not know and a request for another Host are refused; and no bearer reaches the server's log.
func TestServeAnswersMCPOverTLS(t *testing.T) {
	d := setUpDemo(t)
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
	file := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(file, []byte(`{"schema_version": 1, "accounts": [{"account_id": "acc-003", "slack": true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	selection, err := loadSelection(file)
	if err != nil {
		t.Fatal(err)
	}
	surface, err := newSurface(ctx, store, config{publicOrigin: "https://" + address, allowedHosts: []string{address}}, key, selection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { surface.Close() })
	logs := &lockedBuffer{}
	server := newServer(surface, pair, slog.New(slog.NewTextHandler(logs, nil)))
	go server.ServeTLS(listener, "", "")
	t.Cleanup(func() { server.Close() })

	// A machine principal with a token for CRM and calls, as an operator's command will issue one.
	machine, err := d.owner.CreatePrincipal(ctx, d.workspace, "machine", "Test assistant")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, machine.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	const bearer, unknown = "serve-test-bearer-0000000000000000000000000000", "serve-test-unknown-bearer-000000000000000000"
	digest := sha256.Sum256([]byte(bearer))
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, `INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, expires_at)
		VALUES ($1, $2, $3, ARRAY['crm:read', 'calls:read'], now() + interval '5 minutes')`, d.workspace.ID, machine.ID, digest[:]); err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	send := func(host, method, name, token string, params map[string]any) (int, map[string]any) {
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
	call := func(host, tool, token string, arguments map[string]any) (int, map[string]any) {
		return send(host, "tools/call", tool, token, map[string]any{"name": tool, "arguments": arguments})
	}
	result := func(document map[string]any) map[string]any {
		found, _ := document["result"].(map[string]any)
		return found
	}
	reason := func(document map[string]any) any {
		failure, _ := document["error"].(map[string]any)
		data, _ := failure["data"].(map[string]any)
		return data["reason"]
	}
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
