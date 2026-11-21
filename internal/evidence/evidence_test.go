package evidence

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

func decode(t *testing.T, data []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
}

func scannerInputs(t *testing.T) [][]rune {
	var inputs [][]rune
	for _, line := range oracletest.Lines(t, "scanners/inputs.jsonl.gz") {
		var text string
		decode(t, line, &text)
		inputs = append(inputs, []rune(text))
	}
	return inputs
}

func spans(found []Span) [][2]int {
	out := [][2]int{}
	for _, s := range found {
		out = append(out, [2]int{s.Start, s.End})
	}
	return out
}

func TestScanners(t *testing.T) {
	inputs := scannerInputs(t)
	scanners := map[string]func([]rune) any{
		"units_target": func(s []rune) any {
			if end, ok := Target(s); ok {
				return &end
			}
			return (*int)(nil)
		},
		"units_raw_link": func(s []rune) any { return spans(RawLinks(s)) },
		"units_sentence_end": func(s []rune) any {
			out := [][3]int{}
			for _, m := range SentenceEnds(s) {
				out = append(out, [3]int{m.Start, m.End1, m.End2})
			}
			return out
		},
		"units_opener": func(s []rune) any {
			out := [][2]int{}
			for at := 0; at <= len(s); at++ {
				if end, ok := Opener(s, at, len(s)); ok {
					out = append(out, [2]int{at, end})
				}
			}
			return out
		},
		"units_named": func(s []rune) any {
			if m, ok := NamedMatch(s); ok {
				return &[3]int{m.Start, m.NameEnd, m.End}
			}
			return (*[3]int)(nil)
		},
		"slack_mapping_percent":  func(s []rune) any { return spans(Percents(s)) },
		"slack_callback_percent": func(s []rune) any { return spans(Percents(s)) },
	}
	var index struct {
		Scanners map[string]string `json:"scanners"`
	}
	oracletest.JSON(t, "scanners/index.json", &index)
	if len(index.Scanners) != len(scanners) {
		t.Fatalf("the oracle lists %d scanners; this test knows %d", len(index.Scanners), len(scanners))
	}
	for name, scan := range scanners {
		t.Run(name, func(t *testing.T) {
			lines := oracletest.Lines(t, "scanners/"+name+".jsonl.gz")
			if len(lines) != len(inputs) {
				t.Fatalf("%d results for %d inputs", len(lines), len(inputs))
			}
			m := oracletest.NewMismatches(t, name)
			for i, line := range lines {
				m.Row()
				got := scan(inputs[i])
				want := reflect.New(reflect.TypeOf(got))
				decode(t, line, want.Interface())
				if !reflect.DeepEqual(got, want.Elem().Interface()) {
					m.Add("input %d %q: got %s, want %s", i, string(inputs[i]), show(got), line)
				}
			}
		})
	}
}

