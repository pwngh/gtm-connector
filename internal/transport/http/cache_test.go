package httptransport_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// These tests state what a browser may keep of each class of response: a
// hashed asset for a year, the app's page only to ask about it again, and
// nothing from the API.

const (
	keptForAYear = "public, max-age=31536000, immutable"
	publicHost   = "127.0.0.1:8443"
)

// tagOf is the ETag of a page: the SHA-256 of its bytes, quoted.
func tagOf(page []byte) string {
	sum := sha256.Sum256(page)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// builds are the two ways a build is served, a folder and an embedded file
// system, holding the same files.
func builds(t *testing.T) map[string]*httptransport.Server {
	return map[string]*httptransport.Server{
		"a folder":          surface(t),
		"an embedded build": serving(t, httptransport.Config{Web: embeddedBuild(t)}),
	}
}

func TestAHashedAssetIsKeptForAYear(t *testing.T) {
	for name, s := range builds(t) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := send(s, method, "/assets/app.js", publicHost, nil)
			if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != keptForAYear {
				t.Errorf("%s: %s: %d %q", name, method, w.Code, w.Header().Get("Cache-Control"))
			}
		}
		// A missing asset is not the app's page, and is not kept as an asset.
		w := send(s, http.MethodGet, "/assets/missing.js", publicHost, nil)
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") == keptForAYear {
			t.Errorf("%s: a missing asset: %d %q", name, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

// The page and every route that falls back to it, a deep link among them,
// are asked about on each use and carry the page's own tag. A request that
// names the tag gets 304 with no body; one that names another gets the page.
func TestThePageIsAskedAboutEachTimeAndTaggedByItsBytes(t *testing.T) {
	for name, s := range builds(t) {
		page := send(s, http.MethodGet, "/", publicHost, nil).Body.Bytes()
		tag := tagOf(page)
		for _, path := range []string{"/", "/index.html", "/renewals/r-1/evidence", "/operations", "/queue/q-7?tab=2"} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				w := send(s, method, path, publicHost, nil)
				if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") != tag ||
					(method == http.MethodGet && w.Body.String() != string(page)) {
					t.Errorf("%s: %s %s: %d, Cache-Control %q, ETag %q, not %s", name, method, path, w.Code,
						w.Header().Get("Cache-Control"), w.Header().Get("ETag"), tag)
				}
				for _, named := range []string{tag, `"another", ` + tag, "W/" + tag, "*"} {
					w := send(s, method, path, publicHost, http.Header{"If-None-Match": {named}})
					if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-cache" ||
						w.Header().Get("ETag") != tag {
						t.Errorf("%s: %s %s with If-None-Match %s: %d, %d bytes, Cache-Control %q, ETag %q", name, method,
							path, named, w.Code, w.Body.Len(), w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
					}
				}
				w = send(s, method, path, publicHost, http.Header{"If-None-Match": {tagOf([]byte("an earlier build"))}})
				if w.Code != http.StatusOK || w.Header().Get("ETag") != tag ||
					(method == http.MethodGet && w.Body.String() != string(page)) {
					t.Errorf("%s: %s %s with an earlier build's tag: %d", name, method, path, w.Code)
				}
			}
		}
		// A path that does not fall back to the page gets neither its tag nor a 304.
		for _, path := range []string{"/assets/app.js", "/robots.txt", "/missing.png", "/random", "/api/v1/session", "/healthz"} {
			w := send(s, http.MethodGet, path, publicHost, http.Header{"If-None-Match": {tag}})
			if w.Code == http.StatusNotModified || w.Header().Get("ETag") == tag {
				t.Errorf("%s: %s with the page's tag: %d, ETag %q", name, path, w.Code, w.Header().Get("ETag"))
			}
		}
	}
}

// A folder that is built again serves the new page under a new tag, and the
// old tag no longer gets 304.
func TestANewBuildHasANewTag(t *testing.T) {
	dir := webDir(t)
	s := serving(t, httptransport.Config{WebDir: dir})
	first := send(s, http.MethodGet, "/renewals/r-1", publicHost, nil)
	rebuilt := []byte("<!doctype html><title>" + appMarker + "</title><script type=module src=/assets/next.js></script>")
	if err := os.WriteFile(filepath.Join(dir, "index.html"), rebuilt, 0o644); err != nil {
		t.Fatal(err)
	}
	second := send(s, http.MethodGet, "/renewals/r-1", publicHost, http.Header{"If-None-Match": {first.Header().Get("ETag")}})
	if first.Header().Get("ETag") != tagOf(first.Body.Bytes()) || second.Code != http.StatusOK ||
		second.Body.String() != string(rebuilt) || second.Header().Get("ETag") != tagOf(rebuilt) {
		t.Errorf("after a new build: %d, ETag %q, not %s", second.Code, second.Header().Get("ETag"), tagOf(rebuilt))
	}
}

// Nothing under the API, MCP, discovery or health paths is kept, whatever
// answers: a handler, a refusal before routing, or the redirect of another
// allowed host to the public origin.
func TestNoAPIResponseIsKept(t *testing.T) {
	origin := http.Header{"Origin": {"https://127.0.0.1:8443"}}
	for name, s := range builds(t) {
		for _, c := range []struct {
			method, path, host string
			header             http.Header
			code               int
		}{
			{http.MethodGet, "/api/v1/session", publicHost, nil, http.StatusOK},
			{http.MethodHead, "/api/v1/session", publicHost, nil, http.StatusOK},
			{http.MethodPost, "/api/v1/session", publicHost, origin, http.StatusForbidden},
			{http.MethodPost, "/api/v1/session", publicHost, nil, http.StatusForbidden},
			{http.MethodDelete, "/api/v1/session", publicHost, origin, http.StatusUnauthorized},
			{http.MethodPut, "/api/v1/session", publicHost, origin, http.StatusNotFound},
			{http.MethodPost, "/api/v1/accounts", publicHost, origin, http.StatusNotFound},
			{http.MethodGet, "/api/v1/accounts", publicHost, nil, http.StatusUnauthorized},
			{http.MethodGet, "/api/v1/cards/x", publicHost, nil, http.StatusUnauthorized},
			{http.MethodGet, "/api/v1/cards/x/bytes", publicHost, nil, http.StatusUnauthorized},
			{http.MethodGet, "/api/v1/session", publicHost, http.Header{"Authorization": {"Bearer x"}}, http.StatusUnauthorized},
			{http.MethodGet, "/api/v1/nope", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/api", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/api/", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/api/v1//session", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/mcp", publicHost, nil, http.StatusNotFound},
			{http.MethodPost, "/mcp/x", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/.well-known/oauth-protected-resource", publicHost, nil, http.StatusNotFound},
			{http.MethodGet, "/healthz", publicHost, nil, http.StatusOK},
			{http.MethodPost, "/healthz", publicHost, origin, http.StatusMethodNotAllowed},
			{http.MethodGet, "/api/v1/session", "localhost:8443", nil, http.StatusPermanentRedirect},
			{http.MethodGet, "/api/v1/session", "evil.example", nil, http.StatusMisdirectedRequest},
		} {
			w := send(s, c.method, c.path, c.host, c.header)
			if w.Code != c.code || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
				t.Errorf("%s: %s %s on %s: %d (expected %d), Cache-Control %q, ETag %q", name, c.method, c.path, c.host,
					w.Code, c.code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
			}
		}
	}
}

// The session endpoints and the evidence reads of a signed-in browser are
// not kept either, on a real database: the session view, the sign-in, a card,
// its bytes and the sign-out.
func TestNoSignedInResponseIsKept(t *testing.T) {
	s := newSite(t, 0)
	b := s.browser(t)
	kept := func(what string, r reply, status int) {
		t.Helper()
		if r.status != status || r.header.Get("Cache-Control") != "no-store" || r.header.Get("ETag") != "" {
			t.Errorf("%s: %d (expected %d), Cache-Control %q, ETag %q", what, r.status, status,
				r.header.Get("Cache-Control"), r.header.Get("ETag"))
		}
	}
	kept("the session view", b.start(), http.StatusOK)
	kept("a refused sign-in", b.signIn("demo-reader", "not the password"), http.StatusUnauthorized)
	kept("the sign-in", b.signIn("demo-reader", readerPassword), http.StatusOK)
	kept("the signed-in session view", b.do(http.MethodGet, "/api/v1/session", nil, nil), http.StatusOK)
	kept("the accounts", b.do(http.MethodGet, "/api/v1/accounts", nil, nil), http.StatusOK)
	kept("a card", b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil), http.StatusOK)
	kept("a card's bytes", b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/bytes", nil, nil), http.StatusOK)
	kept("a unit", b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/units/2", nil, nil), http.StatusOK)
	kept("a receipt", b.do(http.MethodGet, "/api/v1/receipts/"+s.Read.ID, nil, nil), http.StatusOK)
	kept("a card that is not granted", b.do(http.MethodGet, "/api/v1/cards/00000000-0000-0000-0000-000000000000", nil, nil),
		http.StatusForbidden)
	kept("the sign-out", b.write(http.MethodDelete, "/api/v1/session", nil), http.StatusOK)
}
