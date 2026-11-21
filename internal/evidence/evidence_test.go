package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
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

func TestSafeTextCorpus(t *testing.T) {
	m := oracletest.NewMismatches(t, "safe_text")
	for _, line := range oracletest.Lines(t, "corpora/safe_text.jsonl.gz") {
		var row struct {
			input, text string
			counts      [4]int
		}
		var raw [3]json.RawMessage
		decode(t, line, &raw)
		decode(t, raw[0], &row.input)
		decode(t, raw[1], &row.text)
		decode(t, raw[2], &row.counts)
		m.Row()
		text, counts, err := SafeText(row.input)
		if err != nil {
			t.Fatal(err)
		}
		got := [4]int{counts.Mentions, counts.Links, counts.Controls, counts.Brackets}
		if text != row.text || got != row.counts {
			m.Add("%q: got %q %v, want %q %v", row.input, text, got, row.text, row.counts)
		}
	}
}

func TestSentencesCorpus(t *testing.T) {
	split := oracletest.NewMismatches(t, "sentences")
	byLine := oracletest.NewMismatches(t, "sentences with lines")
	for _, line := range oracletest.Lines(t, "corpora/sentences.jsonl.gz") {
		var raw [3]json.RawMessage
		var input string
		var want, wantLines []string
		decode(t, line, &raw)
		decode(t, raw[0], &input)
		decode(t, raw[1], &want)
		decode(t, raw[2], &wantLines)
		if wantLines == nil {
			wantLines = want
		}
		split.Row()
		byLine.Row()
		if got, _ := Sentences(input, false); !reflect.DeepEqual(got, want) {
			split.Add("%q: got %q, want %q", input, got, want)
		}
		if got, _ := Sentences(input, true); !reflect.DeepEqual(got, wantLines) {
			byLine.Add("%q: got %q, want %q", input, got, wantLines)
		}
	}
}

type outcome struct {
	OK      json.RawMessage `json:"ok"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

func (o outcome) check(got any, err error) string {
	if err != nil {
		var refusal *valueError
		if !errors.As(err, &refusal) {
			return fmt.Sprintf("got %T %q, which is not a ValueError", err, err)
		}
		if o.Error != "ValueError" || o.Message != refusal.Message {
			return fmt.Sprintf("got ValueError %q, want %s %s %q", refusal.Message, o.OK, o.Error, o.Message)
		}
		return ""
	}
	if o.Error != "" {
		return fmt.Sprintf("got %s, want %s %q", show(got), o.Error, o.Message)
	}
	var want any
	if err := json.Unmarshal(o.OK, &want); err != nil || show(want) != show(got) {
		return fmt.Sprintf("got %s, want %s", show(got), o.OK)
	}
	return ""
}

func TestURLSplit(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Input    string  `json:"input"`
			URLSplit outcome `json:"urlsplit"`
			Hostname outcome `json:"hostname"`
			Port     outcome `json:"port"`
			Host     string  `json:"host"`
		} `json:"cases"`
	}
	oracletest.JSON(t, "library/urlsplit.json", &fixture)
	m := oracletest.NewMismatches(t, "urlsplit, hostname, port and host")
	refused, read := 0, 0
	for _, c := range fixture.Cases {
		m.Row()
		if nonASCIINetloc(c.Input) {
			if strings.Contains(c.URLSplit.Message, "NFKC") {
				refused++
			} else {
				read++
			}
		}
		input := []rune(c.Input)
		parts, err := splitURL(input)
		var got any
		if err == nil {
			got = []string{parts.Scheme, parts.Netloc, parts.Path, parts.Query, parts.Fragment}
		}
		if problem := c.URLSplit.check(got, err); problem != "" {
			m.Add("urlsplit(%q): %s", c.Input, problem)
		}
		var port any
		if err == nil {
			var number int
			if number, err = portNumber(parts.Netloc); err == nil && number >= 0 {
				port = number
			}
		}
		if problem := c.Port.check(port, err); problem != "" {
			m.Add("port of %q: %s", c.Input, problem)
		}
		// The hostname as units._host asks for it.
		head := input
		if len(head) > 40 {
			head = head[:40]
		}
		target := input
		if !strings.Contains(string(head), "//") {
			target = append([]rune("//"), input...)
		}
		var hostname any
		parts, err = splitURL(target)
		if err == nil {
			if name := hostName(parts.Netloc); name != "" {
				hostname = name
			}
		}
		if problem := c.Hostname.check(hostname, err); problem != "" {
			m.Add("hostname of %q: %s", c.Input, problem)
		}
		if got := linkHost(input); got != c.Host {
			m.Add("_host(%q): got %q, want %q", c.Input, got, c.Host)
		}
	}
	// The NFKC netloc check must be exercised both ways.
	if refused < 6 || read < 5 {
		t.Errorf("the oracle has %d refused and %d read non-ASCII netlocs; expected at least 6 and 5", refused, read)
	}
}

// nonASCIINetloc is whether a link's netloc holds a non-ASCII character.
func nonASCIINetloc(input string) bool {
	parts, err := splitURL([]rune(input))
	if err != nil {
		_, rest, ok := strings.Cut(input, "//")
		netloc, _, _ := strings.Cut(rest, "/")
		return ok && strings.ContainsFunc(netloc, func(c rune) bool { return c > 127 })
	}
	return strings.ContainsFunc(parts.Netloc, func(c rune) bool { return c > 127 })
}

// The oracle's behaviour where Go will later differ on purpose (backend plan section 4). Until those
// decisions land, Go matches the oracle here too.
func TestDifferencesStillMatchOracle(t *testing.T) {
	var fixture struct {
		SafeTextTwice struct{ Input, Once, Twice string } `json:"safe_text_twice"`
		Sentences     map[string]struct {
			Input     string   `json:"input"`
			Sentences []string `json:"sentences"`
		} `json:"sentences"`
	}
	oracletest.JSON(t, "library/differences.json", &fixture)
	once, _, _ := SafeText(fixture.SafeTextTwice.Input)
	twice, _, _ := SafeText(once)
	if once != fixture.SafeTextTwice.Once || twice != fixture.SafeTextTwice.Twice {
		t.Errorf("safe_text twice: got %q then %q", once, twice)
	}
	for name, c := range fixture.Sentences {
		if got, _ := Sentences(c.Input, false); !reflect.DeepEqual(got, c.Sentences) {
			t.Errorf("%s: got %q, want %q", name, got, c.Sentences)
		}
	}
}

// A link with a backslash or a control character up to the end of its authority shows no host. The cases are
// those of test_a_link_with_an_ambiguous_host_shows_no_host in tests/test_units.py.
func TestAmbiguousLinkHost(t *testing.T) {
	for _, c := range [][2]string{
		// A browser reads a backslash before the path as a slash, so it opens evil.example here.
		{`<http://evil.example\@good.example/|deck>`, "deck [link unknown host]"},
		{`http://evil.example\@good.example/ now`, "[link unknown host] now"},
		{`[deck](http://evil.example\@good.example/)`, "deck [link unknown host]"},
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
// test_a_link_with_no_slashes_after_its_scheme_shows_no_host in tests/test_units.py.
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
		// A www. address, a // address and a link with its // keep the host before a port or a colon.
		{"<files.example/x|deck>", "deck [link files.example]"},
		{"www.files.example:8443/x now", "[link files.example] now"},
		{"<WWW.files.example:8443|deck>", "deck [link files.example]"},
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
