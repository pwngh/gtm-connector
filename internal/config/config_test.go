package config

import (
	"errors"
	"strings"
	"testing"
)

// The file of docs/config.md reads whole: its sources in order, and each setting no build applies yet named.
func TestTheGuidesFileReads(t *testing.T) {
	guide := `sources:
  crm:   {kind: salesforce}
  chat:  {kind: slack, channels: ["#deal-*", "#ext-*", "#renewals", "#deal-desk"]}
  calls: {kind: transcripts, folder: calls/}
  gong:  {kind: gong}
  inbox: {kind: push}                       # internal email, SMS, webhook push
  app:   {kind: sql, view: renewal_contacts}  # a read-only view in your own database
watch:
  renewals_closing_within_days: 90
rules:
  customer_silence_days: 14
timezone: America/Chicago
approval: all
`
	file, err := Read([]byte(guide))
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, source := range file.Sources {
		sources = append(sources, source.Name+"="+source.Kind)
	}
	if got := strings.Join(sources, " "); got != "crm=salesforce chat=slack calls=transcripts" {
		t.Errorf("the sources: %s", got)
	}
	want := "sources.chat.channels sources.calls.folder sources.gong sources.inbox sources.app.view sources.app watch rules timezone approval"
	if got := strings.Join(file.NotApplied, " "); got != want {
		t.Errorf("not applied:\n %s\nwant\n %s", got, want)
	}
	if string(file.Settings()) != `{"sources":{"calls":{"kind":"transcripts"},"chat":{"kind":"slack"},"crm":{"kind":"salesforce"}}}` {
		t.Errorf("the settings: %s", file.Settings())
	}
	// A file of sources alone holds nothing this build does not apply, and an empty file is a file.
	plain, err := Read([]byte("sources:\n  crm: {kind: demo}\n"))
	if err != nil || len(plain.NotApplied) != 0 || len(plain.Sources) != 1 {
		t.Errorf("a file of one source: %+v %v", plain, err)
	}
	if empty, err := Read([]byte("# nothing set yet\n\n")); err != nil || len(empty.Sources) != 0 {
		t.Errorf("an empty file: %+v %v", empty, err)
	}
}

// The same bytes are the same revision, and one byte more is another.
func TestARevisionNamesTheBytes(t *testing.T) {
	a, b := Revision([]byte("approval: all\n")), Revision([]byte("approval: all\n\n"))
	if len(a) != 64 || a == b || a != Revision([]byte("approval: all\n")) || a != strings.ToLower(a) {
		t.Errorf("revisions %s and %s", a, b)
	}
}

// The reader takes the YAML the guide shows and refuses the rest, naming the line.
func TestTheReaderRefusesWhatItDoesNotTake(t *testing.T) {
	for label, c := range map[string]struct {
		file string
		line int
		says string
	}{
		"an anchor":                 {"sources:\n  crm: &base {kind: demo}\n", 2, "may not start with &"},
		"an alias":                  {"approval: *all\n", 1, "may not start with *"},
		"a tag":                     {"approval: !!str all\n", 1, "may not start with !"},
		"a block scalar":            {"timezone: |\n  America/Chicago\n", 1, "may not start with |"},
		"a second document":         {"approval: all\n---\napproval: all\n", 2, "document marker"},
		"a tab":                     {"sources:\n\tcrm: {kind: demo}\n", 2, "tab"},
		"a key twice":               {"approval: all\napproval: ambiguous\n", 2, "given twice"},
		"a key twice in braces":     {"sources:\n  crm: {kind: demo, kind: slack}\n", 2, "given twice"},
		"a list over several lines": {"sources:\n  chat:\n    kind: slack\n    channels:\n      - a\n", 5, "on one line"},
		"three spaces":              {"sources:\n   crm: {kind: demo}\n", 2, "two spaces"},
		"a key with no value":       {"approval:\n", 1, "has no value"},
		"null":                      {"approval: null\n", 1, "no value here"},
		"an open brace":             {"sources:\n  crm: {kind: demo\n", 2, "not closed"},
		"an open quote":             {"timezone: \"America\n", 1, "not closed"},
		"text after a value":        {"sources:\n  crm: {kind: demo} extra\n", 2, "after the value"},
		"an unknown setting":        {"aproval: all\n", 1, "no setting of gtm.yaml"},
		"an unknown source setting": {"sources:\n  crm: {kind: demo, token: abc}\n", 2, "no setting of a source"},
		"an unknown kind":           {"sources:\n  crm: {kind: hubspot}\n", 2, "no kind of source"},
		"a source with no kind":     {"sources:\n  crm: {channels: [a]}\n", 2, "has no kind"},
		"approval as a word":        {"approval: yes\n", 1, "ambiguous or all"},
		"approval as true":          {"approval: true\n", 1, "ambiguous or all"},
		"days as text":              {"watch:\n  renewals_closing_within_days: ninety\n", 2, "whole number of days"},
		"days below one":            {"watch:\n  renewals_closing_within_days: 0\n", 2, "whole number of days"},
		"a control character":       {"approval: all\x07\n", 1, "control character"},
		"an indented first key":     {"  approval: all\n", 1, "indented"},
	} {
		_, err := Read([]byte(c.file))
		var refused *Error
		if !errors.As(err, &refused) || refused.Line != c.line || !strings.Contains(refused.Message, c.says) {
			t.Errorf("%s: %v, want line %d saying %q", label, err, c.line, c.says)
		}
	}
	if _, err := Read([]byte(strings.Repeat("#", MaxBytes+1))); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a file over the limit: %v", err)
	}
	if _, err := Read([]byte("approval: \xff\n")); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("a file that is not UTF-8: %v", err)
	}
}

