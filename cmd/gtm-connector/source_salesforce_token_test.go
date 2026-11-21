package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// The refusals of a token handed to the process, as a command prints them.
const (
	tokenExpired = "salesforce_auth_required: The token expired or was refused; give the container a new one of " +
		"the same org and user."
	tokenMissing = "salesforce_invalid_configuration: SALESFORCE_INSTANCE_URL, and SALESFORCE_ACCESS_TOKEN or " +
		"SALESFORCE_ACCESS_TOKEN_FILE, are missing or invalid."
)

// handToken sets the variables of a token handed to the process, the token in a file of its own, and gives
// a function that replaces the file's token, as an orchestrator that rotates a mounted secret does.
func handToken(t *testing.T, token string) func(string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "salesforce-token")
	write := func(token string) {
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	write(token)
	t.Setenv(salesforce.InstanceVariable, salesforcetest.Instance)
	t.Setenv(salesforce.TokenVariable, "")
	t.Setenv(salesforce.TokenFileVariable, path)
	return write
}

// everything is the text of every row of every table of the database, as the owner reads it.
func (d *demoDatabase) everything(t *testing.T) string {
	t.Helper()
	c := conn(t, d.ownerURL)
	rows, err := c.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if rows.Close(); len(tables) < 5 {
		t.Fatalf("the database has %d tables", len(tables))
	}
	var all strings.Builder
	for _, table := range tables {
		var text string
		if err := c.QueryRow(ctx, `SELECT coalesce(string_agg(t::text, ' '), '') FROM "`+table+`" t`).Scan(&text); err != nil {
			t.Fatal(err)
		}
		all.WriteString(text + "\n")
	}
	return all.String()
}

