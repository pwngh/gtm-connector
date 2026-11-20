package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

const demoPassword = "demo-password-0123456789-abcdefghijklmn"

// demoDatabase is a migrated database with the demo workspace and the demo
// principal, as setup leaves it, and an app role made as setup makes it.
type demoDatabase struct {
	owner     *postgres.Store
	ownerURL  string
	appURL    string
	workspace postgres.Workspace
}

func setUpDemo(t *testing.T) *demoDatabase {
	t.Helper()
	d := &demoDatabase{ownerURL: pgtest.URL(t)}
	var err error
	if d.owner, err = postgres.Open(ctx, d.ownerURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.owner.Close)
	if err := d.owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if d.workspace, err = d.owner.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(demoPassword))
	if _, err := d.owner.EnsureDemoPrincipal(ctx, d.workspace, demoLogin, "Demo reviewer", digest[:]); err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	rand.Read(suffix)
	role := "gtm_app_" + hex.EncodeToString(suffix)
	if err := d.owner.EnsureAppRole(ctx, role, "app-password-for-this-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), d.ownerURL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		conn.Exec(context.Background(), "DROP OWNED BY "+role)
		conn.Exec(context.Background(), "DROP ROLE "+role)
	})
	parsed, err := url.Parse(d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(role, "app-password-for-this-test")
	d.appURL = parsed.String()
	return d
}

// demo runs the command as a person does, with GTM_DATABASE_URL naming databaseURL.
func (d *demoDatabase) demo(t *testing.T, databaseURL string, args ...string) error {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", databaseURL)
	return demoCommand(ctx, args)
}

func (d *demoDatabase) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn, err := pgx.Connect(ctx, d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// signedIn is a browser signed in to the server as the demo principal.
type signedIn struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
}