func show(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// Each windowed search the sentence rules make, by pattern and method: the match's [start, end].
var windowed = map[string]func(s []rune, pos, end int) (int, int, bool){
	"_LIST_MARKER fullmatch": func(s []rune, pos, end int) (int, int, bool) {
		ok := ListMarkerFull(s, pos, end)
		pos, end = clamp(s, pos, end)
		return pos, end, ok
	},
	"_DOTTED search":       atEnd(Dotted),
	"_MAY_END search":      atEnd(MayEnd),
	"_ABBREVIATION search": atEnd(Abbreviation),
	"_SHORT search": func(s []rune, pos, end int) (int, int, bool) {
		start, _, _, ok := Short(s, pos, end)
		_, end = clamp(s, pos, end)
		return start, end, ok
	},
	"_NUMBER_SIGN search": atEnd(NumberSign),
	"_NEVER_LAST search":  atEnd(NeverLast),
	"_ENDED search":       atEnd(Ended),
	"_NUMBER_SIGN fullmatch": func(s []rune, pos, end int) (int, int, bool) {
		ok := NumberSignFull(s, pos, end)
		pos, end = clamp(s, pos, end)
		return pos, end, ok
	},
	"_SECTION_SIGN match": fromPos(SectionSign),
	"_SHORT_NEXT match":   fromPos(ShortNext),
	"_OPENER match":       fromPos(Opener),
}

// atEnd is a search whose pattern ends at \Z, so its match ends at endpos, clamped to the text.
func atEnd(search func([]rune, int, int) (int, bool)) func([]rune, int, int) (int, int, bool) {
	return func(s []rune, pos, end int) (int, int, bool) {
		start, ok := search(s, pos, end)
		_, end = clamp(s, pos, end)
		return start, end, ok
	}
}

// fromPos is a match, which starts at pos, clamped to the text.
func fromPos(match func([]rune, int, int) (int, bool)) func([]rune, int, int) (int, int, bool) {
	return func(s []rune, pos, end int) (int, int, bool) {
		found, ok := match(s, pos, end)
		pos, _ = clamp(s, pos, end)
		return pos, found, ok
	}
}

func TestWindowedSearches(t *testing.T) {
	var fixture struct {
		Lines []string            `json:"lines"`
		Calls [][]json.RawMessage `json:"calls"`
	}
	oracletest.JSON(t, "scanners/windowed.json.gz", &fixture)
	lines := make([][]rune, len(fixture.Lines))
	for i, line := range fixture.Lines {
		lines[i] = []rune(line)
	}
	m := oracletest.NewMismatches(t, "windowed searches")
	for _, call := range fixture.Calls {
		var name, method string
		var line, pos, end int
		var want *[2]int
		decode(t, call[0], &name)
		decode(t, call[1], &method)
		decode(t, call[2], &line)
		decode(t, call[3], &pos)
		decode(t, call[4], &end)
		decode(t, call[5], &want)
		search, ok := windowed[name+" "+method]
		if !ok {
			t.Fatalf("no scanner for %s %s", name, method)
		}
		m.Row()
		var got *[2]int
		if start, stop, found := search(lines[line], pos, end); found {
			got = &[2]int{start, stop}
		}
		if !reflect.DeepEqual(got, want) {
			m.Add("%s.%s(%q, %d, %d): got %s, want %s", name, method, fixture.Lines[line], pos, end, show(got), show(want))
		}
	}
}

// The host a card shows for each recorded link target, and for a www. address in running text. With -update,
// a case Go now answers otherwise is written again.
func TestLinkHost(t *testing.T) {
	const name = "library/link_host.json"
	data := oracletest.Read(t, name)
	value, err := strictjson.Loads(data)
	if err != nil {
		t.Fatal(err)
	}
	pretty := strictjson.DumpOptions{EnsureASCII: true, Indent: 1}
	if was, err := pretty.Dumps(value); oracletest.Update() && (err != nil || was+"\n" != string(data)) {
		t.Fatalf("%s: the file's encoding is not reproduced (%v)", name, err)
	}
	cases, _ := value.(*strictjson.Object).Get("cases")
	m := oracletest.NewMismatches(t, "the host of a link target")
	named, running, changed := 0, 0, 0
	for _, c := range cases.([]any) {
		m.Row()
		recorded := c.(*strictjson.Object)
		input, _ := recorded.Get("input")
		for _, reading := range []struct {
			key  string
			bare bool
		}{{"host", false}, {"running_text", true}} {
			want, _ := recorded.Get(reading.key)
			if want == nil {
				continue
			}
			if reading.bare {
				running++
			}
			got := linkHost([]rune(input.(string)), reading.bare)
			if got == want {
				continue
			}
			if !oracletest.Update() {
				m.Add("_host(%q, bare=%v): got %q, want %q", input, reading.bare, got, want)
				continue
			}
			recorded.Set(reading.key, got)
			changed++
		}
		if host, _ := recorded.Get("host"); host != unknownHost {
			named++
		}
	}
	if changed > 0 {
		now, _ := pretty.Dumps(value)
		oracletest.Write(t, name, []byte(now+"\n"))
		t.Logf("%s: %d answers written again", name, changed)
	}
	// Both answers and both readings must be exercised.
	if named < 50 || len(cases.([]any))-named < 50 || running < 5 {
		t.Errorf("the oracle has %d named hosts of %d and %d www. addresses", named, len(cases.([]any)), running)
	}
}

// The oracle's behaviour where Go will later differ on purpose (backend plan section 4). Until those
// decisions land, Go matches the oracle here too.
func TestDifferencesStillMatchOracle(t *testing.T) {
	var fixture struct {
		Sentences map[string]struct {
			Input     string   `json:"input"`
			Sentences []string `json:"sentences"`
		} `json:"sentences"`
	}
	oracletest.JSON(t, "library/differences.json", &fixture)
	for name, c := range fixture.Sentences {
		if got, _ := Sentences(c.Input, false); !reflect.DeepEqual(got, c.Sentences) {
			t.Errorf("%s: got %q, want %q", name, got, c.Sentences)
		}
	}
}

// A link with a backslash or a control character up to the end of its authority shows no host. The cases are
// those of test_a_link_with_an_ambiguous_host_shows_no_host of the removed Python suite.
func TestAmbiguousLinkHost(t *testing.T) {
	for _, c := range [][2]string{
		// A browser reads a backslash before the path as a slash, so it opens evil.example here.
		{`<http://evil.example\@good.example/|deck>`, "deck [link unknown host]"},
		{`http://evil.example\@good.example/ now`, "[link unknown host] now"},
		// In a Markdown target a backslash before punctuation is an escape, so the browser is given
		// http://evil.example@good.example/ and opens good.example.
		{`[deck](http://evil.example\@good.example/)`, "deck [link good.example]"},
		{`<//evil.example\@good.example|deck>`, "deck [link unknown host]"},
		{`<C:\path|deck>`, "deck [link unknown host]"},
		// A control character in the host, or anywhere before the path.
		{"<http://a\tb.example/x|deck>", "deck [link unknown host]"},
		{"<http://a\rb.example/x|deck>", "deck [link unknown host]"},
		{"<http://user\x00@good.example/|deck>", "deck [link unknown host]"},
		{"<ht\ttp://good.example/|deck>", "deck [link unknown host]"},
		{"http://good\x7f.example/x now", "[link unknown host] now"},
		{"http://\u009f@good.example/x now", "[link unknown host] now"},
		// An ordinary link, and a link with either character after its host, keep the host.
		{"<http://files.example/x|deck>", "deck [link files.example]"},
		{"<https://user@files.example:8443/x?q=1#f|deck>", "deck [link files.example]"},
		{`<http://files.example/a\b|deck>`, "deck [link files.example]"},
		{"<http://files.example/a\tb|deck>", "deck [link files.example]"},
		{`<http://files.example?a\@b|deck>`, "deck [link files.example]"},
	} {
		text, counts, err := SafeText(c[0])
		if err != nil || text != c[1] || counts != (Counts{Links: 1}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c[0], text, counts, err, c[1])
		}
	}
}

