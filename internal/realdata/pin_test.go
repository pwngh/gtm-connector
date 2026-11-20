package realdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var revision40 = strings.Repeat("a", 40)

// fakeHub serves a Hub tree listing of data/ in two pages (a git file, then an LFS file) and the files
// themselves under /files/. listed and served change what it lists and serves.
func fakeHub(t *testing.T, listed map[string]map[string]any, served map[string][]byte, next string) *httptest.Server {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/api/datasets/org/set/tree/" + revision40
		switch {
		case r.URL.Path == base:
			json.NewEncoder(w).Encode([]map[string]any{{"type": "directory", "path": "data"}})
		case r.URL.Path == base+"/data" && r.URL.Query().Get("cursor") == "":
			link := next
			if link == "" {
				link = server.URL + base + "/data?cursor=2"
			}
			w.Header().Set("Link", "<"+link+`>; rel="next"`)
			json.NewEncoder(w).Encode([]map[string]any{listed["page1"]})
		case r.URL.Path == base+"/data":
			json.NewEncoder(w).Encode([]map[string]any{listed["page2"]})
		case strings.HasPrefix(r.URL.Path, "/files/"):
			data, ok := served[strings.TrimPrefix(r.URL.Path, "/files/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func hubDataset(server *httptest.Server) Dataset {
	return Dataset{ID: "set-1", Access: Public, Kind: "natural", BaseURL: server.URL + "/files/",
		Listing: &Listing{HuggingFace: "org/set", Revision: revision40, Include: []string{"data/*.json"}}}
}

func hubFetcher(t *testing.T, server *httptest.Server) *Fetcher {
	f, _ := testFetcher(t, server)
	f.HubAPI = server.URL + "/api/datasets/"
	return f
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// A Hub listing is read page by page, and each file is checked against the digest the Hub lists for it.
func TestPinHubListing(t *testing.T) {
	a, b := []byte(`{"a": 1}`), []byte(`{"b": 2}`)
	good := map[string]map[string]any{
		"page1": {"type": "file", "path": "data/a.json", "oid": gitBlob(a)},
		"page2": {"type": "file", "path": "data/b.json", "lfs": map[string]any{"oid": sha256Hex(b)}},
	}
	served := map[string][]byte{"data/a.json": a, "data/b.json": b}
	server := fakeHub(t, good, served, "")
	f := hubFetcher(t, server)
	files, err := f.Pin(context.Background(), hubDataset(server))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].SHA256 != sha256Hex(a) || files[1].SHA256 != sha256Hex(b) {
		t.Fatalf("%+v", files)
	}
	// A second Hub-shaped server on another host, which no listing call may reach.
	var elsewhereCalls atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereCalls.Add(1)
		json.NewEncoder(w).Encode([]map[string]any{good["page2"]})
	}))
	t.Cleanup(elsewhere.Close)
	for name, c := range map[string]struct {
		listed map[string]map[string]any
		next   string
		bad    string // the file that must not be kept
	}{
		"an LFS digest that is not the file's": {map[string]map[string]any{"page1": good["page1"],
			"page2": {"type": "file", "path": "data/b.json", "lfs": map[string]any{"oid": strings.Repeat("0", 64)}}}, "", "b.json"},
		"a git blob that is not the file's": {map[string]map[string]any{"page2": good["page2"],
			"page1": {"type": "file", "path": "data/a.json", "oid": gitBlob(b)}}, "", "a.json"},
		"a listed path this program does not store": {map[string]map[string]any{"page2": good["page2"],
			"page1": {"type": "file", "path": "data/.a.json", "oid": gitBlob(a)}}, "", ".a.json"},
		"a next page on another host": {good, elsewhere.URL + "/api/datasets/org/set/tree/" + revision40 + "/data?cursor=2", "b.json"},
	} {
		t.Run(name, func(t *testing.T) {
			server := fakeHub(t, c.listed, map[string][]byte{"data/a.json": a, "data/b.json": b, "data/.a.json": a}, c.next)
			f := hubFetcher(t, server)
			if _, err := f.Pin(context.Background(), hubDataset(server)); err == nil {
				t.Fatal("pinned")
			}
			if _, err := os.Lstat(filepath.Join(filesDir(f.Cache, hubDataset(server)), "data", c.bad)); err == nil {
				t.Errorf("%s was kept", c.bad)
			}
			if elsewhereCalls.Load() != 0 {
				t.Errorf("%d calls reached another host", elsewhereCalls.Load())
			}
		})
	}
}

// A git listing comes from the repository's trees; a file whose bytes are not the listed blob is refused,
// and so is a listed path this program does not store.
func TestPinGitListing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
			"-c", "commit.gpgsign=false"}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	os.MkdirAll(filepath.Join(repo, "data"), 0o755)
	os.WriteFile(filepath.Join(repo, "data", "a.json"), []byte(`{"in": "the repository"}`), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "a")
	head := git("rev-parse", "HEAD")
	server, _ := fetchServer(t, map[string][]byte{"files/data/a.json": []byte(`{"other": "bytes"}`)})
	f, _ := testFetcher(t, server)
	f.GitProtocols = []string{"file"}
	d := Dataset{ID: "set-1", Access: Public, Kind: "natural", BaseURL: server.URL + "/files/",
		Listing: &Listing{Git: repo, Revision: head, Include: []string{"data/*.json"}}}
	if _, err := f.Pin(context.Background(), d); err == nil || !strings.Contains(err.Error(), "git blob") {
		t.Errorf("a file that is not its blob: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filesDir(f.Cache, d), "data", "a.json")); err == nil {
		t.Error("the refused file was kept")
	}
	os.WriteFile(filepath.Join(repo, "data", ".hidden.json"), []byte("{}"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "b")
	d.Listing.Revision = git("rev-parse", "HEAD")
	if _, err := f.Pin(context.Background(), d); err == nil || !strings.Contains(err.Error(), "does not store") {
		t.Errorf("a listed dot file: %v", err)
	}
}

// A pinned file fetched again by pin must match its pin.
func TestPinRefetchMatchesPin(t *testing.T) {
	server, _ := fetchServer(t, map[string][]byte{"a.zip": []byte("other bytes")})
	f, _ := testFetcher(t, server)
	d := dataset(Public, pin("a.zip", server.URL+"/a.zip", []byte("the pinned bytes")))
	if _, err := f.Pin(context.Background(), d); err == nil {
		t.Error("pinned")
	}
	if entries, _ := os.ReadDir(filesDir(f.Cache, d)); len(entries) != 0 {
		t.Errorf("%d files left", len(entries))
	}
}
