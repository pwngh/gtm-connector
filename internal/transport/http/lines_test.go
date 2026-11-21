package httptransport_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A card answers with what its bytes say it is as of and its lines as data, each line exactly as
// evidence.Line.Value writes it; its text route answers with the sealed text whole, as plain text. Both
// refuse alike whoever may not read the card.
func TestACardServesItsLinesAndItsText(t *testing.T) {
	s := newSite(t, 0)
	reader := s.browser(t)
	reader.signIn("demo-reader", readerPassword)
	card := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil)
	lines, _ := card.body["lines"].([]any)
	if card.status != 200 || card.body["as_of"] != postgrestest.AsOf || len(lines) != len(postgrestest.FixtureLines()) {
		t.Fatalf("card: %d %s", card.status, card.raw)
	}
	if sources, present := card.body["sources"]; !present || sources != nil {
		t.Errorf("a card whose bytes name no sources: %v", sources)
	}
	for _, l := range postgrestest.FixtureLines() {
		want, err := strictjson.Dumps(l.Value(), strictjson.Prepared)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(card.raw), want) {
			t.Errorf("the card does not hold the line %s:\n%s", want, card.raw)
		}
	}

	document, err := strictjson.Loads(s.Card.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := document.(*strictjson.Object).Get("text")
	text := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/text", nil, nil)
	if text.status != 200 || string(text.raw) != sealed.(string) ||
		text.header.Get("Content-Type") != "text/plain; charset=utf-8" || text.header.Get("Cache-Control") != "no-store" ||
		text.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("text: %d %v %q", text.status, text.header, text.raw)
	}

	if r := s.browser(t).do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/text", nil, nil); r.status != 401 {
		t.Errorf("anonymous text: %d", r.status)
	}
	outsider := s.browser(t)
	outsider.signIn("demo-outsider", outsiderPassword)
	refused := outsider.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil)
	for _, path := range []string{"/api/v1/cards/" + s.Card.ID + "/text",
		"/api/v1/cards/00000000-0000-0000-0000-000000000000/text"} {
		r := outsider.do(http.MethodGet, path, nil, nil)
		if r.status != 403 || r.code() != "not_permitted" || string(r.raw) != string(refused.raw) {
			t.Errorf("outsider %s: %d %s, the card's refusal %d %s", path, r.status, r.raw, refused.status, refused.raw)
		}
	}
}

// A card published before lines were projected answers with lines null, not an empty list, and its text.
func TestACardFromBeforeLinesAnswersWithNoLines(t *testing.T) {
	s := siteOn(t, postgrestest.BeforeLines(t), 0)
	reader := s.browser(t)
	reader.signIn("demo-reader", readerPassword)
	card := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil)
	if lines, present := card.body["lines"]; card.status != 200 || !present || lines != nil ||
		!strings.Contains(string(card.raw), `"lines":null`) {
		t.Fatalf("card: %d %s", card.status, card.raw)
	}
	if text := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/text", nil, nil); text.status != 200 ||
		!strings.HasPrefix(string(text.raw), "DEAL CARD ") {
		t.Fatalf("text: %d %q", text.status, text.raw)
	}
}