// A link with no // after its scheme shows no host. The cases are those of
// test_a_link_with_no_slashes_after_its_scheme_shows_no_host of the removed Python suite.
func TestLinkHostWithNoSlashesAfterScheme(t *testing.T) {
	for _, c := range [][2]string{
		// A browser reads https:files.example as https://files.example. Read as a host, it would be https.
		{"<https:files.example/x|deck>", "deck [link unknown host]"},
		{"<https:/files.example|deck>", "deck [link unknown host]"},
		{"<HTTP:8080|deck>", "deck [link unknown host]"},
		{"<tel:5551234|deck>", "deck [link unknown host]"},
		{"<javascript:alert(1)|deck>", "deck [link unknown host]"},
		// A name before a colon is a scheme to a browser, whatever follows the colon.
		{"<files.example:8443/x|deck>", "deck [link unknown host]"},
		// A scheme too long for its // to be found is not read as a host either.
		{"[deck](" + strings.Repeat("a", 40) + "://files.example/)", "deck [link unknown host]"},
		// A www. address, a // address and a link with its // keep the host before a port or a colon. A
		// target with no scheme and no // is relative to the page and names no host.
		{"<files.example/x|deck>", "deck [link unknown host]"},
		{"www.files.example:8443/x now", "[link files.example] now"},
		{"<//files.example:8443/x|deck>", "deck [link files.example]"},
		{"<https://files.example/a:b|deck>", "deck [link files.example]"},
		{"<mailto:a@files.example|deck>", "deck [link mail]"},
	} {
		text, counts, err := SafeText(c[0])
		if err != nil || text != c[1] || counts != (Counts{Links: 1}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c[0], text, counts, err, c[1])
		}
	}
}

