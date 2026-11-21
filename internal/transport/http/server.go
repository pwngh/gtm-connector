// Package httptransport is the browser surface of gtm-connector serve: the
// JSON API under /api/v1, the deal routes under /v1/deals, the web app's
// build with history fallback, and /healthz. Every request's Host must be on a configured list, and every
// browser write must come from the configured public origin with the
// session's CSRF token. It also mounts the MCP gateway at /mcp, which makes
// every admission decision of its own.
package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// Config is what the browser surface needs to know about its install.
type Config struct {
	// PublicOrigin is the one origin browser writes may come from, such as
	// https://127.0.0.1:8443. It is configured, never derived from a request.
	PublicOrigin string
	// AllowedHosts are the Host header values served, such as
	// 127.0.0.1:8443 and localhost:8443. Anything else is refused, which
	// also blocks DNS rebinding.
	AllowedHosts []string
	// Web is the web app's build (index.html and assets/), such as the one
	// inside the binary. Its files must be seekable, as an embed.FS's are.
	// Nil serves the API without the app.
	Web fs.FS
	// WebDir, when set, is a folder holding the build, served instead of Web.
	WebDir string
	// SessionTTL is how long a signed-in session lasts; PreSessionTTL how
	// long a sign-in CSRF token does.
	SessionTTL, PreSessionTTL time.Duration
	// SessionKey signs pre-session tokens, so a visitor without a session
	// costs no database row. At least 32 bytes, from the credentials folder.
	SessionKey []byte
	// ClientRoutes are the path prefixes the web app routes itself; only
	// they fall back to index.html. Empty means DefaultClientRoutes.
	ClientRoutes []string
	// MCP, when set, serves /mcp and the protected-resource metadata that
	// describes it. It does its own admission checks, the Host among them,
	// and writes the receipt of every call it is sent.
	MCP http.Handler
	// Deals, when set, registers deals for POST /v1/deals. Without it that
	// route answers 404.
	Deals DealRegistry
	// OIDC, when set, signs the people of one workspace in through their
	// company's identity provider.
	OIDC *OIDC
	// DemoSignInOff turns the demo sign-in off: it signs nobody in, and a
	// demo person's session opens nothing.
	DemoSignInOff bool
	// Log, when set, is told each sign-in through the identity provider,
	// kept or refused, in fixed words.
	Log *slog.Logger
}

// DefaultClientRoutes are the web app's routes: "/" and these prefixes,
// each with anything below it.
var DefaultClientRoutes = []string{"/deals", "/ops"}

// Sessions is the storage the surface needs: sessions, and for /healthz
// how the applied migrations stand against this binary's.
type Sessions interface {
	LiveSession(ctx context.Context, idSHA256 []byte) (postgres.Session, error)
	SignIn(ctx context.Context, admitted postgres.Admission, idSHA256 []byte, principal postgres.Principal, ttl time.Duration) (time.Time, error)
	SignInPerson(ctx context.Context, admitted postgres.Admission, idSHA256 []byte, w postgres.Workspace,
		who postgres.PersonIdentity, ttl time.Duration, previous []byte) (postgres.Principal, time.Time, error)
	PreSessionUsed(ctx context.Context, idSHA256 []byte) (bool, error)
	RevokeSession(ctx context.Context, idSHA256 []byte) error
	DemoPrincipalByLogin(ctx context.Context, login string) (postgres.Principal, []byte, error)
	MigrationState(ctx context.Context) (postgres.MigrationState, error)
}

// Server is the browser surface.
type Server struct {
	cfg      Config
	sessions Sessions
	evidence authz.Evidence
	hosts    map[string]bool
	host     string // the public origin's host
	web      fs.FS
	webDir   *os.Root // the folder behind web, when it is one
	policy   string   // the Content-Security-Policy of every response
	mux      *http.ServeMux
}

