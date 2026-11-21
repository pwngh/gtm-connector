package httptransport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oidc"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Sign-in through a company's identity provider. The browser opens OIDCStartPath, which sets a signed
// flow cookie and sends it to the provider; the provider sends it back to OIDCCallbackPath with a code,
// which the server exchanges and verifies, and a session starts for the person the ID token names. The
// flow cookie holds a random flow ID, its expiry and the page to return to, under an HMAC with the
// install's session key; the state, the nonce and the PKCE verifier are derived from the flow ID with
// that key, so the browser holds none of them and no visitor costs a database row. A flow signs in once:
// its digest is consumed with the session it starts.

// OIDC is sign-in through a company's identity provider for the people of one workspace.
type OIDC struct {
	Client *oidc.Client
	// Workspace is the workspace the people who sign in belong to.
	Workspace postgres.Workspace
	// SessionTTL is how long their sessions last. A person the provider disables keeps a session they
	// hold until then.
	SessionTTL time.Duration
}

// The paths of a sign-in through the identity provider. The provider is registered with the public origin
// followed by OIDCCallbackPath as the client's redirect.
const (
	OIDCStartPath    = "/api/v1/session/oidc"
	OIDCCallbackPath = "/api/v1/session/oidc/callback"
)

// DefaultOIDCSessionTTL is how long a session of a person who signed in through the provider lasts unless
// configured otherwise.
const DefaultOIDCSessionTTL = time.Hour

const (
	flowCookieName = "__Host-gtm-oidc"
	// flowTTL is how long a person has to sign in at the provider.
	flowTTL = 10 * time.Minute
	// maxReturn is the longest page address a sign-in returns to.
	maxReturn = 512
)

// The reasons a refused sign-in through the provider sends the browser back with, as the sign_in_error
// parameter of the page it started from.
const (
	refusedDenied      = "denied"      // the person declined at the provider
	refusedNotAllowed  = "not_allowed" // the provider signed them in, and this install does not let them in
	refusedDisabled    = "disabled"    // the person is disabled here
	refusedExpired     = "expired"     // the flow is missing, too old or used
	refusedUnavailable = "unavailable" // the provider could not be read
	refusedFailed      = "failed"      // anything else: a wrong state, or a token that does not verify
)

// flowMAC is the flow cookie's HMAC under the session key.
func (s *Server) flowMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.cfg.SessionKey)
	mac.Write([]byte("gtm-connector oidc flow v1\x00"))
	mac.Write(payload)
	return mac.Sum(nil)
}

