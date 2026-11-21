package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// connectResult is a result of connect.
type connectResult struct {
	Provider, Status string
	Sources          map[string]map[string]any
	Sellers          []string
}

// connect demo makes a workspace and connects it to the demo data, status shows it, and --remove detaches
// it. Each change moves the connections to their next generation. Nothing of the workspace is deleted.
func TestConnectDemoStatusAndRemove(t *testing.T) {
	d := setUpDemo(t)
	const name = "var/renewals of 2027"
	where := []string{"--state-dir", name}
	run := func(tokens ...string) connectResult {
		t.Helper()
		var result connectResult
		d.result(t, &result, append(append([]string{"connect"}, tokens...), where...)...)
		return result
	}
	generations := func() int {
		return d.count(t, "SELECT coalesce(sum(generation), 0) FROM connections c JOIN workspaces w ON w.id = c.workspace_id "+
			"WHERE w.slug = $1", workspaceSlug(name))
	}

	// A workspace that does not exist is connected to nothing, and neither question makes it.
	if status := run(); len(status.Sources) != 0 || status.Sellers == nil || len(status.Sellers) != 0 {
		t.Fatalf("status of no workspace: %+v", status)
	}
	if removed := run("demo", "--remove"); removed.Status != "not_connected" || len(removed.Sources) != 0 {
		t.Fatalf("remove from no workspace: %+v", removed)
	}
	if n := d.count(t, "SELECT count(*) FROM workspaces WHERE slug <> 'demo'"); n != 0 {
		t.Fatalf("%d workspaces were made by asking", n)
	}

	code, stdout, stderr := d.read(t, append([]string{"connect", "demo"}, where...)...)
	if code != 0 || stderr != "" || stdout != "Demo data connected.\ncrm\tconnected\tdemo data\n"+
		"calls\tconnected\tdemo data\nslack\tconnected\tdemo data\n" {
		t.Fatalf("connect demo: exit %d\n%s%s", code, stdout, stderr)
	}
	if n := d.count(t, "SELECT count(*) FROM connections c JOIN workspaces w ON w.id = c.workspace_id WHERE w.slug = $1 "+
		"AND w.kind = 'demo' AND c.mode = 'demo' AND c.generation = 1 AND c.binding = '{}' AND c.identity_sha256 IS NULL "+
		"AND position($2 in w.display_name) = 0", workspaceSlug(name), name); n != 3 {
		t.Fatalf("%d of the 3 connections are demo data in a demo workspace", n)
	}
	// Connecting again changes nothing, and lists the sources as status does.
	_, stdout, _ = d.read(t, append([]string{"connect", "demo", "--json"}, where...)...)
	if calls, crm := strings.Index(stdout, `"calls"`), strings.Index(stdout, `"crm"`); calls < 0 || crm < calls || generations() != 3 {
		t.Fatalf("connect demo again, at generations %d:\n%s", generations(), stdout)
	}
	code, stdout, _ = d.read(t, append([]string{"connect"}, where...)...)
	if code != 0 || stdout != "crm\tconnected\tdemo data\ncalls\tconnected\tdemo data\nslack\tconnected\tdemo data\n"+
		"Sellers: none listed\n" {
		t.Fatalf("connect status: exit %d\n%s", code, stdout)
	}
	var first savedCard
	d.result(t, &first, append([]string{"brief", "acc-003"}, where...)...)

	// Beside the demo CRM, calls are demo data: there are no transcripts to detach.
	if removed := run("calls", "--remove"); removed.Status != "not_connected" || len(removed.Sources) != 3 || generations() != 3 {
		t.Fatalf("connect calls --remove: %+v", removed)
	}
	// Removing the CRM removes its calls and Slack, and keeps the workspace's cards and receipts.
	code, stdout, _ = d.read(t, append([]string{"connect", "demo", "--remove"}, where...)...)
	if code != 0 || stdout != "Demo data removed. Its cards and receipts are kept.\ncrm\tnot connected\t\n"+
		"calls\tnot connected\t\nslack\tnot connected\t\n" || generations() != 6 {
		t.Fatalf("connect demo --remove: exit %d, generations %d\n%s", code, generations(), stdout)
	}
	if status := run(); len(status.Sources) != 0 {
		t.Fatalf("status after the removal: %+v", status)
	}
	if _, message, next := d.refusal(t, 2, append([]string{"accounts"}, where...)...); next != "gtm-connector connect --help" ||
		message != "No CRM is connected. Run gtm-connector connect demo --state-dir 'var/renewals of 2027' for invented data." {
		t.Fatalf("accounts after the removal: %s", message)
	}
	var kept []receiptRow
	if d.result(t, &kept, append([]string{"audit"}, where...)...); len(kept) != 5 {
		t.Fatalf("%d receipts are kept", len(kept))
	}
	code, stdout, _ = d.read(t, append([]string{"connect", "demo", "--remove"}, where...)...)
	if code != 0 || !strings.HasPrefix(stdout, "Demo data was not connected. Nothing changed.\n") || generations() != 6 {
		t.Fatalf("a second removal: exit %d\n%s", code, stdout)
	}

	// Connected again, the workspace makes a card and still opens the one it made before.
	if again := run("demo"); again.Status != "connected" || again.Sources["crm"]["provider"] != "demo" || generations() != 9 {
		t.Fatalf("connect demo after a removal: %+v", again)
	}
	var second savedCard
	d.result(t, &second, append([]string{"brief", "acc-003"}, where...)...)
	for _, brief := range []string{first.BriefID, second.BriefID} {
		if code, _, stderr := d.read(t, append([]string{"inspect", brief}, where...)...); code != 0 {
			t.Errorf("inspect %s: exit %d\n%s", brief, code, stderr)
		}
	}
}