// New checks cfg and builds the surface.
func New(cfg Config, sessions Sessions, evidence authz.Evidence) (*Server, error) {
	origin, err := url.Parse(cfg.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" ||
		origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return nil, fmt.Errorf("the public origin must be https://host[:port], not %q", cfg.PublicOrigin)
	}
	if len(cfg.AllowedHosts) == 0 {
		return nil, errors.New("at least one allowed host is required")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.PreSessionTTL <= 0 {
		cfg.PreSessionTTL = time.Hour
	}
	if len(cfg.SessionKey) < 32 {
		return nil, errors.New("the session key must be at least 32 bytes")
	}
	if len(cfg.ClientRoutes) == 0 {
		cfg.ClientRoutes = DefaultClientRoutes
	}
	if o := cfg.OIDC; o != nil {
		switch {
		case o.Client == nil || o.Workspace.ID == "":
			return nil, errors.New("sign-in through an identity provider needs its client and its workspace")
		case o.Workspace.Kind != "demo" && !cfg.DemoSignInOff:
			return nil, fmt.Errorf("the demo sign-in is on beside sign-in through the identity provider for the "+
				"live workspace %s; set GTM_DEMO_SIGN_IN=off", o.Workspace.Slug)
		case o.Client.RedirectURL() != cfg.PublicOrigin+OIDCCallbackPath:
			return nil, fmt.Errorf("the identity provider's redirect URL must be the public origin followed by %s",
				OIDCCallbackPath)
		case o.SessionTTL <= 0:
			o.SessionTTL = DefaultOIDCSessionTTL
		}
	}
	s := &Server{cfg: cfg, sessions: sessions, evidence: evidence, hosts: map[string]bool{},
		host: strings.ToLower(origin.Host), policy: fmt.Sprintf(basePolicy, ""), web: cfg.Web}
	for _, host := range cfg.AllowedHosts {
		s.hosts[strings.ToLower(host)] = true
	}
	if !s.hosts[strings.ToLower(origin.Host)] {
		return nil, fmt.Errorf("the public origin's host %s is not an allowed host", origin.Host)
	}
	if cfg.WebDir != "" {
		root, err := os.OpenRoot(cfg.WebDir)
		if err != nil {
			return nil, fmt.Errorf("web app folder: %w", err)
		}
		s.webDir, s.web = root, root.FS()
	}
	if s.web != nil {
		if s.policy, err = policyFor(s.web); err != nil {
			s.Close()
			return nil, err
		}
	}
	s.routes()
	return s, nil
}

// Close releases the web app folder.
func (s *Server) Close() error {
	if s.webDir != nil {
		return s.webDir.Close()
	}
	return nil
}

// route is one API route. A write's handler is admitted by Server.write.
type route struct {
	pattern string
	read    http.HandlerFunc
	signIn  bool
	write   func(http.ResponseWriter, *http.Request, admission)
}

// apiRoutes are every /api/v1 and /v1 route. Only a write route answers a method
// other than GET (and HEAD), so every write passes the CSRF check.
func (s *Server) apiRoutes() []route {
	return []route{
		{pattern: "GET /api/v1/session", read: s.getSession},
		{pattern: "POST /api/v1/session", signIn: true, write: s.signIn},
		{pattern: "DELETE /api/v1/session", write: s.signOut},
		{pattern: "GET " + OIDCStartPath, read: s.startOIDC},
		{pattern: "GET " + OIDCCallbackPath, read: s.finishOIDC},
		{pattern: "GET /api/v1/accounts", read: s.signedIn(s.accounts)},
		{pattern: "GET /api/v1/accounts/{account}/cards", read: s.signedIn(s.accountCards)},
		{pattern: "GET /api/v1/cards/{card}", read: s.signedIn(s.card)},
		{pattern: "GET /api/v1/cards/{card}/bytes", read: s.signedIn(s.cardBytes)},
		{pattern: "GET /api/v1/cards/{card}/text", read: s.signedIn(s.cardText)},
		{pattern: "GET /api/v1/cards/{card}/units/{n}", read: s.signedIn(s.unit)},
		{pattern: "GET /api/v1/receipts/{receipt}", read: s.signedIn(s.receipt)},
		{pattern: "GET /v1/deals", read: s.signedIn(s.deals)},
		{pattern: "POST /v1/deals", write: s.registerDeal},
		{pattern: "GET /v1/deals/{deal}", read: s.signedIn(s.deal)},
	}
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	for _, r := range s.apiRoutes() {
		if r.write != nil {
			mux.HandleFunc(r.pattern, s.write(r.signIn, r.write))
		} else {
			mux.HandleFunc(r.pattern, r.read)
		}
	}
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	}
	// Both spellings, so ServeMux never answers with an HTML redirect.
	for _, pattern := range []string{"/api", "/api/", "/v1", "/v1/", "/.well-known", "/.well-known/"} {
		mux.HandleFunc(pattern, notFound)
	}
	mux.HandleFunc("/healthz", s.healthz)
	mcp := s.cfg.MCP
	if mcp == nil {
		mcp = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "MCP is not served here yet.")
		})
	}
	mux.Handle("/mcp", mcp)
	mux.Handle("/mcp/", mcp)
	if s.cfg.MCP != nil {
		for _, pattern := range []string{mcpMetadata, mcpMetadata + "/mcp"} {
			mux.Handle(pattern, mcp)
		}
	}
	mux.HandleFunc("/", s.serveApp)
	s.mux = mux
}

