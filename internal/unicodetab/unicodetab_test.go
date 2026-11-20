package unicodetab

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

func expand(ranges [][2]int) []bool {
	flags := make([]bool, MaxRune+1)
	for _, r := range ranges {
		for c := r[0]; c <= r[1]; c++ {
			flags[c] = true
		}
	}
	return flags
}

func checkEvery(t *testing.T, name string, ranges [][2]int, got func(rune) bool) {
	t.Helper()
	m := oracletest.NewMismatches(t, name)
	want := expand(ranges)
	for c := rune(0); c <= MaxRune; c++ {
		m.Row()
		if got(c) != want[c] {
			m.Add("U+%04X: got %v, want %v", c, got(c), want[c])
		}
	}
}

func TestPredicates(t *testing.T) {
	var predicates map[string]json.RawMessage
	oracletest.JSON(t, "unicode/predicates.json", &predicates)
	for name, got := range map[string]func(rune) bool{
		"isspace": IsSpace, "isdigit": IsDigit, "isdecimal": IsDecimal, "isprintable": IsPrintable,
		"islower": IsLower, "isupper": IsUpper, "istitle_only": IsTitleOnly, "isalpha": IsAlpha, "isascii": IsASCII,
	} {
		var ranges [][2]int
		if err := json.Unmarshal(predicates[name], &ranges); err != nil {
			t.Fatal(name, err)
		}
		checkEvery(t, name, ranges, got)
	}
}

func TestRegexClasses(t *testing.T) {
	var regex struct {
		Classes    map[string][][2]int `json:"classes"`
		IgnoreCase map[string][][2]int `json:"ignorecase"`
	}
	oracletest.JSON(t, "unicode/regex_classes.json", &regex)
	checkEvery(t, `re \s`, regex.Classes[`\s`], ReSpace)
	checkEvery(t, `re \d`, regex.Classes[`\d`], ReDigit)
	checkEvery(t, `re \w`, regex.Classes[`\w`], ReWord)
	checkEvery(t, "IGNORECASE [a-z]", regex.IgnoreCase["[a-z]"], IgnoreCaseAZ)
	for letter := byte('a'); letter <= 'z'; letter++ {
		checkEvery(t, "IGNORECASE "+string(letter), regex.IgnoreCase[string(letter)],
			func(c rune) bool { return IgnoreCaseLetter(letter, c) })
	}
	if len(regex.IgnoreCase) != 27 || len(regex.Classes) != 3 {
		t.Errorf("regex_classes.json holds %d classes and %d IGNORECASE sets; the tests know 3 and 27",
			len(regex.Classes), len(regex.IgnoreCase))
	}
}

func TestNFKCNetloc(t *testing.T) {
	var netloc struct {
		Ranges [][2]int `json:"ranges"`
	}
	oracletest.JSON(t, "unicode/nfkc_netloc.json", &netloc)
	checkEvery(t, "NFKC netloc marks", netloc.Ranges, NFKCNetloc)
}

func TestCategories(t *testing.T) {
	var categories struct {
		Runs [][3]any `json:"runs"`
	}
	oracletest.JSON(t, "unicode/categories.json", &categories)
	want := make([]string, MaxRune+1)
	for _, run := range categories.Runs {
		for c := int(run[0].(float64)); c <= int(run[1].(float64)); c++ {
			want[c] = run[2].(string)
		}
	}
	m := oracletest.NewMismatches(t, "unicodedata.category")
	for c := rune(0); c <= MaxRune; c++ {
		m.Row()
		if got := Category(c); got != want[c] {
			m.Add("U+%04X: got %s, want %s", c, got, want[c])
		}
	}
}

