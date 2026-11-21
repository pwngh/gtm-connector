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
	Against       string      `json:"against"`
	Python        string      `json:"python"`
	PackageSHA256 string      `json:"package_sha256"`
	ItemsSHA256   string      `json:"items_sha256"`
	TextItems     int         `json:"text_items"`
	JSONItems     int         `json:"json_items"`
	Kinds         []KindCount `json:"kinds"`
	// Decoding counts the items file's lines: a line Python and Go decode differently is a difference here,
	// and its functions are not compared.
	Decoding    FunctionCount   `json:"decoding"`
	Functions   []FunctionCount `json:"functions"`
	Differences []Difference    `json:"differences"`
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

// The oracle the records must come from: the pinned interpreter and Unicode, and the package the oracle
// fixtures were made from (testdata/oracle/manifest.json). A test keeps these equal to the fixtures.
const (
	oraclePython  = "3.14.6"
	oracleUnicode = "16.0.0"
	oraclePackage = "1861ed17a5a9139417617263cc29b5c181fefe1db121ee812dca455b3ac37e47"
)

// portOutputs runs the Go port on an item; a test replaces it to check that every function is counted.
var portOutputs = Outputs

// Compare runs the Go port over a run's items and compares each output with the digest Python recorded.
// The records are checked first: they must come from the pinned oracle, be for this very items file, name
// exactly the functions each item has, and hold only SHA-256 digests; when Python's outputs are there, each
// must be the bytes its digest names, and each difference gets the first position where the outputs differ.
// A line Python and Go decode differently is a difference of its own, and the run goes on. Nothing a record
// holds reaches the report except digests that passed these checks. A run with no items is a failure, and a
// failed run leaves no report.
func Compare(run Run) (*Report, error) {
	started := time.Now()
	if run.open == nil {
		return nil, fmt.Errorf("%s: no results folder", run.Name)
	}
	root, err := run.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Remove(reportFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var meta OracleMeta
	if err := readJSON(root, metaFile, &meta); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w; run the oracle first", run.Name, ErrNoOracle)
		}
		return nil, fmt.Errorf("%s: %s is not readable", run.Name, metaFile)
	}
	if meta.Python != oraclePython || meta.Unicode != oracleUnicode || meta.PackageSHA256 != oraclePackage {
		return nil, fmt.Errorf("%s: the oracle's records were not made by CPython %s with Unicode %s from the "+
			"frozen package; run the oracle again", run.Name, oraclePython, oracleUnicode)
	}
	if !slices.Equal(meta.TextFunctions, TextFunctions) || !slices.Equal(meta.JSONFunctions, JSONFunctions) {
		return nil, fmt.Errorf("%s: the oracle ran other functions than this comparison knows; run it again", run.Name)
	}
	// Stale records are records of another items file; nothing else is called stale.
	if whole, err := itemsDigest(run, root); err != nil || whole != meta.ItemsSHA256 {
		return nil, fmt.Errorf("%s: the oracle's records are for other items; run the oracle again", run.Name)
	}
	items, err := openRunItems(run, root)
	if err != nil {
		return nil, err
	}
	defer items.Close()
	digests, err := openLines(root, digestsFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", run.Name, err)
	}
	defer digests.Close()
	report := &Report{Run: run.Name, Against: "digests", Python: oraclePython, PackageSHA256: oraclePackage,
		ItemsSHA256: meta.ItemsSHA256, Decoding: FunctionCount{Function: "item"}, Differences: []Difference{}}
	outputs, err := openLines(root, outputsFile)
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
	forged := fmt.Errorf("%s: the oracle's records are not in the form it writes; run the oracle again", run.Name)
	differ := func(d Difference, it Item) {
		if len(report.Differences) == maxListed {
			report.Unlisted++
			return
		}
		if !run.Local {
			d.File, d.Item, d.Kind = it.File, it.ID, it.Kind
		}
		report.Differences = append(report.Differences, d)
	}
	rows := 0
	for {
		it, err := items.Next()
		if err == io.EOF {
			break
		}
		var refused *ErrNotItem
		if err != nil && !errors.As(err, &refused) {
			return nil, err
		}
		line := it.Line
		if refused != nil {
			line = refused.Line
		}
		recorded, err := digests.next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s: Python and Go read the items file's lines differently at line %d", run.Name, line)
		}
		if err != nil {
			return nil, forged
		}
		rows++
		row, ok := decodeRow(recorded)
		if !ok {
			return nil, forged
		}
		if row.line != line {
			return nil, fmt.Errorf("%s: Python and Go read the items file's lines differently at line %d", run.Name, line)
		}
		var pythonOutputs map[string]json.RawMessage
		if outputs != nil {
			raw, err := outputs.next()
			if err != nil || json.Unmarshal(raw, &pythonOutputs) != nil || len(pythonOutputs) != len(row.sums) {
				return nil, forged
			}
			for name, output := range pythonOutputs {
				if Digest(string(output)) != row.sums[name] {
					return nil, fmt.Errorf("%s: Python's outputs are not the bytes its digests name (line %d); run the oracle again",
						run.Name, line)
				}
			}
		}
		report.Decoding.Items++
		if refused != nil {
			report.Decoding.Differences++
			differ(Difference{Line: line, Function: "item", Position: "Go refuses the line Python read",
				PythonSHA256: row.input}, Item{})
			continue
		}
		if row.input != it.InputSHA256() {
			report.Decoding.Differences++
			differ(Difference{Line: line, Function: "item", Position: "Python and Go decode the line differently",
				InputSHA256: it.InputSHA256(), PythonSHA256: row.input, GoSHA256: it.InputSHA256()}, it)
			continue
		}
		report.Decoding.Matches++
		names := Functions(it)
		if len(row.sums) != len(names) {
			return nil, forged
		}
		for _, name := range names {
			if _, ok := row.sums[name]; !ok {
				return nil, forged
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
		encodedAll := portOutputs(it)
		if len(encodedAll) != len(names) {
			return nil, fmt.Errorf("%s: the Go port gave %d outputs for %d functions", run.Name, len(encodedAll), len(names))
		}
		for i, name := range names {
			encoded := encodedAll[i]
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
			want, got := row.sums[name], Digest(encoded)
			if want == got {
				count.Matches++
				byKind.Matches++
				continue
			}
			count.Differences++
			byKind.Differences++
			kind.Differences++
			d := Difference{Line: it.Line, Function: name, InputSHA256: it.InputSHA256(), PythonSHA256: want,
				GoSHA256: got, Position: "unknown: only digests were recorded"}
			if raw, ok := pythonOutputs[name]; ok {
				d.Position = FirstDifference(raw, []byte(encoded))
			}
			differ(d, it)
		}
	}
	if _, err := digests.next(); err != io.EOF {
		return nil, fmt.Errorf("%s: the oracle's digests hold more rows than the items file has items", run.Name)
	}
	if outputs != nil {
		if _, err := outputs.next(); err != io.EOF {
			return nil, fmt.Errorf("%s: the oracle's outputs hold more rows than the items file has items", run.Name)
		}
	}
	if meta.Items != rows {
		return nil, fmt.Errorf("%s: oracle.json's item count is not the number of its digests' rows", run.Name)
	}
	if report.Decoding.Items == 0 {
		return nil, fmt.Errorf("%s: no items to compare", run.Name)
	}
	for _, name := range append(slices.Clone(TextFunctions), JSONFunctions...) {
		want := report.TextItems
		if slices.Contains(JSONFunctions, name) {
			want = report.JSONItems
		}
		if tally[name].Items != want {
			return nil, fmt.Errorf("%s: %s ran on %d of %d items", run.Name, name, tally[name].Items, want)
		}
		report.Functions = append(report.Functions, *tally[name])
	}
	for _, name := range append([]string{""}, Kinds...) {
		if k := kinds[name]; k != nil {
			report.Kinds = append(report.Kinds, *k)
		}
	}
	report.Seconds = time.Since(started).Seconds()
	return report, writeJSON(root, reportFile, report, 0o600)
}

