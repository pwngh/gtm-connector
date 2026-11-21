package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// The recorded help of mcp, with each line the binary says otherwise and why. The rest is compared as
// recorded: TestRecordedCommandSession compares "mcp help" and "help mcp" exactly against the recording with
// these lines changed, and each recorded line must be found once.
var mcpHelpChanges = []struct{ recorded, now, why string }{
	{"                         --scope {crm:read,calls:read,crm:write,slack:read}\n",
		"                         --scope {crm:read,calls:read,slack:read}\n                         [--selection SELECTION]\n",
		"the connector is read-only, and the store holds no token that carries crm:write"},
	{"Run the local stdio MCP bridge that an assistant app starts.\n",
		"Run the local stdio MCP server that an assistant app starts.\n",
		"the binary serves the tools itself; it forwards nothing to another process"},
	{"                        Use this private state folder (default: var/v2).\n",
		"                        Read this workspace (default: demo).\n",
		"--state-dir names a workspace in PostgreSQL, as it does for the read commands"},
	{"  --scope {crm:read,calls:read,crm:write,slack:read}\n                        Grant this scope. Repeat for each explicit grant.\n",
		"  --scope {crm:read,calls:read,slack:read}\n                        Grant this scope. Repeat for each explicit grant.\n" +
			"  --selection SELECTION\n                        Serve only the accounts and Slack threads in this private selection file,\n" +
			"                        and add the two brief tools.\n",
		"the selection the bridge took from the debug serve it attached to is given to the command itself"},
	{"It attaches to the connector that gtm-connector debug serve runs in the same --state-dir, so start that first.\n",
		"It reads the workspace from PostgreSQL at GTM_DATABASE_URL itself, so no server needs to run.\n",
		"there is no debug serve to attach to: the command reads the database itself"},
}

// recordedHelp is how the replay compares a help step it compares exactly: the recorded text with its listed
// changes made.
var recordedHelp = map[string]func(t *testing.T, recorded string) string{"mcp help": mcpHelpNow, "help mcp": mcpHelpNow}

func mcpHelpNow(t *testing.T, recorded string) string {
	t.Helper()
	for _, change := range mcpHelpChanges {
		if strings.Count(recorded, change.recorded) != 1 {
			t.Fatalf("the recorded help does not hold this line once: %q", change.recorded)
		}
		recorded = strings.Replace(recorded, change.recorded, change.now, 1)
	}
	return recorded
}

// The recorded mcp help, changed as listed, is the command's help.
func TestMCPHelpIsTheRecordedHelp(t *testing.T) {
	for _, s := range recordedSession(t) {
		if change := recordedHelp[s.Label]; change != nil && change(t, s.Stdout) != mcpHelp {
			t.Errorf("%s differs from the recording, changed as listed", s.Label)
		}
	}
}

