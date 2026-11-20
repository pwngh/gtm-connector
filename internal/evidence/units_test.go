package evidence

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// firstDifference is the first line where two texts differ, for a mismatch report.
func firstDifference(got, want string) string {
	a, b := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return "line " + strconv.Itoa(i+1) + ": got " + strictjson.Quote(x) + ", want " + strictjson.Quote(y)
		}
	}
	return "no line differs"
}

// TestBuildFromSealedEvidence rebuilds every oracle card from the evidence its file seals and compares the
// text, units and gaps. Evidence whose checks are not ported yet must be refused as not ported.
func TestBuildFromSealedEvidence(t *testing.T) {
	families := []string{"demo", "books_demo", "varied_demo", "cases", "books_calls", "books_salesforce",
		"books_slack", "slack_threads"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			mismatches := oracletest.NewMismatches(t, "cards/"+family)
			rebuilt, refused := 0, 0
			for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
				var row struct {
					Key  string `json:"key"`
					Card string `json:"card"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					t.Fatal(err)
				}
				if row.Card == "" {
					continue
				}
				mismatches.Row()
				value, err := strictjson.LoadsText(row.Card)
				if err != nil {
					t.Fatal(err)
				}
				card := value.(*strictjson.Object)
				evidence, _ := card.Get("evidence")
				units, text, gaps, err := Build(evidence.(*strictjson.Object))
				if errors.Is(err, ErrNotPorted) {
					refused++
					continue
				}
				if err != nil {
					mismatches.Add("%s: refused: %v", row.Key, err)
					continue
				}
				rebuilt++
				wantText, _ := card.Get("text")
				if text != wantText {
					mismatches.Add("%s: text differs at %s", row.Key, firstDifference(text, wantText.(string)))
				}
				wantUnits, _ := card.Get("units")
				if !Equal(UnitValues(units), wantUnits) {
					mismatches.Add("%s: units differ", row.Key)
				}
				wantGaps, _ := card.Get("gaps")
				if !Equal(GapValues(gaps), wantGaps) {
					mismatches.Add("%s: gaps differ", row.Key)
				}
			}
			t.Logf("%s: %d rebuilt from sealed evidence, %d not ported", family, rebuilt, refused)
		})
	}
}