// itemsDigest is the SHA-256 of a run's whole items file, uncompressed, as the oracle read it.
func itemsDigest(run Run, root *os.Root) (string, error) {
	items, err := openRunItems(run, root)
	if err != nil {
		return "", err
	}
	defer items.Close()
	if _, err := io.Copy(io.Discard, items.in); err != nil {
		return "", err
	}
	return items.SHA256(), nil
}

// ClearReport removes a run's report, so that a run that fails before its comparison leaves none behind.
func (run Run) ClearReport() error {
	if run.open == nil {
		return nil
	}
	root, err := run.open()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(reportFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// openRunItems opens a run's items: a local file where it is, or a dataset's items in its folder.
func openRunItems(run Run, root *os.Root) (*ItemReader, error) {
	if run.Local {
		return OpenItems(run.Items, true)
	}
	file, err := root.Open(itemsFile)
	if err != nil {
		return nil, err
	}
	return newItemReader(file, true, false)
}

// Total is the number of differences.
func (r *Report) Total() int {
	total := r.Decoding.Differences
	for _, f := range r.Functions {
		total += f.Differences
	}
	return total
}

// digestRow is one row of oracle-digests.jsonl.gz: [line, input SHA-256, {function: output SHA-256}].
type digestRow struct {
	line  int
	input string
	sums  map[string]string
}

// decodeRow reads a digests row whose digests are all SHA-256 digests.
func decodeRow(data []byte) (digestRow, bool) {
	var row digestRow
	var parts []json.RawMessage
	if json.Unmarshal(data, &parts) != nil || len(parts) != 3 || json.Unmarshal(parts[0], &row.line) != nil ||
		json.Unmarshal(parts[1], &row.input) != nil || json.Unmarshal(parts[2], &row.sums) != nil ||
		row.line < 1 || !digest.MatchString(row.input) {
		return digestRow{}, false
	}
	for _, sum := range row.sums {
		if !digest.MatchString(sum) {
			return digestRow{}, false
		}
	}
	return row, true
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

func openLines(root *os.Root, name string) (*lineReader, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%s is not gzip", name)
	}
	return &lineReader{file: file, zip: unzipped, in: bufio.NewReaderSize(unzipped, 1<<20)}, nil
}

func (r *lineReader) next() ([]byte, error) {
	line, err := r.in.ReadBytes('\n')
	switch {
	case err == io.EOF && len(line) == 0:
		return nil, io.EOF
	case err != nil && err != io.EOF:
		return nil, errors.New("unreadable")
	}
	return bytes.TrimSuffix(line, []byte("\n")), nil
}

func (r *lineReader) Close() error { return errors.Join(r.zip.Close(), r.file.Close()) }

func readJSON(root *os.Root, name string, value any) error {
	data, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("not UTF-8")
	}
	return json.Unmarshal(data, value)
}