func signInToDemo(t *testing.T, store *postgres.Store) *signedIn {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	origin := "https://" + ts.Listener.Addr().String()
	surface, err := httptransport.New(httptransport.Config{PublicOrigin: origin,
		AllowedHosts: []string{ts.Listener.Addr().String()}, SessionKey: bytes.Repeat([]byte("k"), 32)},
		store, authz.Evidence{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = surface
	ts.StartTLS()
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	b := &signedIn{t: t, server: ts, client: &http.Client{Transport: ts.Client().Transport, Jar: jar}}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	b.json(b.send(http.MethodGet, "/api/v1/session", nil, nil), &session)
	body, _ := json.Marshal(map[string]string{"login": demoLogin, "password": demoPassword})
	reply := b.send(http.MethodPost, "/api/v1/session", body, map[string]string{"Origin": origin,
		"X-CSRF-Token": session.CSRF, "Content-Type": "application/json"})
	if reply.StatusCode != http.StatusOK {
		t.Fatalf("sign-in: %d", reply.StatusCode)
	}
	reply.Body.Close()
	return b
}

func (b *signedIn) send(method, path string, body []byte, header map[string]string) *http.Response {
	b.t.Helper()
	r, err := http.NewRequest(method, b.server.URL+path, bytes.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	for name, value := range header {
		r.Header.Set(name, value)
	}
	reply, err := b.client.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	return reply
}

func (b *signedIn) get(path string) []byte {
	b.t.Helper()
	reply := b.send(http.MethodGet, path, nil, nil)
	defer reply.Body.Close()
	body, _ := io.ReadAll(reply.Body)
	if reply.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s: %d %s", path, reply.StatusCode, body)
	}
	return body
}

func (b *signedIn) json(reply *http.Response, out any) {
	b.t.Helper()
	defer reply.Body.Close()
	if err := json.NewDecoder(reply.Body).Decode(out); err != nil {
		b.t.Fatal(err)
	}
}

func TestDemoPublishesTheBookForTheDemoSignIn(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	if n := d.count(t, "SELECT count(*) FROM cards WHERE kind = 'go'"); n != 30 {
		t.Fatalf("%d cards", n)
	}

	b := signInToDemo(t, d.owner)
	var accounts struct {
		Accounts []struct{ Account, Name string } `json:"accounts"`
	}
	if err := json.Unmarshal(b.get("/api/v1/accounts"), &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts.Accounts) != 30 || accounts.Accounts[2].Account != "acc-003" ||
		accounts.Accounts[2].Name != "Thornfield Wren LLP" {
		t.Fatalf("accounts %+v", accounts)
	}
	var listed struct {
		Cards []struct {
			ID, Kind string
			BriefID  string `json:"brief_id"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(b.get("/api/v1/accounts/acc-003/cards"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Cards) != 1 || listed.Cards[0].Kind != "go" {
		t.Fatalf("acc-003's cards %+v", listed)
	}
	var card struct {
		Units []struct {
			ReceiptID       *string `json:"receipt_id"`
			RecordVersionID *string `json:"record_version_id"`
		} `json:"units"`
	}
	if err := json.Unmarshal(b.get("/api/v1/cards/"+listed.Cards[0].ID), &card); err != nil {
		t.Fatal(err)
	}
	if len(card.Units) == 0 || card.Units[0].ReceiptID == nil || card.Units[0].RecordVersionID == nil {
		t.Fatalf("units %+v", card.Units)
	}
	// The bytes the browser gets are the sealed card, which verifies against the stored receipts.
	payload := b.get("/api/v1/cards/" + listed.Cards[0].ID + "/bytes")
	admitted, err := cards.VerifyCard(ctx, payload, listed.Cards[0].BriefID,
		cards.StoreLog{Store: &postgres.ReceiptStore{Store: d.owner}, WorkspaceID: d.workspace.ID}, demoSources)
	if err != nil || admitted.AccountID != "acc-003" || !strings.Contains(string(payload), "Thornfield Wren LLP") {
		t.Fatalf("the served card: %v", err)
	}

	// Running it again publishes nothing; --again publishes a new card per account.
	if err := d.demo(t, d.appURL); err != nil || d.count(t, "SELECT count(*) FROM cards") != 30 {
		t.Fatalf("a second run: %v", err)
	}
	if err := d.demo(t, d.appURL, "--again"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.get("/api/v1/accounts/acc-003/cards"), &listed); err != nil || len(listed.Cards) != 2 {
		t.Fatalf("after --again: %+v %v", listed, err)
	}
}

// A grant someone revoked stays revoked through every run, and its account
// gets no card; a live grant keeps the time it was granted.
func TestDemoKeepsAGrantDecision(t *testing.T) {
	d := setUpDemo(t)
	principal, err := d.owner.DemoPrincipal(ctx, demoLogin)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"acc-001", "acc-003"} {
		if err := d.owner.EnsureAccount(ctx, d.workspace.ID, account, "Invented account"); err != nil {
			t.Fatal(err)
		}
		if err := d.owner.GrantAccount(ctx, d.workspace.ID, principal.ID, account); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	granted := func() string {
		conn, err := pgx.Connect(ctx, d.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var at string
		if err := conn.QueryRow(ctx, `SELECT granted_at::text FROM account_grants WHERE account_ref = 'acc-001'`).
			Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	before := granted()
	for _, args := range [][]string{nil, {"--again"}} {
		if err := d.demo(t, d.appURL, args...); err != nil {
			t.Fatal(err)
		}
	}
	if d.count(t, "SELECT count(*) FROM cards WHERE account_ref = 'acc-003'") != 0 ||
		d.count(t, "SELECT count(*) FROM account_grants WHERE account_ref = 'acc-003' AND revoked_at IS NULL") != 0 ||
		d.count(t, "SELECT count(*) FROM cards WHERE account_ref = 'acc-001'") != 2 || granted() != before {
		t.Fatal("a run changed a grant decision")
	}
	// GrantAccount itself renews only a revoked grant.
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, principal.ID, "acc-001"); err != nil || granted() != before {
		t.Fatalf("a live grant was granted again: %v", err)
	}
}

// demo runs as the app role, on a database setup has migrated and no
// restore has left half done, as serve does.
func TestDemoRunsOnlyAsTheAppRole(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.ownerURL); err == nil || !strings.Contains(err.Error(), "app role") {
		t.Fatalf("demo as the owner: %v", err)
	}
	conn, err := pgx.Connect(ctx, d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "INSERT INTO restores (archive_sha256) VALUES ($1)", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := d.demo(t, d.appURL); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("demo during a restore: %v", err)
	}
	if d.count(t, "SELECT count(*) FROM cards") != 0 {
		t.Fatal("a refused run published")
	}
}

func TestDemoNeedsSetupAndADemoWorkspace(t *testing.T) {
	store, err := postgres.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := makeDemoCards(ctx, store, demoLogin, false, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "setup") {
		t.Fatalf("without the demo principal: %v", err)
	}
	// A disabled demo principal is named as such: setup does not enable it.
	d := setUpDemo(t)
	if _, err := conn(t, d.ownerURL).Exec(ctx, "UPDATE principals SET disabled_at = now() WHERE login = $1",
		demoLogin); err != nil {
		t.Fatal(err)
	}
	if _, err := makeDemoCards(ctx, d.owner, demoLogin, false, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("a disabled demo principal: %v", err)
	}
	// The demo book never goes into a workspace that holds real sources.
	other, err := postgres.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	if err := other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := other.EnsureWorkspace(ctx, "demo", "live", "A live workspace"); err != nil {
		t.Fatal(err)
	}
	if run, err := makeDemoCards(ctx, other, demoLogin, false, io.Discard); err == nil || run.Published != 0 {
		t.Fatalf("the demo book went into a live workspace: %+v %v", run, err)
	}
}

func conn(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
