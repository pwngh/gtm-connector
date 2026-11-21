// Package oidc signs a person in through their company's identity provider with OpenID Connect: the
// authorization code flow with PKCE (S256), for a confidential client, which also presents its secret, or
// a public one. It reads the provider's discovery document from the issuer, keeps the provider's signing
// keys, and verifies the ID token itself with the standard library: its signature, by a key of the
// provider's key set that fits the token's algorithm, and its iss, aud, azp, exp, iat, nbf, nonce and sub.
// It never accepts alg "none" or an HMAC algorithm, and it never takes a key from the token.
//
// A refusal is one of this package's errors, with nothing of a token, a code or a secret in it.
package oidc

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The refusals. Each token refusal wraps ErrToken.
var (
	// ErrUnavailable: the provider could not be read, or answered outside the shape this package reads.
	ErrUnavailable = errors.New("oidc: the identity provider could not be read")
	// ErrRefused: the provider's token endpoint refused the code.
	ErrRefused = errors.New("oidc: the identity provider refused the code")
	// ErrClient: it refused this client's credentials, such as a wrong secret, or none where one is due.
	ErrClient = fmt.Errorf("%w: it refused this client's credentials", ErrRefused)
	// ErrToken: the ID token does not verify.
	ErrToken = errors.New("oidc: the ID token does not verify")
	// ErrNotAllowed: the person signed in, and the configuration does not let them in here.
	ErrNotAllowed = errors.New("oidc: this account may not sign in here")

	ErrMalformed       = fmt.Errorf("%w: it is not a signed JSON Web Token", ErrToken)
	ErrAlgorithm       = fmt.Errorf("%w: its algorithm is not one a provider's key signs with", ErrToken)
	ErrUnknownKey      = fmt.Errorf("%w: no key of the provider's key set fits it", ErrToken)
	ErrSignature       = fmt.Errorf("%w: its signature does not verify", ErrToken)
	ErrIssuer          = fmt.Errorf("%w: it was issued by another issuer", ErrToken)
	ErrAudience        = fmt.Errorf("%w: it is for another audience", ErrToken)
	ErrAuthorizedParty = fmt.Errorf("%w: it was issued to another party", ErrToken)
	ErrExpired         = fmt.Errorf("%w: it has expired", ErrToken)
	ErrIssuedAt        = fmt.Errorf("%w: its issue time is missing, in the future or too old", ErrToken)
	ErrNotYetValid     = fmt.Errorf("%w: it is not valid yet", ErrToken)
	ErrNonce           = fmt.Errorf("%w: its nonce does not match this sign-in", ErrToken)
	ErrSubject         = fmt.Errorf("%w: its subject is missing or not plain text", ErrToken)
)

// Leeway is how far the provider's clock may stand from this machine's for exp, iat and nbf.
const Leeway = time.Minute

// MaxTokenAge is the oldest an ID token's iat may be: a sign-in waits no longer than this for its code.
const MaxTokenAge = 10 * time.Minute

// Size limits on what the provider sends.
const (
	maxDocument = 64 << 10
	maxKeySet   = 256 << 10
	maxToken    = 16 << 10
)

// requestTime is how long one request to the provider may take.
const requestTime = 10 * time.Second

// metadataLife is how long a discovery document is kept before it is read again.
const metadataLife = time.Hour

// failureHold is how long a failed read of the discovery document or of the key set is given again
// without asking the provider, so sign-ins do not wait on a failing provider one after another.
const failureHold = 5 * time.Second

// Config is a sign-in's configuration. Issuer, ClientID and RedirectURL are required, and at least one of
// AllowedDomains and Tenant.
type Config struct {
	// Issuer is the provider's issuer, an https URL exactly as the provider names itself.
	Issuer string
	// ClientID is this install's client at the provider.
	ClientID string
	// ClientSecret makes the client confidential. Empty, it is a public client, which PKCE alone protects.
	ClientSecret string
	// RedirectURL is the callback the provider sends the browser back to, exactly as registered there.
	RedirectURL string
	// AllowedDomains, when set, admits a person only by an email the provider verified, in one of these
	// domains, in lower case.
	AllowedDomains []string
	// TenantClaim and Tenant, when set, admit a person only when the ID token's claim of that name, "hd"
	// or "tid", is Tenant, in any ASCII case.
	TenantClaim, Tenant string
	// Transport reaches the provider. Nil is the one sourcehttp makes, which dials only in a live process.
	Transport http.RoundTripper
	// Now is the clock. Nil is time.Now.
	Now func() time.Time
}

// Identity is who signed in.
type Identity struct {
	Issuer, Subject string
	// Email is the token's email, or "", and EmailVerified whether the provider says it verified it.
	Email         string
	EmailVerified bool
	// Name is the token's name claim, or "".
	Name string
}

