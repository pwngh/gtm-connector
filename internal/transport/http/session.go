package httptransport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The cookies. The __Host- prefix makes the browser require Secure, Path=/
// and no Domain, so no other host can set or read them. Browsers do not
// separate cookies by port, so another server on 127.0.0.1 sees them too.
const (
	cookieName    = "__Host-gtm-session"
	preCookieName = "__Host-gtm-pre"
)

// csrfHeader carries the CSRF token on every write.
const csrfHeader = "X-CSRF-Token"

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func newToken() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// csrfFor derives a session's or pre-session's CSRF token from its ID, so it
// rotates with the ID and a token from an earlier session never matches.
func csrfFor(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("gtm-connector csrf v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// A pre-session is a signed token: its expiry and a random nonce, with an
// HMAC under the install's session key. It needs no database row; sign-in
// records its digest once, when it consumes it.

func (s *Server) preMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.cfg.SessionKey)
	mac.Write([]byte("gtm-connector pre-session v1\x00"))
	mac.Write(payload)
	return mac.Sum(nil)
}

func (s *Server) newPreSession() (string, time.Time) {
	expires := time.Now().Add(s.cfg.PreSessionTTL).Truncate(time.Second)
	payload := make([]byte, 8+16)
	binary.BigEndian.PutUint64(payload, uint64(expires.Unix()))
	rand.Read(payload[8:])
	return "p1." + base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(s.preMAC(payload)), expires
}

// cookieBase64 reads a signed cookie's parts: unpadded base64url whose
// unused bits are zero, so each part has one spelling and a cookie that is
// used up cannot come back respelled.
var cookieBase64 = base64.RawURLEncoding.Strict()

// preSession checks a pre-session token's signature and expiry.
func (s *Server) preSession(r *http.Request) (string, time.Time, bool) {
	cookie, err := r.Cookie(preCookieName)
	if err != nil {
		return "", time.Time{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] != "p1" {
		return "", time.Time{}, false
	}
	payload, err1 := cookieBase64.DecodeString(parts[1])
	mac, err2 := cookieBase64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || len(payload) != 24 || !hmac.Equal(mac, s.preMAC(payload)) {
		return "", time.Time{}, false
	}
	expires := time.Unix(int64(binary.BigEndian.Uint64(payload)), 0)
	if !time.Now().Before(expires) {
		return "", time.Time{}, false
	}
	return cookie.Value, expires, true
}

// current gives the request's live signed-in session and its token, if any. A session of a sign-in this
// install does not offer now is no session: a demo person's while the demo sign-in is off, and a
// provider's person's unless the provider signs in that person's workspace here.
func (s *Server) current(r *http.Request) (string, postgres.Session, bool, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || !tokenPattern.MatchString(cookie.Value) {
		return "", postgres.Session{}, false, nil
	}
	session, err := s.sessions.LiveSession(r.Context(), digest(cookie.Value))
	if errors.Is(err, postgres.ErrNotFound) {
		return "", postgres.Session{}, false, nil
	}
	if err != nil {
		return "", postgres.Session{}, false, err
	}
	p := session.Principal
	if (p.Login != "" && s.cfg.DemoSignInOff) || (p.Issuer != "" && (s.cfg.OIDC == nil ||
		p.Issuer != s.cfg.OIDC.Client.Issuer() || p.WorkspaceID != s.cfg.OIDC.Workspace.ID)) {
		return "", postgres.Session{}, false, nil
	}
	return cookie.Value, session, true, nil
}

func setCookie(w http.ResponseWriter, name, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", Expires: expires,
		MaxAge: max(1, int(time.Until(expires).Seconds())), HttpOnly: true, Secure: true,
		// Lax, so a link from Slack to the app keeps the session.
		SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: true, SameSite: http.SameSiteLaxMode})
}

type principalView struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Kind          string `json:"kind"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceKind string `json:"workspace_kind"`
	// Demo is true for a demo principal, which the app must show as such.
	Demo bool `json:"demo"`
}

type sessionView struct {
	SignedIn  bool           `json:"signed_in"`
	Principal *principalView `json:"principal"`
	CSRFToken string         `json:"csrf_token,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	// SignIn is how a person signs in here, on every session view but a sign-out's.
	SignIn *signInView `json:"sign_in,omitempty"`
	// EndSessionURL is the identity provider's own sign-out page, which a sign-out of a person who signed
	// in through it offers when the provider publishes one.
	EndSessionURL string `json:"end_session_url,omitempty"`
}

// signInView is the ways in this install offers: through the identity provider, and the demo sign-in.
type signInView struct {
	OIDC bool `json:"oidc"`
	Demo bool `json:"demo"`
}

func (s *Server) viewOf(p *postgres.Principal, token string, expires time.Time) sessionView {
	view := sessionView{CSRFToken: csrfFor(token), ExpiresAt: expires.UTC().Format(time.RFC3339),
		SignIn: &signInView{OIDC: s.cfg.OIDC != nil, Demo: !s.cfg.DemoSignInOff}}
	if p != nil {
		view.SignedIn = true
		view.Principal = &principalView{ID: p.ID, DisplayName: p.DisplayName, Kind: p.Kind,
			WorkspaceID: p.WorkspaceID, WorkspaceKind: p.WorkspaceKind, Demo: p.WorkspaceKind == "demo"}
	}
	return view
}