// A link cut by a tab or a line break before its host ends shows no host. The cases are those of
// test_a_link_cut_by_a_tab_or_a_line_break_shows_no_host of the removed Python suite.
func TestLinkCutByTabOrLineBreak(t *testing.T) {
	for _, c := range []struct {
		raw, safe string
		controls  int
	}{
		// A browser drops a tab or a line break from a link, so it opens first.example.second.example here.
		{"https://first.example\t.second.example/x", "[link unknown host] .second.example/x", 0},
		{"https://first.example\n.second.example/x", "[link unknown host]\n.second.example/x", 0},
		{"https://first.example\r.second.example/x", "[link unknown host][U+000D].second.example/x", 1},
		{"https://first.example\r\n\t.second.example/x", "[link unknown host]\n .second.example/x", 0},
		// A Slack token with a | part holds a line break.
		{"<https://first.example\n.second.example/x|deck>", "deck [link unknown host]", 0},
		{"[deck](https://first.example\t.second.example/x)", "[deck]([link unknown host] .second.example/x)", 0},
		// What follows can also turn the host into a user name, or go on after closing punctuation.
		{"//first.example\t@second.example/x", "[link unknown host] @second.example/x", 0},
		{"https://first.example,\tsecond.example", "[link unknown host], second.example", 0},
		{"https://first.example\nNext line", "[link unknown host]\nNext line", 0},
		// A link cut after its host, or followed by a space, the end, or the start of a path, keeps its host.
		{"https://first.example/a\t.second.example", "[link first.example] .second.example", 0},
		{"https://first.example?\t.second.example", "[link first.example]? .second.example", 0},
		{"https://first.example\t/x", "[link first.example] /x", 0},
		{"https://first.example\t next", "[link first.example]  next", 0},
		{"https://first.example\n", "[link first.example]\n", 0},
		{"https://first.example .second.example", "[link first.example] .second.example", 0},
		{"mailto:a@first.example\t.second.example", "[link mail] .second.example", 0},
	} {
		text, counts, err := SafeText(c.raw)
		if err != nil || text != c.safe || counts != (Counts{Links: 1, Controls: c.controls}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c.raw, text, counts, err, c.safe)
		}
	}
}

