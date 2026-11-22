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
// which are fragments: without their end, with more of the message after them. A line break ends a sentence,
// because Slack wraps no text, except where the text settles that the next line goes on.
var slackCases = []struct {
	body          string
	want          []string
	quoted, parts []int
}{
	// A line is one its author ended, so two lines are never one sentence nobody wrote. A line with no closing
	// punctuation that more of the message follows is a fragment. The end of a message is an end.
	{"We can renew if\nSecurity signs off.", []string{"We can renew if", "Security signs off."}, nil, []int{0}},
	{"GC will not sign without ZDR\nProcurement asked for $300k",
		[]string{"GC will not sign without ZDR", "Procurement asked for $300k"}, nil, []int{0}},
	{"Talked to Dana\nShe wants the rider removed\nWill send the redline tomorrow",
		[]string{"Talked to Dana", "She wants the rider removed", "Will send the redline tomorrow"}, nil, []int{0, 1}},
	// A line that ends on a short form that never ends a sentence is a fragment too.
	{"We met Dr.\n- Example signed.", []string{"We met Dr.", "- Example signed."}, nil, []int{0}},
	// A line that starts in lower case goes on from the line before it, so its tail is never a sentence.
	{"Note from Evelyn at 09:40 UTC\nabout the SLA.", []string{"Note from Evelyn at 09:40 UTC about the SLA."}, nil, nil},
	{"- renew if\nthe rider holds", []string{"- renew if the rider holds"}, nil, nil},
	{"Thanks.\nwe can renew.", []string{"Thanks. we can renew."}, nil, nil},
	// So does a line after a period that ends no sentence there, as a space after it would not.
	{"We met Dr.\nExample today.", []string{"We met Dr. Example today."}, nil, nil},
	{"See invoice No.\n14 for the seats.\nNo.\nWe counted 40.", []string{"See invoice No. 14 for the seats.", "No.", "We counted 40."}, nil, nil},
	// A line that ends in closing punctuation ends its sentence.
	{"Thanks.\nWe can renew if Security signs off.", []string{"Thanks.", "We can renew if Security signs off."}, nil, nil},
	{"Will legal sign?\nThe rider is ready!\nSend it Friday.",
		[]string{"Will legal sign?", "The rider is ready!", "Send it Friday."}, nil, nil},
	// A list item is whole as written, whether Markdown or a bullet Slack writes, after any spaces and before
	// any space: its end is an end.
	{"We need:\n- SSO\n- audit logs", []string{"We need:", "- SSO", "- audit logs"}, nil, []int{0}},
	{"Blockers\n1. legal review\n2) the price", []string{"Blockers", "1. legal review", "2) the price"}, nil, []int{0}},
	{"Asks\n• SSO\n  ◦ with SCIM\n▪ audit logs", []string{"Asks", "• SSO", "◦ with SCIM", "▪ audit logs"}, nil, []int{0}},
	{"Blockers\n    ◦ legal review\n    ◦ pricing", []string{"Blockers", "◦ legal review", "◦ pricing"}, nil, []int{0}},
	{"We can renew if\n- Security signs off.", []string{"We can renew if", "- Security signs off."}, nil, []int{0}},
	{"Security review is the blocker. Maevric won't sign off without:\n• SOC 2 report\n• pen test summary\nPortal is open now.",
		[]string{"Security review is the blocker.", "Maevric won't sign off without:", "• SOC 2 report", "• pen test summary",
			"Portal is open now."}, nil, []int{1}},
	{"Options:\n1) a 3-month bridge now. The rest after Jan 13\n2) wait for the board",
		[]string{"Options:", "1) a 3-month bridge now.", "The rest after Jan 13", "2) wait for the board"}, nil, []int{0}},
	// A blank line ends a sentence.
	{"We can renew if\n\nSecurity signs off.", []string{"We can renew if", "Security signs off."}, nil, []int{0}},
	// A block quote is marked quoted, and its marker is no part of the text.
	{"My update\n> Legal approved the renewal\nI still need confirmation.",
		[]string{"My update", "Legal approved the renewal", "I still need confirmation."}, []int{1}, []int{0, 1}},
	{"> Legal approved the renewal.\n>Pricing holds.\nGood news.",
		[]string{"Legal approved the renewal.", "Pricing holds.", "Good news."}, []int{0, 1}, nil},
	// A quoted line with nothing after its marker is a blank line, and a pasted letter keeps its lines.
	{">Subject: Renewal PO\n>\n>Hello,\n>We will issue the PO by December 5.\n>Saludos,\n>Luis",
		[]string{"Subject: Renewal PO", "Hello,", "We will issue the PO by December 5.", "Saludos,", "Luis"},
		[]int{0, 1, 2, 3, 4}, []int{0, 1, 3}},
	// A quoted line a mail program wrapped goes on in lower case.
	{"> We appreciate the movement but $2.6M remains our\n> ceiling for FY27.",
		[]string{"We appreciate the movement but $2.6M remains our ceiling for FY27."}, []int{0}, nil},
	{"> We appreciate the movement but\n>\n> ceiling for FY27.",
		[]string{"We appreciate the movement but", "ceiling for FY27."}, []int{0, 1}, []int{0}},
	{"Their note says\n> ceiling for FY27.", []string{"Their note says", "ceiling for FY27."}, []int{1}, []int{0}},
	// Each line of a code block is one unit as written, and its fences are no part of the text.
	{"Seat math I just ran\n```\nseats = 116\n```\nPlease check it.",
		[]string{"Seat math I just ran", "seats = 116", "Please check it."}, nil, []int{0}},
	{"Run this:\n```make test```\nthen tell me.", []string{"Run this:", "make test", "then tell me."}, nil, []int{0}},
	{"```\n§ 9.2 Limitation of liability\n- ours: 12 months' fees. Capped\ntheirs: 24 months\n```\nOurs holds.",
		[]string{"§ 9.2 Limitation of liability", "- ours: 12 months' fees. Capped", "theirs: 24 months", "Ours holds."}, nil, nil},
	{"```{\n  \"seats\": 210\n}```", []string{"\"seats\": 210"}, nil, nil},
	// A code block in a block quote is quoted, and ends with the quote. In a code block, > is code.
	{"> ```\n> Seats = 40\n> ```\nOurs holds.", []string{"Seats = 40", "Ours holds."}, []int{0}, nil},
	{"> ```\n> Seats = 40\nOurs holds.", []string{"Seats = 40", "Ours holds."}, []int{0}, nil},
	{"```\n> not a quote\n```", []string{"> not a quote"}, nil, nil},
	// A line that opens with the name of a known person of the card, a contact or a listed seller, is that
	// person's turn, marked quoted.
	{"Pasted from the call\nDana Ito: we can sign\nMorgan  Reed: then send it",
		[]string{"Pasted from the call", "Dana Ito: we can sign", "Morgan Reed: then send it"}, []int{1, 2}, []int{0, 1}},
	// The lines under a turn stay that person's words until a blank line, so none becomes the author's claim.
	{"Dana Ito: I disagree that\nwe will sign Friday.", []string{"Dana Ito: I disagree that we will sign Friday."}, []int{0}, nil},
	{"Dana Ito: we can sign.\nThe rider is fine.\n\nI agree.", []string{"Dana Ito: we can sign.", "The rider is fine.", "I agree."},
		[]int{0, 1}, nil},
	// A label that names nobody known is the author's own line.
	{"Update: legal is done\nBlocker: pricing\nDana: we can sign",
		[]string{"Update: legal is done", "Blocker: pricing", "Dana: we can sign"}, nil, []int{0, 1}},
	{"Our numbers\nJune renewal math: 90 seats.", []string{"Our numbers", "June renewal math: 90 seats."}, nil, []int{0}},
	// A dash or a number that is no list marker starts a line like any other.
	{"Seats go to\n-5 from 40", []string{"Seats go to", "-5 from 40"}, nil, []int{0}},
	// A quotation in curly marks holds its own closing punctuation.
	{"Desmond on the call: “We’ll sign three years, but not at 7%. Give us 2026 rates and we’ll sign.”",
		[]string{"Desmond on the call: “We’ll sign three years, but not at 7%. Give us 2026 rates and we’ll sign.”"}, nil, nil},
	{"“First. Second.\nThird.”", []string{"“First. Second. Third.”"}, nil, nil},
	{"That is a no. § 4 still applies.", []string{"That is a no.", "§ 4 still applies."}, nil, nil},
	// "no." is a number sign only before a number, and a month written short ends a sentence before a word
	// that opens one.
	{"Uncapped liability is a no. Counter at 3x fees.", []string{"Uncapped liability is a no.", "Counter at 3x fees."}, nil, nil},
	{"See invoice no. 14 for the seats.", []string{"See invoice no. 14 for the seats."}, nil, nil},
	{"iOS beta lands in Feb. I'll tell Luis on Monday.", []string{"iOS beta lands in Feb.", "I'll tell Luis on Monday."}, nil, nil},
	{"The board meets Feb. 24 and Mar. 3.", []string{"The board meets Feb. 24 and Mar. 3."}, nil, nil},
}