// Client is one provider's sign-in, with its discovery document and its key set kept between sign-ins.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu          sync.Mutex
	meta        *metadata
	metaUntil   time.Time
	metaFailure failure
	keys        keySet
}

// failure is the last failed read of a document, given again until failureHold has passed.
type failure struct {
	err error
	at  time.Time
}

// held is the failure while it is held, or nil.
func (f failure) held(now time.Time) error {
	if f.err != nil && now.Sub(f.at) < failureHold {
		return f.err
	}
	return nil
}

// remember keeps a failure of the provider's own; an interrupted request is not one.
func (f *failure) remember(err error, now time.Time) {
	if errors.Is(err, ErrUnavailable) {
		f.err, f.at = err, now
	}
}

// New checks a configuration and makes its client. It contacts nothing.
func New(cfg Config) (*Client, error) {
	if err := checkURL(cfg.Issuer, false); err != nil {
		return nil, errors.New("the issuer must be an https URL with no query or fragment")
	}
	if cfg.ClientID == "" || !printableASCII(cfg.ClientID, 512) {
		return nil, errors.New("the client ID must be 1 to 512 printable ASCII characters")
	}
	if cfg.ClientSecret != "" && !printableASCII(cfg.ClientSecret, 1024) {
		return nil, errors.New("the client secret must be 1 to 1024 printable ASCII characters")
	}
	if err := checkURL(cfg.RedirectURL, false); err != nil {
		return nil, errors.New("the redirect URL must be an https URL with no query or fragment")
	}
	for _, domain := range cfg.AllowedDomains {
		if domain == "" || domain != asciiLower(domain) || !printableASCII(domain, 253) || strings.Contains(domain, "@") {
			return nil, errors.New("an allowed domain is a lower-case domain name, such as invented.example")
		}
	}
	switch {
	case (cfg.TenantClaim == "") != (cfg.Tenant == ""):
		return nil, errors.New("a tenant needs both the claim and its value")
	case cfg.TenantClaim != "" && cfg.TenantClaim != "hd" && cfg.TenantClaim != "tid":
		return nil, errors.New("the tenant claim is hd or tid")
	case cfg.Tenant != "" && !printableASCII(cfg.Tenant, 255):
		return nil, errors.New("the tenant must be 1 to 255 printable ASCII characters")
	case len(cfg.AllowedDomains) == 0 && cfg.Tenant == "":
		return nil, errors.New("name the allowed email domains or the tenant, or anyone the provider knows could sign in")
	}
	transport := cfg.Transport
	if transport == nil {
		transport = sourcehttp.Transport(sourcehttp.Direct, nil)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{cfg: cfg, now: now, http: &http.Client{Transport: transport, Timeout: requestTime,
		// A redirect is an answer like any other, and refused below: nothing is sent anywhere it leads.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Issuer is the issuer the client signs in with.
func (c *Client) Issuer() string { return c.cfg.Issuer }

// RedirectURL is the callback the provider sends the browser back to.
func (c *Client) RedirectURL() string { return c.cfg.RedirectURL }

// Confidential is whether the client presents a secret.
func (c *Client) Confidential() bool { return c.cfg.ClientSecret != "" }

// checkURL refuses anything but an absolute https URL with a host and no user, and with no query when
// query is false. No URL has a fragment.
func checkURL(raw string, query bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" ||
		strings.Contains(raw, "#") || (!query && u.RawQuery != "") || u.Opaque != "" || !printableASCII(raw, 2040) {
		return errors.New("not an https URL")
	}
	return nil
}

func printableASCII(text string, most int) bool {
	if text == "" || len(text) > most {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < 0x21 || text[i] > 0x7e {
			return false
		}
	}
	return true
}

func asciiLower(text string) string {
	out := []byte(text)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}

// metadata is what the client reads of the discovery document.
type metadata struct {
	authorization, token, keys, endSession string
	// secretInBody is whether a confidential client sends its secret in the form, because the provider
	// takes no client_secret_basic.
	secretInBody bool
	// issInResponse is whether the provider adds iss to its authorization response.
	issInResponse bool
}

// discovered gives the provider's discovery document, read again once it is an hour old.
func (c *Client) discovered(ctx context.Context) (*metadata, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.meta != nil && c.now().Before(c.metaUntil) {
		return c.meta, nil
	}
	if err := c.metaFailure.held(c.now()); err != nil {
		return nil, err
	}
	body, _, err := c.get(ctx, strings.TrimSuffix(c.cfg.Issuer, "/")+"/.well-known/openid-configuration", maxDocument)
	var meta *metadata
	if err == nil {
		meta, err = c.readMetadata(body)
	}
	if err != nil {
		c.metaFailure.remember(err, c.now())
		return nil, err
	}
	c.meta, c.metaUntil, c.metaFailure = meta, c.now().Add(metadataLife), failure{}
	return meta, nil
}

func (c *Client) readMetadata(body []byte) (*metadata, error) {
	unusable := fmt.Errorf("%w: its discovery document is not usable", ErrUnavailable)
	document, err := strictjson.Loads(body)
	object, isObject := document.(*strictjson.Object)
	if err != nil || !isObject {
		return nil, unusable
	}
	text := func(name string) string {
		value, _ := object.Get(name)
		s, _ := value.(string)
		return s
	}
	if issuer := text("issuer"); subtle.ConstantTimeCompare([]byte(issuer), []byte(c.cfg.Issuer)) != 1 {
		return nil, fmt.Errorf("%w: its discovery document names another issuer", ErrUnavailable)
	}
	meta := &metadata{authorization: text("authorization_endpoint"), token: text("token_endpoint"),
		keys: text("jwks_uri"), endSession: text("end_session_endpoint")}
	if checkURL(meta.authorization, true) != nil || checkURL(meta.token, true) != nil ||
		checkURL(meta.keys, true) != nil || (meta.endSession != "" && checkURL(meta.endSession, true) != nil) {
		return nil, unusable
	}
	listed := func(name, want string) (present, found bool) {
		value, present := object.Get(name)
		items, _ := value.([]any)
		for _, item := range items {
			found = found || item == want
		}
		return present, found
	}
	if present, s256 := listed("code_challenge_methods_supported", "S256"); present && !s256 {
		return nil, fmt.Errorf("%w: it takes no S256 code challenge", ErrUnavailable)
	}
	if c.Confidential() {
		present, basic := listed("token_endpoint_auth_methods_supported", "client_secret_basic")
		_, post := listed("token_endpoint_auth_methods_supported", "client_secret_post")
		switch {
		case !present || basic:
		case post:
			meta.secretInBody = true
		default:
			return nil, fmt.Errorf("%w: it takes a client secret in no way this client sends one", ErrUnavailable)
		}
	}
	value, _ := object.Get("authorization_response_iss_parameter_supported")
	meta.issInResponse = value == true
	return meta, nil
}

// get reads one document of at most most bytes with a GET, and gives the response's Cache-Control too.
// Anything but a 200 is ErrUnavailable.
func (c *Client) get(ctx context.Context, address string, most int64) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrUnavailable, "a provider address could not be used")
	}
	request.Header.Set("Accept", "application/json")
	body, response, err := c.send(request, most)
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: it answered HTTP %d", ErrUnavailable, response.StatusCode)
	}
	return body, response.Header.Get("Cache-Control"), nil
}

