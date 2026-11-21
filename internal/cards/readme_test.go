package cards

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// README.md shows two blocks of the connector's output: the demo card and one traced number of it. This
// test takes the card out of the page and compares every byte of it with what the connector gives. The
// traced number is compared in cmd/gtm-connector, with what inspect prints.

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
	shown := oracletest.ReadmeBlock(t, "DEAL CARD")
	head, rest, found := strings.Cut(demoCard(t).Text, "\nSources read ")
	_, tail, ended := strings.Cut(rest, "\n")
	if !found || !ended {
		t.Fatal("the card has no line that says when its sources were read")
	}
	if text := head + "\n" + shownSourcesRead + "\n" + tail; text != shown {
		t.Errorf("the README card is not the card Go builds: %s", oracletest.FirstDifference(shown, text))
	}
}
