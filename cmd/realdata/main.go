// Command realdata runs real public text through the Go port and compares its outputs with the digests the
// Python oracle recorded for the same text. It downloads datasets, so it is opt-in and never part of the
// tests or CI.
//
//	realdata fetch [DATASET...]          download each pinned public dataset into the cache and check it
//	realdata convert [DATASET...]        turn each fetched dataset into items
//	realdata compare [DATASET...]        compare Go's outputs with the recorded digests
//	realdata verify [DATASET...]         all of it: fetch, convert, compare
//	realdata pin DATASET...              fetch a dataset's files the first time and record their digests
//
// With no dataset named, a command takes every dataset that is not opt-in. pin writes the pins into
// internal/realdata/pins, and the recorded digests are in internal/realdata/recorded; both are committed.
//
// -local FILE runs verify or compare on a private JSON Lines file of {"id", "text"} items instead of the
// datasets, against the oracle's records an earlier run left in FILE.r0 beside it.
//
// -update makes verify or compare write Go's block digests of each dataset that differs into
// internal/realdata/recorded, after a deliberate change to a text function.
//
// The cache is $GTM_REAL_CACHE, or ~/.cache/gtm-connector/real. Nothing it prints holds an item's text.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
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
  compare   compare the Go port's outputs with the digests the oracle recorded
  verify    fetch, convert, compare
  pin       fetch a dataset's files the first time and record their digests in internal/realdata/pins

flags:
`

func main() { os.Exit(run(os.Args[1:])) }

// dispatchCommand runs a command; a test replaces it to check what a panic shows.
var dispatchCommand = dispatch

func run(args []string) (code int) {
	// A panic's value could hold an item's text, so only its type is shown.
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "realdata: internal error (%T)\n", recovered)
			code = 2
		}
	}()
	flags := flag.NewFlagSet("realdata", flag.ContinueOnError)
	local := flags.String("local", "", "a private JSON Lines file of {\"id\", \"text\"} items, outside the repository")
	update := flags.Bool("update", false, "write Go's digests of each dataset that differs into internal/realdata/recorded")
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
	differences, err := dispatchCommand(ctx, command, names, *local, *update)
	if err != nil {
		fmt.Fprintf(os.Stderr, "realdata: %v\n", err)
		return 2
	}
	if differences > 0 {
		return 1
	}
	return 0
}

type state struct {
	cache, root string
	explicit    bool // the datasets were named
	update      bool // a comparison that differs writes Go's digests
}

// runnable says why a dataset cannot be compared, if it cannot.
func runnable(d realdata.Dataset) error {
	switch {
	case d.Access == realdata.Owner && !d.Pinned():
		return fmt.Errorf("%s needs the owner: %s", d.ID, d.OwnerSteps)
	case d.Converter == "":
		return fmt.Errorf("%s has no converter yet, so it cannot be compared (%s) `realdata fetch %s` fetches its files",
			d.ID, d.Note, d.ID)
	case !d.Pinned():
		return fmt.Errorf("%s is not pinned yet; `realdata pin %s` fetches it and records its digests", d.ID, d.ID)
	}
	if _, err := realdata.LoadRecorded(d.ID); err != nil {
		return err
	}
	return nil
}

func dispatch(ctx context.Context, command string, names []string, local string, update bool) (int, error) {
	root, err := repository()
	if err != nil {
		return 0, err
	}
	s := state{root: root, update: update}
	switch {
	case update && local != "":
		return 0, errors.New("-update writes the recorded digests of datasets; a -local run has none")
	case update && command != "verify" && command != "compare":
		return 0, fmt.Errorf("-update goes with verify or compare, not %s", command)
	}
	if local != "" {
		if len(names) > 0 {
			return 0, errors.New("-local runs one file; name no dataset with it")
		}
		if command != "verify" && command != "compare" {
			return 0, fmt.Errorf("-local goes with verify or compare, not %s", command)
		}
		r, err := realdata.LocalRun(local, root)
		if err != nil {
			return 0, err
		}
		return s.compare([]realdata.Run{r})
	}
	if s.cache, err = realdata.CacheDir(); err != nil {
		return 0, err
	}
	if err := realdata.RefuseInsideRepository("the cache", s.cache, root); err != nil {
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
	// A dataset named for a comparison that cannot run is a failure of its own, never a pass: nothing is
	// fetched for it. Without names, such datasets are skipped, each with what it waits for.
	s.explicit = len(names) > 0
	if s.explicit && (command == "verify" || command == "convert" || command == "compare") {
		for _, d := range datasets {
			if err := runnable(d); err != nil && (command != "convert" || !errors.Is(err, realdata.ErrNotRecorded)) {
				return 0, err
			}
		}
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
		return s.compare(runs)
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
		differences, err := s.compare(runs)
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
		if !s.explicit && (errors.Is(err, realdata.ErrNeedsOwner) || errors.Is(err, realdata.ErrNotPinned)) {
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

// compare compares each run and prints its report: a dataset's run against its recorded digests, and a local
// run against the records in its results folder. A run that fails is reported, and the others go on. With
// -update, a dataset's run that differs writes Go's digests as its recorded ones, and its differences are
// not counted.
func (s state) compare(runs []realdata.Run) (int, error) {
	total, failed := 0, 0
	for _, r := range runs {
		var report *realdata.Report
		var err error
		if r.Local {
			report, err = realdata.Compare(r)
		} else {
			var recorded *realdata.Recorded
			if recorded, err = realdata.LoadRecorded(r.Name); err == nil {
				report, err = realdata.CompareRecorded(r, recorded)
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "realdata: %v\n", err)
			failed++
			continue
		}
		realdata.PrintReport(os.Stdout, report, r)
		if s.update && !r.Local && report.Total() > 0 {
			dir := filepath.Join(s.root, "internal", "realdata", "recorded")
			if err := report.WriteRecorded(dir); err != nil {
				fmt.Fprintf(os.Stderr, "realdata: %v\n", err)
				failed++
				continue
			}
			logf("  wrote Go's digests to %s", filepath.Join(dir, r.Name+".json"))
			continue
		}
		total += report.Total()
	}
	if failed > 0 {
		return total, fmt.Errorf("%d of %d comparisons failed", failed, len(runs))
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
