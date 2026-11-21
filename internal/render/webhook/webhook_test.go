package webhook_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/render"
	"github.com/pwngh/gtm-connector/internal/render/rendertest"
	"github.com/pwngh/gtm-connector/internal/render/webhook"
)

var occurred = time.Date(2027, 1, 15, 15, 0, 5, 0, time.UTC)

// secret is an invented signing secret of 32 bytes.
var secret = []byte("test-secret-0123456789-abcdefghi")

// goldens are the three events, each on one of the golden cards.
func goldens(t *testing.T) map[string]struct {
	delivery webhook.Delivery
	event    render.Event
} {
	events := rendertest.Events(t)
	return map[string]struct {
		delivery webhook.Delivery
		event    render.Event
	}{
		"risk_changed": {webhook.Delivery{ID: "evt_01", Type: webhook.RiskChanged, OccurredAt: occurred}, events["at_risk"]},
		"card_changed": {webhook.Delivery{ID: "evt_02", Type: webhook.CardChanged, OccurredAt: occurred},
			events["changed_new_content"]},
		"deal_closed": {webhook.Delivery{ID: "evt_03", Type: webhook.DealClosed, OccurredAt: occurred}, events["closed_won"]},
	}
}

// The three events are their golden files byte for byte, the same event gives the same bytes, and the
// signature of the risk event at its time under the invented secret is its golden header.
func TestGoldenBodies(t *testing.T) {
	for name, g := range goldens(t) {
		got, err := webhook.Body(g.delivery, g.event, rendertest.Site)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again, _ := webhook.Body(g.delivery, g.event, rendertest.Site); !bytes.Equal(got, again) {
			t.Errorf("%s: two runs differ", name)
		}
		rendertest.Golden(t, name+".json", got)
		if name == "risk_changed" {
			header, err := webhook.Sign(secret, occurred, got)
			if err != nil {
				t.Fatal(err)
			}
			rendertest.Golden(t, "risk_changed.signature", []byte(webhook.Header+": "+header+"\n"))
		}
	}
}

