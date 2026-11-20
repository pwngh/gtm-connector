package evidence

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// rebuilt pins how many cards of each family are rebuilt from the evidence they seal. The rest hold an
// imported transcript or live Slack messages, whose checks are not ported yet.
var rebuilt = map[string]int{
	"demo": 33, "books_demo": 78, "varied_demo": 46, "cases": 19, "books_calls": 32, "books_salesforce": 78,
	"books_slack": 10, "slack_threads": 0,
}

// notPorted is whether evidence holds what Build may refuse as not ported: an imported call note, or a live
// Slack message.
func notPorted(evidence *strictjson.Object) bool {
	calls, _ := evidence.Get("calls_backend")
	slack, _ := evidence.Get("slack_backend")
	records, _ := evidence.Get("records")
	list, _ := records.([]any)
	for _, value := range list {
		record, _ := value.(*strictjson.Object)
		ref, _ := record.Get("record_ref")
		text, _ := ref.(string)
		if calls == "imported" && strings.HasPrefix(text, "calls/call_notes/") ||
			slack == "slack" && strings.HasPrefix(text, "slack/messages/") {
			return true
		}
	}
	return false
}

// TestBuildFromSealedEvidence rebuilds every oracle card from the evidence its file seals and compares the
// text, units and gaps.
func TestBuildFromSealedEvidence(t *testing.T) {
	for family, want := range rebuilt {
		t.Run(family, func(t *testing.T) {
			mismatches := oracletest.NewMismatches(t, "cards/"+family)
			built, refused := 0, 0
			for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
				var row struct {
					Key  string `json:"key"`
					Card string `json:"card"`
				}
				if err := json.Unmarshal(line, &row); err != nil {
					t.Fatal(err)
				}
				mismatches.Row()
				value, err := strictjson.LoadsText(row.Card)
				if err != nil {
					t.Fatal(err)
				}
				card := value.(*strictjson.Object)
				evidence, _ := card.Get("evidence")
				units, text, gaps, err := Build(evidence.(*strictjson.Object))
				if errors.Is(err, ErrNotPorted) && notPorted(evidence.(*strictjson.Object)) {
					refused++
					continue
				}
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
			t.Logf("%s: %d rebuilt from sealed evidence, %d hold an imported transcript or live Slack", family, built, refused)
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
	for _, line := range oracletest.Lines(t, "cards/evidence.jsonl.gz") {
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
		units, text, gaps, err := Build(object)
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
		_, _, gaps, err := Build(evidence.(*strictjson.Object))
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
