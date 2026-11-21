package people_test

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
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

var (
	matchesContact  = regexp.MustCompile(`^label matches contact \[(\d+)\]$`)
	matchesContacts = regexp.MustCompile(`^label matches (\d+) contacts$`)
	overLimit       = regexp.MustCompile(`^speaker label over \d+ characters$`)
)

// equivalent says whether a result is the resolver's form of a recorded role and basis. contactKey maps a
// contact's unit number to its person key.
func equivalent(role, basis string, contact *int, r people.Result, contactKey map[int]string) error {
	if string(r.Role) != role {
		return fmt.Errorf("role %s, recorded %s", r.Role, role)
	}
	want := func(rung people.Rung, b people.Basis, reason people.Reason) error {
		if r.Rung != rung || r.Basis != b || r.Reason != reason {
			return fmt.Errorf("rung %d basis %s reason %q, want rung %d basis %s reason %q", r.Rung, r.Basis,
				r.Reason, rung, b, reason)
		}
		return nil
	}
	switch {
	case basis == "listed seller identity":
		if r.Person == nil || contact != nil {
			return fmt.Errorf("no listed seller")
		}
		return want(people.RungName, people.ByName, people.NoReason)
	case matchesContact.MatchString(basis):
		n, _ := strconv.Atoi(matchesContact.FindStringSubmatch(basis)[1])
		if contact == nil || *contact != n || r.Person == nil || r.Person.Key != contactKey[n] {
			return fmt.Errorf("person %v, recorded contact [%d]", r.Person, n)
		}
		return want(people.RungName, people.ByName, people.NoReason)
	case matchesContacts.MatchString(basis):
		n, _ := strconv.Atoi(matchesContacts.FindStringSubmatch(basis)[1])
		if len(r.Candidates) != n {
			return fmt.Errorf("%d candidates, recorded %d contacts", len(r.Candidates), n)
		}
		return want(people.RungName, people.ByName, people.SeveralCandidates)
	case basis == "label matches a listed seller and a contact":
		return want(people.RungName, people.ByName, people.CandidatesOnBothSides)
	case basis == "author is not a listed seller; Slack identities cannot show a customer":
		return want(people.NoRung, people.NoBasis, people.SlackAuthorUnidentified)
	case basis == "label matches no listed seller and no contact":
		if r.Reason == people.AnonymousLabel {
			return want(people.NoRung, people.NoBasis, people.AnonymousLabel)
		}
		return want(people.NoRung, people.NoBasis, people.NoMatch)
	case basis == "no speaker label":
		return want(people.NoRung, people.NoBasis, people.NoLabel)
	}
	return fmt.Errorf("unrecognized recorded basis %q", basis)
}

// TestRecordedCards feeds the resolver, for every sentence of the recorded cards, only what today's rule
// sees: the label, the account's contacts and the listed sellers, with no emails, no aliases and rung 5
// off. Each result must be the recorded role with an equivalent basis.
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
	cards, sentences, cut := 0, 0, 0
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
				if overLimit.MatchString(basis) {
					// The card replaces the role of a label it cannot print before any rule reads it.
					cut++
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
				if err := equivalent(role, basis, contact, r, contactKey); err != nil {
					m.Add("%s %s %q: %v (recorded %q)", family, refName, label, err, basis)
					continue
				}
				roles[role]++
				if r.Role == people.Unknown {
					reasons[string(r.Reason)]++
				}
			}
		}
	}
	t.Logf("%d cards, %d sentences: %d with a label over the limit, decided before any rule", cards, sentences, cut)
	t.Logf("by role: customer %d, seller %d, unknown %d", roles["customer"], roles["seller"], roles["unknown"])
	t.Logf("unknown by reason: %v", reasons)
	if cards != 450 {
		t.Errorf("%d recorded cards, want 450", cards)
	}
}
