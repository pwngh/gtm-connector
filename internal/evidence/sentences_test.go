package evidence

import (
	"reflect"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// slackCases are Slack message bodies and the sentences each gives. A single line break is a wrap, unless the
// line before it ends a sentence or the next line starts a list item; a blank line ends a sentence.
var slackCases = []struct {
	body string
	want []string
}{
	// A line that stops mid-sentence goes on, so no fragment is a quotation.
	{"We can renew if\nSecurity signs off.", []string{"We can renew if Security signs off."}},
	{"GC will not sign without ZDR\nProcurement asked for $300k", []string{"GC will not sign without ZDR Procurement asked for $300k"}},
	{"We met Dr.\nExample today.", []string{"We met Dr. Example today."}},
	// A line that ends in closing punctuation ends its sentence, as one thought a line.
	{"Thanks.\nWe can renew if Security signs off.", []string{"Thanks.", "We can renew if Security signs off."}},
	{"Will legal sign?\nThe rider is ready!\nSend it Friday.",
		[]string{"Will legal sign?", "The rider is ready!", "Send it Friday."}},
	// Each list item starts a sentence, whether Markdown or a bullet Slack writes.
	{"We need:\n- SSO\n- audit logs", []string{"We need:", "- SSO", "- audit logs"}},
	{"Blockers\n1. legal review\n2) the price", []string{"Blockers", "1. legal review", "2) the price"}},
	{"Asks\n• SSO\n  ◦ with SCIM\n▪ audit logs", []string{"Asks", "• SSO", "◦ with SCIM", "▪ audit logs"}},
	{"- renew if\nthe rider holds", []string{"- renew if the rider holds"}},
	// A blank line ends a sentence.
	{"We can renew if\n\nSecurity signs off.", []string{"We can renew if", "Security signs off."}},
	// A dash or a number that is no list marker is a wrap.
	{"Seats go to\n-5 from 40", []string{"Seats go to -5 from 40"}},
	{"Renewal is\n*not* blocked", []string{"Renewal is *not* blocked"}},
}

func TestSentencesInASlackMessage(t *testing.T) {
	for _, c := range slackCases {
		if got, err := Sentences(c.body, true); err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Sentences(%q, lines): got %q %v, want %q", c.body, got, err, c.want)
		}
	}
	// In a call note or a cue, a line break before a list item is a wrap too.
	if got, _ := Sentences("We need:\n- SSO", false); !reflect.DeepEqual(got, []string{"We need: - SSO"}) {
		t.Errorf("Sentences without lines: got %q", got)
	}
}

// On a card, each Slack sentence unit is one of the sentences its message gives.
func TestASlackLineBreakOnTheCard(t *testing.T) {
	var vector *strictjson.Object
	for _, line := range oracletest.Lines(t, "cards/evidence.jsonl.gz") {
		value, err := strictjson.Loads(line)
		if err != nil {
			t.Fatal(err)
		}
		o := value.(*strictjson.Object)
		if test, _ := o.Get("test"); test == "test_units.Evidence.test_a_line_break_ends_a_sentence_in_a_chat_message_and_wraps_one_in_a_note" {
			vector = o
			break
		}
	}
	if vector == nil {
		t.Fatal("no vector with a Slack message")
	}
	for _, c := range slackCases {
		evidence := memberObject(strictjson.Clone(vector), "evidence")
		for _, record := range member(evidence, "records").([]any) {
			if member(record, "record_ref") == "slack/messages/msg-0" {
				record.(*strictjson.Object).Set("body", c.body)
			}
		}
		units, _, _, _, err := Build(evidence)
		if err != nil {
			t.Fatalf("%q: %v", c.body, err)
		}
		var got []string
		for _, u := range units {
			if u.Kind == "sentence" && u.Source == "slack" {
				got = append(got, u.Text)
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: the card's Slack sentences are %q, want %q", c.body, got, c.want)
		}
	}
}

// A unit sealed under neutralizer version 3 is shown under today's rules, and the unit given is not changed.
// The text is that of a sentence unit a recorded card sealed under version 3, which left a mail address in
// angle brackets as written.
func TestASavedUnitIsShownUnderTodaysRules(t *testing.T) {
	const sealed = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) <jovan.mercer@quarrystream.example>"
	older := strictjson.NewObject("n", 8, "kind", "sentence", "speaker", "<@U0ABC123>", "text", sealed,
		"sanitizer_version", 3)
	shown, err := AsShown(older)
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := shown.Get("text"); text != "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) [link mail]" {
		t.Errorf("shown as %q", text)
	}
	if speaker, _ := shown.Get("speaker"); speaker != "[user U0ABC123]" {
		t.Errorf("speaker shown as %q", speaker)
	}
	if text, _ := older.Get("text"); text != sealed {
		t.Error("the saved unit was changed")
	}
	if version, _ := shown.Get("sanitizer_version"); !Equal(version, 3) {
		t.Errorf("shown with version %v; the version is the one it was sealed under", version)
	}
	today := strictjson.NewObject("n", 1, "kind", "sentence", "text", "Signed.", "sanitizer_version", SanitizerVersion)
	if shown, err := AsShown(today); err != nil || shown != today {
		t.Errorf("a unit sealed under today's rules is not shown as sealed: %v", err)
	}
}
