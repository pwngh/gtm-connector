package evidence

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// keptAddress is a quotation as a card sealed under neutralizer version 3 could hold it, with a mail address
// in angle brackets and a bare one, which today's rules replace, and shownAddress is the same quotation under
// today's rules.
const (
	keptAddress  = "Write to <renewals@example.test> or orders@example.test for the order form."
	shownAddress = "Write to [link mail] or [link mail] for the order form."
)

// olderCard is the recorded card of folders/cli_card, sealed under version 3, with its unit 8 holding
// keptAddress, in its units and in its text.
func olderCard(t *testing.T) (*strictjson.Object, string) {
	t.Helper()
	data, err := os.ReadFile(oracletest.Path(t, "folders/cli_card/briefs/brief-2430d066000040008000000000000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := strictjson.Loads(data)
	if err != nil {
		t.Fatal(err)
	}
	card := value.(*strictjson.Object)
	unit := member(card, "units").([]any)[7].(*strictjson.Object)
	was := member(unit, "text").(string)
	unit.Set("text", keptAddress)
	text := strings.Replace(member(card, "text").(string), strictjson.Quote(was), strictjson.Quote(keptAddress), 1)
	if text == member(card, "text") {
		t.Fatal("the card does not print unit 8")
	}
	card.Set("text", text)
	return card, text
}

// A card with a sentence sealed under an older neutralizer version is given to a model or a terminal rendered
// again: a first line that says so, each quotation from its unit as AsShown gives it, and every other line
// as sealed where today's rules leave it. The sealed text in the card is not changed.
func TestAnOlderCardsTextIsRenderedAgain(t *testing.T) {
	card, sealed := olderCard(t)
	text, err := AsShownText(card)
	if err != nil {
		t.Fatal(err)
	}
	got, want := strings.Split(text, "\n"), strings.Split(sealed, "\n")
	if len(got) != len(want)+1 || !strings.Contains(got[0], fmt.Sprintf("rendered again under neutralizer version %d", SanitizerVersion)) ||
		!strings.Contains(got[0], "sealed text is unchanged") {
		t.Fatalf("the text rendered again:\n%s", text)
	}
	for i, line := range want {
		if line == "        "+strictjson.Quote(keptAddress) {
			line = "        " + strictjson.Quote(shownAddress)
		}
		if got[i+1] != line {
			t.Errorf("line %d: %q, want %q", i+1, got[i+1], line)
		}
	}
	if strings.Contains(text, "renewals@example.test") || member(card, "text") != sealed {
		t.Error("the address is shown, or the sealed text was changed")
	}
	// A card whose quotation does not print its unit is refused, never rendered from either.
	unit := member(card, "units").([]any)[7].(*strictjson.Object)
	unit.Set("text", "Something else.")
	if _, err := AsShownText(card); err != ErrCardText {
		t.Errorf("a card whose text does not print its unit: %v", err)
	}
}

// A card made under today's rules is given its sealed text exactly.
func TestACurrentCardsTextIsItsSealedText(t *testing.T) {
	_, text := built(t, "We will see.", keptAddress)
	value, err := strictjson.Loads(oracletest.Lines(t, "cards/demo.jsonl.gz")[0])
	if err != nil {
		t.Fatal(err)
	}
	card, err := strictjson.LoadsText(member(value, "card").(string))
	if err != nil {
		t.Fatal(err)
	}
	if shown, err := AsShownText(card.(*strictjson.Object)); err != nil || shown != member(card, "text") {
		t.Errorf("a current card's text is not its sealed text: %v", err)
	}
	if !strings.Contains(text, strictjson.Quote(shownAddress)) {
		t.Errorf("a card made now does not neutralize the address:\n%s", text)
	}
}
