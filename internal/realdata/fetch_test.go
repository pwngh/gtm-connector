package realdata

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func pin(name, url string, data []byte) File {
	sum := sha256.Sum256(data)
	return File{Path: name, URL: url, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := pin("a.bin", "https://data.example/a.bin", []byte("abc"))
	if err := Verify(path, file); err != nil {
		t.Fatal(err)
	}
	other := pin("a.bin", "https://data.example/a.bin", []byte("abd"))
	if err := Verify(path, other); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("a changed byte: %v", err)
	}
	longer := pin("a.bin", "https://data.example/a.bin", []byte("abcd"))
	if err := Verify(path, longer); err == nil || !strings.Contains(err.Error(), "3 bytes") {
		t.Errorf("a short file: %v", err)
	}
	shorter := pin("a.bin", "https://data.example/a.bin", []byte("ab"))
	if err := Verify(path, shorter); err == nil || !strings.Contains(err.Error(), "longer") {
		t.Errorf("a long file: %v", err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := Verify(link, file); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Errorf("a link: %v", err)
	}
	if err := Verify(path, File{Path: "a.bin"}); err == nil {
		t.Error("an unpinned file was verified")
	}
}

// fetchServer serves files over local TLS and counts requests.
func fetchServer(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func testFetcher(t *testing.T, server *httptest.Server) (*Fetcher, *strings.Builder) {
	var log strings.Builder
	f := NewFetcher(t.TempDir(), func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) })
	f.Client.Transport = server.Client().Transport
	return f, &log
}

func dataset(access string, files ...File) Dataset {
	return Dataset{ID: "set-1", Access: access, Kind: "natural", Files: files,
		OwnerSteps: "Register at the site, then download it."}
}

func TestFetch(t *testing.T) {
	good := []byte("the archive")
	server, requests := fetchServer(t, map[string][]byte{"a.zip": good, "b.zip": []byte("not what was pinned")})
	f, log := testFetcher(t, server)
	d := dataset(Public, pin("a.zip", server.URL+"/a.zip", good))
	if err := f.Fetch(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filesDir(f.Cache, d), "a.zip")
	if data, err := os.ReadFile(path); err != nil || string(data) != string(good) {
		t.Fatalf("the cached file: %q %v", data, err)
	}
	// A file already in the cache is checked, not fetched again.
	if err := f.Fetch(context.Background(), d); err != nil || requests.Load() != 1 {
		t.Fatalf("second fetch: %v, %d requests", err, requests.Load())
	}
	if !strings.Contains(log.String(), "1 were in the cache, 0 fetched") {
		t.Errorf("log: %s", log)
	}
	// A cached file that no longer matches stops the run and stays for a person to look at.
	if err := os.WriteFile(path, []byte("the archivE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), d); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Errorf("a changed cached file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the changed file was removed")
	}
}

func TestFetchRefusesMismatch(t *testing.T) {
	server, _ := fetchServer(t, map[string][]byte{"b.zip": []byte("not what was pinned")})
	for name, file := range map[string]File{
		"another digest":  pin("b.zip", server.URL+"/b.zip", []byte("what was pinned, same len")[:19]),
		"a longer body":   pin("b.zip", server.URL+"/b.zip", []byte("not what")),
		"a shorter body":  pin("b.zip", server.URL+"/b.zip", []byte("not what was pinned at all")),
		"a missing file":  pin("b.zip", server.URL+"/c.zip", []byte("x")),
		"an http address": pin("b.zip", strings.Replace(server.URL, "https", "http", 1)+"/b.zip", []byte("x")),
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := testFetcher(t, server)
			d := dataset(Public, file)
			if err := f.Fetch(context.Background(), d); err == nil {
				t.Fatal("fetched")
			}
			entries, _ := os.ReadDir(filesDir(f.Cache, d))
			if len(entries) != 0 {
				t.Errorf("left %d files behind", len(entries))
			}
		})
	}
}

func TestFetchOwnerDataset(t *testing.T) {
	server, requests := fetchServer(t, map[string][]byte{})
	data := []byte("the owner's download")
	f, log := testFetcher(t, server)
	d := dataset(Owner, pin("a.zip", server.URL+"/a.zip", data))
	err := f.Fetch(context.Background(), d)
	if !errors.Is(err, ErrNeedsOwner) || !strings.Contains(log.String(), "Register at the site") {
		t.Fatalf("%v; log: %s", err, log)
	}
	if requests.Load() != 0 {
		t.Fatal("an owner dataset was requested")
	}
	dir := filesDir(f.Cache, d)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), d); err != nil {
		t.Fatalf("the owner's file: %v", err)
	}
	unpinned := dataset(Owner, File{Path: "a.zip", URL: server.URL + "/a.zip"})
	if err := f.Fetch(context.Background(), unpinned); !errors.Is(err, ErrNeedsOwner) {
		t.Errorf("an unpinned owner file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(context.Background(), d); err == nil || errors.Is(err, ErrNeedsOwner) {
		t.Errorf("a changed owner file: %v", err)
	}
}

// pin fetches a listed file the first time and records its size and digest; a dataset not yet pinned is
// never fetched by fetch.
func TestPin(t *testing.T) {
	data := []byte("a listed file")
	server, _ := fetchServer(t, map[string][]byte{"dir/a.txt": data})
	f, log := testFetcher(t, server)
	d := dataset(Public, File{Path: "dir/a.txt", URL: server.URL + "/dir/a.txt"})
	if err := f.Fetch(context.Background(), d); !errors.Is(err, ErrNotPinned) || !strings.Contains(log.String(), "realdata pin set-1") {
		t.Fatalf("fetch of an unpinned dataset: %v", err)
	}
	files, err := f.Pin(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if want := pin("dir/a.txt", server.URL+"/dir/a.txt", data); len(files) != 1 || files[0] != want {
		t.Fatalf("%+v", files)
	}
	if _, err := f.Pin(context.Background(), d); err == nil || !strings.Contains(err.Error(), "no pin") {
		t.Errorf("pinning over an unpinned cached file: %v", err)
	}
	d.Files = files
	if err := f.Fetch(context.Background(), d); err != nil {
		t.Errorf("fetch once pinned: %v", err)
	}
	local := filepath.Join(filesDir(f.Cache, d), "dir", "a.txt")
	if err := checkSource(local, files[0], listed{gitSHA1: "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"}); err == nil {
		t.Error("a file that is not the listed git blob was accepted")
	}
	// git hash-object of the same bytes.
	if err := checkSource(local, files[0], listed{gitSHA1: gitBlob(data)}); err != nil {
		t.Error(err)
	}
	if err := checkSource(local, files[0], listed{sha256: strings.Repeat("0", 64)}); err == nil {
		t.Error("a file that is not the listed LFS object was accepted")
	}
	if _, err := f.Pin(context.Background(), dataset(Owner)); !errors.Is(err, ErrNeedsOwner) {
		t.Errorf("an owner dataset was pinned: %v", err)
	}
}

func gitBlob(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
