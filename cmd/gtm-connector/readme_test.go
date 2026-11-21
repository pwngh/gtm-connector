package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// README.md shows one traced number of the demo card: the inspect command in a shell block, then what
// inspect prints on a terminal in a text block of its own. The README shortens three values of the trace. A card's ID and a receipt's ID are random in every run, so the
// README's own spelling of them is the reference. The digest is the traced one, cut to its first eight
// characters.
const (
	shownAccount = "acc-003"
	shownNumber  = "5"
	shownCommand = "gtm-connector inspect brief-… 5"
	shownReceipt = "b63a52fb-…"
	// shownMatched is the receipt of the read that returned the record the speaker was matched by.
	shownMatched = "5e0c41d7-…"
)

// asShownInReadme is what inspect printed for one number, with its receipt's ID and its digest written as
// the README shortens them.
func asShownInReadme(t *testing.T, printed string) string {
	t.Helper()
	var receipt, matched, digest string
	for _, line := range strings.Split(printed, "\n") {
		field, value, _ := strings.Cut(line, "  ")
		switch value = strings.TrimLeft(value, " "); field {
		case "Receipt":
			receipt, _, _ = strings.Cut(value, " ")
		case "Matched from":
			_, after, _ := strings.Cut(value, ", receipt ")
			matched, _, _ = strings.Cut(after, " ")
		case "Source text SHA-256":
			digest = value
		}
	}
	if !receiptID.MatchString(receipt) || !receiptID.MatchString(matched) || len(digest) != 64 {
		t.Fatalf("inspect printed no receipt and digest:\n%s", printed)
	}
	printed = strings.Replace(printed, receipt, shownReceipt, 1)
	printed = strings.Replace(printed, matched, shownMatched, 1)
	return strings.Replace(printed, digest, digest[:8]+"…", 1) + "\n"
}

// readmeTrace is the README's trace: the shell block that runs inspect for the shown number, which the
// text block of what it prints must follow.
func readmeTrace(t *testing.T) string {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	command := "```sh\n" + shownCommand + "\n```"
	before, after, found := strings.Cut(string(page), command)
	if !found || strings.Count(before+after, command) != 0 {
		t.Fatalf("README.md does not show %q once, in a shell block", shownCommand)
	}
	shown := oracletest.ReadmeBlock(t, "FIELD ")
	if !strings.Contains(after, "```text\n"+shown) {
		t.Fatal("README.md's inspect output does not follow its inspect command")
	}
	return shown
}

// The block is compared with what inspect prints for the same number of the demo card: the card is made
// from the demo book through the maker the commands use, and traced and laid out by inspect's own code.
func TestReadmeShowsATracedNumber(t *testing.T) {
	shown := readmeTrace(t)
	// The demo workspace as its three rows describe it, with its receipts kept in memory.
	from := &reads{rows: map[string]postgres.Connection{}, kinds: map[string]*sourceKind{},
		bindings: map[string]*strictjson.Object{}}
	log := receipts.NewMemory(nil, nil)
	for _, source := range sourceNames {
		from.rows[source] = postgres.Connection{ID: "connection-" + source, Source: source, Mode: "demo", Generation: 1}
		from.kinds[source], from.bindings[source] = kindOf(source, "demo"), &strictjson.Object{}
		log.SetGeneration("readme", from.rows[source].ID, 1)
	}
	maker := from.maker()
	maker.Receipts = log
	sealed, err := maker.Brief(ctx, cards.Brief{
		Principal: cards.Principal{WorkspaceID: "readme", TokenID: receipts.Fingerprint("an invented bearer"),
			Scopes: []string{"crm:read", "calls:read", "slack:read"}},
		OperationID: "readme", AccountID: shownAccount, AsOf: cards.DemoAsOf, IncludeSlack: true})
	if err != nil {
		t.Fatal(err)
	}
	card, err := strictjson.Loads(sealed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	traced, err := traces(card.(*strictjson.Object), sealed.BriefID, []string{shownNumber})
	if err != nil {
		t.Fatal(err)
	}
	onTerminal := &run{command: "inspect", action: "numbers", screen: screen{terminal: true}}
	if printed := asShownInReadme(t, onTerminal.human(traced)); printed != shown {
		t.Errorf("the README trace is not what inspect prints: %s", oracletest.FirstDifference(shown, printed))
	}
}

// The same block, against the command itself: brief saves the card in PostgreSQL and inspect reads it back.
func TestReadmeShowsWhatTheInspectCommandPrints(t *testing.T) {
	shown := readmeTrace(t)
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	var card savedCard
	d.result(t, &card, "brief", shownAccount)
	var out, errs bytes.Buffer
	if code := runCommand(ctx, "inspect", []string{card.BriefID, shownNumber},
		screen{out: &out, err: &errs, terminal: true}); code != 0 {
		t.Fatalf("inspect: exit %d\n%s", code, errs.String())
	}
	if printed := asShownInReadme(t, strings.TrimSuffix(out.String(), "\n")); printed != shown {
		t.Errorf("the README trace is not what inspect prints: %s", oracletest.FirstDifference(shown, printed))
	}
}
