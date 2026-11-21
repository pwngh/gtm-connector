package evidence

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// slackCases are Slack message bodies, the sentences each gives, which of them are in a block quote, and
// which are fragments: without their end, with more of the message after them. A single line break is a
// wrap, unless the line before it ends a sentence or the next line starts a block: a list item, a block
// quote, a code fence or a speaker label. A blank line ends a sentence.
var slackCases = []struct {
	body          string
	want          []string
	quoted, parts []int
}{
	// A line that stops mid-sentence goes on, so no fragment is a quotation. The end of a message is an end.
	{"We can renew if\nSecurity signs off.", []string{"We can renew if Security signs off."}, nil, nil},
	{"GC will not sign without ZDR\nProcurement asked for $300k", []string{"GC will not sign without ZDR Procurement asked for $300k"}, nil, nil},
	{"We met Dr.\nExample today.", []string{"We met Dr. Example today."}, nil, nil},
	{"Note from Evelyn at 09:40 UTC\nabout the SLA.", []string{"Note from Evelyn at 09:40 UTC about the SLA."}, nil, nil},
	// A line that ends in closing punctuation ends its sentence, as one thought a line.
	{"Thanks.\nWe can renew if Security signs off.", []string{"Thanks.", "We can renew if Security signs off."}, nil, nil},
	{"Will legal sign?\nThe rider is ready!\nSend it Friday.",
		[]string{"Will legal sign?", "The rider is ready!", "Send it Friday."}, nil, nil},
	// Each list item starts a sentence, whether Markdown or a bullet Slack writes, after any spaces and
	// before any space.
	{"We need:\n- SSO\n- audit logs", []string{"We need:", "- SSO", "- audit logs"}, nil, []int{0, 1}},
	{"Blockers\n1. legal review\n2) the price", []string{"Blockers", "1. legal review", "2) the price"}, nil, []int{0, 1}},
	{"Asks\n• SSO\n  ◦ with SCIM\n▪ audit logs", []string{"Asks", "• SSO", "◦ with SCIM", "▪ audit logs"}, nil, []int{0, 1, 2}},
	{"Blockers\n    ◦ legal review\n    ◦ pricing", []string{"Blockers", "◦ legal review", "◦ pricing"}, nil, []int{0, 1}},
	{"Asks\n• SSO\n- audit logs", []string{"Asks", "• SSO", "- audit logs"}, nil, []int{0, 1}},
	{"- renew if\nthe rider holds", []string{"- renew if the rider holds"}, nil, nil},
	{"We can renew if\n- Security signs off.", []string{"We can renew if", "- Security signs off."}, nil, []int{0}},
	// A blank line ends a sentence.
	{"We can renew if\n\nSecurity signs off.", []string{"We can renew if", "Security signs off."}, nil, []int{0}},
	// A block quote is a block of its own, marked quoted, and so is a code block.
	{"My update\n> Legal approved the renewal\nI still need confirmation.",
		[]string{"My update", "> Legal approved the renewal", "I still need confirmation."}, []int{1}, []int{0, 1}},
	{"&gt; Legal approved the renewal.\n&gt; Pricing holds.\nGood news.",
		[]string{"&gt; Legal approved the renewal.", "&gt; Pricing holds.", "Good news."}, []int{0, 1}, nil},
	{"Seat math I just ran\n```\nseats = 116\n```\nPlease check it.",
		[]string{"Seat math I just ran", "``` seats = 116 ```", "Please check it."}, nil, []int{0, 1}},
	{"Run this:\n```make test```\nthen tell me.", []string{"Run this:", "```make test```", "then tell me."}, nil, []int{0, 1}},
	// A line that starts with a speaker label starts a block.
	{"Pasted from the call\nDana: we can sign\nMorgan Reed: then send it",
		[]string{"Pasted from the call", "Dana: we can sign", "Morgan Reed: then send it"}, []int{1, 2}, []int{0, 1}},
	// A dash or a number that is no list marker is a wrap.
	{"Seats go to\n-5 from 40", []string{"Seats go to -5 from 40"}, nil, nil},
	{"Renewal is\n*not* blocked", []string{"Renewal is *not* blocked"}, nil, nil},
}

func TestSentencesInASlackMessage(t *testing.T) {
	for _, c := range slackCases {
		got, quoted, err := sentences(c.body, true)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Sentences(%q, lines): got %q %v, want %q", c.body, got, err, c.want)
			continue
		}
		for i := range got {
			if quoted[i] != slices.Contains(c.quoted, i) {
				t.Errorf("%q: sentence %d quoted %v", c.body, i, quoted[i])
			}
		}
	}
	// In a call note or a cue, a line break before a list item is a wrap too.
	if got, _ := Sentences("We need:\n- SSO", false); !reflect.DeepEqual(got, []string{"We need: - SSO"}) {
		t.Errorf("Sentences without lines: got %q", got)
	}
}

