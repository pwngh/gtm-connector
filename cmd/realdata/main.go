// Command realdata runs real public text through the Python oracle and the Go port and compares them (R0 in
// the backend plan). It downloads datasets, so it is opt-in and never part of the tests or CI.
//
//	realdata fetch [DATASET...]          download each pinned public dataset into the cache and check it
//	realdata convert [DATASET...]        turn each fetched dataset into items
//	realdata compare [DATASET...]        compare Go's outputs with what the oracle recorded
//	realdata verify [DATASET...]         all of it: fetch, convert, run the oracle, compare
//	realdata pin DATASET...              fetch a dataset's files the first time and record their digests
//
// With no dataset named, a command takes every dataset that is not opt-in. pin writes the pins into
// internal/realdata/pins, which are committed.
//
// -python names the pinned CPython for verify; -python ” compares against the recorded digests without
// running Python. -local FILE runs verify or compare on a private JSON Lines file of {"id", "text"} items
// instead of the datasets, with its results in FILE.r0 beside it.
//
// The cache is $GTM_REAL_CACHE, or ~/.cache/gtm-connector/real. Nothing it prints holds an item's text.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/realdata"
)

const usage = `usage: realdata [flags] fetch|convert|compare|verify|pin [DATASET...]

  fetch     download each pinned public dataset into the cache and check it against its pins
  convert   turn each fetched dataset into items
  compare   compare the Go port's outputs with what the oracle recorded
  verify    fetch, convert, run the Python oracle, compare
  pin       fetch a dataset's files the first time and record their digests in internal/realdata/pins

flags:
`

func main() {
	// A panic's value could hold an item's text, so only its type is shown.
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "realdata: internal error (%T)\n", recovered)
			os.Exit(2)
		}
	}()
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("realdata", flag.ContinueOnError)
	python := flags.String("python", ".venv/bin/python", "the pinned CPython 3.14.6 that runs the oracle; '' to compare against recorded digests")
	local := flags.String("local", "", "a private JSON Lines file of {\"id\", \"text\"} items, outside the repository")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	command, names := flags.Arg(0), flags.Args()[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	differences, err := dispatch(ctx, command, names, *python, *local)
	if err != nil {
		fmt.Fprintf(os.Stderr, "realdata: %v\n", err)
		return 2
	}
	if differences > 0 {
		return 1
	}
	return 0
}

type state struct{ cache, root, python string }

func dispatch(ctx context.Context, command string, names []string, python, local string) (int, error) {
	root, err := repository()
	if err != nil {
		return 0, err
	}
	s := state{root: root, python: python}
	if local != "" {
		if len(names) > 0 {
			return 0, errors.New("-local runs one file; name no dataset with it")
		}
		r, err := realdata.LocalRun(local, root)
		if err != nil {
			return 0, err
		}
		switch command {
		case "verify":
			return s.verify(ctx, []realdata.Run{r})
		case "compare":
			return compare([]realdata.Run{r})
		}
		return 0, fmt.Errorf("-local goes with verify or compare, not %s", command)
	}
	if s.cache, err = realdata.CacheDir(); err != nil {
		return 0, err
	}
	manifest, err := realdata.LoadManifest()
	if err != nil {
		return 0, err
	}
	datasets, err := manifest.Select(names)
	if err != nil {
		return 0, err
	}
	switch command {
	case "pin":
		return 0, s.pin(ctx, datasets)
	case "fetch":
		_, err := s.fetch(ctx, datasets)
		return 0, err
	case "convert":
		_, err := s.convert(datasets)
		return 0, err
	case "compare":
		var runs []realdata.Run
		for _, d := range datasets {
			if d.Converter != "" && d.Pinned() {
				runs = append(runs, realdata.DatasetRun(s.cache, d))
			}
		}
		return compare(runs)
	case "verify":
		started := time.Now()
		fetched, err := s.fetch(ctx, datasets)
		if err != nil {
			return 0, err
		}
		if len(fetched) == 0 {
			return 0, errors.New("no dataset is in the cache to verify")
		}
		runs, err := s.convert(fetched)
		if err != nil {
			return 0, err
		}
		differences, err := s.verify(ctx, runs)
		fmt.Printf("verify-real took %.1f s in all\n", time.Since(started).Seconds())
		return differences, err
	}
	return 0, fmt.Errorf("no command %q; use fetch, convert, compare or verify", command)
}