// A Markdown target with any scheme is a link. The cases are those of
// test_a_markdown_link_with_any_scheme_is_a_link of the removed Python suite.
func TestMarkdownLinkWithAnyScheme(t *testing.T) {
	// A client renders [deck](HTTPS:/files.example) as a link, and a browser opens files.example.
	for _, raw := range []string{"[deck](HTTPS:/files.example)", "[deck](https:files.example/x)",
		"[deck](tel:5551234)", "[deck](javascript:alert)", "[deck]( x-app:open )", `[deck](C:\path)`} {
		text, counts, err := SafeText(raw)
		if err != nil || text != "deck [link unknown host]" || counts != (Counts{Links: 1}) {
			t.Errorf("SafeText(%q): got %q %+v %v", raw, text, counts, err)
		}
	}
	// A target with no scheme is a path, and a time is not a scheme.
	for _, plain := range []string{"[docs](relative/path)", "[docs](a/b:c)", "[time](12:30)", "[docs](:x)",
		"(note: fine)", "[docs] (tel:5551234)"} {
		if text, counts, err := SafeText(plain); err != nil || text != plain || counts != (Counts{}) {
			t.Errorf("SafeText(%q): got %q %+v %v", plain, text, counts, err)
		}
	}
	// A client also reads a link whose label holds a line break or brackets, whose target holds parentheses or a
	// control character, or whose label is a bracketed word the connector wrote. The whole link becomes its
	// label and a marker, and a link the marker then starts is read too.
	for _, c := range []struct {
		raw, safe       string
		links, controls int
	}{
		{"[de\nck](HTTPS:/files.example)", "de\nck [link unknown host]", 1, 0},
		{"[a [b] c]( tel:5551234 )", "a [b] c [link unknown host]", 1, 0},
		{"[deck](HTTPS:/files.example/\x1cx)", "deck [link unknown host]", 1, 1},
		{"[deck](javascript:alert(1))", "deck [link unknown host]", 1, 0},
		{"<https://files.example>(HTTPS:/second.example/x)", "link files.example [link unknown host]", 2, 0},
		{"\x00(tel:5551234)(tel:5551234)", "U+0000 link unknown host [link unknown host]", 2, 1},
	} {
		text, counts, err := SafeText(c.raw)
		if err != nil || text != c.safe || counts != (Counts{Links: c.links, Controls: c.controls}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c.raw, text, counts, err, c.safe)
		}
		if text, counts, err := SafeText(c.safe); err != nil || text != c.safe || counts != (Counts{}) {
			t.Errorf("SafeText(%q) a second time: got %q %+v %v", c.raw, text, counts, err)
		}
	}
}

func checkLinks(t *testing.T, cases [][2]string) {
	t.Helper()
	for _, c := range cases {
		text, counts, err := SafeText(c[0])
		if err != nil || text != c[1] || counts != (Counts{Links: 1}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c[0], text, counts, err, c[1])
		}
	}
}

// A www. address is a host only in running text, and a host after // or in running text is shown without a
// leading www. The cases are those of test_a_www_address_is_a_host_only_in_running_text of the removed
// Python suite.
func TestWWWAddress(t *testing.T) {
	checkLinks(t, [][2]string{
		// As a link target, www.files.example:8443/x has the scheme www.files.example and no host.
		{"[deck](www.files.example:8443/x)", "deck [link unknown host]"},
		{"<www.files.example:8443/x|deck>", "deck [link unknown host]"},
		{"<WWW.files.example:8443|deck>", "deck [link unknown host]"},
		{"<WWW.user:pass@files.example/x|deck>", "deck [link unknown host]"},
		// In running text a client opens it as a web address, so the colon starts a password or a port.
		{"www.user:pass@files.example/x now", "[link files.example] now"},
		{"www.user@files.example:8443/a//b now", "[link files.example] now"},
		{"www.files.example:8443/x now", "[link files.example] now"},
		// After // or in running text the host is shown without a leading www., however many.
		{"<https://www.files.example/|deck>", "deck [link files.example]"},
		{"<//WWW.www.files.example:8443/x|deck>", "deck [link files.example]"},
		{"www.files.example/a//b now", "[link files.example] now"},
		// A target with no // names no host of its own, so its www. is not removed, and www. after another
		// label, or before a number a browser refuses, gives no host.
		{"<www.files.example|deck>", "deck [link unknown host]"},
		{"[deck](www.files.example/x)", "deck [link unknown host]"},
		{"<https://files.www.example/|deck>", "deck [link unknown host]"},
		{"<https://www.192.0.2.1/|deck>", "deck [link unknown host]"},
		{"<https://www./|deck>", "deck [link unknown host]"},
		// Two slashes in a path are not the start of a host.
		{"<files.example/a//b|deck>", "deck [link unknown host]"},
		{"<https://files.example/a//b|deck>", "deck [link files.example]"},
		{"<//files.example/a//b|deck>", "deck [link files.example]"},
	})
}

