package realdata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CacheDir is $GTM_REAL_CACHE, or ~/.cache/gtm-connector/real.
func CacheDir() (string, error) {
	if dir := os.Getenv("GTM_REAL_CACHE"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("no home folder: set GTM_REAL_CACHE")
	}
	return filepath.Join(home, ".cache", "gtm-connector", "real"), nil
}

// Run is one set of items and the folder its results go to. A dataset's run is its folder in the cache; a
// local run's folder sits next to its input file.
type Run struct {
	Name  string // the dataset ID, or "local"
	Items string // the items file: JSON Lines, gzipped when the name ends in .gz
	Out   string // oracle.json, oracle-digests.jsonl.gz, oracle.jsonl.gz and report.json
	// Local runs hold private text: reports name items by line, never by ID or file.
	Local bool
}

// The files of a run's folder.
const (
	metaFile    = "oracle.json"
	digestsFile = "oracle-digests.jsonl.gz"
	outputsFile = "oracle.jsonl.gz"
	reportFile  = "report.json"
	itemsFile   = "items.jsonl.gz"
	itemsMeta   = "items.json"
)

// DatasetDir is a dataset's folder in the cache. Its downloads are in files/.
func DatasetDir(cache string, d Dataset) string { return filepath.Join(cache, d.ID) }

func filesDir(cache string, d Dataset) string { return filepath.Join(DatasetDir(cache, d), "files") }

// DatasetRun is the run of a converted dataset.
func DatasetRun(cache string, d Dataset) Run {
	dir := DatasetDir(cache, d)
	return Run{Name: d.ID, Items: filepath.Join(dir, itemsFile), Out: dir}
}

// LocalRun is the run of a private items file outside the repository. Its results go to the folder FILE.r0
// beside it.
func LocalRun(input, repository string) (Run, error) {
	path, err := filepath.Abs(input)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return Run{}, fmt.Errorf("the local file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Run{}, err
	}
	if !info.Mode().IsRegular() {
		return Run{}, errors.New("the local file is not a regular file")
	}
	root, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return Run{}, err
	}
	if inside(root, path) {
		return Run{}, errors.New("the local file is inside the repository; keep private text outside it")
	}
	out := path + ".r0"
	if info, err := os.Lstat(out); err == nil && !info.IsDir() {
		return Run{}, fmt.Errorf("%s exists and is not a folder", out)
	}
	return Run{Name: "local", Items: path, Out: out, Local: true}, nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
