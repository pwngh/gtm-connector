package realdata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// BlockItems is how many items of a run one recorded block digest covers.
const BlockItems = 8192

// Recorded is what the Python oracle gave for one public dataset, kept in the repository as
// internal/realdata/recorded/ID.json so that the Go port is compared with it without Python. It holds
// counts and SHA-256 digests only, never text.
//
// The items are cut into blocks of BlockItems, in the items file's order. A block's decoding digest is the
// SHA-256 of one row "LINE INPUT\n" for each of its items, where INPUT is the SHA-256 of the item's text or
// document. A function's block digest is the SHA-256 of one row "LINE OUTPUT\n" for each item of the block
// the function runs on, where OUTPUT is the SHA-256 of the function's output in its canonical encoding. A
// difference is located to a function and a block.
type Recorded struct {
	Dataset       string             `json:"dataset"`
	Python        string             `json:"python"`
	Unicode       string             `json:"unicode"`
	PackageSHA256 string             `json:"package_sha256"`
	ItemsSHA256   string             `json:"items_sha256"`
	Items         int                `json:"items"`
	BlockItems    int                `json:"block_items"`
	Decoding      []string           `json:"decoding"`
	Functions     []RecordedFunction `json:"functions"`
}

// RecordedFunction is one function's item count and block digests, in TextFunctions then JSONFunctions order.
type RecordedFunction struct {
	Function string   `json:"function"`
	Items    int      `json:"items"`
	Blocks   []string `json:"blocks"`
}

// ErrNotRecorded is a dataset with no recorded digests in the repository.
var ErrNotRecorded = errors.New("no recorded digests")

// LoadRecorded reads a dataset's recorded digests built into this program, and checks their form.
func LoadRecorded(id string) (*Recorded, error) {
	data, err := embedded.ReadFile("recorded/" + id + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", id, ErrNotRecorded)
	}
	if err != nil {
		return nil, err
	}
	return ParseRecorded(id, data)
}

// ParseRecorded reads one dataset's recorded digests: from the pinned oracle, for the functions this
// comparison knows, with one SHA-256 digest for each block.
func ParseRecorded(id string, data []byte) (*Recorded, error) {
	var r Recorded
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil || decoder.More() {
		return nil, fmt.Errorf("%s: the recorded digests are not in their form", id)
	}
	bad := func(what string) error { return fmt.Errorf("%s: the recorded digests %s", id, what) }
	switch {
	case r.Dataset != id:
		return nil, bad("name another dataset")
	case r.Python != oraclePython || r.Unicode != oracleUnicode || r.PackageSHA256 != oraclePackage:
		return nil, bad(fmt.Sprintf("were not made by CPython %s with Unicode %s from the frozen package",
			oraclePython, oracleUnicode))
	case !digest.MatchString(r.ItemsSHA256) || r.Items < 1 || r.BlockItems != BlockItems:
		return nil, bad("hold no items or another block size")
	}
	blocks := (r.Items + BlockItems - 1) / BlockItems
	names := append(slices.Clone(TextFunctions), JSONFunctions...)
	if len(r.Functions) != len(names) {
		return nil, bad("name other functions than this comparison knows")
	}
	lists := [][]string{r.Decoding}
	for i, f := range r.Functions {
		if f.Function != names[i] || f.Items < 0 || f.Items > r.Items {
			return nil, bad("name other functions than this comparison knows")
		}
		lists = append(lists, f.Blocks)
	}
	for _, list := range lists {
		if len(list) != blocks {
			return nil, bad("hold another number of blocks than their items")
		}
		for _, sum := range list {
			if !digest.MatchString(sum) {
				return nil, bad("hold a value that is not a SHA-256 digest")
			}
		}
	}
	return &r, nil
}

// recorder cuts the rows of a run into blocks and digests them as Recorded describes.
type recorder struct {
	r         Recorded
	rows      int
	decoding  hash.Hash
	functions []hash.Hash
	index     map[string]int
}

func newRecorder(dataset string) *recorder {
	rec := &recorder{r: Recorded{Dataset: dataset, Python: oraclePython, Unicode: oracleUnicode,
		PackageSHA256: oraclePackage, BlockItems: BlockItems, Decoding: []string{}}, index: map[string]int{}}
	for i, name := range append(slices.Clone(TextFunctions), JSONFunctions...) {
		rec.r.Functions = append(rec.r.Functions, RecordedFunction{Function: name, Blocks: []string{}})
		rec.index[name] = i
	}
	rec.start()
	return rec
}

