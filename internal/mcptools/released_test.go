package mcptools

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Both brief tools return a unit saved under an older neutralizer version as today's rules show it, and the
// saved brief they read it from is not changed. A unit saved under today's version is returned as saved.
func TestABriefToolShowsAnOlderUnitUnderTodaysRules(t *testing.T) {
	const sealed = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) <jovan.mercer@quarrystream.example>"
	older := strictjson.NewObject("n", 2, "kind", "sentence", "text", sealed, "sanitizer_version", 3)
	today := strictjson.NewObject("n", 3, "kind", "sentence", "text", "Signed.", "sanitizer_version",
		evidence.SanitizerVersion)
	record := strictjson.NewObject("n", 1, "kind", "record")
	artifact := strictjson.NewObject("account_id", "acc-003", "units", []any{record, older, today})
	brief, err := released("brief-x", artifact)
	if err != nil {
		t.Fatal(err)
	}
	units := member(brief, "units").([]any)
	if got := member(units[1].(*strictjson.Object), "text"); got != "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) [link mail]" {
		t.Errorf("the older unit is returned as %q", got)
	}
	if units[0] != record || units[2] != today || member(older, "text") != sealed {
		t.Error("a unit was changed that today's rules show as saved, or the saved brief was changed")
	}
}
