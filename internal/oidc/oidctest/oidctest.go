// Package oidctest is an invented identity provider for tests: a discovery document, a key set, an
// authorization endpoint that approves at once as one invented account, a token endpoint that checks the
// client, the code and its PKCE verifier, and a sign-out page. It answers over TLS on loopback under the
// invented name login.invented.example, and its Transport reaches it and nothing else.
package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp/sourcehttptest"
)

// Host is the invented name the provider answers under.
const Host = "login.invented.example"

// Issuer is the provider's issuer.
const Issuer = "https://" + Host

// Client is the one client registered at the provider. A client with no secret is public.
type Client struct {
	ID, Secret, RedirectURL string
}

// Account is who approves at the authorization endpoint.
type Account struct {
	Subject, Email, Name string
	EmailVerified        bool
	// Claims are added to the ID token as they are, such as hd or tid.
	Claims map[string]any
}

// Key is a signing key of the provider.
type Key struct {
	ID, Alg string
	Private crypto.Signer
}

// Provider is the invented identity provider.
type Provider struct {
	client Client
	server *httptest.Server
	roots  *x509.CertPool

	mu        sync.Mutex
	account   Account
	published []*Key
	signer    *Key
	codes     map[string]*grant
	last      string
	counts    Counts
	// Mint, when set, makes each ID token the token endpoint answers with, from the claims the provider
	// would sign. Sign makes a well-signed one.
	Mint func(claims map[string]any) string
	// ReuseCodes lets a code be exchanged again, as a careless provider would.
	ReuseCodes bool
	// Deny answers each authorization request with error=access_denied.
	Deny bool
	// Discovery, when set, changes the discovery document before it is served.
	Discovery func(document map[string]any)
	// KeysMaxAge is the max-age the key set is served with, in seconds.
	KeysMaxAge int
	// FailKeys answers the key set with HTTP 503.
	FailKeys bool
}

// Counts are the requests each endpoint has answered.
type Counts struct{ Discovery, Keys, Authorize, Token int }

type grant struct {
	challenge, nonce, redirect string
	account                    Account
	used                       bool
}

// New starts the provider, with an RS256 key k1 that signs its tokens.
func New(t testing.TB, client Client, account Account) *Provider {
	t.Helper()
	p := &Provider{client: client, account: account, codes: map[string]*grant{}, KeysMaxAge: 3600}
	p.signer = NewKey(t, "k1", "RS256")
	p.published = []*Key{p.signer}
	certificate, roots := sourcehttptest.Identity(t, Host)
	p.roots = roots
	p.server = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	p.server.StartTLS()
	t.Cleanup(p.server.Close)
	return p
}

// Transport reaches the provider, whatever host is asked for, trusting only its certificate, through the
// transport the connector itself uses.
func (p *Provider) Transport() http.RoundTripper {
	return sourcehttp.Transport(sourcehttptest.To(p.server.Listener.Addr().String()), p.roots)
}

// Roots are the roots that trust the provider's certificate.
func (p *Provider) Roots() *x509.CertPool { return p.roots }

// Address is where the provider listens.
func (p *Provider) Address() string { return p.server.Listener.Addr().String() }

// Counts gives the requests each endpoint has answered.
func (p *Provider) Counts() Counts {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts
}

// SetAccount changes who approves from the next authorization on.
func (p *Provider) SetAccount(account Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.account = account
}

// LastToken is the last ID token the token endpoint gave.
func (p *Provider) LastToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// Signer is the key that signs the provider's tokens.
func (p *Provider) Signer() *Key {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.signer
}

// Rotate publishes a new key, which signs every token from now on. The old keys stay published.
func (p *Provider) Rotate(t testing.TB, id, alg string) *Key {
	k := NewKey(t, id, alg)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, k)
	p.signer = k
	return k
}

// Retire takes a key out of the published key set.
func (p *Provider) Retire(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.published[:0]
	for _, k := range p.published {
		if k.ID != id {
			kept = append(kept, k)
		}
	}
	p.published = kept
}

