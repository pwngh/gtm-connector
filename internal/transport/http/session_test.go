package httptransport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// These tests run the surface over TLS on a real database, with a cookie
// jar standing in for the browser.

type site struct {
	*postgrestest.Fixture
	server   *httptest.Server
	origin   string
	reader   postgres.Principal
	outsider postgres.Principal
}

const (
	readerPassword   = "reader-password-0123456789-abcdefghijkl"
	outsiderPassword = "outsider-password-0123456789-abcdefghij"
)

func newSite(t *testing.T, sessionTTL time.Duration) *site {
	t.Helper()
	f := postgrestest.New(t)
	ts := httptest.NewUnstartedServer(nil)
	origin := "https://" + ts.Listener.Addr().String()
	s, err := httptransport.New(httptransport.Config{PublicOrigin: origin,
		AllowedHosts: []string{ts.Listener.Addr().String()}, SessionTTL: sessionTTL, SessionKey: testKey},
		f.Store, authz.Evidence{Store: f.Store})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s
	ts.StartTLS()
	t.Cleanup(ts.Close)
	result := &site{Fixture: f, server: ts, origin: origin}
	for login, password := range map[string]string{"demo-reader": readerPassword, "demo-outsider": outsiderPassword} {
		digest := sha256.Sum256([]byte(password))
		p, err := f.Store.EnsureDemoPrincipal(t.Context(), f.Workspace, login, "Demo "+login, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		if login == "demo-reader" {
			result.reader = p
			if err := f.Store.GrantAccount(t.Context(), f.Workspace.ID, p.ID, postgrestest.Account); err != nil {
				t.Fatal(err)
			}
		} else {
			result.outsider = p
		}
	}
	return result
}

type browser struct {
	t      *testing.T
	site   *site
	client *http.Client
	csrf   string
}

func (s *site) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: s.server.Client().Transport, Jar: jar}
	return &browser{t: t, site: s, client: client}
}

type reply struct {
	status int
	body   map[string]any
	raw    []byte
	header http.Header
}

func (r reply) code() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

