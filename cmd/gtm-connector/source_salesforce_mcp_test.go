package main

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// stdioSession runs mcp stdio in this process, sends each message and waits for the answer of each request
// before the next, then ends its input. It gives each answer by its ID.
func (d *demoDatabase) stdioSession(t *testing.T, args []string, messages ...string) map[int]map[string]any {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	in, input := io.Pipe()
	output, out := io.Pipe()
	var errs lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- mcpCommand(ctx, append([]string{"stdio"}, args...), screen{out: out, err: &errs}, in)
		out.Close()
	}()
	lines := bufio.NewScanner(output)
	lines.Buffer(make([]byte, 1<<20), 1<<24)
	answers := map[int]map[string]any{}
	for _, message := range messages {
		if _, err := io.WriteString(input, message+"\n"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(message, `"id":`) {
			continue
		}
		if !lines.Scan() {
			t.Fatalf("mcp stdio ended before it answered: %s", errs.String())
		}
		var answer map[string]any
		if err := json.Unmarshal(lines.Bytes(), &answer); err != nil {
			t.Fatalf("an answer that is not JSON: %q", lines.Text())
		}
		id, _ := answer["id"].(float64)
		answers[int(id)] = answer
	}
	input.Close()
	go func() { _, _ = io.Copy(io.Discard, output) }()
	if code := <-done; code != 0 {
		t.Fatalf("mcp stdio: exit %d\n%s", code, errs.String())
	}
	return answers
}

// Both MCP transports serve a Salesforce workspace through the seam its commands read it by: mcp stdio
// --state-dir serves that workspace, and serve's /mcp serves each token's own workspace. A token reads the
// accounts the workspace holds, its calls are receipted there, and the org is signed in on the first read and
// checked against the pin then: an org that is not the pinned one is read nothing.
func TestSalesforceIsServedOverMCP(t *testing.T) {
	d := setUpDemo(t)
	org, sf, _ := inventedSalesforce(t)
	where := []string{"--state-dir", "invented-books"}
	d.result(t, &connectResult{}, append([]string{"connect", "salesforce", "--org", salesforcetest.Alias,
		"--environment", "production"}, where...)...)
	// brief lists the account in the workspace, which is what a token is granted.
	if code, _, stderr := d.read(t, append([]string{"brief", inventedAccount, "--as-of", "2027-01-15"}, where...)...); code != 0 {
		t.Fatalf("brief: %d %s", code, stderr)
	}

	answers := d.stdioSession(t, append([]string{"--scope", "crm:read"}, where...), stdioStart, stdioReady,
		stdioCall(2, "get_account", `{"account_id":"`+inventedAccount+`"}`),
		stdioCall(3, "search_accounts", `{"query":"Gemini export"}`),
		stdioCall(4, "list_opportunities", `{}`),
		stdioCall(5, "get_account", `{"account_id":"acc-sf-0012AF6DF49DA83QNH"}`))
	for id := 2; id <= 4; id++ {
		if read := answers[id]; dig(read, "result", "isError") != false ||
			!strings.Contains(stringOf(dig(read, "result", "content")), inventedAccount) {
			t.Errorf("stdio call %d: %v", id, read)
		}
	}
	// The search holds only the account the workspace holds, of the ten whose name matches.
	if found := stringOf(dig(answers[3], "result", "content")); strings.Count(found, "crm/accounts/acc-sf-") != 1 {
		t.Errorf("the search: %s", found)
	}
	if refused := answers[5]; !strings.Contains(stringOf(dig(refused, "result", "content")), "account_not_granted") {
		t.Errorf("an account the workspace does not hold: %v", refused)
	}
	if got := d.count(t, `SELECT count(*) FROM receipts r JOIN workspaces w ON w.id = r.workspace_id
		WHERE w.slug = 'invented-books' AND r.tool IN ('get_account', 'search_accounts', 'list_opportunities')
		AND r.principal_id IS NOT NULL`); got != 4 {
		t.Errorf("%d receipts of the stdio calls", got)
	}

	// Over HTTPS, a token of the workspace is served by its own gate.
	code, stdout, stderr := d.tokenCommand(t, append([]string{"new", "--scope", "crm:read", "--json"}, where...)...)
	var issued issuedToken
	if err := json.Unmarshal([]byte(stdout), &issued); code != 0 || err != nil || issued.Workspace != "invented-books" {
		t.Fatalf("token new: %d %s", code, stderr)
	}
	s := startServe(t, d, nil)
	if status, read := s.call(s.address, "get_account", issued.Token, map[string]any{"account_id": inventedAccount}); status != 200 ||
		dig(read, "result", "isError") != false || !strings.Contains(stringOf(dig(read, "result", "content")), "Gemini export 0") {
		t.Fatalf("get_account over HTTPS: %d %v", status, read)
	}

	// The alias now opens another org. A session opens the CRM afresh and reads nothing from it.
	org.ID, sf.OrganizationID = "00D000000000000AAA", "00D000000000000AAA"
	org.Requests = nil
	answers = d.stdioSession(t, append([]string{"--scope", "crm:read"}, where...), stdioStart, stdioReady,
		stdioCall(2, "get_account", `{"account_id":"`+inventedAccount+`"}`))
	if refused := answers[2]; dig(refused, "result", "isError") != true ||
		!strings.Contains(stringOf(dig(refused, "result", "content")), "source_identity_mismatch") {
		t.Errorf("another org over stdio: %v", refused)
	}
	for _, request := range org.Requests {
		if strings.Contains(request, "FROM Account") || strings.HasSuffix(request, "/describe") {
			t.Errorf("another org was read: %q", org.Requests)
		}
	}
}
