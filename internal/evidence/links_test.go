package evidence

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// neutral checks that each case gives its text with the counts given, and that the text then stays as it is.
func neutral(t *testing.T, raw, safe string, want Counts) {
	t.Helper()
	text, counts, err := SafeText(raw)
	if err != nil || text != safe || counts != want {
		t.Errorf("SafeText(%q): got %q %+v %v, want %q %+v", raw, text, counts, err, safe, want)
	}
	if text, counts, err := SafeText(safe); err != nil || text != safe || counts != (Counts{}) {
		t.Errorf("SafeText(%q) a second time: got %q %+v %v", raw, text, counts, err)
	}
}

// A link in any form is reduced to its host, and a scheme a browser does not know gives none. The cases are
// those added to test_a_link_in_any_form_is_reduced_to_its_host in tests/test_units.py.
func TestLinkOfASchemeABrowserDoesNotKnow(t *testing.T) {
	checkLinks(t, [][2]string{
		{"<slack://channel?id=C1|open>", "open [link unknown host]"},
		{"custom://UPPER.example/ now", "[link unknown host] now"},
		{"[deck](ssh://files.example/)", "deck [link unknown host]"},
		{"wss://files.example/x", "[link files.example]"},
	})
}

// The cases of test_a_link_in_the_finished_text_is_replaced_and_nothing_around_it in tests/test_units.py.
func TestLinkInFinishedTextLeavesTextAroundIt(t *testing.T) {
	raw := "See [deck](https:/files.example/deck(v2)). The deal is blocked."
	neutral(t, raw, "See deck [link unknown host]. The deal is blocked.", Counts{Links: 1})
	safe, _, _ := SafeText(raw)
	if got, _ := Sentences(safe, false); !reflect.DeepEqual(got, []string{"See deck [link unknown host].",
		"The deal is blocked."}) {
		t.Errorf("Sentences: got %q", got)
	}
	checkLinks(t, [][2]string{
		{"[deck](tel:5551234).", "deck [link unknown host]."},
		{"[deck](https:/a.example/b(c)d) now", "deck [link unknown host] now"},
		{"[deck](tel:5551234 \"the deck\"), then", "deck [link unknown host], then"},
		{"[deck](tel:5551234\n'the\ndeck'\n) then", "deck [link unknown host] then"},
	})
	for _, plain := range []string{"x ]( deal: y", "We said ]( deal: and moved on.", "[deck](tel:1(2)",
		"[deck](tel:1 'a\n\nb')", "[deck](tel:1 'a' b)", "[deck](tel:1 x)", "[deck]\n\n(tel:1)"} {
		neutral(t, plain, plain, Counts{})
	}
}

// The cases of test_a_link_with_its_target_in_angle_brackets_is_replaced_whole in tests/test_units.py.
func TestLinkWithTargetInAngleBrackets(t *testing.T) {
	for _, c := range [][2]string{
		{"[deck](<https:/files.example/a b>)", "deck [link unknown host]"},
		{"[deck](< tel:5551234 x>)", "deck [link unknown host]"},
		{"[deck](<https://files.example/a b> \"t\")", "deck [link files.example]"},
		{"[deck](<tel:5551234>)", "deck [link unknown host]"},
		{"[deck](\n <https://files.example/a b>)", "deck [link files.example]"},
		{"<@[doc](https://c.example/z)", "[user]doc [link c.example]"},
	} {
		neutral(t, c[0], c[1], Counts{Mentions: strings.Count(c[0], "<@"), Links: 1})
	}
	neutral(t, "[x](<@U1>)", "[x]([user U1])", Counts{Mentions: 1})
}

