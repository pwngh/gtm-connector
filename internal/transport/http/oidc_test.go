package httptransport_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/oidc"
	"github.com/pwngh/gtm-connector/internal/oidc/oidctest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// These tests sign in through the invented identity provider of oidctest, with a browser that reaches the
// connector and the provider and follows each redirect by hand, on the fixture's database. The provider
// signs in the fixture's workspace, beside the demo sign-in.

const (
	oidcClientID = "connector-invented-client"
	oidcSecret   = "invented-client-secret-0123456789"
)

var (
	dana = oidctest.Account{Subject: "00u-dana", Email: "dana@invented.example", Name: "Dana Reed", EmailVerified: true}
	pat  = oidctest.Account{Subject: "00u-pat", Email: "pat@other.example", Name: "Pat Other", EmailVerified: true}
)

type companySite struct {
	*postgrestest.Fixture
	server   *httptest.Server
	origin   string
	provider *oidctest.Provider
	logs     *bytes.Buffer
}

// companySiteOn serves the fixture's database with sign-in through the invented provider, changed by
// change before the surface is made.
func companySiteOn(t *testing.T, f *postgrestest.Fixture, change func(*httptransport.Config)) *companySite {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	origin := "https://" + ts.Listener.Addr().String()
	p := oidctest.New(t, oidctest.Client{ID: oidcClientID, Secret: oidcSecret,
		RedirectURL: origin + httptransport.OIDCCallbackPath}, dana)
	client, err := oidc.New(oidc.Config{Issuer: oidctest.Issuer, ClientID: oidcClientID, ClientSecret: oidcSecret,
		RedirectURL: origin + httptransport.OIDCCallbackPath, AllowedDomains: []string{"invented.example"},
		Transport: p.Transport()})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	cfg := httptransport.Config{PublicOrigin: origin, AllowedHosts: []string{ts.Listener.Addr().String()},
		SessionKey: testKey, OIDC: &httptransport.OIDC{Client: client, Workspace: f.Workspace},
		Log: slog.New(slog.NewTextHandler(logs, nil))}
	if change != nil {
		change(&cfg)
	}
	s, err := httptransport.New(cfg, f.Store, authz.Evidence{Store: f.Store})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return &companySite{Fixture: f, server: ts, origin: origin, provider: p, logs: logs}
}

func companySiteFor(t *testing.T) *companySite {
	t.Helper()
	return companySiteOn(t, postgrestest.New(t), nil)
}

// both reaches the invented provider by its name and the connector by its address.
type both struct{ provider, connector http.RoundTripper }

func (b both) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == oidctest.Host {
		return b.provider.RoundTrip(r)
	}
	return b.connector.RoundTrip(r)
}

// person is a browser that follows no redirect by itself.
type person struct {
	t      *testing.T
	site   *companySite
	client *http.Client
}

