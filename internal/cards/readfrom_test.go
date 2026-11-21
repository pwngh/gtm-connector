package cards

import (
	"encoding/json"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// A saved card was read from the sources its evidence names, and from no others: a change of any backend or
// binding, or bytes with no evidence, is another source.
func TestReadFromComparesTheCardsSources(t *testing.T) {
	var recorded struct{ Card string }
	if err := json.Unmarshal(oracletest.Lines(t, "cards/demo.jsonl.gz")[0], &recorded); err != nil {
		t.Fatal(err)
	}
	card := []byte(recorded.Card)
	if !ReadFrom(card, demoSources(nil)) {
		t.Fatal("a demo card was not read from the demo sources")
	}
	moved := demoSources(nil)
	moved.CallsBackend = "none"
	other := demoSources(nil)
	other.CRMBackend = "salesforce"
	for label, bound := range map[string]bool{
		"calls detached":      ReadFrom(card, moved),
		"another CRM":         ReadFrom(card, other),
		"bytes with no card":  ReadFrom([]byte(`{"text": "x"}`), demoSources(nil)),
		"bytes that are none": ReadFrom([]byte(`not json`), demoSources(nil)),
	} {
		if bound {
			t.Errorf("%s: read from the demo sources", label)
		}
	}
}
