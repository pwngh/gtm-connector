package main

import (
	"net/url"
	"strings"
	"testing"
)

// appRole is the name of the app role.
func (d *demoDatabase) appRole(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.User.Username()
}

// With --state-dir, demo opens one renewal in that workspace: it makes the workspace and connects it to
// the demo data, looks the account up, has the read of its call notes refused for a grant without
// calls:read, and makes its card. Each step has its receipt, and the workspace then answers the other
// commands.
func TestDemoOpensOneRenewalInAWorkspaceOfItsOwn(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "renewal-demo"}
	code, stdout, stderr := d.read(t, append([]string{"demo"}, where...)...)
	lines := strings.Split(stdout, "\n")
	if code != 0 || stderr != "" || len(lines) < 8 ||
		lines[0] != "Call-note access was refused (HTTP 403) for a grant without calls:read." ||
		lines[1] != "Every source below is an invented demo fixture." || lines[2] != "" ||
		lines[3] != `DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15` ||
		!strings.HasPrefix(lines[len(lines)-4], "Brief: brief-") ||
		!strings.HasSuffix(lines[len(lines)-3], " --state-dir renewal-demo") {
		t.Fatalf("demo: exit %d\n%s%s", code, stdout, stderr)
	}
	brief := strings.TrimPrefix(lines[len(lines)-4], "Brief: ")
	var receipts []receiptRow
	d.result(t, &receipts, append([]string{"audit"}, where...)...)
	tools := ""
	for _, receipt := range receipts {
		tools += receipt.Tool + ":" + receipt.Status + " "
	}
	if tools != "search_accounts:ok read_call_notes:error get_account:ok list_opportunities:ok read_call_notes:ok "+
		"read_slack_messages:ok save_brief:ok " || *receipts[1].HTTPStatus != 403 || len(receipts[1].Rows) != 0 ||
		receipts[0].TokenID != receipts[1].TokenID || receipts[0].TokenID == receipts[2].TokenID ||
		*receipts[0].BriefID != *receipts[1].BriefID || !strings.HasPrefix(*receipts[0].BriefID, "demo-") ||
		*receipts[2].BriefID != brief {
		t.Fatalf("the renewal's receipts: %s", tools)
	}
	// The run's own receipts are found by its ID, and the workspace is connected as connect demo leaves one.
	var own []receiptRow
	if d.result(t, &own, append([]string{"audit", "--brief", *receipts[0].BriefID}, where...)...); len(own) != 2 {
		t.Errorf("%d receipts of the run", len(own))
	}
	var status connectResult
	d.result(t, &status, append([]string{"connect"}, where...)...)
	if len(status.Sources) != 3 || status.Sources["crm"]["provider"] != "demo" {
		t.Errorf("the demo's workspace: %+v", status)
	}
	var account struct {
		Newest string `json:"newest_brief_id"`
	}
	if d.result(t, &account, append([]string{"inspect", "acc-003"}, where...)...); account.Newest != brief {
		t.Errorf("the newest card of acc-003 is %s, not %s", account.Newest, brief)
	}
	// A second run adds its own seven receipts and another card. The demo workspace of the web app holds
	// none of them.
	var again struct {
		Brief struct {
			BriefID string `json:"brief_id"`
		}
		Audit   struct{ Rows int }
		Created int `json:"activities_created"`
	}
	d.result(t, &again, append([]string{"demo"}, where...)...)
	if again.Brief.BriefID == brief || again.Audit.Rows != 7 || again.Created != 0 ||
		d.count(t, "SELECT count(*) FROM receipts") != 14 || d.count(t, "SELECT count(*) FROM cards") != 2 ||
		d.count(t, "SELECT count(*) FROM receipts WHERE workspace_id = $1", d.workspace.ID) != 0 {
		t.Errorf("a second run: %+v", again)
	}

	// A workspace whose calls are not the demo data is not the demo's.
	other, err := d.owner.EnsureWorkspace(ctx, "no-calls", "demo", "A demo workspace without calls")
	if err != nil {
		t.Fatal(err)
	}
	for source, mode := range map[string]string{"crm": "demo", "calls": "none", "slack": "demo"} {
		if _, err := d.owner.EnsureConnection(ctx, other, source, mode); err != nil {
			t.Fatal(err)
		}
	}
	if code, message, next := d.refusal(t, 2, "demo", "--state-dir", "no-calls"); code != "usage_error" ||
		message != "This workspace is connected to other sources. Give the demo its own --state-dir." ||
		next != "gtm-connector connect --help" {
		t.Errorf("demo beside other sources: %s, %s", message, next)
	}
}
