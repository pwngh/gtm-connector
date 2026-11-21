package realdata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordedRun converts a dataset of two Slack messages and two JSON documents and records Go's own digests
// of it, as the oracle's would be when they agree.
func recordedRun(t *testing.T) (Run, *Recorded) {
	t.Helper()
	order := []string{"top/raw/channels.json", "top/raw/general/2016-01-07.json"}
	cache, d := placeFiles(t, "slack-export", map[string][]byte{"commit.tar.gz": buildTarGz(t, order, slackFiles)})
	if _, err := Convert(cache, d); err != nil {
		t.Fatal(err)
	}
	run := DatasetRun(cache, d)
	items, digest := readItems(t, run.Items, false)
	rec := newRecorder(d.ID)
	for _, it := range items {
		sums := map[string]string{}
		for i, output := range Outputs(it) {
			sums[Functions(it)[i]] = Digest(output)
		}
		if err := rec.add(it.Line, it.InputSHA256(), sums); err != nil {
			t.Fatal(err)
		}
	}
	return run, rec.finish(digest)
}

func TestCompareRecordedMatches(t *testing.T) {
	run, recorded := recordedRun(t)
	report, err := CompareRecorded(run, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 0 || report.TextItems != 2 || report.JSONItems != 2 || report.Against != "recorded digests" ||
		report.Decoding.Matches != 4 {
		t.Fatalf("%+v", report)
	}
	for _, f := range report.Functions {
		if f.Items != 2 || f.Matches != 2 {
			t.Errorf("%+v", f)
		}
	}
	if _, err := os.Stat(filepath.Join(run.Out, reportFile)); err != nil {
		t.Error(err)
	}
}

// A block whose digest differs is a difference of its function, located by its lines, with each of its
// items counted; nothing in the report is text.
func TestCompareRecordedLocatesBlock(t *testing.T) {
	run, recorded := recordedRun(t)
	recorded.Functions[at("sentences")].Blocks[0] = strings.Repeat("0", 64)
	recorded.Decoding[0] = strings.Repeat("1", 64)
	report, err := CompareRecorded(run, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 6 || len(report.Differences) != 2 {
		t.Fatalf("%+v", report)
	}
	sentences := report.Functions[at("sentences")]
	if sentences.Differences != 2 || sentences.Matches != 0 || report.Decoding.Differences != 4 {
		t.Errorf("%+v %+v", sentences, report.Decoding)
	}
	first, second := report.Differences[0], report.Differences[1]
	if first.Function != "item" || second.Function != "sentences" ||
		!strings.HasPrefix(second.Position, "lines 1 to 4 (items 1 to 4)") {
		t.Errorf("%+v", report.Differences)
	}
	data, _ := os.ReadFile(filepath.Join(run.Out, reportFile))
	for _, word := range []string{"Zanzibar", "Quarterly"} {
		if strings.Contains(string(data), word) {
			t.Errorf("%q is in the report", word)
		}
	}
}

// Recorded digests of other items are refused, and leave no report.
func TestCompareRecordedRefusesOtherItems(t *testing.T) {
	run, recorded := recordedRun(t)
	other := *recorded
	other.ItemsSHA256 = strings.Repeat("a", 64)
	if _, err := CompareRecorded(run, &other); err == nil || !strings.Contains(err.Error(), "other items") {
		t.Errorf("other items: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.Out, reportFile)); !os.IsNotExist(err) {
		t.Errorf("a failed comparison left a report: %v", err)
	}
}

// The recorded digests are read only in their form: from the frozen oracle, for this dataset, with the
// functions this comparison knows and one digest per block.
func TestParseRecordedRefusesOtherForms(t *testing.T) {
	_, recorded := recordedRun(t)
	for name, edit := range map[string]func(*Recorded){
		"another dataset":  func(r *Recorded) { r.Dataset = "other" },
		"another package":  func(r *Recorded) { r.PackageSHA256 = strings.Repeat("b", 64) },
		"another Python":   func(r *Recorded) { r.Python = "3.12.0" },
		"another block":    func(r *Recorded) { r.BlockItems = 10 },
		"a function less":  func(r *Recorded) { r.Functions = r.Functions[1:] },
		"a block too many": func(r *Recorded) { r.Decoding = append(r.Decoding, strings.Repeat("c", 64)) },
		"text":             func(r *Recorded) { r.Functions[0].Blocks[0] = secret },
	} {
		copied := *recorded
		copied.Decoding = slices.Clone(recorded.Decoding)
		copied.Functions = slices.Clone(recorded.Functions)
		copied.Functions[0].Blocks = slices.Clone(recorded.Functions[0].Blocks)
		edit(&copied)
		data, _ := json.Marshal(copied)
		if _, err := ParseRecorded("set-1", data); err == nil || strings.Contains(err.Error(), "Zanzibar") {
			t.Errorf("%s: %v", name, err)
		}
	}
	data, _ := json.Marshal(recorded)
	if _, err := ParseRecorded("set-1", data); err != nil {
		t.Errorf("the recorded digests as made: %v", err)
	}
}

// Every pinned dataset with a converter that runs by default has recorded digests built in, each readable.
func TestEveryDefaultDatasetIsRecorded(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	datasets, err := m.Select(nil)
	if err != nil {
		t.Fatal(err)
	}
	recorded := 0
	for _, d := range datasets {
		if d.Converter == "" || !d.Pinned() {
			continue
		}
		if _, err := LoadRecorded(d.ID); err != nil {
			t.Errorf("%s: %v", d.ID, err)
		}
		recorded++
	}
	entries, err := embedded.ReadDir("recorded")
	if err != nil || len(entries) != recorded || recorded == 0 {
		t.Errorf("%d recorded files for %d datasets: %v", len(entries), recorded, err)
	}
}