// An angle-bracket link with a scheme is a link. The cases are those of
// test_an_angle_bracket_link_with_a_scheme_is_a_link of the removed Python suite.
func TestAngleBracketLinkWithScheme(t *testing.T) {
	// A Markdown client links <scheme:value>: a scheme of 2 to 32 characters, a colon, and no space.
	for _, c := range []struct {
		raw, safe       string
		links, controls int
	}{
		{"<https:/files.example>", "[link unknown host]", 1, 0},
		{"see <HTTPS:files.example/x> now", "see [link unknown host] now", 1, 0},
		{"<tel:5551234>", "[link unknown host]", 1, 0},
		{"<x-app:open?id=1|2>.", "2 [link unknown host].", 1, 0},
		{"<" + strings.Repeat("a", 32) + ":x>", "[link unknown host]", 1, 0},
		// A control character or an inner token leaves no space, so the finished text is still a link.
		{"<tel:555\x1c1234>", "[link unknown host]", 1, 1},
		{"<tel:555<#|>1234>", "[link unknown host]", 1, 0},
		// What follows the marker it leaves is read too.
		{"<tel:1>(tel:2)", "link unknown host [link unknown host]", 2, 0},
		{"<tel:1>: tel:2", "[link unknown host]: [link unknown host]", 2, 0},
		// A mail address in angle brackets is a mail link.
		{"<a@files.example>", "[link mail]", 1, 0},
		{"mail <a.b+c@files-1.example> now", "mail [link mail] now", 1, 0},
	} {
		want := Counts{Mentions: strings.Count(c.raw, "<#"), Links: c.links, Controls: c.controls}
		text, counts, err := SafeText(c.raw)
		if err != nil || text != c.safe || counts != want {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c.raw, text, counts, err, c.safe)
		}
		if text, counts, err := SafeText(c.safe); err != nil || text != c.safe || counts != (Counts{}) {
			t.Errorf("SafeText(%q) a second time: got %q %+v %v", c.raw, text, counts, err)
		}
	}
	// A space, a one-letter scheme, a scheme of 33 characters, a time and a voice tag are not links.
	for _, plain := range []string{"<tel:555 1234>", "<v:Dana>", "<" + strings.Repeat("a", 33) + ":x>", "<12:30>",
		"<v Dana Ito>", "<note>", "a < b: c > d"} {
		if text, counts, err := SafeText(plain); err != nil || text != plain || counts != (Counts{}) {
			t.Errorf("SafeText(%q): got %q %+v %v", plain, text, counts, err)
		}
	}
}

