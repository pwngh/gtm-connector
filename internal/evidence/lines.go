package evidence

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The lines a card prints under ATTENTION and RECORD VALUES, kept as data beside the text they become. Build
// makes each Line from the same values its text is made from, and the card prints RenderLines of them, so a
// rendering of the card reads its values and signals from Lines and never from the text.

// The headings of the two sections whose lines are Lines.
const (
	AttentionHeading = "ATTENTION, from record fields and dates"
	ValuesHeading    = "RECORD VALUES, from CRM fields and connector arithmetic"
)

// Line is one line under ATTENTION (a signal) or RECORD VALUES (a value).
type Line struct {
	Section string // "signal" or "value"
	Code    string // what the line states: one of LineCodes
	Label   string // a value line's label, as printed; empty for a signal
	Text    string // the value, or the signal's sentence, in the connector's words
	Cites   []int  // the numbers of the units it cites, in order
	More    int    // a signal's "and N more": how many it counts past those it cites; 0 for none
	// Countdown is set on the renewal date line and on a contract end line that names a contract.
	Countdown *Countdown
	// Amount is set on the annual recurring revenue line.
	Amount *Amount
	// Contact is set on a contact line.
	Contact *Person
}

// Countdown is the day a renewal counts to and how far off it is.
type Countdown struct {
	Date string // YYYY-MM-DD
	Days int    // from the card's as_of day to Date; negative once Date has passed
	// CountsTo is the kind of record Date is on: "renewal", an account's renewal date, or "contract_end", the
	// end date of the contract that ends next.
	CountsTo string
}

// Amount is a figure of a CRM record and its currency.
type Amount struct {
	Amount   string  // a decimal with two places and no grouping, such as "450000.00"
	Currency *string // a three-letter code, nil when the record has none
}

// Person is a contact's name and title as the card shows them: neutralized, on one line, and nil where the
// record's field is empty.
type Person struct {
	Name, Title *string
}

// LineCodes is every code a line can carry, with its section.
var LineCodes = map[string]string{
	"renewal_date": "value", "contract_end": "value", "arr": "value", "crm_risk": "value", "account": "value",
	"opportunity": "value", "contract": "value", "contact": "value",
	"crm_at_risk": "signal", "contract_ended": "signal", "renewal_passed": "signal", "still_open": "signal",
	"close_after_renewal": "signal", "contact_no_role": "signal", "call_stale": "signal",
}

// valueLabels is the label each value line prints.
var valueLabels = map[string]string{
	"renewal_date": "Renewal date", "contract_end": "Contract end", "arr": "Annual recurring revenue",
	"crm_risk": "CRM risk", "account": "Account", "opportunity": "Opportunity", "contract": "Contract",
	"contact": "Contact",
}

// timing is how far off a day is, as the card prints it.
func timing(n int) string {
	switch {
	case n > 0:
		return "in " + days(n)
	case n < 0:
		return days(-n) + " ago"
	}
	return "today"
}

// text is the countdown as its line prints it: the date and how far off it is.
func (c Countdown) text() string { return c.Date + ", " + timing(c.Days) }

// groupedDecimal is a decimal with its whole part grouped by thousands.
func groupedDecimal(amount string) string {
	whole, fraction, _ := strings.Cut(amount, ".")
	n, ok := new(big.Int).SetString(whole, 10)
	if !ok {
		return amount
	}
	return grouped(n) + "." + fraction
}

// text is the amount as its line prints it.
func (a Amount) text() string {
	var currency any
	if a.Currency != nil {
		currency = *a.Currency
	}
	return money(groupedDecimal(a.Amount), currency)
}

// printedField is a field as the card prints it: quoted, or Missing.
func printedField(value *string) string {
	if value == nil {
		return "Missing"
	}
	return strictjson.Quote(*value)
}

// opening is how a contact line starts: its name and its role.
func (p Person) opening() string {
	return printedField(p.Name) + "; role " + printedField(p.Title) + "; email "
}

// render is the line as the card's text prints it.
func (l Line) render() string {
	if l.Section == "value" {
		return "  " + l.Label + ": " + l.Text + " " + cite(l.Cites)
	}
	line := "  " + l.Text + " " + cite(l.Cites)
	if l.More > 0 {
		line += fmt.Sprintf(" and %d more", l.More)
	}
	return line
}