// cardPeople are the known people of the card built builds: its contact Dana Ito and its listed seller
// Morgan Reed, compared as the card compares a label.
func cardPeople(name string) bool {
	return knownPerson(name, []string{"Morgan Reed"}, []contact{{name: "Dana Ito", n: 2}})
}

func TestSentencesInASlackMessage(t *testing.T) {
	for _, c := range slackCases {
		got, quoted, _, err := sentences(c.body, true, cardPeople)
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
	// In a call note or a cue, a line break is a wrap.
	if got, _ := Sentences("We need:\n- SSO", false); !reflect.DeepEqual(got, []string{"We need: - SSO"}) {
		t.Errorf("Sentences without lines: got %q", got)
	}
	// With no person known, no line is a turn.
	if _, quoted, _, _ := sentences("Pasted\nDana Ito: we can sign", true, nil); quoted[1] {
		t.Error("with no person known, a line is a turn")
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

// Slack stores the &, < and > an author typed as &amp;, &lt; and &gt;. The card reads each back once, before
// it neutralizes the text, so a sentence holds the characters its author typed, a link or a mention typed
// by hand is replaced as Slack's own is, and a quoted line is found by its marker.
func TestASlackMessageIsReadAsItsAuthorTypedIt(t *testing.T) {
	for _, c := range []struct {
		body string
		want []string
	}{
		{"Pelham &amp; Oster replied:\n&gt; We will sign by Friday.", []string{"Pelham & Oster replied:", "We will sign by Friday."}},
		{"Use &amp;lt; for a bracket.", []string{"Use &lt; for a bracket."}},
		{"The public one is &lt;//help.example/retention|retention help page&gt;.",
			[]string{"The public one is retention help page [link help.example]."}},
		{"From: Accounts Payable &lt;ap@ironbark.example&gt;", []string{"From: Accounts Payable [link mail]"}},
		{"She typed &lt;@U0ABC123&gt; and &lt;https://portal.example|our portal&gt;.",
			[]string{"She typed [user U0ABC123] and our portal [link portal.example]."}},
		// A tag typed by hand is text, as it is in a call note: a card's text is plain text.
		{"Use &lt;b&gt; for the heading.", []string{"Use <b> for the heading."}},
		// A label that repeats its target says nothing more than the link.
		{"Send it to <mailto:ap@a.example|ap@a.example>.", []string{"Send it to [link mail]."}},
		{"Read <https://a.example/x|https://a.example/x> first.", []string{"Read [link a.example] first."}},
		{"Send it to <mailto:ap@www.a.example|ap@www.a.example>.", []string{"Send it to [link mail]."}},
		// A sentence is neutralized as it stands on the card: without its marker and its indent, this
		// line is a reference definition, which a Markdown client reads as a link.
		{"&gt;     [spec]: &lt;/terms&gt;", []string{"[spec]: [link unknown host]"}},
	} {
		units, _ := built(t, "We will see.", c.body)
		var got []string
		for _, u := range units["slack"] {
			got = append(got, u.Text)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: the card's Slack sentences are %q, want %q", c.body, got, c.want)
		}
	}
	units, _ := built(t, "We will see.", "Pelham &amp; Oster replied:\n&gt; We will sign by Friday.")
	if quoted := units["slack"][1]; !quoted.Quoted || quoted.Role != "unknown" || quoted.Shown != "yes" {
		t.Errorf("the quoted line: %+v", quoted)
	}
	units, _ = built(t, "Intro.\n\n    [spec]: </terms>", "Signed.")
	if note := units["calls"][1].Text; note != "[spec]: [link unknown host]" {
		t.Errorf("an indented definition in a call note is shown as %q", note)
	}
	// A call note is not Slack's: its entities stay as written.
	units, _ = built(t, "Pelham &amp; Oster signed.", "Signed.")
	if note := units["calls"][0].Text; note != "Pelham &amp; Oster signed." {
		t.Errorf("a call note is read as %q", note)
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
	// A label that names nobody known to the card, such as a heading, leaves the line the author's own.
	units, _ = built(t, "We will see.", "From the call today.\nUpdate: we will sign by Friday.\nDana: no rider.")
	for _, u := range units["slack"] {
		if u.Quoted || u.Role != "seller" {
			t.Errorf("the author's own line %q is quoted %v, with role %q", u.Text, u.Quoted, u.Role)
		}
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
	if !strings.Contains(text, header+"\n        \"Legal approved the renewal.\"") {
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
