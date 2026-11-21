package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// command runs one command line as main does, connecting as the app role.
func (d *demoDatabase) command(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	var out, errs bytes.Buffer
	code = dispatch(ctx, argv, screen{out: &out, err: &errs})
	return code, out.String(), errs.String()
}

// succeeded is a command's stdout, which must have exited 0.
func (d *demoDatabase) succeeded(t *testing.T, argv ...string) (stdout, stderr string) {
	t.Helper()
	code, stdout, stderr := d.command(t, argv...)
	if code != 0 {
		t.Fatalf("%v: exit %d\n%s%s", argv, code, stdout, stderr)
	}
	return stdout, stderr
}

// failed is a command's refusal as text, which must have exited with this code.
func (d *demoDatabase) failed(t *testing.T, code int, argv ...string) string {
	t.Helper()
	got, stdout, stderr := d.command(t, argv...)
	if got != code || stdout != "" {
		t.Fatalf("%v: exit %d, not %d\n%s%s", argv, got, code, stdout, stderr)
	}
	return stderr
}

// object is a JSON document read as the program reads one.
func object(t *testing.T, document string) *strictjson.Object {
	t.Helper()
	value, err := strictjson.Loads([]byte(document))
	if err != nil {
		t.Fatalf("%v\n%s", err, document)
	}
	return value.(*strictjson.Object)
}

// recordedBooks fills the demo CRM's entry of sourceKinds, until the test ends, with an opener that gives
// each workspace the demo book with the rows a recorded card inserted into it, by the workspace's slug.
func recordedBooks(t *testing.T) map[string]*demo.Store {
	t.Helper()
	books := map[string]*demo.Store{}
	kind := kindsNamed("demo")[0]
	saved := *kind
	t.Cleanup(func() { *kind = saved })
	kind.crm = func(_ context.Context, o opening) (crmSource, error) {
		book, ok := books[o.workspace.Slug]
		if !ok {
			return nil, fmt.Errorf("no recorded book for %s", o.workspace.Slug)
		}
		return demoBook{book}, nil
	}
	return books
}

// The clock of a card is the recorder's in the recording and this program's here, so the line that names
// when the sources were read is compared without its times. It names one time when the first and last read
// fall in one second, which the clock decides too.
var (
	sourcesRead = regexp.MustCompile(`(?m)^Sources read ` + instant + `(?: to ` + instant + `)?\.$`)
	readTime    = regexp.MustCompile(instant)
)

// withoutTimes is a card's text with the times of its sources line left out.
func withoutTimes(text string) string {
	return sourcesRead.ReplaceAllStringFunc(text, func(line string) string {
		return strings.Replace(readTime.ReplaceAllString(line, "<time>"), "<time> to <time>", "<time>", 1)
	})
}