// built is the card of a recorded evidence vector with one call note and one Slack message by a listed
// seller, with these bodies: its units from source, and its text.
func built(t *testing.T, note, message string) (map[string][]Unit, string) {
	t.Helper()
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
	evidence := memberObject(strictjson.Clone(vector), "evidence")
	for _, record := range member(evidence, "records").([]any) {
		switch member(record, "record_ref") {
		case "slack/messages/msg-0":
			record.(*strictjson.Object).Set("body", message)
		case "calls/call_notes/note-0":
			record.(*strictjson.Object).Set("body", note)
		}
	}
	units, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatalf("%q: %v", message, err)
	}
	bySource := map[string][]Unit{}
	for _, u := range units {
		if u.Kind == "sentence" {
			bySource[u.Source] = append(bySource[u.Source], u)
		}
	}
	return bySource, text
}

// On a card, each Slack sentence unit is one of the sentences its message gives. A fragment is partial and
// never quoted, and a sentence in a block quote is marked quoted, with no role established for it.
func TestASlackLineBreakOnTheCard(t *testing.T) {
	for _, c := range slackCases {
		units, _ := built(t, "We will see.", c.body)
		var got []string
		for i, u := range units["slack"] {
			got = append(got, u.Text)
			fragment, quoted := slices.Contains(c.parts, i), slices.Contains(c.quoted, i)
			if u.Partial != fragment || fragment && u.Shown != "cut" || !fragment && u.Shown == "cut" {
				t.Errorf("%q: sentence %d is partial %v and shown %q", c.body, i, u.Partial, u.Shown)
			}
			if u.Quoted != quoted || quoted && (u.Role != "unknown" || u.Basis != QuotedBasis || u.Contact != nil) {
				t.Errorf("%q: sentence %d is quoted %v, with role %q (%s)", c.body, i, u.Quoted, u.Role, u.Basis)
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: the card's Slack sentences are %q, want %q", c.body, got, c.want)
		}
	}
}

// In a call note too, a sentence without its end that more text follows is partial and not quoted; the
// note's last sentence is whole even without closing punctuation.
func TestAFragmentOfANoteIsNeverQuoted(t *testing.T) {
	units, text := built(t, "We can renew if\n\nSecurity signs off\n\nthe rider holds", "Signed.")
	notes := units["calls"]
	if len(notes) != 3 || !notes[0].Partial || notes[0].Shown != "cut" || !notes[1].Partial ||
		notes[2].Partial || notes[2].Text != "the rider holds" {
		t.Fatalf("the note's units: %+v", notes)
	}
	if strings.Contains(text, `"We can renew if"`) || strings.Contains(text, `"Security signs off"`) {
		t.Errorf("a fragment is quoted on the card:\n%s", text)
	}
}

// A seller who pastes a customer's words after a speaker label relays them: the turn is quoted, under no
// established role, with the label kept in the text, and never the seller's own claim.
func TestASpeakersTurnIsNotTheAuthorsClaim(t *testing.T) {
	units, text := built(t, "We will see.", "From the call today.\nDana Ito: we will sign by Friday.")
	slack := units["slack"]
	if len(slack) != 2 || slack[0].Quoted || slack[0].Role != "seller" {
		t.Fatalf("the author's own sentence: %+v", slack)
	}
	turn := slack[1]
	if !turn.Quoted || turn.Role != "unknown" || turn.Basis != QuotedBasis || turn.Contact != nil ||
		turn.Text != "Dana Ito: we will sign by Friday." || turn.Shown != "yes" {
		t.Fatalf("the relayed turn: %+v", turn)
	}
	if !strings.Contains(text, fmt.Sprintf(`"Morgan Reed" (%s)`, QuotedBasis)+"\n        \"Dana Ito: we will sign by Friday.\"") {
		t.Errorf("the card does not print the turn as quoted by its author:\n%s", text)
	}
}

// The card prints a quoted sentence under no established role, as quoted by the message's author.
func TestAQuotedSentenceIsNotTheAuthorsClaim(t *testing.T) {
	units, text := built(t, "We will see.", "My update.\n> Legal approved the renewal.\nI still need confirmation.")
	slack := units["slack"]
	if len(slack) != 3 || !slack[1].Quoted || slack[1].Shown != "yes" {
		t.Fatalf("the message's units: %+v", slack)
	}
	header := fmt.Sprintf(`[%d] Slack 2027-01-13T15:00:00Z; "Morgan Reed" (%s)`, slack[1].N, QuotedBasis)
	if !strings.Contains(text, header+"\n        \"> Legal approved the renewal.\"") {
		t.Errorf("the card does not print the quotation as quoted by its author:\n%s", text)
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
