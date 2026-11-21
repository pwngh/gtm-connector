package evidence

import (
	"encoding/json"
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