// fetch gives the datasets that are in the cache and match the manifest. An owner dataset the owner has not
// fetched is skipped with what the owner must do.
func (s state) fetch(ctx context.Context, datasets []realdata.Dataset) ([]realdata.Dataset, error) {
	started := time.Now()
	fetcher := realdata.NewFetcher(s.cache, logf)
	var fetched []realdata.Dataset
	for _, d := range datasets {
		err := fetcher.Fetch(ctx, d)
		if errors.Is(err, realdata.ErrNeedsOwner) || errors.Is(err, realdata.ErrNotPinned) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fetched = append(fetched, d)
	}
	logf("fetch: %d of %d datasets in %s, in %.1f s", len(fetched), len(datasets), s.cache, time.Since(started).Seconds())
	return fetched, nil
}

// pin fetches each dataset's unpinned files and writes its pins into the repository.
func (s state) pin(ctx context.Context, datasets []realdata.Dataset) error {
	fetcher := realdata.NewFetcher(s.cache, logf)
	for _, d := range datasets {
		if d.Access == realdata.Owner {
			logf("pin: %s: skipped, the owner fetches it. %s", d.ID, d.OwnerSteps)
			continue
		}
		started := time.Now()
		files, err := fetcher.Pin(ctx, d)
		if err != nil {
			return err
		}
		path := filepath.Join(s.root, "internal", "realdata", "pins", d.ID+".tsv")
		if err := os.WriteFile(path, realdata.FormatPins(files), 0o644); err != nil {
			return err
		}
		var total int64
		for _, f := range files {
			total += f.Bytes
		}
		logf("pin: %s: %d files, %d bytes, in %.1f s; wrote %s", d.ID, len(files), total, time.Since(started).Seconds(), path)
	}
	return nil
}

func (s state) convert(datasets []realdata.Dataset) ([]realdata.Run, error) {
	var runs []realdata.Run
	for _, d := range datasets {
		started := time.Now()
		summary, err := realdata.Convert(s.cache, d)
		if errors.Is(err, realdata.ErrNoConverter) {
			logf("convert: %s: skipped, %s. %s", d.ID, realdata.ErrNoConverter, d.Note)
			continue
		}
		if err != nil {
			return nil, err
		}
		var counts []string
		for name, n := range summary.Counts {
			counts = append(counts, fmt.Sprintf("%s %d", name, n))
		}
		sort.Strings(counts)
		logf("convert: %s: %d text items and %d JSON documents in %.1f s (%s)", d.ID, summary.TextItems,
			summary.JSONItems, time.Since(started).Seconds(), strings.Join(counts, ", "))
		runs = append(runs, realdata.DatasetRun(s.cache, d))
	}
	return runs, nil
}

// verify runs the oracle on each run, then compares. With no Python it compares against what an earlier run
// recorded.
func (s state) verify(ctx context.Context, runs []realdata.Run) (int, error) {
	if s.python == "" {
		logf("oracle: no Python given; comparing against the digests recorded earlier")
		return compare(runs)
	}
	// A bare name is looked up on PATH; a relative path is from the repository.
	python, err := exec.LookPath(s.python)
	if strings.ContainsRune(s.python, filepath.Separator) && !filepath.IsAbs(s.python) {
		python, err = exec.LookPath(filepath.Join(s.root, s.python))
	}
	if err != nil {
		return 0, fmt.Errorf("no Python at %s; set PYTHON to the pinned CPython 3.14.6, or to '' to compare against recorded digests", s.python)
	}
	for _, r := range runs {
		args := []string{"-I", filepath.Join(s.root, "tools", "oracle", "real.py"), "--out", r.Out, r.Items}
		if r.Local {
			args = append(args, "--local")
		}
		logf("oracle: %s", r.Name)
		command := exec.CommandContext(ctx, python, args...)
		command.Dir = s.root
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return 0, fmt.Errorf("the oracle failed on %s", r.Name)
		}
	}
	return compare(runs)
}

func compare(runs []realdata.Run) (int, error) {
	total := 0
	for _, r := range runs {
		report, err := realdata.Compare(r)
		if err != nil {
			return 0, err
		}
		realdata.PrintReport(os.Stdout, report, r)
		total += report.Total()
	}
	return total, nil
}

// repository is the folder holding go.mod, above the working folder.
func repository() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run realdata inside the repository")
		}
		dir = parent
	}
}

func logf(format string, args ...any) { fmt.Printf(format+"\n", args...) }
