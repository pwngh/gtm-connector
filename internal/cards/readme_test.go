package cards

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// README.md shows two blocks of the connector's output: the demo card and one traced number of it. These
// tests take each block out of the page and compare every byte of it with what the connector gives.

// readmeBlock is the text block of README.md that opens with the given words, up to its closing fence. A
// page with no such block fails the test, so a moved or renamed fence cannot pass as nothing to compare.
func readmeBlock(t *testing.T, opening string) string {
	t.Helper()
	page, err := os.ReadFile(filepath.Join(oracletest.Path(t, ""), "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, opened := strings.Cut(string(page), "```text\n"+opening)
	if !opened {
		t.Fatalf("README.md has no text block that opens with %q", opening)
	}
	block, _, closed := strings.Cut(after, "```")
	if !closed {
		t.Fatalf("the README.md block that opens with %q has no closing fence", opening)
	}
	return opening + block
}

// readmeCase is the case of testdata/oracle/cards/cases.jsonl.gz whose card the README shows.
const readmeCase = "test_10_the_readme_shows_the_demo_card_byte_for_byte/1"

// demoCard builds the card of readmeCase from its recorded inputs, as the cases family of the parity test does.
func demoCard(t *testing.T) *Sealed {
	t.Helper()
	for _, line := range oracletest.Lines(t, "cards/cases.jsonl.gz") {
		var row fixtureRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Key != readmeCase {
			continue
		}
		var o options
		if err := json.Unmarshal(row.Options, &o); err != nil {
			t.Fatal(err)
		}
		sc := newScope("cases/" + row.Key)
		m, _ := sc.maker(demoBook(t), demoSources(nil))
		sealed, err := m.Brief(context.Background(), o.brief(row.AccountID, sc.token(accessToken)))
		if err != nil {
			t.Fatalf("%s: no card: %v", row.Key, err)
		}
		return sealed
	}
	t.Fatalf("cards/cases.jsonl.gz has no case %q", readmeCase)
	return nil
}

// One line of a card changes with the clock: when its sources were read. The README shows this one.
const shownSourcesRead = "Sources read 2027-01-15T14:00:02Z."

func TestReadmeShowsTheDemoCard(t *testing.T) {
	shown := readmeBlock(t, "DEAL CARD")
	head, rest, found := strings.Cut(demoCard(t).Text, "\nSources read ")
	_, tail, ended := strings.Cut(rest, "\n")
	if !found || !ended {
		t.Fatal("the card has no line that says when its sources were read")
	}
	if text := head + "\n" + shownSourcesRead + "\n" + tail; text != shown {
		t.Errorf("the README card is not the card Go builds: %s", oracletest.FirstDifference(shown, text))
	}
}

// inspected is what "inspect <card> <number>" prints on a terminal for one sentence, and the two values in
// it that the README shortens.
type inspected struct{ table, receiptID, digest string }

// inspectNumber is where the Go rendering of inspect plugs in: once the command is ported, this gives what Go
// prints for the number on the card demoCard builds. Until then it lays out the Python CLI's recorded trace
// of the number, from the step "inspect numbers" of testdata/oracle/commands/session.jsonl, as that CLI does
// on a terminal: a heading and one row for each field, the first column padded to its widest cell. It lays
// out only a labelled sentence that is shown on the card and has no cue or line range, and it neutralizes
// nothing, which the recorded sentence does not need.
func inspectNumber(t *testing.T, number int) inspected {
	t.Helper()
	var trace *strictjson.Object
	for _, line := range oracletest.Lines(t, "commands/session.jsonl") {
		step := load(t, line).(*strictjson.Object)
		if get(step, "label") != "inspect numbers" {
			continue
		}
		for _, value := range get(step, "json").([]any) {
			if intOf(get(value.(*strictjson.Object), "n")) == number {
				trace = value.(*strictjson.Object)
			}
		}
	}
	if trace == nil {
		t.Fatalf("the recorded session does not inspect number %d", number)
	}
	text, _ := get(trace, "text").(string)
	speaker, labelled := get(trace, "speaker").(string)
	if get(trace, "kind") != "sentence" || !labelled || get(trace, "shown") != "yes" || get(trace, "span") != nil {
		t.Fatalf("number %d is not a labelled sentence shown on the card", number)
	}
	receipt := get(trace, "receipt").(*strictjson.Object)
	next := "none"
	if following := get(trace, "next_by_speaker"); following != nil {
		next = fmt.Sprint(following)
	}
	rows := [][2]string{
		{"FIELD", "VALUE"},
		{"Number", fmt.Sprint(get(trace, "n"))},
		{"Kind", "sentence"},
		{"Record", get(trace, "record_ref").(string)},
		{"Receipt", fmt.Sprintf("%s (%s, %s)", get(receipt, "audit_id"), get(receipt, "tool"), get(receipt, "status"))},
		{"Text", strictjson.Quote(text)},
		{"Speaker", strictjson.Quote(speaker)},
		{"Next by this speaker", next},
		{"Role", fmt.Sprintf("%s (%s)", get(trace, "role"), get(trace, "basis"))},
		{"Time", get(trace, "at").(string)},
		{"On the card", "yes"},
		{"Source text SHA-256", get(trace, "raw_sha256").(string)},
		{"Neutralizer version", fmt.Sprint(get(trace, "sanitizer_version"))},
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row[0]))
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = fmt.Sprintf("%-*s  %s", width, row[0], row[1])
	}
	return inspected{table: strings.Join(lines, "\n"), receiptID: get(receipt, "audit_id").(string),
		digest: get(trace, "raw_sha256").(string)}
}

// The README shortens three values of the trace. A card's ID and a receipt's ID are random in a real run and
// numbered placeholders in the recording, so the README's own spelling of them is the reference. The digest
// is the traced one, cut to its first eight characters.
const (
	shownNumber  = 5
	shownCommand = "$ gtm-connector inspect brief-… 5"
	shownReceipt = "b63a52fb-…"
)

func TestReadmeShowsATracedNumber(t *testing.T) {
	shown := readmeBlock(t, "$ gtm-connector inspect")
	traced := inspectNumber(t, shownNumber)
	table := strings.Replace(traced.table, traced.receiptID, shownReceipt, 1)
	table = strings.Replace(table, traced.digest, traced.digest[:8]+"…", 1)
	if want := shownCommand + "\n" + table + "\n"; shown != want {
		t.Errorf("the README trace is not what inspect prints: %s", oracletest.FirstDifference(shown, want))
	}
}
