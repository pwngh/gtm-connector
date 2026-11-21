package httptransport_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

const themeScript = `try{document.documentElement.dataset.theme="dark"}catch{}`

// noSessions answers the health check and fails every session lookup.
type noSessions struct{}

func (noSessions) LiveSession(context.Context, []byte) (postgres.Session, error) {
	return postgres.Session{}, postgres.ErrNotFound
}
func (noSessions) SignIn(context.Context, postgres.Admission, []byte, postgres.Principal, time.Duration) (time.Time, error) {
	return time.Time{}, errors.New("no database")
}
func (noSessions) RevokeSession(context.Context, []byte) error { return errors.New("no database") }
func (noSessions) DemoPrincipalByLogin(context.Context, string) (postgres.Principal, []byte, error) {
	return postgres.Principal{}, nil, postgres.ErrNotFound
}
func (noSessions) MigrationsCurrent(context.Context) (bool, error) { return true, nil }

var testKey = []byte("0123456789abcdef0123456789abcdef")

func webDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html": "<!doctype html><script>" + themeScript + "</script><title>" + appMarker +
			"</title><script type=module src=/assets/app.js></script>",
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
	return surfaceOn(t, noSessions{})
}

func surfaceOn(t *testing.T, sessions httptransport.Sessions) *httptransport.Server {
	t.Helper()
	s, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443", "localhost:8443"}, WebDir: webDir(t), SessionKey: testKey},
		sessions, authz.Evidence{})
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
	if w := send(s, http.MethodGet, "/healthz", "127.0.0.1:8443", nil); w.Code != http.StatusOK {
		t.Errorf("the public host: %d", w.Code)
	}
	// Item 11 (Opus): OPTIONS * meets the Host check like any request.
	for host, want := range map[string]int{"evil.example": 421, "127.0.0.1:8443": 404} {
		r := httptest.NewRequest(http.MethodOptions, "https://127.0.0.1:8443/", nil)
		r.RequestURI, r.Host = "*", host
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("OPTIONS * with Host %s: %d", host, w.Code)
		}
	}
}