func (b *browser) do(method, path string, body any, header map[string]string) reply {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	r, err := http.NewRequest(method, b.site.server.URL+path, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, value := range header {
		r.Header.Set(name, value)
	}
	response, err := b.client.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	out := reply{status: response.StatusCode, raw: raw, header: response.Header}
	json.Unmarshal(raw, &out.body)
	return out
}

// write sends a same-origin write with the browser's CSRF token.
func (b *browser) write(method, path string, body any) reply {
	return b.do(method, path, body, map[string]string{"Origin": b.site.origin, "X-CSRF-Token": b.csrf})
}

func (b *browser) start() reply {
	b.t.Helper()
	r := b.do(http.MethodGet, "/api/v1/session", nil, nil)
	if r.status != http.StatusOK {
		b.t.Fatalf("GET session: %d", r.status)
	}
	b.csrf, _ = r.body["csrf_token"].(string)
	return r
}

func (b *browser) signIn(login, password string) reply {
	b.t.Helper()
	b.start()
	r := b.write(http.MethodPost, "/api/v1/session", map[string]string{"login": login, "password": password})
	if token, ok := r.body["csrf_token"].(string); ok {
		b.csrf = token
	}
	return r
}

func (b *browser) cookie() string { return b.named("__Host-gtm-session") }

func (b *browser) preCookie() string { return b.named("__Host-gtm-pre") }

func (b *browser) named(name string) string {
	u, _ := url.Parse(b.site.server.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (b *browser) setCookie(value string) { b.setNamed("__Host-gtm-session", value) }

func (b *browser) setNamed(name, value string) {
	u, _ := url.Parse(b.site.server.URL)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

func TestSignInRotatesTheSessionAndCSRF(t *testing.T) {
	s := newSite(t, 0)
	b := s.browser(t)
	first := b.start()
	if first.body["signed_in"] != false || b.csrf == "" {
		t.Fatalf("pre-session %s", first.raw)
	}
	checkCookie := func(set, name string) {
		t.Helper()
		for _, attribute := range []string{name + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
			if !strings.Contains(set, attribute) {
				t.Errorf("cookie lacks %s: %s", attribute, set)
			}
		}
		if strings.Contains(strings.ToLower(set), "domain=") {
			t.Errorf("cookie names a domain: %s", set)
		}
	}
	checkCookie(first.header.Get("Set-Cookie"), "__Host-gtm-pre")
	preCookie, preCSRF := b.preCookie(), b.csrf

	login := map[string]string{"login": "demo-reader", "password": readerPassword}
	origin := map[string]string{"Origin": s.origin}
	if r := b.do(http.MethodPost, "/api/v1/session", login, origin); r.status != 403 || r.code() != "csrf_invalid" {
		t.Fatalf("sign-in without CSRF: %d %s", r.status, r.raw)
	}
	other := s.browser(t)
	other.start()
	if r := b.do(http.MethodPost, "/api/v1/session", login,
		map[string]string{"Origin": s.origin, "X-CSRF-Token": other.csrf}); r.status != 403 {
		t.Fatalf("sign-in with another session's CSRF token: %d", r.status)
	}
	if r := b.write(http.MethodPost, "/api/v1/session",
		map[string]string{"login": "demo-reader", "password": outsiderPassword}); r.status != 401 ||
		r.code() != "sign_in_refused" {
		t.Fatalf("wrong password: %d %s", r.status, r.raw)
	}
	if r := b.write(http.MethodPost, "/api/v1/session",
		map[string]string{"login": "nobody", "password": readerPassword}); r.status != 401 {
		t.Fatalf("unknown login: %d", r.status)
	}
	if r := b.write(http.MethodPost, "/api/v1/session",
		map[string]any{"login": "demo-reader", "password": readerPassword, "admin": true}); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}

	r := b.write(http.MethodPost, "/api/v1/session", login)
	if r.status != 200 || r.body["signed_in"] != true {
		t.Fatalf("sign-in: %d %s", r.status, r.raw)
	}
	checkCookie(strings.Join(r.header.Values("Set-Cookie"), "\n"), "__Host-gtm-session")
	principal := r.body["principal"].(map[string]any)
	if principal["demo"] != true || principal["workspace_kind"] != "demo" || principal["id"] != s.reader.ID {
		t.Fatalf("principal %v", principal)
	}
	signedCSRF := r.body["csrf_token"].(string)
	if b.cookie() == preCookie || signedCSRF == preCSRF {
		t.Fatal("the session ID or CSRF token did not rotate")
	}
	b.csrf = signedCSRF

	// The pre-session signs in once.
	stale := s.browser(t)
	stale.setNamed("__Host-gtm-pre", preCookie)
	stale.csrf = preCSRF
	if r := stale.write(http.MethodPost, "/api/v1/session", login); r.status != 403 {
		t.Fatalf("a used pre-session signed in again: %d", r.status)
	}
	// A stale CSRF token is refused, even with the live cookie.
	if r := b.do(http.MethodDelete, "/api/v1/session", nil,
		map[string]string{"Origin": s.origin, "X-CSRF-Token": preCSRF}); r.status != 403 || r.code() != "csrf_invalid" {
		t.Fatalf("sign-out with a stale CSRF token: %d %s", r.status, r.raw)
	}
	// Reloading the app gives the same token back.
	if again := b.do(http.MethodGet, "/api/v1/session", nil, nil); again.body["csrf_token"] != signedCSRF {
		t.Fatal("the CSRF token changed without a sign-in")
	}
	// A cross-origin write with the right cookie and token is still refused.
	if r := b.do(http.MethodDelete, "/api/v1/session", nil,
		map[string]string{"Origin": "https://evil.example", "X-CSRF-Token": signedCSRF}); r.status != 403 {
		t.Fatalf("cross-origin sign-out: %d", r.status)
	}
	if r := b.do(http.MethodDelete, "/api/v1/session", nil,
		map[string]string{"Origin": "null", "X-CSRF-Token": signedCSRF}); r.status != 403 {
		t.Fatalf("null-origin sign-out: %d", r.status)
	}
	if r := b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil); r.status != 200 {
		t.Fatalf("refused writes ended the session: %d", r.status)
	}

	if r := b.write(http.MethodDelete, "/api/v1/session", nil); r.status != 200 {
		t.Fatalf("sign-out: %d %s", r.status, r.raw)
	}
	if r := b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil); r.status != 401 {
		t.Fatalf("after sign-out: %d", r.status)
	}
}

func TestSessionsExpireAndCanBeRevoked(t *testing.T) {
	s := newSite(t, 300*time.Millisecond)
	b := s.browser(t)
	if r := b.signIn("demo-reader", readerPassword); r.status != 200 {
		t.Fatal(r.status)
	}
	if r := b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil); r.status != 200 {
		t.Fatalf("fresh session: %d", r.status)
	}
	time.Sleep(400 * time.Millisecond)
	if r := b.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil); r.status != 401 {
		t.Fatalf("expired session: %d", r.status)
	}

	long := newSite(t, time.Hour)
	c := long.browser(t)
	c.signIn("demo-reader", readerPassword)
	if _, err := long.Store.RevokePrincipalSessions(t.Context(), long.Workspace.ID, long.reader.ID); err != nil {
		t.Fatal(err)
	}
	if r := c.do(http.MethodGet, "/api/v1/cards/"+long.Card.ID, nil, nil); r.status != 401 {
		t.Fatalf("revoked session: %d", r.status)
	}
}

