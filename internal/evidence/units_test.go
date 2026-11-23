package evidence

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// rebuilt pins how many golden cards each family holds, every one of which is rebuilt from the evidence it
// seals. The other rows of a family hold inputs alone; internal/cards builds them and checks their
// properties.
var rebuilt = map[string]int{
	"demo": 33, "books_demo": 7, "varied_demo": 7, "cases": 19, "books_calls": 12, "books_salesforce": 5,
	"books_slack": 2, "slack_threads": 6,
}

// TestBuildFromSealedEvidence rebuilds every golden card from the evidence its file seals and compares the
// text, units and gaps.
func TestBuildFromSealedEvidence(t *testing.T) {
	for family, want := range rebuilt {
		t.Run(family, func(t *testing.T) {
			mismatches := oracletest.NewMismatches(t, "cards/"+family)
			built := 0
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
				units, text, gaps, _, err := Build(evidence.(*strictjson.Object))
				if err != nil {
					mismatches.Add("%s: refused: %v", row.Key, err)
					continue
				}
				built++
				wantText, _ := card.Get("text")
				if text != wantText {
					mismatches.Add("%s: text differs at %s", row.Key, oracletest.FirstDifference(text, wantText.(string)))
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
			if built != want {
				t.Errorf("%s: %d rebuilt, want %d", family, built, want)
			}
			t.Logf("%s: %d rebuilt from sealed evidence", family, built)
		})
	}
}

// TestEvidenceVectors replays every units.build call of the Python tests that refuse evidence: the same
// refusal message, or the same units, text and gaps.
func TestEvidenceVectors(t *testing.T) {
	var index struct {
		Vectors  int `json:"vectors"`
		Rejected int `json:"rejected"`
	}
	oracletest.JSON(t, "cards/evidence_index.json", &index)
	mismatches := oracletest.NewMismatches(t, "cards/evidence")
	rejected, accepted := 0, 0
	golden := oracletest.NewGolden(t, "cards/evidence.jsonl.gz", strictjson.Canonical.Dumps)
	for i, line := range golden.Lines() {
		value, err := strictjson.Loads(line)
		if err != nil {
			t.Fatal(err)
		}
		vector := value.(*strictjson.Object)
		mismatches.Row()
		test, _ := vector.Get("test")
		evidence, _ := vector.Get("evidence")
		// Build takes evidence as an object; anything else is refused as Python refuses it.
		object, _ := evidence.(*strictjson.Object)
		if object == nil {
			object = &strictjson.Object{}
		}
		units, text, gaps, _, err := Build(object)
		if want, isRejection := vector.Get("rejected"); isRejection {
			rejected++
			if err == nil || err.Error() != want {
				mismatches.Add("%s: got %v, want %q", test, err, want)
			}
			continue
		}
		accepted++
		if err != nil {
			mismatches.Add("%s: refused: %v", test, err)
			continue
		}
		if oracletest.Update() {
			renewed := vector.Copy()
			renewed.Set("units", UnitValues(units))
			renewed.Set("text", text)
			renewed.Set("gaps", GapValues(gaps))
			golden.Set(i, vector, renewed)
			vector = renewed
		}
		wantText, _ := vector.Get("text")
		wantUnits, _ := vector.Get("units")
		wantGaps, _ := vector.Get("gaps")
		if text != wantText || !Equal(UnitValues(units), wantUnits) || !Equal(GapValues(gaps), wantGaps) {
			mismatches.Add("%s: card differs at %s", test, oracletest.FirstDifference(text, wantText.(string)))
		}
	}
	if rejected+accepted != index.Vectors || rejected != index.Rejected {
		t.Errorf("read %d vectors, %d rejected; the index says %d and %d", rejected+accepted, rejected,
			index.Vectors, index.Rejected)
	}
	t.Logf("evidence vectors: %d rejected with Python's message, %d accepted with Python's card", rejected, accepted)
}

// TestSlackFileCountsKeepTheirSize: the synthetic vector's two file counts sum past 2**63.
func TestSlackFileCountsKeepTheirSize(t *testing.T) {
	for _, line := range oracletest.Lines(t, "cards/evidence.jsonl.gz") {
		value, _ := strictjson.Loads(line)
		vector := value.(*strictjson.Object)
		if test, _ := vector.Get("test"); !strings.HasPrefix(test.(string), "synthetic") {
			continue
		}
		evidence, _ := vector.Get("evidence")
		_, _, gaps, _, err := Build(evidence.(*strictjson.Object))
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gaps {
			if g.Code == "slack_channel" {
				want, _ := new(big.Int).SetString("27670116110564327424", 10)
				if g.Count == nil || g.Count.Cmp(want) != 0 {
					t.Fatalf("slack_channel count %v, want %v", g.Count, want)
				}
				return
			}
		}
		t.Fatal("no slack_channel gap")
	}
	t.Fatal("no synthetic vector")
}