// Words that general YAML reads as something else stay words, and quotes hold what they say.
func TestWordsStayWords(t *testing.T) {
	root, err := parse([]byte("a: no\nb: 1e3\nc: \"say \\\"hi\\\" # not a comment\"\nd: 'it''s'\ne: -7\nf: [x, \"y, z\", {k: v}]\ng: on # a comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := func(key string) string {
		if root.items[key].text == nil {
			return "<not text>"
		}
		return *root.items[key].text
	}
	if text("a") != "no" || text("b") != "1e3" || text("c") != `say "hi" # not a comment` || text("d") != "it's" || text("g") != "on" {
		t.Errorf("a=%q b=%q c=%q d=%q g=%q", text("a"), text("b"), text("c"), text("d"), text("g"))
	}
	if root.items["e"].whole == nil || *root.items["e"].whole != -7 {
		t.Errorf("e is %s", root.items["e"].kind())
	}
	list := root.items["f"].list
	if len(list) != 3 || *list[0].text != "x" || *list[1].text != "y, z" || !list[2].mapping() || *list[2].items["k"].text != "v" {
		t.Errorf("f is %+v", list)
	}
}

// A setting this build does not apply is still checked: a draft that could never be right is refused now.
func TestSettingsNotAppliedAreStillChecked(t *testing.T) {
	for label, c := range map[string]struct {
		file string
		line int
	}{
		"channels as true":          {"sources:\n  chat: {kind: slack, channels: true}\n", 2},
		"channels holding a number": {"sources:\n  chat: {kind: slack, channels: [a, 7]}\n", 2},
		"an empty channel":          {"sources:\n  chat: {kind: slack, channels: [\"\"]}\n", 2},
		"folder as a list":          {"sources:\n  calls: {kind: transcripts, folder: []}\n", 2},
		"view as a mapping":         {"sources:\n  app: {kind: sql, view: {extra: 1}}\n", 2},
		"an empty timezone":         {"timezone: \"\"\n", 1},
		"a timezone that is none":   {"timezone: \"not a zone!\"\n", 1},
		"a timezone as a number":    {"timezone: 12\n", 1},
	} {
		_, err := Read([]byte(c.file))
		var refused *Error
		if !errors.As(err, &refused) || refused.Line != c.line {
			t.Errorf("%s: %v, want a refusal at line %d", label, err, c.line)
		}
	}
	if _, err := Read([]byte("timezone: America/Chicago\nsources:\n  chat: {kind: slack, channels: [\"#deal-*\", ext]}\n  calls: {kind: transcripts, folder: calls/}\n")); err != nil {
		t.Errorf("settings of the right kind: %v", err)
	}
}

// The reader reads what was written or refuses it: a key with no space after its colon, a value that is a
// stray bracket, a character that does not print, and a line end that is none.
func TestTheReaderDoesNotReadAnotherFile(t *testing.T) {
	for label, c := range map[string]struct {
		file string
		line int
	}{
		"a key and value run together in braces": {"sources:\n  crm: {kind:demo}\n", 2},
		"a closing bracket as a value":           {"timezone: ]\n", 1},
		"a closing brace as a value":             {"approval: }\n", 1},
		"a comma as a value":                     {"timezone: ,x\n", 1},
		"a next-line control character":          {"approval: all\u0085\n", 1},
		"a right-to-left override in a comment":  {"approval: all # \u202e hidden\n", 1},
		"a zero-width space":                     {"approval: a\u200bll\n", 1},
		"a carriage return with no line end":     {"approval: all\r", 1},
		"a carriage return inside a line":        {"approval: a\rll\n", 1},
	} {
		_, err := Read([]byte(c.file))
		var refused *Error
		if !errors.As(err, &refused) || refused.Line != c.line {
			t.Errorf("%s: %v, want a refusal at line %d", label, err, c.line)
		}
	}
	if file, err := Read([]byte("sources:\r\n  crm: {kind: demo}\r\n")); err != nil || len(file.Sources) != 1 {
		t.Errorf("a file with CRLF line ends: %+v %v", file, err)
	}
}

// A refusal says where and what was expected, and never repeats what it refused: a value pasted by mistake
// stays out of the terminal and the logs.
func TestARefusalRepeatsNothingOfTheFile(t *testing.T) {
	const pasted = "sk-invented-secret-123"
	for label, file := range map[string]string{
		"an unknown kind":           "sources:\n  crm: {kind: " + pasted + "}\n",
		"an unknown setting":        pasted + ": all\n",
		"an unknown source setting": "sources:\n  crm: {kind: demo, " + pasted + ": 1}\n",
		"text after a value":        "sources:\n  crm: {kind: demo} " + pasted + "\n",
		"a wrong approval":          "approval: " + pasted + "\n",
		"a key that is no key":      "a " + pasted + ": 1\n",
		"a number too large":        "watch:\n  renewals_closing_within_days: 99999999999999999999999\n",
	} {
		_, err := Read([]byte(file))
		if err == nil || strings.Contains(err.Error(), pasted) || strings.Contains(err.Error(), "9999999999") {
			t.Errorf("%s: %v", label, err)
		}
	}
}