// RenderLines is the lines in the text card's own format, one string each.
func RenderLines(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.render()
	}
	return out
}

// PrintedLines is what a card's text prints under ATTENTION and under RECORD VALUES. It is how stored lines
// are checked against the sealed text; no rendering reads a value from it.
func PrintedLines(text string) (signals, values []string) {
	all := strings.Split(text, "\n")
	under := func(heading string) []string {
		at := slices.Index(all, heading)
		if at < 0 {
			return nil
		}
		var out []string
		for _, line := range all[at+1:] {
			if line == "" {
				break
			}
			out = append(out, line)
		}
		return out
	}
	return under(AttentionHeading), under(ValuesHeading)
}

func optionalText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// Value is the line as a card's projection stores it and the API serves it.
func (l Line) Value() *strictjson.Object {
	var label, countdown, amount, contact any
	if l.Section == "value" {
		label = l.Label
	}
	cites := make([]any, len(l.Cites))
	for i, n := range l.Cites {
		cites[i] = n
	}
	if l.Countdown != nil {
		countdown = strictjson.NewObject("date", l.Countdown.Date, "days", l.Countdown.Days,
			"counts_to", l.Countdown.CountsTo)
	}
	if l.Amount != nil {
		amount = strictjson.NewObject("amount", l.Amount.Amount, "currency", optionalText(l.Amount.Currency))
	}
	if l.Contact != nil {
		contact = strictjson.NewObject("name", optionalText(l.Contact.Name), "title", optionalText(l.Contact.Title))
	}
	return strictjson.NewObject("section", l.Section, "code", l.Code, "label", label, "text", l.Text,
		"cites", cites, "more", l.More, "countdown", countdown, "amount", amount, "contact", contact)
}

// ErrLine is a stored line that is not one Build makes.
var ErrLine = errors.New("evidence: not a card line")

// exactly reads an object with exactly these keys.
func exactly(value any, keys ...string) (*strictjson.Object, bool) {
	o, ok := value.(*strictjson.Object)
	if !ok || o == nil || len(o.Keys) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if _, found := o.Get(key); !found {
			return nil, false
		}
	}
	return o, true
}

// smallInt is a JSON integer that fits an int32.
func smallInt(value any) (int, bool) {
	n, ok := isInt(value)
	if !ok || !n.IsInt64() || n.Int64() < -1<<31 || n.Int64() >= 1<<31 {
		return 0, false
	}
	return int(n.Int64()), true
}

// textOrNull reads a string or null.
func textOrNull(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	s, ok := value.(string)
	return &s, ok
}

