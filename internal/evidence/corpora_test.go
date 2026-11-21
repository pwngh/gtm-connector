package evidence

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The corpora in testdata/oracle/corpora are text inputs, each row a JSON list whose first member is the
// input. A golden row also holds what Go gives for it; any other row holds the input alone. Every row is run,
// and every result is checked by the corpus's properties. The golden rows are, by this rule, the first
// perShape rows of each shape, in file order: a shape is what a corpus's results can tell apart (shapeOf),
// so every shape keeps rows whose results are compared byte for byte. With -update, the golden rows are
// chosen by the rule again and written from what Go gives now, and every other row holds its input alone.
const perShape = 16

// corpus runs every row of one corpus. give is what Go gives for an input, as a golden row holds it after
// the input; property is what is wrong with that result, or empty; shape is the result's shape.
func corpus(t *testing.T, name string, give func(input string) []any, property func(input string, got []any) string,
	shape func(input string, got []any) string) {
	m := oracletest.NewMismatches(t, name)
	properties := oracletest.NewMismatches(t, "properties of "+name)
	golden := oracletest.NewGolden(t, name, strictjson.Canonical.Dumps)
	chosen, shapes := map[string]int{}, map[string]bool{}
	for i, line := range golden.Lines() {
		m.Row()
		properties.Row()
		row, err := strictjson.Loads(line)
		if err != nil {
			t.Fatal(err)
		}
		recorded := row.([]any)
		input := recorded[0].(string)
		got := give(input)
		if why := property(input, got); why != "" {
			properties.Add("%q: %s", input, why)
		}
		s := shape(input, got)
		shapes[s] = true
		wantGolden := chosen[s] < perShape
		if wantGolden {
			chosen[s]++
		}
		now := []any{input}
		if wantGolden {
			now = append(now, got...)
		}
		switch {
		case oracletest.Update():
			golden.Set(i, row, now)
		case wantGolden != (len(recorded) > 1):
			m.Add("%q: golden %t, and the rule says %t; run -update", input, len(recorded) > 1, wantGolden)
		case wantGolden && !same(recorded[1:], got):
			m.Add("%q: got %v, want %v", input, got, recorded[1:])
		}
	}
	t.Logf("%s: %d shapes, %d golden rows", name, len(shapes), sum(chosen))
}

// same is whether a golden row's recorded result is what Go gives, value for value.
func same(recorded, got []any) bool {
	a, errA := strictjson.Canonical.Dumps(recorded)
	b, errB := strictjson.Canonical.Dumps(got)
	return errA == nil && errB == nil && a == b
}

func sum(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// size is a length class: empty, short, a sentence, or long.
func size(s string) string {
	switch n := len([]rune(s)); {
	case n == 0:
		return "empty"
	case n < 16:
		return "short"
	case n < 64:
		return "sentence"
	}
	return "long"
}

func ascii(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// few is a count as 0, 1 or "2+".
func few(n int) string {
	if n > 1 {
		return "2+"
	}
	return fmt.Sprint(n)
}

// lineEnds writes each line end as \n and each tab as a space, as SafeText does without counting them.
var lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", " ", "\n", " ", "\n",
	"\v", "\n", "\f", "\n", "\x1c", "\n", "\x1d", "\n", "\x1e", "\n", "\t", " ")

// The safe_text corpus: what SafeText gives for each input, the text and its counts. Its properties: the
// text is neutral, so neutralizing it again changes nothing and counts nothing; it holds no link the link
// scanner finds; and a text with nothing counted is the input in NFC with only its line ends and tabs written
// plainly.
func TestSafeTextCorpus(t *testing.T) {
	give := func(input string) []any {
		text, counts, err := SafeText(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		return []any{text, []any{int64(counts.Mentions), int64(counts.Links), int64(counts.Controls),
			int64(counts.Brackets)}}
	}
	property := func(input string, got []any) string {
		text := got[0].(string)
		again, counts, err := SafeText(text)
		switch {
		case err != nil || again != text || counts != (Counts{}):
			return fmt.Sprintf("neutralizing %q again gives %q %v", text, again, counts)
		case len(RawLinks([]rune(text))) > 0:
			return fmt.Sprintf("%q holds a link", text)
		case same(got[1:], []any{[]any{int64(0), int64(0), int64(0), int64(0)}}) && text != u.NFCString(lineEnds.Replace(input)):
			return fmt.Sprintf("nothing is counted, and the text %q is not the input in NFC with its line ends and tabs "+
				"written plainly", text)
		}
		return ""
	}
	shape := func(input string, got []any) string {
		var counted []string
		for _, c := range got[1].([]any) {
			counted = append(counted, few(int(c.(int64))))
		}
		return fmt.Sprintf("counts %s, changed %t, %s, ascii %t", strings.Join(counted, " "), got[0] != input,
			size(input), ascii(input))
	}
	corpus(t, "corpora/safe_text.jsonl.gz", give, property, shape)
}

// The sentences corpus: the sentences of each input, and its sentences with lines, or null where they are
// the sentences without. Its properties: every sentence is a non-empty text with no space at either end;
// and either way the sentences keep every character of the input but its spaces, in order.
func TestSentencesCorpus(t *testing.T) {
	give := func(input string) []any {
		split, err := Sentences(input, false)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		byLine, err := Sentences(input, true)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		var lines any
		if !reflect.DeepEqual(split, byLine) {
			lines = anyList(byLine)
		}
		return []any{anyList(split), lines}
	}
	property := func(input string, got []any) string {
		bare := func(s string) string { return strings.Join(strings.FieldsFunc(s, u.IsSpace), "") }
		for _, list := range got {
			sentences, ok := list.([]any)
			if !ok {
				continue
			}
			joined := ""
			for _, s := range sentences {
				text := s.(string)
				if r := []rune(text); len(r) == 0 || u.IsSpace(r[0]) || u.IsSpace(r[len(r)-1]) {
					return fmt.Sprintf("the sentence %q is empty or has a space at an end", text)
				}
				joined += bare(text)
			}
			if joined != bare(input) {
				return fmt.Sprintf("the sentences %q do not keep the input's characters", sentences)
			}
		}
		return ""
	}
	shape := func(input string, got []any) string {
		split := got[0].([]any)
		ends := "none"
		if len(split) > 0 {
			last := []rune(split[len(split)-1].(string))
			if _, ended := Ended(last, 0, len(last)); ended {
				ends = "ended"
			} else {
				ends = "open"
			}
		}
		return fmt.Sprintf("sentences %d, by line apart %t, line breaks %t, last %s, %s, ascii %t",
			min(len(split), 4), got[1] != nil, strings.ContainsAny(input, "\n\r\u0085  "), ends,
			size(input), ascii(input))
	}
	corpus(t, "corpora/sentences.jsonl.gz", give, property, shape)
}

func anyList(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