// The cases of test_a_reference_definition_is_read_only_where_a_paragraph_starts in tests/test_units.py.
func TestReferenceDefinitionStartsAParagraph(t *testing.T) {
	for _, c := range []struct {
		raw, safe string
		links     int
	}{
		{">  [deck]: https:/files.example\n>   'the title\n>   continues'\n>\n> [deck]",
			">  [deck]: [link unknown host]\n>   'the title\n>   continues'\n>\n> [deck]", 1},
		{"We said:\n> [deck]: tel:5551234", "We said:\n> [deck]: [link unknown host]", 1},
		{"Items\n- [deck]: tel:5551234", "Items\n- [deck]: [link unknown host]", 1},
		{"# Notes\n[deck]: tel:5551234", "# Notes\n[deck]: [link unknown host]", 1},
		{"[a]: tel:1\n[b]: tel:2\nsee [a]", "[a]: [link unknown host]\n[b]: [link unknown host]\nsee [a]", 2},
		{"[deck]: tel:5551234\n\"the deck\" was sent", "[deck]: [link unknown host]\n\"the deck\" was sent", 1},
	} {
		neutral(t, c.raw, c.safe, Counts{Links: c.links})
	}
	for _, plain := range []string{"We kept a log.\n[10:32]: Dana:\nShe agreed.", "[10:32]: Dana: 'we can sign\n\nLater.",
		"> We kept a log.\n> [10:32]: Dana:", "> We kept a log.\n[10:32]: Dana:",
		"[10:32]: Dana: (laughs\nand then she said yes"} {
		neutral(t, plain, plain, Counts{})
	}
}

// The cases of test_a_markdown_target_is_read_with_its_escapes_and_character_references in tests/test_units.py.
func TestMarkdownTargetEscapesAndReferences(t *testing.T) {
	checkLinks(t, [][2]string{
		{"[deck](https://files.example&sol;@other.example/)", "deck [link files.example]"},
		{"[deck](https://files.example&#47;@other.example/)", "deck [link files.example]"},
		{"[deck](https://files.example&#X2f;@other.example/)", "deck [link files.example]"},
		{`[deck](https://files.example\/@other.example/)`, "deck [link files.example]"},
		{"[deck](https://files.example&nosuch;@other.example/)", "deck [link other.example]"},
		{"[deck](https://files.example&#0;/)", "deck [link unknown host]"},
		{"[deck](https://files.example&#1114112;/)", "deck [link unknown host]"},
		{"[deck](&#104;ttps:&sol;&sol;files.example/)", "deck [link files.example]"},
		{`[deck](https\:/files.example)`, "deck [link unknown host]"},
		{"[deck](https&colon;//files.example/x(1))", "deck [link files.example]"},
		{"[deck](//%66iles.example/)", "deck [link unknown host]"},
		{"[deck](///files.example)", "deck [link unknown host]"},
		{"<https://files.example&sol;@other.example/>", "[link unknown host]"},
		{"<https://files.example/a&amp;b|deck>", "deck [link files.example]"},
		{"see https://files.example&sol;@other.example/ now", "see [link unknown host] now"},
	})
	if text, _, _ := SafeText("[deck]: &#104;ttps:&sol;&sol;files.example&sol;@other.example/"); text !=
		"[deck]: [link files.example]" {
		t.Errorf("a definition read with its references: got %q", text)
	}
	for _, plain := range []string{"[deck](relative/path)", "[deck](files.example&sol;x)", `[deck](a\/b:c)`} {
		neutral(t, plain, plain, Counts{})
	}
}

// The cases of test_a_link_in_running_text_runs_as_a_github_client_reads_it in tests/test_units.py.
func TestRawLinkExtent(t *testing.T) {
	checkLinks(t, [][2]string{
		{"See https://files.example'@other.example/a then", "See [link unknown host] then"},
		{"See https://files.example] then", "See [link unknown host] then"},
		{"See https://files.example>@other.example then", "See [link unknown host] then"},
		{`See https://files.example/a"b>c then`, "See [link files.example] then"},
		{`"https://files.example/x"`, `"[link files.example]"`},
		{"(see https://files.example/a_(b)) now", "(see [link files.example]) now"},
		{"see https://files.example/a&amp; now", "see [link files.example]&amp; now"},
		{"see https://files.example/a_~*: now", "see [link files.example]_~*: now"},
		{"see...https://files.example/x now", "see...[link files.example] now"},
		{"svn+ssh://files.example/x", "[link unknown host]"},
	})
}

