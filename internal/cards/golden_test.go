package cards

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The card families in testdata/oracle/cards hold the inputs of every card: its key, account, options and the
// source rows it reads. Only some rows also hold the card Go makes from them, byte for byte, with its digest
// and seal: the golden cards. Every other card is built from its inputs and checked by cardProperties.
//
// The golden cards are every card of cases (ten invented deals, one property each) and of demo (the demo
// book, which the README, the web app and the browser tests show), and goldenSample in the other families.
// goldenSample was chosen by this rule, which coverage.check applies again on every run: start from cases,
// demo and the cards other tests read by key (books_calls exports-gemini/0, its speaker-names import,
// exports-gemini/8 and sol/1; slack_threads gemini/0 and gemini/2). Then, over the cards in the order this
// test builds them, add the card that shows the most features (cardFeatures) no golden card shows yet, the
// first such card on a tie, until every feature any card shows is shown by a golden one. A change that makes
// a feature only a non-golden card shows fails TestCardsFromInputs, which names the cards the rule adds.
// -update writes the card of exactly the golden rows and takes the card out of every other row.
var goldenSample = map[string][]string{
	"books_demo":  {"acc-300", "acc-306", "acc-311", "acc-350", "acc-356", "acc-362", "acc-368"},
	"varied_demo": {"acc-101", "acc-106", "acc-203", "acc-206", "acc-211", "acc-215", "acc-220"},
	"books_calls": {"exports-gemini/0", "exports-gemini/0 speaker names", "exports-gemini/1", "exports-gemini/4",
		"exports-gemini/8", "exports-gemini/9", "exports-grok/1", "fable/0 speaker names", "fable/2",
		"grok/4 speaker names", "grok/6", "sol/1"},
	"books_slack":      {"fable/8", "gemini/1"},
	"slack_threads":    {"gemini/0", "gemini/1", "gemini/2", "gemini/6", "gemini/7", "gemini/10"},
	"books_salesforce": {"fable/1", "fable/2", "grok/5", "grok/8"},
}

// wholeFamilies are the families every card of which is golden.
var wholeFamilies = map[string]bool{"cases": true, "demo": true}

// isGolden is whether a row of a family holds its card.
func isGolden(family, key string) bool {
	return wholeFamilies[family] || slices.Contains(goldenSample[family], key)
}

// coverage collects the features of every card built from its inputs, in the order they are built, and
// which of them a golden card shows.
type coverage struct {
	cards  []featured
	golden map[string]bool
}

type featured struct {
	name     string // family and key
	features []string
}

func newCoverage() *coverage { return &coverage{golden: map[string]bool{}} }

func (c *coverage) add(family, key string, card *strictjson.Object, golden bool) {
	features := cardFeatures(family, card)
	c.cards = append(c.cards, featured{family + " " + key, features})
	for _, f := range features {
		if golden {
			c.golden[f] = true
		}
	}
}

// check fails when a feature is shown only by cards that are not golden, and names the cards the rule
// above adds to goldenSample.
func (c *coverage) check(t *testing.T) {
	t.Helper()
	missing := map[string]bool{}
	for _, card := range c.cards {
		for _, f := range card.features {
			if !c.golden[f] {
				missing[f] = true
			}
		}
	}
	shown := len(c.golden) + len(missing)
	var added []string
	for len(missing) > 0 {
		best, most := -1, 0
		for i, card := range c.cards {
			n := 0
			for _, f := range card.features {
				if missing[f] {
					n++
				}
			}
			if n > most {
				best, most = i, n
			}
		}
		added = append(added, c.cards[best].name)
		for _, f := range c.cards[best].features {
			if missing[f] {
				t.Errorf("no golden card shows %q", f)
				delete(missing, f)
			}
		}
	}
	if len(added) > 0 {
		t.Errorf("add these cards to goldenSample and run -update: %s", strings.Join(added, "; "))
	}
	t.Logf("the golden cards show all %d features the %d cards built from inputs show", shown, len(c.cards))
}

var (
	digits = regexp.MustCompile(`[0-9]+`)
	quoted = regexp.MustCompile(`"[^"]*"`)
	number = regexp.MustCompile(`\b[0-9][0-9,.:-]*\b`)
)

// shape is a sentence the connector wrote with each quoted name as Q, the number 1 kept, since it is the
// one a sentence is singular for, and every other number, date or amount as N.
func shape(s string) string {
	s = quoted.ReplaceAllString(s, "Q")
	return number.ReplaceAllStringFunc(s, func(n string) string {
		if n == "1" {
			return n
		}
		return "N"
	})
}