// ParseLine reads a line as Value wrote it. It refuses any other shape: a key more or less, a mistyped
// field, an unknown code, a label other than its code's, or facts on a line that does not carry them.
func ParseLine(value any) (Line, error) {
	o, ok := exactly(value, "section", "code", "label", "text", "cites", "more", "countdown", "amount", "contact")
	if !ok {
		return Line{}, ErrLine
	}
	get := func(key string) any { v, _ := o.Get(key); return v }
	var l Line
	var okSection, okCode, okText, okMore bool
	l.Section, okSection = get("section").(string)
	l.Code, okCode = get("code").(string)
	l.Text, okText = get("text").(string)
	l.More, okMore = smallInt(get("more"))
	if !okSection || !okCode || !okText || !okMore || l.Text == "" || LineCodes[l.Code] != l.Section || l.More < 0 ||
		l.Section == "value" && l.More != 0 {
		return Line{}, ErrLine
	}
	if l.Section == "value" {
		label, isText := get("label").(string)
		if !isText || label != valueLabels[l.Code] {
			return Line{}, ErrLine
		}
		l.Label = label
	} else if get("label") != nil {
		return Line{}, ErrLine
	}
	cites, isList := get("cites").([]any)
	if !isList || len(cites) == 0 {
		return Line{}, ErrLine
	}
	for _, value := range cites {
		n, isN := smallInt(value)
		if !isN || n < 1 {
			return Line{}, ErrLine
		}
		l.Cites = append(l.Cites, n)
	}
	if countdown := get("countdown"); countdown != nil {
		c, isObject := exactly(countdown, "date", "days", "counts_to")
		if !isObject || l.Code != "renewal_date" && l.Code != "contract_end" {
			return Line{}, ErrLine
		}
		date, _ := c.Get("date")
		days, _ := c.Get("days")
		to, _ := c.Get("counts_to")
		var cd Countdown
		var okDate, okDays, okTo bool
		cd.Date, okDate = date.(string)
		cd.Days, okDays = smallInt(days)
		cd.CountsTo, okTo = to.(string)
		if !okDate || !okDays || !okTo || cd.CountsTo != map[string]string{"renewal_date": "renewal",
			"contract_end": "contract_end"}[l.Code] {
			return Line{}, ErrLine
		}
		l.Countdown = &cd
	} else if l.Code == "renewal_date" {
		return Line{}, ErrLine
	}
	if amount := get("amount"); amount != nil {
		a, isObject := exactly(amount, "amount", "currency")
		if !isObject || l.Code != "arr" {
			return Line{}, ErrLine
		}
		figure, _ := a.Get("amount")
		currency, _ := a.Get("currency")
		var okFigure, okCurrency bool
		var parsed Amount
		parsed.Amount, okFigure = figure.(string)
		parsed.Currency, okCurrency = textOrNull(currency)
		if !okFigure || !okCurrency {
			return Line{}, ErrLine
		}
		l.Amount = &parsed
	} else if l.Code == "arr" {
		return Line{}, ErrLine
	}
	if contact := get("contact"); contact != nil {
		p, isObject := exactly(contact, "name", "title")
		if !isObject || l.Code != "contact" {
			return Line{}, ErrLine
		}
		name, _ := p.Get("name")
		title, _ := p.Get("title")
		var person Person
		var okName, okTitle bool
		person.Name, okName = textOrNull(name)
		person.Title, okTitle = textOrNull(title)
		if !okName || !okTitle {
			return Line{}, ErrLine
		}
		l.Contact = &person
	} else if l.Code == "contact" {
		return Line{}, ErrLine
	}
	return l, nil
}

// CheckLines is whether lines are exactly a card's own: the signals first, then the values; together they
// render the lines its text prints under ATTENTION and RECORD VALUES, byte for byte; each cites a unit of
// the card; and each line's facts are what its text prints. asOf is the card's as_of day, which a countdown
// counts from.
func CheckLines(lines []Line, text string, units int, asOf string) error {
	signals, values := PrintedLines(text)
	var gotSignals, gotValues []Line
	for i, l := range lines {
		if l.Section == "signal" && len(gotValues) > 0 {
			return fmt.Errorf("line %d is a signal after a value", i+1)
		}
		if l.Section == "signal" {
			gotSignals = append(gotSignals, l)
		} else {
			gotValues = append(gotValues, l)
		}
		for _, n := range l.Cites {
			if n < 1 || n > units {
				return fmt.Errorf("line %d cites a unit the card does not hold", i+1)
			}
		}
		if err := l.facts(asOf); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	if !slices.Equal(RenderLines(gotSignals), signals) {
		return errors.New("the signal lines do not render the lines the text prints under ATTENTION")
	}
	if !slices.Equal(RenderLines(gotValues), values) {
		return errors.New("the value lines do not render the lines the text prints under RECORD VALUES")
	}
	return nil
}

// facts is whether a line's countdown, amount or contact is what its text prints.
func (l Line) facts(asOf string) error {
	if c := l.Countdown; c != nil {
		date, errDate := Day(c.Date)
		from, errFrom := Day(asOf)
		if errDate != nil || errFrom != nil || daysBetween(date, from) != c.Days {
			return errors.New("its countdown is not the days from the card's day to its date")
		}
		if l.Code == "renewal_date" && l.Text != c.text() ||
			l.Code == "contract_end" && !strings.HasPrefix(l.Text, c.text()+"; status ") {
			return errors.New("its countdown is not what its text prints")
		}
	}
	if a := l.Amount; a != nil && (!decimalText(a.Amount) || strings.HasPrefix(a.Amount, "-") || l.Text != a.text()) {
		return errors.New("its amount is not what its text prints")
	}
	if p := l.Contact; p != nil && !strings.HasPrefix(l.Text, p.opening()) {
		return errors.New("its contact is not what its text prints")
	}
	return nil
}
