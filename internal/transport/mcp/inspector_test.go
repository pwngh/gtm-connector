package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestInspector runs scripts/check-inspector.py's sequence against the Go gateway: the pinned Inspector CLI,
// a real TLS listener, a rate limit of 2, and a fresh grant for each connect. It needs Node 22 and npx and
// may download the Inspector, so it runs only when GTM_INSPECTOR=1.
func TestInspector(t *testing.T) {
	if os.Getenv("GTM_INSPECTOR") != "1" {
		t.Skip("set GTM_INSPECTOR=1 to run the pinned Inspector CLI")
	}
	const inspector = "2.15.0"
	scopes := map[string]any{"search_accounts": "crm:read", "get_account": "crm:read", "list_opportunities": "crm:read",
		"read_call_notes": "calls:read", "log_activity": "crm:write", "read_slack_messages": "slack:read"}
	directory := t.TempDir()

	type exchange struct {
		httpMethod, path, protocolHeader, methodHeader, method, protocolMeta string
		status                                                               int
	}
	var (
		mu      sync.Mutex
		traffic []exchange
	)
	receipts := &MemoryRecorder{}
	fx := newFixture(t, receipts)
	tokens := map[string]*Token{}
	server := httptest.NewUnstartedServer(nil)
	server.StartTLS()
	defer server.Close()
	resource := server.URL + "/mcp"
	gateway := mustGateway(t, GatewayConfig{Resource: resource, Issuer: "https://127.0.0.1:1", RateLimit: 2, WindowSeconds: 60,
		Verify: func(raw string) *Token {
			mu.Lock()
			defer mu.Unlock()
			if token := tokens[raw]; token != nil {
				copied := *token
				return &copied
			}
			return nil
		}, Receipts: receipts, Run: fx.Run})
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var payload struct {
			Method string `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		json.Unmarshal(body, &payload)
		recorder := httptest.NewRecorder()
		gateway.ServeHTTP(recorder, r)
		version, _ := payload.Params.Meta["io.modelcontextprotocol/protocolVersion"].(string)
		mu.Lock()
		traffic = append(traffic, exchange{r.Method, r.URL.Path, r.Header.Get("Mcp-Protocol-Version"), r.Header.Get("Mcp-Method"),
			payload.Method, version, recorder.Code})
		mu.Unlock()
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		w.Write(recorder.Body.Bytes())
	})
	certificate := filepath.Join(directory, "tls.pem")
	os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600)

	environment := append(os.Environ(), "MCP_STORAGE_DIR="+filepath.Join(directory, "storage"),
		"MCP_CLIENT_CONFIG_PATH="+filepath.Join(directory, "client.json"),
		"MCP_INSPECTOR_OAUTH_STATE_PATH="+filepath.Join(directory, "oauth.json"),
		"MCP_INSPECTOR_SECRET_STORE=memory", "MCP_AUTO_OPEN_ENABLED=false", "NODE_TLS_REJECT_UNAUTHORIZED=1",
		"NODE_EXTRA_CA_CERTS="+certificate, "NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1")
	invoke := func(timeout time.Duration, arguments ...string) (int, string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		command := exec.CommandContext(ctx, "npx", append([]string{"--yes", "@modelcontextprotocol/inspector@" + inspector, "--cli"}, arguments...)...)
		command.Env, command.Stdin = environment, nil
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if ctx.Err() != nil {
			t.Fatalf("Inspector timed out after %s", timeout)
		}
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String(), stderr.String()
	}
	if code, out, _ := invoke(240*time.Second, "--version"); code != 0 || strings.TrimSpace(out) != inspector {
		t.Fatalf("Inspector version: %d %q", code, out)
	}

	config := filepath.Join(directory, "inspector.json")
	grants := 0
	connect := func(granted ...string) {
		grants++
		raw := fmt.Sprintf("fixture-inspector-grant-%02d", grants)
		expires := float64(time.Now().Unix() + 300)
		mu.Lock()
		tokens[raw] = &Token{Scopes: granted, ExpiresAt: &expires, Resource: resource}
		mu.Unlock()
		writeConfig(t, config, resource, "Bearer "+raw)
	}
	base := []string{"--config", config, "--server", "gtm-connector", "--format", "json", "--stored-auth-only", "--quiet"}
	run := func(method, tool string, status int) map[string]any {
		t.Helper()
		mu.Lock()
		start := len(traffic)
		mu.Unlock()
		arguments := append(append([]string{}, base...), "--method", method)
		if tool != "" {
			arguments = append(arguments, "--tool-name", tool, "--tool-arg", "account_id=acc-003")
		}
		code, stdout, stderr := invoke(60*time.Second, arguments...)
		mu.Lock()
		for raw := range tokens {
			if strings.Contains(stdout+stderr, raw) {
				t.Error("a credential reached the CLI output")
			}
		}
		observed := traffic[start:]
		mu.Unlock()
		if len(observed) == 0 || observed[len(observed)-1].status != status {
			t.Fatalf("%s %s: unexpected HTTP outcome %+v (exit %d, stderr %.300s)", method, tool, observed, code, stderr)
		}
		if status == 200 {
			if code != 0 || stderr != "" {
				t.Fatalf("%s %s failed: %d %.300s", method, tool, code, stderr)
			}
			return path(decode(t, []byte(stdout)), "result").(map[string]any)
		}
		if code == 0 || stdout != "" {
			t.Fatalf("denied %s returned data: %q", tool, stdout)
		}
		failure := decode(t, []byte(stderr))
		if len(failure) != 1 || path(failure, "error", "message") == nil {
			t.Fatalf("missing CLI error envelope: %s", stderr)
		}
		return failure
	}
	message := func(failure map[string]any) string { return fmt.Sprint(path(failure, "error", "message")) }

	connect("crm:read")
	discovery := run("initialize", "", 200)
	if discovery["protocolVersion"] != ProtocolVersion || path(discovery, "capabilities", "tools") == nil {
		t.Fatalf("discovery %v", discovery)
	}
	mu.Lock()
	last := traffic[len(traffic)-1]
	mu.Unlock()
	if last.method != "server/discover" {
		t.Fatalf("legacy initialization on the wire: %+v", last)
	}
	listed := run("tools/list", "", 200)["tools"].([]any)
	declared := map[string]any{}
	for _, tool := range listed {
		m := tool.(map[string]any)
		declared[m["name"].(string)] = path(m, "_meta", "gtm.demo/requiredScope")
		for _, field := range []string{"inputSchema", "outputSchema"} {
			if path(m, field, "type") != "object" {
				t.Fatalf("%s %s %v", m["name"], field, m[field])
			}
		}
	}
	if !reflect.DeepEqual(declared, scopes) {
		t.Fatalf("tools %v", declared)
	}
	account := run("tools/call", "get_account", 200)
	if !strings.Contains(message(run("tools/call", "read_call_notes", 403)), "calls:read") {
		t.Fatal("calls denial")
	}
	run("tools/call", "get_account", 429)
	connect("calls:read")
	notes := run("tools/call", "read_call_notes", 200)
	if !strings.Contains(message(run("tools/call", "get_account", 403)), "crm:read") {
		t.Fatal("crm denial")
	}
	connect("crm:read")
	if !strings.Contains(message(run("tools/call", "read_slack_messages", 403)), "slack:read") {
		t.Fatal("slack denial")
	}
	connect("calls:read")
	if !strings.Contains(message(run("tools/call", "read_slack_messages", 403)), "slack:read") {
		t.Fatal("slack denial")
	}
	connect("slack:read")
	messages := run("tools/call", "read_slack_messages", 200)
	if !strings.Contains(message(run("tools/call", "get_account", 403)), "crm:read") {
		t.Fatal("crm denial")
	}
	connect("slack:read")
	if !strings.Contains(message(run("tools/call", "read_call_notes", 403)), "calls:read") {
		t.Fatal("calls denial")
	}
	for _, c := range []struct {
		result map[string]any
		source string
		refs   []string
	}{
		{account, "crm", []string{"crm/accounts/acc-003", "crm/contacts/con-003"}},
		{notes, "calls", []string{"calls/call_notes/note-003", "calls/call_notes/note-003b"}},
		{messages, "slack", []string{"slack/messages/msg-003"}},
	} {
		data := c.result["structuredContent"].(map[string]any)
		if c.result["isError"] != false || data["source"] != c.source || data["untrusted_data"] != true ||
			!reflect.DeepEqual(refs(data["records"].([]any)), c.refs) {
			t.Fatalf("%s result %v", c.source, c.result)
		}
	}
	writeConfig(t, config, resource, "Bearer invalid-fixture-token")
	run("initialize", "", 401)

	rows := receipts.Read()
	var statuses, counts []int
	ids := map[string]bool{}
	var ok []string
	for _, row := range rows {
		statuses, counts, ids[row.AuditID] = append(statuses, *row.HTTPStatus), append(counts, row.RowCount), true
		if row.Status == "pending" {
			t.Fatalf("pending receipt %+v", row)
		}
		if row.Status == "ok" {
			ok = append(ok, row.AuditID)
		}
	}
	if len(rows) != 10 || len(ids) != 10 ||
		!reflect.DeepEqual(statuses, []int{200, 403, 429, 200, 403, 403, 403, 200, 403, 403}) ||
		!reflect.DeepEqual(counts, []int{2, 0, 0, 2, 0, 0, 0, 1, 0, 0}) {
		t.Fatalf("receipts: %d %v %v", len(rows), statuses, counts)
	}
	released := []string{}
	for _, result := range []map[string]any{account, notes, messages} {
		released = append(released, path(result, "_meta", "gtm.demo/auditId").(string))
	}
	if !reflect.DeepEqual(ok, released) {
		t.Fatalf("released %v, ok receipts %v", released, ok)
	}
	calls := 0
	for _, e := range traffic {
		if e.httpMethod != http.MethodPost || e.path != "/mcp" {
			continue
		}
		if e.method == "tools/call" {
			calls++
		}
		if e.protocolHeader != ProtocolVersion || e.protocolMeta != ProtocolVersion || e.methodHeader != e.method {
			t.Fatalf("protocol metadata mismatch: %+v", e)
		}
	}
	if calls != len(rows) || fx.activityCount() != 0 {
		t.Fatalf("%d calls on the wire, %d receipts", calls, len(rows))
	}
}

func writeConfig(t *testing.T, path, url, authorization string) {
	t.Helper()
	data, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"gtm-connector": map[string]any{
		"url": url, "headers": map[string]string{"Authorization": authorization},
		"type": "streamable-http", "protocolEra": "modern"}}}, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
