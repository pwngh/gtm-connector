package realdata

import (
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The canonical encoding is the oracle's own dumps: re-encoding each fixture line Python wrote gives the same
// bytes, so a digest Python records and one Go computes agree exactly when the outputs do.
func TestCanonicalIsOracleDumps(t *testing.T) {
	files := []string{"corpora/sentences.jsonl.gz", "corpora/safe_text.jsonl.gz", "scanners/inputs.jsonl.gz"}
	for _, name := range TextFunctions[5:] {
		files = append(files, "scanners/"+name+".jsonl.gz")
	}
	for _, file := range files {
		m := oracletest.NewMismatches(t, file)
		for i, line := range oracletest.Lines(t, file) {
			m.Row()
			value, err := strictjson.Loads(line)
			if err != nil {
				t.Fatalf("%s line %d: %v", file, i+1, err)
			}
			if got, err := canonical(value); err != nil || got != string(line) {
				m.Add("line %d: %v", i+1, err)
			}
		}
	}
}

func textItem(text string) Item { return Item{ID: "x", Text: text} }

func at(name string) int { return slices.Index(TextFunctions, name) }

// Each Go output has the shape and encoding the oracle fixtures record for the same function.
func TestOutputsMatchOracleFixtures(t *testing.T) {
	inputs := oracletest.Lines(t, "scanners/inputs.jsonl.gz")
	results := map[string][][]byte{}
	for _, name := range TextFunctions[5:] {
		results[name] = oracletest.Lines(t, "scanners/"+name+".jsonl.gz")
	}
	m := oracletest.NewMismatches(t, "scanner outputs")
	for i, line := range inputs {
		m.Row()
		var text string
		decode(t, line, &text)
		outputs := Outputs(textItem(text))
		for name, lines := range results {
			if outputs[at(name)] != string(lines[i]) {
				m.Add("input %d: %s", i, name)
			}
		}
	}
	// Enough rows of each corpus to see every shape; the evidence tests check every row.
	const rows = 5000
	safe := oracletest.NewMismatches(t, "safe_text outputs")
	for _, line := range oracletest.Lines(t, "corpora/safe_text.jsonl.gz")[:rows] {
		safe.Row()
		row := loadList(t, line)
		want, _ := canonical([]any{row[1], row[2]})
		if got := Outputs(textItem(row[0].(string)))[at("safe_text")]; got != want {
			safe.Add("%s", line)
		}
	}
	split := oracletest.NewMismatches(t, "sentences outputs")
	for _, line := range oracletest.Lines(t, "corpora/sentences.jsonl.gz")[:rows] {
		split.Row()
		row := loadList(t, line)
		if row[2] == nil {
			row[2] = row[1]
		}
		outputs := Outputs(textItem(row[0].(string)))
		without, _ := canonical(row[1])
		with, _ := canonical(row[2])
		if outputs[at("sentences")] != without || outputs[at("sentences_lines")] != with {
			split.Add("%s", line)
		}
	}
}

// The invented items in testdata/patterns.jsonl stand for text patterns real data showed: sentences over 300
// characters, character references, format characters other than ZWJ, control characters, a sentence split
// after a Spanish abbreviation, and line breaks inside a message. Every R0 function gives, for each, what
// Python recorded in corpora/patterns.json.
func TestInventedPatternsMatchTheOracle(t *testing.T) {
	items, _ := readItems(t, filepath.Join("testdata", "patterns.jsonl"), true)
	value, err := strictjson.Loads(oracletest.Read(t, "corpora/patterns.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases, _ := value.(*strictjson.Object).Get("cases")
	if len(cases.([]any)) != len(items) {
		t.Fatalf("%d items and %d recorded cases; regenerate the oracle fixtures", len(items), len(cases.([]any)))
	}
	m := oracletest.NewMismatches(t, "corpora/patterns.json")
	for i, it := range items {
		m.Row()
		recorded := cases.([]any)[i].(*strictjson.Object)
		id, _ := recorded.Get("id")
		text, _ := recorded.Get("text")
		if id != it.ID || text != it.Text {
			t.Fatalf("case %d is not item %s; regenerate the oracle fixtures", i, it.ID)
		}
		outputs, _ := recorded.Get("outputs")
		got := Outputs(it)
		for j, name := range TextFunctions {
			want, _ := outputs.(*strictjson.Object).Get(name)
			if encoded, err := canonical(want); err != nil || got[j] != encoded {
				m.Add("%s: %s", it.ID, name)
			}
		}
	}
}

// strict_json's outputs are the outcomes library/strict_json.json records.
func TestJSONOutputsMatchOracleFixtures(t *testing.T) {
	value, err := strictjson.Loads(oracletest.Read(t, "library/strict_json.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases, _ := value.(*strictjson.Object).Get("cases")
	m := oracletest.NewMismatches(t, "strict_json outputs")
	for _, c := range cases.([]any) {
		o := c.(*strictjson.Object)
		if kind, _ := o.Get("kind"); kind != "bytes" {
			continue
		}
		m.Row()
		encoded, _ := o.Get("hex")
		raw, err := hex.DecodeString(encoded.(string))
		if err != nil {
			t.Fatal(err)
		}
		plain := &strictjson.Object{}
		for i, key := range o.Keys {
			if key != "kind" && key != "hex" && key != "decimal" {
				plain.Set(key, o.Values[i])
			}
		}
		decimal, _ := o.Get("decimal")
		wantPlain, _ := canonical(plain)
		wantDecimal, _ := canonical(decimal)
		outputs := Outputs(Item{ID: "x", JSON: raw, IsJSON: true})
		label := encoded.(string)
		if len(label) > 40 {
			label = label[:40]
		}
		if outputs[0] != wantPlain {
			m.Add("%s: strict_json", label)
		}
		if outputs[1] != wantDecimal {
			m.Add("%s: strict_json_decimal", label)
		}
	}
}

// A panic in the port is caught and recorded without its value, which may hold the item's text.
func TestGuardHidesPanics(t *testing.T) {
	got, err := canonical(guard(func() (any, error) { panic(secret) }))
	if err != nil || got != `{"error":"go panic"}` || strings.Contains(got, "Zanzibar") {
		t.Errorf("got %s %v", got, err)
	}
}

func loadList(t *testing.T, line []byte) []any {
	t.Helper()
	value, err := strictjson.Loads(line)
	if err != nil {
		t.Fatal(err)
	}
	return value.([]any)
}

func decode(t *testing.T, line []byte, text *string) {
	t.Helper()
	value, err := strictjson.Loads(line)
	if err != nil {
		t.Fatal(err)
	}
	*text = value.(string)
}