// send sends one request and reads at most most bytes of the answer. A transport's error is replaced by a
// fixed one, since it can name the address.
func (c *Client) send(request *http.Request, most int64) ([]byte, *http.Response, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil, context.Canceled
		}
		return nil, nil, fmt.Errorf("%w: it could not be reached", ErrUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, most+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: its answer was cut off", ErrUnavailable)
	}
	if int64(len(body)) > most {
		return nil, nil, fmt.Errorf("%w: its answer is too large", ErrUnavailable)
	}
	return body, response, nil
}

// Challenge is the S256 code challenge of a verifier.
func Challenge(verifier string) string {
	return base64.RawURLEncoding.EncodeToString(sha256Sum([]byte(verifier)))
}

// AuthorizationURL is where the browser starts a sign-in: the provider's authorization endpoint, asked
// for a code for openid, email and profile, with the state, the nonce and the verifier's S256 challenge.
// state, nonce and verifier are the sign-in's own, each at least 43 characters.
func (c *Client) AuthorizationURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	if len(state) < 43 || len(nonce) < 43 || len(verifier) < 43 || len(verifier) > 128 {
		return "", errors.New("a sign-in needs its state, its nonce and a verifier of 43 to 128 characters")
	}
	meta, err := c.discovered(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(meta.authorization)
	if err != nil {
		return "", fmt.Errorf("%w: its authorization endpoint is not usable", ErrUnavailable)
	}
	query := u.Query()
	for name, value := range map[string]string{"response_type": "code", "client_id": c.cfg.ClientID,
		"redirect_uri": c.cfg.RedirectURL, "scope": "openid email profile", "state": state, "nonce": nonce,
		"code_challenge": Challenge(verifier), "code_challenge_method": "S256"} {
		query.Set(name, value)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// CheckResponseIssuer checks the iss parameter of an authorization response, as RFC 9207 has a provider
// add it: when given it must be the issuer, and a provider that says it adds it must have.
func (c *Client) CheckResponseIssuer(ctx context.Context, given []string) error {
	meta, err := c.discovered(ctx)
	if err != nil {
		return err
	}
	switch {
	case len(given) > 1:
		return ErrIssuer
	case len(given) == 1 && given[0] != c.cfg.Issuer:
		return ErrIssuer
	case len(given) == 0 && meta.issInResponse:
		return ErrIssuer
	}
	return nil
}

// SignIn exchanges a code for the ID token, with the sign-in's verifier, verifies the token against the
// sign-in's nonce, and admits the person it names under the configuration.
func (c *Client) SignIn(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	if code == "" || len(code) > 4096 {
		return Identity{}, ErrRefused
	}
	meta, err := c.discovered(ctx)
	if err != nil {
		return Identity{}, err
	}
	raw, err := c.exchange(ctx, meta, code, verifier)
	if err != nil {
		return Identity{}, err
	}
	claims, err := c.verify(ctx, raw, nonce)
	if err != nil {
		return Identity{}, err
	}
	return c.admit(claims)
}

// exchange asks the token endpoint for the tokens of a code and gives the ID token alone. The access token
// and anything else in the answer are dropped unread.
func (c *Client) exchange(ctx context.Context, meta *metadata, code, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.cfg.RedirectURL},
		"code_verifier": {verifier}}
	if !c.Confidential() || meta.secretInBody {
		form.Set("client_id", c.cfg.ClientID)
	}
	if c.Confidential() && meta.secretInBody {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: its token endpoint is not usable", ErrUnavailable)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if c.Confidential() && !meta.secretInBody {
		// RFC 6749 2.3.1: each part is form-encoded before the pair is.
		request.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	}
	body, response, err := c.send(request, maxDocument)
	if err != nil {
		return "", err
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		// A refusal of the client itself names a setting to fix, such as the secret; the provider's own
		// words are compared, never repeated.
		if document, err := strictjson.Loads(body); err == nil {
			if object, ok := document.(*strictjson.Object); ok {
				if reason, _ := object.Get("error"); reason == "invalid_client" {
					return "", ErrClient
				}
			}
		}
		return "", ErrRefused
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: its token endpoint answered HTTP %d", ErrUnavailable, response.StatusCode)
	}
	document, err := strictjson.Loads(body)
	object, isObject := document.(*strictjson.Object)
	if err != nil || !isObject {
		return "", fmt.Errorf("%w: its token answer is not one JSON object", ErrUnavailable)
	}
	value, _ := object.Get("id_token")
	raw, _ := value.(string)
	if raw == "" {
		return "", fmt.Errorf("%w: its token answer holds no ID token", ErrUnavailable)
	}
	return raw, nil
}

