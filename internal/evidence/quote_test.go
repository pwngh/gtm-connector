package evidence

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A bare mail address in quotation text is replaced as one in angle brackets is, since a GitHub Markdown
// client turns either into a mail link. SafeText, which neutralizes a record value, leaves it, and the text
// QuoteText gives stays as it is when neutralized again.
func TestABareMailAddressInAQuotationIsNeutralized(t *testing.T) {
	for _, c := range []struct {
		raw, quoted string
		links       int
	}{
		{"Mail dana@files.example today.", "Mail [link mail] today.", 1},
		{"Mail dana.crest+renewals@sub.files-01.example.", "Mail [link mail].", 1},
		{"Write to <dana@files.example> or dana@files.example.", "Write to [link mail] or [link mail].", 2},
		{"x:dana@files.example, y@files.example", "x:[link mail], [link mail]", 2},
		{"a@b.cd@e.fg", "[link mail]@e.fg", 1},
		{"Ask @dana, or dana@localhost, or dana@ files.example.", "Ask @dana, or dana@localhost, or dana@ files.example.", 0},
		// The marker is never the text of a link: a ( right after it is set apart.
		{"Write dana@files.example(follow-up).", "Write [link mail] (follow-up).", 1},
		// An address whose domain starts with www. is replaced whole, before the www. is read as a link.
		{"Dana (dana@www.files.example).", "Dana ([link mail]).", 1},
		{"Mail <dana@www.files.example> or mailto:dana@www.files.example.", "Mail [link mail] or [link mail].", 2},
		// A Markdown link whose destination is no link is a link to a client all the same, and is set apart.
		{"See [the order form](order-form) and [it] (here).", "See [the order form] (order-form) and [it] (here).", 0},
	} {
		got, counts, err := QuoteText(c.raw)
		if err != nil || got != c.quoted || counts.Links != c.links {
			t.Errorf("QuoteText(%q): got %q, %d links, %v; want %q, %d", c.raw, got, counts.Links, err, c.quoted, c.links)
		}
		if again, counts, err := QuoteText(got); err != nil || again != got || counts != (Counts{}) {
			t.Errorf("QuoteText(%q) a second time: got %q %+v %v", got, again, counts, err)
		}
	}
	if safe, _, err := SafeText("Mail dana@files.example today."); err != nil || safe != "Mail dana@files.example today." {
		t.Errorf("SafeText changed a bare address: %q %v", safe, err)
	}
}

// On a card, a call note that quotes a contact's address shows [link mail], and the contact's email, a record
// value, is shown as the CRM holds it.
func TestACardQuotesNoBareAddressAndKeepsTheContactsEmail(t *testing.T) {
	line := oracletest.Lines(t, "cards/demo.jsonl.gz")[0]
	var row struct{ Card string }
	if err := json.Unmarshal(line, &row); err != nil {
		t.Fatal(err)
	}
	value, err := strictjson.LoadsText(row.Card)
	if err != nil {
		t.Fatal(err)
	}
	evidence := member(value.(*strictjson.Object), "evidence").(*strictjson.Object)
	var email string
	for _, record := range member(evidence, "records").([]any) {
		record := record.(*strictjson.Object)
		if address, held := record.Get("email"); held {
			email = address.(string)
		}
		if ref, _ := record.Get("record_ref"); strings.HasPrefix(ref.(string), "calls/") {
			record.Set("body", "Send the timeline to "+email+" by Friday.")
		}
	}
	if email == "" {
		t.Fatal("the card's evidence holds no contact email")
	}
	_, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `; email "`+email+`" [`) ||
		!strings.Contains(text, `"Send the timeline to [link mail] by Friday."`) || strings.Count(text, "@") != 1 {
		t.Errorf("the card:\n%s", text)
	}
}

// Whatever it is given, a quotation holds no ] right before a (, which a Markdown client reads as a link and
// its destination, and no bare mail address, and it stays as it is when neutralized again.
func TestNoQuotationIsALinkOrABareAddress(t *testing.T) {
	pieces := []string{"[", "]", "(", ")", "@", ".", "a", "dana", "files", "example", "www.", "mailto:", "http://",
		"<", ">", " ", "\n", ":", "/", "!", "[link mail]", "x@y.zz", "&#64;", "\\", "|", "-", "_"}
	random := rand.New(rand.NewPCG(5, 2026))
	for range 20000 {
		var b strings.Builder
		for range 1 + random.IntN(14) {
			b.WriteString(pieces[random.IntN(len(pieces))])
		}
		raw := b.String()
		quoted, _, err := QuoteText(raw)
		if err != nil {
			t.Fatalf("QuoteText(%q): %v", raw, err)
		}
		var counts Counts
		if strings.Contains(quoted, "](") || string(bareMail([]rune(quoted), &counts, false)) != quoted {
			t.Fatalf("QuoteText(%q) = %q", raw, quoted)
		}
		if again, _, _ := QuoteText(quoted); again != quoted {
			t.Fatalf("QuoteText(%q) = %q, then %q", raw, quoted, again)
		}
	}
}

// A unit sealed under neutralizer version 4, before bare addresses were replaced, is shown with its address
// replaced, and one sealed under today's version is shown as sealed.
func TestAUnitOfVersion4ShowsNoBareAddress(t *testing.T) {
	sealed := "Send it to orders@example.test today."
	shown, err := AsShown(strictjson.NewObject("n", 3, "kind", "sentence", "speaker", "orders@example.test",
		"text", sealed, "sanitizer_version", 4))
	if err != nil || member(shown, "text") != "Send it to [link mail] today." || member(shown, "speaker") != "orders@example.test" {
		t.Errorf("a version 4 unit is shown as %v (%v)", shown, err)
	}
	today, err := AsShown(strictjson.NewObject("n", 3, "kind", "sentence", "text", sealed, "sanitizer_version", SanitizerVersion))
	if err != nil || member(today, "text") != sealed {
		t.Errorf("a unit of today's version is shown as %v (%v)", today, err)
	}
}
