package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// README.md shows one traced number of the demo card, as inspect prints it on a terminal. The README
// shortens three values of the trace. A card's ID and a receipt's ID are random in every run, so the
// README's own spelling of them is the reference. The digest is the traced one, cut to its first eight
// characters.
const (
	shownAccount = "acc-003"
	shownNumber  = "5"
	shownCommand = "$ gtm-connector inspect brief-… 5"
	shownReceipt = "b63a52fb-…"
)

// asShownInReadme is what inspect printed for one number, with its receipt's ID and its digest written as
// the README shortens them.
func asShownInReadme(t *testing.T, printed string) string {
	t.Helper()
	var receipt, digest string
	for _, line := range strings.Split(printed, "\n") {
		field, value, _ := strings.Cut(line, "  ")
		switch value = strings.TrimLeft(value, " "); field {
		case "Receipt":
			receipt, _, _ = strings.Cut(value, " ")
		case "Source text SHA-256":
			digest = value
		}
	}
	if !receiptID.MatchString(receipt) || len(digest) != 64 {
		t.Fatalf("inspect printed no receipt and digest:\n%s", printed)
	}
	printed = strings.Replace(printed, receipt, shownReceipt, 1)
	return shownCommand + "\n" + strings.Replace(printed, digest, digest[:8]+"…", 1) + "\n"
}

// The block is compared with what inspect prints for the same number of the demo card: the card is made
// from the demo book through the maker the commands use, and traced and laid out by inspect's own code.
func TestReadmeShowsATracedNumber(t *testing.T) {
	shown := oracletest.ReadmeBlock(t, "$ gtm-connector inspect")
	crm, err := demoCRM(ctx, opening{})
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := demoCalls(ctx, opening{crm: crm})
	slack, _ := demoSlack(ctx, opening{crm: crm})
	from := reads{crm: crm, calls: calls, slack: slack, connections: map[string]string{}}
	log := receipts.NewMemory(nil, nil)
	for _, source := range sourceNames {
		from.connections[source] = "connection-" + source
		log.SetGeneration("readme", from.connections[source], 1)
	}
	maker := from.maker(nil)
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
	shown := oracletest.ReadmeBlock(t, "$ gtm-connector inspect")
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