// The destination of a reference definition is a link. The cases are those of
// test_the_destination_of_a_reference_definition_is_a_link of the removed Python suite.
func TestReferenceDefinition(t *testing.T) {
	// A Markdown client reads "[deck]: https:/files.example" on its own line as where [deck] leads.
	for _, c := range []struct {
		raw, safe string
		links     int
	}{
		{"[deck]: https:/files.example", "[deck]: [link unknown host]", 1},
		{"see [deck]\n\n   [deck]: HTTPS:files.example/x\n", "see [deck]\n\n   [deck]: [link unknown host]\n", 1},
		{"[deck]:\n  tel:5551234", "[deck]:\n  [link unknown host]", 1},
		{"[deck]:\ttel:5551234  ", "[deck]: [link unknown host]  ", 1},
		// A title may follow the destination, and may go on to the next line.
		{`[deck]: tel:5551234 "the deck"`, `[deck]: [link unknown host] "the deck"`, 1},
		{`[deck]: tel:5551234 (the \) deck) `, `[deck]: [link unknown host] (the \) deck) `, 1},
		{"[deck]: tel:5551234 'the\ndeck'", "[deck]: [link unknown host] 'the\ndeck'", 1},
		// A destination in angle brackets may hold a space.
		{"[deck]: <https:/files.example/a b>", "[deck]: [link unknown host]", 1},
		{"[deck]: <a b> 'the deck'", "[deck]: [link unknown host] 'the deck'", 1},
		// The label may hold a line break or an escaped bracket, or be a bracketed word the connector wrote.
		{"[the\ndeck]: tel:5551234", "[the\ndeck]: [link unknown host]", 1},
		{`[the \] deck]: tel:5551234`, `[the \] deck]: [link unknown host]`, 1},
		{"<https://files.example>: https:/second.example/x", "[link files.example]: [link unknown host]", 2},
		{"[10:32]: Dana:", "[10:32]: [link unknown host]", 1},
	} {
		text, counts, err := SafeText(c.raw)
		if err != nil || text != c.safe || counts != (Counts{Links: c.links}) {
			t.Errorf("SafeText(%q): got %q %+v %v, want %q", c.raw, text, counts, err, c.safe)
		}
		if text, counts, err := SafeText(c.safe); err != nil || text != c.safe || counts != (Counts{}) {
			t.Errorf("SafeText(%q) a second time: got %q %+v %v", c.raw, text, counts, err)
		}
	}
	if text, counts, err := SafeText("[[7]]: tel:5551234"); err != nil || text != "[(7)]: [link unknown host]" ||
		counts != (Counts{Links: 1, Brackets: 1}) {
		t.Errorf("SafeText of a definition with a number in its label: got %q %+v %v", text, counts, err)
	}
	// Words after the destination make the line ordinary text, and so does anything before the label.
	for _, plain := range []string{"[10:32]: Dana: hello", "[Q3 notes]: Summary: the renewal moved to March",
		`   [14:05]: Morgan: "we can sign" was the answer`, "[10:32]: Dana:hello there",
		"    [deck]: https:/files.example", "see [deck]: https:/files.example", "[deck] : https:/files.example",
		"[deck]: relative/path", "[]: tel:5551234", "[deck]: tel:5551234 'the' deck'"} {
		if text, counts, err := SafeText(plain); err != nil || text != plain || counts != (Counts{}) {
			t.Errorf("SafeText(%q): got %q %+v %v", plain, text, counts, err)
		}
	}
}