// getSession reports the session and its CSRF token. Without a live session
// it hands out a signed pre-session, whose token is good only for signing
// in; this writes nothing to the database.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	token, session, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if ok {
		writeJSON(w, http.StatusOK, s.viewOf(session.Principal, token, session.ExpiresAt))
		return
	}
	pre, expires, ok := s.preSession(r)
	if !ok {
		pre, expires = s.newPreSession()
		setCookie(w, preCookieName, pre, expires)
	}
	writeJSON(w, http.StatusOK, s.viewOf(nil, pre, expires))
}

// csrfValid checks the CSRF header against a session's token: exactly one
// header, equal to the token derived from that session.
func csrfValid(r *http.Request, token string) bool {
	given := r.Header.Values(csrfHeader)
	return len(given) == 1 && subtle.ConstantTimeCompare([]byte(given[0]), []byte(csrfFor(token))) == 1
}

// admission is what admitted a write: a live session, its token and its
// principal, or, for sign-in only, a signed pre-session (token empty and no
// principal).
type admission struct {
	token     string
	stored    postgres.Admission
	principal *postgres.Principal
}

// write admits a write by its CSRF token, which must be the live session's
// or, where signIn is set, a signed pre-session's. A write that needs a
// session and has none gets 401; any other failure 403. Every write route in
// apiRoutes is served through it.
func (s *Server) write(signIn bool, next func(http.ResponseWriter, *http.Request, admission)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, session, live, err := s.current(r)
		if err != nil {
			internal(w)
			return
		}
		switch {
		case live && csrfValid(r, token):
			next(w, r, admission{token, postgres.Admission{IDSHA256: digest(token)}, session.Principal})
			return
		case signIn:
			if pre, expires, ok := s.preSession(r); ok && csrfValid(r, pre) {
				next(w, r, admission{stored: postgres.Admission{PreSession: true, IDSHA256: digest(pre),
					ExpiresAt: expires}})
				return
			}
		case !live:
			writeError(w, http.StatusUnauthorized, "sign_in_required", "Sign in first.")
			return
		}
		writeError(w, http.StatusForbidden, "csrf_invalid",
			"The CSRF token is missing or stale. Get one from GET /api/v1/session and send it in X-CSRF-Token.")
	}
}

type signInRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// signIn signs in a demo principal with its per-install credential. It is
// admitted by the CSRF token of a live session or of a signed pre-session,
// and the rotation consumes exactly that one, atomically: a pre-session
// signs in once, and a session revoked meanwhile signs in nobody. While the
// demo sign-in is off it signs nobody in.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, admitted admission) {
	if s.cfg.DemoSignInOff {
		writeError(w, http.StatusForbidden, "demo_sign_in_off",
			"The demo sign-in is off on this install. Sign in with your company account.")
		return
	}
	var request signInRequest
	if !readJSON(w, r, &request) {
		return
	}
	principal, stored, err := s.sessions.DemoPrincipalByLogin(r.Context(), request.Login)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		internal(w)
		return
	}
	given := digest(request.Password)
	if stored == nil {
		stored = make([]byte, len(given)) // compare anyway, so timing says nothing
	}
	if subtle.ConstantTimeCompare(given, stored) != 1 || err != nil || request.Password == "" {
		writeError(w, http.StatusUnauthorized, "sign_in_refused", "The login or password is not right.")
		return
	}
	next := newToken()
	expires, err := s.sessions.SignIn(r.Context(), admitted.stored, digest(next), principal, s.cfg.SessionTTL)
	if errors.Is(err, postgres.ErrSessionUsed) {
		writeError(w, http.StatusForbidden, "csrf_invalid",
			"That session was already used or ended. Get a new token from GET /api/v1/session.")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	clearCookie(w, preCookieName)
	setCookie(w, cookieName, next, expires)
	writeJSON(w, http.StatusOK, s.viewOf(&principal, next, expires))
}

// signOut ends the session here. It is a protected write like any other.
// For a person who signed in through the identity provider, it also offers
// the provider's own sign-out page, when the provider publishes one: the
// provider's session is the provider's to end.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request, admitted admission) {
	if err := s.sessions.RevokeSession(r.Context(), digest(admitted.token)); err != nil {
		internal(w)
		return
	}
	clearCookie(w, cookieName)
	view := sessionView{}
	if s.cfg.OIDC != nil && admitted.principal != nil && admitted.principal.Issuer != "" {
		view.EndSessionURL = s.cfg.OIDC.Client.EndSessionURL(r.Context())
	}
	writeJSON(w, http.StatusOK, view)
}

// signedIn admits only a live, signed-in session, and passes its principal on.
func (s *Server) signedIn(next func(http.ResponseWriter, *http.Request, authz.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, session, ok, err := s.current(r)
		if err != nil {
			internal(w)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign_in_required", "Sign in first.")
			return
		}
		next(w, r, authz.Principal{WorkspaceID: session.Principal.WorkspaceID, ID: session.Principal.ID})
	}
}

const maxBody = 4096

// readJSON decodes exactly one JSON object from a JSON body of at most
// maxBody bytes, as strictjson reads it: no field named twice, no field the
// request does not document, each field of its own type and spelled exactly,
// and nothing after the object.
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send application/json.")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil || strictjson.Decode(body, value) != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object with the documented fields.")
		return false
	}
	return true
}

func internal(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal", "Internal error.")
}
