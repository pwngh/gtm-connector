package evidence

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The signals at the edges of their thresholds, on the README's card: its renewal date is 2027-02-08, its one
// opportunity is open and closes on 2027-02-01, and its newest call is dated 2027-01-12. A renewal 30 days
// away raises the open opportunity and one 31 days away does not, a call 61 days old is stale and one 60
// days old is not, and a close date raises its signal only when it is after the renewal date.
func TestSignalsAtTheirBoundaries(t *testing.T) {
	const readme = "test_10_the_readme_shows_the_demo_card_byte_for_byte/1"
	var recorded *strictjson.Object
	for _, c := range recordedCards(t, "cases") {
		if c.key == readme {
			recorded = c.evidence
		}
	}
	if recorded == nil {
		t.Fatalf("the recorded cases hold no %s", readme)
	}
	// signals builds the card as of a day, with the opportunity's close date changed when one is given, and
	// gives each signal's sentence by its code.
	signals := func(t *testing.T, asOf, closeDate string) map[string]string {
		t.Helper()
		evidence := recorded.Copy()
		evidence.Set("as_of", asOf)
		records, _ := recorded.Get("records")
		changed := make([]any, len(records.([]any)))
		for i, value := range records.([]any) {
			record := value.(*strictjson.Object).Copy()
			if ref, _ := record.Get("record_ref"); ref == "crm/opportunities/opp-003" && closeDate != "" {
				record.Set("close_date", closeDate)
			}
			changed[i] = record
		}
		evidence.Set("records", changed)
		_, _, _, lines, err := Build(evidence)
		if err != nil {
			t.Fatalf("as of %s: refused: %v", asOf, err)
		}
		raised := map[string]string{}
		for _, line := range lines {
			if line.Section == "signal" {
				raised[line.Code] = line.Text
			}
		}
		return raised
	}
	for _, c := range []struct {
		name, asOf, closeDate, code, sentence string
	}{
		{"a renewal 31 days away", "2027-01-08", "", "still_open", ""},
		{"a renewal 30 days away", "2027-01-09", "", "still_open",
			`The renewal is in 30 days and this opportunity is still open at stage "negotiation".`},
		{"a call 60 days old", "2027-03-13", "", "call_stale", ""},
		{"a call 61 days old", "2027-03-14", "", "call_stale", "The latest dated call that was read is 61 days old."},
		{"a close date on the renewal date", "2027-01-15", "2027-02-08", "close_after_renewal", ""},
		{"a close date the day after the renewal date", "2027-01-15", "2027-02-09", "close_after_renewal",
			"This open opportunity's close date, 2027-02-09, is after the renewal date, 2027-02-08."},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := signals(t, c.asOf, c.closeDate)[c.code]; got != c.sentence {
				t.Errorf("%s is %q, not %q", c.code, got, c.sentence)
			}
		})
	}
}
