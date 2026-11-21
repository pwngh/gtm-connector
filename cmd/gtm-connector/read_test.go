package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// read runs one read command as main does, connecting as the app role.
func (d *demoDatabase) read(t *testing.T, tokens ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	rest, asJSON := withoutJSON(tokens)
	var out, errs bytes.Buffer
	code = runCommand(ctx, rest[0], rest[1:], screen{out: &out, err: &errs, json: asJSON})
	return code, out.String(), errs.String()
}

// result is a command's JSON result, which must have succeeded.
func (d *demoDatabase) result(t *testing.T, into any, tokens ...string) {
	t.Helper()
	code, stdout, stderr := d.read(t, append(tokens, "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v: exit %d\n%s", tokens, code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), into); err != nil {
		t.Fatalf("%v: %v\n%s", tokens, err, stdout)
	}
}

// refusal is a command's JSON error, which must have failed with this exit code.
func (d *demoDatabase) refusal(t *testing.T, code int, tokens ...string) (name, message, next string) {
	t.Helper()
	got, stdout, stderr := d.read(t, append(tokens, "--json")...)
	var refused struct {
		Error struct{ Code, Message, Next string }
	}
	if err := json.Unmarshal([]byte(stderr), &refused); err != nil || got != code || stdout != "" {
		t.Fatalf("%v: exit %d, not %d\n%s%s", tokens, got, code, stdout, stderr)
	}
	return refused.Error.Code, refused.Error.Message, refused.Error.Next
}

type savedCard struct {
	BriefID  string `json:"brief_id"`
	SealedBy string `json:"sealed_by"`
	Sources  map[string]string
	Gaps     []struct{ Code string }
}

type receiptRow struct {
	AuditID    string   `json:"audit_id"`
	StartedAt  string   `json:"started_at"`
	TokenID    string   `json:"token_id"`
	Tool       string   `json:"tool"`
	BriefID    *string  `json:"brief_id"`
	Status     string   `json:"status"`
	HTTPStatus *int     `json:"http_status"`
	Rows       []string `json:"rows"`
	Error      *string  `json:"error"`
}

