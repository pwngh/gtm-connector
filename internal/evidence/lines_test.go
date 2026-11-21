package evidence

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// recordedCard is one recorded card: its key, its sealed text, its as_of day, how many units it holds, and
// the lines Build makes from the evidence it seals.
type recordedCard struct {
	key, text, asOf string
	units           int
	lines           []Line
}

// recordedCards builds the lines of every recorded card of a family from the evidence the card seals.
func recordedCards(t *testing.T, family string) []recordedCard {
	t.Helper()
	var out []recordedCard
	for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
		var row struct {
			Key  string `json:"key"`
			Card string `json:"card"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		value, err := strictjson.LoadsText(row.Card)
		if err != nil {
			t.Fatal(err)
		}
		card := value.(*strictjson.Object)
		evidence, _ := card.Get("evidence")
		_, _, _, lines, err := Build(evidence.(*strictjson.Object))
		if err != nil {
			t.Fatalf("%s: refused: %v", row.Key, err)
		}
		text, _ := card.Get("text")
		asOf, _ := card.Get("as_of")
		units, _ := card.Get("units")
		out = append(out, recordedCard{row.Key, text.(string), asOf.(string), len(units.([]any)), lines})
	}
	return out
}

// TestLinesRenderTheRecordedText proves every recorded card's lines its own. Rendered, they are byte for
// byte the lines its recorded text prints under ATTENTION and under RECORD VALUES. Stored as JSON and read
// back, they are the same lines. And CheckLines, the rule publication applies, admits them.
func TestLinesRenderTheRecordedText(t *testing.T) {
	cards, signals, values := 0, 0, 0
	facts := map[string]int{}
	for family, want := range rebuilt {
		mismatches := oracletest.NewMismatches(t, "lines of cards/"+family)
		built := recordedCards(t, family)
		if len(built) != want {
			t.Errorf("%s: %d cards, want %d", family, len(built), want)
		}
		for _, card := range built {
			mismatches.Row()
			cards++
			printedSignals, printedValues := PrintedLines(card.text)
			if got, printed := RenderLines(card.lines), slices.Concat(printedSignals, printedValues); !slices.Equal(got, printed) {
				mismatches.Add("%s: the lines render %q, the text prints %q", card.key, got, printed)
				continue
			}
			if len(printedValues) == 0 {
				mismatches.Add("%s: the text prints no record values", card.key)
			}
			signals, values = signals+len(printedSignals), values+len(printedValues)
			if err := CheckLines(card.lines, card.text, card.units, card.asOf); err != nil {
				mismatches.Add("%s: %v", card.key, err)
			}
			for i, l := range card.lines {
				stored, err := strictjson.Dumps(l.Value(), strictjson.Prepared)
				if err != nil {
					t.Fatal(err)
				}
				value, err := strictjson.Loads([]byte(stored))
				if err != nil {
					t.Fatal(err)
				}
				again, err := ParseLine(value)
				if err != nil || again.render() != l.render() || !sameLine(again, l) {
					mismatches.Add("%s: line %d does not read back as itself: %v", card.key, i+1, err)
				}
				switch {
				case l.Countdown != nil:
					facts["countdown "+l.Countdown.CountsTo]++
				case l.Amount != nil:
					facts["amount"]++
				case l.Contact != nil:
					facts["contact"]++
				}
			}
		}
	}
	if cards != 450 {
		t.Errorf("%d recorded cards, want 450", cards)
	}
	t.Logf("%d recorded cards: %d signal lines and %d value lines render their recorded text byte for byte; "+
		"lines with facts: %v", cards, signals, values, facts)
}

// sameLine compares two lines field by field.
func sameLine(a, b Line) bool {
	x, errA := strictjson.Dumps(a.Value(), strictjson.Prepared)
	y, errB := strictjson.Dumps(b.Value(), strictjson.Prepared)
	return errA == nil && errB == nil && x == y
}

// readmeCase is the case whose card the README shows.
const readmeCase = "test_10_the_readme_shows_the_demo_card_byte_for_byte/1"

// The README's demo card holds its renewal date with its date and days, and its annual recurring revenue
// with its amount and currency, each citing the account, unit 1.
func TestTheReadmeCardsValuesAsData(t *testing.T) {
	for _, card := range recordedCards(t, "cases") {
		if card.key != readmeCase {
			continue
		}
		got := map[string]string{}
		for _, l := range card.lines {
			value, _ := strictjson.Dumps(l.Value(), strictjson.Prepared)
			got[l.Code] = value
		}
		for code, want := range map[string]string{
			"renewal_date": `{"amount":null,"cites":[1],"code":"renewal_date","contact":null,"countdown":{"counts_to":"renewal","date":"2027-02-08","days":24},"label":"Renewal date","more":0,"section":"value","text":"2027-02-08, in 24 days"}`,
			"arr":          `{"amount":{"amount":"450000.00","currency":"USD"},"cites":[1],"code":"arr","contact":null,"countdown":null,"label":"Annual recurring revenue","more":0,"section":"value","text":"USD 450,000.00"}`,
			"contact":      `{"amount":null,"cites":[2],"code":"contact","contact":{"name":"Marian Holt","title":"Deputy general counsel"},"countdown":null,"label":"Contact","more":0,"section":"value","text":"\"Marian Holt\"; role \"Deputy general counsel\"; email \"marian@thornfield-wren.example\""}`,
			"still_open":   `{"amount":null,"cites":[1,3],"code":"still_open","contact":null,"countdown":null,"label":null,"more":0,"section":"signal","text":"The renewal is in 24 days and this opportunity is still open at stage \"negotiation\"."}`,
		} {
			if got[code] != want {
				t.Errorf("%s:\n got %s\nwant %s", code, got[code], want)
			}
		}
		return
	}
	t.Fatalf("no recorded case %q", readmeCase)
}

// A line that differs from the text in any way is refused: its words, a cited number, its order, a cited
// unit past the card's, or a fact that is not what its text prints.
func TestCheckLinesRefusesLinesThatAreNotTheTexts(t *testing.T) {
	var card recordedCard
	for _, c := range recordedCards(t, "cases") {
		if c.key == readmeCase {
			card = c
		}
	}
	if err := CheckLines(card.lines, card.text, card.units, card.asOf); err != nil {
		t.Fatalf("the card's own lines: %v", err)
	}
	clone := func() []Line {
		out := make([]Line, len(card.lines))
		for i, l := range card.lines {
			out[i] = l
			out[i].Cites = slices.Clone(l.Cites)
			if l.Countdown != nil {
				c := *l.Countdown
				out[i].Countdown = &c
			}
			if l.Amount != nil {
				a := *l.Amount
				out[i].Amount = &a
			}
			if l.Contact != nil {
				p := *l.Contact
				out[i].Contact = &p
			}
		}
		return out
	}
	at := func(lines []Line, code string) *Line {
		for i := range lines {
			if lines[i].Code == code {
				return &lines[i]
			}
		}
		t.Fatalf("no %s line", code)
		return nil
	}
	other := "Someone else"
	for name, edit := range map[string]func([]Line) []Line{
		"a word":               func(l []Line) []Line { at(l, "crm_risk").Text = "at risk"; return l },
		"a cited number":       func(l []Line) []Line { at(l, "arr").Cites = []int{2}; return l },
		"a unit past the card": func(l []Line) []Line { at(l, "arr").Cites = []int{card.units + 1}; return l },
		"a line left out":      func(l []Line) []Line { return l[1:] },
		"a line added":         func(l []Line) []Line { return append(l, l[len(l)-1]) },
		"a value before a signal": func(l []Line) []Line {
			return append(l[1:], l[0])
		},
		"the countdown's days":  func(l []Line) []Line { at(l, "renewal_date").Countdown.Days = 25; return l },
		"the countdown's date":  func(l []Line) []Line { at(l, "renewal_date").Countdown.Date = "2027-02-09"; return l },
		"the amount":            func(l []Line) []Line { at(l, "arr").Amount.Amount = "450001.00"; return l },
		"the currency":          func(l []Line) []Line { at(l, "arr").Amount.Currency = nil; return l },
		"the contact's name":    func(l []Line) []Line { at(l, "contact").Contact.Name = &other; return l },
		"the contact's title":   func(l []Line) []Line { at(l, "contact").Contact.Title = nil; return l },
		"a signal's more count": func(l []Line) []Line { at(l, "still_open").More = 1; return l },
	} {
		if err := CheckLines(edit(clone()), card.text, card.units, card.asOf); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	// A countdown counts from the card's own day.
	if err := CheckLines(card.lines, card.text, card.units, "2027-01-14"); err == nil {
		t.Error("a countdown from another day: admitted")
	}
}

// ParseLine reads only the shape Value writes.
func TestParseLineRefusesOtherShapes(t *testing.T) {
	good := Line{Section: "value", Code: "renewal_date", Label: "Renewal date", Text: "2027-02-08, in 24 days",
		Cites: []int{1}, Countdown: &Countdown{Date: "2027-02-08", Days: 24, CountsTo: "renewal"}}
	encoded, _ := strictjson.Dumps(good.Value(), strictjson.Prepared)
	if value, _ := strictjson.Loads([]byte(encoded)); func() error { _, err := ParseLine(value); return err }() != nil {
		t.Fatal("the line Value writes is refused")
	}
	for name, change := range map[string][2]string{
		"an unknown code":             {`"code":"renewal_date"`, `"code":"renewal"`},
		"a code of the other section": {`"section":"value"`, `"section":"signal"`},
		"another label":               {`"label":"Renewal date"`, `"label":"Renewal"`},
		"no cites":                    {`"cites":[1]`, `"cites":[]`},
		"a cite of zero":              {`"cites":[1]`, `"cites":[0]`},
		"a cite as text":              {`"cites":[1]`, `"cites":["1"]`},
		"a more count on a value":     {`"more":0`, `"more":2`},
		"an empty text":               {`"text":"2027-02-08, in 24 days"`, `"text":""`},
		"no countdown":                {`"countdown":{"counts_to":"renewal","date":"2027-02-08","days":24}`, `"countdown":null`},
		"a countdown to a contract":   {`"counts_to":"renewal"`, `"counts_to":"contract_end"`},
		"an amount on this line":      {`"amount":null`, `"amount":{"amount":"1.00","currency":null}`},
		"a key more":                  {`"more":0`, `"more":0,"extra":1`},
	} {
		changed := strings.Replace(encoded, change[0], change[1], 1)
		if changed == encoded {
			t.Fatalf("%s: the change does not apply", name)
		}
		value, err := strictjson.Loads([]byte(changed))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseLine(value); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}