// The command line is read before anything else, with argparse's messages, and refused with exit 2.
func TestMCPArgumentsNeedNoDatabase(t *testing.T) {
	t.Setenv("GTM_DATABASE_URL", "")
	for _, c := range []struct {
		args    []string
		message string
	}{
		{nil, "the following arguments are required: action, --scope"},
		{[]string{"stdio"}, "the following arguments are required: --scope"},
		{[]string{"--scope", "crm:read"}, "the following arguments are required: action"},
		{[]string{"serve", "--scope", "crm:read"}, "argument action: invalid choice (choose from 'stdio')"},
		{[]string{"stdio", "--scope", "crm:write"}, "argument --scope: invalid choice (choose from 'crm:read', 'calls:read', 'slack:read')"},
		{[]string{"stdio", "--scope", "crm:read", "extra"}, "unrecognized arguments"},
		{[]string{"stdio", "--s", "crm:read"}, "ambiguous option, which could match --state-dir, --scope, --selection"},
		{[]string{"stdio", "--scope", "crm:read", "--selection", ""}, "--selection needs the name of a selection file; an empty name is not \"no selection\""},
		{[]string{"stdio", "--scope", "crm:read", "--selection", filepath.Join(t.TempDir(), "none.json")}, "The selection file was not found."},
	} {
		var out, errs bytes.Buffer
		code := mcpCommand(ctx, c.args, screen{out: &out, err: &errs, json: true}, strings.NewReader(""))
		want := fmt.Sprintf(`{"error": {"code": "usage_error", "message": %q, "next": "gtm-connector mcp --help"}}`+"\n", c.message)
		if code != 2 || out.Len() != 0 || errs.String() != want {
			t.Errorf("mcp %v: exit %d\n%s%s", c.args, code, out.String(), errs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := mcpCommand(ctx, []string{"stdio", "--help"}, screen{out: &out, err: &errs}, nil); code != 0 ||
		out.String() != mcpHelp || errs.Len() != 0 {
		t.Errorf("mcp stdio --help: exit %d\n%s", code, errs.String())
	}
	// Each --scope adds one; the token carries each once, in a fixed order.
	args, err := mcpArguments.parse([]string{"stdio", "--scope", "slack:read", "--scope=crm:read", "--scope", "slack:read"})
	if err != nil || !reflect.DeepEqual(scopeSet(args.lists["--scope"]), []string{"crm:read", "slack:read"}) {
		t.Errorf("scopes %v %v", args.lists, err)
	}
}

// stdioProcess is the binary serving mcp stdio, as an assistant app starts it: its stdin, every line of its
// stdout, and its stderr.
type stdioProcess struct {
	t      *testing.T
	in     io.WriteCloser
	lines  chan []byte
	stderr *lockedBuffer
	done   chan struct{} // closed when the process has exited
	err    error         // how it exited
	held   map[string]map[string]any
}

// buildBinary builds the command into a test's own folder.
func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "gtm-connector")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Env = append(build.Environ(), "GOTOOLCHAIN=local")
	if log, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, log)
	}
	return binary
}

// startStdio runs "gtm-connector mcp stdio" with these arguments, connecting as the app role.
func startStdio(t *testing.T, d *demoDatabase, args ...string) *stdioProcess {
	t.Helper()
	return startStdioWith(t, []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GTM_DATABASE_URL=" + d.appURL}, args...)
}

// startStdioWith runs it with exactly this environment.
func startStdioWith(t *testing.T, env []string, args ...string) *stdioProcess {
	t.Helper()
	command := exec.Command(buildBinary(t), append([]string{"mcp", "stdio"}, args...)...)
	command.Env = env
	command.Dir = t.TempDir()
	p := &stdioProcess{t: t, lines: make(chan []byte, 256), stderr: &lockedBuffer{}, done: make(chan struct{}),
		held: map[string]map[string]any{}}
	var err error
	if p.in, err = command.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = p.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 8<<20)
		for scanner.Scan() {
			p.lines <- append([]byte{}, scanner.Bytes()...)
		}
		close(p.lines)
		p.err = command.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.in.Close()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			command.Process.Kill()
		}
	})
	return p
}

func (p *stdioProcess) send(messages ...string) {
	p.t.Helper()
	for _, m := range messages {
		if _, err := io.WriteString(p.in, m+"\n"); err != nil {
			p.t.Fatal(err)
		}
	}
}

// answer is the reply to one request id. Replies may come in any order; others are kept for later.
func (p *stdioProcess) answer(id int) map[string]any {
	p.t.Helper()
	key := fmt.Sprint(id)
	for {
		if reply, ok := p.held[key]; ok {
			delete(p.held, key)
			return reply
		}
		select {
		case line, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("stdout ended before the answer to %d; stderr: %s", id, p.stderr.String())
			}
			var reply map[string]any
			if err := json.Unmarshal(line, &reply); err != nil {
				p.t.Fatalf("stdout holds a line that is not JSON: %q", line)
			}
			p.held[fmt.Sprint(reply["id"])] = reply
		case <-time.After(20 * time.Second):
			p.t.Fatalf("no answer to %d; stderr: %s", id, p.stderr.String())
		}
	}
}

// stop closes stdin and waits for the process, which must exit 0 with nothing more on stdout or stderr.
func (p *stdioProcess) stop() {
	p.t.Helper()
	p.in.Close()
	for line := range p.lines {
		p.t.Errorf("more on stdout after the last answer: %s", line)
	}
	select {
	case <-p.done:
		if p.err != nil {
			p.t.Fatalf("mcp stdio: %v; stderr: %s", p.err, p.stderr.String())
		}
	case <-time.After(10 * time.Second):
		p.t.Fatal("mcp stdio did not stop at the end of its input")
	}
	if p.stderr.String() != "" {
		p.t.Errorf("stderr: %s", p.stderr.String())
	}
}