func TestEvidenceReadsNeedAGrant(t *testing.T) {
	s := newSite(t, 0)
	anonymous := s.browser(t)
	for _, path := range []string{"/api/v1/cards/" + s.Card.ID, "/api/v1/receipts/" + s.Read.ID,
		"/api/v1/accounts/acc-003/cards"} {
		if r := anonymous.do(http.MethodGet, path, nil, nil); r.status != 401 {
			t.Errorf("anonymous %s: %d", path, r.status)
		}
	}

	reader := s.browser(t)
	reader.signIn("demo-reader", readerPassword)
	card := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID, nil, nil)
	if card.status != 200 || len(card.body["units"].([]any)) != 2 || len(card.body["gaps"].([]any)) != 1 {
		t.Fatalf("card: %d %s", card.status, card.raw)
	}
	unit := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/units/2", nil, nil)
	if unit.status != 200 || unit.body["receipt"].(map[string]any)["id"] != s.Read.ID {
		t.Fatalf("unit: %d %s", unit.status, unit.raw)
	}
	if r := reader.do(http.MethodGet, "/api/v1/receipts/"+s.Seal.ID, nil, nil); r.status != 200 ||
		r.body["status"] != "ok" {
		t.Fatalf("receipt: %d", r.status)
	}
	bytesReply := reader.do(http.MethodGet, "/api/v1/cards/"+s.Card.ID+"/bytes", nil, nil)
	sum := sha256.Sum256(bytesReply.raw)
	if !bytes.Equal(bytesReply.raw, s.Card.Bytes) || bytesReply.header.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) {
		t.Fatal("card bytes differ")
	}
	if r := reader.do(http.MethodGet, "/api/v1/accounts/acc-003/cards", nil, nil); r.status != 200 ||
		len(r.body["cards"].([]any)) != 1 {
		t.Fatalf("account cards: %d", r.status)
	}
	for path, want := range map[string]int{
		"/api/v1/cards/00000000-0000-0000-0000-000000000000":    403,
		"/api/v1/cards/" + s.Card.ID + "/units/9":               403,
		"/api/v1/cards/" + s.Card.ID + "/units/01":              404,
		"/api/v1/receipts/00000000-0000-0000-0000-000000000000": 403,
		"/api/v1/accounts/acc-999/cards":                        403,
	} {
		if r := reader.do(http.MethodGet, path, nil, nil); r.status != want {
			t.Errorf("%s: %d", path, r.status)
		}
	}

	// An ungranted principal gets neither a unit nor its receipt: 403, not
	// an empty list.
	outsider := s.browser(t)
	if r := outsider.signIn("demo-outsider", outsiderPassword); r.status != 200 {
		t.Fatal(r.status)
	}
	for _, path := range []string{"/api/v1/cards/" + s.Card.ID, "/api/v1/cards/" + s.Card.ID + "/units/1",
		"/api/v1/cards/" + s.Card.ID + "/bytes", "/api/v1/receipts/" + s.Read.ID, "/api/v1/receipts/" + s.Seal.ID,
		"/api/v1/accounts/acc-003/cards"} {
		r := outsider.do(http.MethodGet, path, nil, nil)
		if r.status != 403 || r.code() != "not_permitted" || strings.Contains(string(r.raw), "acc-003\",\"cards") {
			t.Errorf("outsider %s: %d %s", path, r.status, r.raw)
		}
	}
}