func TestCase(t *testing.T) {
	var cases struct {
		Lower      [][2]json.RawMessage `json:"lower"`
		FinalSigma map[string][][2]int  `json:"final_sigma"`
		Strings    [][4]any             `json:"strings"`
	}
	oracletest.JSON(t, "unicode/case.json", &cases)
	want := map[rune][]rune{}
	for _, entry := range cases.Lower {
		var code rune
		var parts []rune
		if json.Unmarshal(entry[0], &code) != nil || json.Unmarshal(entry[1], &parts) != nil {
			t.Fatal("case.json lower entry")
		}
		want[code] = parts
	}
	m := oracletest.NewMismatches(t, "str.lower per code point")
	for c := rune(0); c <= MaxRune; c++ {
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		m.Row()
		expected, ok := want[c]
		if !ok {
			expected = []rune{c}
		}
		if got := Lower([]rune{c}); !slices.Equal(got, expected) {
			m.Add("U+%04X: got %U, want %U", c, got, expected)
		}
	}
	checkEvery(t, "case-ignorable", cases.FinalSigma["case_ignorable"], caseIgnorable.has)
	checkEvery(t, "cased", cases.FinalSigma["cased"], cased.has)
	s := oracletest.NewMismatches(t, "whole strings: lower, islower, isupper")
	for _, row := range cases.Strings {
		s.Row()
		text := []rune(row[0].(string))
		if got := string(Lower(text)); got != row[1].(string) {
			s.Add("%q.lower(): got %q, want %q", row[0], got, row[1])
		}
		if got := IsLowerString(text); got != row[2].(bool) {
			s.Add("%q.islower(): got %v", row[0], got)
		}
		if got := IsUpperString(text); got != row[3].(bool) {
			s.Add("%q.isupper(): got %v", row[0], got)
		}
	}
}

type nfcFixture struct {
	Combining    [][3]int             `json:"combining_class"`
	NFD          [][2]json.RawMessage `json:"nfd"`
	NFC          [][2]json.RawMessage `json:"nfc"`
	Compositions [][3]rune            `json:"compositions"`
	Hangul       [][3][]rune          `json:"hangul"`
}

func mapped(t *testing.T, entries [][2]json.RawMessage) map[rune][]rune {
	m := map[rune][]rune{}
	for _, entry := range entries {
		var code rune
		var parts []rune
		if json.Unmarshal(entry[0], &code) != nil || json.Unmarshal(entry[1], &parts) != nil {
			t.Fatal("nfc.json entry")
		}
		m[code] = parts
	}
	return m
}

func TestNormalizationPerCodePoint(t *testing.T) {
	var fixture nfcFixture
	oracletest.JSON(t, "unicode/nfc.json", &fixture)
	classes := make([]int, MaxRune+1)
	for _, run := range fixture.Combining {
		for c := run[0]; c <= run[1]; c++ {
			classes[c] = run[2]
		}
	}
	cc := oracletest.NewMismatches(t, "unicodedata.combining")
	for c := rune(0); c <= MaxRune; c++ {
		cc.Row()
		if got := int(CombiningClass(c)); got != classes[c] {
			cc.Add("U+%04X: got %d, want %d", c, got, classes[c])
		}
	}
	nfd, nfc := mapped(t, fixture.NFD), mapped(t, fixture.NFC)
	d := oracletest.NewMismatches(t, "NFD per code point")
	n := oracletest.NewMismatches(t, "NFC per code point")
	for c := rune(0); c <= MaxRune; c++ {
		if c >= 0xD800 && c <= 0xDFFF || c >= hangulS && c < hangulS+hangulSCount {
			continue
		}
		d.Row()
		n.Row()
		want, ok := nfd[c]
		if !ok {
			want = []rune{c}
		}
		if got := NFD([]rune{c}); !slices.Equal(got, want) {
			d.Add("U+%04X: got %U, want %U", c, got, want)
		}
		want, ok = nfc[c]
		if !ok {
			want = []rune{c}
		}
		if got := NFC([]rune{c}); !slices.Equal(got, want) {
			n.Add("U+%04X: got %U, want %U", c, got, want)
		}
	}
	p := oracletest.NewMismatches(t, "canonical pairs that compose")
	for _, pair := range fixture.Compositions {
		p.Row()
		if got := NFC([]rune{pair[0], pair[1]}); !slices.Equal(got, []rune{pair[2]}) {
			p.Add("%U %U: got %U, want %U", pair[0], pair[1], got, pair[2])
		}
	}
	h := oracletest.NewMismatches(t, "Hangul")
	for _, row := range fixture.Hangul {
		h.Row()
		if got := NFC(row[0]); !slices.Equal(got, row[1]) {
			h.Add("NFC %U: got %U, want %U", row[0], got, row[1])
		}
		if got := NFD(row[0]); !slices.Equal(got, row[2]) {
			h.Add("NFD %U: got %U, want %U", row[0], got, row[2])
		}
	}
	// NFC returns text below U+0300 unchanged without normalizing it. That holds only while no such code
	// point changes under NFC and none is the second of a composing pair.
	for c := range nfc {
		if c < 0x300 {
			t.Errorf("U+%04X changes under NFC, so the shortcut below U+0300 is wrong", c)
		}
	}
	for _, pair := range fixture.Compositions {
		if pair[1] < 0x300 {
			t.Errorf("%U composes as a second character, so the shortcut below U+0300 is wrong", pair[1])
		}
	}
}