func dig(value any, keys ...string) any {
	for _, key := range keys {
		object, _ := value.(map[string]any)
		value = object[key]
	}
	return value
}

func reasonOf(reply map[string]any) any { return dig(reply, "error", "data", "reason") }

const (
	stdioStart = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	stdioReady = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	stdioMeta  = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
)

func stdioCall(id int, name, arguments string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, arguments)
}

// stdioReceipts are the workspace's outer receipts, in the order they started.
func stdioReceipts(t *testing.T, d *demoDatabase) []postgres.Receipt {
	t.Helper()
	all, err := d.owner.Receipts(ctx, d.workspace.ID, postgres.ReceiptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(all, func(r postgres.Receipt) bool { return r.ParentID != "" })
}

// outcomes are receipts as "tool status http".
func outcomes(receipts []postgres.Receipt) []string {
	var got []string
	for _, r := range receipts {
		status := 0
		if r.HTTPStatus != nil {
			status = *r.HTTPStatus
		}
		got = append(got, fmt.Sprintf("%s %s %d", r.Tool, r.Status, status))
	}
	return got
}

// stdioDemo is a database as setup and demo leave it: the demo workspace connected to the demo data.
func stdioDemo(t *testing.T) *demoDatabase {
	t.Helper()
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	return d
}

// The tools the recorded bridge listed, without the write tool the connector no longer has
// ("test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens: tools/list" and
// "test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input: tools/list").
var recordedStdioTools = []string{"get_account", "list_opportunities", "read_call_notes", "read_slack_messages", "search_accounts"}

func listedTools(t *testing.T, listing map[string]any) []string {
	t.Helper()
	tools, ok := dig(listing, "result", "tools").([]any)
	if !ok {
		t.Fatalf("no tools in %v", listing)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, dig(tool, "name").(string))
	}
	slices.Sort(names)
	return names
}

// test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens, through the command on PostgreSQL:
// a crm:read process lists the recorded tools, reads CRM, and is refused call notes, each call under one
// receipt of the one token the process holds, which belongs to the workspace's MCP clients principal and is
// revoked when the process ends.
func TestStdioCommandCRMOnlyReadsCRMAndNeverWidens(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		stdioCall(3, "get_account", `{"account_id":"acc-007"}`), stdioCall(4, "read_call_notes", `{"account_id":"acc-007"}`))
	p.answer(1)
	if names := listedTools(t, p.answer(2)); !reflect.DeepEqual(names, recordedStdioTools) {
		t.Fatalf("tools %v", names)
	}
	account := p.answer(3)
	if dig(account, "result", "isError") != false || !strings.Contains(fmt.Sprint(dig(account, "result", "structuredContent")), "crm/accounts/acc-007") {
		t.Fatalf("account %v", account)
	}
	denied := p.answer(4)
	if reasonOf(denied) != "insufficient_scope" || fmt.Sprint(dig(denied, "error", "code")) != "1004" {
		t.Fatalf("denied %v", denied)
	}
	p.stop()
	receipts := stdioReceipts(t, d)
	if got := outcomes(receipts); !reflect.DeepEqual(got, []string{"get_account ok 200", "read_call_notes error 403"}) ||
		receipts[0].TokenID != receipts[1].TokenID {
		t.Fatalf("receipts %v", got)
	}
	tokens, err := d.owner.MachineTokens(ctx, d.workspace.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens %v %v", tokens, err)
	}
	token := tokens[0]
	if token.Fingerprint != receipts[0].TokenID || token.RevokedAt == nil || !reflect.DeepEqual(token.Scopes, []string{"crm:read"}) ||
		receipts[0].PrincipalID != token.PrincipalID {
		t.Fatalf("the process's token %+v", token)
	}
	if got := d.count(t, `SELECT count(*) FROM principals WHERE id = $1 AND kind = 'machine' AND display_name = 'MCP clients'`,
		token.PrincipalID); got != 1 {
		t.Fatal("the token is not the MCP clients principal's")
	}
}

// test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input, through the command.
func TestStdioCommandStdoutCarriesOnlyProtocolAndStopsAtEndOfInput(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	first, second := p.answer(1), p.answer(2)
	if first["jsonrpc"] != "2.0" || second["jsonrpc"] != "2.0" || !reflect.DeepEqual(listedTools(t, second), recordedStdioTools) {
		t.Fatalf("%v %v", first, second)
	}
	p.stop()
}

