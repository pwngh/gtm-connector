package httptransport

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
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
	payload, err1 := base64.RawURLEncoding.DecodeString(parts[1])
	mac, err2 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || len(payload) != 24 || !hmac.Equal(mac, s.preMAC(payload)) {
		return "", time.Time{}, false
	}
	expires := time.Unix(int64(binary.BigEndian.Uint64(payload)), 0)
	if !time.Now().Before(expires) {
		return "", time.Time{}, false
	}
	return cookie.Value, expires, true
}

// current gives the request's live signed-in session and its token, if any.
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
// it hands out a signed pre-session, whose token is good only for signing
// in; this writes nothing to the database.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	token, session, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if ok {
		writeJSON(w, http.StatusOK, viewOf(session.Principal, token, session.ExpiresAt))
		return
	}
	pre, expires, ok := s.preSession(r)
	if !ok {
		pre, expires = s.newPreSession()
		setCookie(w, preCookieName, pre, expires)
	}
	writeJSON(w, http.StatusOK, viewOf(nil, pre, expires))
}

// csrfValid checks the CSRF header against a session's token.
func csrfValid(r *http.Request, token string) bool {
	given := r.Header.Values(csrfHeader)
	return len(given) == 1 && subtle.ConstantTimeCompare([]byte(given[0]), []byte(csrfFor(token))) == 1
}

type signInRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// signIn signs in a demo principal with its per-install credential. It is
// admitted by the CSRF token of a live session or of a signed pre-session,
// and the rotation consumes exactly that one, atomically: a pre-session
// signs in once, and a session revoked meanwhile signs in nobody.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var admitted postgres.Admission
	token, _, live, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if live && csrfValid(r, token) {
		admitted = postgres.Admission{IDSHA256: digest(token)}
	} else if pre, expires, ok := s.preSession(r); ok && csrfValid(r, pre) {
		admitted = postgres.Admission{PreSession: true, IDSHA256: digest(pre), ExpiresAt: expires}
	} else {
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
	expires, err := s.sessions.SignIn(r.Context(), admitted, digest(next), principal, s.cfg.SessionTTL)
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
	writeJSON(w, http.StatusOK, viewOf(&principal, next, expires))
}

// signOut ends the session. It is a protected write like any other.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	token, _, ok, err := s.current(r)
	if err != nil {
		internal(w)
		return
	}
	if !ok {
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
	clearCookie(w, cookieName)
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
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign_in_required", "Sign in first.")
			return
		}
		next(w, r, authz.Principal{WorkspaceID: session.Principal.WorkspaceID, ID: session.Principal.ID})
	}
}

const maxBody = 4096

// readJSON decodes exactly one JSON object, with no unknown fields and
// nothing after it, from a JSON body.
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send application/json.")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	invalid := func() bool {
		writeError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object with the documented fields.")
		return false
	}
	if err != nil || !bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("{")) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return invalid()
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	return true
}

func internal(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal", "Internal error.")
}
