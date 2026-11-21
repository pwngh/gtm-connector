package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oidc"
	"github.com/pwngh/gtm-connector/internal/oidc/oidctest"
)

// Every test signs in against the invented provider in oidctest, reached through the connector's own
// transport over TLS on loopback.

const (
	clientID = "connector-invented-client"
	secret   = "invented-client-secret-0123456789"
	redirect = "https://127.0.0.1:8443/api/v1/session/oidc/callback"
)

var dana = oidctest.Account{Subject: "00u-dana", Email: "dana@invented.example", Name: "Dana Reed", EmailVerified: true}

func randomText() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// provider starts the invented provider with this client registered, confidential unless secret is "".
func provider(t *testing.T, clientSecret string) *oidctest.Provider {
	t.Helper()
	return oidctest.New(t, oidctest.Client{ID: clientID, Secret: clientSecret, RedirectURL: redirect}, dana)
}

func client(t *testing.T, p *oidctest.Provider, change ...func(*oidc.Config)) *oidc.Client {
	t.Helper()
	cfg := oidc.Config{Issuer: oidctest.Issuer, ClientID: clientID, ClientSecret: secret, RedirectURL: redirect,
		AllowedDomains: []string{"invented.example"}, Transport: p.Transport()}
	for _, c := range change {
		c(&cfg)
	}
	c, err := oidc.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// signIn is one sign-in: the browser is sent to the provider, which approves at once and sends it back,
// and the client exchanges the code and verifies the token. It gives the redirect's parameters too.
type signIn struct {
	state, nonce, verifier string
	back                   url.Values
}

func start(t *testing.T, p *oidctest.Provider, c *oidc.Client) signIn {
	t.Helper()
	s := signIn{state: randomText(), nonce: randomText(), verifier: randomText()}
	address, err := c.AuthorizationURL(context.Background(), s.state, s.nonce, s.verifier)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Transport: p.Transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := browser.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	location, err := url.Parse(response.Header.Get("Location"))
	if response.StatusCode != http.StatusSeeOther || err != nil {
		t.Fatalf("authorize: %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	if got := location.Scheme + "://" + location.Host + location.Path; got != redirect {
		t.Fatalf("sent back to %s", got)
	}
	s.back = location.Query()
	if s.back.Get("state") != s.state {
		t.Fatal("the state did not come back")
	}
	return s
}

func (s signIn) finish(c *oidc.Client) (oidc.Identity, error) {
	return c.SignIn(context.Background(), s.back.Get("code"), s.verifier, s.nonce)
}

func TestASignInWithPKCE(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		methods      []string
	}{
		{"a confidential client with HTTP Basic", secret, nil},
		{"a confidential client with the secret in the form", secret, []string{"client_secret_post"}},
		{"a public client", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := provider(t, tc.secret)
			if tc.methods != nil {
				p.Discovery = func(document map[string]any) { document["token_endpoint_auth_methods_supported"] = tc.methods }
			}
			c := client(t, p, func(cfg *oidc.Config) { cfg.ClientSecret = tc.secret })
			s := start(t, p, c)
			if err := c.CheckResponseIssuer(context.Background(), s.back["iss"]); err != nil {
				t.Fatal(err)
			}
			who, err := s.finish(c)
			if err != nil {
				t.Fatal(err)
			}
			want := oidc.Identity{Issuer: oidctest.Issuer, Subject: "00u-dana", Email: "dana@invented.example",
				EmailVerified: true, Name: "Dana Reed"}
			if who != want {
				t.Fatalf("signed in %+v", who)
			}
			// The code is spent: the provider refuses it again, and nothing is retried.
			if _, err := s.finish(c); !errors.Is(err, oidc.ErrRefused) {
				t.Fatalf("a code used twice: %v", err)
			}
			if counts := p.Counts(); counts.Discovery != 1 || counts.Keys != 1 || counts.Token != 2 {
				t.Fatalf("requests %+v", counts)
			}
		})
	}
}