// Item 8: another allowed host is sent to the public origin, where the
// session cookie lives.
func TestOtherAllowedHostsGoToThePublicOrigin(t *testing.T) {
	s := surface(t)
	for _, host := range []string{"localhost:8443", "LOCALHOST:8443"} {
		w := send(s, http.MethodGet, "/renewals/x?tab=2", host, nil)
		if w.Code != http.StatusPermanentRedirect ||
			w.Header().Get("Location") != "https://127.0.0.1:8443/renewals/x?tab=2" {
			t.Errorf("%s: %d %q", host, w.Code, w.Header().Get("Location"))
		}
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
			if w.Code == http.StatusOK && path != "/healthz" && path != "/api/v1/session" {
				t.Errorf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/", "/renewals/r-1/evidence", "/operations", "/index.html", "/queue",
		"/playbooks"} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), appMarker) ||
			!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("client route %s: %d", path, w.Code)
		}
	}
	// Item 12: only client routes fall back; non-canonical spellings of a
	// reserved path get the API's JSON 404.
	for _, path := range []string{"/random", "/renewalsx", "/admin/x", "/renewals/x.png"} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), appMarker) {
			t.Errorf("not a client route %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1//session", "/api//", "/API/v1/session", "/mcp/../api", "/Healthz",
		"/api/v1/./session", "/renewals//x"} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("non-canonical %s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
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
		"http origin":        {PublicOrigin: "http://127.0.0.1:8443", AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: testKey},
		"origin with a path": {PublicOrigin: "https://127.0.0.1:8443/app", AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: testKey},
		"origin not allowed": {PublicOrigin: "https://127.0.0.1:8443", AllowedHosts: []string{"localhost:8443"}, SessionKey: testKey},
		"no hosts":           {PublicOrigin: "https://127.0.0.1:8443", SessionKey: testKey},
		"no session key":     {PublicOrigin: "https://127.0.0.1:8443", AllowedHosts: []string{"127.0.0.1:8443"}},
	} {
		if _, err := httptransport.New(cfg, noSessions{}, authz.Evidence{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Item 12: every response carries the CSP; the shell's inline theme script
// is allowed by its hash and no other inline script is; content types come
// from a fixed table.
func TestEveryDocumentIsUnderThePolicy(t *testing.T) {
	dir := webDir(t)
	os.WriteFile(filepath.Join(dir, "assets", "logo.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644)
	os.WriteFile(filepath.Join(dir, "report.html"), []byte(`<script>alert(1)</script>`), 0o644)
	os.WriteFile(filepath.Join(dir, "assets", "blob.xyz123"), []byte(`<html><script>alert(1)</script>`), 0o644)
	s, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443"}, WebDir: dir, SessionKey: testKey}, noSessions{}, authz.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum := sha256.Sum256([]byte(themeScript))
	allowed := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for path, kind := range map[string]string{
		"/assets/logo.svg": "image/svg+xml", "/report.html": "text/html; charset=utf-8",
		"/assets/blob.xyz123": "application/octet-stream", "/assets/app.js": "text/javascript; charset=utf-8",
		"/": "text/html; charset=utf-8", "/api/v1/nope": "application/json", "/robots.txt": "text/plain; charset=utf-8",
	} {
		w := send(s, http.MethodGet, path, "127.0.0.1:8443", nil)
		policy := w.Header().Get("Content-Security-Policy")
		if w.Header().Get("Content-Type") != kind || !strings.Contains(policy, "script-src 'self' "+allowed+";") ||
			strings.Contains(policy, "unsafe-inline' https://fonts.gstatic") || !strings.Contains(policy, "https://fonts.googleapis.com") {
			t.Errorf("%s: %q, policy %q", path, w.Header().Get("Content-Type"), policy)
		}
	}
}

// Item 13: the sign-in body is exactly one JSON object.
func TestTheSignInBodyIsOneObject(t *testing.T) {
	s := surface(t)
	first := send(s, http.MethodGet, "/api/v1/session", "127.0.0.1:8443", nil)
	var view struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(first.Body.Bytes(), &view)
	cookie := first.Header().Get("Set-Cookie")
	cookie = cookie[:strings.Index(cookie, ";")]
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "https://127.0.0.1:8443/api/v1/session", strings.NewReader(body))
		r.Host = "127.0.0.1:8443"
		r.Header.Set("Origin", "https://127.0.0.1:8443")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", view.CSRF)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	for _, body := range []string{`{"login":"a","password":"b"}}`, `{"login":"a","password":"b"}{}`,
		`{"login":"a","password":"b"} []`, `null`, `[]`, `"x"`, `{"login":"a","password":"b","x":1}`,
		// As the strict reader reads it: a field twice, a field in another case, a field of another type,
		// bytes that are not UTF-8, and a number no float holds are each refused.
		`{"login":"a","password":"b","password":"c"}`, `{"login":"a","login":"a","password":"b"}`,
		`{"Login":"a","password":"b"}`, `{"login":"a","PASSWORD":"b"}`, `{"login":null,"password":"b"}`,
		`{"login":"a","password":1}`, "{\"login\":\"a\xff\",\"password\":\"b\"}", `{"login":"a","password":"b"} NaN`,
		`{"login":"a","password":"\ud800"}`} {
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	// The reader's depth limit holds inside the body's size limit.
	if code := post(`{"login":` + strings.Repeat("[", 1001) + strings.Repeat("]", 1001) + `}`); code != http.StatusBadRequest {
		t.Errorf("a body nested past the limit: %d", code)
	}
	if code := post(`{"login":"a","password":"b"}` + "\n"); code != http.StatusUnauthorized {
		t.Errorf("one object: %d", code)
	}
}

// Item 8: visitors without a session cost no database work.
func TestPreSessionsAreStateless(t *testing.T) {
	s := surface(t) // noSessions fails any write
	for range 50 {
		w := send(s, http.MethodGet, "/api/v1/session", "127.0.0.1:8443", nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Set-Cookie"), "__Host-gtm-pre=") {
			t.Fatalf("GET session: %d", w.Code)
		}
	}
	if _, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: []byte("short")}, noSessions{}, authz.Evidence{}); err == nil {
		t.Fatal("a short session key was accepted")
	}
}
