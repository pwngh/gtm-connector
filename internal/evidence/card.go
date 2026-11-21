package evidence

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// card is units._Card: units in a fixed order and the lines that cite them.
type card struct {
	evidence      *strictjson.Object
	groups        map[string][]*strictjson.Object
	units         []Unit
	neutralized   Counts
	missing       int
	values        []Line
	signals       []Line
	gaps          []Gap
	titles        map[int]string
	unshownTitles map[int]any
	// known is whether a label names a known person of the card, as speakerRole compares one: a contact of
	// the account or a listed seller. A line of a message that opens with such a label is that person's turn.
	known func(name string) bool
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

// quotation is source text a quotation is made from, neutralized by QuoteText, with what was replaced
// counted.
func (c *card) quotation(value any) string {
	raw, ok := value.(string)
	if !ok {
		reject("Evidence text fields must be strings.")
	}
	safe, counts, err := QuoteText(raw)
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
	shown, _ := c.field(value)
	return shown
}

// field is a displayed field as text prints it and as data: its line, or nil when it is empty.
func (c *card) field(value any) (string, *string) {
	if empty(value) {
		c.missing++
		return "Missing", nil
	}
	line := c.line(value)
	return strictjson.Quote(line), &line
}

func (c *card) record(row *strictjson.Object, table string) int {
	rule := sourceRules[table]
	c.units = append(c.units, Unit{N: len(c.units) + 1, Kind: "record", Source: rule.system,
		RecordRef: str(field(row, "record_ref")), Tool: rule.tool})
	return len(c.units)
}

func (c *card) gap(code, text string, count *big.Int, first bool, byRole *RoleCounts) {
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

// value adds a line under RECORD VALUES, labelled by its code.
func (c *card) value(l Line) {
	l.Section, l.Label, l.Cites = "value", valueLabels[l.Code], slices.Clone(l.Cites)
	c.values = append(c.values, l)
}

// signal adds a line under ATTENTION.
func (c *card) signal(code, sentence string, more int, numbers ...int) {
	c.signals = append(c.signals, Line{Section: "signal", Code: code, Text: sentence, Cites: slices.Clone(numbers),
		More: more})
}

// num is a count a gap states.
func num(n int) *big.Int { return big.NewInt(int64(n)) }

func intp(n int) *int { return &n }

// ---- speech ----

// label is units._label: a label on one line, or "" when it is not a string.
func label(value any) string {
	if s, ok := value.(string); ok {
		return oneLine(s)
	}
	return ""
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

// named is the contacts a speaker label equals, and whether it equals a listed seller, as speakerRole
// compares them: each a label on one line.
func named(name string, sellers []string, contacts []contact) (matches []contact, listed bool) {
	for _, c := range contacts {
		if label(c.name) == name {
			matches = append(matches, c)
		}
	}
	for _, seller := range sellers {
		listed = listed || label(seller) == name
	}
	return matches, listed
}

// knownPerson is whether a label names a contact of the account or a listed seller.
func knownPerson(value any, sellers []string, contacts []contact) bool {
	name := label(value)
	if name == "" {
		return false
	}
	matches, listed := named(name, sellers, contacts)
	return listed || len(matches) > 0
}

// speakerRole is units.speaker_role: a role from identity only.
func speakerRole(value any, sellers []string, contacts []contact, slack bool) role {
	name := label(value)
	if name == "" {
		return role{"unknown", "no speaker label", nil}
	}
	if slack {
		contacts = nil
	}
	matches, listed := named(name, sellers, contacts)
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

// part is one piece of a record's speech, in order: its speaker label, its text and, in an imported
// transcript, its cue or line and that cue's times.
type part struct {
	label, raw any
	start, end *int
	cue        *int
}

// run is neighbouring parts under one speaker label: the label, the parts' text joined by line breaks, when
// the last part ended, and one piece for each cue.
type run struct {
	label  *string
	raw    any
	end    *int
	pieces []part
}

// whole is the one part of a record that is not a transcript: one speaker label and one text.
func whole(speaker, raw any) []part { return []part{{label: speaker, raw: raw}} }

// joins is whether a part goes on the run before it. Both have the same speaker label and times, at most
// two seconds apart, or at most ten when the run stops mid-sentence and the part goes on in lower case.
func (r *run) joins(l *string, p part) bool {
	if r == nil || l == nil || r.label == nil || *r.label != *l || p.start == nil || r.end == nil {
		return false
	}
	gap := *p.start - *r.end
	return gap >= -runGapMS && gap <= runGapMS ||
		gap > 0 && gap <= bridgeGapMS && unfinished(str(r.raw)) && goesOn(str(p.raw))
}

// bisect is how many of the sorted ends are below n, or at most n when right is set (bisect_left and
// bisect_right).
func bisect(ends []int, n int, right bool) int {
	return sort.Search(len(ends), func(i int) bool { return ends[i] > n || !right && ends[i] == n })
}

// spoken is _Card.spoken: the units of one record's speech. Neighbouring parts under one speaker label are
// one run, so a sentence wrapped across cues stays whole. Parts with no label or no times never join. In a
// transcript, a run that stops mid-sentence is not a quotation, and neither is the opening of that speaker's
// next run; with openStart, the excerpt starts inside the call, and neither are each speaker's first words.
// It returns the number of the record's first unit; a record with no sentence still gets one unit, so every
// record resolves.
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
		if !last.joins(l, p) {
			runs = append(runs, &run{label: l, raw: p.raw, end: p.end, pieces: []part{p}})
			continue
		}
		last.raw, last.end = str(last.raw)+"\n"+str(p.raw), p.end
		// One piece for each cue: a link or a mention written across two voices of a cue is still one.
		if tail := &last.pieces[len(last.pieces)-1]; tail.cue != nil && p.cue != nil && *tail.cue == *p.cue ||
			tail.cue == nil && p.cue == nil {
			tail.raw, tail.end = str(tail.raw)+"\n"+str(p.raw), p.end
		} else {
			last.pieces = append(last.pieces, p)
		}
	}
	before := len(c.units)
	// The digest is of the record's text as stored, so it can be checked against the source.
	rawDigest := digest(str(field(row, "body")))
	// A speaker is a label, or no label.
	speakerOf := func(l *string) string {
		if l == nil {
			return ""
		}
		return "=" + *l
	}
	waiting := map[string]bool{} // speakers whose last run stopped mid-sentence
	heard := map[string]bool{}   // speakers who have spoken in this excerpt
	cut := false                 // whether the sentence before this one, by anyone, stopped mid-sentence
	rule := sourceRules[table]
	for _, r := range runs {
		who := roleOf(r.label)
		// A speaker label is source text too. The role was derived from the label as given.
		var speaker *string
		if r.label != nil {
			if text := c.line(*r.label); text != "" {
				speaker = &text
			}
		}
		if speaker != nil && length(*speaker) > MaxLabel {
			speaker, who = nil, role{"unknown", fmt.Sprintf("speaker label over %d characters", MaxLabel), nil}
		}
		// Each cue is neutralized on its own, so a sentence can be placed among the cues by counting its
		// characters, spaces apart.
		texts, ends, total := make([]string, len(r.pieces)), make([]int, len(r.pieces)), 0
		for i, piece := range r.pieces {
			texts[i] = c.quotation(piece.raw)
			total += nonSpace(texts[i])
			ends[i] = total
		}
		safe := strings.Join(texts, "\n")
		var known func(string) bool
		if table == "messages" {
			known = c.known
		}
		found, quotedAt, err := sentences(safe, table == "messages", known)
		if err != nil {
			panic(invalid)
		}
		// What follows a sentence that stopped may be its other half: the same speaker's next words, or
		// anyone's when they go on in lower case.
		name := speakerOf(r.label)
		follows := openStart && !heard[name] || waiting[name] || cut && goesOn(safe)
		if len(found) > 0 {
			delete(waiting, name)
			heard[name] = true
		}
		reached, size := 0, 0
		for position, sentence := range found {
			// Inside a run only a paragraph break can leave a sentence without its end. At the end of a
			// run, a short form that expects more does too.
			cut = false
			s := []rune(sentence)
			_, ended := Ended(s, 0, len(s))
			if transcript && position == len(found)-1 {
				cut = unfinished(sentence)
			} else if transcript {
				cut = !ended
			}
			// In a note or a message, a sentence without its end that more text follows is a fragment. It
			// is not whole, and what follows it is read on its own.
			fragment := !transcript && !ended && position < len(found)-1
			partial := follows || cut || fragment
			follows = cut
			// A block quote or a speaker's turn in a message is someone else's words, which the author
			// relayed: its speaker label is the author's, and no role is established for it.
			spokenBy := who
			if quotedAt[position] {
				spokenBy = role{"unknown", QuotedBasis, nil}
			}
			reached, size = reached+size, nonSpace(sentence)
			from := bisect(ends, reached, true)
			if from == len(ends) {
				panic(invalid)
			}
			lies := r.pieces[from:min(bisect(ends, reached+size, false)+1, len(ends))]
			shown := "more"
			switch {
			case partial:
				shown = "cut"
			case length(sentence) > MaxSentence:
				shown = "long"
			}
			// Joined cues may be out of time order: the times cover every cue the sentence lies in.
			var span *UnitSpan
			if lies[0].cue != nil {
				span = &UnitSpan{First: *lies[0].cue, Last: *lies[len(lies)-1].cue}
				if lies[0].start != nil {
					span.StartMS = intp(*lies[0].start)
					for _, piece := range lies {
						*span.StartMS = min(*span.StartMS, *piece.start)
					}
				}
				if lies[0].end != nil {
					span.EndMS = intp(*lies[0].end)
					for _, piece := range lies {
						*span.EndMS = max(*span.EndMS, *piece.end)
					}
				}
			}
			c.units = append(c.units, Unit{N: len(c.units) + 1, Kind: "sentence", Source: rule.system,
				RecordRef: str(field(row, "record_ref")), Tool: rule.tool, At: shownAt, Speaker: speaker,
				Role: spokenBy.role, Basis: spokenBy.basis, Contact: spokenBy.contact, Text: sentence,
				Partial: partial, Quoted: quotedAt[position], Shown: shown, Span: span, RawSHA256: rawDigest, SanitizerVersion: SanitizerVersion})
		}
		if len(found) > 0 && cut {
			waiting[name] = true
		}
	}
	if len(c.units) == before {
		c.record(row, table)
	}
	return before + 1
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
	switch {
	case errors.Is(err, ErrDay):
		panic(&Rejected{ErrDay.Error()})
	case err != nil:
		panic(invalid)
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

// amount is units._amount: an amount as printed and how many of its fields are empty. An amount in cents is
// also given as data.
func amount(row *strictjson.Object, salesforce bool, name string) (string, *Amount, int) {
	currency := field(row, "currency")
	if currency != nil && !currencyCode(currency) {
		reject("A currency must be a three-letter code or absent.")
	}
	var value string
	var figure *Amount
	if salesforce {
		raw := field(row, "amount_decimal")
		if raw == nil {
			return "Missing", nil, 1
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
		figure = &Amount{Amount: fmt.Sprintf("%s.%02d", whole, rest.Int64())}
		if currency != nil {
			code := currency.(string)
			figure.Currency = &code
		}
	}
	missing := 0
	if currency == nil {
		missing = 1
	}
	return money(value, currency), figure, missing
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
			num(more), false, nil)
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
			countdown := Countdown{Date: isoDay(best.end), Days: *daysLeft, CountsTo: "contract_end"}
			c.value(Line{Code: "contract_end", Text: fmt.Sprintf("%s; status %s%s", countdown.text(),
				c.text(field(best.contract, "status")), activated), Cites: renewalNumbers, Countdown: &countdown})
		} else {
			c.missing++
			c.value(Line{Code: "contract_end", Text: "Missing: no contract ends on or after " + isoDay(asOf),
				Cites: []int{first}})
			if len(ended) > 0 {
				last := ended[0]
				for _, d := range ended[1:] {
					if earlier(last, d) {
						last = d
					}
				}
				c.signal("contract_ended", fmt.Sprintf("The latest contract end date, %s, was %s ago.", isoDay(last.end),
					days(daysBetween(asOf, last.end))), 0, contractNumbers[last.id])
			}
		}
		c.gap("crm_unmapped_fields", "CRM: annual recurring revenue and renewal risk are not mapped "+
			"from standard Salesforce fields.", nil, false, nil)
	} else {
		renewal := dayOf(field(account, "renewal_date"))
		renewalDay = &renewal
		daysLeft = intp(daysBetween(renewal, asOf))
		countdown := Countdown{Date: isoDay(renewal), Days: *daysLeft, CountsTo: "renewal"}
		c.value(Line{Code: "renewal_date", Text: countdown.text(), Cites: []int{first}, Countdown: &countdown})
		recurring, figure, missing := amount(account, false, "arr_cents")
		c.missing += missing
		c.value(Line{Code: "arr", Text: recurring, Cites: []int{first}, Amount: figure})
		risk := field(account, "risk_status")
		if !oneOf(risk, "healthy", "at_risk") {
			reject("Unknown CRM risk status.")
		}
		reason := ""
		if truthy(field(account, "risk_reason")) {
			reason = "; reason " + c.text(field(account, "risk_reason"))
		}
		c.value(Line{Code: "crm_risk", Text: strings.ReplaceAll(risk.(string), "_", " ") + reason, Cites: []int{first}})
		if risk == "at_risk" {
			c.signal("crm_at_risk", "CRM marks this account at risk.", 0, first)
		}
	}
	c.value(Line{Code: "account", Text: fmt.Sprintf("%s; industry %s", c.text(field(account, "name")),
		c.text(field(account, "industry"))), Cites: []int{first}})
	type priced struct {
		text    string
		missing int
	}
	var stillOpen []*strictjson.Object
	open := map[string]bool{}
	amounts := map[string]priced{}
	for _, row := range opportunities {
		text, _, missing := amount(row, salesforce, "amount_cents")
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
		c.value(Line{Code: "opportunity", Text: fmt.Sprintf("%s; stage %s; amount %s; close date %s%s", name, stage,
			price.text, str(field(row, "close_date")), outcome), Cites: []int{opportunityNumbers[rowID(row)]}})
	}
	if len(opportunities) == 0 {
		c.gap("crm_no_opportunity", "CRM: no opportunity was returned for this account.", num(0), false, nil)
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
		c.value(Line{Code: "contract", Text: fmt.Sprintf("status %s; %s", c.text(field(row, "status")), ends),
			Cites: []int{contractNumbers[rowID(row)]}})
	}
	for _, row := range c.shownRows(contacts, "contact", "contacts") {
		n := contactNumbers[rowID(row)]
		title, titleValue := c.field(field(row, "title"))
		c.titles[n] = title
		name, nameValue := c.field(field(row, "name"))
		c.value(Line{Code: "contact", Text: fmt.Sprintf("%s; role %s; email %s", name, title,
			c.text(field(row, "email"))), Cites: []int{n}, Contact: &Person{Name: nameValue, Title: titleValue}})
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
		c.signal("renewal_passed", when+".", 0, renewalNumbers...)
	case len(stillOpen) > ShownRecords:
		c.signal("still_open", fmt.Sprintf("%s and %d opportunities are still open.", when, len(stillOpen)),
			len(stillOpen)-ShownRecords,
			append(append([]int(nil), renewalNumbers...), numbersOf(stillOpen[:ShownRecords], opportunityNumbers)...)...)
	default:
		for _, row := range stillOpen {
			c.signal("still_open", fmt.Sprintf("%s and this opportunity is still open at stage %s.", when,
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
		c.signal("close_after_renewal", fmt.Sprintf("%d open opportunities have a close date after the %s date, %s.",
			len(late), what,
			renewalText), len(late)-ShownRecords,
			append(append([]int(nil), renewalNumbers...), numbersOf(late[:ShownRecords], opportunityNumbers)...)...)
	} else {
		for _, row := range late {
			c.signal("close_after_renewal", fmt.Sprintf("This open opportunity's close date, %s, is after the %s date, %s.",
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
		c.signal("contact_no_role", fmt.Sprintf("%d contacts have no role recorded.", len(noRole)),
			len(noRole)-ShownRecords, numbersOf(noRole[:ShownRecords], contactNumbers)...)
	} else {
		for _, row := range noRole {
			c.signal("contact_no_role", "This contact has no role recorded.", 0, contactNumbers[rowID(row)])
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
			c.gap("calls_no_transcript", "Calls: no transcript is imported for this account.", num(0), false, nil)
		} else {
			c.gap("calls_no_record", "Calls: no call record was returned for this account.", num(0), false, nil)
		}
		return
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
		ref := str(field(note, "record_ref"))
		if !imported {
			speaker, _ := note.Get("speaker")
			firstUnit[ref] = c.spoken(note, "call_notes", whole(speaker, field(note, "body")),
				field(note, "called_at"), roleOf, false, false)
			continue
		}
		manifest := object(field(note, "manifest"))
		cited := object(field(manifest, "cited_range"))
		first, last, total := field(cited, "first"), field(cited, "last"), field(manifest, "total_units")
		firstUnit[ref] = c.spoken(note, "call_notes", cues(manifest), field(note, "called_at"), roleOf, true,
			!equal(first, 1))
		rest := "The rest of that call and every other call were not read."
		if equal(first, 1) && equal(last, total) {
			rest = "Every other call was not read."
		}
		c.gap("calls_excerpt", fmt.Sprintf("Calls: one transcript excerpt was read, %s %s to %s of %s. %s",
			str(field(manifest, "unit_kind")), pyStr(first), pyStr(last), pyStr(total), rest), nil, false, nil)
	}
	if !imported {
		noun := "records were"
		if len(notes) == 1 {
			noun = "record was"
		}
		c.gap("calls_outside_records", fmt.Sprintf("Calls: %d call %s read. Recordings and anything outside "+
			"those records were not read.", len(notes), noun), nil, false, nil)
	}
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
			c.signal("call_stale", fmt.Sprintf("The latest dated call that was read is %s old.", days(age)), 0,
				firstUnit[latestRef])
		}
	}
}

func (c *card) slack(contacts []contact) {
	sellers := c.sellers("slack_backend")
	c.known = func(name string) bool { return knownPerson(name, sellers, contacts) }
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
	roleOf := func(l *string) role {
		var v any
		if l != nil {
			v = *l
		}
		return speakerRole(v, sellers, nil, true)
	}
	read := func(n int) string {
		if n == 1 {
			return "1 message was read"
		}
		return fmt.Sprintf("%d messages were read", n)
	}
	if field(c.evidence, "slack_backend") == "slack" {
		// A live thread is read in the order Slack gave it, and its speaker is the author's user ID.
		for _, message := range c.groups["messages"] {
			speaker, _ := field(object(field(message, "slack_message")), "user_id").(string)
			var label any
			if speaker != "" {
				label = speaker
			}
			c.spoken(message, "messages", whole(label, field(message, "body")), field(message, "sent_at"), roleOf,
				false, false)
		}
		counts := object(field(object(field(c.evidence, "thread_snapshot")), "counts"))
		left, total := map[string]*big.Int{}, new(big.Int)
		for _, key := range omitted {
			n, ok := isInt(field(counts, "omitted_"+key))
			if !ok {
				panic(invalid)
			}
			left[key] = n
			total.Add(total, n)
		}
		c.gap("slack_thread", fmt.Sprintf("Slack: %s from one selected thread, and paging completed. Not read: "+
			"files (%s), attachments (%s), blocks (%s), earlier versions of edited messages, and every other thread.",
			read(len(c.groups["messages"])), left["files"], left["attachments"], left["blocks"]), total, false, nil)
		return
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
	for _, message := range messages {
		attached, ok := isInt(field(message, "files"))
		if !ok || attached.Sign() < 0 {
			reject("A Slack file count must be a nonnegative integer.")
		}
		files.Add(files, attached)
		first := c.spoken(message, "messages", whole(field(message, "author"), field(message, "body")),
			field(message, "sent_at"), roleOf, false, false)
		if attached.Sign() != 0 {
			attachedTo = append(attachedTo, fmt.Sprintf("[%d]", first))
		}
	}
	if len(messages) == 0 {
		c.gap("slack_no_message", "Slack: no message was returned for this account.", num(0), false, nil)
		return
	}
	attachedText := ""
	var fileCount *big.Int
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
		fileCount = files
	}
	c.gap("slack_channel", fmt.Sprintf("Slack: %s.%s Other channels were not read.", read(len(messages)),
		attachedText), fileCount, false, nil)
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
		c.gap("sentences_"+why, line(n, "("+strings.Join(parts, ", ")+")"), num(n), false, &counts)
	}
	readNotShown := func(n int) string {
		if n == 1 {
			return "was read and is"
		}
		return "were read and are"
	}
	withheld("cut", func(n int, roles string) string {
		return fmt.Sprintf("Sentences: %d cut off at a pause, a change of speaker, a break before their end, "+
			"or the edge of the excerpt %s not shown %s.", n, readNotShown(n), roles)
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
			"with a sentence.", len(records), noun), num(len(records)), false, nil)
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
			len(spoken), noun), num(unknown), false, &RoleCounts{Unknown: unknown})
	}
	return lines
}

// ---- the card ----

func build(evidence *strictjson.Object) ([]Unit, string, []Gap, []Line) {
	v := validate(evidence)
	asOf := dayOf(field(evidence, "as_of"))
	c := &card{evidence: evidence, groups: v.groups, titles: map[int]string{}, unshownTitles: map[int]any{}}
	contacts := c.crm(asOf)
	c.calls(asOf, contacts)
	c.slack(contacts)
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
		c.gap("crm_fields_empty", text, num(c.missing), true, nil)
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
		c.gap("neutralized", "Neutralized in source text: "+strings.Join(replaced, ", ")+".", num(n), false, nil)
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
		lines = append(append(append(lines, AttentionHeading), RenderLines(c.signals)...), "")
	}
	lines = append(append(lines, ValuesHeading), RenderLines(c.values)...)
	lines = append(append(append(lines, "", SaidHeading), said...), "", NotReadHeading)
	for _, g := range c.gaps {
		lines = append(lines, "  "+g.Text)
	}
	lines = append(lines, "")
	return c.units, strings.Join(lines, "\n"), c.gaps, append(c.signals, c.values...)
}

// Trace resolves the numbers of one sealed card, as units.trace does, from an index of its evidence built
// once: the first receipt of each tool, the first record of each reference, and each record's body digest.
type Trace struct {
	receipts map[string]int // tool to the position of its first receipt
	records  map[string]int // record_ref to the position of its first record
	// Python's next() raises at the first malformed entry it meets, so nothing at or past it resolves.
	receiptsEnd, recordsEnd int
	receiptList, recordList []*strictjson.Object
	rows                    map[int]map[string]bool
	bodies                  map[int]string
}

// NewTrace indexes a card's evidence.
func NewTrace(evidence *strictjson.Object) *Trace {
	t := &Trace{receipts: map[string]int{}, records: map[string]int{}, rows: map[int]map[string]bool{},
		bodies: map[int]string{}}
	receipts, _ := evidence.Get("receipts")
	list, _ := receipts.([]any)
	t.receiptsEnd = len(list)
	for i, value := range list {
		receipt, ok := value.(*strictjson.Object)
		tool, hasTool := receipt.Get("tool")
		if !ok || !hasTool {
			t.receiptsEnd = i
			break
		}
		t.receiptList = append(t.receiptList, receipt)
		if name, isText := tool.(string); isText {
			if _, seen := t.receipts[name]; !seen {
				t.receipts[name] = i
			}
		}
	}
	records, _ := evidence.Get("records")
	list, _ = records.([]any)
	t.recordsEnd = len(list)
	for i, value := range list {
		record, ok := value.(*strictjson.Object)
		ref, hasRef := record.Get("record_ref")
		if !ok || !hasRef {
			t.recordsEnd = i
			break
		}
		t.recordList = append(t.recordList, record)
		if name, isText := ref.(string); isText {
			if _, seen := t.records[name]; !seen {
				t.records[name] = i
			}
		}
	}
	return t
}

func (t *Trace) inRows(receipt int, ref string) bool {
	rows, indexed := t.rows[receipt]
	if !indexed {
		rows = map[string]bool{}
		value, _ := t.receiptList[receipt].Get("rows")
		list, _ := value.([]any)
		for _, row := range list {
			if text, ok := row.(string); ok {
				rows[text] = true
			}
		}
		t.rows[receipt] = rows
	}
	return rows[ref]
}

func (t *Trace) body(record int) (string, bool) {
	if d, cached := t.bodies[record]; cached {
		return d, true
	}
	value, _ := t.recordList[record].Get("body")
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	t.bodies[record] = digest(text)
	return t.bodies[record], true
}

// Resolve is units.trace's check of number n against the card's units: the unit is numbered n, the first
// receipt of its tool returned its record, and a sentence's raw_sha256 is the digest of that record's
// stored text. It gives the receipt and the record.
func (t *Trace) Resolve(units []any, n int) (receipt, record *strictjson.Object, ok bool) {
	if n < 1 || n > len(units) {
		return nil, nil, false
	}
	unit, isObject := units[n-1].(*strictjson.Object)
	tool, hasTool := unit.Get("tool")
	ref, hasRef := unit.Get("record_ref")
	number, hasN := unit.Get("n")
	toolName, toolText := tool.(string)
	refText, refIsText := ref.(string)
	if !isObject || !hasTool || !hasRef || !hasN || !toolText || !refIsText {
		return nil, nil, false
	}
	at, found := t.receipts[toolName]
	from, recorded := t.records[refText]
	if !found || at >= t.receiptsEnd || !recorded || from >= t.recordsEnd || !equal(number, n) ||
		!t.inRows(at, refText) {
		return nil, nil, false
	}
	if kind, _ := unit.Get("kind"); kind == "sentence" {
		sealed, _ := unit.Get("raw_sha256")
		body, hasBody := t.body(from)
		if !hasBody || sealed != body {
			return nil, nil, false
		}
	}
	return t.receiptList[at], t.recordList[from], true
}
