package httptransport_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// These tests need no database: each request is decided before storage.

const appMarker = "APP-HTML-MARKER"

// noSessions answers Ping and fails every session lookup.
type noSessions struct{}

func (noSessions) CreateSession(context.Context, []byte, *postgres.Principal, time.Duration) (time.Time, error) {
	return time.Time{}, errors.New("no database")
}
func (noSessions) LiveSession(context.Context, []byte) (postgres.Session, error) {
	return postgres.Session{}, postgres.ErrNotFound
}
func (noSessions) SignIn(context.Context, []byte, []byte, postgres.Principal, time.Duration) (time.Time, error) {
	return time.Time{}, errors.New("no database")
}
func (noSessions) RevokeSession(context.Context, []byte) error { return errors.New("no database") }
func (noSessions) DemoPrincipalByLogin(context.Context, string) (postgres.Principal, []byte, error) {
	return postgres.Principal{}, nil, postgres.ErrNotFound
}
func (noSessions) Ping(context.Context) error { return nil }

func webDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":     "<!doctype html><title>" + appMarker + "</title><script type=module src=/assets/app.js></script>",
		"assets/app.js":  "console.log(1)",
		"robots.txt":     "User-agent: *",
		".env":           "SECRET=1",
		"assets/.hidden": "x",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func surface(t *testing.T) *httptransport.Server {
	t.Helper()
	s, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443", "localhost:8443"}, WebDir: webDir(t)},
		noSessions{}, authz.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func send(s http.Handler, method, path, host string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://127.0.0.1:8443"+path, nil)
	r.Host = host
	for name, values := range header {
		r.Header[name] = values
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestWrongHostIsRefused(t *testing.T) {
	s := surface(t)
	for _, host := range []string{"evil.example", "evil.example:8443", "127.0.0.1:9999", "127.0.0.1",
		"rebind.example:8443", "127.0.0.1:8443.evil.example", ""} {
		for _, path := range []string{"/", "/renewals/x", "/api/v1/session", "/healthz", "/mcp"} {
			w := send(s, http.MethodGet, path, host, nil)
			if w.Code != http.StatusMisdirectedRequest || strings.Contains(w.Body.String(), appMarker) ||
				!strings.Contains(w.Body.String(), "untrusted_host") {
				t.Errorf("Host %q %s: %d %q", host, path, w.Code, w.Body.String())
			}
		}
	}
	if w := send(s, http.MethodGet, "/healthz", "LOCALHOST:8443", nil); w.Code != http.StatusOK {
		t.Errorf("an allowed host in capitals: %d", w.Code)
	}
}

func TestReservedPathsNeverServeTheApp(t *testing.T) {
	s := surface(t)
	for _, path := range []string{"/api", "/api/", "/api/v1", "/api/v1/unknown", "/api/v1/session",
		"/api/v1/cards/x", "/mcp", "/mcp/", "/mcp/anything", "/.well-known/", "/.well-known/oauth-protected-resource",
		"/healthz", "/healthz/", "/assets", "/assets/", "/assets/missing.js", "/assets/.hidden", "/missing.png",
		"/favicon.ico", "/.env", "/renewals/.env", "/index.html/x.js"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := send(s, method, path, "127.0.0.1:8443", nil)
			if strings.Contains(w.Body.String(), appMarker) || strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Errorf("%s %s returned the app's HTML (%d)", method, path, w.Code)
			}
			if w.Code == http.StatusOK && path != "/healthz" {
				t.Errorf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/", "/renewals/r-1/evidence", "/operations", "/index.html"} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), appMarker) ||
			!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("client route %s: %d", path, w.Code)
		}
	}
	w := send(s, http.MethodGet, "/assets/app.js", "127.0.0.1:8443", nil)
	if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" ||
		!strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Errorf("asset: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := send(s, http.MethodPost, "/renewals/x", "127.0.0.1:8443",
		http.Header{"Origin": {"https://127.0.0.1:8443"}}); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to a client route: %d", w.Code)
	}
}

func TestWritesNeedTheConfiguredOrigin(t *testing.T) {
	s := surface(t)
	refused := map[string]http.Header{
		"missing":       nil,
		"null":          {"Origin": {"null"}},
		"cross-origin":  {"Origin": {"https://evil.example"}},
		"plain http":    {"Origin": {"http://127.0.0.1:8443"}},
		"other port":    {"Origin": {"https://127.0.0.1:9443"}},
		"other alias":   {"Origin": {"https://localhost:8443"}},
		"trailing path": {"Origin": {"https://127.0.0.1:8443/"}},
		"two origins":   {"Origin": {"https://127.0.0.1:8443", "https://evil.example"}},
	}
	for name, header := range refused {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut, http.MethodPatch} {
			w := send(s, method, "/api/v1/session", "127.0.0.1:8443", header)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "origin_") {
				t.Errorf("%s %s: %d %s", name, method, w.Code, w.Body.String())
			}
		}
	}
	// The configured origin reaches the handler, which then wants a CSRF token.
	w := send(s, http.MethodPost, "/api/v1/session", "127.0.0.1:8443", http.Header{"Origin": {"https://127.0.0.1:8443"}})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "csrf_invalid") {
		t.Errorf("same origin: %d %s", w.Code, w.Body.String())
	}
	// /mcp makes its own admission decisions, so it is not origin-checked here.
	if w := send(s, http.MethodPost, "/mcp", "127.0.0.1:8443", nil); strings.Contains(w.Body.String(), "origin_") {
		t.Errorf("/mcp was origin-checked: %s", w.Body.String())
	}
}

func TestConfigIsChecked(t *testing.T) {
	for name, cfg := range map[string]httptransport.Config{
		"http origin":        {PublicOrigin: "http://127.0.0.1:8443", AllowedHosts: []string{"127.0.0.1:8443"}},
		"origin with a path": {PublicOrigin: "https://127.0.0.1:8443/app", AllowedHosts: []string{"127.0.0.1:8443"}},
		"origin not allowed": {PublicOrigin: "https://127.0.0.1:8443", AllowedHosts: []string{"localhost:8443"}},
		"no hosts":           {PublicOrigin: "https://127.0.0.1:8443"},
	} {
		if _, err := httptransport.New(cfg, noSessions{}, authz.Evidence{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