// The Go counterpart of test_a_refused_grant_is_renewed_once_for_the_same_scopes, through the command: once
// the process's token has expired, a call and a listing are refused, the call with a receipt, and nothing is
// retried with another token.
func TestStdioCommandExpiredTokenIsRefusedNotRetried(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady, stdioCall(2, "get_account", `{"account_id":"acc-007"}`))
	p.answer(1)
	p.answer(2)
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, `UPDATE machine_tokens SET created_at = now() - interval '1 hour',
		expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	p.send(stdioCall(3, "get_account", `{"account_id":"acc-007"}`), `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if refused, listing := p.answer(3), p.answer(4); reasonOf(refused) != "invalid_token" || reasonOf(listing) != "invalid_token" {
		t.Fatalf("refused %v listing %v", refused, listing)
	}
	p.stop()
	if got := outcomes(stdioReceipts(t, d)); !reflect.DeepEqual(got, []string{"get_account ok 200", "get_account error 401"}) {
		t.Fatalf("receipts %v", got)
	}
	if got := d.count(t, `SELECT count(*) FROM machine_tokens`); got != 1 {
		t.Fatalf("%d tokens: an expired one was replaced", got)
	}
	// A token that has expired is never renewed.
	tokens, _ := d.owner.MachineTokens(ctx, d.workspace.ID)
	if renewed, err := d.owner.RenewMachineToken(ctx, d.workspace.ID, tokens[0].ID, time.Hour); renewed || err != nil {
		t.Fatalf("an expired token was renewed: %v", err)
	}
}

