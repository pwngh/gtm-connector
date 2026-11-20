package httptransport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// The session cookie. The __Host- prefix makes the browser require Secure,
// Path=/ and no Domain, so no other host can set or read it.
const cookieName = "__Host-gtm-session"

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

// csrfFor derives a session's CSRF token from its ID, so it rotates with the
// ID and a token from an earlier session never matches.
func csrfFor(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("gtm-connector csrf v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// current gives the request's live session and its token, if any.
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
	return cookie.Value, session, true, nil
}

func setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: expires,
		MaxAge: max(1, int(time.Until(expires).Seconds())), HttpOnly: true, Secure: true,
		// Lax, so a link from Slack to the app keeps the session.
		SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
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
}

func viewOf(p *postgres.Principal, token string, expires time.Time) sessionView {
	view := sessionView{CSRFToken: csrfFor(token), ExpiresAt: expires.UTC().Format(time.RFC3339)}
	if p != nil {
		view.SignedIn = true
		view.Principal = &principalView{ID: p.ID, DisplayName: p.DisplayName, Kind: p.Kind,
			WorkspaceID: p.WorkspaceID, WorkspaceKind: p.WorkspaceKind, Demo: p.WorkspaceKind == "demo"}
	}
	return view
}

// getSession reports the session and its CSRF token. Without a live session
// it starts a pre-session, whose token is good only for signing in.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	token, session, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if !ok {
		token = newToken()
		expires, err := s.sessions.CreateSession(r.Context(), digest(token), nil, s.cfg.PreSessionTTL)
		if err != nil {
			internal(w)
			return
		}
		setCookie(w, token, expires)
		writeJSON(w, http.StatusOK, viewOf(nil, token, expires))
		return
	}
	writeJSON(w, http.StatusOK, viewOf(session.Principal, token, session.ExpiresAt))
}

// csrfValid checks the CSRF header against the session's token.
func csrfValid(r *http.Request, token string) bool {
	given := r.Header.Values(csrfHeader)
	return len(given) == 1 && subtle.ConstantTimeCompare([]byte(given[0]), []byte(csrfFor(token))) == 1
}

type signInRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// signIn signs in a demo principal with its per-install credential. The old
// session ends and a new one, with a new ID and CSRF token, takes its place.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	token, _, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if !ok || !csrfValid(r, token) {
		writeError(w, http.StatusForbidden, "csrf_invalid",
			"Get a CSRF token from GET /api/v1/session and send it in X-CSRF-Token.")
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
	expires, err := s.sessions.SignIn(r.Context(), digest(token), digest(next), principal, s.cfg.SessionTTL)
	if err != nil {
		internal(w)
		return
	}
	setCookie(w, next, expires)
	writeJSON(w, http.StatusOK, viewOf(&principal, next, expires))
}

// signOut ends the session. It is a protected write like any other.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	token, session, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if !ok || session.Principal == nil {
		writeError(w, http.StatusUnauthorized, "sign_in_required", "Sign in first.")
		return
	}
	if !csrfValid(r, token) {
		writeError(w, http.StatusForbidden, "csrf_invalid", "The CSRF token is missing or stale.")
		return
	}
	if err := s.sessions.RevokeSession(r.Context(), digest(token)); err != nil {
		internal(w)
		return
	}
	clearCookie(w)
	writeJSON(w, http.StatusOK, sessionView{})
}

// signedIn admits only a live, signed-in session, and passes its principal on.
func (s *Server) signedIn(next func(http.ResponseWriter, *http.Request, authz.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, session, ok, err := s.current(r)
		if err != nil {
			internal(w)
			return
		}
		if !ok || session.Principal == nil {
			writeError(w, http.StatusUnauthorized, "sign_in_required", "Sign in first.")
			return
		}
		next(w, r, authz.Principal{WorkspaceID: session.Principal.WorkspaceID, ID: session.Principal.ID})
	}
}

const maxBody = 4096

// readJSON decodes one JSON object with no unknown fields from a JSON body.
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil || decoder.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object with the documented fields.")
		return false
	}
	return true
}

func internal(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal", "Internal error.")
}
