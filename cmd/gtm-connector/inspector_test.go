package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
)

// TestInspector runs the pinned Inspector CLI against the binary, as a person sets it up: setup, demo, a deal
// with its card, and token new, then serve, with the Inspector calling /mcp over HTTPS with each token, and
// then starting mcp stdio itself. It checks discovery, the seven tools and their schemas, a read by each of
// the five read tools and the two deal tools over HTTPS and by get_account, read_call_notes,
// read_slack_messages and read_deal_card over stdio, each marked untrusted data, the six cross-source
// denials, an unknown bearer, one finished receipt for each tool call, and that no token reaches the
// Inspector's output, the audit log or serve's log. It needs Node 22, npx and PostgreSQL, and may
// download the Inspector, so it runs only when GTM_INSPECTOR=1.
func TestInspector(t *testing.T) {
	if os.Getenv("GTM_INSPECTOR") != "1" {
		t.Skip("set GTM_INSPECTOR=1 to run the pinned Inspector CLI")
	}
	const inspector = "2.15.0"
	binary := buildBinary(t)
	ownerURL := pgtest.URL(t)
	suffix := make([]byte, 6)
	rand.Read(suffix)
	role := "gtm_app_" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), ownerURL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		conn.Exec(context.Background(), "DROP OWNED BY "+role)
		conn.Exec(context.Background(), "DROP ROLE "+role)
	})
	app, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	app.User = url.UserPassword(role, "app-password-for-the-inspector")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.Addr().String()
	probe.Close()
	credentials := filepath.Join(t.TempDir(), "credentials")
	host := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GTM_OWNER_DATABASE_URL=" + ownerURL,
		"GTM_DATABASE_URL=" + app.String(), "GTM_CREDENTIALS_DIR=" + credentials, "GTM_LISTEN=" + listen}
	gtm := func(args ...string) (string, string) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Env, command.Dir = host, t.TempDir()
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("gtm-connector %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	gtm("setup")
	gtm("demo")
	// A deal of acc-003, and its card, which reads all three sources.
	var deal struct{ ID string }
	created, _ := gtm("deal", "new", "acc-003", "--json")
	if err := json.Unmarshal([]byte(created), &deal); err != nil || deal.ID == "" {
		t.Fatalf("deal new: %v %s", err, created)
	}
	gtm("brief", "acc-003")
	issued := map[string]string{} // each token, by its scopes
	issue := func(scopes ...string) string {
		var made issuedToken
		arguments := []string{"token", "new", "--json"}
		for _, scope := range scopes {
			arguments = append(arguments, "--scope", scope)
		}
		out, _ := gtm(arguments...)
		if err := json.Unmarshal([]byte(out), &made); err != nil || made.Token == "" {
			t.Fatalf("token new: %v", err)
		}
		issued[strings.Join(scopes, " ")] = made.Token
		return made.Token
	}
	for _, scope := range []string{"crm:read", "calls:read", "slack:read"} {
		issue(scope)
	}
	issue("crm:read", "calls:read", "slack:read")
	serve := exec.Command(binary, "serve")
	serve.Env, serve.Dir = host, t.TempDir()
	serveLog := &lockedBuffer{}
	serve.Stdout, serve.Stderr = serveLog, serveLog
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		serve.Process.Signal(syscall.SIGTERM)
		serve.Wait()
	})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		check := exec.Command(binary, "health")
		check.Env = host
		if check.Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not answer: %s", serveLog.String())
		}
	}

	directory := t.TempDir()
	environment := append(os.Environ(), "MCP_STORAGE_DIR="+filepath.Join(directory, "storage"),
		"MCP_CLIENT_CONFIG_PATH="+filepath.Join(directory, "client.json"),
		"MCP_INSPECTOR_OAUTH_STATE_PATH="+filepath.Join(directory, "oauth.json"),
		"MCP_INSPECTOR_SECRET_STORE=memory", "MCP_AUTO_OPEN_ENABLED=false", "NODE_TLS_REJECT_UNAUTHORIZED=1",
		"NODE_EXTRA_CA_CERTS="+filepath.Join(credentials, tlsCertFile), "NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1")
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
		var exited *exec.ExitError
		code := 0
		if errors.As(err, &exited) {
			code = exited.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String(), stderr.String()
	}
	if code, out, _ := invoke(240*time.Second, "--version"); code != 0 || strings.TrimSpace(out) != inspector {
		t.Fatalf("Inspector version: %d %q", code, out)
	}

	config := filepath.Join(directory, "inspector.json")
	server := func(entry map[string]any) {
		entry["protocolEra"] = "modern"
		data, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"gtm-connector": entry}}, "", "  ")
		if err := os.WriteFile(config, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	overHTTPS := func(token string) {
		server(map[string]any{"type": "streamable-http", "url": "https://" + listen + "/mcp",
			"headers": map[string]string{"Authorization": "Bearer " + token}})
	}
	overStdio := func(scopes ...string) {
		args := []string{"mcp", "stdio"}
		for _, scope := range scopes {
			args = append(args, "--scope", scope)
		}
		server(map[string]any{"type": "stdio", "command": binary, "args": args,
			"env": map[string]string{"GTM_DATABASE_URL": app.String(), "GTM_CREDENTIALS_DIR": credentials}})
	}
	// run calls one method. A call that is refused exits non-zero with an error envelope on stderr and
	// nothing on stdout.
	run := func(method, tool string, ok bool) map[string]any {
		t.Helper()
		arguments := []string{"--config", config, "--server", "gtm-connector", "--format", "json", "--stored-auth-only",
			"--quiet", "--method", method}
		if tool != "" {
			arguments = append(arguments, "--tool-name", tool)
			switch tool {
			case "search_accounts":
				arguments = append(arguments, "--tool-arg", "query=Thornfield")
			case "read_deal_card":
				arguments = append(arguments, "--tool-arg", "deal_id="+deal.ID)
			case "list_deals":
			default:
				arguments = append(arguments, "--tool-arg", "account_id=acc-003")
			}
		}
		code, stdout, stderr := invoke(60*time.Second, arguments...)
		for _, token := range issued {
			if strings.Contains(stdout+stderr, token) {
				t.Fatal("a token reached the Inspector's output")
			}
		}
		var document map[string]any
		if ok {
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &document) != nil {
				t.Fatalf("%s %s failed: %d %.300s %.300s", method, tool, code, stdout, stderr)
			}
			return dig(document, "result").(map[string]any)
		}
		if code == 0 || stdout != "" || json.Unmarshal([]byte(stderr), &document) != nil || len(document) != 1 ||
			dig(document, "error", "message") == nil {
			t.Fatalf("%s %s was not refused: %d %.300s %.300s", method, tool, code, stdout, stderr)
		}
		return document
	}
	message := func(failure map[string]any) string { return fmt.Sprint(dig(failure, "error", "message")) }
	scopes := map[string]any{"search_accounts": "crm:read", "get_account": "crm:read", "list_opportunities": "crm:read",
		"read_call_notes": "calls:read", "read_slack_messages": "slack:read", "list_deals": "crm:read",
		"read_deal_card": "crm:read"}
	discover := func() {
		t.Helper()
		if discovery := run("initialize", "", true); discovery["protocolVersion"] != "2026-07-28" || dig(discovery, "capabilities", "tools") == nil {
			t.Fatalf("discovery %v", discovery)
		}
		declared := map[string]any{}
		for _, tool := range run("tools/list", "", true)["tools"].([]any) {
			declared[dig(tool, "name").(string)] = dig(tool, "_meta", "gtm.demo/requiredScope")
			for _, field := range []string{"inputSchema", "outputSchema"} {
				if dig(tool, field, "type") != "object" {
					t.Fatalf("%v %s", dig(tool, "name"), field)
				}
			}
		}
		if !reflect.DeepEqual(declared, scopes) {
			t.Fatalf("tools %v", declared)
		}
	}
	denied := func(tool, scope string) {
		t.Helper()
		if !strings.Contains(message(run("tools/call", tool, false)), scope) {
			t.Fatalf("%s was not refused for %s", tool, scope)
		}
	}

	// Over HTTPS, with the tokens token new printed.
	overHTTPS(issued["crm:read"])
	discover()
	account := run("tools/call", "get_account", true)
	search := run("tools/call", "search_accounts", true)
	opportunities := run("tools/call", "list_opportunities", true)
	denied("read_call_notes", "calls:read")
	overHTTPS(issued["calls:read"])
	notes := run("tools/call", "read_call_notes", true)
	denied("get_account", "crm:read")
	overHTTPS(issued["crm:read"])
	denied("read_slack_messages", "slack:read")
	overHTTPS(issued["calls:read"])
	denied("read_slack_messages", "slack:read")
	overHTTPS(issued["slack:read"])
	messages := run("tools/call", "read_slack_messages", true)
	denied("get_account", "crm:read")
	denied("read_call_notes", "calls:read")
	// The deal tools: the deals a crm:read token may read, and the deal's card for a token whose scopes cover
	// every source the card read.
	overHTTPS(issued["crm:read"])
	deals := run("tools/call", "list_deals", true)
	overHTTPS(issued["crm:read calls:read slack:read"])
	card := run("tools/call", "read_deal_card", true)
	// An unknown bearer is refused for its authentication, HTTP 401, not for any other reason.
	overHTTPS("invalid-fixture-token")
	if refused := message(run("initialize", "", false)); !strings.Contains(refused, "requires authorization (HTTP 401)") {
		t.Fatalf("an unknown bearer was not refused as unauthorized: %s", refused)
	}

	// Over stdio, the Inspector starts mcp stdio for each call.
	overStdio("crm:read")
	discover()
	stdioAccount := run("tools/call", "get_account", true)
	denied("read_call_notes", "calls:read")
	overStdio("calls:read", "slack:read")
	stdioNotes := run("tools/call", "read_call_notes", true)
	stdioMessages := run("tools/call", "read_slack_messages", true)
	denied("get_account", "crm:read")
	overStdio("crm:read", "calls:read", "slack:read")
	stdioCard := run("tools/call", "read_deal_card", true)

	for _, c := range []struct {
		result map[string]any
		source string
		refs   []string
	}{
		{account, "crm", []string{"crm/accounts/acc-003", "crm/contacts/con-003"}},
		{search, "crm", []string{"crm/accounts/acc-003"}},
		{opportunities, "crm", []string{"crm/opportunities/opp-003"}},
		{notes, "calls", []string{"calls/call_notes/note-003", "calls/call_notes/note-003b"}},
		{messages, "slack", []string{"slack/messages/msg-003"}},
		{stdioAccount, "crm", []string{"crm/accounts/acc-003", "crm/contacts/con-003"}},
		{stdioNotes, "calls", []string{"calls/call_notes/note-003", "calls/call_notes/note-003b"}},
		{stdioMessages, "slack", []string{"slack/messages/msg-003"}},
		{deals, "deals", []string{"deals/" + deal.ID}},
		{card, "deals", []string{"deals/" + deal.ID, cardRef(card)}},
		{stdioCard, "deals", []string{"deals/" + deal.ID, cardRef(card)}},
	} {
		data, _ := c.result["structuredContent"].(map[string]any)
		var refs []string
		records, _ := data["records"].([]any)
		for _, record := range records {
			refs = append(refs, fmt.Sprint(dig(record, "record_ref")))
		}
		if c.result["isError"] != false || data["source"] != c.source || data["untrusted_data"] != true || !reflect.DeepEqual(refs, c.refs) {
			t.Fatalf("%s result %v", c.source, c.result)
		}
	}

	// The deal's card over both transports: its sealed text, its lines as data, and its seal.
	text := fmt.Sprint(dig(card, "structuredContent", "card", "text"))
	lines, _ := dig(card, "structuredContent", "card", "lines").([]any)
	if !strings.HasPrefix(text, "DEAL CARD: ") || len(lines) == 0 || !strings.HasPrefix(cardRef(card), "briefs/") ||
		!reflect.DeepEqual(dig(card, "structuredContent", "card"), dig(stdioCard, "structuredContent", "card")) {
		t.Fatalf("the deal's card %v", dig(card, "structuredContent", "card"))
	}

	// Each stdio process revoked its own token when it ended; the four printed ones are live.
	list, _ := gtm("token", "list", "--json")
	var tokens []issuedToken
	json.Unmarshal([]byte(list), &tokens)
	states, callers := map[string]int{}, map[string]bool{}
	for _, token := range tokens {
		states[token.State]++
		callers[token.Caller] = true
	}
	if !reflect.DeepEqual(states, map[string]int{"live": 4, "revoked": 8}) {
		t.Fatalf("tokens %v", states)
	}

	// One finished receipt for each tool call, and the released results are the ok receipts. The demo's own
	// receipts name no client token.
	out, _ := gtm("audit", "--json")
	var receipts []receiptRow
	if err := json.Unmarshal([]byte(out), &receipts); err != nil {
		t.Fatal(err)
	}
	var statuses, counts []int
	var ok []string
	for _, row := range receipts {
		if !callers[row.TokenID] {
			continue
		}
		if row.Status == "pending" || row.HTTPStatus == nil {
			t.Fatalf("an unfinished receipt %+v", row)
		}
		statuses, counts = append(statuses, *row.HTTPStatus), append(counts, len(row.Rows))
		if row.Status == "ok" {
			ok = append(ok, row.AuditID)
		}
	}
	if want := []int{200, 200, 200, 403, 200, 403, 403, 403, 200, 403, 403, 200, 200, 200, 403, 200, 200, 403, 200}; !reflect.DeepEqual(statuses, want) ||
		!reflect.DeepEqual(counts, []int{2, 1, 1, 0, 2, 0, 0, 0, 1, 0, 0, 1, 2, 2, 0, 2, 1, 0, 2}) {
		t.Fatalf("receipts: %v %v", statuses, counts)
	}
	var released []string
	for _, result := range []map[string]any{account, search, opportunities, notes, messages, deals, card, stdioAccount,
		stdioNotes, stdioMessages, stdioCard} {
		released = append(released, fmt.Sprint(dig(result, "_meta", "gtm.demo/auditId")))
	}
	if !reflect.DeepEqual(ok, released) {
		t.Fatalf("released %v, ok receipts %v", released, ok)
	}
	for _, text := range []string{out, list, serveLog.String()} {
		for _, token := range issued {
			if strings.Contains(text, token) {
				t.Fatal("a token was printed again")
			}
		}
	}
}

// cardRef is the record reference of the card a read_deal_card result released: its second record.
func cardRef(result map[string]any) string {
	records, _ := dig(result, "structuredContent", "records").([]any)
	if len(records) != 2 {
		return ""
	}
	return fmt.Sprint(dig(records[1], "record_ref"))
}
