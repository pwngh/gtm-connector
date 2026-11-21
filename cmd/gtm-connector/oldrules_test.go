package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A card sealed under rules version 3 still verifies against its stored bytes and its receipts, and inspect
// traces every number of it: each sentence states the version it was sealed under, and shows its text under
// today's rules. A text version 3 left as written is shown neutralized; the card's bytes are not changed.
func TestACardSealedUnderOlderRulesVerifiesAndShows(t *testing.T) {
	const brief = "brief-2430d066000040008000000000000001"
	folder, err := cards.OpenTrustedExport(oracletest.Path(t, "folders/cli_card"))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Clone(folder.Cards[brief])
	if !bytes.Contains(payload, []byte(`"rules_version": 3,`)) || evidence.SanitizerVersion == 3 {
		t.Fatal("the folder's card is not one sealed under older rules")
	}
	if _, err := folder.Verify(ctx, brief, demoSources); err != nil {
		t.Fatalf("the older card is refused: %v", err)
	}
	value, err := strictjson.Loads(payload)
	if err != nil {
		t.Fatal(err)
	}
	card := value.(*strictjson.Object)
	units := get(card, "units").([]any)
	numbers := make([]string, len(units))
	for i := range units {
		numbers[i] = strconv.Itoa(i + 1)
	}
	// One sentence holds the text a card sealed under version 3 held for a mail address in angle brackets.
	// The card is no longer the sealed one, so this part checks only what inspect shows.
	shownCard := strictjson.Clone(card).(*strictjson.Object)
	var changed int
	for i, value := range get(shownCard, "units").([]any) {
		if unit := value.(*strictjson.Object); get(unit, "kind") == "sentence" {
			unit.Set("text", "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) <jovan.mercer@quarrystream.example>")
			changed = i
			break
		}
	}
	for _, c := range []struct {
		card  *strictjson.Object
		label string
	}{{card, "sealed"}, {shownCard, "changed"}} {
		traced, err := traces(c.card, brief, numbers)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		sentences := 0
		for i, value := range traced.([]any) {
			unit := value.(*strictjson.Object)
			if get(unit, "kind") != "sentence" {
				continue
			}
			sentences++
			printed := (&run{command: "inspect", action: "numbers", screen: screen{terminal: true}}).human([]any{unit})
			if !strings.Contains(printed, "Neutralizer version   3") {
				t.Errorf("%s %d: inspect does not state version 3:\n%s", c.label, i+1, printed)
			}
			want, _, _ := evidence.SafeText(get(units[i].(*strictjson.Object), "text").(string))
			if c.label == "changed" && i == changed {
				want = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) [link mail]"
			}
			if get(unit, "text") != want {
				t.Errorf("%s %d: inspect shows %q, want %q", c.label, i+1, get(unit, "text"), want)
			}
		}
		if sentences == 0 {
			t.Errorf("%s: no sentence traced", c.label)
		}
	}
	if !bytes.Equal(folder.Cards[brief], payload) {
		t.Error("showing the card changed its bytes")
	}
}

// The CLI prints a card sealed under an older neutralizer version rendered again, with a first line that
// says so: a quotation that version left as written is printed as today's rules show it.
func TestTheCLIPrintsAnOlderCardRenderedAgain(t *testing.T) {
	const brief = "brief-2430d066000040008000000000000001"
	folder, err := cards.OpenTrustedExport(oracletest.Path(t, "folders/cli_card"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := strictjson.Loads(folder.Cards[brief])
	if err != nil {
		t.Fatal(err)
	}
	card := value.(*strictjson.Object)
	unit := get(card, "units").([]any)[7].(*strictjson.Object)
	sealed := "Write to <renewals@example.test> or orders@example.test for the order form."
	card.Set("text", strings.Replace(get(card, "text").(string), strictjson.Quote(get(unit, "text").(string)),
		strictjson.Quote(sealed), 1))
	unit.Set("text", sealed)
	printed := (&run{command: "brief", screen: screen{terminal: true}}).human(card)
	first, _, _ := strings.Cut(printed, "\n")
	if !strings.Contains(first, fmt.Sprintf("rendered again under neutralizer version %d", evidence.SanitizerVersion)) ||
		!strings.Contains(printed, `"Write to [link mail] or [link mail] for the order form."`) ||
		strings.Contains(printed, "renewals@example.test") || strings.Contains(printed, "orders@example.test") || !strings.Contains(printed, "Brief: "+brief) {
		t.Errorf("the CLI prints the older card as:\n%s", printed)
	}
}
