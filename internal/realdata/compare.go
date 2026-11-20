package realdata

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

// OracleMeta is oracle.json, which tools/oracle/real.py writes beside its outputs.
type OracleMeta struct {
	Python        string   `json:"python"`
	Unicode       string   `json:"unicode"`
	PackageSHA256 string   `json:"package_sha256"`
	ItemsSHA256   string   `json:"items_sha256"`
	Items         int      `json:"items"`
	TextFunctions []string `json:"text_functions"`
	JSONFunctions []string `json:"json_functions"`
}

// Report is what one comparison found. It holds counts, positions and digests, never text.
type Report struct {
	Run string `json:"run"`
	// Against is "outputs" when Python's outputs were there to locate each difference, or "digests" when
	// only their digests were (after the Python package is gone).
	Against       string          `json:"against"`
	Python        string          `json:"python"`
	PackageSHA256 string          `json:"package_sha256"`
	ItemsSHA256   string          `json:"items_sha256"`
	TextItems     int             `json:"text_items"`
	JSONItems     int             `json:"json_items"`
	Kinds         []KindCount     `json:"kinds"`
	Functions     []FunctionCount `json:"functions"`
	Differences   []Difference    `json:"differences"`
	// Unlisted counts the differences past the first maxListed, which are counted but not listed.
	Unlisted int     `json:"unlisted_differences"`
	Seconds  float64 `json:"seconds"`
}

// FunctionCount is one function's tally over a run. NonEmpty counts the items whose Go output is not [] or
// null, which shows how much of the run reached the function's matches.
type FunctionCount struct {
	Function    string            `json:"function"`
	Items       int               `json:"items"`
	NonEmpty    int               `json:"non_empty"`
	Matches     int               `json:"matches"`
	Differences int               `json:"differences"`
	ByKind      map[string]*Tally `json:"by_kind,omitempty"`
}

// Tally is one function's counts over the items of one kind.
type Tally struct {
	Items       int `json:"items"`
	Matches     int `json:"matches"`
	Differences int `json:"differences"`
}

// KindCount is a run's items and differences of one kind: natural, role-played, elicited or invented.
type KindCount struct {
	Kind        string `json:"kind"`
	TextItems   int    `json:"text_items"`
	JSONItems   int    `json:"json_items"`
	Outputs     int    `json:"outputs"`
	Differences int    `json:"differences"`
}

