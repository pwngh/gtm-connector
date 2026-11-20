package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

const demoPassword = "demo-password-0123456789-abcdefghijklmn"

// setUpDemo is a migrated database with the demo workspace and the demo
// principal, as setup leaves it.
func setUpDemo(t *testing.T) (*postgres.Store, postgres.Workspace) {
	t.Helper()
	store, err := postgres.Open(ctx, pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(demoPassword))
	if _, err := store.EnsureDemoPrincipal(ctx, workspace, demoLogin, "Demo reviewer", digest[:]); err != nil {
		t.Fatal(err)
	}
	return store, workspace
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
	store, workspace := setUpDemo(t)
	var out bytes.Buffer
	made, err := makeDemoCards(ctx, store, demoLogin, &out)
	if err != nil {
		t.Fatal(err)
	}
	if made != 30 || strings.Count(out.String(), "\n") != 30 || !strings.Contains(out.String(), "acc-003  brief-") {
		t.Fatalf("made %d:\n%s", made, out.String())
	}

	b := signInToDemo(t, store)
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
		cards.StoreLog{Store: &postgres.ReceiptStore{Store: store}, WorkspaceID: workspace.ID}, demoSources)
	if err != nil || admitted.AccountID != "acc-003" || !strings.Contains(string(payload), "Thornfield Wren LLP") {
		t.Fatalf("the served card: %v", err)
	}

	// Each run adds a new card per account.
	if _, err := makeDemoCards(ctx, store, demoLogin, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.get("/api/v1/accounts/acc-003/cards"), &listed); err != nil || len(listed.Cards) != 2 {
		t.Fatalf("after a second run: %+v %v", listed, err)
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
	if _, err := makeDemoCards(ctx, store, demoLogin, io.Discard); err == nil || !strings.Contains(err.Error(), "setup") {
		t.Fatalf("without the demo principal: %v", err)
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
	if made, err := makeDemoCards(ctx, other, demoLogin, io.Discard); err == nil || made != 0 {
		t.Fatalf("the demo book went into a live workspace: %d %v", made, err)
	}
}
