package rendertest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/render"
)

// Golden holds got to the file testdata/NAME of the test's package. With -update it writes the file
// instead, in a run of the whole package.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if oracletest.Update() {
		oracletest.RefuseNarrowRun(t)
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run the package's tests with -update to write it", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from what is built now:\n%s", path, got)
	}
}

// Site is the invented server and revision every golden file links to.
var Site = render.Site{Address: "https://gtm.example.internal", Revision: "0123456789abcdef0123456789abcdef01234567"}

// Before is when the card before a golden event's card was published.
var Before = time.Date(2027, 1, 15, 9, 10, 0, 0, time.UTC)

func rules(states map[string][2]render.State) []render.Rule {
	var out []render.Rule
	for _, code := range render.RuleCodes {
		s := states[code]
		if s[0] == "" {
			s = [2]render.State{render.Clear, render.Clear}
		}
		out = append(out, render.Rule{Code: code, State: s[0], Previous: s[1]})
	}
	return out
}

// earlier is a recorded card as the card before, published at Before.
func earlier(t testing.TB, key string) *Recorded {
	r := Card(t, "demo", key)
	r.View.Card.PublishedAt = Before
	return &r
}

// Events are the golden events, each on the README's account (acc-003) in the demo book, by name:
//   - at_risk: two raised rules, a blocker and close-date slippage, and customer silence unknown, on the card
//     whose grant lacks slack:read;
//   - changed_new_content: the card with calls read, after the card without them;
//   - changed_unread_source: the card whose grant lacks slack:read, after the card that read Slack;
//   - on_track: close-date slippage no longer holds and every source was read;
//   - closed_won: the final card.
func Events(t testing.TB) map[string]render.Event {
	t.Helper()
	full := Card(t, "demo", "acc-003")
	denied := Card(t, "demo", "acc-003 slack not granted")
	deal := func(status, previous render.Status) render.Deal {
		return render.Deal{ID: "acc-003", AccountName: full.Name, Status: status, Previous: previous}
	}
	atRisk := rules(map[string][2]render.State{"customer_silence": {render.Unknown, render.Unknown}})
	for i, r := range atRisk {
		switch r.Code {
		case "legal_security_blocker":
			atRisk[i] = render.Rule{Code: r.Code, State: render.Raised, Previous: render.Clear,
				Sentence: "Marked as a blocker on 2027-01-14. 1 sentence cited.", Cites: []int{5}}
		case "close_date_slippage":
			atRisk[i] = render.Rule{Code: r.Code, State: render.Raised, Previous: render.Clear,
				Sentence:  "The close date moved 2 times in 30 days, last from 2027-01-25 to 2027-02-01.",
				Threshold: "2 moves", Cites: []int{3}}
		}
	}
	withoutCalls := earlier(t, "acc-003 without calls")
	slackRead := earlier(t, "acc-003")
	return map[string]render.Event{
		"at_risk": {Deal: deal(render.AtRisk, render.Changed), Card: denied.View, Rules: atRisk},
		"changed_new_content": {Deal: deal(render.Changed, render.OnTrack), Card: full.View,
			Previous: &withoutCalls.View,
			Rules:    rules(map[string][2]render.State{"customer_silence": {render.Clear, render.Unknown}})},
		"changed_unread_source": {Deal: deal(render.Changed, render.OnTrack), Card: denied.View,
			Previous: &slackRead.View,
			Rules:    rules(map[string][2]render.State{"customer_silence": {render.Unknown, render.Clear}})},
		"on_track": {Deal: deal(render.OnTrack, render.AtRisk), Card: full.View,
			Rules: rules(map[string][2]render.State{"close_date_slippage": {render.Clear, render.Raised}})},
		"closed_won": {Deal: deal(render.ClosedWon, render.OnTrack), Card: full.View, Rules: rules(nil)},
	}
}