// Item 8: one pre-session signs in once, however many requests race.
func TestConcurrentSignInsFromOnePreSession(t *testing.T) {
	s := newSite(t, 0)
	b := s.browser(t)
	b.start()
	pre := b.preCookie()
	statuses := make(chan int, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := s.browser(t)
			c.setNamed("__Host-gtm-pre", pre)
			c.csrf = b.csrf
			statuses <- c.write(http.MethodPost, "/api/v1/session",
				map[string]string{"login": "demo-reader", "password": readerPassword}).status
		}()
	}
	wg.Wait()
	close(statuses)
	won := 0
	for status := range statuses {
		if status == 200 {
			won++
		} else if status != 403 {
			t.Errorf("status %d", status)
		}
	}
	if won != 1 || liveSessionRows(t, s) != 1 {
		t.Fatalf("%d sign-ins won, %d live sessions", won, liveSessionRows(t, s))
	}
}

// Item 8: a session revoked between admission and rotation signs in nobody.
type revokingSessions struct {
	httptransport.Sessions
	store *postgres.Store
}

func (r revokingSessions) SignIn(ctx context.Context, admitted postgres.Admission, id []byte,
	p postgres.Principal, ttl time.Duration) (time.Time, error) {
	if !admitted.PreSession {
		r.store.RevokeSession(ctx, admitted.IDSHA256)
	}
	return r.Sessions.SignIn(ctx, admitted, id, p, ttl)
}

func TestARevocationBetweenAdmissionAndRotation(t *testing.T) {
	f := postgrestest.New(t)
	ts := httptest.NewUnstartedServer(nil)
	origin := "https://" + ts.Listener.Addr().String()
	server, err := httptransport.New(httptransport.Config{PublicOrigin: origin,
		AllowedHosts: []string{ts.Listener.Addr().String()}, SessionKey: testKey},
		revokingSessions{f.Store, f.Store}, authz.Evidence{Store: f.Store})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = server
	ts.StartTLS()
	defer ts.Close()
	digest := sha256.Sum256([]byte(readerPassword))
	if _, err := f.Store.EnsureDemoPrincipal(t.Context(), f.Workspace, "demo-reader", "Demo reader", digest[:]); err != nil {
		t.Fatal(err)
	}
	s := &site{Fixture: f, server: ts, origin: origin}
	b := s.browser(t)
	if r := b.signIn("demo-reader", readerPassword); r.status != 200 {
		t.Fatalf("first sign-in: %d", r.status)
	}
	// Signing in again from the session: the session is revoked after
	// admission and before rotation, so no new session may appear.
	if r := b.write(http.MethodPost, "/api/v1/session",
		map[string]string{"login": "demo-reader", "password": readerPassword}); r.status != 403 {
		t.Fatalf("a revoked session rotated into a new one: %d", r.status)
	}
	if liveSessionRows(t, s) != 0 {
		t.Fatal("a live session remains")
	}
}

// Item 8: cookieless GETs leave no rows.
func TestCookielessVisitsLeaveNoRows(t *testing.T) {
	s := newSite(t, 0)
	for range 100 {
		s.browser(t).start()
	}
	if n := rowCount(t, s, "SELECT count(*) FROM sessions") + rowCount(t, s, "SELECT count(*) FROM consumed_presessions"); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

func liveSessionRows(t *testing.T, s *site) int {
	return rowCount(t, s, "SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at > now()")
}

func rowCount(t *testing.T, s *site, query string) int {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	var n int
	if err := conn.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
