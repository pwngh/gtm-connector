package slack_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/render"
	"github.com/pwngh/gtm-connector/internal/render/rendertest"
	"github.com/pwngh/gtm-connector/internal/render/slack"
)

// texts is every text object of a message, with the kind of object it is in.
func texts(t *testing.T, raw []byte) (notification string, objects []slack.Text, blocks []slack.Block) {
	t.Helper()
	var m slack.Message
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	for _, b := range m.Blocks {
		if b.Text != nil {
			objects = append(objects, *b.Text)
		}
		objects = append(objects, b.Fields...)
		objects = append(objects, b.Elements...)
	}
	return m.Text, objects, m.Blocks
}

// withinLimits holds a decoded message to Slack's limits, read again from its JSON.
func withinLimits(t *testing.T, name string, raw []byte) {
	t.Helper()
	notification, objects, blocks := texts(t, raw)
	if len(blocks) == 0 || len(blocks) > slack.MaxBlocks || slack.Length(notification) > slack.MaxMessageText {
		t.Errorf("%s: %d blocks", name, len(blocks))
	}
	for _, b := range blocks {
		if b.Type == "header" && slack.Length(b.Text.Text) > slack.MaxHeader || len(b.Fields) > slack.MaxFields ||
			len(b.Elements) > slack.MaxElements {
			t.Errorf("%s: a block over its limit: %+v", name, b)
		}
		for _, f := range b.Fields {
			if n := slack.Length(f.Text); n < 1 || n > slack.MaxField {
				t.Errorf("%s: a field of %d characters", name, n)
			}
		}
	}
	for _, o := range objects {
		if n := slack.Length(o.Text); n < 1 || n > slack.MaxText {
			t.Errorf("%s: a text object of %d characters", name, n)
		}
		if o.Type == "mrkdwn" && (o.Verbatim == nil || !*o.Verbatim) || o.Type == "plain_text" && (o.Emoji == nil || *o.Emoji) {
			t.Errorf("%s: %+v is formatted by Slack", name, o)
		}
	}
}

