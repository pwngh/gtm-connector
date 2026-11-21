package evidence

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// recordedCard is one recorded card: its key, its sealed text, its as_of day, its units and the evidence it
// seals, and the lines Build makes from that evidence.
type recordedCard struct {
	key, text, asOf string
	units           []any
	evidence        *strictjson.Object
	lines           []Line
}

// recordedCards builds the lines of every golden card of a family from the evidence the card seals.
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
		if row.Card == "" {
			continue
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
		out = append(out, recordedCard{row.Key, text.(string), asOf.(string), units.([]any),
			evidence.(*strictjson.Object), lines})
	}
	return out
}

// cited is the record a card's unit n was read from, as its evidence holds it.
func (c recordedCard) cited(n int) *strictjson.Object {
	unit := c.units[n-1].(*strictjson.Object)
	ref, _ := unit.Get("record_ref")
	records, _ := c.evidence.Get("records")
	for _, value := range records.([]any) {
		record := value.(*strictjson.Object)
		if got, _ := record.Get("record_ref"); got == ref {
			return record
		}
	}
	return nil
}

// shownField is a record's field as a card shows it as data: neutralized, on one line, nil when empty.
func shownField(record *strictjson.Object, key string) *string {
	value, _ := record.Get(key)
	if empty(value) {
		return nil
	}
	safe, _, err := SafeText(value.(string))
	if err != nil {
		return nil
	}
	line := oneLine(safe)
	return &line
}