// After setup and demo, the four commands read the demo workspace with no option: the accounts, a card,
// its units, a number on it, its seal, and its reads. The card brief makes is the owner's, and the demo
// sign-in reads it through its grant on the account.
func TestReadCommandsAfterSetupAndDemo(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := d.read(t, "accounts")
	lines := strings.Split(stdout, "\n")
	if code != 0 || stderr != "" || len(lines) != 31 ||
		lines[0] != "acc-001\tAlder Vale Packaging\tManufacturing\t2027-02-01" || lines[30] != "" {
		t.Fatalf("accounts: exit %d\n%s%s", code, stdout, stderr)
	}

	code, stdout, stderr = d.read(t, "brief", "acc-003")
	card := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	brief := strings.TrimPrefix(card[len(card)-3], "Brief: ")
	if code != 0 || stderr != "Sources: CRM, calls, and Slack are demo data.\n" ||
		card[0] != `DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15` || !briefID.MatchString(brief) ||
		card[len(card)-2] != "List every unit: gtm-connector inspect "+brief+" --state-dir demo" ||
		card[len(card)-1] != "Resolve a number: gtm-connector inspect "+brief+" N --state-dir demo" {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	var newest struct {
		Contacts []struct{ Name string }
		Newest   string `json:"newest_brief_id"`
	}
	d.result(t, &newest, "inspect", "acc-003")
	if newest.Newest != brief || len(newest.Contacts) != 1 || newest.Contacts[0].Name != "Marian Holt" {
		t.Errorf("inspect acc-003: %+v, and the newest card is %s", newest, brief)
	}

	_, stdout, _ = d.read(t, "inspect", brief)
	units := strings.Split(stdout, "\n")
	if len(units) != 10 || units[0] != "1  record  accounts  acc-003" ||
		units[5] != `6  calls  2027-01-12T15:00:00Z  customer  "Marian Holt"  shown  "Please do not put a date on the order form."` {
		t.Errorf("inspect %s:\n%s", brief, stdout)
	}
	_, stdout, _ = d.read(t, "inspect", brief, "5", "1")
	if tables := strings.Split(stdout, "\n\n"); len(tables) != 2 ||
		!strings.HasPrefix(tables[0], "Number\t5\nKind\tsentence\nRecord\tcalls/call_notes/note-003b\nReceipt\t") ||
		!strings.Contains(tables[0], " (read_call_notes, ok)\n") || !strings.Contains(tables[0], "\nNext by this speaker\t6\n") ||
		!strings.Contains(tables[1], "\nname\t\"Thornfield Wren LLP\"\n") || !strings.HasSuffix(tables[1], "\nrisk_reason\tMissing\nrisk_status\t\"healthy\"\n") {
		t.Errorf("inspect %s 5 1:\n%s", brief, stdout)
	}

	var reads []receiptRow
	d.result(t, &reads, "audit", "--brief", brief)
	tools := ""
	for _, read := range reads {
		tools += read.Tool + " "
		if read.Status != "ok" || read.TokenID != reads[0].TokenID || len(read.TokenID) != 64 {
			t.Errorf("a read of %s: %+v", brief, read)
		}
	}
	if tools != "get_account list_opportunities read_call_notes read_slack_messages " {
		t.Errorf("the reads of %s: %s", brief, tools)
	}
	if n := d.count(t, "SELECT count(*) FROM receipts WHERE brief_id = $1 AND principal_id IS NULL", brief); n != 4 {
		t.Errorf("%d of the card's reads name no principal", n)
	}
	var seals []receiptRow
	d.result(t, &seals, "audit", "--tool", "save_brief", "--caller", reads[0].TokenID)
	stored, err := d.owner.CardByBrief(ctx, d.workspace.ID, brief)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(stored.Bytes)
	row := "briefs/" + brief + "/" + hex.EncodeToString(digest[:])
	if len(seals) != 1 || len(seals[0].Rows) != 1 || seals[0].Rows[0] != row || seals[0].BriefID != nil {
		t.Fatalf("the seal of %s: %+v", brief, seals)
	}
	_, stdout, _ = d.read(t, "inspect", seals[0].AuditID)
	if want := "\tsave_brief\t" + reads[0].TokenID + "\tok\t"; !strings.Contains(stdout, want) ||
		!strings.HasSuffix(stdout, "\n\nArguments\n{\"account_id\": \"acc-003\", \"brief_id\": \""+brief+"\"}\n\nReturned rows\n"+row+"\n") {
		t.Errorf("inspect %s:\n%s", seals[0].AuditID, stdout)
	}

	var listed struct {
		Cards []struct {
			BriefID string `json:"brief_id"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(signInToDemo(t, d.owner).get("/api/v1/accounts/acc-003/cards"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Cards) != 2 || listed.Cards[0].BriefID != brief {
		t.Errorf("the demo sign-in sees %+v, and brief made %s", listed.Cards, brief)
	}
}

// A workspace is named by --state-dir, which need not be a slug. Without calls:read the read of the call
// notes is refused with its receipt, and the card says so.
func TestBriefWithoutCallsInANamedWorkspace(t *testing.T) {
	d := setUpDemo(t)
	const name = "var/it's here"
	workspace := d.connectDemo(t, name)
	if workspace.Slug == name || !slugPattern.MatchString(workspace.Slug) {
		t.Fatalf("the workspace of %q is %q", name, workspace.Slug)
	}
	var card savedCard
	d.result(t, &card, "brief", "acc-007", "--without-calls", "--state-dir", name)
	denied := false
	for _, gap := range card.Gaps {
		denied = denied || gap.Code == "calls_denied"
	}
	if !denied || card.Sources["calls"] != "demo_fixture" {
		t.Errorf("the card without calls: %+v", card)
	}
	var refused []receiptRow
	d.result(t, &refused, "audit", "--status", "error", "--state-dir", name)
	if len(refused) != 1 || refused[0].Tool != "read_call_notes" || *refused[0].HTTPStatus != 403 ||
		*refused[0].Error != "request_failed" || len(refused[0].Rows) != 0 || *refused[0].BriefID != card.BriefID {
		t.Errorf("the refused read: %+v", refused)
	}
	// The demo workspace holds none of it: not the receipts, the card, or its seal.
	var none []receiptRow
	if d.result(t, &none, "audit"); len(none) != 0 {
		t.Errorf("the demo workspace holds %d receipts", len(none))
	}
	d.connectDemo(t, defaultWorkspace)
	if _, message, _ := d.refusal(t, 2, "inspect", card.BriefID); message != "No saved card has this brief ID in this state folder." {
		t.Errorf("another workspace's card: %s", message)
	}
	if _, message, _ := d.refusal(t, 2, "inspect", card.SealedBy); message != "Audit receipt not found." {
		t.Errorf("another workspace's receipt: %s", message)
	}
	_, stdout, _ := d.read(t, "inspect", "acc-007", "--state-dir", name)
	if want := "\n\nNewest card: " + card.BriefID + "  (gtm-connector inspect " + card.BriefID +
		` --state-dir 'var/it'"'"'s here')` + "\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("inspect acc-007:\n%s", stdout)
	}
	if code, message, next := d.refusal(t, 2, "inspect", "acc-999", "--state-dir", name); code != "usage_error" ||
		message != "account not found: acc-999" || next != `gtm-connector accounts --state-dir 'var/it'"'"'s here'` {
		t.Errorf("inspect acc-999: %s, %s", message, next)
	}
	if code, message, _ := d.refusal(t, 5, "brief", "acc-999", "--state-dir", name); code != "upstream_failure" ||
		message != "get_account: invalid or failed tool result; no brief generated" {
		t.Errorf("brief acc-999: %s, %s", code, message)
	}
	if code, message, _ := d.refusal(t, 2, "inspect", card.BriefID, "0", "--state-dir", name); code != "usage_error" ||
		message != "This card has numbers 1 to 8." {
		t.Errorf("inspect a number off the card: %s", message)
	}
}

// A workspace with no CRM, or none at all, gives no account and makes no card, and nothing is written. A
// source the read commands do not read is refused by name and never read as demo data.
func TestReadCommandsRefuseWhatIsNotConnected(t *testing.T) {
	d := setUpDemo(t)
	const advice = "No CRM is connected. Run gtm-connector connect salesforce --org ALIAS%[1]s, or gtm-connector " +
		"connect demo%[2]s for invented data."
	for _, tokens := range [][]string{{"accounts"}, {"brief", "acc-003"}, {"inspect", "acc-003"},
		{"brief", "not an account"}} {
		if code, message, next := d.refusal(t, 2, tokens...); code != "usage_error" ||
			message != fmt.Sprintf(advice, " --state-dir NAME", "") || next != "gtm-connector connect --help" {
			t.Errorf("%v: %s, %s", tokens, message, next)
		}
	}
	if _, message, _ := d.refusal(t, 2, "accounts", "--state-dir", "other"); message !=
		fmt.Sprintf(advice, " --state-dir other", " --state-dir other") {
		t.Errorf("accounts in a workspace that does not exist: %s", message)
	}
	if code, stdout, stderr := d.read(t, "audit", "--json"); code != 0 || stdout != "[]\n" || stderr != "" {
		t.Errorf("audit of a workspace with no receipt: exit %d\n%s%s", code, stdout, stderr)
	}
	missing := "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct {
		tokens        []string
		message, next string
	}{
		{[]string{"audit", "--state-dir", "no where"}, "audit database not found; run the demo first",
			"gtm-connector demo --state-dir 'no where'"},
		{[]string{"inspect", missing, "--state-dir", "other"}, "audit database not found; run the demo first",
			"gtm-connector demo --state-dir other"},
		{[]string{"inspect", missing}, "Audit receipt not found.", "gtm-connector inspect --help"},
		{[]string{"inspect", "brief-" + strings.Repeat("0", 32)}, "No saved card has this brief ID in this state folder.",
			"gtm-connector inspect --help"},
		{[]string{"inspect", "brief-" + strings.Repeat("0", 32), "1", "--state-dir", "other"},
			"No saved card has this brief ID in this state folder.", "gtm-connector inspect --help"},
	} {
		if _, message, next := d.refusal(t, 2, c.tokens...); message != c.message || next != c.next {
			t.Errorf("%v: %s, %s", c.tokens, message, next)
		}
	}

	live, err := d.owner.EnsureWorkspace(ctx, "live-one", "live", "A live workspace")
	if err != nil {
		t.Fatal(err)
	}
	// A Salesforce token handed to the container is not read in this build.
	if _, err := d.owner.EnsureConnection(ctx, live, "crm", "env_token"); err != nil {
		t.Fatal(err)
	}
	for _, tokens := range [][]string{{"accounts"}, {"brief", "acc-003"}, {"inspect", "acc-003"}} {
		code, message, _ := d.refusal(t, 3, append(tokens, "--state-dir", "live-one")...)
		if code != "refused" || message != "This workspace is connected to a Salesforce token handed to the container, "+
			"which this command does not read yet." {
			t.Errorf("%v on Salesforce: %s, %s", tokens, code, message)
		}
	}
	if n := d.count(t, "SELECT (SELECT count(*) FROM receipts) + (SELECT count(*) FROM accounts) + (SELECT count(*) FROM cards)"); n != 0 {
		t.Errorf("the refused commands wrote %d rows", n)
	}
}

// A demo workspace whose calls are not connected reads no call, and its card says so.
func TestBriefWithCallsNotConnected(t *testing.T) {
	d := setUpDemo(t)
	workspace, err := d.owner.EnsureWorkspace(ctx, "no-calls", "demo", "A demo workspace without calls")
	if err != nil {
		t.Fatal(err)
	}
	for source, mode := range map[string]string{"crm": "demo", "calls": "none", "slack": "demo"} {
		if _, err := d.owner.EnsureConnection(ctx, workspace, source, mode); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := d.read(t, "brief", "acc-003", "--state-dir", "no-calls")
	if code != 0 || stderr != "Sources: CRM is demo data; calls are not connected; Slack is demo data.\n" ||
		!strings.Contains(stdout, "\n  Calls: not connected.\n") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	var reads []receiptRow
	d.result(t, &reads, "audit", "--state-dir", "no-calls")
	for _, read := range reads {
		if read.Tool == "read_call_notes" {
			t.Errorf("a call read has a receipt: %+v", read)
		}
	}
	var card struct{ Units []any }
	brief := strings.TrimPrefix(strings.Split(stdout[strings.Index(stdout, "\nBrief: ")+1:], "\n")[0], "Brief: ")
	if d.result(t, &card, "inspect", brief, "--state-dir", "no-calls"); len(card.Units) == 0 {
		t.Errorf("inspect %s lists no unit", brief)
	}
}

func TestAuditFilters(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	var first, second savedCard
	d.result(t, &first, "brief", "acc-003")
	d.result(t, &second, "brief", "acc-007")
	var all, found []receiptRow
	if d.result(t, &all, "audit"); len(all) != 10 || all[4].AuditID != first.SealedBy || all[9].AuditID != second.SealedBy {
		t.Fatalf("the log: %+v", all)
	}
	for _, c := range []struct {
		want   int
		tokens []string
	}{
		{2, []string{"--tool", "save_brief"}}, {0, []string{"--tool", "save_brie"}},
		{5, []string{"--caller", all[9].TokenID}}, {0, []string{"--caller", "anonymous"}},
		{10, []string{"--status", "ok"}}, {0, []string{"--status", "pending"}},
		{4, []string{"--brief", second.BriefID}}, {0, []string{"--brief", ""}},
		{1, []string{"--brief", first.BriefID, "--tool", "read_slack_messages", "--status", "ok"}},
		// At or after --since and before --until, whatever offset either is written with.
		{5, []string{"--since", all[5].StartedAt}}, {5, []string{"--until", all[5].StartedAt}},
		{1, []string{"--since", all[4].StartedAt, "--until", all[5].StartedAt}},
		{0, []string{"--since", "9999-01-01T00:00:00+00:00"}}, {10, []string{"--since", "2000-01-01T00:00:00-08:00"}},
	} {
		if d.result(t, &found, append([]string{"audit"}, c.tokens...)...); len(found) != c.want {
			t.Errorf("audit %v lists %d receipts, not %d", c.tokens, len(found), c.want)
		}
	}
	// A filter is refused before anything is read, so also where there is no workspace.
	for message, tokens := range map[string][]string{
		"Receipt filters must be nonempty text.":                                            {"--tool", " "},
		"--caller must be a complete SHA-256 fingerprint or anonymous, not a bearer token.": {"--caller", all[0].TokenID[:63]},
		"--since must include its UTC offset.":                                              {"--since", "2027-01-01"},
		"--until must include its UTC offset.":                                              {"--until", "2027-01-01T10:00:00"},
		"--since must be an ISO 8601 timestamp with a UTC offset.":                          {"--since", "yesterday"},
		"--since must be earlier than --until.":                                             {"--since", all[5].StartedAt, "--until", all[5].StartedAt},
	} {
		if _, got, next := d.refusal(t, 2, append([]string{"audit", "--state-dir", "nowhere"}, tokens...)...); got != message ||
			next != "gtm-connector audit --help" {
			t.Errorf("audit %v: %s, %s", tokens, got, next)
		}
	}
	_, stdout, _ := d.read(t, "audit", "--brief", first.BriefID)
	rows := strings.Split(stdout, "\n")
	if cells := strings.Split(rows[0], "\t"); len(rows) != 5 || len(cells) != 7 || cells[0] != all[0].StartedAt ||
		cells[1] != "get_account" || cells[2] != all[0].TokenID || cells[3] != "ok" || cells[5] != all[0].AuditID || cells[6] != "" {
		t.Errorf("audit as text:\n%s", stdout)
	}
}

// lines is what a command has written so far, read while it runs.
type lines struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

func (l *lines) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.text.String(), "\n")
}

// audit --follow prints the receipts there are, then each new one, until it is interrupted, which exits 130.
func TestAuditFollows(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	var card savedCard
	d.result(t, &card, "brief", "acc-003")
	following, interrupt := context.WithCancel(ctx)
	defer interrupt()
	var out lines
	var errs bytes.Buffer
	done := make(chan int)
	go func() {
		done <- runCommand(following, "audit", []string{"--follow", "--tool", "save_brief"}, screen{out: &out, err: &errs, json: true})
	}()
	wait := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); out.count() != want; time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("audit --follow printed %d receipts, not %d", out.count(), want)
			}
		}
	}
	wait(1)
	d.result(t, &card, "brief", "acc-007")
	wait(2)
	interrupt()
	var printed []receiptRow
	out.mu.Lock()
	for _, line := range strings.Split(strings.TrimSuffix(out.text.String(), "\n"), "\n") {
		var row receiptRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		printed = append(printed, row)
	}
	out.mu.Unlock()
	if code := <-done; code != 130 || printed[1].AuditID != card.SealedBy || printed[1].Status != "ok" ||
		errs.String() != `{"error": {"code": "interrupted", "message": "Interrupted. Completed local writes are retained.", `+
			`"next": "gtm-connector audit --help"}}`+"\n" {
		t.Errorf("exit %d after %+v\n%s", code, printed, errs.String())
	}
}

// A receipt's arguments can hold any text a caller sent. inspect shows them inert, as source text on a card
// is, and a tool name the connector does not know as a quoted literal on one line.
func TestInspectShowsAReceiptsArgumentsInert(t *testing.T) {
	d := setUpDemo(t)
	started, err := d.owner.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: d.workspace.ID, OperationID: "a-call",
		TokenID: "anonymous", Tool: "made\tup", Arguments: json.RawMessage(`{"query": "<!channel> see [3]"}`),
		Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status    string
		Arguments struct{ Query string }
	}
	d.result(t, &receipt, "inspect", started.ID)
	if receipt.Status != "pending" || receipt.Arguments.Query != "[mention channel] see (3)" {
		t.Errorf("the receipt as JSON: %+v", receipt)
	}
	_, stdout, _ := d.read(t, "inspect", started.ID)
	if !strings.Contains(stdout, "\t\"made\\tup\"\tanonymous\tpending\tunknown\t"+started.ID+"\t\n") ||
		!strings.HasSuffix(stdout, "\n\nArguments\n{\"query\": \"[mention channel] see (3)\"}\n\nReturned rows\nnone\n") {
		t.Errorf("the receipt as text:\n%s", stdout)
	}
}

// The messages are argparse's for the same arguments, as gtm_connector/cli.py reports them.
func TestArgumentsAreRefusedAsTheRecordedParserRefusesThem(t *testing.T) {
	for _, c := range []struct{ command, tokens, message string }{
		{"brief", "", "the following arguments are required: account_id"},
		{"inspect", "--role seller", "the following arguments are required: ID"},
		{"inspect", "--state-dir", "argument --state-dir: expected one argument"},
		{"accounts", "--query -x", "argument --query: expected one argument"},
		{"accounts", "--query=-x extra", "unrecognized arguments"},
		{"accounts", "--bogus", "unrecognized arguments"},
		{"brief", "acc-003 acc-004", "unrecognized arguments"},
		{"brief", "--bogus", "the following arguments are required: account_id"},
		{"brief", "acc-003 -- --json", "unrecognized arguments"},
		{"brief", "--without-calls=1 acc-003", "argument --without-calls: ignored explicit argument"},
		{"audit", "--status bad", "argument --status: invalid choice (choose from 'ok', 'error', 'pending')"},
		{"audit", "--s ok", "ambiguous option, which could match --state-dir, --status, --since"},
		{"audit", "--stat ok", "ambiguous option, which could match --state-dir, --status"},
		{"inspect", "x --role nobody", "argument --role: invalid choice (choose from 'customer', 'seller', 'unknown')"},
		{"inspect", "x 1 2x", "argument N: a number printed on the card, such as 5"},
		{"inspect", "x --role seller y", "argument N: a number printed on the card, such as 5"},
	} {
		if _, err := commands[c.command].parse(strings.Fields(c.tokens)); err == nil || err.Error() != c.message {
			t.Errorf("%s %s: %v", c.command, c.tokens, err)
		}
	}
	// An option may be shortened or joined to its argument, a number below zero is an argument, and what
	// follows "--" is never an option.
	got, err := commands["inspect"].parse([]string{"--state=a folder", "--rol", "seller", "brief", "-5", "--", "-7"})
	state, _ := got.text("--state-dir")
	role, _ := got.text("--role")
	if err != nil || state != "a folder" || role != "seller" || strings.Join(got.positionals, " ") != "brief -5 -7" {
		t.Errorf("%+v %v", got, err)
	}
	if got, err := commands["audit"].parse([]string{"--statu", "ok", "-h", "--bogus"}); err != nil || !got.help {
		t.Errorf("help after an option: %+v %v", got, err)
	}
	// An option that may be given again keeps each of its arguments in order, and the one argument connect
	// may be given can stand after an option.
	got, err = commands["connect"].parse([]string{"--seller", "Dana Crest", "calls", "--seller=U0731P9Q2R", "--sell", "-7"})
	if err != nil || strings.Join(got.lists["--seller"], "|") != "Dana Crest|U0731P9Q2R|-7" ||
		strings.Join(got.positionals, " ") != "calls" {
		t.Errorf("%+v %v", got, err)
	}
	if got, err := commands["connect"].parse([]string{"--remove", "demo"}); err != nil || got.positionals[0] != "demo" {
		t.Errorf("an argument after an option: %+v %v", got, err)
	}
	if _, err := commands["connect"].parse([]string{"hubspot", "--environment", "staging"}); err == nil ||
		!strings.HasPrefix(err.Error(), "argument provider: invalid choice") {
		t.Errorf("the first mistake on the line is the one reported: %v", err)
	}
	if rest, asJSON := withoutJSON([]string{"--json", "brief", "--json", "--", "--json"}); !asJSON ||
		strings.Join(rest, " ") != "brief -- --json" {
		t.Errorf("--json taken out: %v %v", rest, asJSON)
	}
}

func TestHelpAndUsageErrorsNeedNoDatabase(t *testing.T) {
	t.Setenv("GTM_DATABASE_URL", "")
	for command, spec := range commands {
		var out, errs bytes.Buffer
		if code := runCommand(ctx, command, []string{"--help"}, screen{out: &out, err: &errs}); code != 0 ||
			out.String() != spec.help || errs.Len() != 0 || !strings.HasPrefix(spec.help, "usage: gtm-connector "+command+" [-h] [--json] [--state-dir STATE_DIR]") {
			t.Errorf("%s --help: exit %d\n%s%s", command, code, out.String(), errs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := runCommand(ctx, "inspect", []string{"note-7"}, screen{out: &out, err: &errs}); code != 2 || out.Len() != 0 ||
		errs.String() != "Error: "+inspectForms+"\nNext: gtm-connector inspect --help\n" {
		t.Errorf("inspect of nothing it reads: exit %d\n%s", code, errs.String())
	}
	errs.Reset()
	if code := runCommand(ctx, "accounts", nil, screen{out: &out, err: &errs, json: true}); code != 1 ||
		errs.String() != `{"error": {"code": "local_failure", "message": "set GTM_DATABASE_URL to the app role's PostgreSQL URL", `+
			`"next": "gtm-connector accounts --help"}}`+"\n" {
		t.Errorf("accounts with no database: exit %d\n%s", code, errs.String())
	}
}

func TestTablesAndLiterals(t *testing.T) {
	rows := [][]any{{"acc-001", "Alder\x1b[2J Vale", nil}, {"acc-002", 12, 0.5}}
	headers := []string{"ID", "ACCOUNT", "MS"}
	if got := (screen{}).table(headers, rows); got != "acc-001\tAlder\\u001b[2J Vale\tunknown\nacc-002\t12\t0.5" {
		t.Errorf("in a pipe: %q", got)
	}
	want := "ID       ACCOUNT              MS\nacc-001  Alder\\u001b[2J Vale  unknown\nacc-002  12                   0.5"
	if got := (screen{terminal: true}).table(headers, rows); got != want {
		t.Errorf("on a terminal: %q", got)
	}
	if got := (screen{terminal: true, bold: true}).table(headers, nil); got != "\033[1mID  ACCOUNT  MS\033[0m" {
		t.Errorf("headings alone: %q", got)
	}
	for text, want := range map[string]string{"": "''", "var/v2": "var/v2", "a b": "'a b'", "it's": `'it'"'"'s'`,
		"caf\u00e9": "'caf\u00e9'"} {
		if got := shellQuote(text); got != want {
			t.Errorf("shellQuote(%q) = %s", text, got)
		}
	}
	for value, want := range map[float64]float64{1.269750006031245: 1.27, 1.8: 1.8, 0.125: 0.12, 2: 2, 0.004: 0} {
		if got := rounded(value); got != want {
			t.Errorf("rounded(%v) = %v", value, got)
		}
	}
	if literal(nil) != "Missing" || literal("") != "Missing" || literal("a \"b\"") != `"a \"b\""` || literal(true) != true {
		t.Error("a literal is not as the card prints it")
	}
	if !replaced("Dana [user U123]") || !replaced("(5)") || replaced("Dana (Crest)") || replaced("[draft] Dana") {
		t.Error("a label that neutralizing wrote in is not told from one it did not")
	}
}
