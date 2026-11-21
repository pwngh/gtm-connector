package httptransport_test

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pwngh/gtm-connector/internal/authz"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
	"github.com/pwngh/gtm-connector/web"
)

// These tests serve a build from an embedded file system, as the binary does.

// fixture holds the files webDir writes, compiled into the test binary.
//
//go:embed all:testdata/app
var fixture embed.FS

func embeddedBuild(t *testing.T) fs.FS {
	t.Helper()
	build, err := fs.Sub(fixture, "testdata/app")
	if err != nil {
		t.Fatal(err)
	}
	return build
}

func serving(t *testing.T, cfg httptransport.Config) *httptransport.Server {
	t.Helper()
	cfg.PublicOrigin, cfg.SessionKey = "https://127.0.0.1:8443", testKey
	cfg.AllowedHosts = []string{"127.0.0.1:8443", "localhost:8443"}
	s, err := httptransport.New(cfg, noSessions{}, authz.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The embedded fixture is the folder the other tests serve, file for file.
func TestTheEmbeddedFixtureIsTheFolderFixture(t *testing.T) {
	list := func(build fs.FS) map[string]string {
		files := map[string]string{}
		err := fs.WalkDir(build, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			body, err := fs.ReadFile(build, name)
			files[name] = string(body)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	folder, embedded := list(os.DirFS(webDir(t))), list(embeddedBuild(t))
	if len(folder) != len(embedded) {
		t.Errorf("the folder holds %d files, the embedded build %d", len(folder), len(embedded))
	}
	for name, body := range folder {
		if embedded[name] != body {
			t.Errorf("%s: embedded %q, the folder holds %q", name, embedded[name], body)
		}
	}
}

// clientPaths get the app's HTML. otherPaths never do: reserved prefixes,
// missing and hidden files, non-canonical spellings, and paths outside the
// client routes. Both lists hold every path the folder tests ask for.
var (
	clientPaths = []string{"/", "/index.html", "/renewals/r-1/evidence", "/operations", "/queue", "/playbooks"}
	otherPaths  = []string{"/api", "/api/", "/api/v1", "/api/v1/unknown", "/api/v1/session", "/api/v1/cards/x",
		"/api/v1/nope", "/mcp", "/mcp/", "/mcp/anything", "/.well-known/", "/.well-known/oauth-protected-resource",
		"/healthz", "/healthz/", "/assets", "/assets/", "/assets/missing.js", "/assets/.hidden", "/missing.png",
		"/favicon.ico", "/.env", "/renewals/.env", "/index.html/x.js", "/random", "/renewalsx", "/admin/x",
		"/renewals/x.png", "/api/v1//session", "/api//", "/API/v1/session", "/mcp/../api", "/Healthz",
		"/api/v1/./session", "/renewals//x", "/assets/app.js", "/robots.txt"}
)

// An embedded build answers every path as the folder does: the same status,
// body, content type, caching, tag and policy. Only client routes get the
// app's HTML under the page's own tag, and a reserved path or a missing asset
// never does.
func TestAnEmbeddedBuildIsServedAsAFolderIs(t *testing.T) {
	folder := surface(t)
	embedded := serving(t, httptransport.Config{Web: embeddedBuild(t)})
	same := []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Content-Disposition",
		"Content-Security-Policy", "X-Content-Type-Options", "Accept-Ranges", "Location"}
	// The page's tag is true of the page alone: no other response carries it.
	pageTag := tagOf(send(folder, http.MethodGet, "/", "127.0.0.1:8443", nil).Body.Bytes())
	compare := func(method, path, host string, header http.Header) *httptest.ResponseRecorder {
		t.Helper()
		want, got := send(folder, method, path, host, header), send(embedded, method, path, host, header)
		// A new visitor's session view carries a fresh CSRF token each time.
		fresh := want.Header().Get("Set-Cookie") != "" && got.Header().Get("Set-Cookie") != ""
		if got.Code != want.Code || (!fresh && got.Body.String() != want.Body.String()) {
			t.Errorf("%s %s: embedded %d %q, the folder %d %q", method, path, got.Code, got.Body.String(),
				want.Code, want.Body.String())
		}
		for _, name := range same {
			if got.Header().Get(name) != want.Header().Get(name) {
				t.Errorf("%s %s: embedded %s %q, the folder %q", method, path, name, got.Header().Get(name),
					want.Header().Get(name))
			}
		}
		return got
	}
	for _, path := range slices.Concat(clientPaths, otherPaths) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := compare(method, path, "127.0.0.1:8443", nil)
			client := slices.Contains(clientPaths, path)
			html := strings.HasPrefix(w.Header().Get("Content-Type"), "text/html")
			if client != html || client != (w.Code == http.StatusOK && w.Header().Get("ETag") == pageTag) ||
				(method == http.MethodGet && client != strings.Contains(w.Body.String(), appMarker)) {
				t.Errorf("%s %s: %d %q, the app's HTML: %t", method, path, w.Code, w.Header().Get("Content-Type"), html)
			}
		}
	}
	w := compare(http.MethodGet, "/assets/app.js", "127.0.0.1:8443", nil)
	if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" ||
		w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
		w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("asset: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}
	if w := compare(http.MethodGet, "/assets/app.js", "127.0.0.1:8443",
		http.Header{"Range": {"bytes=0-6"}}); w.Code != http.StatusPartialContent || w.Body.String() != "console" {
		t.Errorf("a range of an asset: %d %q", w.Code, w.Body.String())
	}
	if w := compare(http.MethodPost, "/renewals/x", "127.0.0.1:8443",
		http.Header{"Origin": {"https://127.0.0.1:8443"}}); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to a client route: %d", w.Code)
	}
	compare(http.MethodGet, "/renewals/x?tab=2", "localhost:8443", nil)
	if w := compare(http.MethodGet, "/", "evil.example", nil); w.Code != http.StatusMisdirectedRequest {
		t.Errorf("a foreign Host: %d", w.Code)
	}
}

// A folder, when one is named, is served instead of the embedded build.
func TestAFolderIsServedInsteadOfTheEmbeddedBuild(t *testing.T) {
	other := fstest.MapFS{"index.html": {Data: []byte("<title>ANOTHER-BUILD</title>")}}
	s := serving(t, httptransport.Config{Web: other, WebDir: webDir(t)})
	if w := send(s, http.MethodGet, "/", "127.0.0.1:8443", nil); !strings.Contains(w.Body.String(), appMarker) {
		t.Errorf("with a folder named: %q", w.Body.String())
	}
	s = serving(t, httptransport.Config{Web: other})
	if w := send(s, http.MethodGet, "/", "127.0.0.1:8443", nil); !strings.Contains(w.Body.String(), "ANOTHER-BUILD") {
		t.Errorf("with no folder named: %q", w.Body.String())
	}
	if _, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: testKey, Web: other,
		WebDir: filepath.Join(t.TempDir(), "missing")}, noSessions{}, authz.Evidence{}); err == nil {
		t.Error("a missing folder fell back to the embedded build")
	}
}

