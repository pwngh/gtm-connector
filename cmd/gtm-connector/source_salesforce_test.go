package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// No test reaches the sf of this machine or the network: one that reads Salesforce gives the invented ones,
// and any other is refused as a login sf did not give.
func init() {
	salesforceOptions = salesforce.Options{Runner: unreachable{}, Transport: unreachable{}}
}

type unreachable struct{}

func (unreachable) Run(context.Context, []string) ([]byte, int, error) {
	return nil, 0, errors.New("a test ran sf without the invented one")
}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("a test sent a request without the invented org")
}

// recordedBook is one card of the books_salesforce family: the org rows it was read from, and the card.
type recordedBook struct {
	Key       string
	AccountID string `json:"account_id"`
	Options   struct {
		AsOf string `json:"as_of"`
	}
	Input struct {
		Rows json.RawMessage `json:"org_rows"`
	}
	Card string
}

// inventedSalesforce points the salesforce entry at the invented org and the invented sf until the test
// ends, with every account of the recorded Salesforce books in the org.
func inventedSalesforce(t *testing.T) (*salesforcetest.Org, *salesforcetest.SF, []recordedBook) {
	t.Helper()
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	var books []recordedBook
	for _, line := range oracletest.Lines(t, "cards/books_salesforce.jsonl.gz") {
		var book recordedBook
		if err := json.Unmarshal(line, &book); err != nil {
			t.Fatal(err)
		}
		rows, err := strictjson.Loads(book.Input.Rows)
		if err != nil || org.Add(rows.(*strictjson.Object)) != book.AccountID {
			t.Fatalf("%s: the recorded rows are not of %s: %v", book.Key, book.AccountID, err)
		}
		books = append(books, book)
	}
	saved := salesforceOptions
	salesforceOptions = salesforce.Options{Runner: sf, Transport: org}
	t.Cleanup(func() {
		salesforceOptions = saved
		if len(org.Unexpected) > 0 {
			t.Errorf("the org was sent requests it does not serve: %q", org.Unexpected)
		}
	})
	return org, sf, books
}

// debug runs a debug command line as a person does.
func (d *demoDatabase) debug(t *testing.T, tokens ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	_, asJSON := withoutJSON(tokens)
	var out, errs bytes.Buffer
	code = dispatch(ctx, append([]string{"debug", "salesforce"}, tokens...), screen{out: &out, err: &errs, json: asJSON})
	return code, out.String(), errs.String()
}

// checkReport is debug salesforce check's JSON report.
type checkReport struct {
	Status string
	AsOf   string `json:"as_of"`
	Steps  []struct {
		Name, Status, Code, Message, Fix string
		Count                            int
		APICalls                         int `json:"api_calls"`
		SFCommands                       int `json:"sf_cli_invocations"`
	}
	APICalls   int `json:"api_calls"`
	SFCommands int `json:"sf_cli_invocations"`
}

func (c checkReport) shown() string {
	var steps []string
	for _, step := range c.Steps {
		steps = append(steps, step.Name+":"+step.Status+":"+step.Code)
	}
	return strings.Join(steps, " ")
}

// elsewhere is the seam's refusal of an identity that is not the pinned one.
const elsewhere = "This workspace belongs to a different connection of this source, and keeps the accounts, cards " +
	"and receipts read through it. Connect the one it was first connected to, or connect this one in a " +
	"workspace of its own with --state-dir NAME. Nothing was changed."

