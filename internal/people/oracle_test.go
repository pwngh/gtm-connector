package people_test

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/people"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

func get(value any, key string) any {
	o, ok := value.(*strictjson.Object)
	if !ok {
		return nil
	}
	v, _ := o.Get(key)
	return v
}

func list(value any) []any {
	l, _ := value.([]any)
	return l
}

func number(value any) (int, bool) {
	n, ok := value.(*big.Int)
	if !ok || !n.IsInt64() {
		return 0, false
	}
	return int(n.Int64()), true
}

func text(value any) string {
	s, _ := value.(string)
	return s
}

// rawLabels is every speaker label a recorded record can hand the card: a call note's speaker, a Slack
// message's author or live author ID, and in an imported transcript each cue identifier, each name that
// opens a line, and each voice tag's name.
func rawLabels(record any) []string {
	var out []string
	for _, key := range []string{"speaker", "author"} {
		if s, ok := get(record, key).(string); ok {
			out = append(out, s)
		}
	}
	if s, ok := get(get(record, "slack_message"), "user_id").(string); ok {
		out = append(out, s)
	}
	manifest := get(record, "manifest")
	for _, cue := range list(get(manifest, "units")) {
		if s, ok := get(cue, "cue_id").(string); ok {
			out = append(out, s)
		}
		line := text(get(cue, "text"))
		if name, _, ok := evidence.NamedLine(line); ok {
			out = append(out, name)
		}
		runes := []rune(line)
		for _, tag := range evidence.VoiceTags(runes) {
			if tag.Named {
				out = append(out, string(runes[tag.NameStart:tag.NameEnd]))
			}
		}
	}
	return out
}

// shown is the speaker a card prints for a raw label: the label on one line, neutralized, on one line.
func shown(raw string) string {
	safe, _, err := evidence.SafeText(people.Label(raw))
	if err != nil {
		return ""
	}
	return people.Label(safe)
}

var overLimit = regexp.MustCompile(`^speaker label over \d+ characters$`)

// reasonWords are the bases a card prints for the reasons the recorded evidence can give.
var reasonWords = map[people.Reason]string{
	people.NoLabel:                 "no speaker label",
	people.NoMatch:                 "names no listed seller, contact or user",
	people.AnonymousLabel:          "anonymous label with no alias for this call",
	people.SlackAuthorUnidentified: "Slack author not identified; a Slack name never shows a customer",
}

// equivalent says whether a sealed unit's role, basis, rung and contact are the resolver's result.
// contactKey maps a contact's unit number to its person key.
func equivalent(role, basis string, rung int, contact *int, r people.Result, contactKey map[int]string) error {
	if string(r.Role) != role || int(r.Rung) != rung {
		return fmt.Errorf("role %s by rung %d, sealed %s by rung %d", r.Role, r.Rung, role, rung)
	}
	if r.Role == people.Customer {
		if contact == nil || r.Person == nil || r.Person.Key != contactKey[*contact] {
			return fmt.Errorf("person %v, sealed contact %v", r.Person, contact)
		}
	} else if contact != nil {
		return fmt.Errorf("a %s with contact [%d]", role, *contact)
	}
	want := "by name"
	switch {
	case r.Reason == people.SeveralCandidates:
		want = fmt.Sprintf("%d people match by name", len(r.Candidates))
	case r.Reason == people.CandidatesOnBothSides:
		want = fmt.Sprintf("%d people on both sides match by name", len(r.Candidates))
	case r.Role == people.Unknown:
		want = reasonWords[r.Reason]
	}
	if basis != want {
		return fmt.Errorf("basis %q, the resolver's %q", basis, want)
	}
	return nil
}