func TestNFCMarkPairs(t *testing.T) {
	m := oracletest.NewMismatches(t, "NFC of a and two combining marks")
	for _, line := range oracletest.Lines(t, "unicode/nfc_mark_pairs.jsonl.gz") {
		var row [3]json.RawMessage
		var first, second rune
		var want []rune
		if json.Unmarshal(line, &row) != nil || json.Unmarshal(row[0], &first) != nil ||
			json.Unmarshal(row[1], &second) != nil || json.Unmarshal(row[2], &want) != nil {
			t.Fatalf("bad row %s", line)
		}
		input := []rune{'a', first, second}
		if want == nil {
			want = input
		}
		m.Row()
		if got := NFC(input); !slices.Equal(got, want) {
			m.Add("a %U %U: got %U, want %U", first, second, got, want)
		}
	}
}

// The oracle's NFC strings never put two marks of one class after a base, or a mark between a base and a
// starter it composes with. These cases cover that; each expected value was read from CPython 3.14.6.
func TestNFCBlocking(t *testing.T) {
	for _, c := range []struct{ input, want []rune }{
		{[]rune{'a', 0x316, 0x325}, []rune{'a', 0x316, 0x325}},
		{[]rune{'a', 0x325, 0x316}, []rune{0x1E01, 0x316}},
		{[]rune{'a', 0x301, 0x301}, []rune{0xE1, 0x301}},
		{[]rune{'e', 0x304, 0x300}, []rune{0x1E15}},
		{[]rune{'E', 0x304, 0x300, 0x301}, []rune{0x1E14, 0x301}},
		{[]rune{0x1100, 0x1161, 0x11A8, 0x301}, []rune{0xAC01, 0x301}},
		{[]rune{0xB47, 0xB3E, 0xB57}, []rune{0xB4B, 0xB57}},
		{[]rune{'a', 0x328, 0x301, 0x303}, []rune{0x105, 0x301, 0x303}},
		{[]rune{0x3B1, 0x313, 0x301, 0x345}, []rune{0x1F84}},
		{[]rune{'o', 0x31B, 0x323, 0x302}, []rune{0x1EE3, 0x302}},
		{[]rune{0xC5, 0x301}, []rune{0x1FA}},
		{[]rune{'A', 0x30A, 0x323}, []rune{0x1EA0, 0x30A}},
	} {
		if got := NFC(c.input); !slices.Equal(got, c.want) {
			t.Errorf("NFC %U: got %U, want %U", c.input, got, c.want)
		}
	}
}

// NFC keeps a text canonically equivalent and is stable: NFD(NFC(x)) == NFD(x) and NFC(NFC(x)) == NFC(x),
// over seeded strings of composing characters and marks.
func TestNFCEquivalence(t *testing.T) {
	var pool []rune
	for _, e := range compositionEntries {
		pool = append(pool, e[0], e[1], e[2])
	}
	for _, r := range combiningRuns {
		pool = append(pool, r.lo, r.hi)
	}
	state := uint64(1)
	next := func(n int) int {
		state = state*6364136223846793005 + 1442695040888963407
		return int(state>>33) % n
	}
	for i := 0; i < 200_000; i++ {
		text := make([]rune, 1+next(6))
		for j := range text {
			text[j] = pool[next(len(pool))]
		}
		composed := NFC(text)
		if !slices.Equal(NFD(composed), NFD(text)) || !slices.Equal(NFC(composed), composed) {
			t.Fatalf("NFC %U = %U is not stable and equivalent", text, composed)
		}
	}
}
