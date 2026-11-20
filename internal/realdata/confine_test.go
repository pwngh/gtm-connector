package realdata

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// outsideSentinel is a file outside every folder a run may write, with content the test checks afterwards.
func outsideSentinel(t *testing.T) (string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sentinel.txt")
	if err := os.WriteFile(path, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, func() {
		t.Helper()
		if data, err := os.ReadFile(path); err != nil || string(data) != "untouched" {
			t.Errorf("the file outside was changed: %q %v", data, err)
		}
	}
}

// A local run's folder is created private, and one that is not private, or not a folder, is refused.
func TestLocalRunOutputFolder(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	input := filepath.Join(outside, "messages.jsonl")
	if err := os.WriteFile(input, []byte(`{"id": "a", "text": "x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := LocalRun(input, repo)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(run.Out)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("the results folder: %v %v", info, err)
	}
	if err := os.Chmod(run.Out, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalRun(input, repo); err == nil {
		t.Error("a results folder others can read was accepted")
	}
	os.Remove(run.Out)
	if err := os.Symlink(t.TempDir(), run.Out); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalRun(input, repo); err == nil {
		t.Error("a results folder that is a link was accepted")
	}
}

// The report is written as a fresh file in the results folder: a link or a file left at any name there is
// replaced, never written through, and the report is private.
func TestReportStaysInFolder(t *testing.T) {
	run, _, _ := localRun(t, texts...)
	sentinel, check := outsideSentinel(t)
	if err := os.Symlink(sentinel, filepath.Join(run.Out, reportFile+".partial")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sentinel, filepath.Join(run.Out, reportFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(run); err != nil {
		t.Fatal(err)
	}
	check()
	info, err := os.Lstat(filepath.Join(run.Out, reportFile))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("the report: %v %v", info, err)
	}
	// A plain file left at the old temporary name keeps no say over the report's mode.
	os.Remove(filepath.Join(run.Out, reportFile+".partial"))
	if err := os.WriteFile(filepath.Join(run.Out, reportFile+".partial"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(run); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(run.Out, reportFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the report: %v %v", info, err)
	}
}

// A fetch writes only inside its dataset's folder in the cache: a linked folder or a link at a temporary
// name sends nothing outside.
func TestFetchStaysInCache(t *testing.T) {
	data := []byte("the archive")
	server, _ := fetchServer(t, map[string][]byte{"a.zip": data})
	f, _ := testFetcher(t, server)
	d := dataset(Public, pin("a.zip", server.URL+"/a.zip", data))
	outside := t.TempDir()
	if err := os.MkdirAll(DatasetDir(f.Cache, d), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filesDir(f.Cache, d)); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), d); err == nil {
		t.Error("a fetch through a linked folder succeeded")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("the fetch wrote %d files outside the cache", len(entries))
	}
	os.Remove(filesDir(f.Cache, d))
	if err := os.MkdirAll(filesDir(f.Cache, d), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel, check := outsideSentinel(t)
	if err := os.Symlink(sentinel, filepath.Join(filesDir(f.Cache, d), "a.zip.partial")); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	check()
}

// Two fetches of one file at once each write their own temporary file: a bad response to one cannot reach
// the file the other verified.
func TestConcurrentFetches(t *testing.T) {
	good := []byte("the archive, exactly")
	bad := bytes.Repeat([]byte("x"), len(good))
	release := make(chan struct{})
	var mu sync.Mutex
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		first := requests == 1
		mu.Unlock()
		if !first {
			w.Write(good)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
		w.Write(bad)
	}))
	t.Cleanup(server.Close)
	cache := t.TempDir()
	newFetcher := func() *Fetcher {
		f := NewFetcher(cache, func(string, ...any) {})
		f.Client.Transport = server.Client().Transport
		return f
	}
	d := dataset(Public, pin("a.zip", server.URL+"/a.zip", good))
	first := make(chan error, 1)
	go func() { first <- newFetcher().Fetch(context.Background(), d) }()
	// Wait until the first fetch has its temporary file open.
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, _ := os.ReadDir(filesDir(cache, d))
		if len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the first fetch never opened a file")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := newFetcher().Fetch(context.Background(), d); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err == nil {
		t.Error("the bad response was accepted")
	}
	if err := Verify(filepath.Join(filesDir(cache, d), "a.zip"), d.Files[0]); err != nil {
		t.Errorf("the verified file was changed: %v", err)
	}
}

// Conversion parses the bytes it verified: a plain file or a tar archive replaced after the check is not
// read.
func TestConvertReadsVerifiedBytes(t *testing.T) {
	order := []string{"top/raw/general/2016-01-07.json"}
	for name, c := range map[string]struct {
		converter, path string
		original, other []byte
	}{
		"a plain file": {"irc-raw", "data/train/a.raw.txt", []byte("[03:56] <a> the original\n"), []byte("[03:56] <a> Replaced\n")},
		"a tar archive": {"slack-export", "c.tar.gz",
			buildTarGz(t, order, map[string]string{order[0]: `[{"text": "the original"}]`}),
			buildTarGz(t, order, map[string]string{order[0]: `[{"text": "Replaced"}]`})},
	} {
		t.Run(name, func(t *testing.T) {
			cache, d := placeFiles(t, c.converter, map[string][]byte{c.path: c.original})
			files, err := openFiles(cache, d)
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			replacement := filepath.Join(t.TempDir(), "replacement")
			if err := os.WriteFile(replacement, c.other, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, filepath.Join(filesDir(cache, d), filepath.FromSlash(c.path))); err != nil {
				t.Fatal(err)
			}
			var texts []string
			_, err = converters[c.converter](files, func(it Item) error { texts = append(texts, it.Text); return nil })
			if strings.Contains(strings.Join(texts, "|"), "Replaced") {
				t.Errorf("conversion read the replaced file: %q %v", texts, err)
			}
		})
	}
}

// A path inside the repository is refused whatever its spelling: through a link, or in another case on a
// volume that ignores case.
func TestLocalRunRepositoryAliases(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "Repo")
	if err := os.MkdirAll(filepath.Join(repo, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(repo, "notes", "messages.jsonl")
	if err := os.WriteFile(input, []byte(`{"id": "a", "text": "x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalRun(filepath.Join(link, "notes", "messages.jsonl"), repo); err == nil {
		t.Error("a file inside the repository, reached through a link, was accepted")
	}
	lower := filepath.Join(parent, "repo", "notes", "messages.jsonl")
	if _, err := os.Stat(lower); err != nil {
		t.Skip("this volume tells case apart")
	}
	if _, err := LocalRun(lower, repo); err == nil {
		t.Error("a file inside the repository, spelled in another case, was accepted")
	}
}

// A redirect may go only to the hosts the dataset names.
func TestRedirectsStayWithSource(t *testing.T) {
	data := []byte("the archive")
	elsewhere, _ := fetchServer(t, map[string][]byte{"a.zip": data})
	origin := httptest.NewTLSServer(http.RedirectHandler(elsewhere.URL+"/a.zip", http.StatusFound))
	t.Cleanup(origin.Close)
	f, _ := testFetcher(t, origin)
	d := dataset(Public, pin("a.zip", origin.URL+"/a.zip", data))
	if err := f.Fetch(context.Background(), d); err == nil {
		t.Error("a redirect to another host was followed")
	}
	// The same redirect to a host the dataset names, as an archive host or a CDN, is followed.
	d.RedirectHosts = []string{strings.TrimPrefix(elsewhere.URL, "https://")}
	if err := f.Fetch(context.Background(), d); err != nil {
		t.Errorf("a redirect to a named host: %v", err)
	}
}

// A download changed in place while it is converted is found before its items are kept.
func TestConvertRechecks(t *testing.T) {
	cache, d := placeFiles(t, "irc-raw", map[string][]byte{"data/train/a.raw.txt": []byte("[03:56] <a> the original\n")})
	files, err := openFiles(cache, d)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	out, err := os.OpenFile(filepath.Join(filesDir(cache, d), "data", "train", "a.raw.txt"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	out.WriteAt([]byte("X"), 13)
	out.Close()
	if err := files.Recheck(); err == nil {
		t.Error("a file changed in place passed the recheck")
	}
}