// flowSecret is one of a flow's derived values: its state, its nonce or its PKCE verifier, each 43
// characters of base64url.
func (s *Server) flowSecret(label string, flow []byte) string {
	mac := hmac.New(sha256.New, s.cfg.SessionKey)
	mac.Write([]byte("gtm-connector oidc " + label + " v1\x00"))
	mac.Write(flow)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// newFlow makes a flow cookie that returns to back.
func (s *Server) newFlow(back string) (string, []byte, time.Time) {
	expires := time.Now().Add(flowTTL).Truncate(time.Second)
	payload := make([]byte, 8+32, 8+32+len(back))
	binary.BigEndian.PutUint64(payload, uint64(expires.Unix()))
	rand.Read(payload[8:])
	payload = append(payload, back...)
	return "o1." + base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(s.flowMAC(payload)), payload[8:40], expires
}

// flowOf reads the request's flow cookie: its flow ID, the page it returns to and its expiry, when its
// HMAC is the session key's and it has not expired. Its parts are read strictly, so a used flow cannot
// come back under another spelling of the same bytes.
func (s *Server) flowOf(r *http.Request) (flow []byte, back string, expires time.Time, ok bool) {
	found, err := r.Cookie(flowCookieName)
	if err != nil {
		return nil, "", time.Time{}, false
	}
	parts := strings.Split(found.Value, ".")
	if len(parts) != 3 || parts[0] != "o1" {
		return nil, "", time.Time{}, false
	}
	payload, err1 := cookieBase64.DecodeString(parts[1])
	mac, err2 := cookieBase64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || len(payload) < 40 || len(payload) > 40+maxReturn ||
		!hmac.Equal(mac, s.flowMAC(payload)) {
		return nil, "", time.Time{}, false
	}
	expires = time.Unix(int64(binary.BigEndian.Uint64(payload)), 0)
	if !time.Now().Before(expires) {
		return nil, "", time.Time{}, false
	}
	return payload[8:40], s.returnPath(string(payload[40:])), expires, true
}

// flowDigest is what a used flow is recorded by: the digest of its flow ID, whatever its cookie's spelling.
func flowDigest(flow []byte) []byte {
	sum := sha256.Sum256(append([]byte("gtm-connector oidc flow\x00"), flow...))
	return sum[:]
}

// returnPath is the page of the app a sign-in returns to: a client route of this origin, with its query,
// as the browser asked for it, or "/" for anything else.
func (s *Server) returnPath(asked string) string {
	if asked == "" || len(asked) > maxReturn || !strings.HasPrefix(asked, "/") || strings.HasPrefix(asked, "//") {
		return "/"
	}
	for i := 0; i < len(asked); i++ {
		if asked[i] < 0x21 || asked[i] > 0x7e || asked[i] == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(asked)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Fragment != "" || !canonical(u.Path) ||
		!s.clientRoute(path.Clean(u.Path)) || u.Query().Has("sign_in_error") {
		return "/"
	}
	return asked
}

// startOIDC sends the browser to the provider with a new flow. Its return parameter names the page to
// come back to.
func (s *Server) startOIDC(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC == nil {
		writeError(w, http.StatusNotFound, "not_found", "Sign-in through an identity provider is not set up here.")
		return
	}
	back := "/"
	if asked := r.URL.Query()["return"]; len(asked) == 1 {
		back = s.returnPath(asked[0])
	}
	cookie, flow, expires := s.newFlow(back)
	address, err := s.cfg.OIDC.Client.AuthorizationURL(r.Context(), s.flowSecret("state", flow),
		s.flowSecret("nonce", flow), s.flowSecret("verifier", flow))
	if err != nil {
		s.refuseSignIn(w, r, back, refusedUnavailable, err)
		return
	}
	setCookie(w, flowCookieName, cookie, expires)
	http.Redirect(w, r, address, http.StatusSeeOther)
}

// errFlow and errState are the refusals of a callback before the provider is asked anything.
var (
	errFlow     = errors.New("the flow cookie is missing, not this install's, expired or used")
	errState    = errors.New("the state or the code is missing, repeated or not this flow's")
	errDeclined = errors.New("the provider sent the browser back with an error")
)

// finishOIDC takes the browser back from the provider: it checks the flow cookie and the state, asks
// whether the flow has signed in already, exchanges the code with the flow's verifier, verifies the ID
// token against the flow's nonce, and signs the person in, ending any session the browser held. Every
// refusal sends the browser back to the page it started from with the reason, and starts no session.
func (s *Server) finishOIDC(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC == nil {
		writeError(w, http.StatusNotFound, "not_found", "Sign-in through an identity provider is not set up here.")
		return
	}
	// A flow is used once, whatever comes of it.
	clearCookie(w, flowCookieName)
	flow, back, expires, ok := s.flowOf(r)
	if !ok {
		s.refuseSignIn(w, r, "/", refusedExpired, errFlow)
		return
	}
	// The state comes first, so that only the provider this flow went to can end it, with a code or an
	// error.
	query := r.URL.Query()
	state := query["state"]
	if len(state) != 1 || subtle.ConstantTimeCompare([]byte(state[0]), []byte(s.flowSecret("state", flow))) != 1 {
		s.refuseSignIn(w, r, back, refusedFailed, errState)
		return
	}
	if declined := query["error"]; len(declined) > 0 {
		reason := refusedFailed
		if len(declined) == 1 && declined[0] == "access_denied" {
			reason = refusedDenied
		}
		s.refuseSignIn(w, r, back, reason, errDeclined)
		return
	}
	code := query["code"]
	if len(code) != 1 {
		s.refuseSignIn(w, r, back, refusedFailed, errState)
		return
	}
	client := s.cfg.OIDC.Client
	if err := client.CheckResponseIssuer(r.Context(), query["iss"]); err != nil {
		s.refuseSignIn(w, r, back, refusalOf(err), err)
		return
	}
	admitted := postgres.Admission{PreSession: true, IDSHA256: flowDigest(flow), ExpiresAt: expires}
	switch used, err := s.sessions.PreSessionUsed(r.Context(), admitted.IDSHA256); {
	case err != nil:
		s.refuseSignIn(w, r, back, refusedFailed, errors.New("the flow could not be looked up"))
		return
	case used:
		s.refuseSignIn(w, r, back, refusedExpired, errFlow)
		return
	}
	who, err := client.SignIn(r.Context(), code[0], s.flowSecret("verifier", flow), s.flowSecret("nonce", flow))
	if err != nil {
		s.refuseSignIn(w, r, back, refusalOf(err), err)
		return
	}
	var previous []byte
	if token, _, live, err := s.current(r); err == nil && live {
		previous = digest(token)
	}
	next := newToken()
	principal, until, err := s.sessions.SignInPerson(r.Context(), admitted, digest(next), s.cfg.OIDC.Workspace,
		personOf(who), s.cfg.OIDC.SessionTTL, previous)
	switch {
	case errors.Is(err, postgres.ErrSessionUsed):
		s.refuseSignIn(w, r, back, refusedExpired, errFlow)
		return
	case errors.Is(err, postgres.ErrDisabled):
		s.refuseSignIn(w, r, back, refusedDisabled, err)
		return
	case err != nil:
		s.refuseSignIn(w, r, back, refusedFailed, errors.New("the session could not be stored"))
		return
	}
	// A sign-in is one line of the log, as a refusal is, by the principal and the workspace: no token,
	// code, secret or email.
	if s.cfg.Log != nil {
		s.cfg.Log.Info("signed in through the identity provider", "principal", principal.ID,
			"workspace", s.cfg.OIDC.Workspace.Slug)
	}
	setCookie(w, cookieName, next, until)
	http.Redirect(w, r, s.cfg.PublicOrigin+back, http.StatusSeeOther)
}

// refusalOf is the reason the app is given for a provider's refusal.
func refusalOf(err error) string {
	switch {
	case errors.Is(err, oidc.ErrNotAllowed):
		return refusedNotAllowed
	case errors.Is(err, oidc.ErrUnavailable):
		return refusedUnavailable
	}
	return refusedFailed
}

// refuseSignIn sends the browser back to the page a sign-in started from, with the reason, and logs the
// refusal by its fixed words. No refusal of this package or of the oidc package holds a token, a code or a
// secret; any other error is logged as a fixed line.
func (s *Server) refuseSignIn(w http.ResponseWriter, r *http.Request, back, reason string, err error) {
	if s.cfg.Log != nil {
		detail := "internal error"
		if err != nil && (errors.Is(err, oidc.ErrUnavailable) || errors.Is(err, oidc.ErrRefused) ||
			errors.Is(err, oidc.ErrToken) || errors.Is(err, oidc.ErrNotAllowed) || errors.Is(err, errFlow) ||
			errors.Is(err, errState) || errors.Is(err, errDeclined) || errors.Is(err, postgres.ErrDisabled)) {
			detail = err.Error()
		}
		s.cfg.Log.Warn("sign-in through the identity provider refused", "reason", reason, "detail", detail)
	}
	separator := "?"
	if strings.Contains(back, "?") {
		separator = "&"
	}
	http.Redirect(w, r, s.cfg.PublicOrigin+back+separator+"sign_in_error="+reason, http.StatusSeeOther)
}

// personOf is who signed in, as the person is stored: the name the token gives, made inert and cut to 200
// characters, or else the email or the subject; and the email only when it is one line of printable text.
func personOf(who oidc.Identity) postgres.PersonIdentity {
	person := postgres.PersonIdentity{Issuer: who.Issuer, Subject: who.Subject}
	if plainEmail(who.Email) {
		person.Email, person.EmailVerified = who.Email, who.EmailVerified
	}
	name, _, err := evidence.SafeText(who.Name)
	if err != nil {
		name = ""
	}
	name = strings.TrimSpace(name)
	switch {
	case name != "":
	case person.Email != "":
		name = person.Email
	default:
		name = who.Subject
	}
	if utf8.RuneCountInString(name) > 200 {
		name = string([]rune(name)[:200])
	}
	person.DisplayName = name
	return person
}

// plainEmail is whether an email can be kept as it is: 3 to 320 bytes of valid text with an @, every
// character printable and none a space.
func plainEmail(email string) bool {
	if len(email) < 3 || len(email) > 320 || !utf8.ValidString(email) || !strings.Contains(email, "@") {
		return false
	}
	for _, c := range email {
		if !unicodetab.IsPrintable(c) || c == ' ' {
			return false
		}
	}
	return true
}