// cardFeatures is what a card shows that a test of cards, lines or renderings can tell apart: its family, its
// sources and their states, each gap, each kind of unit by source and table, each role and basis of a
// sentence, how a sentence is shown, each line code with its countdown, amount and contact shapes, and each
// heading of its text.
func cardFeatures(family string, card *strictjson.Object) []string {
	found := map[string]bool{"family " + family: true}
	add := func(format string, args ...any) { found[fmt.Sprintf(format, args...)] = true }
	ev := get(card, "evidence").(*strictjson.Object)
	add("status %v", get(card, "status"))
	if sources, ok := get(card, "sources").(*strictjson.Object); ok {
		var pairs []string
		for _, k := range sources.Keys {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, get(sources, k)))
		}
		add("sources %s", strings.Join(pairs, " "))
	}
	add("states calls %v slack %v", get(ev, "notes_status"), get(ev, "slack_status"))
	add("backends %v %v %v", get(ev, "crm_backend"), get(ev, "calls_backend"), get(ev, "slack_backend"))
	if s, ok := get(ev, "sellers").([]any); ok && len(s) > 0 {
		add("listed sellers")
	}
	if snapshot, ok := get(ev, "thread_snapshot").(*strictjson.Object); ok {
		counts := get(snapshot, "counts").(*strictjson.Object)
		for _, k := range counts.Keys {
			if n := intOf(get(counts, k)); n > 0 && k != "response_bytes" && k != "raw_rows" && k != "messages" {
				add("thread %s %s", k, shape(strconv.Itoa(n)))
			}
		}
	}
	for _, value := range get(ev, "records").([]any) {
		if manifest, ok := get(value.(*strictjson.Object), "manifest").(*strictjson.Object); ok {
			whole := evidence.Equal(get(manifest, "cited_range"), get(manifest, "requested_range"))
			add("transcript %v in %v, time %t, cited as requested %t", get(manifest, "format"),
				get(manifest, "unit_kind"), get(manifest, "called_at") != nil, whole)
		}
	}
	for _, value := range get(card, "gaps").([]any) {
		gap := value.(*strictjson.Object)
		// Each clause of a gap's text, so that a list such as "N mentions, 1 link" counts each kind once.
		for _, clause := range strings.Split(shape(fmt.Sprint(get(gap, "text"))), ", ") {
			add("gap %v clause %s", get(gap, "code"), clause)
		}
		add("gap %v %v", get(gap, "code"), get(gap, "disposition"))
		if get(gap, "by_role") != nil {
			add("gap %v by role", get(gap, "code"))
		}
	}
	for _, value := range get(card, "units").([]any) {
		unit := value.(*strictjson.Object)
		ref := strings.SplitN(get(unit, "record_ref").(string), "/", 3)
		table := ref[0] + "/" + ref[1]
		if get(unit, "kind") == "record" {
			add("record %s", table)
			continue
		}
		add("sentence %s", table)
		add("role %s %v", ref[0], get(unit, "role"))
		add("basis %s %s", ref[0], digits.ReplaceAllString(fmt.Sprint(get(unit, "basis")), "N"))
		add("shown %v", get(unit, "shown"))
		for _, flag := range []string{"partial", "quoted"} {
			if get(unit, flag) == true {
				add("%s %s", flag, ref[0])
			}
		}
		if get(unit, "speaker") == nil {
			add("no speaker %s", ref[0])
		}
		if get(unit, "at") == nil {
			add("no time %s", ref[0])
		}
		if span, ok := get(unit, "span").(*strictjson.Object); ok {
			add("span, times %t", get(span, "start_ms") != nil)
		}
	}
	if _, _, _, lines, err := evidence.Build(ev); err == nil {
		for _, l := range lines {
			add("line %s %s", l.Section, l.Code)
			// Each clause of a signal, so that "The renewal is today and N opportunities are still open."
			// counts its two halves apart.
			for _, clause := range strings.Split(strings.TrimSuffix(shape(l.Text), "."), " and ") {
				if l.Section == "signal" {
					add("signal %s clause %s", l.Code, clause)
				}
			}
			if l.More > 0 {
				add("line %s and more", l.Code)
			}
			if l.Countdown != nil {
				add("countdown to %s, passed %t", l.Countdown.CountsTo, l.Countdown.Days < 0)
			}
			if l.Amount != nil && l.Amount.Currency == nil {
				add("amount with no currency")
			}
			if l.Contact != nil {
				add("contact with name %t, title %t", l.Contact.Name != nil, l.Contact.Title != nil)
			}
		}
	}
	for _, line := range strings.Split(get(card, "text").(string), "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, "role") ||
			line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "DEAL CARD") &&
				!strings.HasPrefix(line, "Sources read") {
			add("heading %s", digits.ReplaceAllString(strings.TrimSpace(line), "N"))
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// oneLine is a label as the card compares one: its words joined by one space.
func oneLine(s string) string {
	var words []string
	for _, w := range strings.FieldsFunc(s, u.IsSpace) {
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// cardProperties are what every card built from its inputs holds, golden or not:
//
//   - it verifies against its seal and the read receipts in the log it was made in;
//   - every unit names a record the evidence holds, read by a receipt the evidence and the log both hold with
//     status ok and that record among its rows, and every number the text cites is a unit;
//   - neutralizing changes nothing more: each sentence and each source text of the evidence, neutralized
//     once, is unchanged by a second pass;
//   - no sentence holds a live link, a bare mail address or a code fence;
//   - no sentence is attributed to the wrong side: a seller's sentence is spoken by a listed seller, a
//     customer's by the one contact of that name, and a sentence the author quoted has no role;
//   - built again from the same inputs, it is the same bytes.
func cardProperties(t *testing.T, key string, sealed, again *Sealed, log Log, sources evidence.Sources,
	m *oracletest.Mismatches) {
	t.Helper()
	if err := renewable(sealed, log, sources); err != nil {
		m.Add("%s: does not verify against its seal: %v", key, err)
	}
	if again == nil || string(again.Payload) != string(sealed.Payload) {
		m.Add("%s: built twice from the same inputs, the bytes differ", key)
	}
	card := load(t, sealed.Payload).(*strictjson.Object)
	ev := get(card, "evidence").(*strictjson.Object)
	records := map[string]*strictjson.Object{}
	for _, value := range get(ev, "records").([]any) {
		record := value.(*strictjson.Object)
		records[get(record, "record_ref").(string)] = record
	}
	read := map[string]*strictjson.Object{} // tool: its receipt in the evidence
	var ids []string
	for _, value := range get(ev, "receipts").([]any) {
		receipt := value.(*strictjson.Object)
		read[get(receipt, "tool").(string)] = receipt
		ids = append(ids, get(receipt, "audit_id").(string))
	}
	logged := map[string]*strictjson.Object{}
	if found, err := log.Receipts(context.Background(), ids); err != nil {
		m.Add("%s: the log lacks a read the evidence names: %v", key, err)
	} else {
		for _, receipt := range found {
			logged[get(receipt, "audit_id").(string)] = receipt
		}
	}
	units := get(card, "units").([]any)
	sellers := map[string][]string{}
	for _, source := range []string{"calls", "slack"} {
		for _, list := range []string{"sellers", "demo_sellers"} {
			if list == "demo_sellers" && get(ev, source+"_backend") != "fixture" {
				continue
			}
			for _, s := range sliceOf(get(ev, list)) {
				sellers[source] = append(sellers[source], oneLine(s.(string)))
			}
		}
	}
	var contacts []string // the name of each contact record, by its unit number
	contactOf := map[int]string{}
	for _, value := range units {
		unit := value.(*strictjson.Object)
		n := intOf(get(unit, "n"))
		ref := get(unit, "record_ref").(string)
		if get(unit, "kind") == "record" && strings.HasPrefix(ref, "crm/contacts/") {
			name, _ := get(records[ref], "name").(string)
			contacts = append(contacts, oneLine(name))
			contactOf[n] = oneLine(name)
		}
	}
	for i, value := range units {
		unit := value.(*strictjson.Object)
		ref := get(unit, "record_ref").(string)
		if intOf(get(unit, "n")) != i+1 {
			m.Add("%s: unit %d is numbered %v", key, i+1, get(unit, "n"))
		}
		if records[ref] == nil {
			m.Add("%s: unit %d names %s, which the evidence does not hold", key, i+1, ref)
		}
		receipt := read[get(unit, "tool").(string)]
		if receipt == nil || get(receipt, "status") != "ok" || !slices.Contains(sliceOf(get(receipt, "rows")), any(ref)) {
			m.Add("%s: unit %d: no ok read of %s in the evidence", key, i+1, ref)
		} else if stored := logged[get(receipt, "audit_id").(string)]; stored == nil || get(stored, "status") != "ok" ||
			!slices.Contains(sliceOf(get(stored, "rows")), any(ref)) {
			m.Add("%s: unit %d: the log holds no ok read of %s", key, i+1, ref)
		}
		if get(unit, "kind") != "sentence" {
			continue
		}
		text := get(unit, "text").(string)
		if again, _, err := evidence.QuoteText(text); err != nil || again != text {
			m.Add("%s: unit %d is not neutral: a second pass gives %q", key, i+1, again)
		}
		if why := live(text); why != "" {
			m.Add("%s: unit %d holds %s: %q", key, i+1, why, text)
		}
		source := strings.SplitN(ref, "/", 2)[0]
		speaker := ""
		if s, ok := get(unit, "speaker").(string); ok {
			speaker = oneLine(s)
		}
		seller := speaker != "" && slices.Contains(sellers[source], speaker)
		var named []int
		for j, name := range contacts {
			if speaker != "" && name == speaker && source != "slack" {
				named = append(named, j)
			}
		}
		role, quoted := get(unit, "role"), get(unit, "quoted") == true
		switch {
		case quoted && role != "unknown":
			m.Add("%s: unit %d, a quotation its author relayed, is given the role %v", key, i+1, role)
		case quoted:
		case seller && len(named) == 0 && role != "seller":
			m.Add("%s: unit %d, spoken by the listed seller %q, is given the role %v", key, i+1, speaker, role)
		case !seller && len(named) == 1 && role != "customer":
			m.Add("%s: unit %d, spoken by the contact %q, is given the role %v", key, i+1, speaker, role)
		case role == "seller" && (!seller || len(named) > 0):
			m.Add("%s: unit %d is a seller's, and %q is not only a listed seller", key, i+1, speaker)
		case role == "customer" && (seller || len(named) != 1 || contactOf[intOf(get(unit, "contact"))] != speaker):
			m.Add("%s: unit %d is a customer's, and %q is not the one contact it names", key, i+1, speaker)
		}
	}
	for _, cite := range citations.FindAllStringSubmatch(get(card, "text").(string), -1) {
		if n, _ := strconv.Atoi(cite[1]); n < 1 || n > len(units) {
			m.Add("%s: the text cites [%d], and the card has %d units", key, n, len(units))
		}
	}
	for _, value := range get(ev, "records").([]any) {
		for _, k := range value.(*strictjson.Object).Keys {
			raw, ok := get(value.(*strictjson.Object), k).(string)
			if !ok {
				continue
			}
			once, _, err := evidence.SafeText(raw)
			if twice, _, _ := evidence.SafeText(once); err == nil && twice != once {
				m.Add("%s: %s of a record: a second neutralizing pass changes %q", key, k, once)
			}
		}
	}
}

var citations = regexp.MustCompile(`\[([0-9]+)\]`)

func sliceOf(v any) []any { s, _ := v.([]any); return s }

// live is what a sentence holds that a Markdown client would act on, or empty when it holds none:
//   - a link: any scheme://, a www. address, or what evidence.RawLinks finds;
//   - a bare mail address: a local part of letters, digits and ._%+- right before an @, and a domain with a
//     dot inside it after;
//   - a code fence: a line that opens a fenced code block as CommonMark reads one, after at most three
//     spaces three or more backticks with no backtick after them, or three or more tildes. Backticks inside
//     a line, as in "```seats = 40```", are a code span, not a fence.
func live(text string) string {
	lower := strings.ToLower(text)
	if len(evidence.RawLinks([]rune(text))) > 0 || strings.Contains(lower, "://") || strings.Contains(lower, "www.") {
		return "a link"
	}
	for _, line := range strings.Split(text, "\n") {
		rest := strings.TrimLeft(line, " ")
		if len(line)-len(rest) > 3 {
			continue
		}
		ticks := len(rest) - len(strings.TrimLeft(rest, "`"))
		if ticks >= 3 && !strings.Contains(rest[ticks:], "`") || strings.HasPrefix(rest, "~~~") {
			return "a code fence"
		}
	}
	local := func(r byte) bool {
		return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.IndexByte("._%+-", r) >= 0
	}
	for i := 1; i < len(text); i++ {
		if text[i] != '@' || !local(text[i-1]) {
			continue
		}
		end := i + 1
		for end < len(text) && (local(text[end]) && text[end] != '%' && text[end] != '+') {
			end++
		}
		domain := strings.TrimRight(text[i+1:end], ".-")
		if dot := strings.IndexByte(domain, '.'); dot > 0 && dot < len(domain)-1 {
			return "a mail address"
		}
	}
	return ""
}

// stripped is a fixture row without the card Go makes from it: its inputs alone.
func stripped(t *testing.T, golden *oracletest.Golden, i int, line []byte, row fixtureRow) fixtureRow {
	t.Helper()
	old := load(t, line).(*strictjson.Object)
	value := old.Copy()
	for _, member := range []string{"sha256", "card", "seal"} {
		value.Delete(member)
	}
	golden.Set(i, old, value)
	row.SHA256, row.Card, row.Seal = "", "", nil
	return row
}