// connect salesforce signs in through sf, checks the org and pins the workspace to it, and saves no
// credential, address or ID. accounts, inspect, brief and audit then read it with no further option, each
// read under a receipt; debug salesforce check reports the setup step by step; and removing it leaves a
// workspace that reads nothing, never the demo data. The same org connects again and another is refused.
func TestSalesforceIsReadByEveryCommand(t *testing.T) {
	d := setUpDemo(t)
	org, sf, _ := inventedSalesforce(t)
	connect := []string{"connect", "salesforce", "--org", salesforcetest.Alias, "--environment", "production",
		"--seller", "Morgan Reed", "--seller", "U0731P9Q2R"}
	where := []string{"--state-dir", "invented-books"}

	// The default workspace holds demo data, so Salesforce is connected in one named with --state-dir.
	if _, message, _ := d.refusal(t, 3, connect...); !strings.Contains(message, "--state-dir NAME") {
		t.Errorf("connect salesforce in the default workspace: %s", message)
	}
	code, stdout, stderr := d.read(t, append(connect, where...)...)
	if code != 0 || stderr != "" || stdout != "Salesforce connected. The org's identity and environment were checked.\n"+
		"crm\tconnected\tSalesforce org alias invented-books (production)\ncalls\tnot connected\t\n"+
		"slack\tnot connected\t\n" {
		t.Fatalf("connect: exit %d\n%s%s", code, stdout, stderr)
	}
	// connect ran the two login commands, checked the org and described the four objects. It read no record.
	if len(sf.Commands) != 2 || len(org.Requests) != 5 || strings.Contains(strings.Join(org.Requests, " "), "FROM Account") {
		t.Errorf("connect ran %d sf commands and sent %q", len(sf.Commands), org.Requests)
	}
	var status connectResult
	d.result(t, &status, append([]string{"connect"}, where...)...)
	source := status.Sources["crm"]
	if len(status.Sources) != 1 || source["provider"] != "salesforce" || source["org"] != salesforcetest.Alias ||
		source["environment"] != "production" || strings.Join(status.Sellers, ", ") != "Morgan Reed, U0731P9Q2R" {
		t.Fatalf("status %+v", status)
	}
	// The rows hold the alias and a digest. They hold no credential, org ID or address.
	var stored string
	if err := conn(t, d.ownerURL).QueryRow(ctx, `SELECT string_agg(c::text, ' ') || ' ' || string_agg(w::text, ' ')
		FROM connections c JOIN workspaces w ON w.id = c.workspace_id WHERE w.slug = 'invented-books' AND w.kind = 'live'
		 AND c.mode = 'sf'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{salesforcetest.Token, salesforcetest.OrganizationID, "salesforce.com"} {
		if strings.Contains(stored, secret) {
			t.Errorf("the saved rows hold %q", secret)
		}
	}

	code, stdout, stderr = d.read(t, append([]string{"accounts", "--query", "Gemini export 0"}, where...)...)
	if code != 0 || stderr != "Sources: CRM is Salesforce (production), identity verified.\n" ||
		!strings.HasPrefix(stdout, inventedAccount+"\tGemini export 0: Zoom cloud recording VTT\tExports\t") {
		t.Fatalf("accounts: exit %d\n%s%s", code, stdout, stderr)
	}
	var account struct {
		ID        string
		Contacts  []map[string]any
		Contracts []map[string]any
		Newest    *string `json:"newest_brief_id"`
	}
	d.result(t, &account, append([]string{"inspect", inventedAccount}, where...)...)
	if account.ID != inventedAccount || len(account.Contacts) != 1 || len(account.Contracts) != 1 || account.Newest != nil {
		t.Fatalf("inspect %s: %+v", inventedAccount, account)
	}
	if _, message, _ := d.refusal(t, 2, append([]string{"inspect", "acc-sf-001000000000000AAA"}, where...)...); message !=
		"account not found: acc-sf-001000000000000AAA" {
		t.Errorf("inspect of an account the org does not hold: %s", message)
	}

	code, stdout, stderr = d.read(t, append([]string{"brief", inventedAccount}, where...)...)
	lines := strings.Split(stdout, "\n")
	if code != 0 || stderr != "Sources: CRM is Salesforce (production), identity verified; calls are not connected; "+
		"Slack is not connected.\n" || lines[0] != `DEAL CARD: "Gemini export 0: Zoom cloud recording VTT" (`+
		inventedAccount+`) as of `+time.Now().UTC().Format("2006-01-02") ||
		!strings.Contains(stdout, "\n  Calls: not connected.\n") {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	brief := strings.TrimPrefix(lines[len(lines)-4], "Brief: ")
	workspace, err := d.owner.Workspace(ctx, "invented-books")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := d.owner.CardByBrief(ctx, workspace.ID, brief)
	if err != nil || !strings.Contains(string(payload.Bytes), `"Morgan Reed"`) ||
		!strings.Contains(string(payload.Bytes), `"crm_backend": "salesforce"`) {
		t.Errorf("the saved card does not hold the workspace's sellers and its CRM: %v", err)
	}
	if got := d.audited(t, "invented-books"); got != "search_accounts:ok:200: get_account:ok:200: "+
		"get_account:error:200:request_failed get_account:ok:200: get_account:ok:200: list_opportunities:ok:200: "+
		"save_brief:ok:200:" {
		t.Errorf("the receipts: %s", got)
	}

	// The check of a connected workspace reads its records under receipts, and prints none of them.
	code, stdout, stderr = d.debug(t, append([]string{"check", "--as-of", "2027-01-15"}, where...)...)
	if code != 0 || !strings.HasPrefix(stderr, "Salesforce credentials: running.\nSalesforce credentials: pass. Count: 2.\n") {
		t.Fatalf("check: exit %d\n%s%s", code, stdout, stderr)
	}
	for _, private := range []string{"acc-sf-", salesforcetest.OrganizationID, salesforcetest.Token, "my.salesforce.com",
		"Gemini"} {
		if strings.Contains(stdout+stderr, private) {
			t.Errorf("the check printed %q", private)
		}
	}
	var report checkReport
	if code, stdout, _ = d.debug(t, append([]string{"check", "--as-of", "2027-01-15", "--json"}, where...)...); code != 0 {
		t.Fatalf("check --json: exit %d", code)
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Status != "passed" || report.shown() !=
		"credentials:pass:ok environment:pass:ok account_fields:pass:ok contact_fields:pass:ok "+
			"opportunity_fields:pass:ok contract_fields:pass:ok account_search:pass:ok renewal_candidate:pass:ok "+
			"account_read:pass:ok opportunity_read:pass:ok" || report.SFCommands != 2 || report.Steps[0].SFCommands != 2 ||
		report.Steps[6].Count != 78 {
		t.Fatalf("check: %v %+v", err, report)
	}
	var found []receiptRow
	d.result(t, &found, append([]string{"audit", "--tool", "find_renewal_candidate"}, where...)...)
	if len(found) != 2 || found[0].Status != "ok" || len(found[0].Rows) != 1 ||
		!strings.HasPrefix(found[0].Rows[0], "crm/contracts/ctr-sf-") || !strings.HasPrefix(*found[0].BriefID, "check-") {
		t.Errorf("the candidate's receipts: %+v", found)
	}

	var removed connectResult
	d.result(t, &removed, append([]string{"connect", "salesforce", "--remove"}, where...)...)
	if _, message, _ := d.refusal(t, 2, append([]string{"accounts"}, where...)...); removed.Status != "removed" ||
		!strings.HasPrefix(message, "No CRM is connected. Run gtm-connector connect salesforce --org ALIAS") {
		t.Errorf("after the removal: %+v, then accounts: %s", removed, message)
	}
	// Another org behind the same alias is refused, also while the workspace is detached.
	org.ID, sf.OrganizationID = "00D000000000000AAA", "00D000000000000AAA"
	if _, message, _ := d.refusal(t, 3, append(connect, where...)...); message != elsewhere {
		t.Errorf("another org: %s", message)
	}
	org.ID, sf.OrganizationID = salesforcetest.OrganizationID, salesforcetest.OrganizationID
	if code, _, stderr := d.read(t, append(connect, where...)...); code != 0 {
		t.Fatalf("the same org again: exit %d\n%s", code, stderr)
	}
	// A command signs in on its first read and reads nothing from an org that is no longer the pinned one.
	org.ID, sf.OrganizationID = "00D000000000000AAA", "00D000000000000AAA"
	_, message, _ := d.refusal(t, 3, append([]string{"accounts"}, where...)...)
	if !strings.HasPrefix(message, "source_identity_mismatch: The org behind this alias is not the one this "+
		"workspace is pinned to") {
		t.Errorf("accounts from another org: %s", message)
	}
	code, stdout, _ = d.debug(t, append([]string{"check", "--json"}, where...)...)
	if err := json.Unmarshal([]byte(stdout), &report); code != 4 || err != nil ||
		!strings.HasPrefix(report.shown(), "credentials:pass:ok environment:fail:source_identity_mismatch "+
			"account_fields:fail:prerequisite_failed") {
		t.Errorf("check of another org: exit %d, %v %s", code, err, report.shown())
	}
}

// connect salesforce refuses, before anything is saved, what Python's refuses: no alias or a username, an
// org of another environment or edition, and a login sf cannot give. Each is its code and fixed text.
func TestConnectSalesforceRefusesWhatItCannotPin(t *testing.T) {
	d := setUpDemo(t)
	org, sf, _ := inventedSalesforce(t)
	for _, c := range []struct {
		name    string
		code    int
		tokens  []string
		message string
	}{
		{"no alias", 2, nil, orgAlias},
		{"a username", 2, []string{"--org", "someone@example.com"}, orgAlias},
		{"Developer Edition by default", 5, []string{"--org", salesforcetest.Alias},
			"salesforce_edition_required: The selected org did not report Developer Edition."},
		{"a sandbox", 3, []string{"--org", salesforcetest.Alias, "--environment", "sandbox"},
			"salesforce_environment_mismatch: The selected org does not match the expected Salesforce environment."},
		{"an alias sf does not hold", 5, []string{"--org", "unknown-alias", "--environment", "production"},
			"salesforce_auth_required: The sf CLI login or token refresh did not provide usable credentials."},
	} {
		tokens := append(append([]string{"connect", "salesforce"}, c.tokens...), "--state-dir", "refused-books")
		_, message, next := d.refusal(t, c.code, tokens...)
		if message != c.message || c.code != 2 && next != salesforceNext {
			t.Errorf("%s: %s (%s)", c.name, message, next)
		}
	}
	if _, err := d.owner.Workspace(ctx, "refused-books"); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("a refused connect made its workspace: %v", err)
	}
	if strings.Contains(strings.Join(org.Requests, " "), "FROM Account") || len(sf.Commands) != 5 {
		t.Errorf("sf ran %d commands, and the org was sent %q", len(sf.Commands), org.Requests)
	}
}

// Every recorded Salesforce card is made again by brief from the same org rows: the account, its contract,
// its contacts and its opportunities read the same and print the same. The recording read calls and Slack
// from demo data beside Salesforce, which this build never does, so those parts of each card differ: beside
// a real CRM, calls and Slack are not connected here.
func TestBriefMakesTheRecordedSalesforceCards(t *testing.T) {
	d := setUpDemo(t)
	_, _, books := inventedSalesforce(t)
	where := []string{"--state-dir", "invented-books"}
	d.result(t, &connectResult{}, append([]string{"connect", "salesforce", "--org", salesforcetest.Alias,
		"--environment", "production"}, where...)...)
	type card struct {
		Text  string
		Units []map[string]any
	}
	crmOf := func(c card) (string, []map[string]any) {
		sections := strings.Split(c.Text, "\n\n")
		var shown []string
		shown = append(shown, strings.SplitN(sections[0], "\n", 2)[0])
		for _, section := range sections {
			if strings.HasPrefix(section, "RECORD VALUES") {
				shown = append(shown, section)
			}
			if strings.HasPrefix(section, "NOT READ") {
				for _, line := range strings.Split(section, "\n") {
					if strings.HasPrefix(line, "  CRM:") {
						shown = append(shown, line)
					}
				}
			}
		}
		var units []map[string]any
		for _, unit := range c.Units {
			if unit["source"] == "crm" {
				units = append(units, unit)
			}
		}
		return strings.Join(shown, "\n"), units
	}
	for _, book := range books {
		var recorded, made card
		if err := json.Unmarshal([]byte(book.Card), &recorded); err != nil {
			t.Fatal(err)
		}
		d.result(t, &made, append([]string{"brief", book.AccountID, "--as-of", book.Options.AsOf}, where...)...)
		wantText, wantUnits := crmOf(recorded)
		gotText, gotUnits := crmOf(made)
		if gotText != wantText || !reflect.DeepEqual(gotUnits, wantUnits) || len(wantUnits) == 0 {
			t.Errorf("%s:\n%s\nrecorded:\n%s", book.Key, gotText, wantText)
		}
	}
	if saved := d.count(t, `SELECT count(*) FROM cards c JOIN workspaces w ON w.id = c.workspace_id
		WHERE w.slug = 'invented-books'`); saved != len(books) || len(books) != 78 {
		t.Errorf("%d cards were saved of %d", saved, len(books))
	}
}

// Before an org is connected, debug salesforce check --sf-org checks the sign-in, the environment and each
// object's fields, and names the fields an object does not expose. It reads no record and no workspace. A
// failed report is printed, then the check fails with exit 4. In a connected workspace with no contract
// ending on or after the planning date, the check says so, and the reads of its account are not run.
func TestDebugSalesforceCheckReportsEachStep(t *testing.T) {
	d := setUpDemo(t)
	org, _, _ := inventedSalesforce(t)
	before := []string{"check", "--sf-org", salesforcetest.Alias, "--sf-environment", "production", "--json"}
	code, stdout, stderr := d.debug(t, before...)
	var report checkReport
	if err := json.Unmarshal([]byte(stdout), &report); code != 0 || err != nil || report.Status != "passed" ||
		report.shown() != "credentials:pass:ok environment:pass:ok account_fields:pass:ok contact_fields:pass:ok "+
			"opportunity_fields:pass:ok contract_fields:pass:ok" || report.APICalls != 5 || report.SFCommands != 2 {
		t.Fatalf("check --sf-org: exit %d, %v %+v\n%s", code, err, report, stderr)
	}
	if !strings.HasPrefix(stderr, `{"progress": {"command": "debug salesforce check", "step": "credentials", `+
		`"status": "running", "count": null}}`+"\n") || strings.Contains(strings.Join(org.Requests, " "), "FROM Account") {
		t.Errorf("progress:\n%s\nrequests %q", stderr, org.Requests)
	}
	org.Intercept = func(request *http.Request) *http.Response {
		if strings.HasSuffix(request.URL.Path, "/Contact/describe") {
			return salesforcetest.Respond(200, `{"name": "Contact", "queryable": true, "fields": [{"name": "Id"}, `+
				`{"name": "AccountId"}, {"name": "Name"}, {"name": "Title"}]}`)
		}
		return nil
	}
	code, stdout, stderr = d.debug(t, before...)
	if err := json.Unmarshal([]byte(stdout), &report); code != 4 || err != nil || report.Status != "failed" ||
		report.Steps[3].Code != "missing_fields" || report.Steps[3].Message != "Contact does not expose required fields: Email." ||
		report.Steps[4].Status != "pass" || !strings.HasSuffix(stderr, `{"error": {"code": "check_failed", "message": `+
		`"Salesforce checks failed. Apply the fix beside each failed step.", "next": "`+salesforceNext+`"}}`+"\n") {
		t.Errorf("a missing field: exit %d, %v %+v\n%s", code, err, report, stderr)
	}
	org.Intercept = nil
	for _, c := range [][]string{
		{"check", "--sf-org", salesforcetest.Alias, "--state-dir", "invented-books"},
		{"check", "--sf-environment", "production", "--state-dir", "invented-books"},
		{"check", "--sf-org", "someone@example.com"},
		{"check", "--as-of", "2027-02-30", "--sf-org", salesforcetest.Alias},
		{"check"},
		{},
	} {
		if code, _, stderr := d.debug(t, c...); code != 2 {
			t.Errorf("%v: exit %d\n%s", c, code, stderr)
		}
	}

	where := []string{"--state-dir", "invented-books"}
	d.result(t, &connectResult{}, append([]string{"connect", "salesforce", "--org", salesforcetest.Alias,
		"--environment", "production"}, where...)...)
	code, stdout, _ = d.debug(t, append([]string{"check", "--as-of", "2099-01-01", "--json"}, where...)...)
	if err := json.Unmarshal([]byte(stdout), &report); code != 4 || err != nil || !strings.HasSuffix(report.shown(),
		"account_search:pass:ok renewal_candidate:fail:renewal_fields_unmapped account_read:fail:prerequisite_failed "+
			"opportunity_read:fail:prerequisite_failed") {
		t.Errorf("no candidate: exit %d, %v %s", code, err, report.shown())
	}
	if got := d.audited(t, "invented-books"); got != "search_accounts:ok:200: find_renewal_candidate:ok:200:" {
		t.Errorf("the receipts: %s", got)
	}
}

// A cancel while connect waits on sf ends sf and everything it started, and saves nothing.
func TestACancelEndsTheSFProcessGroup(t *testing.T) {
	d := setUpDemo(t)
	folder := t.TempDir()
	script := "#!/bin/sh\nfolder=$(dirname \"$0\")\nsleep 600 &\n" +
		"echo \"$$ $!\" > \"$folder/pids.part\" && mv \"$folder/pids.part\" \"$folder/pids\"\nwait\n"
	if err := os.WriteFile(filepath.Join(folder, "sf"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", folder+string(os.PathListSeparator)+os.Getenv("PATH"))
	saved := salesforceOptions
	salesforceOptions = salesforce.Options{}
	t.Cleanup(func() { salesforceOptions = saved })

	interrupted, cancel := context.WithCancel(ctx)
	defer cancel()
	var pids []int
	go func() {
		defer cancel()
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			written, err := os.ReadFile(filepath.Join(folder, "pids"))
			if err != nil {
				continue
			}
			for _, field := range strings.Fields(string(written)) {
				pid, _ := strconv.Atoi(field)
				pids = append(pids, pid)
			}
			return
		}
	}()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	var out, errs bytes.Buffer
	code := runCommand(interrupted, "connect", []string{"salesforce", "--org", salesforcetest.Alias, "--state-dir",
		"cancelled-books", "--json"}, screen{out: &out, err: &errs, json: true})
	if code != 130 || out.Len() != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errs.String())
	}
	if len(pids) != 2 || pids[0] <= 1 || pids[1] <= 1 {
		t.Fatalf("sf did not start: %v", pids)
	}
	running := func(pid int) bool { return syscall.Kill(pid, 0) != syscall.ESRCH }
	for deadline := time.Now().Add(10 * time.Second); running(pids[1]) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if running(pids[0]) || running(pids[1]) {
		t.Errorf("sf runs: %t; its child runs: %t", running(pids[0]), running(pids[1]))
	}
	if _, err := d.owner.Workspace(ctx, "cancelled-books"); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("the cancelled connect made its workspace: %v", err)
	}
}
