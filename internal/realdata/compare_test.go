package realdata

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// writeOracle records outputs in a run's folder as tools/oracle/real.py does: one line of outputs and one of
// digests per item, then oracle.json.
func writeOracle(t *testing.T, run Run, items []Item, outputs [][]string, itemsSHA256 string) {
	t.Helper()
	var lines, digests bytes.Buffer
	for i, it := range items {
		names := Functions(it)
		pieces := make([]string, len(names))
		sums := &strictjson.Object{}
		for j, name := range names {
			pieces[j] = `"` + name + `":` + outputs[i][j]
			sums.Set(name, Digest(outputs[i][j]))
		}
		lines.WriteString("{" + strings.Join(pieces, ",") + "}\n")
		row, err := canonical([]any{it.InputSHA256(), sums})
		if err != nil {
			t.Fatal(err)
		}
		digests.WriteString(row + "\n")
	}
	if err := os.MkdirAll(run.Out, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{outputsFile: lines.Bytes(), digestsFile: digests.Bytes()} {
		var zipped bytes.Buffer
		w := gzip.NewWriter(&zipped)
		w.Write(data)
		w.Close()
		if err := os.WriteFile(filepath.Join(run.Out, name), zipped.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	meta := OracleMeta{Python: "3.14.6", Unicode: "16.0.0", PackageSHA256: strings.Repeat("ab", 32),
		ItemsSHA256: itemsSHA256, Items: len(items), TextFunctions: TextFunctions, JSONFunctions: JSONFunctions}
	if err := writeJSON(filepath.Join(run.Out, metaFile), meta, 0o600); err != nil {
		t.Fatal(err)
	}
}

// localRun writes private items to a file outside the repository and records Go's own outputs as the
// oracle's, so the run starts with no differences.
func localRun(t *testing.T, texts ...string) (Run, []Item, [][]string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.jsonl")
	var lines strings.Builder
	for i, text := range texts {
		line, err := Item{ID: "msg-" + string(rune('a'+i)), Text: text}.line()
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(line)
	}
	if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := LocalRun(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items, digest := readItems(t, run.Items, true)
	var outputs [][]string
	for _, it := range items {
		outputs = append(outputs, Outputs(it))
	}
	writeOracle(t, run, items, outputs, digest)
	return run, items, outputs
}

var texts = []string{
	"We agree. " + secret + " until Friday.",
	"<@U0ABC123> see https://deals.example/renewal?id=7 " + secret,
	secret + "\n\nNo. 14 is the clause.",
}

func TestCompareMatches(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	report, err := Compare(run)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 0 || report.TextItems != 3 || report.Against != "outputs" {
		t.Fatalf("%+v", report)
	}
	for _, f := range report.Functions {
		want := 3
		if strings.HasPrefix(f.Function, "strict_json") {
			want = 0
		}
		if f.Items != want || f.Matches != want {
			t.Errorf("%+v", f)
		}
	}
}

// A planted difference is found, located by path and code point, and reported without text, by line only in
// a local run.
func TestCompareReportsDifferences(t *testing.T) {
	run, items, outputs := localRun(t, texts...)
	changed := strings.Replace(outputs[1][at("sentences")], "Zanzibar", "Zanzibas", 1)
	if changed == outputs[1][at("sentences")] {
		t.Fatal("the change did not apply")
	}
	outputs[1][at("sentences")] = changed
	outputs[2][at("units_sentence_end")] = "[]"
	_, digest := readItems(t, run.Items, true)
	writeOracle(t, run, items, outputs, digest)
	report, err := Compare(run)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 2 || len(report.Differences) != 2 {
		t.Fatalf("%+v", report)
	}
	first, second := report.Differences[0], report.Differences[1]
	offset := strings.Index(texts[1], "Zanzibar") + len("Zanziba")
	wantFirst := Difference{Line: 2, Function: "sentences", InputSHA256: items[1].InputSHA256(),
		PythonSHA256: Digest(changed), GoSHA256: Digest(Outputs(items[1])[at("sentences")]),
		Position: "output[0]: code point " + strconv.Itoa(offset) + " (Python's string has " + strconv.Itoa(len([]rune(texts[1]))) +
			" code points, Go's " + strconv.Itoa(len([]rune(texts[1]))) + ")"}
	if first != wantFirst {
		t.Errorf("got  %+v\nwant %+v", first, wantFirst)
	}
	if second.Line != 3 || second.Function != "units_sentence_end" ||
		second.Position != "output: Python has 0 elements, Go 1" {
		t.Errorf("%+v", second)
	}
	var printed bytes.Buffer
	PrintReport(&printed, report, run)
	saved, err := os.ReadFile(filepath.Join(run.Out, reportFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{printed.String(), string(saved)} {
		for _, word := range append(longWords(secret), "msg-", "deals.example") {
			if strings.Contains(out, word) {
				t.Errorf("the report holds %q:\n%s", word, out)
			}
		}
	}
	if !strings.Contains(printed.String(), "line 2 sentences at output[0]: code point") {
		t.Errorf("printed:\n%s", printed.String())
	}
	// With only the digests, the difference is still found, without a position.
	if err := os.Remove(filepath.Join(run.Out, outputsFile)); err != nil {
		t.Fatal(err)
	}
	report, err = Compare(run)
	if err != nil || report.Against != "digests" || report.Total() != 2 ||
		!strings.HasPrefix(report.Differences[0].Position, "unknown") {
		t.Fatalf("%v %+v", err, report)
	}
}

// Records made for other items are refused, never compared.
func TestCompareRefusesStaleRecords(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	if err := os.WriteFile(run.Items, []byte(`{"id": "a", "text": "something else"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(run); err == nil || !strings.Contains(err.Error(), "other items") {
		t.Errorf("got %v", err)
	}
	other, _, _ := localRun(t, texts...)
	extra := append([]byte(nil), mustRead(t, other.Items)...)
	extra = append(extra, []byte(`{"id": "z", "text": "one more"}`+"\n")...)
	if err := os.WriteFile(other.Items, extra, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(other); err == nil || !strings.Contains(err.Error(), "other items") {
		t.Errorf("an added item: %v", err)
	}
	missing := Run{Name: "local", Items: other.Items, Out: filepath.Join(t.TempDir(), "none"), Local: true}
	if _, err := Compare(missing); !errors.Is(err, ErrNoOracle) {
		t.Errorf("no oracle: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A dataset run names each difference by its file, item ID and kind, and splits its counts by kind.
func TestCompareNamesDatasetItems(t *testing.T) {
	order := []string{"top/raw/channels.json", "top/raw/general/2016-01-07.json"}
	cache, d := placeFiles(t, "slack-export", map[string][]byte{"commit.tar.gz": buildTarGz(t, order, slackFiles)})
	if _, err := Convert(cache, d); err != nil {
		t.Fatal(err)
	}
	run := DatasetRun(cache, d)
	items, digest := readItems(t, run.Items, false)
	var outputs [][]string
	for _, it := range items {
		outputs = append(outputs, Outputs(it))
	}
	outputs[0][0] = `{"error":"JSONDecodeError","message":"x"}`
	writeOracle(t, run, items, outputs, digest)
	report, err := Compare(run)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 1 || report.JSONItems != 2 || report.TextItems != 2 {
		t.Fatalf("%+v", report)
	}
	got := report.Differences[0]
	if got.Item != "top/raw/channels.json" || got.Kind != "role-played" || got.Function != "strict_json" ||
		got.Position != "output: only Python has a member error" {
		t.Errorf("%+v", got)
	}
	if len(report.Kinds) != 1 || report.Kinds[0] != (KindCount{Kind: "role-played", TextItems: 2, JSONItems: 2,
		Outputs: 2*len(TextFunctions) + 2*len(JSONFunctions), Differences: 1}) {
		t.Errorf("%+v", report.Kinds)
	}
	if byKind := report.Functions[len(TextFunctions)].ByKind["role-played"]; *byKind != (Tally{Items: 2, Matches: 1, Differences: 1}) {
		t.Errorf("%+v", byKind)
	}
}

func TestLocalRun(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	inRepo := filepath.Join(repo, "tests", "messages.jsonl")
	os.MkdirAll(filepath.Dir(inRepo), 0o755)
	os.WriteFile(inRepo, []byte("{}"), 0o600)
	if _, err := LocalRun(inRepo, repo); err == nil {
		t.Error("a file inside the repository was accepted")
	}
	link := filepath.Join(outside, "link.jsonl")
	os.Symlink(inRepo, link)
	if _, err := LocalRun(link, repo); err == nil {
		t.Error("a link into the repository was accepted")
	}
	path := filepath.Join(outside, "messages.jsonl")
	os.WriteFile(path, []byte("{}"), 0o600)
	run, err := LocalRun(path, repo)
	if err != nil || !run.Local || run.Out != run.Items+".r0" {
		t.Fatalf("%+v %v", run, err)
	}
	os.WriteFile(path+".r0", nil, 0o600)
	if _, err := LocalRun(path, repo); err == nil {
		t.Error("a file named like the results folder was accepted")
	}
	if _, err := LocalRun(outside, repo); err == nil {
		t.Error("a folder was accepted")
	}
}
