package evidence

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// card is units._Card: units in a fixed order and the lines that cite them.
type card struct {
	evidence      *strictjson.Object
	groups        map[string][]*strictjson.Object
	units         []Unit
	neutralized   Counts
	missing       int
	values        []string
	signals       []string
	gaps          []Gap
	titles        map[int]string
	unshownTitles map[int]any
}

// lines is source text neutralized, with what was replaced counted.
func (c *card) lines(value any) string {
	raw, ok := value.(string)
	if !ok {
		reject("Evidence text fields must be strings.")
	}
	safe, counts, err := SafeText(raw)
	if err != nil {
		reject("Evidence text fields must be strings.")
	}
	c.neutralized.Mentions += counts.Mentions
	c.neutralized.Links += counts.Links
	c.neutralized.Controls += counts.Controls
	c.neutralized.Brackets += counts.Brackets
	return safe
}

// oneLine is " ".join(text.split()).
func oneLine(text string) string {
	words := fields([]rune(text))
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = string(w)
	}
	return strings.Join(parts, " ")
}

func (c *card) line(value any) string { return oneLine(c.lines(value)) }

func empty(value any) bool { return value == nil || value == "" }

// quoted is one field as a quoted literal on one line, or Missing.
func (c *card) quoted(value any) string {
	if empty(value) {
		return "Missing"
	}
	return strictjson.Quote(c.line(value))
}

// text is a displayed field; an empty one is counted once, where it is shown.
func (c *card) text(value any) string {
	if empty(value) {
		c.missing++
	}
	return c.quoted(value)
}

func (c *card) record(row *strictjson.Object, table string) int {
	rule := sourceRules[table]
	c.units = append(c.units, Unit{N: len(c.units) + 1, Kind: "record", Source: rule.system,
		RecordRef: str(field(row, "record_ref")), Tool: rule.tool})
	return len(c.units)
}

func (c *card) gap(code, text string, count *int, first bool, byRole *RoleCounts) {
	g := Gap{Code: code, Disposition: GapCodes[code], Count: count, ByRole: byRole, Text: text}
	if first {
		c.gaps = append([]Gap{g}, c.gaps...)
	} else {
		c.gaps = append(c.gaps, g)
	}
}

func cite(numbers []int) string {
	var b strings.Builder
	for _, n := range numbers {
		fmt.Fprintf(&b, "[%d]", n)
	}
	return b.String()
}

func (c *card) value(label, value string, numbers ...int) {
	c.values = append(c.values, "  "+label+": "+value+" "+cite(numbers))
}

func (c *card) signal(sentence string, more int, numbers ...int) {
	line := "  " + sentence + " " + cite(numbers)
	if more > 0 {
		line += fmt.Sprintf(" and %d more", more)
	}
	c.signals = append(c.signals, line)
}

func intp(n int) *int { return &n }

// ---- speech ----

// part is one piece of a record's speech: (speaker label, text, start ms, end ms, cue or line).
type part struct {
	label      any
	raw        string
	start, end *int
	cue        *int
}

type piece struct {
	raw        string
	cue        *int
	start, end *int
}

type run struct {
	label  *string
	raw    string
	end    *int
	pieces []piece
}

// label is units._label: a label on one line, or "" when it is not a string.
func label(value any) string {
	if s, ok := value.(string); ok {
		return oneLine(s)
	}
	return ""
}

func labelKey(l *string) string {
	if l == nil {
		return "\x00"
	}
	return "=" + *l
}

type role struct {
	role, basis string
	contact     *int
}

type contact struct {
	name any
	n    int
}

var noContact = [2]string{"listed seller identity", "label matches no listed seller and no contact"}

// speakerRole is units.speaker_role: a role from identity only.
func speakerRole(value any, sellers []string, contacts []contact, slack bool) role {
	name := label(value)
	if name == "" {
		return role{"unknown", "no speaker label", nil}
	}
	var matches []contact
	if !slack {
		for _, c := range contacts {
			if label(c.name) == name {
				matches = append(matches, c)
			}
		}
	}
	listed := false
	for _, seller := range sellers {
		listed = listed || label(seller) == name
	}
	switch {
	case listed && len(matches) > 0:
		return role{"unknown", "label matches a listed seller and a contact", nil}
	case listed:
		return role{"seller", noContact[0], nil}
	case len(matches) == 1:
		n := matches[0].n
		return role{"customer", fmt.Sprintf("label matches contact [%d]", n), &n}
	case len(matches) > 1:
		return role{"unknown", fmt.Sprintf("label matches %d contacts", len(matches)), nil}
	case slack:
		return role{"unknown", "author is not a listed seller; Slack identities cannot show a customer", nil}
	}
	return role{"unknown", noContact[1], nil}
}

const closers = "\"')]”’»"

// unfinished is units._unfinished: transcript text that stops mid-sentence.
func unfinished(text string) bool {
	s := strip([]rune(text))
	n := len(s)
	if _, ok := Ended(s, 0, n); !ok {
		return true
	}
	t := strings.TrimRight(string(s), closers)
	if strings.HasSuffix(t, "..") || ListMarkerFull(s, 0, n) {
		return true
	}
	_, last := NeverLast(s, max(n-24, 0), n)
	return last && !NumberSignFull(s, 0, n)
}

// goesOn is units._goes_on: text that opens with a lower-case letter.
func goesOn(text string) bool {
	s := []rune(text)
	for len(s) > 0 && u.IsSpace(s[0]) {
		s = s[1:]
	}
	return len(s) > 0 && u.IsLower(s[0])
}