// Difference is one output that differs. A local run names its item by line only.
type Difference struct {
	File         string `json:"file,omitempty"`
	Item         string `json:"item,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Line         int    `json:"line"`
	Function     string `json:"function"`
	Position     string `json:"position"`
	InputSHA256  string `json:"input_sha256"`
	PythonSHA256 string `json:"python_sha256"`
	GoSHA256     string `json:"go_sha256"`
}

const maxListed = 1000

// ErrNoOracle is a run with no recorded oracle digests.
var ErrNoOracle = errors.New("no oracle digests")

// Compare runs the Go port over a run's items and compares each output with the digest Python recorded.
// When Python's outputs are there too, each difference gets the first position where the outputs differ.
func Compare(run Run) (*Report, error) {
	started := time.Now()
	var meta OracleMeta
	if err := readJSON(filepath.Join(run.Out, metaFile), &meta); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w; run the oracle first", run.Name, ErrNoOracle)
		}
		return nil, fmt.Errorf("%s: %s is not readable", run.Name, metaFile)
	}
	if !slices.Equal(meta.TextFunctions, TextFunctions) || !slices.Equal(meta.JSONFunctions, JSONFunctions) {
		return nil, fmt.Errorf("%s: the oracle ran other functions than this comparison knows; run it again", run.Name)
	}
	items, err := OpenItems(run.Items, run.Local)
	if err != nil {
		return nil, err
	}
	defer items.Close()
	digests, err := openLines(filepath.Join(run.Out, digestsFile))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", run.Name, err)
	}
	defer digests.Close()
	report := &Report{Run: run.Name, Against: "digests", Python: meta.Python, PackageSHA256: meta.PackageSHA256,
		ItemsSHA256: meta.ItemsSHA256, Differences: []Difference{}}
	outputs, err := openLines(filepath.Join(run.Out, outputsFile))
	if err == nil {
		defer outputs.Close()
		report.Against = "outputs"
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", run.Name, err)
	}
	tally := map[string]*FunctionCount{}
	for _, name := range append(slices.Clone(TextFunctions), JSONFunctions...) {
		tally[name] = &FunctionCount{Function: name, ByKind: map[string]*Tally{}}
	}
	kinds := map[string]*KindCount{}
	stale := fmt.Errorf("%s: the oracle's records are for other items; run the oracle again", run.Name)
	for n := 1; ; n++ {
		it, err := items.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		recorded, err := digests.next()
		if err != nil {
			return nil, stale
		}
		input, sums, ok := decodeDigests(recorded)
		if !ok || input != it.InputSHA256() {
			return nil, stale
		}
		var pythonOutputs map[string]json.RawMessage
		if outputs != nil {
			line, err := outputs.next()
			if err != nil || json.Unmarshal(line, &pythonOutputs) != nil {
				return nil, fmt.Errorf("%s: %s line %d is not readable", run.Name, outputsFile, n)
			}
		}
		kind := kinds[it.Kind]
		if kind == nil {
			kind = &KindCount{Kind: it.Kind}
			kinds[it.Kind] = kind
		}
		if it.IsJSON {
			report.JSONItems++
			kind.JSONItems++
		} else {
			report.TextItems++
			kind.TextItems++
		}
		for i, encoded := range Outputs(it) {
			name := Functions(it)[i]
			count := tally[name]
			byKind := count.ByKind[it.Kind]
			if byKind == nil {
				byKind = &Tally{}
				count.ByKind[it.Kind] = byKind
			}
			count.Items++
			byKind.Items++
			kind.Outputs++
			if encoded != "[]" && encoded != "null" {
				count.NonEmpty++
			}
			want, ok := sums[name]
			got := Digest(encoded)
			if ok && want == got {
				count.Matches++
				byKind.Matches++
				continue
			}
			count.Differences++
			byKind.Differences++
			kind.Differences++
			if len(report.Differences) == maxListed {
				report.Unlisted++
				continue
			}
			d := Difference{Line: it.Line, Function: name, InputSHA256: it.InputSHA256(), PythonSHA256: want,
				GoSHA256: got, Position: "unknown: only digests were recorded"}
			if !run.Local {
				d.File, d.Item, d.Kind = it.File, it.ID, it.Kind
			}
			if raw, ok := pythonOutputs[name]; ok {
				d.Position = FirstDifference(raw, []byte(encoded))
			}
			report.Differences = append(report.Differences, d)
		}
	}
	if _, err := digests.next(); err != io.EOF {
		return nil, stale
	}
	if outputs != nil {
		if _, err := outputs.next(); err != io.EOF {
			return nil, stale
		}
	}
	if items.SHA256() != meta.ItemsSHA256 || report.TextItems+report.JSONItems != meta.Items {
		return nil, stale
	}
	for _, name := range append(slices.Clone(TextFunctions), JSONFunctions...) {
		report.Functions = append(report.Functions, *tally[name])
	}
	for _, name := range append([]string{""}, Kinds...) {
		if k := kinds[name]; k != nil {
			report.Kinds = append(report.Kinds, *k)
		}
	}
	report.Seconds = time.Since(started).Seconds()
	return report, writeJSON(filepath.Join(run.Out, reportFile), report, 0o600)
}

// Total is the number of differences.
func (r *Report) Total() int {
	total := 0
	for _, f := range r.Functions {
		total += f.Differences
	}
	return total
}

// decodeDigests reads a digests line: [input SHA-256, {function: output SHA-256}].
func decodeDigests(line []byte) (input string, outputs map[string]string, ok bool) {
	var pair []json.RawMessage
	ok = json.Unmarshal(line, &pair) == nil && len(pair) == 2 && json.Unmarshal(pair[0], &input) == nil &&
		json.Unmarshal(pair[1], &outputs) == nil
	return input, outputs, ok
}

// FirstDifference says where two outputs first differ, by path and code point, with no text: a path such as
// [2][0], a code point offset into a string, a count of elements, or two numbers (positions or counts).
func FirstDifference(python, goOutput []byte) string {
	a, errA := decodeValue(python)
	b, errB := decodeValue(goOutput)
	if errA != nil || errB != nil {
		return "not comparable: an output is not JSON"
	}
	if found := firstDifference("output", a, b); found != "" {
		return found
	}
	return "the values are equal; their encodings differ"
}

func decodeValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	err := decoder.Decode(&value)
	return value, err
}

func firstDifference(path string, a, b any) string {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok {
			break
		}
		for i := 0; i < len(x) && i < len(y); i++ {
			if found := firstDifference(fmt.Sprintf("%s[%d]", path, i), x[i], y[i]); found != "" {
				return found
			}
		}
		if len(x) != len(y) {
			return fmt.Sprintf("%s: Python has %d elements, Go %d", path, len(x), len(y))
		}
		return ""
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		var sorted []string
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			// Output keys are the comparison's own words (ok, error, message, decimal), never source text.
			kx, inX := x[k]
			ky, inY := y[k]
			if !inX || !inY {
				return fmt.Sprintf("%s: only %s has a member %s", path, map[bool]string{true: "Python", false: "Go"}[inX],
					safeKey(k))
			}
			if found := firstDifference(path+"."+safeKey(k), kx, ky); found != "" {
				return found
			}
		}
		return ""
	case string:
		y, ok := b.(string)
		if !ok {
			break
		}
		if x == y {
			return ""
		}
		rx, ry := []rune(x), []rune(y)
		i := 0
		for i < len(rx) && i < len(ry) && rx[i] == ry[i] {
			i++
		}
		return fmt.Sprintf("%s: code point %d (Python's string has %d code points, Go's %d)", path, i, len(rx), len(ry))
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			break
		}
		if x == y {
			return ""
		}
		return fmt.Sprintf("%s: Python %s, Go %s", path, x, y)
	default:
		if a == b {
			return ""
		}
	}
	return fmt.Sprintf("%s: Python gives %s, Go %s", path, kind(a), kind(b))
}

func safeKey(k string) string {
	switch k {
	case "ok", "error", "message", "decimal":
		return k
	}
	return "(another key)"
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "another value"
}

// lineReader reads a gzipped JSON Lines file.
type lineReader struct {
	file *os.File
	zip  *gzip.Reader
	in   *bufio.Reader
}

func openLines(path string) (*lineReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%s is not gzip", filepath.Base(path))
	}
	return &lineReader{file: file, zip: unzipped, in: bufio.NewReaderSize(unzipped, 1<<20)}, nil
}

func (r *lineReader) next() ([]byte, error) {
	line, err := r.in.ReadBytes('\n')
	if len(line) == 0 && err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, errors.New("unreadable")
	}
	return bytes.TrimSuffix(line, []byte("\n")), nil
}

func (r *lineReader) Close() error { return errors.Join(r.zip.Close(), r.file.Close()) }

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("not UTF-8")
	}
	return json.Unmarshal(data, value)
}
