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

// CheckLines is whether lines are exactly a card's own. The signals come first, then the values, and there
// is at least one value, as every card prints RECORD VALUES. Rendered, they are the lines its text prints
// under ATTENTION and RECORD VALUES, byte for byte. Each line's text has the form its code states, each
// cites units of the records that code rests on, and each carries exactly the facts its code implies, which
// are what its text prints. units are the card's units as its bytes hold them; asOf is its as_of day, which
// a countdown counts from.
func CheckLines(lines []Line, text string, units []any, asOf string) error {
	signals, values := PrintedLines(text)
	if len(values) == 0 {
		return errors.New("the text prints no record values")
	}
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
		if err := l.citesItsRecords(units); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		if !l.form(asOf) {
			return fmt.Errorf("line %d: its text is not the form its code states", i+1)
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

// restsOn is the tables of the records each code's citations come from: the first citation's, then each
// other's. A code with no others cites exactly one unit.
var restsOn = map[string][2][]string{
	"renewal_date": {{"accounts"}}, "arr": {{"accounts"}}, "crm_risk": {{"accounts"}}, "account": {{"accounts"}},
	"crm_at_risk": {{"accounts"}}, "contract_end": {{"accounts", "contracts"}}, "opportunity": {{"opportunities"}},
	"contract": {{"contracts"}}, "contact": {{"contacts"}}, "contract_ended": {{"contracts"}},
	"renewal_passed":      {{"accounts", "contracts"}},
	"still_open":          {{"accounts", "contracts"}, {"opportunities"}},
	"close_after_renewal": {{"accounts", "contracts"}, {"opportunities"}},
	"contact_no_role":     {{"contacts"}, {"contacts"}},
	"call_stale":          {{"call_notes"}},
}

// unitRecord is the kind of a card's unit n and the source and table of the record it was read from, or
// empty strings when the card holds no unit n.
func unitRecord(units []any, n int) (kind, source, table string) {
	if n < 1 || n > len(units) {
		return "", "", ""
	}
	u, _ := units[n-1].(*strictjson.Object)
	if u == nil {
		return "", "", ""
	}
	k, _ := u.Get("kind")
	r, _ := u.Get("record_ref")
	kind, _ = k.(string)
	ref, _ := r.(string)
	if parts := strings.SplitN(ref, "/", 3); len(parts) == 3 {
		source, table = parts[0], parts[1]
	}
	return kind, source, table
}

// citesItsRecords is whether a line cites units of the records its code rests on: CRM records for every
// code but call_stale, which cites a call. A contract end line with a countdown cites the contract that
// ends; one without cites the account.
func (l Line) citesItsRecords(units []any) error {
	rule, known := restsOn[l.Code]
	if !known || len(l.Cites) == 0 {
		return errors.New("its code is not a line's")
	}
	if len(rule[1]) == 0 && len(l.Cites) != 1 ||
		(l.Code == "still_open" || l.Code == "close_after_renewal") && len(l.Cites) < 2 {
		return errors.New("it cites another number of units than its code does")
	}
	for i, n := range l.Cites {
		allowed := rule[0]
		if i > 0 {
			allowed = rule[1]
		}
		kind, source, table := unitRecord(units, n)
		call := table == "call_notes" && source == "calls"
		crm := source == "crm" && kind == "record"
		if !slices.Contains(allowed, table) || !call && !crm {
			return errors.New("it cites a unit of a record its code does not rest on")
		}
		if i == 0 && l.Code == "contract_end" && (l.Countdown != nil) != (table == "contracts") {
			return errors.New("its contract end cites another record than its countdown counts to")
		}
	}
	return nil
}

// cutDay reads a YYYY-MM-DD day off the front of s.
func cutDay(s string) (string, bool) {
	if len(s) < 10 {
		return s, false
	}
	_, err := Day(s[:10])
	return s[10:], err == nil
}

// cutCount reads a positive count, written without leading zeros, off the front of s.
func cutCount(s string) (int, string, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, ok := digits(s[:end])
	return n, s[end:], ok && end > 0 && end < 10 && s[0] != '0'
}

// cutDays reads "1 day" or "N days" off the front of s.
func cutDays(s string) (string, bool) {
	n, rest, ok := cutCount(s)
	unit := " days"
	if n == 1 {
		unit = " day"
	}
	if !ok || !strings.HasPrefix(rest, unit) || n == 1 && strings.HasPrefix(rest, " days") {
		return s, false
	}
	return rest[len(unit):], true
}

// what reads what a countdown counts to off the front of s: "renewal" or "contract end".
func what(s string) (string, bool) {
	for _, w := range []string{"renewal", "contract end"} {
		if rest, ok := strings.CutPrefix(s, w); ok {
			return rest, true
		}
	}
	return s, false
}

// when reads how far off the renewal is, as a signal says it, off the front of s, and whether it has passed.
func when(s string) (rest string, passed, ok bool) {
	if rest, ok = strings.CutPrefix(s, "The "); ok {
		rest, ok = what(rest)
	}
	if !ok {
		return s, false, false
	}
	if after, today := strings.CutPrefix(rest, " is today"); today {
		return after, false, true
	}
	if after, ahead := strings.CutPrefix(rest, " is in "); ahead {
		after, ok = cutDays(after)
		return after, false, ok
	}
	if after, gone := strings.CutPrefix(rest, " date passed "); gone {
		if after, ok = cutDays(after); ok {
			after, ok = strings.CutPrefix(after, " ago")
			return after, true, ok
		}
	}
	return s, false, false
}

// form is whether a line's text has the form its code states, with a count that agrees with its citations
// and its "and N more".
func (l Line) form(asOf string) bool {
	t, single := l.Text, l.More == 0
	switch l.Code {
	case "renewal_date":
		return l.Countdown != nil && t == l.Countdown.text()
	case "contract_end":
		if l.Countdown == nil {
			return t == "Missing: no contract ends on or after "+asOf
		}
		return strings.HasPrefix(t, l.Countdown.text()+"; status ")
	case "arr":
		return l.Amount != nil && t == l.Amount.text()
	case "crm_risk":
		head, _, _ := strings.Cut(t, "; reason ")
		return head == "healthy" || head == "at risk"
	case "account":
		return strings.Contains(t, "; industry ")
	case "opportunity":
		return strings.Contains(t, "; stage ") && strings.Contains(t, "; amount ") && strings.Contains(t, "; close date ")
	case "contract":
		return strings.HasPrefix(t, "status ") && (strings.HasSuffix(t, "; no end date") || strings.Contains(t, "; ends "))
	case "contact":
		return l.Contact != nil && strings.HasPrefix(t, l.Contact.opening())
	case "crm_at_risk":
		return single && t == "CRM marks this account at risk."
	case "contract_ended":
		rest, ok := strings.CutPrefix(t, "The latest contract end date, ")
		if ok {
			rest, ok = cutDay(rest)
		}
		if ok {
			rest, ok = strings.CutPrefix(rest, ", was ")
		}
		if ok {
			rest, ok = cutDays(rest)
		}
		return single && ok && rest == " ago."
	case "renewal_passed":
		rest, passed, ok := when(t)
		return single && ok && passed && rest == "."
	case "still_open":
		rest, _, ok := when(t)
		if ok {
			rest, ok = strings.CutPrefix(rest, " and ")
		}
		if !ok {
			return false
		}
		if single {
			return strings.HasPrefix(rest, "this opportunity is still open at stage ") && strings.HasSuffix(rest, ".")
		}
		n, rest, ok := cutCount(rest)
		return ok && n == len(l.Cites)-1+l.More && rest == " opportunities are still open."
	case "close_after_renewal":
		rest, ok, n := t, false, 1
		if single {
			rest, ok = strings.CutPrefix(rest, "This open opportunity's close date, ")
			if ok {
				rest, ok = cutDay(rest)
			}
			if ok {
				rest, ok = strings.CutPrefix(rest, ", is after the ")
			}
		} else if n, rest, ok = cutCount(rest); ok {
			rest, ok = strings.CutPrefix(rest, " open opportunities have a close date after the ")
		}
		if ok {
			rest, ok = what(rest)
		}
		if ok {
			rest, ok = strings.CutPrefix(rest, " date, ")
		}
		if ok {
			rest, ok = cutDay(rest)
		}
		return ok && rest == "." && n == len(l.Cites)-1+l.More
	case "contact_no_role":
		if single {
			return t == "This contact has no role recorded."
		}
		n, rest, ok := cutCount(t)
		return ok && n == len(l.Cites)+l.More && rest == " contacts have no role recorded."
	case "call_stale":
		rest, ok := strings.CutPrefix(t, "The latest dated call that was read is ")
		if ok {
			rest, ok = cutDays(rest)
		}
		return single && ok && rest == " old."
	}
	return false
}

// facts is whether a line carries exactly the facts its code implies, and whether they are what its text
// prints: a countdown counts the days from the card's day to its date, an amount is a decimal with two
// places in a three-letter currency or none, and a contact is the name and title its line opens with.
func (l Line) facts(asOf string) error {
	dated := l.Code == "renewal_date" || l.Code == "contract_end" && !strings.HasPrefix(l.Text, "Missing: ")
	if (l.Countdown != nil) != dated || (l.Amount != nil) != (l.Code == "arr") ||
		(l.Contact != nil) != (l.Code == "contact") {
		return errors.New("it does not carry the facts its code implies")
	}
	if c := l.Countdown; c != nil {
		date, errDate := Day(c.Date)
		from, errFrom := Day(asOf)
		if errDate != nil || errFrom != nil || daysBetween(date, from) != c.Days ||
			c.CountsTo != map[string]string{"renewal_date": "renewal", "contract_end": "contract_end"}[l.Code] {
			return errors.New("its countdown is not the days from the card's day to its date")
		}
	}
	if a := l.Amount; a != nil {
		whole, fraction, dotted := strings.Cut(a.Amount, ".")
		_, wholeOK := digits(whole)
		_, fractionOK := digits(fraction)
		if !dotted || whole == "" || !wholeOK || len(whole) > 1 && whole[0] == '0' || len(fraction) != 2 ||
			!fractionOK || a.Currency != nil && !currencyCode(*a.Currency) || l.Text != a.text() {
			return errors.New("its amount is not what its text prints")
		}
	}
	return nil
}