// recordedFacts is whether a line's facts are its cited record's own fields, read here from the record and
// not from the line: a countdown's date is the account's renewal date or the contract's end date, and its
// days count from the card's day; an amount is the account's annual recurring revenue in cents, and its
// currency; a contact is the contact's name and title. A line whose code implies a fact must carry it, and
// no other line carries one.
func (c recordedCard) recordedFacts(l Line) bool {
	if len(l.Cites) == 0 || l.Cites[0] > len(c.units) {
		return false
	}
	record := c.cited(l.Cites[0])
	if record == nil {
		return false
	}
	text := func(key string) string { v, _ := record.Get(key); s, _ := v.(string); return s }
	same := func(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	switch {
	case l.Code == "renewal_date" || l.Code == "contract_end" && !strings.HasPrefix(l.Text, "Missing: "):
		key := map[string]string{"renewal_date": "renewal_date", "contract_end": "end_date"}[l.Code]
		date, err := Day(text(key))
		from, errFrom := Day(c.asOf)
		return l.Countdown != nil && err == nil && errFrom == nil && l.Countdown.Date == text(key) &&
			l.Countdown.Days == daysBetween(date, from)
	case l.Code == "arr":
		cents, _ := record.Get("arr_cents")
		n, isN := isInt(cents)
		currency, _ := record.Get("currency")
		var code *string
		if s, ok := currency.(string); ok {
			code = &s
		}
		if !isN {
			return false
		}
		whole, rest := new(big.Int).QuoRem(n, big.NewInt(100), new(big.Int))
		return l.Amount != nil && l.Amount.Amount == fmt.Sprintf("%s.%02d", whole, rest.Int64()) &&
			same(l.Amount.Currency, code)
	case l.Code == "contact":
		return l.Contact != nil && same(l.Contact.Name, shownField(record, "name")) &&
			same(l.Contact.Title, shownField(record, "title"))
	}
	return l.Countdown == nil && l.Amount == nil && l.Contact == nil
}

// TestLinesRenderTheRecordedText proves every golden card's lines its own. Rendered, they are byte for
// byte the lines its recorded text prints under ATTENTION and under RECORD VALUES. Their facts are their
// cited records' own fields, read from the evidence and not from the lines, so a builder that drops or
// changes a fact fails here. Stored as JSON and read back, they are the same lines. And CheckLines, the rule
// publication applies, admits them.
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
				if !card.recordedFacts(l) {
					mismatches.Add("%s: line %d's facts are not its record's", card.key, i+1)
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
	if cards != 90 {
		t.Errorf("%d golden cards, want 90", cards)
	}
	t.Logf("%d golden cards: %d signal lines and %d value lines render their golden text byte for byte; "+
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

// cloneLines copies lines, facts and all, so an edit changes none of the card's own.
func cloneLines(lines []Line) []Line {
	out := make([]Line, len(lines))
	for i, l := range lines {
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

// A line that differs from the card in any way is refused: its words, a cited number, its order, a cited
// unit past the card's or of another record, a code its sentence or its citations do not have, a fact its
// code implies left out, or a fact that is not what its text prints. A text that prints no record values
// takes no lines.
func TestCheckLinesRefusesLinesThatAreNotTheCards(t *testing.T) {
	var readme, dated recordedCard
	for _, c := range recordedCards(t, "cases") {
		if c.key == readmeCase {
			readme = c
		}
	}
	for _, c := range recordedCards(t, "books_salesforce") {
		for _, l := range c.lines {
			if l.Code == "contract_end" && l.Countdown != nil && dated.key == "" {
				dated = c
			}
		}
	}
	if readme.key == "" || dated.key == "" {
		t.Fatal("no README card or no Salesforce card with a dated contract end")
	}
	other := "Someone else"
	for _, c := range []struct {
		card  recordedCard
		edits map[string]func(at func(string) *Line, l []Line) []Line
	}{
		{readme, map[string]func(at func(string) *Line, l []Line) []Line{
			"a word":                  func(at func(string) *Line, l []Line) []Line { at("crm_risk").Text = "at risk"; return l },
			"a cited number":          func(at func(string) *Line, l []Line) []Line { at("arr").Cites = []int{2}; return l },
			"a unit past the card":    func(at func(string) *Line, l []Line) []Line { at("arr").Cites = []int{len(readme.units) + 1}; return l },
			"a line left out":         func(_ func(string) *Line, l []Line) []Line { return l[1:] },
			"a line added":            func(_ func(string) *Line, l []Line) []Line { return append(l, l[len(l)-1]) },
			"a value before a signal": func(_ func(string) *Line, l []Line) []Line { return append(l[1:], l[0]) },
			"the countdown's days":    func(at func(string) *Line, l []Line) []Line { at("renewal_date").Countdown.Days = 25; return l },
			"the countdown's date": func(at func(string) *Line, l []Line) []Line {
				at("renewal_date").Countdown.Date = "2027-02-09"
				return l
			},
			"the countdown left out": func(at func(string) *Line, l []Line) []Line { at("renewal_date").Countdown = nil; return l },
			"the amount":             func(at func(string) *Line, l []Line) []Line { at("arr").Amount.Amount = "450001.00"; return l },
			"the amount left out":    func(at func(string) *Line, l []Line) []Line { at("arr").Amount = nil; return l },
			"the currency":           func(at func(string) *Line, l []Line) []Line { at("arr").Amount.Currency = nil; return l },
			"the contact's name":     func(at func(string) *Line, l []Line) []Line { at("contact").Contact.Name = &other; return l },
			"the contact's title":    func(at func(string) *Line, l []Line) []Line { at("contact").Contact.Title = nil; return l },
			"the contact left out":   func(at func(string) *Line, l []Line) []Line { at("contact").Contact = nil; return l },
			"a signal's more count":  func(at func(string) *Line, l []Line) []Line { at("still_open").More = 1; return l },
			"a signal's code":        func(at func(string) *Line, l []Line) []Line { at("still_open").Code = "close_after_renewal"; return l },
			"a value's code":         func(at func(string) *Line, l []Line) []Line { at("crm_risk").Code = "account"; return l },
			"a value citing a contact": func(at func(string) *Line, l []Line) []Line {
				at("crm_risk").Cites = slices.Clone(at("contact").Cites)
				return l
			},
			"a countdown on another line": func(at func(string) *Line, l []Line) []Line {
				at("crm_risk").Countdown = at("renewal_date").Countdown
				return l
			},
		}},
		{dated, map[string]func(at func(string) *Line, l []Line) []Line{
			"the contract end's countdown left out": func(at func(string) *Line, l []Line) []Line {
				at("contract_end").Countdown = nil
				return l
			},
			"the contract end's countdown to a renewal": func(at func(string) *Line, l []Line) []Line {
				at("contract_end").Countdown.CountsTo = "renewal"
				return l
			},
		}},
	} {
		if err := CheckLines(c.card.lines, c.card.text, c.card.units, c.card.asOf); err != nil {
			t.Fatalf("%s: the card's own lines: %v", c.card.key, err)
		}
		for name, edit := range c.edits {
			lines := cloneLines(c.card.lines)
			at := func(code string) *Line {
				for i := range lines {
					if lines[i].Code == code {
						return &lines[i]
					}
				}
				t.Fatalf("%s: no %s line", c.card.key, code)
				return nil
			}
			if err := CheckLines(edit(at, lines), c.card.text, c.card.units, c.card.asOf); err == nil {
				t.Errorf("%s: admitted", name)
			}
		}
	}
	// The README card's at-risk signal under another code, with the same sentence and citations.
	risky := []Line{{Section: "signal", Code: "call_stale", Text: "CRM marks this account at risk.", Cites: []int{1}},
		{Section: "value", Code: "crm_risk", Label: "CRM risk", Text: "at risk", Cites: []int{1}}}
	printed := "DEAL CARD\n\n" + AttentionHeading + "\n" + RenderLines(risky)[0] + "\n\n" + ValuesHeading + "\n" +
		RenderLines(risky)[1] + "\n"
	if err := CheckLines(risky, printed, readme.units, readme.asOf); err == nil {
		t.Error("a signal under another code: admitted")
	}
	risky[0].Code = "crm_at_risk"
	if err := CheckLines(risky, printed, readme.units, readme.asOf); err != nil {
		t.Errorf("the same signal under its code: %v", err)
	}
	// A countdown counts from the card's own day.
	if err := CheckLines(readme.lines, readme.text, readme.units, "2027-01-14"); err == nil {
		t.Error("a countdown from another day: admitted")
	}
	// Every card prints RECORD VALUES, so a text without them is not a card's, even with no lines.
	if err := CheckLines(nil, "DEAL CARD\n", readme.units, readme.asOf); err == nil {
		t.Error("a text with no record values: admitted")
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