// With no build the API is served alone, under the policy with no inline script.
func TestWithoutABuildOnlyTheAPIIsServed(t *testing.T) {
	s := serving(t, httptransport.Config{})
	for _, path := range clientPaths {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusNotFound || strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	w := send(s, http.MethodGet, "/api/v1/session", "127.0.0.1:8443", nil)
	if policy := w.Header().Get("Content-Security-Policy"); w.Code != http.StatusOK ||
		!strings.Contains(policy, "script-src 'self'; ") {
		t.Errorf("the API alone: %d, policy %q", w.Code, policy)
	}
}

var assetLink = regexp.MustCompile(`"(/assets/[^"]+)"`)

// The build compiled into this test binary, which is the one the connector's
// binary carries, serves its page, a deep link and every asset the page
// names. A checkout that has not built the web app has none to check.
func TestTheBuildInTheBinaryIsServed(t *testing.T) {
	build := web.App()
	if build == nil {
		t.Skip("the web app is not built: run npm ci and npm run build in web/")
	}
	s := serving(t, httptransport.Config{Web: build})
	page := send(s, http.MethodGet, "/", "127.0.0.1:8443", nil)
	if page.Code != http.StatusOK || page.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(page.Body.String(), `<div id="root">`) {
		t.Fatalf("/: %d %q", page.Code, page.Header().Get("Content-Type"))
	}
	if strings.Contains(page.Body.String(), "<script>") &&
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'self' 'sha256-") {
		t.Errorf("the page's inline script is not in the policy %q", page.Header().Get("Content-Security-Policy"))
	}
	deep := send(s, http.MethodGet, "/renewals/r-1/evidence", "127.0.0.1:8443", nil)
	if deep.Code != http.StatusOK || deep.Body.String() != page.Body.String() {
		t.Errorf("a deep link: %d", deep.Code)
	}
	assets := assetLink.FindAllStringSubmatch(page.Body.String(), -1)
	if len(assets) == 0 {
		t.Fatal("the page names no asset")
	}
	for _, match := range assets {
		w := send(s, http.MethodGet, match[1], "127.0.0.1:8443", nil)
		kind := w.Header().Get("Content-Type")
		if w.Code != http.StatusOK || w.Body.Len() == 0 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") ||
			(!strings.HasPrefix(kind, "text/javascript") && !strings.HasPrefix(kind, "text/css")) {
			t.Errorf("%s: %d %q, %d bytes", match[1], w.Code, kind, w.Body.Len())
		}
	}
	for _, path := range []string{"/api/v1/nope", "/mcp", "/.well-known/x", "/assets/missing.js", "/missing.png"} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusNotFound || strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	if w := send(s, http.MethodGet, "/healthz", "127.0.0.1:8443", nil); w.Code != http.StatusOK ||
		w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("/healthz: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}