// A host a browser rewrites or refuses is not shown. The cases are those of
// test_a_host_a_browser_rewrites_or_refuses_is_not_shown of the removed Python suite.
func TestHostABrowserRewritesOrRefuses(t *testing.T) {
	checkLinks(t, [][2]string{
		// A browser reads each of these as the address 192.0.2.1.
		{"<http://0xc0000201/|deck>", "deck [link unknown host]"},
		{"<http://0XC0000201/|deck>", "deck [link unknown host]"},
		{"<http://3221225985/|deck>", "deck [link unknown host]"},
		{"<http://0300.0.2.1/|deck>", "deck [link unknown host]"},
		{"<http://192.0.513/|deck>", "deck [link unknown host]"},
		{"<http://192.0.2.1./|deck>", "deck [link unknown host]"},
		{"<http://192.0.2.01/|deck>", "deck [link unknown host]"},
		// A last label that is a number makes the whole host an address, which a browser then refuses.
		{"<http://files.example.0x1/|deck>", "deck [link unknown host]"},
		{"<http://files.example.7/|deck>", "deck [link unknown host]"},
		{"<http://files.0x/|deck>", "deck [link unknown host]"},
		{"<http://192.0.2.256/|deck>", "deck [link unknown host]"},
		{"<http://1.2.3.4.5/|deck>", "deck [link unknown host]"},
		// A port that is not a number up to 65535.
		{"<https://files.example:65536/|deck>", "deck [link unknown host]"},
		{"<https://files.example:80a/|deck>", "deck [link unknown host]"},
		{"<https://files.example:80:80/|deck>", "deck [link unknown host]"},
		{"<https://files.example:-1/|deck>", "deck [link unknown host]"},
		{"<https://files.example:" + strings.Repeat("9", 5000) + "/|deck>", "deck [link unknown host]"},
		// A bracketed host, whatever it holds, and a file link, which opens a path.
		{"<http://[v1.files.example]/|deck>", "deck [link unknown host]"},
		{"<http://[::1]:8443/|deck>", "deck [link unknown host]"},
		{"<http://files.example]/|deck>", "deck [link unknown host]"},
		{"<file://C:/x|deck>", "deck [link unknown host]"},
		// Four plain decimal parts, a number in another label, and a port in range stay as written.
		{"<http://192.0.2.1/|deck>", "deck [link 192.0.2.1]"},
		{"<http://192.0.2.1:8443/|deck>", "deck [link 192.0.2.1]"},
		{"<https://1.example/|deck>", "deck [link 1.example]"},
		{"<https://0x10.files.example/|deck>", "deck [link 0x10.files.example]"},
		{"<https://files.example:65535/|deck>", "deck [link files.example]"},
		{"<https://files.example:00000080/|deck>", "deck [link files.example]"},
		{"<https://files.example:/|deck>", "deck [link files.example]"},
		// A dot that ends the host is part of it.
		{"<https://files.example./x|deck>", "deck [link files.example.]"},
		{"<https://files.example.|deck>", "deck [link files.example.]"},
		{"see https://files.example./x now", "see [link files.example.] now"},
		// In running text a dot right after the host or its port may be the link's or the sentence's.
		{"see https://files.example. Next", "see [link unknown host]. Next"},
		{"see https://user@files.example.) Next", "see [link unknown host].) Next"},
		{"see //files.example... Next", "see [link unknown host]... Next"},
		{"see https://files.example:8443. Next", "see [link unknown host]. Next"},
		{"see http://192.0.2.1. Next", "see [link unknown host]. Next"},
		// A dot after a path closes the sentence, and so does other punctuation after the host.
		{"see https://files.example/x. Next", "see [link files.example]. Next"},
		{"see https://files.example?. Next", "see [link files.example]?. Next"},
		{"see (https://files.example). Next", "see ([link files.example]). Next"},
		{"see https://files.example, then", "see [link files.example], then"},
		{"see mailto:a@files.example. Next", "see [link mail]. Next"},
	})
}

// Text that is not valid UTF-8 is refused, whether its bytes are malformed or encode a lone surrogate.
func TestInvalidText(t *testing.T) {
	for name, text := range map[string]string{
		"a stray continuation byte": "We sign \x80 today.",
		"a cut sequence":            "We sign today \xc3",
		"an overlong slash":         "<http://a\xc0\xafb.example|deck>",
		"a lone surrogate":          "We sign \xed\xa0\x80 today.",
		"a low surrogate":           "\xed\xb0\x80",
	} {
		if _, _, err := SafeText(text); !errors.Is(err, ErrInvalidText) {
			t.Errorf("SafeText with %s: got %v", name, err)
		}
		for _, lines := range []bool{false, true} {
			if _, err := Sentences(text, lines); !errors.Is(err, ErrInvalidText) {
				t.Errorf("Sentences with %s: got %v", name, err)
			}
		}
	}
}

// The scanners take code points, so a surrogate reaches them as Python's re sees it. Each expected value was
// read from CPython 3.14.6.
func TestScannersCarrySurrogates(t *testing.T) {
	text := []rune{'h', 't', 't', 'p', ':', '/', '/', 'a', 0xD800, 'b', ' ', 'W', 'e', '.', ' ', 0xDC00, 'X'}
	if got := spans(RawLinks(text)); !reflect.DeepEqual(got, [][2]int{{0, 10}}) {
		t.Errorf("RawLinks: got %v", got)
	}
	if got := SentenceEnds(text); !reflect.DeepEqual(got, []SentenceEnd{{13, 14, 15}}) {
		t.Errorf("SentenceEnds: got %v", got)
	}
}
