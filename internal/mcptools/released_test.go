package mcptools

import (
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// cardText is a card's text that quotes one sentence, unit 2, under SAID.
func cardText(quoted string) string {
	return "DEAL CARD: \"Invented account\" (acc-003) as of 2027-01-15\n\n" + evidence.SaidHeading +
		"\n  No established role\n    [2] Slack 2027-01-13T15:00:00Z; no speaker label\n        " +
		strictjson.Quote(quoted) + "\n\n" + evidence.NotReadHeading + "\n  Speakers: 1 of 1 sentence has no " +
		"established speaker role.\n"
}

// Both brief tools return a saved brief's units and text as today's rules show them, for a model to read: a
// unit saved under an older neutralizer version neutralized again, and the text rendered again from it, with
// a first line that says so. The saved brief they read it from is not changed. A brief made under today's
// rules is returned as saved.
func TestABriefToolShowsAnOlderUnitUnderTodaysRules(t *testing.T) {
	const sealed = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) <jovan.mercer@quarrystream.example>"
	const shown = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) [link mail]"
	gaps := []any{strictjson.NewObject("code", "speakers_unknown", "text",
		"Speakers: 1 of 1 sentence has no established speaker role.")}
	record := strictjson.NewObject("n", 1, "kind", "record")
	for _, version := range []int{3, evidence.SanitizerVersion} {
		unit := strictjson.NewObject("n", 2, "kind", "sentence", "text", sealed, "sanitizer_version", version)
		artifact := strictjson.NewObject("account_id", "acc-003", "text", cardText(sealed),
			"units", []any{record, unit}, "gaps", gaps)
		brief, err := released("brief-x", artifact)
		if err != nil {
			t.Fatal(err)
		}
		units := member(brief, "units").([]any)
		text := member(brief, "text").(string)
		if version == evidence.SanitizerVersion {
			if units[1] != unit || text != cardText(sealed) {
				t.Errorf("a brief made under today's rules is not returned as saved:\n%s", text)
			}
			continue
		}
		if got := member(units[1].(*strictjson.Object), "text"); got != shown {
			t.Errorf("the older unit is returned as %q", got)
		}
		first, rest, _ := strings.Cut(text, "\n")
		if !strings.Contains(first, "rendered again") || rest != cardText(shown) {
			t.Errorf("the older brief's text is returned as:\n%s", text)
		}
		if units[0] != record || member(unit, "text") != sealed || member(artifact, "text") != cardText(sealed) {
			t.Error("the saved brief was changed")
		}
	}
}