// A provider that refuses the client's own credentials says so apart from a refused code, so the operator
// knows to fix the secret: a wrong secret, and a public client where the provider registered a
// confidential one.
func TestARefusedClientIsNamed(t *testing.T) {
	p := provider(t, secret)
	for name, given := range map[string]string{"a wrong secret": "another-secret-0123456789", "no secret": ""} {
		c := client(t, p, func(cfg *oidc.Config) { cfg.ClientSecret = given })
		_, err := start(t, p, c).finish(c)
		if !errors.Is(err, oidc.ErrClient) || !errors.Is(err, oidc.ErrRefused) || strings.Contains(err.Error(), "secret-0123456789") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The authorization request carries the S256 challenge of the verifier, the state and the nonce, and only
// the verifier opens the code.
func TestTheCodeOpensOnlyWithItsVerifier(t *testing.T) {
	p := provider(t, secret)
	c := client(t, p)
	s := start(t, p, c)
	if _, err := c.SignIn(context.Background(), s.back.Get("code"), randomText(), s.nonce); !errors.Is(err, oidc.ErrRefused) {
		t.Fatalf("another verifier: %v", err)
	}
	address, _ := c.AuthorizationURL(context.Background(), s.state, s.nonce, s.verifier)
	query := mustQuery(t, address)
	if query.Get("code_challenge") != oidc.Challenge(s.verifier) || query.Get("code_challenge_method") != "S256" ||
		query.Get("nonce") != s.nonce || query.Get("scope") != "openid email profile" || query.Get("client_id") != clientID {
		t.Fatalf("authorization request %v", query)
	}
	if _, err := c.AuthorizationURL(context.Background(), "short", s.nonce, s.verifier); err == nil {
		t.Fatal("a short state was taken")
	}
}

func mustQuery(t *testing.T, address string) url.Values {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// A token the provider's key set does not verify, or whose claims are not this sign-in's, is refused, each
// with its own error. Every one of them is signed well unless the case is about the signature.
func TestTokensThatDoNotVerify(t *testing.T) {
	p := provider(t, secret)
	stranger := oidctest.NewKey(t, "k1", "RS256")
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, tc := range []struct {
		name string
		mint func(claims map[string]any) string
		want error
	}{
		{"a signature by another key under the provider's key ID", func(claims map[string]any) string {
			return oidctest.SignWith(stranger, map[string]any{"alg": "RS256", "kid": "k1"}, claims)
		}, oidc.ErrSignature},
		{"a changed claim under the original signature", func(claims map[string]any) string {
			signed := p.Sign(claims)
			claims["sub"] = "00u-someone-else"
			forged := oidctest.Unsigned(map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"}, claims)
			return forged + signed[strings.LastIndexByte(signed, '.'):]
		}, oidc.ErrSignature},
		{"alg none", func(claims map[string]any) string {
			return oidctest.Unsigned(map[string]any{"alg": "none"}, claims) + "."
		}, oidc.ErrAlgorithm},
		{"HS256 keyed with the provider's public key", func(claims map[string]any) string {
			return oidctest.HMACWithPublicKey(p.Signer(), claims)
		}, oidc.ErrAlgorithm},
		{"an RS256 signature by a key of 1024 bits", func(claims map[string]any) string {
			return oidctest.SignWith(&oidctest.Key{ID: "k1", Alg: "RS256", Private: weak},
				map[string]any{"alg": "RS256", "kid": "k1"}, claims)
		}, oidc.ErrSignature},
		{"an ES256 header over the RSA key", func(claims map[string]any) string {
			k := oidctest.NewKey(t, "k1", "ES256")
			return oidctest.SignWith(k, map[string]any{"alg": "ES256", "kid": "k1"}, claims)
		}, oidc.ErrUnknownKey},
		{"a key ID the provider never published", func(claims map[string]any) string {
			return oidctest.SignWith(p.Signer(), map[string]any{"alg": "RS256", "kid": "k9"}, claims)
		}, oidc.ErrUnknownKey},
		{"a critical header", func(claims map[string]any) string {
			return oidctest.SignWith(p.Signer(), map[string]any{"alg": "RS256", "kid": "k1", "crit": []string{"b64"}, "b64": false}, claims)
		}, oidc.ErrMalformed},
		{"an access token's type", func(claims map[string]any) string {
			return oidctest.SignWith(p.Signer(), map[string]any{"alg": "RS256", "kid": "k1", "typ": "at+jwt"}, claims)
		}, oidc.ErrMalformed},
		{"a claim named twice", func(claims map[string]any) string {
			payload, _ := json.Marshal(claims)
			doubled := append([]byte(`{"sub":"00u-someone-else",`), payload[1:]...)
			return oidctest.SignPayload(p.Signer(), map[string]any{"alg": "RS256", "kid": "k1"}, doubled)
		}, oidc.ErrMalformed},
		{"another issuer", func(claims map[string]any) string {
			claims["iss"] = "https://login.other.example"
			return p.Sign(claims)
		}, oidc.ErrIssuer},
		{"another audience", func(claims map[string]any) string {
			claims["aud"] = "another-client"
			return p.Sign(claims)
		}, oidc.ErrAudience},
		{"this audience beside another", func(claims map[string]any) string {
			claims["aud"], claims["azp"] = []string{clientID, "another-client"}, clientID
			return p.Sign(claims)
		}, oidc.ErrAudience},
		{"no audience", func(claims map[string]any) string {
			delete(claims, "aud")
			return p.Sign(claims)
		}, oidc.ErrAudience},
		{"another authorized party", func(claims map[string]any) string {
			claims["azp"] = "another-client"
			return p.Sign(claims)
		}, oidc.ErrAuthorizedParty},
		{"expired", func(claims map[string]any) string {
			claims["exp"], claims["iat"] = now-120, now-400
			return p.Sign(claims)
		}, oidc.ErrExpired},
		{"no expiry", func(claims map[string]any) string {
			delete(claims, "exp")
			return p.Sign(claims)
		}, oidc.ErrExpired},
		{"issued in the future", func(claims map[string]any) string {
			claims["iat"], claims["exp"] = now+600, now+900
			return p.Sign(claims)
		}, oidc.ErrIssuedAt},
		{"issued an hour ago", func(claims map[string]any) string {
			claims["iat"] = now - 3600
			return p.Sign(claims)
		}, oidc.ErrIssuedAt},
		{"no issue time", func(claims map[string]any) string {
			delete(claims, "iat")
			return p.Sign(claims)
		}, oidc.ErrIssuedAt},
		{"not valid before a time to come", func(claims map[string]any) string {
			claims["nbf"] = now + 600
			return p.Sign(claims)
		}, oidc.ErrNotYetValid},
		{"another nonce", func(claims map[string]any) string {
			claims["nonce"] = randomText()
			return p.Sign(claims)
		}, oidc.ErrNonce},
		{"no nonce", func(claims map[string]any) string {
			delete(claims, "nonce")
			return p.Sign(claims)
		}, oidc.ErrNonce},
		{"no subject", func(claims map[string]any) string {
			delete(claims, "sub")
			return p.Sign(claims)
		}, oidc.ErrSubject},
		{"a subject with a space", func(claims map[string]any) string {
			claims["sub"] = "00u dana"
			return p.Sign(claims)
		}, oidc.ErrSubject},
		{"padded base64", func(claims map[string]any) string {
			return p.Sign(claims) + "=="
		}, oidc.ErrMalformed},
		{"two parts", func(claims map[string]any) string {
			return oidctest.Unsigned(map[string]any{"alg": "RS256"}, claims)
		}, oidc.ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.Mint = tc.mint
			c := client(t, p)
			_, err := start(t, p, c).finish(c)
			if !errors.Is(err, tc.want) || !errors.Is(err, oidc.ErrToken) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// Keys of every algorithm the client verifies with sign in, a token that names no key is tried with each,
// and a key pinned to one algorithm verifies no other.
func TestEveryAlgorithmOfTheProvidersKeys(t *testing.T) {
	for _, alg := range []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"} {
		t.Run(alg, func(t *testing.T) {
			p := provider(t, secret)
			p.Rotate(t, "k-"+alg, alg)
			c := client(t, p)
			if _, err := start(t, p, c).finish(c); err != nil {
				t.Fatal(err)
			}
			p.Mint = func(claims map[string]any) string {
				return oidctest.SignWith(p.Signer(), map[string]any{"alg": alg}, claims)
			}
			if _, err := start(t, p, c).finish(c); err != nil {
				t.Fatalf("a token that names no key: %v", err)
			}
		})
	}
	p := provider(t, secret)
	c := client(t, p)
	p.Mint = func(claims map[string]any) string {
		return oidctest.SignWith(p.Signer(), map[string]any{"alg": "PS256", "kid": "k1"}, claims)
	}
	if _, err := start(t, p, c).finish(c); !errors.Is(err, oidc.ErrUnknownKey) {
		t.Fatalf("a PS256 token under a key the set pins to RS256: %v", err)
	}
}

// The key set is read once and kept. A token under a key the provider rotated in reads it again and
// verifies; a key the provider retired verifies nothing once the set is read again; and tokens under keys
// the set lacks read it at most once every ten seconds.
func TestKeyRotation(t *testing.T) {
	p := provider(t, secret)
	clock := time.Now()
	c := client(t, p, func(cfg *oidc.Config) { cfg.Now = func() time.Time { return clock } })
	signInNow := func() error {
		t.Helper()
		p.Mint = nil
		_, err := start(t, p, c).finish(c)
		return err
	}
	if err := signInNow(); err != nil {
		t.Fatal(err)
	}
	if err := signInNow(); err != nil || p.Counts().Keys != 1 {
		t.Fatalf("the kept key set: %v, %d reads", err, p.Counts().Keys)
	}
	old := p.Signer()
	clock = clock.Add(11 * time.Second)
	p.Rotate(t, "k2", "ES256")
	if err := signInNow(); err != nil || p.Counts().Keys != 2 {
		t.Fatalf("a rotated key: %v, %d reads", err, p.Counts().Keys)
	}
	p.Retire("k1")
	// The kept set still holds k1 until it is read again: a token under k1 verifies until then.
	oldToken := func(claims map[string]any) string {
		return oidctest.SignWith(old, map[string]any{"alg": "RS256", "kid": "k1"}, claims)
	}
	p.Mint = oldToken
	c2 := start(t, p, c)
	if _, err := c2.finish(c); err != nil {
		t.Fatalf("k1 before the set is read again: %v", err)
	}
	// An hour later the set is read again before use, and k1 is gone.
	clock = clock.Add(time.Hour)
	p.Mint = oldToken
	if _, err := start(t, p, c).finish(c); !errors.Is(err, oidc.ErrUnknownKey) || p.Counts().Keys != 3 {
		t.Fatalf("a retired key: %v, %d reads", err, p.Counts().Keys)
	}
	// Unknown keys within ten seconds of the last read do not read the set again.
	stranger := oidctest.NewKey(t, "k7", "RS256")
	p.Mint = func(claims map[string]any) string {
		return oidctest.SignWith(stranger, map[string]any{"alg": "RS256", "kid": "k7"}, claims)
	}
	for range 3 {
		if _, err := start(t, p, c).finish(c); !errors.Is(err, oidc.ErrUnknownKey) {
			t.Fatal(err)
		}
	}
	if reads := p.Counts().Keys; reads != 3 {
		t.Fatalf("%d reads for unknown keys within ten seconds", reads)
	}
	clock = clock.Add(11 * time.Second)
	if _, err := start(t, p, c).finish(c); !errors.Is(err, oidc.ErrUnknownKey) || p.Counts().Keys != 4 {
		t.Fatalf("an unknown key after ten seconds: %v, %d reads", err, p.Counts().Keys)
	}
}

// The configuration names who may sign in: an email the provider verified, in an allowed domain, and the
// tenant claim when one is set.
func TestWhoMaySignIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account oidctest.Account
		change  func(*oidc.Config)
		allowed bool
	}{
		{"an allowed domain", dana, nil, true},
		{"an allowed domain in capitals", oidctest.Account{Subject: "s", Email: "Dana@Invented.EXAMPLE", EmailVerified: true}, nil, true},
		{"another domain", oidctest.Account{Subject: "s", Email: "pat@other.example", EmailVerified: true}, nil, false},
		{"a domain that ends like an allowed one", oidctest.Account{Subject: "s", Email: "pat@notinvented.example", EmailVerified: true}, nil, false},
		{"a subdomain", oidctest.Account{Subject: "s", Email: "pat@mail.invented.example", EmailVerified: true}, nil, false},
		{"an email not verified", oidctest.Account{Subject: "s", Email: "dana@invented.example"}, nil, false},
		{"no email", oidctest.Account{Subject: "s"}, nil, false},
		{"the tenant", oidctest.Account{Subject: "s", Claims: map[string]any{"hd": "invented.example"}},
			func(cfg *oidc.Config) {
				cfg.AllowedDomains, cfg.TenantClaim, cfg.Tenant = nil, "hd", "invented.example"
			}, true},
		{"another tenant", oidctest.Account{Subject: "s", Claims: map[string]any{"tid": "tenant-b"}},
			func(cfg *oidc.Config) { cfg.AllowedDomains, cfg.TenantClaim, cfg.Tenant = nil, "tid", "tenant-a" }, false},
		{"no tenant claim", oidctest.Account{Subject: "s"},
			func(cfg *oidc.Config) { cfg.AllowedDomains, cfg.TenantClaim, cfg.Tenant = nil, "tid", "tenant-a" }, false},
		{"the domain and another tenant", oidctest.Account{Subject: "s", Email: "dana@invented.example", EmailVerified: true,
			Claims: map[string]any{"hd": "other.example"}},
			func(cfg *oidc.Config) { cfg.TenantClaim, cfg.Tenant = "hd", "invented.example" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := provider(t, secret)
			p.SetAccount(tc.account)
			changes := []func(*oidc.Config){}
			if tc.change != nil {
				changes = append(changes, tc.change)
			}
			c := client(t, p, changes...)
			_, err := start(t, p, c).finish(c)
			if (err == nil) != tc.allowed || (err != nil && !errors.Is(err, oidc.ErrNotAllowed)) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// email_verified sent as the text "true" or "false", as some providers send it, is read as that answer; any
// other value of it refuses the token.
func TestEmailVerifiedSentAsText(t *testing.T) {
	for verified, want := range map[any]error{"true": nil, "false": oidc.ErrNotAllowed, "yes": oidc.ErrToken, 1: oidc.ErrToken} {
		p := provider(t, secret)
		p.SetAccount(oidctest.Account{Subject: "s", Email: "dana@invented.example",
			Claims: map[string]any{"email_verified": verified}})
		c := client(t, p)
		_, err := start(t, p, c).finish(c)
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("email_verified %v: %v", verified, err)
		}
	}
}

// A key the provider publishes with the fully specified name Ed25519 verifies an EdDSA token, and a token
// that names its algorithm Ed25519.
func TestAKeyPublishedAsEd25519VerifiesEdDSA(t *testing.T) {
	p := provider(t, secret)
	k := p.Rotate(t, "k-ed", "Ed25519")
	c := client(t, p)
	if _, err := start(t, p, c).finish(c); err != nil {
		t.Fatalf("a token named Ed25519: %v", err)
	}
	p.Mint = func(claims map[string]any) string {
		return oidctest.SignWith(k, map[string]any{"alg": "EdDSA", "kid": "k-ed"}, claims)
	}
	if _, err := start(t, p, c).finish(c); err != nil {
		t.Fatalf("a token named EdDSA: %v", err)
	}
}

// A provider whose discovery document or key set fails is asked again only after five seconds, so sign-ins
// do not wait on it one after another.
func TestAFailingProviderIsAskedAgainAfterFiveSeconds(t *testing.T) {
	p := provider(t, secret)
	clock := time.Now()
	c := client(t, p, func(cfg *oidc.Config) { cfg.Now = func() time.Time { return clock } })
	p.Discovery = func(d map[string]any) { d["issuer"] = "https://login.other.example" }
	for range 3 {
		if _, err := c.AuthorizationURL(context.Background(), randomText(), randomText(), randomText()); !errors.Is(err, oidc.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if n := p.Counts().Discovery; n != 1 {
		t.Fatalf("a failed discovery was read %d times within five seconds", n)
	}
	clock = clock.Add(6 * time.Second)
	p.Discovery = nil
	p.FailKeys = true
	if _, err := c.AuthorizationURL(context.Background(), randomText(), randomText(), randomText()); err != nil || p.Counts().Discovery != 2 {
		t.Fatalf("after five seconds: %v, %d reads", err, p.Counts().Discovery)
	}
	for range 2 {
		if _, err := start(t, p, c).finish(c); !errors.Is(err, oidc.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if n := p.Counts().Keys; n != 1 {
		t.Fatalf("a failed key set was read %d times within five seconds", n)
	}
	clock = clock.Add(6 * time.Second)
	p.FailKeys = false
	if _, err := start(t, p, c).finish(c); err != nil || p.Counts().Keys != 2 {
		t.Fatalf("after five seconds: %v, %d reads", err, p.Counts().Keys)
	}
}

func TestAConfigurationThatCannotSignInSafelyIsRefused(t *testing.T) {
	good := oidc.Config{Issuer: oidctest.Issuer, ClientID: clientID, RedirectURL: redirect, AllowedDomains: []string{"invented.example"}}
	if _, err := oidc.New(good); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*oidc.Config){
		"no domain and no tenant":         func(c *oidc.Config) { c.AllowedDomains = nil },
		"an http issuer":                  func(c *oidc.Config) { c.Issuer = "http://" + oidctest.Host },
		"an issuer with a query":          func(c *oidc.Config) { c.Issuer += "?tenant=a" },
		"an issuer with a user":           func(c *oidc.Config) { c.Issuer = "https://someone@" + oidctest.Host },
		"no client ID":                    func(c *oidc.Config) { c.ClientID = "" },
		"a client ID with a space":        func(c *oidc.Config) { c.ClientID = "a b" },
		"an http redirect":                func(c *oidc.Config) { c.RedirectURL = "http://127.0.0.1:8443/api/v1/session/oidc/callback" },
		"a domain in capitals":            func(c *oidc.Config) { c.AllowedDomains = []string{"Invented.example"} },
		"an address as a domain":          func(c *oidc.Config) { c.AllowedDomains = []string{"dana@invented.example"} },
		"a tenant without its claim":      func(c *oidc.Config) { c.Tenant = "a" },
		"a tenant claim of another name":  func(c *oidc.Config) { c.TenantClaim, c.Tenant = "org", "a" },
		"a secret with a control charact": func(c *oidc.Config) { c.ClientSecret = "secret\n" },
	} {
		cfg := good
		change(&cfg)
		if _, err := oidc.New(cfg); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
}

// A discovery document of another issuer, with an endpoint that is not https, or without the S256
// challenge, signs nobody in; and an authorization response must carry the provider's iss, which it says
// it adds.
func TestDiscoveryRefusals(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"another issuer":         func(d map[string]any) { d["issuer"] = "https://login.other.example" },
		"an http token endpoint": func(d map[string]any) { d["token_endpoint"] = "http://" + oidctest.Host + "/token" },
		"no key set":             func(d map[string]any) { delete(d, "jwks_uri") },
		"no S256 challenge":      func(d map[string]any) { d["code_challenge_methods_supported"] = []string{"plain"} },
		"no secret method":       func(d map[string]any) { d["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := provider(t, secret)
			p.Discovery = edit
			c := client(t, p)
			if _, err := c.AuthorizationURL(context.Background(), randomText(), randomText(), randomText()); !errors.Is(err, oidc.ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
		})
	}
	p := provider(t, secret)
	c := client(t, p)
	for name, given := range map[string][]string{"none": nil, "another": {"https://login.other.example"},
		"two": {oidctest.Issuer, oidctest.Issuer}} {
		if err := c.CheckResponseIssuer(context.Background(), given); !errors.Is(err, oidc.ErrIssuer) {
			t.Errorf("iss %s: %v", name, err)
		}
	}
}

// Without a transport of its own the client reaches nothing in a process that is not live, as in these
// tests, and a refusal names no address, code or secret.
func TestNothingIsReachedWithoutATransportAndNothingIsRepeated(t *testing.T) {
	c, err := oidc.New(oidc.Config{Issuer: oidctest.Issuer, ClientID: clientID, ClientSecret: secret,
		RedirectURL: redirect, AllowedDomains: []string{"invented.example"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SignIn(context.Background(), "invented-code", randomText(), randomText())
	if !errors.Is(err, oidc.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	for _, held := range []string{oidctest.Host, "invented-code", secret, clientID} {
		if strings.Contains(err.Error(), held) {
			t.Errorf("the refusal holds %q", held)
		}
	}
	if c.EndSessionURL(context.Background()) != "" {
		t.Fatal("an end-session address without a provider")
	}
}

func TestTheEndSessionAddressIsTheProvidersForThisClient(t *testing.T) {
	p := provider(t, secret)
	c := client(t, p)
	if got := c.EndSessionURL(context.Background()); got != oidctest.Issuer+"/logout?client_id="+clientID {
		t.Fatalf("end session %q", got)
	}
	none := provider(t, secret)
	none.Discovery = func(d map[string]any) { delete(d, "end_session_endpoint") }
	if got := client(t, none).EndSessionURL(context.Background()); got != "" {
		t.Fatalf("a provider that publishes none: %q", got)
	}
}