// The five alerts the plan lists, and the summary of the at-risk card, are their golden files byte for
// byte, and the same event gives the same bytes on every run.
func TestGoldenMessages(t *testing.T) {
	events := rendertest.Events(t)
	for name, e := range events {
		got, err := slack.Alert(e, rendertest.Site)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, _ := slack.Alert(e, rendertest.Site)
		if !bytes.Equal(got, again) {
			t.Errorf("%s: two runs differ", name)
		}
		withinLimits(t, name, got)
		rendertest.Golden(t, "alert_"+name+".json", got)
	}
	got, err := slack.Summarize(events["at_risk"], rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	rendertest.Golden(t, "summary_at_risk.json", got)
}

// The at-risk alert's notification names the status, the deal, the countdown, the raised rules and the
// count of unknown ones, and its header says the status first.
func TestTheNotificationSaysTheStatusFirst(t *testing.T) {
	got, err := slack.Alert(rendertest.Events(t)["at_risk"], rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	notification, _, blocks := texts(t, got)
	want := "At risk: acc-003. Renewal in 24 days. Legal or security blocker, Close-date slippage. 1 rule unknown."
	if notification != want || blocks[0].Text.Text != "At risk: acc-003" {
		t.Errorf("notification %q, header %q", notification, blocks[0].Text.Text)
	}
	if strings.Contains(string(got), "450") || strings.Contains(string(got), "USD") {
		t.Errorf("an alert carries an amount:\n%s", got)
	}
}

// An account name full of Slack's markup is carried once, unchanged, in a plain_text object of its own with
// emoji off, and in no mrkdwn object and not in the notification.
func TestAHostileAccountNameIsInert(t *testing.T) {
	hostile := "*bold* _x_ `code` ~gone~ :smile: <!channel> <http://a.example|b> & co"
	for name, e := range rendertest.Events(t) {
		e.Deal.AccountName = hostile
		for kind, build := range map[string]func(render.Event, render.Site) ([]byte, error){
			"alert": slack.Alert, "summary": slack.Summarize} {
			got, err := build(e, rendertest.Site)
			if err != nil {
				t.Fatalf("%s %s: %v", name, kind, err)
			}
			notification, objects, _ := texts(t, got)
			plain := 0
			for _, o := range objects {
				if o.Text == hostile && o.Type == "plain_text" && !*o.Emoji {
					plain++
				}
				for _, part := range []string{"*bold*", "_x_", "`code`", ":smile:", "<!channel>", "a.example", "& co"} {
					if o.Type == "mrkdwn" && strings.Contains(o.Text, part) {
						t.Errorf("%s %s: %q in mrkdwn: %q", name, kind, part, o.Text)
					}
				}
			}
			if plain != 1 || strings.Contains(notification, "bold") {
				t.Errorf("%s %s: the name is in %d plain_text objects; notification %q", name, kind, plain, notification)
			}
		}
	}
}

// A name longer than a section allows is left out and said to be; an empty one is left out.
func TestANameTooLongIsLeftOutAndSaidToBe(t *testing.T) {
	e := rendertest.Events(t)["at_risk"]
	e.Deal.AccountName = strings.Repeat("&", slack.MaxText/5+1)
	got, err := slack.Alert(e, rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "&&") || !strings.Contains(string(got), "The account name is too long to show here.") {
		t.Errorf("a long name:\n%s", got)
	}
	e.Deal.AccountName = " \t"
	got, err = slack.Alert(e, rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	if _, objects, _ := texts(t, got); slices.ContainsFunc(objects, func(o slack.Text) bool { return o.Text == " \t" }) {
		t.Errorf("an empty name was sent:\n%s", got)
	}
}

// A shared unit adds the one line that says so, and names no other deal.
func TestSharedEvidenceIsSaidOnce(t *testing.T) {
	e := rendertest.Events(t)["at_risk"]
	e.Deal.Shared = true
	got, err := slack.Alert(e, rendertest.Site)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "Some evidence on this card is also on another deal of this account.") != 1 {
		t.Errorf("shared:\n%s", got)
	}
}

// An event that does not hold together gives no message.
func TestAnEventThatDoesNotHoldTogetherIsRefused(t *testing.T) {
	events := rendertest.Events(t)
	for want, change := range map[string]func(e *render.Event, s *render.Site){
		"an at-risk deal has no raised rule": func(e *render.Event, _ *render.Site) { e.Rules = nil },
		"a deal with a raised rule is not at risk": func(e *render.Event, _ *render.Site) {
			e.Deal.Status = render.Changed
		},
		"the sentence of pricing_seat_conflict holds a figure": func(e *render.Event, _ *render.Site) {
			e.Rules[1] = render.Rule{Code: "pricing_seat_conflict", State: render.Raised,
				Sentence: "Slack says 40 seats and the CRM says 35.", Cites: []int{5}}
		},
		"the sentence of legal_security_blocker holds a string of the card's sources": func(e *render.Event, _ *render.Site) {
			e.Rules[0].Sentence = "Marked: We will have the order form countersigned by January 30."
		},
		"the sentence of legal_security_blocker is not the connector's words": func(e *render.Event, _ *render.Site) {
			e.Rules[0].Sentence = "Marked *now*."
		},
		"legal_security_blocker cites unit 99, which the card does not hold": func(e *render.Event, _ *render.Site) {
			e.Rules[0].Cites = []int{99}
		},
		"close_date_slippage is unknown, and every source it needs was read": func(e *render.Event, _ *render.Site) {
			e.Rules[3] = render.Rule{Code: "close_date_slippage", State: render.Unknown}
		},
		"a deal with an unknown rule or an unread source is not on track": func(e *render.Event, _ *render.Site) {
			e.Deal.Status = render.OnTrack
			e.Rules[0], e.Rules[3] = render.Rule{Code: e.Rules[0].Code, State: render.Clear},
				render.Rule{Code: e.Rules[3].Code, State: render.Clear}
		},
		"the deal ID is not": func(e *render.Event, _ *render.Site) { e.Deal.ID = "acc-003|x" },
		"the public address is not an http or https origin": func(_ *render.Event, s *render.Site) {
			s.Address = "https://gtm.example.internal/deals"
		},
		"legal_security_blocker is given twice": func(e *render.Event, _ *render.Site) {
			e.Rules = append(e.Rules, e.Rules[0])
		},
	} {
		e, site := events["at_risk"], rendertest.Site
		e.Rules = slices.Clone(e.Rules)
		change(&e, &site)
		if _, err := slack.Alert(e, site); !errors.Is(err, render.ErrEvent) || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

// For every golden card, every message built from it is within Slack's limits, gives the same bytes
// twice, and holds none of its sentences and none of its contacts' names, titles or emails, in any string.
func TestNoRecordedCardLeaksASentenceOrAContact(t *testing.T) {
	cards := 0
	for _, family := range rendertest.Families {
		for _, r := range rendertest.Cards(t, family) {
			cards++
			for name, e := range eventsOn(r) {
				build := slack.Alert
				if strings.HasPrefix(name, "summary") {
					build = slack.Summarize
				}
				got, err := build(e, rendertest.Site)
				if err != nil {
					t.Fatalf("%s %s %s: %v", family, r.Key, name, err)
				}
				if again, _ := build(e, rendertest.Site); !bytes.Equal(got, again) {
					t.Errorf("%s %s %s: two runs differ", family, r.Key, name)
				}
				withinLimits(t, family+" "+r.Key+" "+name, got)
				notification, objects, _ := texts(t, got)
				for _, o := range append(objects, slack.Text{Type: "notification", Text: notification}) {
					if o.Type == "plain_text" && o.Text == r.Name {
						continue
					}
					for _, s := range append(slices.Clone(r.Sentences), r.Contacts...) {
						if render.Holds(o.Text, s) {
							t.Errorf("%s %s %s: %q holds %q", family, r.Key, name, o.Text, s)
						}
					}
				}
			}
		}
	}
	if cards != 90 {
		t.Errorf("%d golden cards, want 90", cards)
	}
}

// eventsOn is one event of each message on a recorded card: at risk, changed against a card with none of
// its units and lines, on track where every source was read, closed lost, and the at-risk summary.
func eventsOn(r rendertest.Recorded) map[string]render.Event {
	var unknown []render.Rule
	for _, g := range r.View.Gaps {
		if strings.HasPrefix(g.Code, "slack_not_") || strings.HasSuffix(g.Code, "_denied") {
			unknown = []render.Rule{{Code: "customer_silence", State: render.Unknown}}
		}
	}
	raised := append([]render.Rule{{Code: "close_date_slippage", State: render.Raised,
		Sentence: "The close date moved 2 times in 30 days.", Threshold: "2 moves", Cites: []int{1}}}, unknown...)
	deal := render.Deal{ID: "deal-1", AccountName: r.Name, Status: render.AtRisk, Shared: true}
	before := r.View
	before.Units, before.Lines = nil, []evidence.Line{}
	before.Card.PublishedAt = r.View.Card.PublishedAt.Add(-time.Hour)
	out := map[string]render.Event{
		"at_risk":     {Deal: deal, Card: r.View, Rules: raised},
		"summary":     {Deal: deal, Card: r.View, Rules: raised},
		"changed":     {Deal: with(deal, render.Changed), Card: r.View, Previous: &before, Rules: unknown},
		"closed_lost": {Deal: with(deal, render.ClosedLost), Card: r.View},
	}
	if unknown == nil {
		out["on_track"] = render.Event{Deal: with(deal, render.OnTrack), Card: r.View,
			Rules: []render.Rule{{Code: "close_date_slippage", State: render.Clear, Previous: render.Raised}}}
	}
	return out
}

func with(d render.Deal, s render.Status) render.Deal { d.Status = s; return d }