func (rec *recorder) start() {
	rec.decoding = sha256.New()
	rec.functions = make([]hash.Hash, len(rec.r.Functions))
	for i := range rec.functions {
		rec.functions[i] = sha256.New()
	}
}

func (rec *recorder) flush() {
	rec.r.Decoding = append(rec.r.Decoding, hex.EncodeToString(rec.decoding.Sum(nil)))
	for i, h := range rec.functions {
		rec.r.Functions[i].Blocks = append(rec.r.Functions[i].Blocks, hex.EncodeToString(h.Sum(nil)))
	}
	rec.start()
}

// add records one item: its line, its input's digest, and each function's output digest. A function this
// comparison does not know is an error.
func (rec *recorder) add(line int, input string, outputs map[string]string) error {
	if rec.rows > 0 && rec.rows%BlockItems == 0 {
		rec.flush()
	}
	rec.rows++
	prefix := strconv.Itoa(line) + " "
	io.WriteString(rec.decoding, prefix+input+"\n")
	for name, sum := range outputs {
		i, ok := rec.index[name]
		if !ok {
			return fmt.Errorf("no function %q", name)
		}
		rec.r.Functions[i].Items++
		io.WriteString(rec.functions[i], prefix+sum+"\n")
	}
	return nil
}

// finish gives the digests of the rows added, for an items file of the given digest.
func (rec *recorder) finish(itemsSHA256 string) *Recorded {
	if rec.rows%BlockItems != 0 {
		rec.flush()
	}
	rec.r.Items, rec.r.ItemsSHA256 = rec.rows, itemsSHA256
	return &rec.r
}

// failedOutputs are the outputs the Go port gives for a function that returned an error or panicked.
var failedOutputs = func() map[string]bool {
	set := map[string]bool{}
	for _, kind := range []string{"go error", "go panic"} {
		encoded, _ := canonical(failed(kind))
		set[encoded] = true
	}
	return set
}()

// WriteRecorded writes, into dir, the dataset's recorded digests with the block digests and item counts of
// the named functions taken from what this comparison made, and every other function's kept as recorded. It
// refuses a comparison where Go decodes an item differently, a function it does not know, and a function
// whose output is an error or a panic for any item, so that only what a function gives can change.
func (r *Report) WriteRecorded(dir string, functions []string) error {
	if r.made == nil || r.recorded == nil || r.Decoding.Differences > 0 {
		return fmt.Errorf("%s: only a comparison whose items all decode alike writes recorded digests", r.Run)
	}
	written := *r.recorded
	written.Functions = slices.Clone(r.recorded.Functions)
	for _, name := range functions {
		named := func(f RecordedFunction) bool { return f.Function == name }
		i, j := slices.IndexFunc(written.Functions, named), slices.IndexFunc(r.made.Functions, named)
		if i < 0 || j < 0 {
			return fmt.Errorf("%s: no function %q", r.Run, name)
		}
		if n := r.failed[name]; n > 0 {
			return fmt.Errorf("%s: %s gives an error or a panic for %d items, which is never recorded", r.Run, name, n)
		}
		written.Functions[i] = r.made.Functions[j]
	}
	data, err := json.MarshalIndent(written, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, written.Dataset+".json"), append(data, '\n'), 0o644)
}

// Kept is the differences of a comparison outside these functions: of the items' decoding and of every
// other function. WriteRecorded leaves those as they were.
func (r *Report) Kept(functions []string) int {
	total := r.Decoding.Differences
	for _, f := range r.Functions {
		if !slices.Contains(functions, f.Function) {
			total += f.Differences
		}
	}
	return total
}