func ended(sentence string) bool {
	s := []rune(sentence)
	_, ok := Ended(s, 0, len(s))
	return ok
}

func wordsLength(text string) int {
	n := 0
	for _, w := range fields([]rune(text)) {
		n += len(w)
	}
	return n
}

// spoken is _Card.spoken: the units of one record's speech. It returns the number of its first unit.
func (c *card) spoken(row *strictjson.Object, table string, parts []part, at any, roleOf func(*string) role,
	transcript, openStart bool) int {
	var shownAt *string
	if at != nil {
		if text, ok := at.(string); ok && PlainInstant(text) {
			shownAt = &text
		} else if text := c.line(at); text != "" {
			shownAt = &text
		}
	}
	var runs []*run
	for _, p := range parts {
		var l *string
		if text := label(p.label); text != "" {
			l = &text
		}
		var last *run
		if len(runs) > 0 {
			last = runs[len(runs)-1]
		}
		var gap *int
		if last != nil && p.start != nil && last.end != nil {
			gap = intp(*p.start - *last.end)
		}
		if last != nil && l != nil && last.label != nil && *last.label == *l && gap != nil &&
			(abs(*gap) <= runGapMS || 0 < *gap && *gap <= bridgeGapMS && unfinished(last.raw) && goesOn(p.raw)) {
			last.raw, last.end = last.raw+"\n"+p.raw, p.end
			tail := &last.pieces[len(last.pieces)-1]
			if sameCue(tail.cue, p.cue) {
				tail.raw, tail.end = tail.raw+"\n"+p.raw, p.end
			} else {
				last.pieces = append(last.pieces, piece{p.raw, p.cue, p.start, p.end})
			}
		} else {
			runs = append(runs, &run{l, p.raw, p.end, []piece{{p.raw, p.cue, p.start, p.end}}})
		}
	}
	before := len(c.units)
	body := str(field(row, "body"))
	rawDigest := digest(body)
	waiting, heard := map[string]bool{}, map[string]bool{}
	cut := false
	rule := sourceRules[table]
	for _, r := range runs {
		who := roleOf(r.label)
		var speaker *string
		if r.label != nil {
			if text := c.line(*r.label); text != "" {
				speaker = &text
			}
		}
		if speaker != nil && length(*speaker) > MaxLabel {
			speaker, who = nil, role{"unknown", fmt.Sprintf("speaker label over %d characters", MaxLabel), nil}
		}
		texts := make([]string, len(r.pieces))
		for i, p := range r.pieces {
			texts[i] = c.lines(p.raw)
		}
		safe := strings.Join(texts, "\n")
		found, err := Sentences(safe, table == "messages")
		if err != nil {
			panic(invalid)
		}
		ends := make([]int, len(texts))
		total := 0
		for i, text := range texts {
			total += wordsLength(text)
			ends[i] = total
		}
		reached, size := 0, 0
		key := labelKey(r.label)
		follows := openStart && !heard[key] || waiting[key] || cut && goesOn(safe)
		if len(found) > 0 {
			delete(waiting, key)
			heard[key] = true
		}
		for position, sentence := range found {
			if transcript {
				if position == len(found)-1 {
					cut = unfinished(sentence)
				} else {
					cut = !ended(sentence)
				}
			} else {
				cut = false
			}
			partial := follows || cut
			follows = cut
			reached, size = reached+size, wordsLength(sentence)
			from := sort.Search(len(ends), func(i int) bool { return ends[i] > reached })
			to := min(sort.Search(len(ends), func(i int) bool { return ends[i] >= reached+size })+1, len(r.pieces))
			if from >= to {
				panic(invalid)
			}
			lies := r.pieces[from:to]
			shown := "more"
			switch {
			case partial:
				shown = "cut"
			case length(sentence) > MaxSentence:
				shown = "long"
			}
			var span *UnitSpan
			if lies[0].cue != nil {
				span = &UnitSpan{First: *lies[0].cue, Last: deref(lies[len(lies)-1].cue)}
				if lies[0].start != nil {
					span.StartMS = intp(extreme(lies, func(p piece) *int { return p.start }, false))
				}
				if lies[0].end != nil {
					span.EndMS = intp(extreme(lies, func(p piece) *int { return p.end }, true))
				}
			}
			c.units = append(c.units, Unit{N: len(c.units) + 1, Kind: "sentence", Source: rule.system,
				RecordRef: str(field(row, "record_ref")), Tool: rule.tool, At: shownAt, Speaker: speaker,
				Role: who.role, Basis: who.basis, Contact: who.contact, Text: sentence, Partial: partial,
				Shown: shown, Span: span, RawSHA256: rawDigest, SanitizerVersion: SanitizerVersion})
		}
		if len(found) > 0 && cut {
			waiting[key] = true
		}
	}
	if len(c.units) == before {
		c.record(row, table)
	}
	return before + 1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sameCue(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func deref(p *int) int {
	if p == nil {
		panic(invalid)
	}
	return *p
}

// extreme is min or max of one time over the pieces; a missing time among them cannot be compared.
func extreme(pieces []piece, get func(piece) *int, largest bool) int {
	best := deref(get(pieces[0]))
	for _, p := range pieces[1:] {
		v := deref(get(p))
		if largest && v > best || !largest && v < best {
			best = v
		}
	}
	return best
}

// ---- CRM ----

func days(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

func dayOf(value any) time.Time {
	text, ok := value.(string)
	if !ok {
		panic(&Rejected{ErrDay.Error()})
	}
	d, err := Day(text)
	if err != nil {
		panic(&Rejected{ErrDay.Error()})
	}
	return d
}

// daysBetween is (a - b).days for two days at midnight UTC.
func daysBetween(a, b time.Time) int { return int((a.Unix() - b.Unix()) / 86400) }

// dateOf is the UTC day of an instant, at midnight.
func dateOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

func money(amount string, currency any) string {
	if currency != nil {
		return currency.(string) + " " + amount
	}
	return amount + " (currency Missing)"
}

func currencyCode(v any) bool {
	s, ok := v.(string)
	if !ok || len(s) != 3 {
		return false
	}
	for _, c := range []byte(s) {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func decimalText(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	whole, fraction, dotted := strings.Cut(s, ".")
	_, okWhole := digits(whole)
	_, okFraction := digits(fraction)
	return whole != "" && okWhole && (!dotted || fraction != "" && okFraction)
}

// grouped is f"{n:,}".
func grouped(n *big.Int) string {
	text := n.String()
	var b strings.Builder
	for i, c := range text {
		if i > 0 && (len(text)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// amount is units._amount: an amount as printed and how many of its fields are empty.
func amount(row *strictjson.Object, salesforce bool, name string) (string, int) {
	currency := field(row, "currency")
	if currency != nil && !currencyCode(currency) {
		reject("A currency must be a three-letter code or absent.")
	}
	var value string
	if salesforce {
		raw := field(row, "amount_decimal")
		if raw == nil {
			return "Missing", 1
		}
		text, ok := raw.(string)
		if !ok || length(text) > 128 || !decimalText(text) {
			reject("Salesforce amounts must be finite decimal strings or null.")
		}
		value = text
	} else {
		cents, ok := isInt(field(row, name))
		if !ok || cents.Sign() < 0 {
			reject("Evidence amounts must be nonnegative integer cents.")
		}
		whole, rest := new(big.Int).QuoRem(cents, big.NewInt(100), new(big.Int))
		value = fmt.Sprintf("%s.%02d", grouped(whole), rest.Int64())
	}
	missing := 0
	if currency == nil {
		missing = 1
	}
	return money(value, currency), missing
}

// shownRows is units._shown: the first few rows; the rest are counted.
func (c *card) shownRows(rows []*strictjson.Object, one, many string) []*strictjson.Object {
	more := len(rows) - ShownRecords
	if more > 0 {
		noun, verb := one, "was read and is"
		if more > 1 {
			noun, verb = many, "were read and are"
		}
		c.gap("crm_more_"+many, fmt.Sprintf("CRM: %d more %s %s not shown (%d in all).", more, noun, verb, len(rows)),
			intp(more), false, nil)
		return rows[:ShownRecords]
	}
	return rows
}

func byID(rows []*strictjson.Object) []*strictjson.Object {
	sorted := append([]*strictjson.Object(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(field(sorted[i], "id"), field(sorted[j], "id")) })
	return sorted
}

func rowID(row *strictjson.Object) string { return str(field(row, "id")) }

func (c *card) crm(asOf time.Time) []contact {
	salesforce := field(c.evidence, "crm_backend") == "salesforce"
	account := c.groups["accounts"][0]
	first := c.record(account, "accounts")
	contacts := byID(c.groups["contacts"])
	contactNumbers := map[string]int{}
	for _, row := range contacts {
		contactNumbers[rowID(row)] = c.record(row, "contacts")
	}
	named := make([]contact, len(contacts))
	for i, row := range contacts {
		named[i] = contact{field(row, "name"), contactNumbers[rowID(row)]}
	}
	contracts := byID(c.groups["contracts"])
	contractNumbers := map[string]int{}
	for _, row := range contracts {
		contractNumbers[rowID(row)] = c.record(row, "contracts")
	}
	opportunities := byID(c.groups["opportunities"])
	opportunityNumbers := map[string]int{}
	for _, row := range opportunities {
		opportunityNumbers[rowID(row)] = c.record(row, "opportunities")
	}
	var daysLeft *int
	var renewalDay *time.Time
	renewalNumbers := []int{first}
	if salesforce {
		type dated struct {
			end      time.Time
			id       string
			contract *strictjson.Object
		}
		var ahead, ended []dated
		for _, contract := range contracts {
			if !oneOf(field(contract, "status_code"), "Draft", "InApproval", "Activated") {
				reject("Unknown Salesforce Contract status category.")
			}
			if end := field(contract, "end_date"); end != nil {
				d := dayOf(end)
				if !d.Before(asOf) {
					ahead = append(ahead, dated{d, rowID(contract), contract})
				} else {
					ended = append(ended, dated{d, rowID(contract), contract})
				}
			}
		}
		earlier := func(a, b dated) bool { return a.end.Before(b.end) || a.end.Equal(b.end) && a.id < b.id }
		if len(ahead) > 0 {
			best := ahead[0]
			for _, d := range ahead[1:] {
				if earlier(d, best) {
					best = d
				}
			}
			daysLeft, renewalDay = intp(daysBetween(best.end, asOf)), &best.end
			renewalNumbers = []int{contractNumbers[best.id]}
			activated := "; a planning candidate, not activated"
			if field(best.contract, "status_code") == "Activated" {
				activated = ""
			}
			when := "today"
			if *daysLeft != 0 {
				when = "in " + days(*daysLeft)
			}
			c.value("Contract end", fmt.Sprintf("%s, %s; status %s%s", isoDay(best.end), when,
				c.text(field(best.contract, "status")), activated), renewalNumbers...)
		} else {
			c.missing++
			c.value("Contract end", "Missing: no contract ends on or after "+isoDay(asOf), first)
			if len(ended) > 0 {
				last := ended[0]
				for _, d := range ended[1:] {
					if earlier(last, d) {
						last = d
					}
				}
				c.signal(fmt.Sprintf("The latest contract end date, %s, was %s ago.", isoDay(last.end),
					days(daysBetween(asOf, last.end))), 0, contractNumbers[last.id])
			}
		}
		c.gap("crm_unmapped_fields", "CRM: annual recurring revenue and renewal risk are not mapped "+
			"from standard Salesforce fields.", nil, false, nil)
	} else {
		renewal := dayOf(field(account, "renewal_date"))
		renewalDay = &renewal
		daysLeft = intp(daysBetween(renewal, asOf))
		timing := "today"
		switch {
		case *daysLeft > 0:
			timing = "in " + days(*daysLeft)
		case *daysLeft < 0:
			timing = days(-*daysLeft) + " ago"
		}
		c.value("Renewal date", isoDay(renewal)+", "+timing, first)
		recurring, missing := amount(account, false, "arr_cents")
		c.missing += missing
		c.value("Annual recurring revenue", recurring, first)
		risk := field(account, "risk_status")
		if !oneOf(risk, "healthy", "at_risk") {
			reject("Unknown CRM risk status.")
		}
		reason := ""
		if truthy(field(account, "risk_reason")) {
			reason = "; reason " + c.text(field(account, "risk_reason"))
		}
		c.value("CRM risk", strings.ReplaceAll(risk.(string), "_", " ")+reason, first)
		if risk == "at_risk" {
			c.signal("CRM marks this account at risk.", 0, first)
		}
	}
	c.value("Account", fmt.Sprintf("%s; industry %s", c.text(field(account, "name")),
		c.text(field(account, "industry"))), first)
	type priced struct {
		text    string
		missing int
	}
	var stillOpen []*strictjson.Object
	open := map[string]bool{}
	amounts := map[string]priced{}
	for _, row := range opportunities {
		text, missing := amount(row, salesforce, "amount_cents")
		amounts[rowID(row)] = priced{text, missing}
		var closed bool
		if salesforce {
			isClosed, ok1 := field(row, "is_closed").(bool)
			_, ok2 := field(row, "is_won").(bool)
			if !ok1 || !ok2 {
				reject("Salesforce opportunity outcomes must be booleans.")
			}
			closed = isClosed
		} else {
			closed = oneOf(field(row, "stage"), "closed_won", "closed_lost")
		}
		dayOf(field(row, "close_date"))
		if !closed {
			open[rowID(row)] = true
			if daysLeft != nil && *daysLeft <= soonDays {
				stillOpen = append(stillOpen, row)
			}
		}
	}
	byClose := func(rows []*strictjson.Object, latest bool) []*strictjson.Object {
		sorted := append([]*strictjson.Object(nil), rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if latest {
				return closeKey(sorted[j]).less(closeKey(sorted[i]))
			}
			return closeKey(sorted[i]).less(closeKey(sorted[j]))
		})
		return sorted
	}
	var openRows, closedRows []*strictjson.Object
	for _, row := range opportunities {
		if open[rowID(row)] {
			openRows = append(openRows, row)
		} else {
			closedRows = append(closedRows, row)
		}
	}
	ordered := append(byClose(openRows, false), byClose(closedRows, true)...)
	for _, row := range c.shownRows(ordered, "opportunity", "opportunities") {
		price := amounts[rowID(row)]
		c.missing += price.missing
		outcome := ""
		if salesforce {
			outcome = fmt.Sprintf("; closed %t; won %t", field(row, "is_closed").(bool), field(row, "is_won").(bool))
		}
		name, stage := c.text(field(row, "name")), c.text(field(row, "stage"))
		c.value("Opportunity", fmt.Sprintf("%s; stage %s; amount %s; close date %s%s", name, stage, price.text,
			str(field(row, "close_date")), outcome), opportunityNumbers[rowID(row)])
	}
	if len(opportunities) == 0 {
		c.gap("crm_no_opportunity", "CRM: no opportunity was returned for this account.", intp(0), false, nil)
	}
	for _, row := range contracts {
		if end := field(row, "end_date"); end != nil {
			dayOf(end)
		}
	}
	var otherContracts []*strictjson.Object
	for _, row := range contracts {
		if !citesAny(renewalNumbers, contractNumbers[rowID(row)]) {
			otherContracts = append(otherContracts, row)
		}
	}
	for _, row := range c.shownRows(otherContracts, "contract", "contracts") {
		ends := "no end date"
		if end := field(row, "end_date"); end != nil {
			ends = "ends " + str(end)
		}
		c.value("Contract", fmt.Sprintf("status %s; %s", c.text(field(row, "status")), ends), contractNumbers[rowID(row)])
	}
	for _, row := range c.shownRows(contacts, "contact", "contacts") {
		n := contactNumbers[rowID(row)]
		title := c.text(field(row, "title"))
		c.titles[n] = title
		c.value("Contact", fmt.Sprintf("%s; role %s; email %s", c.text(field(row, "name")), title,
			c.text(field(row, "email"))), n)
	}
	c.unshownTitles = map[int]any{}
	if len(contacts) > ShownRecords {
		for _, row := range contacts[ShownRecords:] {
			c.unshownTitles[contactNumbers[rowID(row)]] = field(row, "title")
		}
	}
	what := "renewal"
	if salesforce {
		what = "contract end"
	}
	when := ""
	if daysLeft != nil {
		switch {
		case *daysLeft == 0:
			when = "The " + what + " is today"
		case *daysLeft > 0:
			when = "The " + what + " is in " + days(*daysLeft)
		default:
			when = "The " + what + " date passed " + days(-*daysLeft) + " ago"
		}
	}
	numbersOf := func(rows []*strictjson.Object, numbers map[string]int) []int {
		var out []int
		for _, row := range rows {
			out = append(out, numbers[rowID(row)])
		}
		return out
	}
	switch {
	case daysLeft != nil && *daysLeft < 0 && len(stillOpen) == 0:
		c.signal(when+".", 0, renewalNumbers...)
	case len(stillOpen) > ShownRecords:
		c.signal(fmt.Sprintf("%s and %d opportunities are still open.", when, len(stillOpen)),
			len(stillOpen)-ShownRecords,
			append(append([]int(nil), renewalNumbers...), numbersOf(stillOpen[:ShownRecords], opportunityNumbers)...)...)
	default:
		for _, row := range stillOpen {
			c.signal(fmt.Sprintf("%s and this opportunity is still open at stage %s.", when,
				c.quoted(field(row, "stage"))), 0,
				append(append([]int(nil), renewalNumbers...), opportunityNumbers[rowID(row)])...)
		}
	}
	var late []*strictjson.Object
	for _, row := range opportunities {
		if open[rowID(row)] && renewalDay != nil && dayOf(field(row, "close_date")).After(*renewalDay) {
			late = append(late, row)
		}
	}
	renewalText := ""
	if renewalDay != nil {
		renewalText = isoDay(*renewalDay)
	}
	if len(late) > ShownRecords {
		c.signal(fmt.Sprintf("%d open opportunities have a close date after the %s date, %s.", len(late), what,
			renewalText), len(late)-ShownRecords,
			append(append([]int(nil), renewalNumbers...), numbersOf(late[:ShownRecords], opportunityNumbers)...)...)
	} else {
		for _, row := range late {
			c.signal(fmt.Sprintf("This open opportunity's close date, %s, is after the %s date, %s.",
				str(field(row, "close_date")), what, renewalText), 0,
				append(append([]int(nil), renewalNumbers...), opportunityNumbers[rowID(row)])...)
		}
	}
	var noRole []*strictjson.Object
	for _, row := range contacts {
		if empty(field(row, "title")) {
			noRole = append(noRole, row)
		}
	}
	if len(noRole) > ShownRecords {
		c.signal(fmt.Sprintf("%d contacts have no role recorded.", len(noRole)), len(noRole)-ShownRecords,
			numbersOf(noRole[:ShownRecords], contactNumbers)...)
	} else {
		for _, row := range noRole {
			c.signal("This contact has no role recorded.", 0, contactNumbers[rowID(row)])
		}
	}
	return named
}

type closeOrder struct{ day, id string }

func closeKey(row *strictjson.Object) closeOrder {
	return closeOrder{str(field(row, "close_date")), rowID(row)}
}

func (a closeOrder) less(b closeOrder) bool { return a.day < b.day || a.day == b.day && a.id < b.id }

func citesAny(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// ---- calls and Slack ----

func (c *card) sellers(backend string) []string {
	var out []string
	for _, key := range []string{"sellers", "demo_sellers"} {
		if key == "demo_sellers" && field(c.evidence, backend) != "fixture" {
			continue
		}
		for _, s := range list(field(c.evidence, key)) {
			out = append(out, str(s))
		}
	}
	return out
}

func (c *card) calls(asOf time.Time, contacts []contact) {
	sellers := c.sellers("calls_backend")
	notes := c.groups["call_notes"]
	imported := field(c.evidence, "calls_backend") == "imported"
	switch field(c.evidence, "notes_status") {
	case "not_connected":
		c.gap("calls_not_connected", "Calls: not connected.", nil, false, nil)
		return
	case "denied":
		c.gap("calls_denied", "Calls: not read. The grant lacks calls:read.", nil, false, nil)
		return
	}
	if len(notes) == 0 {
		if imported {
			c.gap("calls_no_transcript", "Calls: no transcript is imported for this account.", intp(0), false, nil)
		} else {
			c.gap("calls_no_record", "Calls: no call record was returned for this account.", intp(0), false, nil)
		}
		return
	}
	if imported {
		panic(ErrNotPorted)
	}
	roleOf := func(l *string) role {
		var v any
		if l != nil {
			v = *l
		}
		return speakerRole(v, sellers, contacts, false)
	}
	sorted := append([]*strictjson.Object(nil), notes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := pyStr(field(sorted[i], "called_at")), pyStr(field(sorted[j], "called_at"))
		return a < b || a == b && rowID(sorted[i]) < rowID(sorted[j])
	})
	firstUnit := map[string]int{}
	for _, note := range sorted {
		speaker, _ := note.Get("speaker")
		firstUnit[str(field(note, "record_ref"))] = c.spoken(note, "call_notes",
			[]part{{label: speaker, raw: str(field(note, "body"))}}, field(note, "called_at"), roleOf, false, false)
	}
	noun := "records were"
	if len(notes) == 1 {
		noun = "record was"
	}
	c.gap("calls_outside_records", fmt.Sprintf("Calls: %d call %s read. Recordings and anything outside those "+
		"records were not read.", len(notes), noun), nil, false, nil)
	var latest *time.Time
	var latestRef string
	for _, note := range notes {
		text, ok := field(note, "called_at").(string)
		if !ok {
			continue
		}
		moment, ok := Instant(text)
		if !ok || dateOf(moment).After(asOf) {
			continue
		}
		ref := str(field(note, "record_ref"))
		if latest == nil || moment.After(*latest) || moment.Equal(*latest) && ref > latestRef {
			latest, latestRef = &moment, ref
		}
	}
	if latest != nil {
		age := daysBetween(asOf, dateOf(*latest))
		if age > staleDays {
			c.signal(fmt.Sprintf("The latest dated call that was read is %s old.", days(age)), 0, firstUnit[latestRef])
		}
	}
}

func (c *card) slack() {
	sellers := c.sellers("slack_backend")
	switch field(c.evidence, "slack_status") {
	case "not_connected":
		c.gap("slack_not_connected", "Slack: not connected.", nil, false, nil)
		return
	case "not_selected":
		c.gap("slack_not_selected", "Slack: no thread is selected for this account.", nil, false, nil)
		return
	case "not_requested":
		c.gap("slack_not_requested", "Slack: not requested for this card.", nil, false, nil)
		return
	case "denied":
		c.gap("slack_denied", "Slack: not read. The grant lacks slack:read.", nil, false, nil)
		return
	}
	if field(c.evidence, "slack_backend") == "slack" {
		panic(ErrNotPorted) // validate refuses a live thread with messages before this
	}
	messages := append([]*strictjson.Object(nil), c.groups["messages"]...)
	sort.SliceStable(messages, func(i, j int) bool {
		a, b := field(messages[i], "sent_at"), field(messages[j], "sent_at")
		if less(a, b) {
			return true
		}
		return !less(b, a) && rowID(messages[i]) < rowID(messages[j])
	})
	files := new(big.Int)
	var attachedTo []string
	roleOf := func(l *string) role {
		var v any
		if l != nil {
			v = *l
		}
		return speakerRole(v, sellers, nil, true)
	}
	for _, message := range messages {
		attached, ok := isInt(field(message, "files"))
		if !ok || attached.Sign() < 0 {
			reject("A Slack file count must be a nonnegative integer.")
		}
		files.Add(files, attached)
		first := c.spoken(message, "messages", []part{{label: field(message, "author"), raw: str(field(message, "body"))}},
			field(message, "sent_at"), roleOf, false, false)
		if attached.Sign() != 0 {
			attachedTo = append(attachedTo, fmt.Sprintf("[%d]", first))
		}
	}
	noun := "messages were"
	if len(messages) == 1 {
		noun = "message was"
	}
	read := fmt.Sprintf("%d %s read", len(messages), noun)
	if len(messages) == 0 {
		c.gap("slack_no_message", "Slack: no message was returned for this account.", intp(0), false, nil)
		return
	}
	attachedText := ""
	var fileCount *int
	if files.Sign() != 0 {
		where := ""
		if len(attachedTo) > 0 {
			where = fmt.Sprintf(" (messages starting at %s)", strings.Join(attachedTo[:min(len(attachedTo), ShownRecords)], ""))
		}
		word := "files were"
		if files.Cmp(big.NewInt(1)) == 0 {
			word = "file was"
		}
		attachedText = fmt.Sprintf(" %s attached %s not read%s.", files, word, where)
		fileCount = intp(int(files.Int64()))
	}
	c.gap("slack_channel", fmt.Sprintf("Slack: %s.%s Other channels were not read.", read, attachedText), fileCount,
		false, nil)
}

// ---- SAID ----

// cardTime is units.card_time: a plain time as it is, any other value as a quoted literal.
func cardTime(at *string) string {
	switch {
	case at == nil:
		return "time Missing"
	case PlainInstant(*at):
		return *at
	}
	return "time " + strictjson.Quote(*at)
}

var roles = []string{"customer", "seller", "unknown"}

type recency struct {
	timed   bool
	micros  int64
	opens   int
	startMS int
	n       int
}

func (a recency) less(b recency) bool {
	switch {
	case a.timed != b.timed:
		return !a.timed
	case a.micros != b.micros:
		return a.micros < b.micros
	case a.opens != b.opens:
		return a.opens < b.opens
	case a.startMS != b.startMS:
		return a.startMS < b.startMS
	}
	return a.n < b.n
}

func (c *card) said() []string {
	var spoken []*Unit
	for i := range c.units {
		if c.units[i].Kind == "sentence" {
			spoken = append(spoken, &c.units[i])
		}
	}
	opens := map[string]int{}
	for _, u := range spoken {
		if _, ok := opens[u.RecordRef]; !ok {
			opens[u.RecordRef] = u.N
		}
	}
	moment := func(u *Unit) (time.Time, bool) {
		if u.At == nil || !PlainInstant(*u.At) {
			return time.Time{}, false
		}
		return Instant(*u.At)
	}
	recencyOf := func(u *Unit) recency {
		at, timed := moment(u)
		r := recency{timed: timed, opens: opens[u.RecordRef], n: u.N}
		if timed {
			r.micros = at.UnixMicro()
		}
		if u.Span != nil && u.Span.StartMS != nil {
			r.startMS = *u.Span.StartMS
		}
		return r
	}
	asOf := dayOf(field(c.evidence, "as_of"))
	for _, u := range spoken {
		if at, ok := moment(u); u.Shown != "cut" && ok && dateOf(at).After(asOf) {
			u.Shown = "later"
		}
	}
	queues := map[string][]*Unit{}
	for _, r := range roles {
		var queue []*Unit
		for _, u := range spoken {
			if u.Shown == "more" && u.Role == r {
				queue = append(queue, u)
			}
		}
		sort.SliceStable(queue, func(i, j int) bool { return recencyOf(queue[j]).less(recencyOf(queue[i])) })
		queues[r] = queue
	}
	dealt := map[string]int{}
	total := 0
	for range ShownSentences {
		for _, r := range roles {
			if dealt[r] < len(queues[r]) && total < ShownSentences {
				queues[r][dealt[r]].Shown = "yes"
				dealt[r]++
				total++
			}
		}
	}
	var lines []string
	headings := map[string]string{"customer": "Customer, by a speaker label equal to one contact's name",
		"seller": "Seller, by a listed seller's name or ID", "unknown": "No established role"}
	for _, r := range roles {
		chosen := append([]*Unit(nil), queues[r][:dealt[r]]...)
		// A sentence with no time is printed last in its role, as it is dealt last.
		sort.SliceStable(chosen, func(i, j int) bool {
			a, b := recencyOf(chosen[i]), recencyOf(chosen[j])
			a.timed, b.timed = !a.timed, !b.timed
			return a.less(b)
		})
		if len(chosen) > 0 {
			lines = append(lines, "  "+headings[r])
		}
		for _, u := range chosen {
			where := map[string]string{"calls": "call", "slack": "Slack"}[u.Source]
			who := "no speaker label"
			if u.Speaker != nil {
				who = strictjson.Quote(*u.Speaker)
			}
			if u.Contact != nil {
				title, ok := c.titles[*u.Contact]
				if !ok {
					unshown, found := c.unshownTitles[*u.Contact]
					if !found {
						panic(invalid)
					}
					title = c.text(unshown)
					c.titles[*u.Contact] = title
				}
				who += fmt.Sprintf(", role %s [%d]", title, *u.Contact)
			} else if r == "unknown" && u.Basis != "no speaker label" {
				who += " (" + u.Basis + ")"
			}
			lines = append(lines, fmt.Sprintf("    [%d] %s %s; %s", u.N, where, cardTime(u.At), who),
				"        "+strictjson.Quote(u.Text))
		}
	}
	withheld := func(why string, line func(n int, roles string) string) {
		var counts RoleCounts
		n := 0
		for _, u := range spoken {
			if u.Shown != why {
				continue
			}
			n++
			switch u.Role {
			case "customer":
				counts.Customer++
			case "seller":
				counts.Seller++
			default:
				counts.Unknown++
			}
		}
		if n == 0 {
			return
		}
		var parts []string
		for _, rc := range []struct {
			n    int
			role string
		}{{counts.Customer, "customer"}, {counts.Seller, "seller"}, {counts.Unknown, "unknown"}} {
			if rc.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", rc.n, rc.role))
			}
		}
		c.gap("sentences_"+why, line(n, "("+strings.Join(parts, ", ")+")"), intp(n), false, &counts)
	}
	readNotShown := func(n int) string {
		if n == 1 {
			return "was read and is"
		}
		return "were read and are"
	}
	withheld("cut", func(n int, roles string) string {
		return fmt.Sprintf("Sentences: %d cut off at a pause, a change of speaker, or the edge of the excerpt "+
			"%s not shown %s.", n, readNotShown(n), roles)
	})
	withheld("later", func(n int, roles string) string {
		return fmt.Sprintf("Sentences: %d dated after %s %s not shown %s.", n, isoDay(asOf), readNotShown(n), roles)
	})
	withheld("more", func(n int, roles string) string {
		noun := "sentences were read and are"
		if n == 1 {
			noun = "sentence was read and is"
		}
		return fmt.Sprintf("Sentences: %d more %s not shown %s; the card shows %d.", n, noun, roles, ShownSentences)
	})
	withheld("long", func(n int, roles string) string {
		return fmt.Sprintf("Sentences: %d over %d characters %s not shown %s, because a quotation is never cut.",
			n, MaxSentence, readNotShown(n), roles)
	})
	labelled, allUnmatched := false, true
	records := map[string]bool{}
	for _, u := range spoken {
		if u.Source != "calls" {
			continue
		}
		records[u.RecordRef] = true
		if u.Speaker != nil {
			labelled = true
			allUnmatched = allUnmatched && (u.Basis == noContact[0] || u.Basis == noContact[1])
		}
	}
	if labelled && allUnmatched {
		noun := "records"
		if len(records) == 1 {
			noun = "record"
		}
		c.gap("calls_no_contact", fmt.Sprintf("Calls: no speaker label equals a contact's name in the %d call %s "+
			"with a sentence.", len(records), noun), intp(len(records)), false, nil)
	}
	unknown := 0
	for _, u := range spoken {
		if u.Role == "unknown" {
			unknown++
		}
	}
	if unknown > 0 {
		noun := "sentences have"
		if len(spoken) == 1 {
			noun = "sentence has"
		}
		c.gap("speakers_unknown", fmt.Sprintf("Speakers: %d of %d %s no established speaker role.", unknown,
			len(spoken), noun), intp(unknown), false, &RoleCounts{Unknown: unknown})
	}
	return lines
}

// ---- the card ----

func build(evidence *strictjson.Object) ([]Unit, string, []Gap) {
	v := validate(evidence)
	asOf := dayOf(field(evidence, "as_of"))
	c := &card{evidence: evidence, groups: v.groups, titles: map[int]string{}, unshownTitles: map[int]any{}}
	contacts := c.crm(asOf)
	c.calls(asOf, contacts)
	c.slack()
	said := c.said()
	if len(said) == 0 {
		read := 0
		for _, u := range c.units {
			if u.Kind == "sentence" {
				read++
			}
		}
		switch read {
		case 0:
			said = []string{"  No sentence was read."}
		case 1:
			said = []string{"  1 sentence was read and is not shown. NOT READ says why."}
		default:
			said = []string{fmt.Sprintf("  %d sentences were read and none is shown. NOT READ says why.", read)}
		}
	}
	if c.missing > 0 {
		text := "CRM: 1 field is empty on the record and prints as Missing."
		if c.missing != 1 {
			text = fmt.Sprintf("CRM: %d fields are empty on the record and print as Missing.", c.missing)
		}
		c.gap("crm_fields_empty", text, intp(c.missing), true, nil)
	}
	var replaced []string
	for _, item := range []struct {
		n    int
		word string
	}{{c.neutralized.Mentions, "mention"}, {c.neutralized.Links, "link"},
		{c.neutralized.Controls, "control character"}, {c.neutralized.Brackets, "bracketed number"}} {
		if item.n == 1 {
			replaced = append(replaced, "1 "+item.word)
		} else if item.n > 1 {
			replaced = append(replaced, fmt.Sprintf("%d %ss", item.n, item.word))
		}
	}
	if len(replaced) > 0 {
		n := c.neutralized.Mentions + c.neutralized.Links + c.neutralized.Controls + c.neutralized.Brackets
		c.gap("neutralized", "Neutralized in source text: "+strings.Join(replaced, ", ")+".", intp(n), false, nil)
	}
	account := v.groups["accounts"][0]
	safeName, _, err := SafeText(str(field(account, "name")))
	if err != nil {
		panic(invalid)
	}
	lines := []string{fmt.Sprintf("DEAL CARD: %s (%s) as of %s", strictjson.Quote(oneLine(safeName)),
		str(field(evidence, "account_id")), str(field(evidence, "as_of")))}
	var started []time.Time
	for _, value := range list(field(evidence, "receipts")) {
		receipt := object(value)
		text, ok := field(receipt, "started_at").(string)
		var moment time.Time
		if ok && PlainInstant(text) {
			moment, ok = Instant(text)
		} else {
			ok = false
		}
		if !ok {
			reject("An audit receipt has no valid start time.")
		}
		if field(receipt, "status") == "ok" {
			started = append(started, moment)
		}
	}
	if len(started) == 0 {
		panic(invalid)
	}
	first, last := started[0], started[0]
	for _, m := range started[1:] {
		if m.Before(first) {
			first = m
		}
		if m.After(last) {
			last = m
		}
	}
	from, to := first.Format("2006-01-02T15:04:05Z"), last.Format("2006-01-02T15:04:05Z")
	if to != from {
		lines = append(lines, "Sources read "+from+" to "+to+".", "")
	} else {
		lines = append(lines, "Sources read "+from+".", "")
	}
	if len(c.signals) > 0 {
		lines = append(append(append(lines, "ATTENTION, from record fields and dates"), c.signals...), "")
	}
	lines = append(append(lines, "RECORD VALUES, from CRM fields and connector arithmetic"), c.values...)
	lines = append(append(append(lines, "", "SAID, whole sentences as recorded; not record values"), said...), "",
		"NOT READ")
	for _, g := range c.gaps {
		lines = append(lines, "  "+g.Text)
	}
	lines = append(lines, "")
	return c.units, strings.Join(lines, "\n"), c.gaps
}

// TraceNumber reports whether number n resolves as units.trace resolves it: to a unit numbered n whose
// record is in the evidence and in the rows of its read's receipt, and, for a sentence, whose raw_sha256
// is the digest of the record's stored text.
func TraceNumber(evidence *strictjson.Object, units []any, n int) (receiptID string, ok bool) {
	defer func() {
		if recover() != nil {
			receiptID, ok = "", false
		}
	}()
	if n < 1 || n > len(units) {
		return "", false
	}
	unit := object(units[n-1])
	tool := field(unit, "tool")
	var receipt, record *strictjson.Object
	for _, value := range list(field(evidence, "receipts")) {
		if r := object(value); receipt == nil && equal(field(r, "tool"), tool) {
			receipt = r
		}
	}
	reference := field(unit, "record_ref")
	for _, value := range list(field(evidence, "records")) {
		if r := object(value); record == nil && equal(field(r, "record_ref"), reference) {
			record = r
		}
	}
	if !equal(field(unit, "n"), n) || receipt == nil || record == nil {
		return "", false
	}
	inRows := false
	for _, row := range list(field(receipt, "rows")) {
		inRows = inRows || equal(row, reference)
	}
	if !inRows {
		return "", false
	}
	if field(unit, "kind") == "sentence" && !equal(field(unit, "raw_sha256"), digest(str(field(record, "body")))) {
		return "", false
	}
	return str(field(receipt, "audit_id")), true
}