// A receiver's check takes the body it was sent with the header it was sent, and refuses a body changed by
// one byte, another secret, a timestamp outside the tolerance either way, and a header of another form.
func TestTheReceiversCheckRefusesWhatWasNotSigned(t *testing.T) {
	g := goldens(t)["risk_changed"]
	body, err := webhook.Body(g.delivery, g.event, rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	header, err := webhook.Sign(secret, occurred, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := webhook.Verify(secret, header, body, occurred.Add(time.Minute), webhook.Tolerance); err != nil {
		t.Fatalf("the signed body: %v", err)
	}
	changed := slices.Clone(body)
	changed[len(changed)/2] ^= 1
	other := slices.Clone(secret)
	other[0] ^= 1
	header2, _ := webhook.Sign(secret, occurred.Add(time.Second), body)
	for name, c := range map[string]struct {
		secret []byte
		header string
		body   []byte
		now    time.Time
		want   error
	}{
		"a body changed by one byte":        {secret, header, changed, occurred, webhook.ErrSignature},
		"another secret":                    {other, header, body, occurred, webhook.ErrSignature},
		"a timestamp too old":               {secret, header, body, occurred.Add(webhook.Tolerance + time.Second), webhook.ErrStale},
		"a timestamp too far ahead":         {secret, header, body, occurred.Add(-webhook.Tolerance - time.Second), webhook.ErrStale},
		"another timestamp, same signature": {secret, header2[:13] + header[13:], body, occurred, webhook.ErrSignature},
		"no timestamp":                      {secret, header[13:], body, occurred, webhook.ErrMalformed},
		"a second signature":                {secret, header + ",v1=00", body, occurred, webhook.ErrMalformed},
		"a short signature":                 {secret, header[:len(header)-2], body, occurred, webhook.ErrMalformed},
	} {
		if err := webhook.Verify(c.secret, c.header, c.body, c.now, webhook.Tolerance); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := webhook.Sign(secret[:31], occurred, body); !errors.Is(err, webhook.ErrSecret) {
		t.Errorf("a short secret: %v", err)
	}
}

// An event that does not fit its type gives no body.
func TestAnEventOfTheWrongTypeIsRefused(t *testing.T) {
	g := goldens(t)
	for name, c := range map[string]struct {
		delivery webhook.Delivery
		event    render.Event
	}{
		"closed on an open deal":       {g["deal_closed"].delivery, g["risk_changed"].event},
		"card changed with no card":    {g["card_changed"].delivery, g["risk_changed"].event},
		"risk changed on a closed one": {g["risk_changed"].delivery, g["deal_closed"].event},
		"another type":                 {webhook.Delivery{ID: "evt_04", Type: "deal.opened", OccurredAt: occurred}, g["risk_changed"].event},
		"no ID":                        {webhook.Delivery{Type: webhook.RiskChanged, OccurredAt: occurred}, g["risk_changed"].event},
	} {
		if _, err := webhook.Body(c.delivery, c.event, rendertest.Site); !errors.Is(err, render.ErrEvent) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// strings is every string value of a JSON document.
func stringsOf(t *testing.T, raw []byte) []string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(value)
	return out
}

// For every recorded card, a body of each type built from it holds no string of its units (no sentence,
// speaker or record reference), not the account's name and no contact's name, title or email.
func TestNoRecordedCardLeaksIntoABody(t *testing.T) {
	cards := 0
	for _, family := range rendertest.Families {
		for _, r := range rendertest.Cards(t, family) {
			cards++
			forbidden := append(slices.Clone(r.Sentences), r.Contacts...)
			if render.Distinct(r.Name) {
				forbidden = append(forbidden, r.Name)
			}
			for _, u := range r.View.Units {
				var b struct{ Speaker string }
				_ = json.Unmarshal(u.Body, &b)
				forbidden = append(forbidden, u.RecordRef)
				if render.Distinct(b.Speaker) {
					forbidden = append(forbidden, b.Speaker)
				}
			}
			before := r.View
			before.Units, before.Lines = nil, []evidence.Line{}
			before.Card.PublishedAt = r.View.Card.PublishedAt.Add(-time.Hour)
			deal := render.Deal{ID: "deal-1", AccountName: r.Name, Status: render.AtRisk, Previous: render.OnTrack}
			raised := []render.Rule{{Code: "close_date_slippage", State: render.Raised, Previous: render.Clear,
				Sentence: "The close date moved 2 times in 30 days.", Threshold: "2 moves", Cites: []int{1}}}
			for _, c := range []struct {
				d webhook.Delivery
				e render.Event
			}{
				{webhook.Delivery{ID: "evt_1", Type: webhook.RiskChanged, OccurredAt: occurred},
					render.Event{Deal: deal, Card: r.View, Rules: raised}},
				{webhook.Delivery{ID: "evt_2", Type: webhook.CardChanged, OccurredAt: occurred},
					render.Event{Deal: deal, Card: r.View, Previous: &before, Rules: raised}},
				{webhook.Delivery{ID: "evt_3", Type: webhook.DealClosed, OccurredAt: occurred},
					render.Event{Deal: render.Deal{ID: "deal-1", AccountName: r.Name, Status: render.ClosedLost}, Card: r.View}},
			} {
				got, err := webhook.Body(c.d, c.e, rendertest.Site)
				if err != nil {
					t.Fatalf("%s %s %s: %v", family, r.Key, c.d.Type, err)
				}
				for _, s := range stringsOf(t, got) {
					for _, f := range forbidden {
						if render.Holds(s, f) {
							t.Errorf("%s %s %s: %q holds %q", family, r.Key, c.d.Type, s, f)
						}
					}
				}
			}
		}
	}
	if cards != 450 {
		t.Errorf("%d recorded cards, want 450", cards)
	}
}
