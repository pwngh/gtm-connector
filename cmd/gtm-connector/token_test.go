package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// tokenCommand runs token as main does, connecting as the app role.
func (d *demoDatabase) tokenCommand(t *testing.T, tokens ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	rest, asJSON := withoutJSON(tokens)
	var out, errs bytes.Buffer
	code = runToken(ctx, rest, screen{out: &out, err: &errs, json: asJSON})
	return code, out.String(), errs.String()
}

type issuedToken struct {
	Token, Workspace, ID, State, Caller, URL string
	Scopes                                   []string
	CreatedAt                                string  `json:"created_at"`
	ExpiresAt                                string  `json:"expires_at"`
	RevokedAt                                *string `json:"revoked_at"`
}

// After setup and demo, token new makes a token that is shown once and stored as its SHA-256 only, for the
// demo workspace's MCP clients principal with a grant on every demo account. /mcp answers it with its scope
// and refuses it for another scope, once it has expired, and once it is revoked. Its receipts, audit, token
// list and serve's log never hold it.
func TestATokenFromTheCommandOpensMCP(t *testing.T) {
	d := stdioDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := d.tokenCommand(t, "new", "--scope", "crm:read", "--scope", "calls:read", "--scope", "crm:read", "--json")
	var issued issuedToken
	if err := json.Unmarshal([]byte(stdout), &issued); code != 0 || stderr != "" || err != nil {
		t.Fatalf("token new: exit %d %v\n%s", code, err, stderr)
	}
	digest := sha256.Sum256([]byte(issued.Token))
	created, _ := time.Parse(time.RFC3339, issued.CreatedAt)
	expires, _ := time.Parse(time.RFC3339, issued.ExpiresAt)
	if len(issued.Token) != 43 || issued.Workspace != "demo" || issued.Caller != hex.EncodeToString(digest[:]) ||
		!reflect.DeepEqual(issued.Scopes, []string{"crm:read", "calls:read"}) || issued.State != "live" ||
		expires.Sub(created) < 4*time.Minute || expires.Sub(created) > 6*time.Minute || !strings.HasSuffix(issued.URL, "/mcp") {
		t.Fatalf("issued %+v", issued)
	}
	if got := d.count(t, `SELECT count(*) FROM machine_tokens t WHERE position($1 in t::text) > 0`, issued.Token); got != 0 {
		t.Fatal("the store holds the token")
	}
	if got := d.count(t, `SELECT count(*) FROM account_grants g JOIN principals p ON p.id = g.principal_id
		WHERE p.display_name = 'MCP clients' AND p.kind = 'machine' AND g.revoked_at IS NULL`); got != 30 {
		t.Fatalf("the MCP clients principal holds %d grants, not 30", got)
	}

	s := startServe(t, d, nil)
	account := map[string]any{"account_id": "acc-003"}
	if status, read := s.call(s.address, "get_account", issued.Token, account); status != 200 || dig(read, "result", "isError") != false {
		t.Fatalf("get_account: %d %v", status, read)
	}
	if status, read := s.call(s.address, "read_call_notes", issued.Token, account); status != 200 || dig(read, "result", "isError") != false {
		t.Fatalf("read_call_notes: %d %v", status, read)
	}
	if status, denied := s.call(s.address, "read_slack_messages", issued.Token, account); status != 403 || reasonOf(denied) != "insufficient_scope" {
		t.Fatalf("read_slack_messages: %d %v", status, denied)
	}

	// The list shows it live, and the audit log names its calls by its fingerprint.
	code, stdout, _ = d.tokenCommand(t, "list", "--json")
	var listed []issuedToken
	json.Unmarshal([]byte(stdout), &listed)
	if code != 0 || len(listed) != 1 || listed[0].ID != issued.ID || listed[0].State != "live" || listed[0].Token != "" ||
		listed[0].Caller != issued.Caller {
		t.Fatalf("token list: %d %+v", code, listed)
	}
	_, audit, _ := d.read(t, "audit", "--caller", issued.Caller, "--json")
	var receipts []receiptRow
	if err := json.Unmarshal([]byte(audit), &receipts); err != nil || len(receipts) != 3 {
		t.Fatalf("audit --caller: %v %s", err, audit)
	}

	// Revoked, it is refused, with a receipt.
	code, stdout, _ = d.tokenCommand(t, "revoke", issued.ID)
	if code != 0 || !strings.HasPrefix(stdout, "Revoked token "+issued.ID+".") {
		t.Fatalf("token revoke: %d %s", code, stdout)
	}
	if status, refused := s.call(s.address, "get_account", issued.Token, account); status != 401 || reasonOf(refused) != "invalid_token" {
		t.Fatalf("a revoked token: %d %v", status, refused)
	}
	code, stdout, _ = d.tokenCommand(t, "list", "--json")
	json.Unmarshal([]byte(stdout), &listed)
	if code != 0 || listed[0].State != "revoked" || listed[0].RevokedAt == nil {
		t.Fatalf("after revoke: %+v", listed)
	}
	if code, _, stderr := d.tokenCommand(t, "revoke", "6f1c2f0e-0000-4000-8000-000000000000", "--json"); code != 2 ||
		!strings.Contains(stderr, "No token in this workspace has that ID.") {
		t.Fatalf("revoke of no token: %d %s", code, stderr)
	}

	// Expired, it is refused.
	_, stdout, _ = d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	var second issuedToken
	json.Unmarshal([]byte(stdout), &second)
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, `UPDATE machine_tokens SET created_at = now() - interval '1 hour',
		expires_at = now() - interval '1 second' WHERE id = $1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if status, refused := s.call(s.address, "get_account", second.Token, account); status != 401 || reasonOf(refused) != "invalid_token" {
		t.Fatalf("an expired token: %d %v", status, refused)
	}
	_, stdout, _ = d.tokenCommand(t, "list")
	if !strings.Contains(stdout, second.ID+"\tcrm:read\texpired\t") {
		t.Fatalf("token list:\n%s", stdout)
	}

	// No token reaches a receipt, the audit log, the list or serve's log.
	_, audit, _ = d.read(t, "audit", "--json")
	_, list, _ := d.tokenCommand(t, "list", "--json")
	for _, text := range []string{audit, list, s.logs.String()} {
		for _, token := range []string{issued.Token, second.Token} {
			if strings.Contains(text, token) {
				t.Fatalf("a token was printed again: %.300s", text)
			}
		}
	}
	if got := d.count(t, `SELECT count(*) FROM receipts r WHERE position($1 in r::text) > 0 OR position($2 in r::text) > 0`,
		issued.Token, second.Token); got != 0 {
		t.Fatal("a receipt holds a token")
	}
}

// Every client token of a workspace holds the grants of its MCP clients principal. A grant revoked for that
// principal stays revoked when a token is issued again, and a disabled principal gets no token and serves no
// stdio. A token of another workspace opens nothing at the demo workspace's /mcp.
func TestClientTokenGrantsAndWorkspaces(t *testing.T) {
	d := stdioDemo(t)
	_, stdout, _ := d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	var first issuedToken
	json.Unmarshal([]byte(stdout), &first)
	tokens, err := d.owner.MachineTokens(ctx, d.workspace.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatal(err)
	}
	principal := tokens[0].PrincipalID
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal, "acc-003"); err != nil {
		t.Fatal(err)
	}
	_, stdout, _ = d.tokenCommand(t, "new", "--scope", "crm:read", "--minutes", "60", "--json")
	var second issuedToken
	json.Unmarshal([]byte(stdout), &second)
	tokens, _ = d.owner.MachineTokens(ctx, d.workspace.ID)
	created, _ := time.Parse(time.RFC3339, second.CreatedAt)
	expires, _ := time.Parse(time.RFC3339, second.ExpiresAt)
	if len(tokens) != 2 || tokens[1].PrincipalID != principal || expires.Sub(created) < 59*time.Minute {
		t.Fatalf("second token %+v", second)
	}
	if granted, err := d.owner.HasAccountGrant(ctx, d.workspace.ID, principal, "acc-003"); granted || err != nil {
		t.Fatalf("issuing a token gave back a revoked grant: %v", err)
	}
	s := startServe(t, d, nil)
	if status, refused := s.call(s.address, "get_account", second.Token, map[string]any{"account_id": "acc-003"}); status != 200 ||
		!strings.Contains(stringOf(dig(refused, "result", "content")), "account_not_granted") {
		t.Fatalf("an account no longer granted: %d %v", status, refused)
	}

	// Another workspace's token is served by that workspace's own gate: its call is receipted there, and the
	// demo workspace's revoked grant does not apply to it.
	elsewhere := d.connectDemo(t, "elsewhere")
	code, stdout, stderr := d.tokenCommand(t, "new", "--scope", "crm:read", "--state-dir", "elsewhere", "--json")
	var other issuedToken
	if err := json.Unmarshal([]byte(stdout), &other); code != 0 || err != nil || other.Workspace != "elsewhere" {
		t.Fatalf("a token of another workspace: %d %s", code, stderr)
	}
	before := d.count(t, "SELECT count(*) FROM receipts WHERE workspace_id = $1", d.workspace.ID)
	if status, answered := s.call(s.address, "get_account", other.Token, map[string]any{"account_id": "acc-003"}); status != 200 ||
		dig(answered, "result", "isError") != false {
		t.Fatalf("another workspace's token: %d %v", status, answered)
	}
	if d.count(t, "SELECT count(*) FROM receipts WHERE workspace_id = $1 AND tool = 'get_account'", elsewhere.ID) != 1 ||
		d.count(t, "SELECT count(*) FROM receipts WHERE workspace_id = $1", d.workspace.ID) != before {
		t.Fatal("the call was not receipted in its own workspace alone")
	}

	// A disabled clients principal gets no token, and no stdio server.
	if err := d.owner.DisablePrincipal(ctx, d.workspace.ID, principal); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	if code != 3 || !strings.Contains(stderr, "principal of this workspace is disabled") {
		t.Fatalf("token new for a disabled principal: %d %s", code, stderr)
	}
	var out, errs bytes.Buffer
	if code := mcpCommand(ctx, []string{"stdio", "--scope", "crm:read"}, screen{out: &out, err: &errs}, strings.NewReader("")); code != 3 ||
		out.Len() != 0 || !strings.Contains(errs.String(), "principal of this workspace is disabled") {
		t.Fatalf("mcp stdio for a disabled principal: %d %s", code, errs.String())
	}
	if got := d.count(t, `SELECT count(*) FROM principals WHERE display_name = 'MCP clients'`); got != 2 {
		t.Fatalf("%d MCP clients principals, not one for each of the two workspaces", got)
	}
}

func stringOf(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// token refuses what it cannot issue before it reads the database, and a workspace with no CRM gets no token.
func TestTokenArguments(t *testing.T) {
	t.Setenv("GTM_DATABASE_URL", "")
	for _, c := range []struct {
		args            []string
		command, reason string
	}{
		{nil, "token", "the following arguments are required: action"},
		{[]string{"make"}, "token", "argument action: invalid choice (choose from 'new', 'list', 'revoke')"},
		{[]string{"new"}, "token new", "the following arguments are required: --scope"},
		{[]string{"new", "--scope", "crm:write"}, "token new", "argument --scope: invalid choice (choose from 'crm:read', 'calls:read', 'slack:read')"},
		{[]string{"new", "--scope", "crm:read", "--minutes", "0"}, "token new", "argument --minutes: a whole number of minutes from 1 to 60"},
		{[]string{"new", "--scope", "crm:read", "--minutes", "61"}, "token new", "argument --minutes: a whole number of minutes from 1 to 60"},
		{[]string{"new", "--scope", "crm:read", "--minutes", "1.5"}, "token new", "argument --minutes: a whole number of minutes from 1 to 60"},
		{[]string{"revoke"}, "token revoke", "the following arguments are required: ID"},
		{[]string{"revoke", "postgres://user:secret@host/db"}, "token revoke", "A token ID is the UUID that gtm-connector token list prints."},
		{[]string{"list", "extra"}, "token list", "unrecognized arguments"},
	} {
		var out, errs bytes.Buffer
		code := runToken(ctx, c.args, screen{out: &out, err: &errs, json: true})
		var refused struct {
			Error struct{ Code, Message, Next string }
		}
		json.Unmarshal(errs.Bytes(), &refused)
		if code != 2 || out.Len() != 0 || refused.Error.Message != c.reason || refused.Error.Next != "gtm-connector "+c.command+" --help" {
			t.Errorf("token %v: exit %d\n%s", c.args, code, errs.String())
		}
	}
	for _, args := range [][]string{{"--help"}, {"new", "--help"}, {"list", "-h"}, {"revoke", "--help"}} {
		var out, errs bytes.Buffer
		if code := runToken(ctx, args, screen{out: &out, err: &errs}); code != 0 || !strings.HasPrefix(out.String(), "usage: gtm-connector token") {
			t.Errorf("token %v: exit %d\n%s", args, code, errs.String())
		}
	}
}

func TestTokenNeedsAConnectedWorkspace(t *testing.T) {
	d := setUpDemo(t)
	code, stdout, stderr := d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "No CRM is connected.") {
		t.Fatalf("token new without a CRM: %d %s%s", code, stdout, stderr)
	}
	if code, stdout, _ := d.tokenCommand(t, "list", "--state-dir", "nowhere"); code != 0 ||
		stdout != "No tokens. Make one with gtm-connector token new --scope crm:read --state-dir nowhere.\n" {
		t.Fatalf("token list of no workspace: %d %q", code, stdout)
	}
}
