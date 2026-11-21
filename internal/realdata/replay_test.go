package realdata

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// The package digest the oracle fixtures were made from (testdata/oracle/manifest.json).
const frozenPackage = "1861ed17a5a9139417617263cc29b5c181fefe1db121ee812dca455b3ac37e47"

func readGzipLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(unzipped)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func writeGzipLines(t *testing.T, path string, lines []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(file)
	io.WriteString(w, strings.Join(lines, "\n")+"\n")
	w.Close()
	file.Close()
}

func editMeta(t *testing.T, run Run, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(run.Out, metaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	edit(meta)
	data, _ = json.Marshal(meta)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Replay checks the records before it reports anything: the interpreter, the Unicode version, the frozen
// package, the functions of every item and the form of every digest, and that Python's outputs are the
// bytes its digests name. Nothing from a record reaches the report as text.
func TestCompareRefusesForgedRecords(t *testing.T) {
	for name, forge := range map[string]func(t *testing.T, run Run){
		"another interpreter": func(t *testing.T, run Run) {
			editMeta(t, run, func(m map[string]any) { m["python"] = "3.12.0 " + secret })
		},
		"another Unicode": func(t *testing.T, run Run) { editMeta(t, run, func(m map[string]any) { m["unicode"] = "15.1.0" }) },
		"another package": func(t *testing.T, run Run) { editMeta(t, run, func(m map[string]any) { m["package_sha256"] = secret }) },
		"a digest that is text": func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
			lines[0] = strings.Replace(lines[0], `"safe_text":"`, `"safe_text":"`+secret+`","x":"`, 1)
			writeGzipLines(t, filepath.Join(run.Out, digestsFile), lines)
		},
		"a function missing": func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
			var row []any
			json.Unmarshal([]byte(lines[1]), &row)
			delete(row[len(row)-1].(map[string]any), "units_opener")
			data, _ := json.Marshal(row)
			lines[1] = string(data)
			writeGzipLines(t, filepath.Join(run.Out, digestsFile), lines)
		},
		"a function added": func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
			var row []any
			json.Unmarshal([]byte(lines[2]), &row)
			row[len(row)-1].(map[string]any)["strict_json"] = strings.Repeat("a", 64)
			data, _ := json.Marshal(row)
			lines[2] = string(data)
			writeGzipLines(t, filepath.Join(run.Out, digestsFile), lines)
		},
		"an output that is not its digest": func(t *testing.T, run Run) {
			lines := readGzipLines(t, filepath.Join(run.Out, outputsFile))
			lines[0] = strings.Replace(lines[0], "Zanzibar", "Zanzibas", 1)
			writeGzipLines(t, filepath.Join(run.Out, outputsFile), lines)
		},
	} {
		t.Run(name, func(t *testing.T) {
			run, _, _ := localRun(t, texts...)
			forge(t, run)
			report, err := Compare(run)
			if err == nil {
				t.Fatalf("compared: %+v", report)
			}
			for _, word := range longWords(secret) {
				if strings.Contains(err.Error(), word) {
					t.Errorf("the refusal quotes the record: %v", err)
				}
			}
		})
	}
}

// The same checks hold when only the digests are left.
func TestCompareRefusesForgedDigestsAlone(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	os.Remove(filepath.Join(run.Out, outputsFile))
	lines := readGzipLines(t, filepath.Join(run.Out, digestsFile))
	lines[0] = strings.Replace(lines[0], `"safe_text":"`, `"safe_text":"`+secret+`","x":"`, 1)
	writeGzipLines(t, filepath.Join(run.Out, digestsFile), lines)
	if _, err := Compare(run); err == nil {
		t.Error("forged digests were compared")
	}
}

// A run with no items compares nothing, and that is a failure, not a pass.
func TestCompareRefusesNoItems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.jsonl")
	if err := os.WriteFile(path, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := LocalRun(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, digest := readItems(t, run.Items, true)
	writeOracle(t, run, nil, nil, digest)
	if report, err := Compare(run); err == nil {
		t.Errorf("a run with no items passed: %+v", report)
	}
}

// A converter that finds nothing it reads fails the conversion.
func TestConvertRefusesNothingFound(t *testing.T) {
	cache, d := placeFiles(t, "irc-raw", map[string][]byte{"elsewhere/a.raw.txt": []byte("[03:56] <a> hi\n")})
	if summary, err := Convert(cache, d); err == nil {
		t.Errorf("a conversion with no items passed: %+v", summary)
	}
}

// The oracle a replay accepts is the one the fixtures were made by.
func TestOracleIsTheFixtures(t *testing.T) {
	var manifest struct {
		Python  string `json:"python_version"`
		Unicode string `json:"unicode_version"`
		Package string `json:"package_sha256"`
	}
	oracletest.JSON(t, "manifest.json", &manifest)
	if manifest.Python != oraclePython || manifest.Unicode != oracleUnicode || manifest.Package != oraclePackage ||
		frozenPackage != oraclePackage {
		t.Errorf("the fixtures are %+v", manifest)
	}
}

// Every function is counted for every item: a port that gives fewer outputs than functions fails the run.
func TestCompareCountsEveryFunction(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	saved := portOutputs
	portOutputs = func(it Item) []string { return Outputs(it)[1:] }
	defer func() { portOutputs = saved }()
	if report, err := Compare(run); err == nil {
		t.Errorf("a run that skipped a function passed: %+v", report.Functions)
	}
}