// connect salesforce --token-env reads the instance address and the token from the variables of the
// process, with no sf, checks the org and the user as an sf login is checked, and pins the workspace to
// the same identity. Every command then reads the org with that token. A first HTTP 401 reads the file
// once more: a new token of the same org and user is taken and the read goes on, and one of another org or
// user is refused as expired. Missing or invalid variables are refused by one fixed line. No token reaches
// any output, row or receipt.
func TestASalesforceTokenHandedToTheProcessIsReadByEveryCommand(t *testing.T) {
	d := setUpDemo(t)
	org, sf, _ := inventedSalesforce(t)
	rotate := handToken(t, salesforcetest.Token)
	const (
		rotated      = "invented-rotated-token-of-the-same-user"
		otherUser    = "invented-rotated-token-of-another-user"
		otherOrg     = "invented-rotated-token-of-another-org"
		otherUserNew = "invented-token-of-another-user-at-start"
	)
	secrets := []string{salesforcetest.Token, rotated, otherUser, otherOrg, otherUserNew}
	var printed strings.Builder
	run := func(tokens ...string) (int, string, string) {
		t.Helper()
		code, stdout, stderr := d.read(t, tokens...)
		printed.WriteString(stdout + stderr)
		return code, stdout, stderr
	}
	refusal := func(code int, tokens ...string) (string, string) {
		t.Helper()
		_, message, next := d.refusal(t, code, tokens...)
		printed.WriteString(message + next)
		return message, next
	}
	where := []string{"--state-dir", "token-books"}
	connect := append([]string{"connect", "salesforce", "--token-env", "--environment", "production", "--seller",
		"Morgan Reed"}, where...)

	if message, _ := refusal(2, "connect", "salesforce", "--token-env", "--org", salesforcetest.Alias,
		"--state-dir", "token-books"); message != "--org and --token-env are two ways to sign in. Give one." {
		t.Errorf("both ways to sign in: %s", message)
	}
	code, stdout, stderr := run(connect...)
	if code != 0 || stderr != "" || stdout != "Salesforce connected. The org's identity and environment were checked.\n"+
		"crm\tconnected\tSalesforce token from the environment (production)\ncalls\tnot connected\t\n"+
		"slack\tnot connected\t\n" {
		t.Fatalf("connect: exit %d\n%s%s", code, stdout, stderr)
	}
	// No sf ran. The org was asked who it is, which user the token reads as, and the four objects' fields.
	if len(sf.Commands) != 0 || len(org.Requests) != 6 || strings.Contains(strings.Join(org.Requests, " "), "FROM Account") {
		t.Errorf("connect ran %d sf commands and sent %q", len(sf.Commands), org.Requests)
	}
	var status connectResult
	d.result(t, &status, append([]string{"connect"}, where...)...)
	if source := status.Sources["crm"]; source["provider"] != "salesforce" || source["credentials"] != "environment" ||
		source["environment"] != "production" || source["org"] != nil {
		t.Errorf("status %+v", status)
	}
	// The workspace is pinned to the identity an sf login of the same org and user is pinned to.
	d.result(t, &connectResult{}, "connect", "salesforce", "--org", salesforcetest.Alias, "--environment",
		"production", "--state-dir", "sf-books")
	pins := map[string]string{}
	for _, name := range []string{"token-books", "sf-books"} {
		workspace, err := d.owner.Workspace(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := d.owner.Connections(ctx, workspace.ID)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		pins[rows[0].Mode] = rows[0].IdentitySHA256
	}
	if pins["env_token"] == "" || pins["env_token"] != pins["sf"] {
		t.Errorf("the pins: %v", pins)
	}
	sf.Commands = nil

	code, stdout, stderr = run(append([]string{"accounts", "--query", "Gemini export 0"}, where...)...)
	if code != 0 || stderr != "Sources: CRM is Salesforce (production), identity verified.\n" ||
		!strings.HasPrefix(stdout, inventedAccount+"\tGemini export 0: Zoom cloud recording VTT\tExports\t") {
		t.Fatalf("accounts: exit %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, stderr = run(append([]string{"brief", inventedAccount, "--as-of", "2027-01-15"}, where...)...)
	if code != 0 || !strings.HasPrefix(stdout, `DEAL CARD: "Gemini export 0: Zoom cloud recording VTT" (`+
		inventedAccount+`) as of 2027-01-15`) {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	// The MCP tools read the same org through the same token.
	answers := d.stdioSession(t, append([]string{"--scope", "crm:read"}, where...), stdioStart, stdioReady,
		stdioCall(2, "get_account", `{"account_id":"`+inventedAccount+`"}`))
	printed.WriteString(fmt.Sprint(answers))
	if read := answers[2]; dig(read, "result", "isError") != false ||
		!strings.Contains(stringOf(dig(read, "result", "content")), inventedAccount) {
		t.Errorf("get_account over stdio: %v", read)
	}

	// expireAt makes the org refuse the token at the first account search, after the command's first
	// check, and gives the file the token next.
	expireAt := func(next string, change func()) {
		done := false
		org.Intercept = func(request *http.Request) *http.Response {
			if !done && strings.Contains(request.URL.Query().Get("q"), "FROM Account") {
				done = true
				org.Token = next
				change()
				rotate(next)
			}
			return nil
		}
	}
	reset := func() {
		org.Intercept, org.Token, org.ID = nil, salesforcetest.Token, salesforcetest.OrganizationID
		delete(org.Users, otherUser)
		rotate(salesforcetest.Token)
	}

	// A new token of the same org and user: the read goes on with it.
	expireAt(rotated, func() {})
	org.Requests = nil
	code, stdout, stderr = run(append([]string{"accounts", "--query", "Gemini export 0"}, where...)...)
	if code != 0 || !strings.HasPrefix(stdout, inventedAccount+"\t") {
		t.Fatalf("accounts across a rotation: exit %d\n%s%s", code, stdout, stderr)
	}
	// Who the org is, the user and the four objects' fields, the refused search, who the org and the user
	// are under the new token, and the search again.
	if len(org.Requests) != 10 || org.Requests[6] != org.Requests[9] {
		t.Errorf("the requests across a rotation:\n%s", strings.Join(org.Requests, "\n"))
	}
	// A new token of another user or another org is refused as an expired one, and nothing is read with it.
	for name, change := range map[string]func(){
		otherUser: func() { org.Users[otherUser] = [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"} },
		otherOrg:  func() { org.ID = "00D000000000000AAA" },
	} {
		reset()
		expireAt(name, change)
		org.Requests = nil
		message, next := refusal(5, append([]string{"accounts", "--query", "Gemini export 0"}, where...)...)
		if message != tokenExpired || next != salesforceNext ||
			strings.Count(strings.Join(org.Requests, " "), "FROM Account") != 1 {
			t.Errorf("%s: %s (%s), after %q", name, message, next, org.Requests)
		}
	}
	// A token of another user from the start is not the pinned identity.
	reset()
	org.Token = otherUserNew
	org.Users[otherUserNew] = [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"}
	rotate(otherUserNew)
	org.Requests = nil
	if message, _ := refusal(3, append([]string{"accounts"}, where...)...); !strings.HasPrefix(message,
		"source_identity_mismatch: The org or user of this token is not the one this workspace is pinned to") ||
		strings.Contains(strings.Join(org.Requests, " "), "FROM Account") {
		t.Errorf("a token of another user: %s, after %q", message, org.Requests)
	}
	reset()

	// Missing or invalid variables are one fixed line, before any request.
	org.Requests = nil
	for name, set := range map[string]func(){
		"no instance":      func() { t.Setenv(salesforce.InstanceVariable, "") },
		"an http instance": func() { t.Setenv(salesforce.InstanceVariable, "http://invented-books.my.salesforce.com") },
		"no token":         func() { t.Setenv(salesforce.TokenFileVariable, "") },
		"a missing file":   func() { t.Setenv(salesforce.TokenFileVariable, filepath.Join(t.TempDir(), "gone")) },
		"a short token":    func() { t.Setenv(salesforce.TokenFileVariable, ""); t.Setenv(salesforce.TokenVariable, "short") },
		"two tokens":       func() { t.Setenv(salesforce.TokenVariable, salesforcetest.Token) },
		"a token in spaces": func() {
			t.Setenv(salesforce.TokenFileVariable, "")
			t.Setenv(salesforce.TokenVariable, " "+salesforcetest.Token)
		},
	} {
		path := os.Getenv(salesforce.TokenFileVariable)
		set()
		for _, tokens := range [][]string{append([]string{"accounts"}, where...),
			{"connect", "salesforce", "--token-env", "--environment", "production", "--state-dir", "missing-books"}} {
			if message, _ := refusal(2, tokens...); message != tokenMissing {
				t.Errorf("%s, %s: %s", name, tokens[0], message)
			}
		}
		t.Setenv(salesforce.InstanceVariable, salesforcetest.Instance)
		t.Setenv(salesforce.TokenVariable, "")
		t.Setenv(salesforce.TokenFileVariable, path)
	}
	if len(org.Requests) != 0 {
		t.Errorf("invalid variables sent %q", org.Requests)
	}
	if _, err := d.owner.Workspace(ctx, "missing-books"); !errors.Is(err, postgres.ErrNotFound) {
		t.Error("a refused connect made its workspace")
	}

	// The check of the connected workspace runs every step through the token, and no sf.
	code, stdout, stderr = d.debug(t, append([]string{"check", "--as-of", "2027-01-15"}, where...)...)
	printed.WriteString(stdout + stderr)
	if code != 0 || !strings.HasPrefix(stderr, "Salesforce credentials: running.\nSalesforce credentials: pass. Count: 2.\n") {
		t.Fatalf("check: exit %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = run(append([]string{"audit"}, where...)...)
	if code != 0 || len(sf.Commands) != 0 {
		t.Fatalf("audit: exit %d, sf ran %d commands", code, len(sf.Commands))
	}

	// No token reaches what any command printed, or any row: receipts, cards, connections, workspaces.
	rows := d.everything(t)
	for _, secret := range secrets {
		if strings.Contains(printed.String(), secret) {
			t.Errorf("a command printed a token of %d characters", len(secret))
		}
		if strings.Contains(rows, secret) {
			t.Errorf("the database holds a token of %d characters", len(secret))
		}
	}
	for _, private := range []string{salesforcetest.OrganizationID, "my.salesforce.com"} {
		if strings.Contains(rows, private) {
			t.Errorf("the database holds %q", private)
		}
	}
	var stored string
	if err := conn(t, d.ownerURL).QueryRow(ctx, `SELECT c.binding::text FROM connections c
		JOIN workspaces w ON w.id = c.workspace_id WHERE w.slug = 'token-books' AND c.mode = 'env_token'`).Scan(&stored); err != nil ||
		stored != `{"credentials":"environment","environment":"production"}` {
		t.Errorf("the saved binding: %s %v", stored, err)
	}
	var removed connectResult
	if d.result(t, &removed, append([]string{"connect", "salesforce", "--remove"}, where...)...); removed.Status != "removed" {
		t.Errorf("remove: %+v", removed)
	}
	workspace, err := d.owner.Workspace(ctx, "token-books")
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := d.owner.Connections(ctx, workspace.ID); err != nil || rows[0].Mode != "none" ||
		rows[0].IdentitySHA256 != pins["env_token"] {
		t.Errorf("the removed row: %+v %v", rows, err)
	}
}

// A CRM that serve or mcp stdio keeps open is let go of a login the pin refused: once the token file holds
// a token of the pinned user again, the next read signs in afresh and reads. A token file others can read
// is refused by one line that gives its mode and nothing of the file.
func TestATokenRefusedByThePinIsReadAgainOnceItIsPutRight(t *testing.T) {
	d := setUpDemo(t)
	org, _, _ := inventedSalesforce(t)
	rotate := handToken(t, salesforcetest.Token)
	d.result(t, &connectResult{}, "connect", "salesforce", "--token-env", "--environment", "production",
		"--state-dir", "token-books")
	workspace, err := d.owner.Workspace(ctx, "token-books")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.owner.Connections(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindingOf(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	crm, err := openSalesforce(ctx, opening{connection: rows[0], binding: binding})
	if err != nil {
		t.Fatal(err)
	}

	const otherUser = "invented-token-of-another-user-of-the-org"
	org.Token = otherUser
	org.Users[otherUser] = [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"}
	rotate(otherUser)
	if _, err := crm.Read(ctx, "get_account", inventedAccount); failureCode(err) != "source_identity_mismatch" {
		t.Fatalf("a token of another user: %v", err)
	}
	org.Token = salesforcetest.Token
	rotate(salesforcetest.Token)
	if records, err := crm.Read(ctx, "get_account", inventedAccount); err != nil || len(records) == 0 {
		t.Fatalf("after the file was put right: %v", err)
	}

	if err := os.Chmod(os.Getenv(salesforce.TokenFileVariable), 0o644); err != nil {
		t.Fatal(err)
	}
	_, message, _ := d.refusal(t, 2, "accounts", "--state-dir", "token-books")
	if message != "salesforce_invalid_configuration: The token file can be read by others than its owner (mode "+
		"0644). Make it readable by its owner alone: chmod 600." {
		t.Errorf("a token file others can read: %s", message)
	}
}