// The cases of test_a_label_a_browser_refuses_is_not_shown in tests/test_units.py.
func TestPunycodeLabel(t *testing.T) {
	checkLinks(t, [][2]string{
		{"https://xn--bcher-kva.example/ now", "[link unknown host] now"},
		{"<https://files.XN--p1ai/|deck>", "deck [link unknown host]"},
		{"<https://files.example.xn--/|deck>", "deck [link unknown host]"},
		{"<https://axn--b.example/|deck>", "deck [link axn--b.example]"},
	})
}

// The cases of test_a_slack_token_is_read_innermost_first_and_may_hold_a_line_break in tests/test_units.py.
func TestSlackTokenInnermostFirst(t *testing.T) {
	for _, c := range []struct {
		raw, safe       string
		mentions, links int
	}{
		{"<https:/files.example|outer <https://inner.example|inner>>",
			"outer inner [link inner.example] [link unknown host]", 0, 2},
		{"<https:/files.example|one\ntwo>", "one\ntwo [link unknown host]", 0, 1},
		{"We agree <#<@U1> will not sign> today.", "We agree [channel] today.", 2, 0},
		{"<x|<y|<z|deep>>>", "deep [link z] [link y] [link x]", 0, 3},
		{"<a <b> c>", "<a <b> c>", 0, 0},
		{"<https://files.example\n>", "<[link unknown host]\n>", 0, 1},
	} {
		neutral(t, c.raw, c.safe, Counts{Mentions: c.mentions, Links: c.links})
	}
	safe, _, _ := SafeText("<https:/files.example|one\ntwo>")
	if got, _ := Sentences(safe, false); !reflect.DeepEqual(got, []string{"one two [link unknown host]"}) {
		t.Errorf("Sentences: got %q", got)
	}
}

// Text that is already neutral stays as it is. The pieces are those of test_neutral_text_stays_as_it_is in
// tests/test_units.py.
func TestNeutralTextStaysAsItIs(t *testing.T) {
	pieces := []string{"<", ">", "|", "<@U1>", "<#C1|", "<!here", "<https://a.example|", "<tel:1>", "<a@b.example>",
		"[", "]", "](", ")", "(", "[deck](", "tel:1", "https:/x.example", "https://x.example", "www.",
		"//x.example", "[7]", "\x00", "\x1c", "\t", "\n", "\n\n", "> ", "- ", "]: ", "'", `"`, " ", `\`,
		"&sol;", "&#104;", "x", "Dana:", ".", "mailto:"}
	generator := rand.New(rand.NewPCG(7, 0))
	for range 4000 {
		var raw strings.Builder
		for range 1 + generator.IntN(11) {
			raw.WriteString(pieces[generator.IntN(len(pieces))])
		}
		safe, _, _ := SafeText(raw.String())
		if text, counts, err := SafeText(safe); err != nil || text != safe || counts != (Counts{}) {
			t.Errorf("SafeText(%q) a second time: got %q %+v %v", raw.String(), text, counts, err)
		}
	}
}

// A Markdown destination is read with its escapes and character references as units._unescape reads it,
// and every named reference of Python's html5 table stands for the same text.
func TestReferences(t *testing.T) {
	var fixture struct {
		Entities map[string]string `json:"entities"`
		Cases    []struct {
			Input      string `json:"input"`
			Escapes    string `json:"escapes"`
			References string `json:"references"`
		} `json:"cases"`
	}
	oracletest.JSON(t, "library/references.json", &fixture)
	if len(fixture.Entities) < 2000 {
		t.Fatalf("the oracle has %d named references", len(fixture.Entities))
	}
	for name, want := range fixture.Entities {
		if got, ok := namedReference(name); !ok || got != want {
			t.Errorf("&%s;: got %q %v, want %q", name, got, ok, want)
		}
	}
	for _, c := range fixture.Cases {
		if got := string(unescape([]rune(c.Input), true)); got != c.Escapes {
			t.Errorf("_unescape(%q): got %q, want %q", c.Input, got, c.Escapes)
		}
		if got := string(unescape([]rune(c.Input), false)); got != c.References {
			t.Errorf("_unescape(%q, False): got %q, want %q", c.Input, got, c.References)
		}
	}
}