// EndSessionURL is the provider's own sign-out page for this client, or "" when the provider publishes
// none or cannot be read.
func (c *Client) EndSessionURL(ctx context.Context) string {
	meta, err := c.discovered(ctx)
	if err != nil || meta.endSession == "" {
		return ""
	}
	u, err := url.Parse(meta.endSession)
	if err != nil {
		return ""
	}
	query := u.Query()
	query.Set("client_id", c.cfg.ClientID)
	u.RawQuery = query.Encode()
	return u.String()
}

// admit makes the identity of verified claims and checks it against the configuration: an email in an
// allowed domain, verified by the provider, and the tenant claim.
func (c *Client) admit(claims *strictjson.Object) (Identity, error) {
	subject, _ := claims.Get("sub")
	who := Identity{Issuer: c.cfg.Issuer, Subject: subject.(string)}
	if value, present := claims.Get("email"); present {
		email, ok := value.(string)
		if !ok {
			return Identity{}, fmt.Errorf("%w: its email is not text", ErrToken)
		}
		who.Email = email
	}
	if value, present := claims.Get("email_verified"); present {
		// Some providers send the answer as the text "true" or "false".
		verified, ok := value.(bool)
		if text, isText := value.(string); isText && (text == "true" || text == "false") {
			verified, ok = text == "true", true
		}
		if !ok {
			return Identity{}, fmt.Errorf("%w: its email_verified is not true or false", ErrToken)
		}
		who.EmailVerified = verified && who.Email != ""
	}
	if value, present := claims.Get("name"); present {
		who.Name, _ = value.(string)
	}
	if len(c.cfg.AllowedDomains) > 0 {
		at := strings.LastIndexByte(who.Email, '@')
		if !who.EmailVerified || at < 1 {
			return Identity{}, ErrNotAllowed
		}
		domain, allowed := asciiLower(who.Email[at+1:]), false
		for _, listed := range c.cfg.AllowedDomains {
			allowed = allowed || domain == listed
		}
		if !allowed {
			return Identity{}, ErrNotAllowed
		}
	}
	if c.cfg.Tenant != "" {
		value, _ := claims.Get(c.cfg.TenantClaim)
		tenant, _ := value.(string)
		if tenant == "" || asciiLower(tenant) != asciiLower(c.cfg.Tenant) {
			return Identity{}, ErrNotAllowed
		}
	}
	return who, nil
}