// CompareRecorded runs the Go port over a dataset run's items and compares its block digests with the
// recorded ones. The recorded digests must be for this very items file. A function's matches and
// differences count items: every item of a block whose digest differs counts as a difference, and each such
// block is listed once with its first and last line. A failed run leaves no report.
func CompareRecorded(run Run, want *Recorded) (*Report, error) {
	started := time.Now()
	if run.open == nil {
		return nil, fmt.Errorf("%s: no results folder", run.Name)
	}
	root, err := run.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Remove(reportFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	items, err := openRunItems(run, root)
	if err != nil {
		return nil, err
	}
	defer items.Close()
	report := &Report{Run: run.Name, Against: "recorded digests", Python: want.Python,
		PackageSHA256: want.PackageSHA256, ItemsSHA256: want.ItemsSHA256, Decoding: FunctionCount{Function: "item"},
		Differences: []Difference{}, recorded: want, failed: map[string]int{}}
	got := newRecorder(want.Dataset)
	tally := map[string]*FunctionCount{}
	for _, f := range got.r.Functions {
		tally[f.Function] = &FunctionCount{Function: f.Function, ByKind: map[string]*Tally{}}
	}
	kinds := map[string]*KindCount{}
	// Each row's line, kind and functions, to count the items of a block that differs.
	type row struct {
		line            int
		kind            string
		isJSON, refused bool
	}
	var rows []row
	for {
		it, err := items.Next()
		if err == io.EOF {
			break
		}
		var refused *ErrNotItem
		if errors.As(err, &refused) {
			// Go refuses a line the oracle read: its block's decoding digest and its functions' differ.
			rows = append(rows, row{line: refused.Line, refused: true})
			got.add(refused.Line, "refused", nil)
			continue
		}
		if err != nil {
			return nil, err
		}
		names := Functions(it)
		encoded := portOutputs(it)
		if len(encoded) != len(names) {
			return nil, fmt.Errorf("%s: the Go port gave %d outputs for %d functions", run.Name, len(encoded), len(names))
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
		sums := map[string]string{}
		for i, name := range names {
			sums[name] = Digest(encoded[i])
			if failedOutputs[encoded[i]] {
				report.failed[name]++
			}
			count := tally[name]
			count.Items++
			kind.Outputs++
			if encoded[i] != "[]" && encoded[i] != "null" {
				count.NonEmpty++
			}
		}
		rows = append(rows, row{line: it.Line, kind: it.Kind, isJSON: it.IsJSON})
		if err := got.add(it.Line, it.InputSHA256(), sums); err != nil {
			return nil, err
		}
	}
	if items.SHA256() != want.ItemsSHA256 {
		return nil, fmt.Errorf("%s: the recorded digests are for other items; convert the dataset again", run.Name)
	}
	if len(rows) != want.Items {
		return nil, fmt.Errorf("%s: the recorded digests hold %d items and the items file %d", run.Name, want.Items, len(rows))
	}
	have := got.finish(items.SHA256())
	report.made = have
	report.Decoding.Items = len(rows)
	// blockRows gives the rows of a block, and the first and last line among them.
	blockRows := func(b int) ([]row, string) {
		part := rows[b*BlockItems : min((b+1)*BlockItems, len(rows))]
		return part, fmt.Sprintf("lines %d to %d (items %d to %d); only block digests were recorded",
			part[0].line, part[len(part)-1].line, b*BlockItems+1, b*BlockItems+len(part))
	}
	differ := func(d Difference) {
		if len(report.Differences) == maxListed {
			report.Unlisted++
			return
		}
		report.Differences = append(report.Differences, d)
	}
	for b := range want.Decoding {
		part, where := blockRows(b)
		if have.Decoding[b] == want.Decoding[b] {
			report.Decoding.Matches += len(part)
			continue
		}
		report.Decoding.Differences += len(part)
		differ(Difference{Line: part[0].line, Function: "item", Position: where})
	}
	for i, f := range want.Functions {
		count := tally[f.Function]
		onJSON := slices.Contains(JSONFunctions, f.Function)
		for b := range f.Blocks {
			part, where := blockRows(b)
			same := have.Functions[i].Blocks[b] == f.Blocks[b]
			if !same {
				differ(Difference{Line: part[0].line, Function: f.Function, Position: where})
			}
			for _, r := range part {
				if r.refused || r.isJSON != onJSON {
					continue
				}
				byKind := count.ByKind[r.kind]
				if byKind == nil {
					byKind = &Tally{}
					count.ByKind[r.kind] = byKind
				}
				byKind.Items++
				if same {
					count.Matches++
					byKind.Matches++
				} else {
					count.Differences++
					byKind.Differences++
					kinds[r.kind].Differences++
				}
			}
		}
	}
	for _, f := range got.r.Functions {
		report.Functions = append(report.Functions, *tally[f.Function])
	}
	for _, name := range append([]string{""}, Kinds...) {
		if k := kinds[name]; k != nil {
			report.Kinds = append(report.Kinds, *k)
		}
	}
	report.Seconds = time.Since(started).Seconds()
	return report, writeJSON(root, reportFile, report, 0o600)
}