func (s *companySite) person(t *testing.T) *person {
	jar, _ := cookiejar.New(nil)
	return &person{t: t, site: s, client: &http.Client{Jar: jar,
		Transport:     both{provider: s.provider.Transport(), connector: s.server.Client().Transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// step is one request: its status, where it redirects to, its Set-Cookie headers and its JSON body.
type step struct {
	status   int
	location string
	cookies  []string
	body     map[string]any
	raw      string
}

func (p *person) get(address string) step {
	p.t.Helper()
	return p.send(http.MethodGet, address, nil)
}

func (p *person) send(method, address string, header map[string]string) step {
	p.t.Helper()
	if strings.HasPrefix(address, "/") {
		address = p.site.origin + address
	}
	request, err := http.NewRequest(method, address, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	for name, value := range header {
		request.Header.Set(name, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		p.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	out := step{status: response.StatusCode, location: response.Header.Get("Location"),
		cookies: response.Header.Values("Set-Cookie"), raw: string(raw)}
	json.Unmarshal(raw, &out.body)
	return out
}

// start opens the sign-in and gives where it sends the browser.
func (p *person) start(back string) step {
	p.t.Helper()
	address := httptransport.OIDCStartPath
	if back != "" {
		address += "?return=" + url.QueryEscape(back)
	}
	sent := p.get(address)
	if sent.status != http.StatusSeeOther {
		p.t.Fatalf("start: %d %s", sent.status, sent.raw)
	}
	return sent
}

// approve takes the browser through the provider and gives the callback address it is sent back to.
func (p *person) approve(back string) string {
	p.t.Helper()
	at := p.start(back)
	if !strings.HasPrefix(at.location, oidctest.Issuer+"/authorize?") {
		p.t.Fatalf("the start sent the browser to %q", at.location)
	}
	approved := p.get(at.location)
	if approved.status != http.StatusSeeOther || !strings.HasPrefix(approved.location, p.site.origin+httptransport.OIDCCallbackPath+"?") {
		p.t.Fatalf("the provider answered %d to %q", approved.status, approved.location)
	}
	return approved.location
}

// signIn signs in through the provider and gives where the callback sends the browser.
func (p *person) signIn(back string) step {
	p.t.Helper()
	return p.get(p.approve(back))
}

func (p *person) cookie(name string) string {
	u, _ := url.Parse(p.site.origin)
	for _, c := range p.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (p *person) setCookie(name, value string) {
	u, _ := url.Parse(p.site.origin)
	p.client.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

func (p *person) session() map[string]any {
	p.t.Helper()
	return p.get("/api/v1/session").body
}

func signedInAs(session map[string]any) string {
	if session["signed_in"] != true {
		return ""
	}
	name, _ := session["principal"].(map[string]any)["display_name"].(string)
	return name
}

// refusedWith checks a callback's answer: back to the page asked for, with the reason, and no session.
func (p *person) refusedWith(sent step, back, reason string) {
	p.t.Helper()
	separator := "?"
	if strings.Contains(back, "?") {
		separator = "&"
	}
	if want := p.site.origin + back + separator + "sign_in_error=" + reason; sent.status != http.StatusSeeOther ||
		sent.location != want {
		p.t.Fatalf("refused to %q (%d), not %q", sent.location, sent.status, want)
	}
	for _, set := range sent.cookies {
		if strings.HasPrefix(set, "__Host-gtm-session=") {
			p.t.Fatalf("a refused sign-in set a session: %s", set)
		}
	}
	if name := signedInAs(p.session()); name != "" {
		p.t.Fatalf("signed in as %s after a refusal", name)
	}
}

func (s *companySite) people(t *testing.T) int {
	return rowCount(t, &site{Fixture: s.Fixture}, "SELECT count(*) FROM principals WHERE oidc_subject IS NOT NULL")
}

// A person signs in through the provider and comes back to the page they asked for, with a session whose
// cookie is the session cookie of any sign-in. They are a new principal of the workspace with no grant, so
// they read nothing until the operator grants them an account, and then that account alone.
func TestASignInThroughTheProvider(t *testing.T) {
	s := companySiteFor(t)
	receipts := func() int { return rowCount(t, &site{Fixture: s.Fixture}, "SELECT count(*) FROM receipts") }
	before := receipts()
	dana := s.person(t)
	first := dana.session()
	if ways, _ := first["sign_in"].(map[string]any); ways["oidc"] != true || ways["demo"] != true {
		t.Fatalf("the ways in: %v", first["sign_in"])
	}
	started := dana.start("/deals/acc-003?card=1")
	flowCookie := strings.Join(started.cookies, "\n")
	for _, attribute := range []string{"__Host-gtm-oidc=o1.", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(flowCookie, attribute) {
			t.Errorf("the flow cookie lacks %s: %s", attribute, flowCookie)
		}
	}
	asked, _ := url.Parse(started.location)
	for name, want := range map[string]string{"client_id": oidcClientID, "response_type": "code",
		"redirect_uri": s.origin + httptransport.OIDCCallbackPath, "code_challenge_method": "S256"} {
		if got := asked.Query().Get(name); got != want {
			t.Errorf("the authorization request's %s is %q", name, got)
		}
	}
	// The browser holds none of what the flow is checked by.
	for _, held := range []string{asked.Query().Get("state"), asked.Query().Get("nonce")} {
		if len(held) < 43 || strings.Contains(flowCookie, held) {
			t.Errorf("the flow cookie holds its state or nonce, or they are short")
		}
	}

	back := dana.get(dana.get(started.location).location)
	if back.status != http.StatusSeeOther || back.location != s.origin+"/deals/acc-003?card=1" {
		t.Fatalf("the callback: %d to %q %s", back.status, back.location, back.raw)
	}
	cookies := strings.Join(back.cookies, "\n")
	for _, attribute := range []string{"__Host-gtm-session=", "HttpOnly", "Secure", "SameSite=Lax", "__Host-gtm-oidc=; Path=/; Max-Age=0"} {
		if !strings.Contains(cookies, attribute) {
			t.Errorf("the callback's cookies lack %s: %s", attribute, cookies)
		}
	}
	session := dana.session()
	principal, _ := session["principal"].(map[string]any)
	if signedInAs(session) != "Dana Reed" || principal["kind"] != "person" || principal["workspace_id"] != s.Workspace.ID {
		t.Fatalf("session %v", session)
	}
	// The ID token and the client's secret reach neither the callback's answer nor the session.
	token, _ := json.Marshal(session)
	for _, held := range []string{s.provider.LastToken(), oidcSecret} {
		if strings.Contains(back.raw+strings.Join(back.cookies, "")+back.location+string(token), held) {
			t.Fatal("the browser was given the ID token or the secret")
		}
	}
	// The sign-in is one line of the log, naming the principal and the workspace, and no token, code,
	// secret or email.
	logged := s.logs.String()
	if want := `msg="signed in through the identity provider" principal=` + principal["id"].(string) + " workspace=demo"; !strings.Contains(logged, want) {
		t.Fatalf("the log does not say %q:\n%s", want, logged)
	}
	for _, held := range []string{s.provider.LastToken(), oidcSecret, "code=", "dana@invented.example"} {
		if strings.Contains(logged, held) {
			t.Fatalf("the log holds %q", held)
		}
	}
	if n := s.people(t); n != 1 {
		t.Fatalf("%d people", n)
	}
	if n := receipts(); n != before {
		t.Fatalf("a sign-in left %d receipts", n-before)
	}

	if r := dana.get("/api/v1/accounts"); r.status != 200 || r.raw != `{"accounts":[]}`+"\n" {
		t.Fatalf("a new person's accounts: %d %s", r.status, r.raw)
	}
	for _, path := range []string{"/api/v1/cards/" + s.Card.ID, "/api/v1/receipts/" + s.Read.ID, "/api/v1/accounts/acc-003/cards"} {
		if r := dana.get(path); r.status != 403 || r.body["error"].(map[string]any)["code"] != "not_permitted" {
			t.Errorf("a new person read %s: %d", path, r.status)
		}
	}
	id := principal["id"].(string)
	if err := s.Store.GrantAccount(t.Context(), s.Workspace.ID, id, postgrestest.Account); err != nil {
		t.Fatal(err)
	}
	if r := dana.get("/api/v1/accounts"); r.status != 200 || r.raw != `{"accounts":[{"account":"acc-003","name":"Invented account"}]}`+"\n" {
		t.Fatalf("after a grant: %d %s", r.status, r.raw)
	}
	if r := dana.get("/api/v1/cards/" + s.Card.ID); r.status != 200 {
		t.Fatalf("the granted card: %d", r.status)
	}
	// A grant on one account opens no other.
	other := postgrestest.Publish(t, s.Fixture, "acc-004", s.Reader.ID, "brief-other")
	if r := dana.get("/api/v1/cards/" + other.Card.ID); r.status != 403 {
		t.Fatalf("another account's card: %d", r.status)
	}
	if err := s.Store.RevokeAccountGrant(t.Context(), s.Workspace.ID, id, postgrestest.Account); err != nil {
		t.Fatal(err)
	}
	if r := dana.get("/api/v1/cards/" + s.Card.ID); r.status != 403 {
		t.Fatalf("after a revocation: %d", r.status)
	}

	// Signing in again finds the same principal by issuer and subject, and records the email and name
	// the provider gives now.
	s.provider.SetAccount(oidctest.Account{Subject: "00u-dana", Email: "dana.reed@invented.example",
		Name: "Dana R.", EmailVerified: true})
	again := s.person(t)
	again.signIn("")
	if session := again.session(); session["principal"].(map[string]any)["id"] != id || signedInAs(session) != "Dana R." {
		t.Fatalf("the second sign-in: %v", session)
	}
	if n := s.people(t); n != 1 {
		t.Fatalf("%d people after a second sign-in", n)
	}
	// Another subject with the same email is another person.
	s.provider.SetAccount(oidctest.Account{Subject: "00u-dana-2", Email: "dana.reed@invented.example", EmailVerified: true})
	s.person(t).signIn("")
	if n := s.people(t); n != 2 {
		t.Fatalf("%d people after a sign-in by another subject", n)
	}
}

// Each sign-in that does not hold is refused with its reason, back on the page it started from, and starts
// no session.
func TestSignInsThatAreRefused(t *testing.T) {
	s := companySiteFor(t)
	tokens := func() int { return s.provider.Counts().Token }

	t.Run("a wrong state", func(t *testing.T) {
		p := s.person(t)
		callback, _ := url.Parse(p.approve("/ops/coverage"))
		query := callback.Query()
		query.Set("state", strings.Repeat("A", 43))
		callback.RawQuery = query.Encode()
		before := tokens()
		p.refusedWith(p.get(callback.String()), "/ops/coverage", "failed")
		if tokens() != before {
			t.Fatal("a wrong state reached the token endpoint")
		}
	})
	t.Run("a state given twice", func(t *testing.T) {
		p := s.person(t)
		callback := p.approve("")
		state := mustParse(t, callback).Query().Get("state")
		p.refusedWith(p.get(callback+"&state="+url.QueryEscape(state)), "/", "failed")
	})
	t.Run("no iss though the provider adds it", func(t *testing.T) {
		p := s.person(t)
		callback := mustParse(t, p.approve(""))
		query := callback.Query()
		query.Del("iss")
		callback.RawQuery = query.Encode()
		p.refusedWith(p.get(callback.String()), "/", "failed")
	})
	t.Run("a code used twice", func(t *testing.T) {
		p := s.person(t)
		callback := p.approve("/deals")
		flow := p.cookie("__Host-gtm-oidc")
		if sent := p.get(callback); sent.status != http.StatusSeeOther || sent.location != s.origin+"/deals" {
			t.Fatalf("the first use: %d %q", sent.status, sent.location)
		}
		// The callback cleared the flow: the same address again finds none.
		p.refusedWithSession(p.get(callback), "/", "expired")
		// With the flow cookie kept and sent again, the used flow is refused before the code reaches the
		// provider, even one that would take it twice.
		s.provider.ReuseCodes = true
		defer func() { s.provider.ReuseCodes = false }()
		replayer := s.person(t)
		replayer.setCookie("__Host-gtm-oidc", flow)
		before := tokens()
		replayer.refusedWith(replayer.get(callback), "/deals", "expired")
		if tokens() != before {
			t.Fatal("a used flow reached the token endpoint")
		}
	})
	t.Run("a code the provider refuses", func(t *testing.T) {
		p := s.person(t)
		callback := mustParse(t, p.approve(""))
		query := callback.Query()
		query.Set("code", "invented-code-the-provider-never-gave")
		callback.RawQuery = query.Encode()
		p.refusedWith(p.get(callback.String()), "/", "failed")
	})
	t.Run("a replayed nonce", func(t *testing.T) {
		s.person(t).signIn("")
		earlier := s.provider.LastToken()
		s.provider.Mint = func(map[string]any) string { return earlier }
		defer func() { s.provider.Mint = nil }()
		p := s.person(t)
		p.refusedWith(p.signIn(""), "/", "failed")
	})
	t.Run("a token for another audience", func(t *testing.T) {
		s.provider.Mint = func(claims map[string]any) string {
			claims["aud"] = "another-client"
			return s.provider.Sign(claims)
		}
		defer func() { s.provider.Mint = nil }()
		p := s.person(t)
		p.refusedWith(p.signIn(""), "/", "failed")
	})
	t.Run("an expired token", func(t *testing.T) {
		s.provider.Mint = func(claims map[string]any) string {
			now := time.Now().Unix()
			claims["exp"], claims["iat"] = now-120, now-400
			return s.provider.Sign(claims)
		}
		defer func() { s.provider.Mint = nil }()
		p := s.person(t)
		p.refusedWith(p.signIn(""), "/", "failed")
	})
	t.Run("a bad signature", func(t *testing.T) {
		stranger := oidctest.NewKey(t, "k1", "RS256")
		s.provider.Mint = func(claims map[string]any) string {
			return oidctest.SignWith(stranger, map[string]any{"alg": "RS256", "kid": "k1"}, claims)
		}
		defer func() { s.provider.Mint = nil }()
		p := s.person(t)
		p.refusedWith(p.signIn(""), "/", "failed")
	})
	t.Run("a disallowed email domain", func(t *testing.T) {
		s.provider.SetAccount(pat)
		defer s.provider.SetAccount(dana)
		before := s.people(t)
		p := s.person(t)
		p.refusedWith(p.signIn("/deals"), "/deals", "not_allowed")
		if s.people(t) != before {
			t.Fatal("a person who may not sign in was made a principal")
		}
	})
	t.Run("a sign-in declined at the provider", func(t *testing.T) {
		s.provider.Deny = true
		defer func() { s.provider.Deny = false }()
		p := s.person(t)
		p.refusedWith(p.signIn(""), "/", "denied")
	})
	t.Run("an error that is not the flow's", func(t *testing.T) {
		p := s.person(t)
		p.approve("/deals")
		// Sent by anyone but the provider this flow went to, which alone knows the state.
		p.refusedWith(p.get(httptransport.OIDCCallbackPath+"?error=access_denied"), "/deals", "failed")
	})
	t.Run("no flow cookie", func(t *testing.T) {
		p := s.person(t)
		callback := p.approve("/deals")
		p.setCookie("__Host-gtm-oidc", "")
		p.refusedWith(p.get(callback), "/", "expired")
	})
	t.Run("a flow cookie of another install", func(t *testing.T) {
		other := companySiteOn(t, s.Fixture, func(c *httptransport.Config) {
			c.SessionKey = []byte("another-install-session-key-0123456789")
		})
		p := other.person(t)
		p.approve("")
		flow := p.cookie("__Host-gtm-oidc")
		q := s.person(t)
		callback := q.approve("")
		q.setCookie("__Host-gtm-oidc", flow)
		q.refusedWith(q.get(callback), "/", "expired")
	})
	t.Run("a disabled person", func(t *testing.T) {
		p := s.person(t)
		p.signIn("")
		id := p.session()["principal"].(map[string]any)["id"].(string)
		if err := s.Store.DisablePrincipal(t.Context(), s.Workspace.ID, id); err != nil {
			t.Fatal(err)
		}
		if name := signedInAs(p.session()); name != "" {
			t.Fatal("a disabled person's session still reads")
		}
		q := s.person(t)
		q.refusedWith(q.signIn(""), "/", "disabled")
	})
	// The log says why, in fixed words, and holds no code, state or token.
	logged := s.logs.String()
	for _, want := range []string{"reason=failed", "reason=expired", "reason=not_allowed", "reason=denied",
		"reason=disabled", "it is for another audience", "its signature does not verify", "it has expired",
		"its nonce does not match this sign-in"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log does not say %q", want)
		}
	}
	for _, secret := range []string{oidcSecret, s.provider.LastToken(), "code=", "dana@invented.example", "pat@other.example"} {
		if secret != "" && strings.Contains(logged, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}

// refusedWithSession is refusedWith for a browser that holds a session: the refusal leaves it as it was.
func (p *person) refusedWithSession(sent step, back, reason string) {
	p.t.Helper()
	if want := p.site.origin + back + "?sign_in_error=" + reason; sent.status != http.StatusSeeOther || sent.location != want {
		p.t.Fatalf("refused to %q, not %q", sent.location, want)
	}
}

func digestOf(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	return sum[:]
}

func mustParse(t *testing.T, address string) *url.URL {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Ops needs no role of its own. What Coverage and Activity read, the account list, an account's cards, a
// card, a receipt and the deals, holds the accounts a person is granted and no other, as Deals does, and a
// person granted every account reads the whole book.
func TestOpsReadsOnlyWhatAPersonIsGranted(t *testing.T) {
	s := companySiteFor(t)
	other := postgrestest.Publish(t, s.Fixture, "acc-004", s.Reader.ID, "brief-other")
	postgrestest.RegisterDeal(t, s.Fixture, postgrestest.Account, s.Reader.ID, "renewal")
	postgrestest.RegisterDeal(t, s.Fixture, "acc-004", s.Reader.ID, "renewal")
	dana := s.person(t)
	dana.signIn("")
	id := dana.session()["principal"].(map[string]any)["id"].(string)
	deals := func() string {
		t.Helper()
		listed := dana.get("/v1/deals")
		if listed.status != 200 {
			t.Fatalf("deals: %d %s", listed.status, listed.raw)
		}
		var accounts []string
		for _, deal := range listed.body["deals"].([]any) {
			accounts = append(accounts, deal.(map[string]any)["account"].(string))
		}
		return strings.Join(accounts, ",")
	}
	if got := deals(); got != "" {
		t.Fatalf("deals with no grant: %q", got)
	}
	if err := s.Store.GrantAccount(t.Context(), s.Workspace.ID, id, postgrestest.Account); err != nil {
		t.Fatal(err)
	}
	if got := deals(); got != postgrestest.Account {
		t.Fatalf("deals with one grant: %q", got)
	}
	if r := dana.get("/api/v1/accounts"); !strings.Contains(r.raw, postgrestest.Account) || strings.Contains(r.raw, "acc-004") {
		t.Fatalf("accounts with one grant: %s", r.raw)
	}
	for _, path := range []string{"/api/v1/accounts/acc-004/cards", "/api/v1/cards/" + other.Card.ID,
		"/api/v1/receipts/" + other.Read.ID, "/api/v1/receipts/" + other.Seal.ID} {
		if r := dana.get(path); r.status != 403 {
			t.Errorf("%s with no grant on its account: %d", path, r.status)
		}
	}
	if err := s.Store.GrantAccount(t.Context(), s.Workspace.ID, id, "acc-004"); err != nil {
		t.Fatal(err)
	}
	if got := deals(); got != postgrestest.Account+",acc-004" && got != "acc-004,"+postgrestest.Account {
		t.Fatalf("deals with every account granted: %q", got)
	}
	for _, path := range []string{"/api/v1/accounts/acc-004/cards", "/api/v1/cards/" + other.Card.ID,
		"/api/v1/receipts/" + other.Read.ID} {
		if r := dana.get(path); r.status != 200 {
			t.Errorf("%s with every account granted: %d", path, r.status)
		}
	}
}

// A sign-in returns only to a page of the app: any other address returns to "/".
func TestASignInReturnsOnlyToThisApp(t *testing.T) {
	s := companySiteFor(t)
	for asked, want := range map[string]string{
		"/deals/acc-003":                "/deals/acc-003",
		"/ops/coverage?account=acc-003": "/ops/coverage?account=acc-003",
		"//evil.example/deals":          "/",
		"https://evil.example/":         "/",
		"/\\evil.example":               "/",
		"/api/v1/accounts":              "/",
		"/deals/../api/v1/accounts":     "/",
		"/assets/app.js":                "/",
		"/deals?sign_in_error=failed":   "/",
		"/deals#fragment":               "/",
		"/" + strings.Repeat("d", 600):  "/",
		"deals":                         "/",
	} {
		p := s.person(t)
		if sent := p.signIn(asked); sent.status != http.StatusSeeOther || sent.location != s.origin+want {
			t.Errorf("asked %q: returned to %q", asked, sent.location)
		}
	}
}

// A person who signed in through the provider is offered its sign-out page when they sign out here; the
// demo sign-in offers none. Signing in through the provider ends the session the browser held.
func TestSigningOutOffersTheProvidersSignOut(t *testing.T) {
	s := companySiteFor(t)
	p := s.person(t)
	p.signIn("")
	csrf := p.session()["csrf_token"].(string)
	out := p.send(http.MethodDelete, "/api/v1/session", map[string]string{"Origin": s.origin, "X-CSRF-Token": csrf})
	if out.status != 200 || out.body["end_session_url"] != oidctest.Issuer+"/logout?client_id="+oidcClientID ||
		out.body["signed_in"] != false {
		t.Fatalf("sign-out: %d %s", out.status, out.raw)
	}
	if name := signedInAs(p.session()); name != "" {
		t.Fatal("still signed in")
	}

	demo := &site{Fixture: s.Fixture, server: s.server, origin: s.origin}
	if _, err := s.Store.EnsureDemoPrincipal(t.Context(), s.Workspace, "demo-reader", "Demo reader",
		digestOf(readerPassword)); err != nil {
		t.Fatal(err)
	}
	b := demo.browser(t)
	if r := b.signIn("demo-reader", readerPassword); r.status != 200 {
		t.Fatalf("the demo sign-in beside the provider: %d %s", r.status, r.raw)
	}
	if r := b.write(http.MethodDelete, "/api/v1/session", nil); r.status != 200 || strings.Contains(string(r.raw), "end_session_url") {
		t.Fatalf("a demo sign-out: %d %s", r.status, r.raw)
	}

	// A browser signed in with the demo sign-in that signs in through the provider holds one session.
	b.signIn("demo-reader", readerPassword)
	before := b.cookie()
	q := s.person(t)
	q.setCookie("__Host-gtm-session", before)
	q.signIn("")
	if name := signedInAs(q.session()); name != "Dana Reed" {
		t.Fatalf("signed in as %q", name)
	}
	if r := b.do(http.MethodGet, "/api/v1/accounts", nil, nil); r.status != 401 {
		t.Fatalf("the earlier session after a sign-in through the provider: %d", r.status)
	}
}

// A session of a person who signed in through the provider lasts as configured, and opens nothing once the
// provider no longer signs that workspace in here. With the demo sign-in off, the demo sign-in signs
// nobody in and a demo session opens nothing.
func TestSessionsFollowTheSignInsOffered(t *testing.T) {
	f := postgrestest.New(t)
	short := companySiteOn(t, f, func(c *httptransport.Config) { c.OIDC.SessionTTL = 7 * time.Minute })
	p := short.person(t)
	p.signIn("")
	session := p.session()
	expires, _ := time.Parse(time.RFC3339, session["expires_at"].(string))
	if signedInAs(session) != "Dana Reed" || time.Until(expires) < 6*time.Minute || time.Until(expires) > 8*time.Minute {
		t.Fatalf("a session configured for seven minutes: %v", session)
	}
	// The database's clock decides expiry: the session's times are moved past its end rather than waited for.
	conn, err := pgx.Connect(t.Context(), f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), `UPDATE sessions SET created_at = created_at - interval '8 minutes',
		expires_at = expires_at - interval '8 minutes' WHERE id_sha256 = $1`, digestOf(p.cookie("__Host-gtm-session"))); err != nil {
		t.Fatal(err)
	}
	if name := signedInAs(p.session()); name != "" {
		t.Fatal("a session outlived its lifetime")
	}

	long := companySiteOn(t, f, nil)
	q := long.person(t)
	q.signIn("")
	if expires, _ := time.Parse(time.RFC3339, q.session()["expires_at"].(string)); time.Until(expires) < 59*time.Minute ||
		time.Until(expires) > 61*time.Minute {
		t.Fatalf("the default session lasts until %v", expires)
	}
	cookie := q.cookie("__Host-gtm-session")

	// The same database served without the provider, or with it for another workspace: the cookie opens
	// nothing.
	plain := siteOn(t, f, 0)
	b := plain.browser(t)
	b.setCookie(cookie)
	if r := b.do(http.MethodGet, "/api/v1/accounts", nil, nil); r.status != 401 {
		t.Fatalf("without the provider: %d", r.status)
	}
	elsewhere, err := f.Store.EnsureWorkspace(t.Context(), "elsewhere", "demo", "Another workspace")
	if err != nil {
		t.Fatal(err)
	}
	moved := companySiteOn(t, f, func(c *httptransport.Config) { c.OIDC.Workspace = elsewhere })
	r := moved.person(t)
	r.setCookie("__Host-gtm-session", cookie)
	if name := signedInAs(r.session()); name != "" {
		t.Fatal("a session of another workspace's sign-in opened")
	}

	// The demo sign-in off: refused, and a demo session from before opens nothing.
	demo := plain.browser(t)
	if r := demo.signIn("demo-reader", readerPassword); r.status != 200 {
		t.Fatal(r.status)
	}
	off := companySiteOn(t, f, func(c *httptransport.Config) { c.DemoSignInOff = true })
	o := off.person(t)
	o.setCookie("__Host-gtm-session", demo.cookie())
	view := o.session()
	if ways, _ := view["sign_in"].(map[string]any); signedInAs(view) != "" || ways["demo"] != false || ways["oidc"] != true {
		t.Fatalf("a demo session with the demo sign-in off: %v", view)
	}
	offSite := &site{Fixture: f, server: off.server, origin: off.origin}
	c := offSite.browser(t)
	if r := c.signIn("demo-reader", readerPassword); r.status != 403 || r.code() != "demo_sign_in_off" {
		t.Fatalf("the demo sign-in while off: %d %s", r.status, r.raw)
	}
}

// Without the provider configured its two paths answer 404, and the session says so.
func TestWithoutAProviderItsPathsAreNotFound(t *testing.T) {
	s := newSite(t, 0)
	b := s.browser(t)
	if ways, _ := b.start().body["sign_in"].(map[string]any); ways["oidc"] != false || ways["demo"] != true {
		t.Fatalf("the ways in: %v", ways)
	}
	for _, path := range []string{httptransport.OIDCStartPath, httptransport.OIDCCallbackPath + "?code=x&state=y"} {
		if r := b.do(http.MethodGet, path, nil, nil); r.status != 404 || r.code() != "not_found" {
			t.Errorf("%s: %d %s", path, r.status, r.raw)
		}
	}
}

// The surface itself refuses the demo sign-in beside a provider that signs in a workspace of real sources.
func TestTheDemoSignInIsRefusedBesideAProviderOfALiveWorkspace(t *testing.T) {
	client, err := oidc.New(oidc.Config{Issuer: oidctest.Issuer, ClientID: oidcClientID,
		RedirectURL: "https://127.0.0.1:8443" + httptransport.OIDCCallbackPath, AllowedDomains: []string{"invented.example"}})
	if err != nil {
		t.Fatal(err)
	}
	config := func(kind string, demoOff bool) httptransport.Config {
		return httptransport.Config{PublicOrigin: "https://127.0.0.1:8443", AllowedHosts: []string{"127.0.0.1:8443"},
			SessionKey: testKey, DemoSignInOff: demoOff,
			OIDC: &httptransport.OIDC{Client: client, Workspace: postgres.Workspace{ID: "w", Slug: "acme", Kind: kind}}}
	}
	if _, err := httptransport.New(config("live", false), noSessions{}, authz.Evidence{}); err == nil ||
		!strings.Contains(err.Error(), "GTM_DEMO_SIGN_IN=off") {
		t.Fatalf("the demo sign-in beside a live workspace's provider: %v", err)
	}
	for _, c := range []httptransport.Config{config("live", true), config("demo", false)} {
		if _, err := httptransport.New(c, noSessions{}, authz.Evidence{}); err != nil {
			t.Fatal(err)
		}
	}
}

// The redirect the provider is registered with must be this origin's callback.
func TestTheRedirectMustBeThisOriginsCallback(t *testing.T) {
	client, err := oidc.New(oidc.Config{Issuer: oidctest.Issuer, ClientID: oidcClientID,
		RedirectURL: "https://127.0.0.1:8443/elsewhere", AllowedDomains: []string{"invented.example"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: testKey,
		OIDC: &httptransport.OIDC{Client: client, Workspace: postgres.Workspace{ID: "w", Kind: "demo"}}}, noSessions{}, authz.Evidence{})
	if err == nil || !strings.Contains(err.Error(), httptransport.OIDCCallbackPath) {
		t.Fatalf("got %v", err)
	}
}
