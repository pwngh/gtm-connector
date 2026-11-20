package realdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rewriteDigestRow(t *testing.T, run Run, row int, edit func([]any) []any) {
	t.Helper()
	lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
	var value []any
	if err := json.Unmarshal([]byte(lines[row]), &value); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(edit(value))
	lines[row] = string(data)
	writeGzipLines(t, filepath.Join(run.Out, digestsFile), lines)
}

// A line Python and Go decode differently is a located difference, not stale records, and the other items
// are still compared.
func TestCompareLocatesDecodingDifference(t *testing.T) {
	run, items, _ := localRun(t, texts...)
	rewriteDigestRow(t, run, 1, func(row []any) []any {
		row[len(row)-2] = strings.Repeat("0", 64) // the input's digest, as if Python decoded another text
		return row
	})
	report, err := Compare(run)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total() != 1 || len(report.Differences) != 1 || report.Differences[0].Line != items[1].Line ||
		report.Differences[0].Function != "item" {
		t.Errorf("%+v", report.Differences)
	}
	for _, f := range report.Functions[:len(TextFunctions)] {
		if f.Items != 2 {
			t.Errorf("%s compared %d items, not the 2 decoded alike", f.Function, f.Items)
		}
	}
}

// A failed comparison leaves no report behind: an earlier report that says no differences is removed.
func TestFailedCompareRemovesReport(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	if _, err := Compare(run); err != nil {
		t.Fatal(err)
	}
	editMeta(t, run, func(m map[string]any) { m["text_functions"] = []string{"safe_text"} })
	if _, err := Compare(run); err == nil {
		t.Fatal("compared")
	}
	if _, err := os.Lstat(filepath.Join(run.Out, reportFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an earlier report was left: %v", err)
	}
}

// Past the differences the report lists, the rest are counted but not listed, and the terminal says which.
func TestPrintReportCountsListed(t *testing.T) {
	r := &Report{Run: "x", Functions: []FunctionCount{{Function: "sentences", Items: 2000, Differences: 1746}},
		Differences: make([]Difference, maxListed), Unlisted: 746}
	var printed bytes.Buffer
	PrintReport(&printed, r, Run{Out: "out"})
	if !strings.Contains(printed.String(), "and 980 more listed in the report") ||
		!strings.Contains(printed.String(), "and 746 counted but not listed") {
		t.Errorf("%s", printed.String())
	}
}

// Each check on the records is the only one that catches its own fault.
func TestCompareStalenessGuards(t *testing.T) {
	for name, c := range map[string]struct {
		forge func(t *testing.T, run Run)
		want  string
	}{
		"the same texts under other IDs": {func(t *testing.T, run Run) {
			data, _ := os.ReadFile(run.Items)
			os.WriteFile(run.Items, bytes.ReplaceAll(data, []byte("msg-"), []byte("MSG-")), 0o600)
		}, "for other items"},
		"one digests row too many": {func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
			writeGzipLines(t, filepath.Join(run.Out, digestsFile), append(lines, lines[0]))
		}, "more rows than"},
		"one outputs row too many": {func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, outputsFile))
			writeGzipLines(t, filepath.Join(run.Out, outputsFile), append(lines, lines[0]))
		}, "more rows than"},
		"another item count": {func(t *testing.T, run Run) {
			editMeta(t, run, func(m map[string]any) { m["items"] = 4 })
		}, "item count"},
		"other functions": {func(t *testing.T, run Run) {
			editMeta(t, run, func(m map[string]any) { m["text_functions"] = []string{"safe_text"} })
		}, "other functions"},
		"a row for another line": {func(t *testing.T, run Run) {
			rewriteDigestRow(t, run, 2, func(row []any) []any { row[0] = 7.0; return row })
		}, "read the items file's lines differently"},
	} {
		t.Run(name, func(t *testing.T) {
			run, _, _ := localRun(t, texts...)
			c.forge(t, run)
			if _, err := Compare(run); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}