// ServeHTTP checks the Host, sends other allowed hosts to the public origin,
// refuses non-canonical paths, checks the origin of any browser write, then
// routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Content-Security-Policy", s.policy)
	if underReserved(r.URL.Path) {
		// No answer under the API, MCP, discovery or health paths is kept by a
		// browser or a proxy, whoever writes it: a handler, the router, or
		// the redirect to the public origin.
		header.Set("Cache-Control", "no-store")
	}
	if s.cfg.MCP != nil && r.URL.Path == "/mcp" {
		// The gateway admits only the public origin's own Host, with no
		// Origin header, and refuses anything else itself. It is asked before
		// the checks below so that a call refused for its Host gets its
		// receipt, as every refused call does.
		s.cfg.MCP.ServeHTTP(w, r)
		return
	}
	host := strings.ToLower(r.Host)
	if !s.hosts[host] {
		writeError(w, http.StatusMisdirectedRequest, "untrusted_host", "This Host is not served here.")
		return
	}
	if r.RequestURI == "*" {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	if host != s.host {
		// One origin holds the session cookie, so every allowed alias leads there.
		http.Redirect(w, r, s.cfg.PublicOrigin+r.URL.RequestURI(), http.StatusPermanentRedirect)
		return
	}
	if !canonical(r.URL.Path) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	// The API takes the session cookie and nothing else. A request that also
	// carries a bearer token is refused, not served by the cookie: the browser
	// and MCP surfaces never share credentials.
	if underAPI(r.URL.Path) && len(r.Header.Values("Authorization")) > 0 {
		writeError(w, http.StatusUnauthorized, "authorization_refused",
			"The API signs in with its session cookie; it takes no Authorization header.")
		return
	}
	if isWrite(r.Method) && !isMCP(r.URL.Path) {
		if code, message := s.checkOrigin(r); code != "" {
			writeError(w, http.StatusForbidden, code, message)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// canonical admits a path that ServeMux would not rewrite: it starts with
// "/", has no empty, "." or ".." segment (a final "/" aside), and names a
// reserved prefix only in lower case.
func canonical(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	trimmed := strings.TrimSuffix(p[1:], "/")
	if trimmed != "" {
		for _, segment := range strings.Split(trimmed, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return false
			}
		}
	}
	lower := strings.ToLower(p)
	for _, prefix := range reserved {
		if (lower == prefix || strings.HasPrefix(lower, prefix+"/")) && !strings.HasPrefix(p, prefix) {
			return false
		}
	}
	return true
}

// underAPI is whether a path is under the API: /api or /v1.
func underAPI(p string) bool {
	for _, prefix := range []string{"/api", "/v1"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func isMCP(path string) bool { return path == "/mcp" || strings.HasPrefix(path, "/mcp/") }

// mcpMetadata is where a client finds the protected-resource metadata of
// the MCP endpoint, alone or followed by the endpoint's path.
const mcpMetadata = "/.well-known/oauth-protected-resource"

// checkOrigin requires exactly one Origin header equal to the public origin.
// A missing or "null" Origin is refused: a write must say where it came from.
func (s *Server) checkOrigin(r *http.Request) (code, message string) {
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) == 0:
		return "origin_required", "A write must carry an Origin header."
	case len(origins) > 1:
		return "origin_refused", "A write must carry one Origin header."
	case origins[0] == "null":
		return "origin_refused", "A write from an opaque origin is refused."
	case origins[0] != s.cfg.PublicOrigin:
		return "origin_refused", "A write from another origin is refused."
	}
	return "", ""
}

// healthz answers ok when the database answers and its applied migrations
// are this binary's. Otherwise it answers 503 with one of four fixed
// statuses, never with the error, which can name the database:
// migrations_behind, migrations_ahead and migrations_changed are the states
// serve refuses to start on, and database_unavailable is no answer.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	code, status := http.StatusServiceUnavailable, "database_unavailable"
	switch state, err := s.sessions.MigrationState(ctx); {
	case err != nil:
	case state == postgres.MigrationsCurrent:
		code, status = http.StatusOK, "ok"
	default:
		status = "migrations_" + string(state)
	}
	writeJSON(w, code, map[string]string{"status": status})
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {code, message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":{"code":"internal","message":"Internal error."}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}