// NewKey makes a key for an algorithm: RS*, PS*, ES* or EdDSA.
func NewKey(t testing.TB, id, alg string) *Key {
	t.Helper()
	var private crypto.Signer
	var err error
	switch {
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		private, err = rsa.GenerateKey(rand.Reader, 2048)
	case alg == "ES256":
		private, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case alg == "ES384":
		private, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case alg == "ES512":
		private, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	case alg == "EdDSA", alg == "Ed25519":
		_, private, err = ed25519.GenerateKey(rand.Reader)
	default:
		t.Fatalf("no key for %s", alg)
	}
	if err != nil {
		t.Fatal(err)
	}
	return &Key{ID: id, Alg: alg, Private: private}
}

func encode(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func hashed(alg string, data []byte) (crypto.Hash, []byte) {
	switch alg[2:] {
	case "384":
		sum := sha512.Sum384(data)
		return crypto.SHA384, sum[:]
	case "512":
		sum := sha512.Sum512(data)
		return crypto.SHA512, sum[:]
	}
	sum := sha256.Sum256(data)
	return crypto.SHA256, sum[:]
}

// Sign signs claims with the provider's signing key, under a header that names the key.
func (p *Provider) Sign(claims map[string]any) string {
	k := p.Signer()
	return SignWith(k, map[string]any{"alg": k.Alg, "kid": k.ID, "typ": "JWT"}, claims)
}

// SignWith signs claims with a key under any header.
func SignWith(k *Key, header, claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return SignPayload(k, header, payload)
}

// SignPayload signs a payload as it is, so a test can sign JSON a map cannot hold, such as a claim named
// twice.
func SignPayload(k *Key, header map[string]any, payload []byte) string {
	h, _ := json.Marshal(header)
	input := encode(h) + "." + encode(payload)
	var signature []byte
	var err error
	switch private := k.Private.(type) {
	case *rsa.PrivateKey:
		hash, digest := hashed(k.Alg, []byte(input))
		if strings.HasPrefix(k.Alg, "PS") {
			signature, err = rsa.SignPSS(rand.Reader, private, hash, digest,
				&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			signature, err = rsa.SignPKCS1v15(rand.Reader, private, hash, digest)
		}
	case *ecdsa.PrivateKey:
		_, digest := hashed(k.Alg, []byte(input))
		var r, s *big.Int
		r, s, err = ecdsa.Sign(rand.Reader, private, digest)
		size := (private.Curve.Params().BitSize + 7) / 8
		signature = append(r.FillBytes(make([]byte, size)), s.FillBytes(make([]byte, size))...)
	case ed25519.PrivateKey:
		signature = ed25519.Sign(private, []byte(input))
	}
	if err != nil {
		panic(err)
	}
	return input + "." + encode(signature)
}

// Unsigned is a token's header and claims, encoded, without the signature or the dot before it.
func Unsigned(header, claims map[string]any) string {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	return encode(h) + "." + encode(c)
}

// HMACWithPublicKey signs claims with HS256 under the key's ID, keyed by the key's public key in PEM, as
// an attacker would against a verifier that takes the alg header's word.
func HMACWithPublicKey(k *Key, claims map[string]any) string {
	der, _ := x509.MarshalPKIXPublicKey(k.Private.Public())
	input := Unsigned(map[string]any{"alg": "HS256", "kid": k.ID, "typ": "JWT"}, claims)
	mac := hmac.New(sha256.New, der)
	mac.Write([]byte(input))
	return input + "." + encode(mac.Sum(nil))
}

// JWK is the public key as a key set publishes it.
func (k *Key) JWK() map[string]any {
	jwk := map[string]any{"kid": k.ID, "alg": k.Alg, "use": "sig"}
	switch public := k.Private.Public().(type) {
	case *rsa.PublicKey:
		jwk["kty"], jwk["n"] = "RSA", encode(public.N.Bytes())
		jwk["e"] = encode(big.NewInt(int64(public.E)).Bytes())
	case *ecdsa.PublicKey:
		raw, _ := public.Bytes()
		size := (public.Curve.Params().BitSize + 7) / 8
		jwk["kty"], jwk["crv"] = "EC", public.Curve.Params().Name
		jwk["x"], jwk["y"] = encode(raw[1:1+size]), encode(raw[1+size:])
	case ed25519.PublicKey:
		jwk["kty"], jwk["crv"], jwk["x"] = "OKP", "Ed25519", encode(public)
	}
	return jwk
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/openid-configuration":
		p.discovery(w)
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		p.keys(w)
	case r.Method == http.MethodGet && r.URL.Path == "/authorize":
		p.authorize(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		p.token(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/logout":
		w.Write([]byte("Signed out of the invented provider.\n"))
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) discovery(w http.ResponseWriter) {
	p.mu.Lock()
	p.counts.Discovery++
	edit := p.Discovery
	p.mu.Unlock()
	document := map[string]any{
		"issuer": Issuer, "authorization_endpoint": Issuer + "/authorize", "token_endpoint": Issuer + "/token",
		"jwks_uri": Issuer + "/keys", "end_session_endpoint": Issuer + "/logout",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported":          []string{"RS256", "ES256", "EdDSA"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post", "none"},
		"authorization_response_iss_parameter_supported": true,
	}
	if edit != nil {
		edit(document)
	}
	reply(w, http.StatusOK, document)
}

func (p *Provider) keys(w http.ResponseWriter) {
	p.mu.Lock()
	p.counts.Keys++
	if p.FailKeys {
		p.mu.Unlock()
		http.Error(w, "the key set is unavailable", http.StatusServiceUnavailable)
		return
	}
	keys := make([]any, len(p.published))
	for i, k := range p.published {
		keys[i] = k.JWK()
	}
	age := p.KeysMaxAge
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(age))
	json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// authorize approves at once, as the current account, and sends the browser back with a code, the state
// and the issuer. A request that is not this client's, to its registered redirect, with an S256 challenge,
// a state and a nonce, gets a plain 400 and no redirect.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts.Authorize++
	if q.Get("response_type") != "code" || q.Get("client_id") != p.client.ID ||
		q.Get("redirect_uri") != p.client.RedirectURL || !strings.Contains(" "+q.Get("scope")+" ", " openid ") ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" ||
		q.Get("nonce") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}, "iss": {Issuer}}
	if p.Deny {
		back.Set("error", "access_denied")
	} else {
		code := encode(random(32))
		p.codes[code] = &grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"),
			redirect: q.Get("redirect_uri"), account: p.account}
		back.Set("code", code)
	}
	http.Redirect(w, r, p.client.RedirectURL+"?"+back.Encode(), http.StatusSeeOther)
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// token exchanges a code: it checks the client, by its secret for a confidential one, the code, unused
// unless ReuseCodes, its redirect and its verifier, and answers with an ID token for the code's account.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	p.mu.Lock()
	p.counts.Token++
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.client.ID || secret != p.client.Secret {
		p.mu.Unlock()
		reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	g := p.codes[r.PostForm.Get("code")]
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if g == nil || (g.used && !p.ReuseCodes) || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Get("redirect_uri") != g.redirect || encode(sum[:]) != g.challenge {
		p.mu.Unlock()
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	g.used = true
	now := time.Now().Unix()
	claims := map[string]any{"iss": Issuer, "sub": g.account.Subject, "aud": p.client.ID, "exp": now + 300,
		"iat": now, "nonce": g.nonce}
	if g.account.Email != "" {
		claims["email"], claims["email_verified"] = g.account.Email, g.account.EmailVerified
	}
	if g.account.Name != "" {
		claims["name"] = g.account.Name
	}
	for name, value := range g.account.Claims {
		claims[name] = value
	}
	mint := p.Mint
	p.mu.Unlock()
	var token string
	if mint != nil {
		token = mint(claims)
	} else {
		token = p.Sign(claims)
	}
	p.mu.Lock()
	p.last = token
	p.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"access_token": "invented-access-token", "token_type": "Bearer",
		"expires_in": 300, "id_token": token})
}