// test_a_selected_server_prepares_a_brief_through_the_bridge, through the command with --selection: the two
// brief tools are listed and no write is, a write is refused, and an account outside the selection is refused
// before any read.
func TestStdioCommandWithASelection(t *testing.T) {
	d := stdioDemo(t)
	file := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(file, []byte(`{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startStdio(t, d, "--scope", "crm:read", "--scope", "calls:read", "--scope", "slack:read", "--selection", file)
	p.send(stdioStart, stdioReady, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		stdioCall(3, "log_activity", `{"account_id":"acc-007","kind":"note","body":"x","idempotency_key":"k"}`),
		stdioCall(4, "get_account", `{"account_id":"acc-003"}`),
		stdioCall(5, "prepare_renewal_brief", `{"account_id":"acc-007"}`))
	p.answer(1)
	if names := listedTools(t, p.answer(2)); !slices.Contains(names, "prepare_renewal_brief") ||
		!slices.Contains(names, "read_renewal_brief") || slices.Contains(names, "log_activity") {
		t.Fatalf("tools %v", names)
	}
	if write := p.answer(3); reasonOf(write) != "crm_write_disabled" {
		t.Fatalf("write %v", write)
	}
	if outside := p.answer(4); dig(outside, "result", "isError") != true ||
		!strings.Contains(fmt.Sprint(dig(outside, "result", "content")), "account_not_selected") {
		t.Fatalf("an account outside the selection %v", outside)
	}
	if prepared := p.answer(5); dig(prepared, "result", "isError") != false {
		t.Fatalf("prepare_renewal_brief %v", prepared)
	}
	p.stop()
	if got := outcomes(stdioReceipts(t, d)); !reflect.DeepEqual(got, []string{"log_activity error 403",
		"get_account error 200", "prepare_renewal_brief ok 200"}) {
		t.Fatalf("receipts %v", got)
	}
	if got := d.count(t, `SELECT count(*) FROM cards`); got != 1 {
		t.Fatalf("%d cards", got)
	}
}

// test_a_call_the_bridge_itself_refuses_still_leaves_a_receipt, through the command.
func TestStdioCommandMalformedCallsStillLeaveReceipts(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_account","arguments":["acc-001"]}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":7,"arguments":{}}}`,
		stdioCall(4, "get_accøunt", `{"account_id":"acc-001"}`))
	p.answer(1)
	for id := 2; id <= 4; id++ {
		if answer := p.answer(id); answer["error"] == nil && dig(answer, "result", "isError") != true {
			t.Fatalf("released %v", answer)
		}
	}
	p.stop()
	receipts := stdioReceipts(t, d)
	if len(receipts) != 3 {
		t.Fatalf("%d receipts", len(receipts))
	}
	for _, r := range receipts {
		if r.Status != "error" || len(r.Rows) != 0 {
			t.Fatalf("receipt %+v", r)
		}
	}
}

// test_rate_limit_counts_scope_denials_is_per_token_and_resets, on the 2026-07-28 revision through the
// command: a scope denial counts against the token's 60 calls a minute, and the 61st call is refused. The
// denial is call 1 and the search call 2, so get_account is calls 3 to 61, with request IDs 4 to 62.
func TestStdioCommandChargesScopeDenials(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	meta := func(id int, name, arguments string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s,%s}}`, id, name, arguments, stdioMeta)
	}
	p.send(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+stdioMeta+`}}`,
		meta(2, "read_call_notes", `{"account_id":"acc-001"}`), meta(3, "search_accounts", `{}`))
	if discovered := p.answer(1); !slices.Contains(dig(discovered, "result", "supportedVersions").([]any), any("2026-07-28")) {
		t.Fatalf("discover %v", discovered)
	}
	if denied, searched := p.answer(2), p.answer(3); reasonOf(denied) != "insufficient_scope" || dig(searched, "result", "isError") != false {
		t.Fatalf("%v\n%v", denied, searched)
	}
	for id := 4; id <= 62; id++ {
		p.send(meta(id, "get_account", `{"account_id":"acc-001"}`))
		reply := p.answer(id)
		switch {
		case id < 62 && dig(reply, "result", "isError") != false:
			t.Fatalf("request %d: %v", id, reply)
		case id == 62 && reasonOf(reply) != "rate_limited":
			t.Fatalf("the 61st call: %v", reply)
		}
	}
	p.stop()
	got := outcomes(stdioReceipts(t, d))
	if len(got) != 61 || got[0] != "read_call_notes error 403" || got[1] != "search_accounts ok 200" || got[60] != "get_account error 429" {
		t.Fatalf("receipts %d: %v ... %v", len(got), got[:2], got[len(got)-1])
	}
}

// Arguments with a NUL character are refused through the command too, under a receipt that records a fixed
// note in their place.
func TestStdioCommandArgumentsWithANULAreRefused(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady,
		stdioCall(2, "get_account", `{"account_id":"acc-001","a\u0000b":1}`),
		stdioCall(3, "search_accounts", `{"query":"a\u0000b"}`),
		stdioCall(4, "nope", `{"list":[{"\u0000":1}]}`))
	p.answer(1)
	for id := 2; id <= 4; id++ {
		if refused := p.answer(id); reasonOf(refused) != "invalid_arguments" {
			t.Fatalf("call %d: %v", id, refused)
		}
	}
	p.stop()
	receipts := stdioReceipts(t, d)
	if len(receipts) != 3 {
		t.Fatalf("%d receipts", len(receipts))
	}
	for _, r := range receipts {
		var arguments map[string]any
		json.Unmarshal(r.Arguments, &arguments)
		if r.Status != "error" || *r.HTTPStatus != 400 || arguments["unrecorded"] != "the arguments hold a NUL character" {
			t.Fatalf("%+v", r)
		}
	}
}

// The principal's grants apply on stdio: an account whose grant was revoked is refused before any read and
// left out of a search, while the process runs. Revoking the process's token refuses its next call.
func TestStdioCommandGrantsAndRevocation(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady)
	p.answer(1)
	tokens, err := d.owner.MachineTokens(ctx, d.workspace.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens %v %v", tokens, err)
	}
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, tokens[0].PrincipalID, "acc-007"); err != nil {
		t.Fatal(err)
	}
	p.send(stdioCall(2, "get_account", `{"account_id":"acc-007"}`), stdioCall(3, "get_account", `{"account_id":"acc-003"}`),
		stdioCall(4, "search_accounts", `{"query":""}`))
	if refused := p.answer(2); dig(refused, "result", "isError") != true ||
		!strings.Contains(fmt.Sprint(dig(refused, "result", "content")), "account_not_granted") {
		t.Fatalf("an account no longer granted %v", refused)
	}
	if read := p.answer(3); dig(read, "result", "isError") != false {
		t.Fatalf("a granted account %v", read)
	}
	if found := fmt.Sprint(dig(p.answer(4), "result", "structuredContent")); strings.Contains(found, "acc-007") || !strings.Contains(found, "acc-003") {
		t.Fatalf("search %s", found)
	}
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	var out, errs bytes.Buffer
	if code := runToken(ctx, []string{"revoke", tokens[0].ID}, screen{out: &out, err: &errs}); code != 0 {
		t.Fatalf("token revoke: %d %s", code, errs.String())
	}
	p.send(stdioCall(5, "get_account", `{"account_id":"acc-003"}`))
	if refused := p.answer(5); reasonOf(refused) != "invalid_token" {
		t.Fatalf("after revocation %v", refused)
	}
	p.stop()
	if got := outcomes(stdioReceipts(t, d)); !reflect.DeepEqual(got, []string{"get_account error 200", "get_account ok 200",
		"search_accounts ok 200", "get_account error 401"}) {
		t.Fatalf("receipts %v", got)
	}
}

// A workspace with no CRM is refused before anything is served, with nothing on stdout and no token made.
func TestStdioCommandNeedsAConnectedWorkspace(t *testing.T) {
	d := setUpDemo(t)
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	for _, name := range []string{defaultWorkspace, "elsewhere"} {
		var out, errs bytes.Buffer
		code := mcpCommand(context.Background(), []string{"stdio", "--scope", "crm:read", "--state-dir", name},
			screen{out: &out, err: &errs, json: true}, strings.NewReader(""))
		if code != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "No CRM is connected.") {
			t.Errorf("%s: exit %d\n%s%s", name, code, out.String(), errs.String())
		}
	}
	if got := d.count(t, `SELECT count(*) FROM machine_tokens`); got != 0 {
		t.Fatalf("%d tokens made", got)
	}
}

// An app's entry can hold the database URL without its password: the binary reads it from ~/.pgpass.
// It needs a server that checks passwords, so that only the file's password can open the connection, and a
// wrong one in the file is refused.
func TestStdioCommandReadsThePasswordFromPgpass(t *testing.T) {
	d := stdioDemo(t)
	parsed, err := url.Parse(d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	wrong := *parsed
	wrong.User = url.UserPassword(parsed.User.Username(), "not-the-password")
	if check, err := postgres.Open(ctx, wrong.String()); err == nil {
		check.Close()
		t.Skip("the test server accepts a wrong password, so it cannot show the password file was read")
	}
	home := t.TempDir()
	passfile := func(password string) {
		t.Helper()
		entry := strings.Join([]string{parsed.Hostname(), parsed.Port(), strings.TrimPrefix(parsed.Path, "/"),
			parsed.User.Username(), password}, ":")
		if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte(entry+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bare := *parsed
	bare.User = url.User(parsed.User.Username())
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "GTM_DATABASE_URL=" + bare.String()}

	passfile("not-the-password")
	refused := exec.Command(buildBinary(t), "mcp", "stdio", "--scope", "crm:read")
	refused.Env, refused.Stdin = env, strings.NewReader("")
	if out, err := refused.Output(); err == nil || len(out) != 0 {
		t.Fatalf("a wrong password in the file was not refused: %v %q", err, out)
	}

	passfile(password)
	p := startStdioWith(t, env, "--scope", "crm:read")
	p.send(stdioStart, stdioReady, stdioCall(2, "get_account", `{"account_id":"acc-003"}`))
	p.answer(1)
	if read := p.answer(2); dig(read, "result", "isError") != false {
		t.Fatalf("get_account %v", read)
	}
	p.stop()
}

// When its input ends the process stops, and a call it was still running leaves a finished receipt.
func TestStdioCommandFinishesEveryReceiptAtTheEndOfInput(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady)
	p.answer(1)
	p.send(stdioCall(2, "search_accounts", `{"query":""}`), stdioCall(3, "get_account", `{"account_id":"acc-003"}`))
	p.in.Close()
	for range p.lines {
	}
	<-p.done
	if p.err != nil {
		t.Fatalf("mcp stdio: %v; stderr: %s", p.err, p.stderr.String())
	}
	receipts := stdioReceipts(t, d)
	if len(receipts) != 2 {
		t.Fatalf("%d receipts", len(receipts))
	}
	for _, r := range receipts {
		if r.Status == "pending" || r.FinishedAt == nil {
			t.Fatalf("a receipt left pending: %+v", r)
		}
	}
}