// TestRecordedCards feeds the resolver, for every sentence of the recorded cards, what their evidence holds
// of people: the label, the account's contacts and the listed sellers. The evidence holds no attendee
// list, Slack profile, alias or user, so only the name rung can decide. Each result must be the role, basis,
// rung and contact the card sealed.
func TestRecordedCards(t *testing.T) {
	var index struct {
		Families map[string]int `json:"families"`
	}
	oracletest.JSON(t, "cards/index.json", &index)
	families := make([]string, 0, len(index.Families))
	for name := range index.Families {
		families = append(families, name)
	}
	sort.Strings(families)
	roles, reasons := map[string]int{}, map[string]int{}
	cards, sentences, cut, quoted := 0, 0, 0, 0
	for _, family := range families {
		m := oracletest.NewMismatches(t, family)
		for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
			row, err := strictjson.Loads(line)
			if err != nil {
				t.Fatal(err)
			}
			body, ok := get(row, "card").(string)
			if !ok {
				continue
			}
			card, err := strictjson.LoadsText(body)
			if err != nil {
				t.Fatal(err)
			}
			cards++
			m.Row()
			account := text(get(card, "account_id"))
			ev := get(card, "evidence")
			numbers := map[string]int{}
			for _, unit := range list(get(card, "units")) {
				if text(get(unit, "kind")) == "record" {
					n, _ := number(get(unit, "n"))
					numbers[text(get(unit, "record_ref"))] = n
				}
			}
			var contacts []people.Person
			contactKey := map[int]string{}
			records := map[string]any{}
			for _, record := range list(get(ev, "records")) {
				ref := text(get(record, "record_ref"))
				records[ref] = record
				if len(ref) <= len("crm/contacts/") || ref[:len("crm/contacts/")] != "crm/contacts/" {
					continue
				}
				id := text(get(record, "id"))
				p := people.Person{Key: "contact:" + id, Identifiers: []people.Identifier{
					{Kind: people.SalesforceContact, Value: id, Scope: text(get(record, "account_id")), From: ref}}}
				if name, ok := get(record, "name").(string); ok {
					p.Identifiers = append(p.Identifiers, people.Identifier{Kind: people.Name, Value: name, From: ref})
				}
				contacts = append(contacts, p)
				contactKey[numbers[ref]] = p.Key
			}
			known := func(backend string, source people.Source) people.Known {
				var entries []any
				entries = append(entries, list(get(ev, "sellers"))...)
				if text(get(ev, backend)) == "fixture" {
					entries = append(entries, list(get(ev, "demo_sellers"))...)
				}
				k := people.Known{Account: account}
				if source == people.Call {
					k.People = append(k.People, contacts...)
				}
				seen := map[string]bool{}
				for _, entry := range entries {
					l := people.Label(text(entry))
					if l == "" || seen[l] {
						continue
					}
					seen[l] = true
					k.People = append(k.People, people.Person{Key: "seller:" + l, Identifiers: []people.Identifier{
						{Kind: people.ListedSeller, Value: text(entry), From: "sellers"}}})
				}
				return k
			}
			byTable := map[string]people.Known{"calls": known("calls_backend", people.Call),
				"slack": known("slack_backend", people.Slack)}
			for _, unit := range list(get(card, "units")) {
				if text(get(unit, "kind")) != "sentence" {
					continue
				}
				sentences++
				role, basis := text(get(unit, "role")), text(get(unit, "basis"))
				rung, _ := number(get(unit, "rung"))
				if overLimit.MatchString(basis) {
					// The card replaces the role of a label it cannot print before any rule reads it.
					cut++
					continue
				}
				if get(unit, "quoted") == true {
					// A sentence the author quoted is no one's established words, whatever the author's label.
					quoted++
					continue
				}
				refName := text(get(unit, "record_ref"))
				label := ""
				if speaker, ok := get(unit, "speaker").(string); ok {
					found := map[string]bool{}
					for _, raw := range rawLabels(records[refName]) {
						if shown(raw) == speaker {
							found[people.Label(raw)] = true
						}
					}
					if len(found) != 1 {
						m.Add("%s %s: %d raw labels print as %q", family, refName, len(found), speaker)
						continue
					}
					for l := range found {
						label = l
					}
				}
				source := people.Call
				if text(get(unit, "source")) == "slack" {
					source = people.Slack
				}
				var contact *int
				if n, ok := number(get(unit, "contact")); ok {
					contact = &n
				}
				r := people.Resolve(byTable[text(get(unit, "source"))], people.Ref{Source: source, Label: label})
				if err := equivalent(role, basis, rung, contact, r, contactKey); err != nil {
					m.Add("%s %s %q: %v (sealed %q)", family, refName, label, err, basis)
					continue
				}
				roles[role]++
				if r.Role == people.Unknown {
					reasons[string(r.Reason)]++
				}
			}
		}
	}
	t.Logf("%d cards, %d sentences: %d with a label over the limit, decided before any rule, and %d quoted by "+
		"their message's author, under no role", cards, sentences, cut, quoted)
	t.Logf("by role: customer %d, seller %d, unknown %d", roles["customer"], roles["seller"], roles["unknown"])
	t.Logf("unknown by reason: %v", reasons)
	if cards != 450 {
		t.Errorf("%d recorded cards, want 450", cards)
	}
}
