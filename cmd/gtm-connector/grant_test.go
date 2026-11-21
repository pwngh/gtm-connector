package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oidc/oidctest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// The account of the live workspace these tests grant: a Salesforce account ID, invented.
const liveAccount = "acc-sf-001000000000000AAA"

var (
	danaAccount = oidctest.Account{Subject: "00u-dana", Email: "dana@invented.example", Name: "Dana Reed", EmailVerified: true}
	oidcEnv     = map[string]string{"GTM_OIDC_CLIENT_ID": "connector-invented-client", "GTM_OIDC_WORKSPACE": "acme",
		"GTM_OIDC_ALLOWED_DOMAINS": "invented.example", "GTM_OIDC_REDIRECT_URL": "", "GTM_OIDC_TENANT_CLAIM": "",
		"GTM_OIDC_TENANT": "", "GTM_OIDC_SESSION_MINUTES": "", "GTM_DEMO_SIGN_IN": "off", "GTM_OIDC_ISSUER": oidctest.Issuer}
)

// liveWorkspace makes the workspace acme, of real sources, holding one account.
func liveWorkspace(t *testing.T, d *demoDatabase) postgres.Workspace {
	t.Helper()
	w, err := d.owner.EnsureWorkspace(ctx, "acme", "live", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.EnsureAccount(ctx, w.ID, liveAccount, "Invented Renewal Co"); err != nil {
		t.Fatal(err)
	}
	return w
}

// companyServe is serve as serve builds it from the environment, signing the people of acme in through the
// invented provider, with the demo sign-in off.
func companyServe(t *testing.T, d *demoDatabase) (*served, *oidctest.Provider) {
	t.Helper()
	for name, value := range oidcEnv {
		t.Setenv(name, value)
	}
	var p *oidctest.Provider
	s := startServeWith(t, d, nil, func(origin string, store *postgres.Store, logs *slog.Logger) signInSurface {
		p = oidctest.New(t, oidctest.Client{ID: oidcEnv["GTM_OIDC_CLIENT_ID"],
			RedirectURL: origin + httptransport.OIDCCallbackPath}, danaAccount)
		settings, err := loadSignIn(config{publicOrigin: origin})
		if err != nil {
			t.Fatal(err)
		}
		people, err := settings.open(ctx, store, "", p.Transport(), logs)
		if err != nil {
			t.Fatal(err)
		}
		if got := people.described(); got != "identity provider (public client) for acme" {
			t.Fatalf("serve describes its sign-ins as %q", got)
		}
		return people
	})
	return s, p
}

// browserFor reaches serve, trusting its certificate, and the invented provider, following no redirect.
type browserFor struct {
	t      *testing.T
	origin string
	client *http.Client
}

type routes struct{ provider, serve http.RoundTripper }

func (r routes) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host == oidctest.Host {
		return r.provider.RoundTrip(request)
	}
	return r.serve.RoundTrip(request)
}