// connect knows salesforce, calls, slack and the assistant apps by name and refuses each plainly, before
// it opens the database. Nothing is connected, and no later command reads demo data in a refused source's
// place.
func TestConnectRefusesWhatThisBuildDoesNotConnect(t *testing.T) {
	t.Setenv("GTM_DATABASE_URL", "")
	refuse := func(code int, tokens ...string) string {
		t.Helper()
		var out, errs bytes.Buffer
		if got := runCommand(ctx, "connect", tokens, screen{out: &out, err: &errs}); got != code || out.Len() != 0 {
			t.Fatalf("connect %v: exit %d, not %d\n%s%s", tokens, got, code, out.String(), errs.String())
		}
		return errs.String()
	}
	for _, tokens := range [][]string{{"salesforce", "--org", "invented-alias"}, {"calls"}, {"slack", "--seller", "Dana Crest"},
		{"inspector", "--scope", "crm:read"}, {"cursor"}} {
		want := "Error: connect " + tokens[0] + " is not available in this build. "
		if message := refuse(3, append(tokens, "--state-dir", "invented")...); !strings.HasPrefix(message, want) ||
			!strings.HasSuffix(message, "\nNext: gtm-connector connect --help\n") {
			t.Errorf("connect %v: %s", tokens, message)
		}
	}
	long := strings.Repeat("é", 201)
	many := []string{"calls"}
	for range 201 {
		many = append(many, "--seller", "Dana Crest")
	}
	const sellers = "Error: Name at most 200 sellers with --seller, each a name or Slack ID of 1 to 200 characters.\n"
	for _, c := range []struct {
		tokens []string
		want   string
	}{
		{[]string{"demo", "--seller", "Dana Crest"}, "Error: connect demo does not take --seller.\n"},
		{[]string{"--org", "invented-alias", "--seller", "Dana Crest"}, "Error: connect status does not take --org, --seller.\n"},
		{[]string{"status", "--remove"}, "Error: connect status does not take --remove.\n"},
		{[]string{"calls", "--remove", "--seller", "Dana Crest"}, "Error: --remove detaches a source and takes no other option.\n"},
		{[]string{"cursor", "--remove"}, "Error: connect cursor does not take --remove.\n"},
		{[]string{"slack", "--seller", ""}, sellers},
		{[]string{"salesforce", "--org", "invented-alias", "--seller", long}, sellers},
		{[]string{"salesforce", "--seller", "bad\xffname"}, sellers},
		{many, sellers},
		{[]string{"hubspot"}, "Error: argument provider: invalid choice (choose from 'status', 'demo', 'salesforce', 'calls', " +
			"'slack', 'inspector', 'claude-code', 'claude-desktop', 'cursor')\n"},
		{[]string{"demo", "calls"}, "Error: unrecognized arguments\n"},
		{[]string{"--environment", "staging"}, "Error: argument --environment: invalid choice (choose from 'developer', " +
			"'sandbox', 'production')\n"},
	} {
		if message := refuse(2, c.tokens...); !strings.HasPrefix(message, c.want) {
			t.Errorf("connect %v: %s", c.tokens[:min(4, len(c.tokens))], message)
		}
	}
	// The longest list and the longest name a workspace may hold are not refused for their size.
	most := slices.Repeat([]string{"Dana Crest"}, 200)
	if !validSellers(most) || validSellers(append(most, "Dana Crest")) || !validSellers([]string{strings.Repeat("é", 200)}) {
		t.Error("the bounds of a seller list are not 200 entries of 1 to 200 characters")
	}

	d := setUpDemo(t)
	if _, message, _ := d.refusal(t, 3, "connect", "salesforce", "--org", "invented-alias", "--state-dir", "invented"); !strings.Contains(message, "not available") {
		t.Fatal(message)
	}
	if n := d.count(t, "SELECT (SELECT count(*) FROM workspaces WHERE slug <> 'demo') + (SELECT count(*) FROM connections)"); n != 0 {
		t.Fatalf("a refused connect wrote %d rows", n)
	}
	for _, tokens := range [][]string{{"accounts"}, {"brief", "acc-003"}, {"inspect", "acc-003"}} {
		if _, message, _ := d.refusal(t, 2, append(tokens, "--state-dir", "invented")...); !strings.HasPrefix(message, "No CRM is connected.") {
			t.Errorf("%v after a refused connect: %s", tokens, message)
		}
	}

	// A workspace of real sources never takes the demo data, and its rows can still be detached.
	live, err := d.owner.EnsureWorkspace(ctx, "live-one", "live", "A live workspace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.owner.EnsureConnection(ctx, live, "crm", "sf"); err != nil {
		t.Fatal(err)
	}
	if code, message, _ := d.refusal(t, 3, "connect", "demo", "--state-dir", "live-one"); code != "refused" ||
		!strings.HasPrefix(message, "This workspace reads real sources, and demo data never shares a workspace with them.") {
		t.Errorf("connect demo in a live workspace: %s", message)
	}
	if _, message, _ := d.refusal(t, 2, "demo", "--state-dir", "live-one"); message !=
		"This workspace is connected to other sources. Give the demo its own --state-dir." {
		t.Errorf("demo in a live workspace: %s", message)
	}
	var status, removed connectResult
	d.result(t, &status, "connect", "--state-dir", "live-one")
	d.result(t, &removed, "connect", "salesforce", "--remove", "--state-dir", "live-one")
	if status.Sources["crm"]["provider"] != "salesforce" || removed.Status != "removed" || len(removed.Sources) != 0 {
		t.Errorf("a Salesforce row: %+v, then %+v", status, removed)
	}
	if n := d.count(t, "SELECT (SELECT count(*) FROM receipts) + (SELECT count(*) FROM accounts) + (SELECT count(*) FROM cards)"); n != 0 {
		t.Errorf("the refused commands wrote %d rows", n)
	}
}