// Each recorded transcript a books_calls card read is imported through debug calls import, beside the demo
// CRM holding the rows the card's book inserted, and brief then prints the recorded card. Card text is
// compared byte for byte, and units and gaps as values, except for what the recorder decided by itself:
//
//   - its clock, which wrote the times of the line naming when the sources were read and the time of each
//     import;
//   - its IDs: the card's brief ID and seal, the receipt each import was resolved under, and so each
//     import's ID, which is the digest of a manifest that holds the import's time and that receipt;
//   - Slack: the recorder made these cards with Slack not requested, and the brief command always asks for
//     it, as the Python brief command does. The card's Slack line, gap and source are each asserted as the
//     demo Slack's, which holds no message for these accounts.
//
// A card whose book imported nothing is made the same way, and says no transcript is imported.
func TestImportedTranscriptsMakeTheRecordedCards(t *testing.T) {
	d := setUpDemo(t)
	books := recordedBooks(t)
	folder := exports(t)
	imported, cards := 0, 0
	for i, line := range oracletest.Lines(t, "cards/books_calls.jsonl.gz") {
		var row struct {
			Key       string          `json:"key"`
			AccountID string          `json:"account_id"`
			Options   json.RawMessage `json:"options"`
			Input     json.RawMessage `json:"input"`
			Card      string          `json:"card"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		input, options := object(t, string(row.Input)), object(t, string(row.Options))
		book, err := demo.Load(demo.Book)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range get(get(input, "rows").(*strictjson.Object), "crm").([]any) {
			pair := entry.([]any)
			if err := book.Insert("crm", pair[0].(string), pair[1].([]any)); err != nil {
				t.Fatal(err)
			}
		}
		name := fmt.Sprintf("books-calls-%d", i)
		books[name] = book
		where := []string{"--state-dir", name}
		d.succeeded(t, append([]string{"connect", "demo"}, where...)...)
		d.succeeded(t, append([]string{"connect", "calls"}, where...)...)

		recordedID := ""
		if transcript, ok := get(input, "transcript").(*strictjson.Object); ok {
			imported++
			file := filepath.Join(folder, fmt.Sprintf("call-%d.%s", i, get(transcript, "format")))
			if err := os.WriteFile(file, []byte(get(transcript, "text").(string)), 0o600); err != nil {
				t.Fatal(err)
			}
			argv := append([]string{"--json", "debug", "calls", "import", file, "--account", row.AccountID}, where...)
			if get(transcript, "speaker_names") == true {
				argv = append(argv, "--speaker-names")
			}
			if called, ok := get(transcript, "called_at").(string); ok {
				argv = append(argv, "--called-at", called)
			}
			stdout, _ := d.succeeded(t, argv...)
			got, want := object(t, stdout), get(transcript, "import").(*strictjson.Object)
			recordedID = get(want, "import_id").(string)
			for _, key := range want.Keys {
				switch key {
				case "import_id", "imported_at", "resolution_receipt_id":
					// The recorder's clock and IDs.
				default:
					if !evidence.Equal(get(got, key), get(want, key)) {
						t.Errorf("%s: the import's %s is %v, recorded %v", row.Key, key, get(got, key), get(want, key))
					}
				}
			}
			if strings.Join(got.Keys, " ") != strings.Join(want.Keys, " ") {
				t.Errorf("%s: the import's members are %v, recorded %v", row.Key, got.Keys, want.Keys)
			}
			// The import's ID is its own: the recorded one is put in its place below.
			row.Card = strings.ReplaceAll(row.Card, recordedID, get(got, "import_id").(string))
		}

		asOf, _ := get(options, "as_of").(string)
		stdout, _ := d.succeeded(t, append([]string{"--json", "brief", row.AccountID, "--as-of", asOf}, where...)...)
		got, want := object(t, stdout), object(t, row.Card)
		cards++
		gotText, wantText := withoutTimes(get(got, "text").(string)), withoutTimes(get(want, "text").(string))
		// The one Slack line: the recorder did not request Slack, and brief reads the demo Slack, which holds
		// no message for these accounts.
		const notRequested, noMessage = "\n  Slack: not requested for this card.\n",
			"\n  Slack: no message was returned for this account.\n"
		if strings.Count(wantText, notRequested) != 1 || strings.Count(gotText, noMessage) != 1 {
			t.Errorf("%s: the Slack line is not the one expected", row.Key)
		}
		if wantText = strings.Replace(wantText, notRequested, noMessage, 1); gotText != wantText {
			t.Errorf("%s: the card's text differs at %s", row.Key, oracletest.FirstDifference(gotText, wantText))
		}
		for _, key := range []string{"schema_version", "account_id", "as_of", "status", "rules_version", "units"} {
			if !evidence.Equal(get(got, key), get(want, key)) {
				t.Errorf("%s: the card's %s differs", row.Key, key)
			}
		}
		// The recorded sources are crm and calls; the card adds Slack as the demo data it read.
		wantSources := get(want, "sources").(*strictjson.Object).Copy()
		wantSources.Set("slack", "demo_fixture")
		if !evidence.Equal(get(got, "sources"), wantSources) {
			t.Errorf("%s: the card's sources differ", row.Key)
		}
		// The recorded Slack gap is not_requested; the card's, in its place, is the empty demo Slack's.
		wantGaps := get(want, "gaps").([]any)
		for i, gap := range wantGaps {
			if get(gap.(*strictjson.Object), "code") == "slack_not_requested" {
				wantGaps[i] = strictjson.NewObject("code", "slack_no_message", "disposition", "note", "count", 0,
					"by_role", nil, "text", strings.TrimSpace(noMessage))
			}
		}
		if !evidence.Equal(get(got, "gaps"), wantGaps) {
			t.Errorf("%s: the card's gaps differ", row.Key)
		}
	}
	if imported != 50 || cards != 82 {
		t.Errorf("%d transcripts imported and %d cards made, want 50 and 82", imported, cards)
	}
}

// exports is a new folder for transcript exports. On macOS its path goes through /var, a link, as a
// person's own folders can.
func exports(t *testing.T) string { return t.TempDir() }

// meeting is an invented call of the demo book's acc-007: its contact speaks, then a listed seller.
const meeting = "WEBVTT\n\n1\n00:00:01.000 --> 00:00:04.000\n<v Dana Crest>We will sign the renewal this quarter.\n\n" +
	"2\n00:00:05.000 --> 00:00:08.000\n<v Morgan Reed>Great, I will send the order form today.\n"

// A demo workspace connects imported calls, debug calls imports a transcript under a CRM read of its own,
// lists and inspects it, and brief quotes it. Each kind of refusal is given once, and removing the calls
// leaves the imports and a card that reads no calls.
func TestDebugCallsBesideTheDemoCRM(t *testing.T) {
	d := setUpDemo(t)
	folder := exports(t)
	file := filepath.Join(folder, "meeting.vtt")
	if err := os.WriteFile(file, []byte(meeting), 0o600); err != nil {
		t.Fatal(err)
	}
	where := []string{"--state-dir", "calls-books"}
	with := func(argv ...string) []string { return append(argv, where...) }

	// Before a CRM and before calls are connected.
	if message := d.failed(t, 2, with("debug", "calls", "import", file, "--account", "acc-007")...); !strings.HasPrefix(message,
		"Error: No CRM is connected.") {
		t.Errorf("an import with no CRM: %s", message)
	}
	if message := d.failed(t, 3, with("debug", "calls", "list")...); !strings.HasPrefix(message,
		"Error: source_identity_mismatch: This workspace holds no imported transcripts.") {
		t.Errorf("a list with no imports: %s", message)
	}
	d.succeeded(t, with("connect", "demo")...)
	if message := d.failed(t, 2, with("debug", "calls", "import", file, "--account", "acc-007")...); message !=
		"Error: Transcripts are not connected. Run gtm-connector connect calls --state-dir calls-books first.\n"+
			"Next: gtm-connector connect --help\n" {
		t.Errorf("an import before connect calls: %s", message)
	}
	stdout, _ := d.succeeded(t, with("connect", "calls", "--seller", "Morgan Reed")...)
	if stdout != "Call transcripts connected.\ncrm\tconnected\tdemo data\ncalls\tconnected\timported transcripts\n"+
		"slack\tconnected\tdemo data\n" {
		t.Errorf("connect calls:\n%s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "connect", "status")...)
	status := object(t, stdout)
	if calls := get(get(status, "sources").(*strictjson.Object), "calls"); !evidence.Equal(calls,
		strictjson.NewObject("provider", "imported")) {
		t.Errorf("connect status: %s", stdout)
	}

	// The import, with its steps on stderr and the CRM read under its own receipt.
	stdout, stderr := d.succeeded(t, with("debug", "calls", "import", file, "--account", "acc-007")...)
	if stderr != "Reading the transcript export.\nPreparing the connected CRM and private state.\n"+
		"Resolving the account through an audited CRM read.\nSaving the private transcript selection.\n"+
		"Sources: CRM is demo data; calls are imported transcripts, whose provider and statements are unverified; "+
		"Slack is demo data.\n" ||
		!strings.HasPrefix(stdout, "Transcript imported. The excerpt is selected for this account.\nImport\tnote-") {
		t.Fatalf("the import:\n%s%s", stdout, stderr)
	}
	stdout, stderr = d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007")...)
	again := object(t, stdout)
	importID, receiptID := get(again, "import_id").(string), get(again, "resolution_receipt_id").(string)
	if get(again, "status") != "unchanged" || get(again, "created") != false || strings.Count(stderr, `{"progress": `) != 4 {
		t.Errorf("the same import again:\n%s%s", stdout, stderr)
	}
	var receipt receiptRow
	d.result(t, &receipt, with("inspect", receiptID)...)
	if receipt.Tool != "get_account" || receipt.Status != "ok" || receipt.Rows[0] != "crm/accounts/acc-007" ||
		!strings.HasPrefix(*receipt.BriefID, "import-") {
		t.Errorf("the receipt the import was resolved under: %+v", receipt)
	}

	// list and inspect read the imports only.
	stdout, stderr = d.succeeded(t, with("debug", "calls", "list")...)
	if stderr != "Source: user-supplied transcript export. Provider identity and source statements are unverified.\n" ||
		!strings.HasPrefix(stdout, importID+"\tacc-007\tTrue\tunknown\t") ||
		!strings.HasSuffix(stdout, "\tcues 1:2 (includes adjacent context)\tFalse\n") {
		t.Errorf("debug calls list:\n%s%s", stdout, stderr)
	}
	stdout, _ = d.succeeded(t, with("debug", "calls", "inspect", importID, "--full")...)
	if !strings.Contains(stdout, "\n\nAll imported units\n1 (1000 to 4000 ms): \"<v Dana Crest>We will sign the renewal this quarter.\"\n") ||
		!strings.HasSuffix(stdout, "\n\nOriginal export text (quoted)\n\"WEBVTT\\n\\n1\\n00:00:01.000 --> 00:00:04.000\\n<v Dana Crest>"+
			"We will sign the renewal this quarter.\\n\\n2\\n00:00:05.000 --> 00:00:08.000\\n<v Morgan Reed>Great, I will "+
			"send the order form today.\\n\"\n") {
		t.Errorf("debug calls inspect --full:\n%s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "inspect", importID)...)
	if inspected := object(t, stdout); get(get(inspected, "import").(*strictjson.Object), "active") != true ||
		len(get(inspected, "units").([]any)) != 2 {
		t.Errorf("debug calls inspect --json:\n%s", stdout)
	}

	// brief quotes it, under the roles the contact and the listed seller give.
	stdout, stderr = d.succeeded(t, with("brief", "acc-007")...)
	if !strings.Contains(stdout, "  Customer, by a speaker label equal to one contact's name\n"+
		"    [4] call time Missing; \"Dana Crest\", role Missing [2]\n        \"We will sign the renewal this quarter.\"\n") ||
		!strings.Contains(stdout, "\n  Calls: one transcript excerpt was read, cues 1 to 2 of 2. Every other call was not read.\n") ||
		!strings.HasPrefix(stderr, "Sources: CRM is demo data; calls are imported transcripts") {
		t.Errorf("brief:\n%s%s", stdout, stderr)
	}
	if audited := d.audited(t, "calls-books"); audited != "get_account:ok:200: get_account:ok:200: "+
		"get_account:ok:200: list_opportunities:ok:200: read_call_notes:ok:200: read_slack_messages:ok:200: save_brief:ok:200:" {
		t.Errorf("the workspace's receipts: %s", audited)
	}

	// Each kind of refusal, once.
	large := filepath.Join(folder, "large.vtt")
	if err := os.WriteFile(large, bytes.Repeat([]byte("a"), 2<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(folder, "link.vtt")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	// A folder on the way to the file that is a link is the person's choice, and is followed: the same
	// file through it is the same import. The file itself as a link (link.vtt) is refused below.
	linkedFolder := filepath.Join(folder, "linked")
	if err := os.Symlink(folder, linkedFolder); err != nil {
		t.Fatal(err)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "import", filepath.Join(linkedFolder, "meeting.vtt"),
		"--account", "acc-007")...)
	if through := object(t, stdout); get(through, "status") != "unchanged" || get(through, "import_id") != importID {
		t.Errorf("an import through a linked folder:\n%s", stdout)
	}
	for _, c := range []struct {
		code    int
		argv    []string
		message string
		next    string
	}{
		{2, []string{"debug", "calls", "import", file}, callsArgumentsRefused, "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-007", "--account-number", "7"}, callsArgumentsRefused,
			"gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "frob"}, callsArgumentsRefused, "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-7"},
			"The account must be a canonical acc-NNN or acc-sf- Salesforce identifier.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account-number", "7"},
			"--account-number needs Salesforce as the connected CRM.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-sf-001000000000000AAA"},
			"The canonical account ID must match the connected CRM.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-007", "--lines", "1:2"},
			"Use --cues with VTT or SRT, or --lines with TXT.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-007", "--called-at", "2027-01-14"},
			"--called-at requires an ISO timestamp with a UTC offset, such as 2027-01-15T10:00:00Z.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", filepath.Join(folder, "meeting.doc"), "--account", "acc-007"},
			"Import an existing UTF-8 .txt, .vtt, or .srt transcript export.", "gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-007", "--cues", "1:200"},
			"calls_import_invalid: " + callFailures["calls_import_invalid"], "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "import", link, "--account", "acc-007"},
			"calls_import_invalid: " + callFailures["calls_import_invalid"], "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "import", large, "--account", "acc-007"},
			"calls_import_too_large: " + callFailures["calls_import_too_large"], "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-007", "--speaker-names"},
			"calls_speaker_names_missing: " + callFailures["calls_speaker_names_missing"], "gtm-connector debug calls import --help"},
		{1, []string{"debug", "calls", "import", filepath.Join(folder, "missing.vtt"), "--account", "acc-007"},
			"calls_import_io: " + callFailures["calls_import_io"], "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "import", file, "--account", "acc-999"},
			"calls_account_not_found: " + callFailures["calls_account_not_found"], "gtm-connector accounts --help"},
		{3, []string{"debug", "calls", "import", file, "--account", "acc-008"},
			"calls_reassign_required: " + callFailures["calls_reassign_required"], "gtm-connector debug calls import --help"},
		{2, []string{"debug", "calls", "inspect", "note-" + strings.Repeat("0", 64)},
			"calls_import_not_found: " + callFailures["calls_import_not_found"], "gtm-connector debug calls list --state-dir calls-books"},
		{2, []string{"debug", "calls", "inspect", "note-1"},
			"The import ID must be the note- identifier returned by debug calls import or debug calls list.",
			"gtm-connector debug calls --help"},
		{2, []string{"debug", "calls", "list", "--after", "note-1"},
			"--after requires the import ID returned as next_after by debug calls list.", "gtm-connector debug calls --help"},
	} {
		argv := append([]string{"--json"}, with(c.argv...)...)
		// The error is the last line on stderr, after the steps the import reached.
		lines := strings.Split(strings.TrimSuffix(d.failed(t, c.code, argv...), "\n"), "\n")
		var refused struct {
			Error struct{ Code, Message, Next string }
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &refused); err != nil ||
			refused.Error.Message != c.message || refused.Error.Next != c.next {
			t.Errorf("%v: %+v", c.argv[2:], refused.Error)
		}
	}
	// A refused resolution still has its receipt, and a refused import saved nothing.
	if audited := d.audited(t, "calls-books"); !strings.Contains(audited, "get_account:error:200:request_failed") {
		t.Errorf("the refused resolution's receipt: %s", audited)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "list")...)
	if n := len(get(object(t, stdout), "imports").([]any)); n != 1 {
		t.Errorf("%d imports after the refusals", n)
	}

	// connect demo keeps connected imports connected.
	if stdout, _ = d.succeeded(t, with("connect", "demo")...); !strings.Contains(stdout, "\ncalls\tconnected\timported transcripts\n") {
		t.Errorf("connect demo beside connected imports:\n%s", stdout)
	}
	// Removing the calls keeps the imports, and a card then reads no calls. connect demo never brings them
	// back, and never makes them demo data: only connect calls does.
	stdout, _ = d.succeeded(t, with("connect", "calls", "--remove")...)
	if !strings.HasPrefix(stdout, "Call transcripts removed.") || !strings.Contains(stdout, "\ncalls\tnot connected\t\n") {
		t.Errorf("connect calls --remove:\n%s", stdout)
	}
	d.succeeded(t, with("debug", "calls", "inspect", importID)...)
	if stdout, _ = d.succeeded(t, with("connect", "demo")...); !strings.Contains(stdout, "\ncalls\tnot connected\t\n") {
		t.Errorf("connect demo beside detached imports:\n%s", stdout)
	}
	if stdout, _ = d.succeeded(t, with("brief", "acc-007")...); !strings.Contains(stdout, "\n  Calls: not connected.\n") ||
		strings.Contains(stdout, "We will sign") {
		t.Errorf("brief after the removal and connect demo:\n%s", stdout)
	}
	d.succeeded(t, with("connect", "calls")...)
	if stdout, _ = d.succeeded(t, with("brief", "acc-007")...); !strings.Contains(stdout, "We will sign") {
		t.Errorf("brief after connect calls again:\n%s", stdout)
	}

	// A workspace that read demo call notes takes no imports, and a demo workspace holds no other real source.
	d.succeeded(t, "connect", "demo", "--state-dir", "demo-notes")
	d.succeeded(t, "brief", "acc-007", "--state-dir", "demo-notes")
	if message := d.failed(t, 3, "connect", "calls", "--state-dir", "demo-notes"); !strings.HasPrefix(message,
		"Error: This workspace has read demo call notes, and imported transcripts never share a workspace with them.") {
		t.Errorf("connect calls after demo call notes: %s", message)
	}
	workspace, err := d.owner.Workspace(ctx, "demo-notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []postgres.SourceChange{{Source: "slack", Mode: "slack"}, {Source: "crm", Mode: "sf"}} {
		if _, err := d.owner.UpdateSources(ctx, workspace, func([]postgres.Connection) (postgres.SourceUpdate, error) {
			return postgres.SourceUpdate{Changes: []postgres.SourceChange{change}}, nil
		}); err == nil {
			t.Errorf("a demo workspace took %s %s", change.Source, change.Mode)
		}
	}
}

// Imported calls beside a real CRM: the import resolves the account through it under a receipt of its own,
// after brief's listing read, and the card quotes the import.
func TestDebugCallsBesideARegisteredCRM(t *testing.T) {
	d := setUpDemo(t)
	registerInventedCRM(t, d)
	const name = "invented-books"
	d.connectInvented(t, name, salesforcetest.Alias)
	d.succeeded(t, "connect", "calls", "--state-dir", name)
	file := filepath.Join(exports(t), "meeting.txt")
	if err := os.WriteFile(file, []byte("Dana Crest: We will sign the renewal this quarter.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if message := d.failed(t, 2, "debug", "calls", "import", file, "--account", "acc-007", "--state-dir", name); !strings.Contains(message,
		"The canonical account ID must match the connected CRM.") {
		t.Errorf("a demo account ID beside a real CRM: %s", message)
	}
	stdout, stderr := d.succeeded(t, "--json", "debug", "calls", "import", file, "--account", inventedAccount,
		"--speaker-names", "--state-dir", name)
	if result := object(t, stdout); get(result, "status") != "imported" || get(result, "speaker_names") != true {
		t.Errorf("the import:\n%s%s", stdout, stderr)
	}
	if audited := d.audited(t, name); audited != "get_account:ok:200: get_account:ok:200:" {
		t.Errorf("the import's reads: %s", audited)
	}
	stdout, stderr = d.succeeded(t, "brief", inventedAccount, "--state-dir", name)
	if !strings.Contains(stdout, "\"We will sign the renewal this quarter.\"") ||
		stderr != "Sources: CRM is Salesforce (production), identity verified; calls are imported transcripts, whose "+
			"provider and statements are unverified; Slack is not connected.\n" {
		t.Errorf("brief:\n%s%s", stdout, stderr)
	}
}

// interrupted is the demo book as a CRM that runs during once, on its read under a receipt: the second
// get_account of an import, after the listing read.
type interrupted struct {
	demoBook
	reads  int
	during func()
}

func (c *interrupted) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	if c.reads++; tool == "get_account" && c.reads == 2 {
		c.during()
	}
	return c.demoBook.Read(ctx, tool, accountID)
}

// An import whose calls are detached after its account is resolved is not saved: the resolution's receipt
// is committed, and the save refuses it as changed, so no transcript and no active selection is written.
func TestAnImportIsNotSavedAfterItsCallsAreDetached(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "detached-calls"}
	d.succeeded(t, append([]string{"connect", "demo"}, where...)...)
	d.succeeded(t, append([]string{"connect", "calls"}, where...)...)
	workspace, err := d.owner.Workspace(ctx, "detached-calls")
	if err != nil {
		t.Fatal(err)
	}
	book, err := seededBook()
	if err != nil {
		t.Fatal(err)
	}
	crm := &interrupted{demoBook: demoBook{book}, during: func() {
		if _, err := d.owner.UpdateSources(ctx, workspace, func([]postgres.Connection) (postgres.SourceUpdate, error) {
			return postgres.SourceUpdate{Changes: []postgres.SourceChange{{Source: "calls", Mode: "none"}}}, nil
		}); err != nil {
			t.Error(err)
		}
	}}
	kind := kindsNamed("demo")[0]
	saved := *kind
	t.Cleanup(func() { *kind = saved })
	kind.crm = func(context.Context, opening) (crmSource, error) { return crm, nil }

	file := filepath.Join(exports(t), "meeting.vtt")
	if err := os.WriteFile(file, []byte(meeting), 0o600); err != nil {
		t.Fatal(err)
	}
	if message := d.failed(t, 3, append([]string{"debug", "calls", "import", file, "--account", "acc-007"}, where...)...); !strings.Contains(message,
		"Error: A source of this workspace was connected again while it was read") {
		t.Errorf("an import whose calls were detached: %s", message)
	}
	if audited := d.audited(t, "detached-calls"); audited != "get_account:ok:200:" {
		t.Errorf("the resolution's receipt: %s", audited)
	}
	if n := d.count(t, "SELECT (SELECT count(*) FROM call_import_revisions) + (SELECT count(*) FROM call_import_active)"); n != 0 {
		t.Errorf("the refused import wrote %d rows", n)
	}
}

// --json writes a transcript's format characters, such as a bidirectional override, as escapes, so a
// terminal shows the text in the order it was written. The decoded text is the transcript's.
func TestFormatCharactersAreEscapedInJSON(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "format-books"}
	d.succeeded(t, append([]string{"connect", "demo"}, where...)...)
	d.succeeded(t, append([]string{"connect", "calls"}, where...)...)
	// A right-to-left override and its pop, and a language tag, written here by code point.
	override, pop, tag := string(rune(0x202e)), string(rune(0x202c)), string(rune(0xe0001))
	said := "We will sign " + override + "the renewal" + pop + " this quarter." + tag + "\n"
	file := filepath.Join(exports(t), "override.txt")
	if err := os.WriteFile(file, []byte(said), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := d.succeeded(t, append([]string{"--json", "debug", "calls", "import", file, "--account", "acc-007"}, where...)...)
	importID := get(object(t, stdout), "import_id").(string)
	stdout, _ = d.succeeded(t, append([]string{"--json", "debug", "calls", "inspect", importID, "--full"}, where...)...)
	escape := func(units ...string) string {
		var text string
		for _, unit := range units {
			text += string(rune(92)) + "u" + unit
		}
		return text
	}
	if strings.ContainsAny(stdout, override+pop+tag) ||
		!strings.Contains(stdout, escape("202e")+"the renewal"+escape("202c")) || !strings.Contains(stdout, escape("db40", "dc01")) {
		t.Errorf("format characters are written as they are:\n%s", stdout)
	}
	if got := get(object(t, stdout), "original_text"); got != said {
		t.Errorf("the decoded text is %q", got)
	}
}

// A page of imports on a terminal: a block for each, and how to continue.
func TestImportsOnATerminal(t *testing.T) {
	r := &run{screen: screen{terminal: true}, name: "calls books"}
	row := object(t, `{"import_id": "note-1", "account_id": "acc-007", "called_at": null, "imported_at": "2027-01-15T12:00:00Z",
		"requested_range": {"first": 1, "last": 2}, "cited_range": {"first": 1, "last": 3}, "unit_kind": "cues",
		"speaker_names": true, "active": false}`)
	if got := r.importsText([]any{row}, "note-1"); got != "note-1\n  Account: acc-007. Active: no.\n"+
		"  Called: unknown. Imported: 2027-01-15T12:00:00Z.\n  Requested: cues 1:2. Cited: cues 1:3.\n"+
		"  Speaker names: the name that opens each line.\n"+adjacent+"\nMore imports are available. Continue with:\n"+
		"gtm-connector debug calls list --after note-1 \\\n  --state-dir 'calls books'" {
		t.Errorf("the page:\n%s", got)
	}
	if got := r.importsText(nil, nil); got != "No transcript imports are stored." {
		t.Errorf("an empty page: %s", got)
	}
}

// An attendee list given with --attendees is part of the import: the result counts its rows and shows none
// of them, the same list again is the same import, another list is a new import that becomes the account's,
// and a bad file is refused before anything is opened.
func TestAnImportCarriesItsAttendeeList(t *testing.T) {
	d := setUpDemo(t)
	folder := exports(t)
	file := filepath.Join(folder, "meeting.vtt")
	write := func(name, text string) string {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("meeting.vtt", meeting)
	two := write("two.json", `{"schema_version": 1, "attendees": [{"label": "Dana Crest", "email": "dana@alder-vale.example"}, `+
		`{"label": "Morgan Reed", "email": "morgan@northwind.example"}]}`)
	one := write("one.json", `{"schema_version": 1, "attendees": [{"label": "Dana Crest", "email": "dana@alder-vale.example"}]}`)
	where := []string{"--state-dir", "calls-attendees"}
	with := func(argv ...string) []string { return append(argv, where...) }
	d.succeeded(t, with("connect", "demo")...)
	d.succeeded(t, with("connect", "calls")...)

	stdout, _ := d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007", "--attendees", two)...)
	first := object(t, stdout)
	if get(first, "status") != "imported" || !evidence.Equal(get(first, "attendees"), 2) ||
		strings.Contains(stdout, "alder-vale") || strings.Contains(stdout, "Dana Crest") {
		t.Errorf("the import with two attendees: %s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007", "--attendees", two)...)
	if again := object(t, stdout); get(again, "status") != "unchanged" || get(again, "import_id") != get(first, "import_id") {
		t.Errorf("the same list again: %s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007", "--attendees", one)...)
	other := object(t, stdout)
	if get(other, "status") != "imported" || get(other, "import_id") == get(first, "import_id") ||
		!evidence.Equal(get(other, "attendees"), 1) {
		t.Errorf("another list: %s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007")...)
	if plain := object(t, stdout); get(plain, "status") != "imported" || get(plain, "attendees") != nil {
		t.Errorf("no list: %s", stdout)
	}
	if stdout, _ = d.succeeded(t, with("brief", "acc-007")...); !strings.Contains(stdout, "We will sign") {
		t.Errorf("brief after the imports:\n%s", stdout)
	}

	for _, c := range []struct {
		code    int
		sidecar string
		refusal string
	}{
		{2, write("bad.json", `{"schema_version": 1, "attendees": [{"label": "Dana Crest", "email": "dana"}]}`),
			"calls_attendees_invalid"},
		{2, write("big.json", strings.Repeat(" ", calls.MaxAttendeeFileBytes+1)), "calls_attendees_too_large"},
		{1, filepath.Join(folder, "missing.json"), "calls_import_io"},
	} {
		lines := strings.Split(strings.TrimSuffix(d.failed(t, c.code, with("--json", "debug", "calls", "import", file,
			"--account", "acc-007", "--attendees", c.sidecar)...), "\n"), "\n")
		var refused struct {
			Error struct{ Message, Next string }
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &refused); err != nil ||
			refused.Error.Message != c.refusal+": "+callFailures[c.refusal] ||
			refused.Error.Next != "gtm-connector debug calls import --help" || len(lines) != 2 {
			t.Errorf("%s: %v %+v", c.refusal, lines, refused.Error)
		}
	}
}