func newBrowserFor(t *testing.T, s *served, p *oidctest.Provider) *browserFor {
	jar, _ := cookiejar.New(nil)
	serve := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: s.roots, MinVersion: tls.VersionTLS13}}
	t.Cleanup(serve.CloseIdleConnections)
	return &browserFor{t: t, origin: "https://" + s.address, client: &http.Client{Jar: jar,
		Transport:     routes{provider: p.Transport(), serve: serve},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browserFor) do(method, address string, body []byte, header map[string]string) (int, string, string) {
	b.t.Helper()
	if strings.HasPrefix(address, "/") {
		address = b.origin + address
	}
	request, err := http.NewRequest(method, address, bytes.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	for name, value := range header {
		request.Header.Set(name, value)
	}
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Location"), string(raw)
}

// signIn goes through the provider and comes back signed in, to the page it started from.
func (b *browserFor) signIn() {
	b.t.Helper()
	location := httptransport.OIDCStartPath
	for range 3 {
		status, next, _ := b.do(http.MethodGet, location, nil, nil)
		if status != http.StatusSeeOther {
			b.t.Fatalf("%s answered %d", location, status)
		}
		location = next
	}
	if location != b.origin+"/" {
		b.t.Fatalf("the sign-in came back to %q", location)
	}
}

func (b *browserFor) accounts() string {
	b.t.Helper()
	status, _, body := b.do(http.MethodGet, "/api/v1/accounts", nil, nil)
	if status != http.StatusOK {
		b.t.Fatalf("accounts: %d %s", status, body)
	}
	return body
}

type grantResult struct {
	Account     string `json:"account"`
	AccountName string `json:"account_name"`
	State       string
	Changed     bool
	ReceiptID   string `json:"receipt_id"`
	Person      struct {
		ID, Name, Subject string
		Email             *string
	}
}

// What serve builds signs a person in through the provider for a live workspace, with the demo sign-in
// off. The person reads nothing until grant gives them an account, then that account, until grant
// --revoke ends it. Each change is a receipt, and so is each refusal of a person or an account.
func TestGrantsThroughTheCommandOpenAccountsInTheBrowser(t *testing.T) {
	d := setUpDemo(t)
	w := liveWorkspace(t, d)
	s, p := companyServe(t, d)
	b := newBrowserFor(t, s, p)

	status, _, body := b.do(http.MethodGet, "/api/v1/session", nil, nil)
	var session struct {
		CSRF   string                    `json:"csrf_token"`
		SignIn struct{ OIDC, Demo bool } `json:"sign_in"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &session) != nil || !session.SignIn.OIDC || session.SignIn.Demo {
		t.Fatalf("the session: %d %s", status, body)
	}
	login, _ := json.Marshal(map[string]string{"login": demoLogin, "password": demoPassword})
	if status, _, body := b.do(http.MethodPost, "/api/v1/session", login, map[string]string{"Origin": b.origin,
		"X-CSRF-Token": session.CSRF, "Content-Type": "application/json"}); status != 403 ||
		!strings.Contains(body, "demo_sign_in_off") {
		t.Fatalf("the demo sign-in with it off: %d %s", status, body)
	}

	b.signIn()
	if got := b.accounts(); got != `{"accounts":[]}`+"\n" {
		t.Fatalf("a new person's accounts: %s", got)
	}
	var listed []struct {
		Name, Subject string
		Email         string
		Grants        []any
	}
	d.result(t, &listed, "grants", "--state-dir", "acme")
	if len(listed) != 1 || listed[0].Name != "Dana Reed" || listed[0].Email != "dana@invented.example" ||
		listed[0].Subject != "00u-dana" || len(listed[0].Grants) != 0 {
		t.Fatalf("grants: %+v", listed)
	}

	var granted grantResult
	d.result(t, &granted, "grant", liveAccount, "--to", "Dana@Invented.example", "--state-dir", "acme")
	if granted.State != "live" || !granted.Changed || granted.Person.Subject != "00u-dana" || granted.ReceiptID == "" {
		t.Fatalf("grant: %+v", granted)
	}
	if got := b.accounts(); got != `{"accounts":[{"account":"`+liveAccount+`","name":"Invented Renewal Co"}]}`+"\n" {
		t.Fatalf("after the grant: %s", got)
	}
	var again grantResult
	d.result(t, &again, "grant", liveAccount, "--to", "00u-dana", "--state-dir", "acme")
	if again.Changed || again.State != "live" {
		t.Fatalf("a grant held already: %+v", again)
	}
	stdout, _ := d.succeeded(t, "grants", "--state-dir", "acme")
	if !strings.Contains(stdout, "Dana Reed\tdana@invented.example\t00u-dana\t"+liveAccount+"\tlive\t") {
		t.Fatalf("grants as text:\n%s", stdout)
	}

	var revoked grantResult
	d.result(t, &revoked, "grant", liveAccount, "--to", "dana@invented.example", "--revoke", "--state-dir", "acme")
	if revoked.State != "revoked" || !revoked.Changed {
		t.Fatalf("revoke: %+v", revoked)
	}
	if got := b.accounts(); got != `{"accounts":[]}`+"\n" {
		t.Fatalf("after the revocation: %s", got)
	}
	stdout, _ = d.succeeded(t, "grant", liveAccount, "--to", "dana@invented.example", "--revoke", "--state-dir", "acme")
	if !strings.HasPrefix(stdout, "Dana Reed (dana@invented.example) holds no live grant on "+liveAccount+
		" (Invented Renewal Co). Nothing changed. Receipt ") {
		t.Fatalf("a revocation of nothing, as text: %s", stdout)
	}

	// Refusals: a person or an account the workspace does not have is refused under a receipt; an
	// argument that is not one is refused before anything is written.
	for _, c := range []struct {
		args    []string
		message string
	}{
		{[]string{"grant", liveAccount, "--to", "nobody@invented.example"}, "No person of this workspace has that email or subject."},
		{[]string{"grant", "acc-sf-001000000000000ZZZ", "--to", "dana@invented.example"}, "This workspace holds no account by that ID."},
	} {
		if _, message, _ := d.refusal(t, 2, append(c.args, "--state-dir", "acme")...); !strings.HasPrefix(message, c.message) {
			t.Errorf("%v: %s", c.args, message)
		}
	}
	receipts := func() []receiptRow {
		var rows []receiptRow
		d.result(t, &rows, "audit", "--state-dir", "acme")
		return rows
	}
	before := len(receipts())
	for _, args := range [][]string{
		{"grant", liveAccount, "--to", "two words"},
		{"grant", "not-an-account", "--to", "dana@invented.example"},
		{"grant", liveAccount},
		{"grants", "--to", "line\nbreak"},
	} {
		d.refusal(t, 2, append(args, "--state-dir", "acme")...)
	}
	d.refusal(t, 2, "grant", liveAccount, "--to", "dana@invented.example", "--state-dir", "nowhere")
	rows := receipts()
	if len(rows) != before {
		t.Fatalf("refused arguments left %d receipts", len(rows)-before)
	}
	var outcomes []string
	for _, r := range rows {
		reason := ""
		if r.Error != nil {
			reason = *r.Error
		}
		outcomes = append(outcomes, r.Tool+" "+r.Status+" "+reason+" "+strings.Join(r.Rows, ","))
	}
	grantRow := "grants/" + granted.Person.ID + "/" + liveAccount
	want := []string{"grant_account ok  " + grantRow, "grant_account ok  " + grantRow, "revoke_grant ok  " + grantRow,
		"revoke_grant ok  " + grantRow, "grant_account error person_not_found ", "grant_account error account_not_found "}
	if strings.Join(outcomes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the receipts:\n%s", strings.Join(outcomes, "\n"))
	}
	if table, _ := d.succeeded(t, "audit", "--state-dir", "acme"); !strings.Contains(table, "\tgrant_account\t") ||
		!strings.Contains(table, "\trevoke_grant\t") {
		t.Fatalf("audit names the grant tools as tools it knows:\n%s", table)
	}

	// A second subject with the same verified email makes the email ambiguous; the subject still names
	// each. An email the provider did not verify names nobody.
	p.SetAccount(oidctest.Account{Subject: "00u-dana-2", Email: "dana@invented.example", Name: "Dana Two", EmailVerified: true})
	newBrowserFor(t, s, p).signIn()
	if _, message, _ := d.refusal(t, 2, "grant", liveAccount, "--to", "dana@invented.example", "--state-dir", "acme"); !strings.HasPrefix(message, "More than one person") {
		t.Fatalf("an ambiguous email: %s", message)
	}
	if last := receipts()[len(receipts())-1]; last.Tool != "grant_account" || last.Status != "error" ||
		last.Error == nil || *last.Error != "person_ambiguous" || last.HTTPStatus == nil || *last.HTTPStatus != 409 {
		t.Fatalf("the receipt of an ambiguous email: %+v", last)
	}
	var nobody []any
	d.result(t, &nobody, "grants")
	if nobody == nil || len(nobody) != 0 {
		t.Fatalf("grants of a workspace no one signed in to: %v", nobody)
	}
	d.result(t, &listed, "grants", "--to", "00u-dana-2", "--state-dir", "acme")
	if len(listed) != 1 || listed[0].Name != "Dana Two" {
		t.Fatalf("grants of one person: %+v", listed)
	}
	d.result(t, &granted, "grant", liveAccount, "--to", "00u-dana-2", "--state-dir", "acme")
	if granted.Person.Name != "Dana Two" || !granted.Changed {
		t.Fatalf("by subject: %+v", granted)
	}
	flow := sha256.Sum256([]byte("an invented flow"))
	session2 := sha256.Sum256([]byte("an invented session"))
	if _, _, err := d.owner.SignInPerson(ctx, postgres.Admission{PreSession: true, IDSHA256: flow[:],
		ExpiresAt: time.Now().Add(time.Minute)}, session2[:], w, postgres.PersonIdentity{Issuer: oidctest.Issuer,
		Subject: "00u-lee", Email: "lee@invented.example", DisplayName: "Lee"}, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	d.refusal(t, 2, "grant", liveAccount, "--to", "lee@invented.example", "--state-dir", "acme")
	d.result(t, &granted, "grant", liveAccount, "--to", "00u-lee", "--state-dir", "acme")
	if granted.Person.Email == nil || *granted.Person.Email != "lee@invented.example" {
		t.Fatalf("an unverified email, by subject: %+v", granted)
	}
	// No code, token or secret reached the log of serve.
	for _, held := range []string{p.LastToken(), "code="} {
		if held != "" && strings.Contains(s.logs.String(), held) {
			t.Errorf("the log holds %q", held)
		}
	}
}

// In the demo workspace, an account of the demo book can be granted before any command has read it.
func TestADemoBookAccountCanBeGrantedAtOnce(t *testing.T) {
	d := setUpDemo(t)
	flow := sha256.Sum256([]byte("an invented flow"))
	session := sha256.Sum256([]byte("an invented session"))
	if _, _, err := d.owner.SignInPerson(ctx, postgres.Admission{PreSession: true, IDSHA256: flow[:],
		ExpiresAt: time.Now().Add(time.Minute)}, session[:], d.workspace, postgres.PersonIdentity{Issuer: oidctest.Issuer,
		Subject: "00u-dana", Email: "dana@invented.example", EmailVerified: true, DisplayName: "Dana Reed"}, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	stdout, _ := d.succeeded(t, "grant", "acc-007", "--to", "dana@invented.example")
	if !strings.HasPrefix(stdout, "Granted acc-007 (") {
		t.Fatalf("grant: %s", stdout)
	}
}

// serve refuses, in one line before it listens, the demo sign-in beside a provider that signs in a live
// workspace, and every sign-in setting it cannot serve. No refusal repeats the issuer, which can name a
// company's tenant.
func TestServeRefusesSignInSettingsItCannotServe(t *testing.T) {
	d := setUpDemo(t)
	liveWorkspace(t, d)
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.setUp(certificateHosts(nil)); err != nil {
		t.Fatal(err)
	}
	creds.Close()
	issuer := "https://" + oidctest.Host + "/tenant-0f1e2d3c"
	base := map[string]string{"GTM_DATABASE_URL": d.appURL, "GTM_CREDENTIALS_DIR": dir, "GTM_LISTEN": "127.0.0.1:0",
		"GTM_CONTAINER_LISTEN": "", "GTM_PUBLIC_ORIGIN": "https://127.0.0.1:8443", "GTM_ALLOWED_HOSTS": "127.0.0.1:8443"}
	for name, value := range oidcEnv {
		base[name] = value
	}
	base["GTM_OIDC_ISSUER"] = issuer
	for _, c := range []struct {
		change map[string]string
		want   string
	}{
		{map[string]string{"GTM_DEMO_SIGN_IN": ""}, "the demo sign-in is on beside sign-in through the identity provider " +
			"for the live workspace acme; set GTM_DEMO_SIGN_IN=off"},
		{map[string]string{"GTM_DEMO_SIGN_IN": "on"}, "the demo sign-in is on beside sign-in through the identity provider " +
			"for the live workspace acme; set GTM_DEMO_SIGN_IN=off"},
		{map[string]string{"GTM_DEMO_SIGN_IN": "no"}, "GTM_DEMO_SIGN_IN is on or off"},
		{map[string]string{"GTM_OIDC_ISSUER": ""}, "GTM_OIDC_CLIENT_ID is set, and GTM_OIDC_ISSUER is not; set it to sign in through the identity provider"},
		{map[string]string{"GTM_OIDC_CLIENT_ID": ""}, "GTM_OIDC_ISSUER is set, and GTM_OIDC_CLIENT_ID is not"},
		{map[string]string{"GTM_OIDC_WORKSPACE": ""}, "GTM_OIDC_ISSUER is set, and GTM_OIDC_WORKSPACE, the workspace its people sign in to, is not"},
		{map[string]string{"GTM_OIDC_WORKSPACE": "nowhere"}, "GTM_OIDC_WORKSPACE names no workspace; make it first, as connect --state-dir NAME does"},
		{map[string]string{"GTM_OIDC_ALLOWED_DOMAINS": ""}, "sign-in through the identity provider: name the allowed email domains or the tenant, or anyone the provider knows could sign in"},
		{map[string]string{"GTM_OIDC_REDIRECT_URL": "https://127.0.0.1:8443/callback"}, "GTM_OIDC_REDIRECT_URL must be GTM_PUBLIC_ORIGIN followed by /api/v1/session/oidc/callback, as the provider has it registered"},
		{map[string]string{"GTM_OIDC_SESSION_MINUTES": "1440"}, "GTM_OIDC_SESSION_MINUTES is a whole number of minutes from 5 to 720"},
		{map[string]string{"GTM_OIDC_ISSUER": "http://" + oidctest.Host + "/tenant-0f1e2d3c"}, "sign-in through the identity provider: the issuer must be an https URL with no query or fragment"},
		{map[string]string{"GTM_OIDC_TENANT_CLAIM": "hd"}, "sign-in through the identity provider: a tenant needs both the claim and its value"},
	} {
		for name, value := range base {
			t.Setenv(name, value)
		}
		for name, value := range c.change {
			t.Setenv(name, value)
		}
		code, stdout, stderr := line(t, "serve")
		if code != 1 || stdout != "" || stderr != "gtm-connector: "+c.want+"\n" || strings.Contains(stderr, "tenant-0f1e2d3c") {
			t.Errorf("%v: exit %d\n%s", c.change, code, stderr)
		}
	}
}

// The confidential client's secret is the file oidc-client-secret in the credentials folder, held to the
// folder's rules. Without it the client is public.
func TestTheClientSecretIsACredentialFile(t *testing.T) {
	for name, value := range oidcEnv {
		t.Setenv(name, value)
	}
	settings, err := loadSignIn(config{publicOrigin: "https://127.0.0.1:8443"})
	if err != nil {
		t.Fatal(err)
	}
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if secret, err := settings.secret(creds); err != nil || secret != "" {
		t.Fatalf("without the file: %q %v", secret, err)
	}
	path := filepath.Join(dir, oidcSecretFile)
	if err := os.WriteFile(path, []byte("invented-client-secret-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := settings.secret(creds)
	if err != nil || secret != "invented-client-secret-0123456789" {
		t.Fatalf("the secret: %v", err)
	}
	client, err := settings.client(secret, nil)
	if err != nil || !client.Confidential() {
		t.Fatalf("a client with the secret: %v", err)
	}
	os.Chmod(path, 0o644)
	if _, err := settings.secret(creds); err == nil || strings.Contains(err.Error(), "invented-client-secret") {
		t.Fatalf("a secret others can read: %v", err)
	}
	os.WriteFile(path, []byte("\n"), 0o600)
	os.Chmod(path, 0o600)
	if _, err := settings.secret(creds); err == nil {
		t.Fatal("an empty secret was taken")
	}
}
