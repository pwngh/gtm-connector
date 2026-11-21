package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/sources/slack"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// No test reaches Slack: a test that does not set its own invented Slack is refused every request.
func init() {
	slackTransport = refusingSlack{}
}

type refusingSlack struct{}

func (refusingSlack) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("this test reaches no Slack")
}

// The invented sign-in. No value here was ever issued by Slack.
const (
	inventedClient   = "123456789.987654321"
	inventedAccess   = "invented-private-access-token"
	inventedRefresh  = "invented-private-refresh-token"
	rotatedAccess    = "invented-replacement-access-token"
	rotatedRefresh   = "invented-replacement-refresh-token"
	inventedCode     = "invented-approval-code"
	inventedOrigin   = "https://invented-workspace.slack.com"
	inventedTeam     = "T12345678"
	inventedApp      = "A12345678"
	inventedUser     = "U12345678"
	recordedWallTime = "2027-01-16T00:00:00Z"
)

// inventedSlack is the Slack the commands of a test talk to: the token endpoint, auth.test, auth.revoke
// and conversations.replies, each checked as Slack would check it, with one thread to serve.
type inventedSlack struct {
	t  *testing.T
	mu sync.Mutex

	redirect  string
	challenge string // the PKCE challenge of the authorization page the browser was sent to
	valid     map[string]bool
	expiresIn int    // the lifetime of the access tokens it grants; 0 grants tokens that do not expire
	unrevoked bool   // auth.revoke does not confirm
	user      string // the user it grants; empty is inventedUser
	paths     []string
	thread    []any // the messages of the thread, oldest first
	channel   string
	threadTS  string
	page      int
	// before, when set, is called as each request arrives, before it is answered.
	before  func(path string)
	noGrant bool // the code exchange answers ok with no authed_user
}

func newInventedSlack(t *testing.T) *inventedSlack {
	s := &inventedSlack{t: t, valid: map[string]bool{}}
	previous := slackTransport
	slackTransport = s
	t.Cleanup(func() { slackTransport = previous })
	return s
}

func (s *inventedSlack) answer(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Length": {strconv.Itoa(len(body))}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func (s *inventedSlack) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.URL.Scheme != "https" || request.URL.Host != "slack.com" {
		s.t.Errorf("a request to %s", request.URL.Host)
		return nil, errors.New("not Slack")
	}
	s.paths = append(s.paths, request.URL.Path)
	if s.before != nil {
		s.before(request.URL.Path)
	}
	bearer := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	switch request.URL.Path {
	case "/api/oauth.v2.access":
		raw, _ := io.ReadAll(request.Body)
		form, err := url.ParseQuery(string(raw))
		if err != nil || request.Method != http.MethodPost || bearer != "" || form.Get("client_id") != inventedClient {
			s.t.Errorf("token request %s %v", request.Method, err)
			return s.answer(request, 400, `{"ok":false,"error":"invalid_request"}`), nil
		}
		var access, refresh string
		switch form.Get("grant_type") {
		case "authorization_code":
			// PKCE: the verifier sent now is the one whose digest the authorization page carried.
			sum := sha256.Sum256([]byte(form.Get("code_verifier")))
			if form.Get("code") != inventedCode || form.Get("redirect_uri") != s.redirect || s.challenge == "" ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != s.challenge {
				return s.answer(request, 200, `{"ok":false,"error":"invalid_code_verifier"}`), nil
			}
			access, refresh = inventedAccess, inventedRefresh
		case "refresh_token":
			if form.Get("refresh_token") != inventedRefresh {
				return s.answer(request, 200, `{"ok":false,"error":"invalid_refresh_token"}`), nil
			}
			access, refresh = rotatedAccess, rotatedRefresh
		}
		s.valid[access] = true
		user := map[string]any{"id": s.userID(), "scope": "channels:history", "access_token": access, "token_type": "user"}
		if s.expiresIn > 0 {
			user["refresh_token"], user["expires_in"] = refresh, s.expiresIn
		}
		body := map[string]any{"ok": true, "app_id": inventedApp, "authed_user": user, "team": map[string]any{"id": inventedTeam},
			"enterprise": nil, "is_enterprise_install": false}
		if form.Get("grant_type") == "refresh_token" {
			body = user
			body["ok"] = true
		} else if s.noGrant {
			delete(body, "authed_user")
		}
		encoded, _ := json.Marshal(body)
		return s.answer(request, 200, string(encoded)), nil
	case "/api/auth.test":
		if !s.valid[bearer] {
			return s.answer(request, 200, `{"ok":false,"error":"invalid_auth"}`), nil
		}
		return s.answer(request, 200, `{"ok":true,"url":"`+inventedOrigin+`/","team":"Invented","user":"invented",`+
			`"team_id":"`+inventedTeam+`","user_id":"`+s.userID()+`"}`), nil
	case "/api/auth.revoke":
		if !s.valid[bearer] || request.Method != http.MethodGet {
			return s.answer(request, 200, `{"ok":false,"error":"invalid_auth"}`), nil
		}
		if s.unrevoked {
			return s.answer(request, 200, `{"ok":true,"revoked":false}`), nil
		}
		delete(s.valid, bearer)
		return s.answer(request, 200, `{"ok":true,"revoked":true}`), nil
	case "/api/conversations.replies":
		if !s.valid[bearer] {
			return s.answer(request, 200, `{"ok":false,"error":"invalid_auth"}`), nil
		}
		query := request.URL.Query()
		if query.Get("ts") != s.threadTS || query.Get("channel") != s.channel {
			return s.answer(request, 200, `{"ok":false,"error":"thread_not_found"}`), nil
		}
		start := 0
		if cursor := query.Get("cursor"); cursor != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(cursor, "page-"))
		}
		end := min(start+s.page, len(s.thread))
		cursor := ""
		if end < len(s.thread) {
			cursor = fmt.Sprintf("page-%d", end)
		}
		body, err := strictjson.Dumps(strictjson.NewObject("ok", true, "messages", s.thread[start:end],
			"has_more", cursor != "", "response_metadata", strictjson.NewObject("next_cursor", cursor)),
			strictjson.DumpOptions{Compact: true})
		if err != nil {
			s.t.Fatal(err)
		}
		return s.answer(request, 200, body), nil
	}
	s.t.Errorf("a request of %s", request.URL.Path)
	return s.answer(request, 404, `{"ok":false}`), nil
}

func (s *inventedSlack) userID() string {
	if s.user != "" {
		return s.user
	}
	return inventedUser
}

// serve makes the invented Slack serve one thread of a channel, in pages of the given size.
func (s *inventedSlack) serve(channel string, rows []any, page int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channel, s.thread, s.page = channel, rows, page
	s.threadTS, _ = get(rows[0].(*strictjson.Object), "ts").(string)
}

// slackSetup is the install of a Slack test: an empty credentials folder, a database with the demo set up,
// and a wall clock that stands at the time of the recorded reads until the test moves it.
func slackSetup(t *testing.T) (*demoDatabase, string, *time.Time) {
	t.Helper()
	d := setUpDemo(t)
	folder := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GTM_CREDENTIALS_DIR", folder)
	wall, _ := time.Parse(time.RFC3339, recordedWallTime)
	previous := slackClock
	slackClock = slack.Clock{Now: func() time.Time { return wall }}
	t.Cleanup(func() { slackClock = previous })
	return d, folder, &wall
}

// run runs one command line as dispatch does, with the app role's database.
func (d *demoDatabase) line(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	var out, errs bytes.Buffer
	code = dispatch(ctx, argv, screen{out: &out, err: &errs})
	return code, out.String(), errs.String()
}

// recordedFolders are the books of recorded cards' folders, by workspace slug. A test that registers them
// fills the salesforce and calls entries of sourceKinds with the demo book and a card's rows, read as the
// demo CRM and demo calls, as the recorded folder read them and as its card records them. Slack is then the
// only source of the card read live.
type recordedFolders map[string]*demo.Store

func registerRecordedFolders(t *testing.T) recordedFolders {
	t.Helper()
	books := recordedFolders{}
	crm, calls := kindsNamed("salesforce")[0], kindsNamed("calls")[0]
	savedCRM, savedCalls := *crm, *calls
	t.Cleanup(func() { *crm, *calls = savedCRM, savedCalls })
	none := func(context.Context, attaching) (attached, error) { return attached{}, nil }
	crm.backend, crm.described, crm.attach = "sqlite", demoData, none
	crm.bound = func(postgres.Connection, any) any { return evidence.FixtureCRMBinding() }
	crm.crm = func(_ context.Context, o opening) (crmSource, error) { return demoBook{books[o.workspace.Slug]}, nil }
	calls.backend, calls.described, calls.attach, calls.bound = "fixture", demoData, none, nil
	calls.calls = func(_ context.Context, o opening) (callsSource, error) {
		return demoBook{books[o.workspace.Slug]}, nil
	}
	return books
}

// recordedCard is one card of the books_slack or slack_threads family: its folder's rows, the thread Slack
// served it, and the card.
type recordedCard struct {
	key, account string
	rows         *strictjson.Object // the rows the folder's demo databases held besides the demo book
	tables       []string           // the databases they were inserted into
	thread       []any              // nil for an account with no mapped thread
	page         int
	card         *strictjson.Object
}

func recordedCards(t *testing.T) []recordedCard {
	t.Helper()
	var cards []recordedCard
	for _, family := range []string{"books_slack", "slack_threads"} {
		for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
			value, err := strictjson.Loads(line)
			if err != nil {
				t.Fatal(err)
			}
			row := value.(*strictjson.Object)
			input := get(row, "input").(*strictjson.Object)
			card, err := strictjson.Loads([]byte(get(row, "card").(string)))
			if err != nil {
				t.Fatal(err)
			}
			c := recordedCard{key: family + " " + get(row, "key").(string), account: get(row, "account_id").(string),
				card: card.(*strictjson.Object)}
			if family == "books_slack" {
				c.rows, c.tables = get(input, "rows").(*strictjson.Object), []string{"crm", "calls"}
				c.thread, _ = get(input, "thread").([]any)
			} else {
				c.rows, c.tables = strictjson.NewObject("crm", get(input, "crm")), []string{"crm"}
				c.thread, _ = get(input, "rows").([]any)
			}
			if c.thread != nil {
				c.page, _ = strconv.Atoi(cellText(get(input, "page")))
			}
			cards = append(cards, c)
		}
	}
	return cards
}

// book is the demo book with the card's rows.
func (c recordedCard) book(t *testing.T) *demo.Store {
	t.Helper()
	store, err := demo.Load(oracletest.Read(t, "cards/demo_book.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range c.tables {
		list, _ := get(c.rows, database).([]any)
		for _, entry := range list {
			pair := entry.([]any)
			if err := store.Insert(database, pair[0].(string), pair[1].([]any)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return store
}

// snapshot is the recorded card's thread snapshot.
func (c recordedCard) snapshot() *strictjson.Object {
	snapshot, _ := get(get(c.card, "evidence").(*strictjson.Object), "thread_snapshot").(*strictjson.Object)
	return snapshot
}

// mapped is a member of the recorded snapshot's mapping.
func (c recordedCard) mapped(key string) string {
	return get(get(c.snapshot(), "mapping").(*strictjson.Object), key).(string)
}

// withoutReadTimes is a card's text without its second line, which gives the times its sources were read.
func withoutReadTimes(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && strings.HasPrefix(lines[1], "Sources read ") {
		lines = append(lines[:1], lines[2:]...)
	}
	return strings.Join(lines, "\n")
}

// seedSignIn saves a sign-in for a workspace, obtained at the given time, as debug slack connect leaves it
// when state is "ready".
func seedSignIn(t *testing.T, folder, slug string, at time.Time, state string) {
	t.Helper()
	signIn := &slack.Credentials{ClientID: inventedClient, RedirectURI: "http://localhost:8765/slack/callback",
		Identity:        slack.Identity{AppID: inventedApp, TeamID: inventedTeam, UserID: inventedUser},
		WorkspaceOrigin: inventedOrigin, RequestedScopes: []string{"channels:history"},
		GrantedScopes: []string{"channels:history"}, State: state, AccessToken: slack.NewSecret(inventedAccess),
		AccessObtainedAt: at}
	if state == "ready" {
		signIn.LastVerifiedAt = at
	}
	err := slackSignIn{dir: folder, slug: slug}.Hold(ctx, func(saved slack.Saved) error { return saved.Save(ctx, signIn) })
	if err != nil {
		t.Fatal(err)
	}
}

// cardResult is a card as brief --json gives it.
type cardResult struct {
	BriefID string `json:"brief_id"`
	Text    string
}

// connectRecorded connects a workspace to a recorded card's folder: its CRM and calls, then the saved
// sign-in.
func connectRecorded(t *testing.T, d *demoDatabase, books recordedFolders, c recordedCard, name string) {
	t.Helper()
	books[name] = c.book(t)
	for _, connect := range [][]string{{"connect", "salesforce"}, {"connect", "calls"}, {"connect", "slack"}} {
		if code, _, stderr := d.line(t, append(connect, "--state-dir", name)...); code != 0 {
			t.Fatalf("%s: %v: exit %d\n%s", c.key, connect, code, stderr)
		}
	}
}

// Every recorded card that read a live Slack thread is made again through the commands: its folder's CRM
// and calls are registered as the recorded folder read them, the sign-in is saved, connect slack connects
// it, debug slack map maps the recorded thread, and brief reads that thread from the invented Slack. The
// card's text is the recorded card's, byte for byte, but for the line that gives when its sources were read;
// and the snapshot it saved holds the recorded thread's counts, cutoff and message digests. An account with
// no mapped thread gets the recorded card that says so.
func TestBriefReadsAMappedThreadAsTheRecordedCard(t *testing.T) {
	d, folder, wall := slackSetup(t)
	slackAPI := newInventedSlack(t)
	slackAPI.valid[inventedAccess] = true
	books := registerRecordedFolders(t)
	matched, unmapped := 0, 0
	for i, c := range recordedCards(t) {
		name := fmt.Sprintf("thread-%d", i)
		seedSignIn(t, folder, name, *wall, "ready")
		connectRecorded(t, d, books, c, name)
		if c.thread != nil {
			slackAPI.serve(c.mapped("channel_id"), c.thread, c.page)
			code, _, stderr := d.line(t, "debug", "slack", "map", "--account", c.account, "--channel",
				c.mapped("channel_id"), "--thread-ts", c.mapped("thread_ts"), "--state-dir", name, "--json")
			if code != 0 {
				t.Fatalf("%s: map: exit %d\n%s", c.key, code, stderr)
			}
		} else {
			unmapped++
		}
		code, stdout, stderr := d.line(t, "brief", c.account, "--as-of", "2027-01-15", "--state-dir", name, "--json")
		var made cardResult
		if err := json.Unmarshal([]byte(stdout), &made); code != 0 || err != nil {
			t.Fatalf("%s: brief: exit %d\n%s", c.key, code, stderr)
		}
		if got, want := withoutReadTimes(made.Text), withoutReadTimes(get(c.card, "text").(string)); got != want {
			t.Errorf("%s: the card\n%s\nis not the recorded card\n%s", c.key, got, want)
			continue
		}
		if c.thread != nil {
			workspace, err := d.owner.Workspace(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := d.owner.CardByBrief(ctx, workspace.ID, made.BriefID)
			if err != nil {
				t.Fatal(err)
			}
			value, err := strictjson.Loads(saved.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := get(get(value.(*strictjson.Object), "evidence").(*strictjson.Object), "thread_snapshot").(*strictjson.Object)
			for _, key := range []string{"manifest_sha256", "counts", "cutoff_ts"} {
				if !sameValue(get(snapshot, key), get(c.snapshot(), key)) {
					t.Errorf("%s: the snapshot's %s differs from the recorded read's", c.key, key)
				}
			}
		}
		matched++
	}
	if matched != 114 || unmapped != 10 {
		t.Errorf("%d cards matched, %d of them for an account with no thread; the oracle holds 114 and 10", matched, unmapped)
	}
}

// freeRedirect is the registered redirect of a test: a loopback port no one listens on.
func freeRedirect(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return fmt.Sprintf("http://localhost:%d/slack/callback", port)
}

// browser is the system browser of a test. Sent to the sign-in's start page, it follows it to Slack's
// authorization page, checks that page asks for a PKCE grant of the invented app, tells the invented Slack
// the challenge, and then goes to the redirect as Slack would send it: first with each request in wrong,
// which the callback answers and ignores, then with the approval, or with the refusal given.
type browser struct {
	t       *testing.T
	slack   *inventedSlack
	refusal string // the error Slack's redirect carries instead of a code, when set
	strays  []string
	answers chan string
}

func useBrowser(t *testing.T, s *inventedSlack, refusal string, strays ...string) *browser {
	b := &browser{t: t, slack: s, refusal: refusal, strays: strays, answers: make(chan string, 16)}
	previous := openBrowser
	openBrowser = b.open
	t.Cleanup(func() { openBrowser = previous })
	answerTerminal(t, "yes")
	return b
}

// answerTerminal answers each question the command asks at its terminal with answer, and gives the
// questions asked.
func answerTerminal(t *testing.T, answer string) *[]string {
	var asked []string
	previous := askTerminal
	askTerminal = func(_ context.Context, question string) (string, error) {
		asked = append(asked, question)
		return answer, nil
	}
	t.Cleanup(func() { askTerminal = previous })
	return &asked
}

// noRedirects is a client that shows a redirect rather than following it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// browserClient is a browser's own client: it keeps cookies in a jar, which sends each only to the paths it
// was set for, and shows a redirect rather than following it.
func browserClient(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: noRedirects.CheckRedirect}
}

// visit asks for a page with the given headers, with no cookie jar, and gives the status, the Location and
// the body.
func visit(target string, header map[string]string) (int, string, string) {
	return visitWith(noRedirects, target, header)
}

// visitWith is visit through a client of its own.
func visitWith(client *http.Client, target string, header map[string]string) (int, string, string) {
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, "", err.Error()
	}
	for name, value := range header {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err.Error()
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Location"), string(body)
}

// follow opens the sign-in's start page through a browser's client: it must redirect once to Slack's
// authorization page for the invented app, with a PKCE challenge the invented Slack then expects, set the
// cookie, named for the sign-in, that binds the callback to this browser and that the jar keeps for the
// callback's path only, and then be gone. It gives the page's state.
func (s *inventedSlack) follow(t *testing.T, client *http.Client, start string) (state string) {
	if !strings.HasPrefix(start, strings.TrimSuffix(s.redirect, "/slack/callback")+"/start/") {
		t.Errorf("the browser was sent to %q", start)
	}
	request, err := http.NewRequest(http.MethodGet, start, nil)
	if err != nil {
		t.Errorf("the start page %v", err)
		return ""
	}
	request.Header.Set("Sec-Fetch-Site", "none")
	response, err := client.Do(request)
	if err != nil {
		t.Errorf("the start page %v", err)
		return ""
	}
	response.Body.Close()
	status, page := response.StatusCode, response.Header.Get("Location")
	callback, _ := url.Parse(s.redirect)
	set := response.Cookies()
	if len(set) != 1 || !strings.HasPrefix(set[0].Name, "gtm_slack_sign_in_"+callback.Port()+"_") ||
		set[0].Path != "/slack/callback" || !set[0].HttpOnly || set[0].SameSite != http.SameSiteLaxMode ||
		set[0].MaxAge <= 0 || len(set[0].Value) < 43 {
		t.Errorf("the start page set %d cookies", len(set))
		return ""
	}
	startURL, _ := url.Parse(start)
	if kept := client.Jar.Cookies(callback); len(kept) != 1 || kept[0].Name != set[0].Name || len(client.Jar.Cookies(startURL)) != 0 {
		t.Errorf("the jar keeps %d cookies for the callback and %d for the start page", len(kept), len(client.Jar.Cookies(startURL)))
	}
	parsed, err := url.Parse(page)
	if status != 302 || err != nil {
		t.Errorf("the start page answered %d", status)
		return ""
	}
	query := parsed.Query()
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "https://slack.com/oauth/v2/authorize" ||
		query.Get("client_id") != inventedClient || query.Get("scope") != "" ||
		query.Get("user_scope") != "channels:history" || query.Get("redirect_uri") != s.redirect ||
		query.Get("code_challenge_method") != "S256" || len(query.Get("code_challenge")) != 43 ||
		len(query.Get("state")) < 32 || strings.Contains(page, "code_verifier") {
		t.Errorf("the authorization page %s", parsed.Path)
	}
	if again, _, _ := visitWith(client, start, nil); again != 404 {
		t.Errorf("the start page answered %d a second time", again)
	}
	s.mu.Lock()
	s.challenge = query.Get("code_challenge")
	s.mu.Unlock()
	return query.Get("state")
}

func (b *browser) open(_ context.Context, start string) error {
	client := browserClient(b.t)
	go func() {
		state := b.slack.follow(b.t, client, start)
		get := func(target string) string {
			status, _, body := visitWith(client, b.slack.redirect+target, nil)
			return fmt.Sprintf("%d %s", status, body)
		}
		for _, stray := range b.strays {
			b.answers <- get(strings.ReplaceAll(stray, "STATE", url.QueryEscape(state)))
		}
		if b.refusal != "" {
			b.answers <- get("?error=" + b.refusal + "&state=" + url.QueryEscape(state))
		} else {
			b.answers <- get("?code=" + inventedCode + "&state=" + url.QueryEscape(state))
		}
		close(b.answers)
	}()
	return nil
}

// seen gathers every byte a test's commands printed, so that it can be searched for a credential.
type seen struct{ bytes.Buffer }

func (s *seen) line(t *testing.T, d *demoDatabase, argv ...string) (int, string, string) {
	t.Helper()
	code, stdout, stderr := d.line(t, argv...)
	s.WriteString(stdout + stderr)
	return code, stdout, stderr
}

// The tokens of the invented sign-in and of the pair its refresh gives.
var inventedTokens = []string{inventedAccess, inventedRefresh, rotatedAccess, rotatedRefresh}

// From setup to a card that quotes a Slack thread, through the binary's commands against an invented Slack:
// debug slack connect signs in with PKCE through the browser and the loopback callback, status shows and
// checks the sign-in, connect slack connects it, debug slack map maps a thread by its link after an
// audited CRM read, debug slack check reads it under a receipt, brief makes the card, and disconnect
// revokes the token and removes the sign-in. No token reaches any output, receipt or row.
func TestSlackFromSignInToACard(t *testing.T) {
	d, folder, wall := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect, api.expiresIn = freeRedirect(t), 12*3600
	books := registerRecordedFolders(t)
	var printed seen
	where := []string{"--state-dir", "acme"}

	// Sign in. A request that is not the callback for this sign-in is answered and ignored.
	b := useBrowser(t, api, "", "", "?code=x&state=wrong", "?code=x&state=STATE&error=also")
	code, stdout, stderr := printed.line(t, d, append([]string{"debug", "slack", "connect", "--client-id", inventedClient,
		"--redirect-uri", api.redirect, "--json"}, where...)...)
	var answers []string
	for answer := range b.answers {
		answers = append(answers, answer)
	}
	var status struct {
		Configured      bool
		State           string `json:"credential_state"`
		Verification    string
		Rotation        *bool
		NeedsLogin      bool    `json:"needs_login"`
		LastVerifiedAt  *string `json:"last_verified_at"`
		AccessExpiresAt *string `json:"access_expires_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); code != 0 || err != nil || !status.Configured ||
		status.State != "ready" || status.Verification != "live" || status.Rotation == nil || !*status.Rotation ||
		status.NeedsLogin || *status.LastVerifiedAt != recordedWallTime || *status.AccessExpiresAt != "2027-01-16T12:00:00Z" {
		t.Fatalf("connect: exit %d\n%s%s", code, stdout, stderr)
	}
	if len(answers) != 4 || !strings.HasPrefix(answers[0], "400 ") || !strings.HasPrefix(answers[1], "400 ") ||
		!strings.HasPrefix(answers[2], "400 ") || !strings.Contains(answers[3], "Slack authentication is saved.") {
		t.Errorf("the browser was answered %q", answers)
	}
	if got := strings.Join(api.paths, " "); got != "/api/oauth.v2.access /api/auth.test" {
		t.Errorf("connect asked Slack for %s", got)
	}
	info, err := os.Lstat(filepath.Join(folder, "slack-acme.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the saved sign-in: %v %v", info, err)
	}

	// Status reads the file; --check asks Slack once, and a token near its expiry is refreshed once.
	api.paths = nil
	code, stdout, _ = printed.line(t, d, append([]string{"debug", "slack", "status"}, where...)...)
	if code != 0 || !strings.HasPrefix(stdout, "Saved Slack authentication is available. This command did not contact Slack.\n") ||
		len(api.paths) != 0 {
		t.Errorf("status: exit %d\n%s", code, stdout)
	}
	code, stdout, _ = printed.line(t, d, append([]string{"debug", "slack", "status", "--check"}, where...)...)
	if code != 0 || !strings.HasPrefix(stdout, "Slack authentication is verified. No messages were read.\n") ||
		strings.Join(api.paths, " ") != "/api/auth.test" {
		t.Errorf("status --check: exit %d, asked %v\n%s", code, api.paths, stdout)
	}

	// The CRM and calls of a recorded card's folder, then Slack.
	var card recordedCard
	for _, c := range recordedCards(t) {
		if c.key == "slack_threads gemini/2" {
			card = c
		}
	}
	connectRecorded(t, d, books, card, "acme")
	code, stdout, _ = printed.line(t, d, append([]string{"connect"}, where...)...)
	if code != 0 || !strings.Contains(stdout, "slack\tconnected\tthe saved Slack sign-in\n") {
		t.Errorf("connect status: exit %d\n%s", code, stdout)
	}

	// Map the thread by the link Slack copies for a reply, which names its parent.
	api.serve(card.mapped("channel_id"), card.thread, card.page)
	parent := card.mapped("thread_ts")
	reply := get(card.thread[1].(*strictjson.Object), "ts").(string)
	link := inventedOrigin + "/archives/" + card.mapped("channel_id") + "/p" + strings.Replace(reply, ".", "", 1) +
		"?thread_ts=" + parent + "&cid=" + card.mapped("channel_id")
	code, stdout, stderr = printed.line(t, d, append([]string{"debug", "slack", "map", "--account", card.account,
		"--thread-url", link}, where...)...)
	if code != 0 || stdout != "Slack thread selection saved. Thread access has not been checked.\n"+
		"Next: gtm-connector debug slack check --help\n" || !strings.Contains(stderr, "Sources: CRM is demo data") {
		t.Fatalf("map: exit %d\n%s%s", code, stdout, stderr)
	}
	var mapped struct{ Status string }
	if code, stdout, _ = printed.line(t, d, append([]string{"debug", "slack", "map", "--account", card.account,
		"--channel", card.mapped("channel_id"), "--thread-ts", parent, "--json"}, where...)...); code != 0 ||
		json.Unmarshal([]byte(stdout), &mapped) != nil || mapped.Status != "unchanged" {
		t.Errorf("the same thread again: exit %d\n%s", code, stdout)
	}

	// Check the read, past the access token's expiry: the read refreshes it once.
	*wall = wall.Add(12 * time.Hour)
	api.paths = nil
	code, stdout, stderr = printed.line(t, d, append([]string{"debug", "slack", "check", "--account", card.account,
		"--json"}, where...)...)
	var check struct {
		Status   string
		APICalls int `json:"api_calls"`
		Counts   map[string]int
		Steps    []struct{ Name, Status, Code string }
	}
	if err := json.Unmarshal([]byte(stdout), &check); code != 0 || err != nil || check.Status != "passed" ||
		check.Counts["messages"] != len(card.thread) || check.Counts["refresh_attempts"] != 1 || len(check.Steps) != 5 {
		t.Fatalf("check: exit %d\n%s%s", code, stdout, stderr)
	}
	if got := strings.Join(api.paths, " "); !strings.HasPrefix(got, "/api/oauth.v2.access /api/auth.test /api/conversations.replies") {
		t.Errorf("check asked Slack for %s", got)
	}

	// The card is the recorded card.
	code, stdout, stderr = printed.line(t, d, append([]string{"brief", card.account, "--as-of", "2027-01-15", "--json"}, where...)...)
	var made cardResult
	if err := json.Unmarshal([]byte(stdout), &made); code != 0 || err != nil ||
		withoutReadTimes(made.Text) != withoutReadTimes(get(card.card, "text").(string)) {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	for _, argv := range [][]string{{"audit"}, {"audit", "--json"}, {"inspect", made.BriefID}, {"inspect", made.BriefID, "--json"},
		{"connect", "--json"}, {"debug", "slack", "status", "--json"}} {
		if code, _, stderr := printed.line(t, d, append(argv, where...)...); code != 0 {
			t.Errorf("%v: exit %d\n%s", argv, code, stderr)
		}
	}

	// Disconnect asks Slack to revoke the token, then removes the sign-in. A card can no longer read Slack.
	code, stdout, _ = printed.line(t, d, append([]string{"debug", "slack", "disconnect", "--revoke"}, where...)...)
	if code != 0 || stdout != "Local Slack credentials were removed.\nSlack confirmed revocation of this access token.\n"+
		"App removal and other credentials are managed separately in Slack.\n" {
		t.Errorf("disconnect --revoke: exit %d\n%s", code, stdout)
	}
	if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sign-in is still saved: %v", err)
	}
	code, _, stderr = printed.line(t, d, append([]string{"brief", card.account, "--as-of", "2027-01-15"}, where...)...)
	if code != 3 || !strings.Contains(stderr, "Error: slack_not_connected: ") {
		t.Errorf("brief after disconnect: exit %d\n%s", code, stderr)
	}

	// Rule seven: no token reached what any command printed, or any row of the database.
	owner := conn(t, d.ownerURL)
	var tables []string
	if err := owner.QueryRow(ctx, `SELECT array_agg(tablename::text) FROM pg_tables
		WHERE schemaname = current_schema()`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	rows := ""
	for _, table := range tables {
		var text string
		if err := owner.QueryRow(ctx, `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&text); err != nil {
			t.Fatal(err)
		}
		rows += text
	}
	if !strings.Contains(rows, card.account) {
		t.Fatal("the database dump holds none of the workspace's rows")
	}
	for _, token := range append(inventedTokens, inventedCode) {
		if strings.Contains(printed.String(), token) || strings.Contains(rows, token) {
			t.Errorf("a credential reached the output or the database")
		}
	}
}

// slackRefused runs a command that must fail with this exit code, and gives its message.
func slackRefused(t *testing.T, d *demoDatabase, code int, argv ...string) string {
	t.Helper()
	got, stdout, stderr := d.line(t, append(argv, "--json")...)
	var refused struct {
		Error struct{ Message, Next string }
	}
	last := stderr[strings.LastIndex(strings.TrimSuffix(stderr, "\n"), "\n")+1:]
	if err := json.Unmarshal([]byte(last), &refused); err != nil || got != code {
		t.Fatalf("%v: exit %d, not %d\n%s%s", argv, got, code, stdout, stderr)
	}
	return refused.Error.Message
}

// What the Slack commands refuse, with the exit codes docs/sources.md lists. A workspace that reads
// demo Slack never reads live Slack, and nothing is saved by a refusal.
func TestSlackCommandsRefuse(t *testing.T) {
	d, folder, wall := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect = freeRedirect(t)
	books := registerRecordedFolders(t)
	cards := recordedCards(t)

	// The demo workspace reads demo Slack. Live Slack never joins it.
	d.connectDemo(t, "demo")
	seedSignIn(t, folder, "demo", *wall, "ready")
	if message := slackRefused(t, d, 3, "connect", "slack"); !strings.Contains(message, "holds demo data") {
		t.Errorf("connect slack beside demo data: %s", message)
	}
	if message := slackRefused(t, d, 2, "debug", "slack", "map", "--account", "acc-003", "--channel", "C0123ABCD",
		"--thread-ts", "1799971200.000000"); !strings.HasPrefix(message, "Slack is not connected.") {
		t.Errorf("map in the demo workspace: %s", message)
	}
	if message := slackRefused(t, d, 2, "connect", "slack", "--state-dir", "nothing-yet"); !strings.HasPrefix(message, "No CRM is connected.") {
		t.Errorf("connect slack with no CRM: %s", message)
	}

	// A workspace with a CRM, and no sign-in, one that is unchecked, then one that is ready.
	books["acme"] = cards[0].book(t)
	for _, connect := range [][]string{{"connect", "salesforce"}, {"connect", "calls"}} {
		if code, _, stderr := d.line(t, append(connect, "--state-dir", "acme")...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", connect, code, stderr)
		}
	}
	if message := slackRefused(t, d, 2, "connect", "slack", "--state-dir", "acme"); !strings.HasPrefix(message,
		"No Slack sign-in is saved for this workspace. Run gtm-connector debug slack connect --state-dir acme") {
		t.Errorf("connect slack with no sign-in: %s", message)
	}
	seedSignIn(t, folder, "acme", *wall, "identity_unchecked")
	if message := slackRefused(t, d, 3, "connect", "slack", "--state-dir", "acme"); !strings.HasPrefix(message, "slack_identity_unchecked: ") {
		t.Errorf("connect slack unchecked: %s", message)
	}
	seedSignIn(t, folder, "acme", *wall, "refresh_pending")
	if message := slackRefused(t, d, 3, "connect", "slack", "--state-dir", "acme"); !strings.HasPrefix(message, "slack_reconnect_required: ") {
		t.Errorf("connect slack with a sign-in that needs a login: %s", message)
	}
	seedSignIn(t, folder, "acme", *wall, "ready")
	// connect slack --remove detaches Slack and keeps the sign-in. None of it asks Slack.
	for _, argv := range [][]string{{"connect", "slack"}, {"connect", "slack", "--remove"}, {"connect", "slack"}} {
		if code, _, stderr := d.line(t, append(argv, "--state-dir", "acme")...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", argv, code, stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); err != nil || len(api.paths) != 0 {
		t.Errorf("after connect slack --remove the sign-in is %v, and Slack was asked for %v", err, api.paths)
	}

	// Selections.
	for _, argv := range [][]string{
		{"--account", "acc-3", "--channel", "C0123ABCD", "--thread-ts", "1799971200.000000"},
		{"--account", "acc-400", "--channel", "X0123ABCD", "--thread-ts", "1799971200.000000"},
		{"--account", "acc-400", "--channel", "C0123ABCD", "--thread-ts", "1799971200.1"},
		{"--account", "acc-400", "--channel", "C0123ABCD"},
		{"--account", "acc-400", "--thread-url", "https://other-workspace.slack.com/archives/C0123ABCD/p1799971200000000"},
		{"--account", "acc-400", "--thread-url", inventedOrigin + "/archives/C0123ABCD/p1799971200000000#frag"},
		{"--account", "acc-400", "--thread-url", inventedOrigin + "/archives/C0123ABCD/p1799971200000000?cid=C9999"},
		{"--account", "acc-400", "--thread-url", inventedOrigin + "/archives/C0123ABCD/p1799971200000000?thread_ts=1799971300.000000"},
		{"--account", "acc-400", "--thread-url", "http://invented-workspace.slack.com/archives/C0123ABCD/p1799971200000000"},
		{"--account-number", "INVENTED-1", "--channel", "C0123ABCD", "--thread-ts", "1799971200.000000"},
	} {
		argv = append([]string{"debug", "slack", "map", "--state-dir", "acme"}, argv...)
		if message := slackRefused(t, d, 2, argv...); !strings.HasPrefix(message, "slack_selection_invalid: ") {
			t.Errorf("%v: %s", argv[5:], message)
		}
	}
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"map", "--channel", "C0123ABCD"}, "one of the arguments --account --account-number is required"},
		{[]string{"map", "--account", "acc-400"}, "one of the arguments --thread-url --channel is required"},
		{[]string{"map", "--account", "acc-400", "--account-number", "1", "--channel", "C1"},
			"argument --account-number: not allowed with argument --account"},
		{[]string{"connect", "--client-id", inventedClient}, "the following arguments are required: --redirect-uri"},
		{[]string{"check"}, "the following arguments are required: --account"},
		{[]string{"connect", "--client-id", inventedClient, "--redirect-uri", api.redirect, "--user-scope", "chat:write"},
			"argument --user-scope: invalid choice (choose from 'channels:history', 'groups:history')"},
		{[]string{"unknown"}, "argument ACTION: invalid choice (choose from 'connect', 'status', 'disconnect', 'map', 'check')"},
	} {
		if message := slackRefused(t, d, 2, append([]string{"debug", "slack"}, c.argv...)...); message != c.want {
			t.Errorf("%v: %s", c.argv, message)
		}
	}
	for _, argv := range [][]string{
		{"--client-id", "not-a-client", "--redirect-uri", api.redirect},
		{"--client-id", inventedClient, "--redirect-uri", "http://127.0.0.1:8765/slack/callback"},
		{"--client-id", inventedClient, "--redirect-uri", "http://localhost:80/slack/callback"},
		{"--client-id", inventedClient, "--redirect-uri", api.redirect, "--team-id", "invented"},
		{"--client-id", inventedClient, "--redirect-uri", api.redirect, "--user-scope", "channels:history",
			"--user-scope", "channels:history"},
	} {
		if message := slackRefused(t, d, 2, append([]string{"debug", "slack", "connect", "--state-dir", "acme"}, argv...)...); !strings.HasPrefix(message, "slack_invalid_configuration: ") {
			t.Errorf("connect %v: %s", argv, message)
		}
	}

	// An account the CRM does not hold, and a thread mapped to another account.
	if message := slackRefused(t, d, 2, "debug", "slack", "map", "--state-dir", "acme", "--account", "acc-999",
		"--channel", "C0123ABCD", "--thread-ts", "1799971200.000000"); !strings.HasPrefix(message, "slack_account_not_found: ") {
		t.Errorf("map of an account the CRM does not hold: %s", message)
	}
	if code, _, stderr := d.line(t, "debug", "slack", "map", "--state-dir", "acme", "--account", cards[0].account,
		"--channel", "C0123ABCD", "--thread-ts", "1799971200.000000"); code != 0 {
		t.Fatalf("map: exit %d\n%s", code, stderr)
	}
	if message := slackRefused(t, d, 3, "debug", "slack", "map", "--state-dir", "acme", "--account", cards[0].account,
		"--channel", "C0123ABCD", "--thread-ts", "1799971200.000001"); !strings.HasPrefix(message, "slack_mapping_conflict: ") {
		t.Errorf("another thread without --replace: %s", message)
	}

	// A check of an account with no thread fails its steps, and says what to run next.
	code, stdout, _ := d.line(t, "debug", "slack", "check", "--state-dir", "acme", "--account", "acc-001", "--json")
	var check struct {
		Status string
		Next   string
		Steps  []struct{ Name, Status, Code string }
	}
	if err := json.Unmarshal([]byte(stdout), &check); code != 4 || err != nil || check.Status != "failed" ||
		check.Steps[1].Code != "slack_mapping_missing" || check.Next != "gtm-connector debug slack map --help" {
		t.Errorf("check of an unmapped account: exit %d\n%s", code, stdout)
	}

	// Another command that holds the sign-in's lock makes this one wait for nothing.
	err := slackSignIn{dir: folder, slug: "acme"}.Hold(ctx, func(slack.Saved) error {
		if message := slackRefused(t, d, 1, "debug", "slack", "status", "--state-dir", "acme"); !strings.HasPrefix(message,
			"slack_connection_busy: ") {
			t.Errorf("status while the sign-in is held: %s", message)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The browser's refusal is the person's.
	useBrowser(t, api, "access_denied")
	if message := slackRefused(t, d, 3, "debug", "slack", "connect", "--state-dir", "fresh", "--client-id", inventedClient,
		"--redirect-uri", api.redirect); !strings.HasPrefix(message, "slack_login_denied: ") {
		t.Errorf("a refused approval: %s", message)
	}
	// A sign-in for another identity is refused, and the saved one stays.
	api.valid = map[string]bool{}
	useBrowser(t, api, "")
	if message := slackRefused(t, d, 3, "debug", "slack", "connect", "--state-dir", "acme", "--client-id", inventedClient,
		"--redirect-uri", api.redirect, "--team-id", "T99999999"); !strings.HasPrefix(message, "slack_identity_mismatch: ") {
		t.Errorf("a sign-in of another workspace: %s", message)
	}
	if code, stdout, _ := d.line(t, "debug", "slack", "status", "--state-dir", "acme", "--json"); code != 0 ||
		!strings.Contains(stdout, `"credential_state": "ready"`) {
		t.Errorf("the saved sign-in after a refused one: exit %d\n%s", code, stdout)
	}

	// A revocation Slack does not confirm fails, after the sign-in is removed.
	api.valid[inventedAccess], api.unrevoked = true, true
	if message := slackRefused(t, d, 5, "debug", "slack", "disconnect", "--revoke", "--state-dir", "acme"); !strings.HasPrefix(message,
		"Remote access-token revocation is unconfirmed. ") {
		t.Errorf("an unconfirmed revocation: %s", message)
	}
	for _, argv := range [][]string{{"disconnect"}, {"status"}} {
		code, stdout, _ := d.line(t, append([]string{"debug", "slack", "--state-dir", "acme"}, argv...)...)
		if code != 0 || !strings.HasPrefix(stdout, map[string]string{"disconnect": "No local Slack credentials were present.",
			"status": "No Slack connection is saved."}[argv[0]]) {
			t.Errorf("%v after the sign-in is removed: exit %d\n%s", argv, code, stdout)
		}
	}
	if message := slackRefused(t, d, 3, "debug", "slack", "status", "--check", "--state-dir", "acme"); !strings.HasPrefix(message,
		"slack_not_connected: ") {
		t.Errorf("status --check with no sign-in: %s", message)
	}
}

// The system browser is started, not waited on, with the start page alone and an environment that holds
// none of the connector's settings. An opener that stays running, as xdg-open can, holds up nothing: the
// approval is taken while it runs. The start page leads to Slack once; a callback from another site's page,
// and one without the cookie the start page set, is answered and ignored.
func TestTheSystemBrowserIsGivenOnlyTheStartPage(t *testing.T) {
	d, folder, _ := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect = freeRedirect(t)
	previous := openBrowser
	openBrowser = systemBrowser
	t.Cleanup(func() { openBrowser = previous })
	answerTerminal(t, "yes")
	// An opener that notes what it was given and then stays running.
	bin := t.TempDir()
	given, environment := filepath.Join(bin, "given"), filepath.Join(bin, "environment")
	script := "#!/bin/sh\nenv > '" + environment + ".part' && mv '" + environment + ".part' '" + environment + "'\n" +
		"printf '%s' \"$*\" > '" + given + "'\nexec sleep 20\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PGPASSWORD", "invented-database-password")
	t.Setenv("INVENTED_TOKEN", "invented-token-value")
	t.Setenv("XDG_RUNTIME_DIR", "/invented/runtime")

	answers := make(chan string, 2)
	go func() {
		var start []byte
		for range 400 {
			if start, _ = os.ReadFile(given); len(start) > 0 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		client := browserClient(t)
		state := api.follow(t, client, string(start))
		elsewhere, _, _ := visitWith(client, api.redirect+"?code="+inventedCode+"&state="+url.QueryEscape(state),
			map[string]string{"Origin": "https://invented-elsewhere.example"})
		unbound, _, _ := visit(api.redirect+"?code="+inventedCode+"&state="+url.QueryEscape(state), nil)
		approved, _, body := visitWith(client, api.redirect+"?code="+inventedCode+"&state="+url.QueryEscape(state), nil)
		answers <- fmt.Sprintf("%d %d %d %s", elsewhere, unbound, approved, body)
	}()
	began := time.Now()
	code, stdout, stderr := d.line(t, "debug", "slack", "connect", "--client-id", inventedClient, "--redirect-uri",
		api.redirect, "--state-dir", "acme", "--json")
	if code != 0 || time.Since(began) > 8*time.Second || !strings.Contains(stdout, `"credential_state": "ready"`) {
		t.Fatalf("connect with an opener that stays running: exit %d after %v\n%s%s", code, time.Since(began), stdout, stderr)
	}
	if answer := <-answers; !strings.HasPrefix(answer, "400 400 200 ") {
		t.Errorf("the callbacks were answered %s", answer)
	}
	if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); err != nil {
		t.Errorf("the sign-in was not saved: %v", err)
	}
	argument, _ := os.ReadFile(given)
	for _, private := range []string{"state", "code_challenge", "slack.com"} {
		if strings.Contains(string(argument), private) {
			t.Errorf("the opener's argument holds %s", private)
		}
	}
	env, _ := os.ReadFile(environment)
	if !strings.Contains(string(env), "XDG_RUNTIME_DIR=/invented/runtime\n") || !strings.Contains(string(env), "PATH=") {
		t.Errorf("the opener's environment lacks what a desktop needs:\n%s", env)
	}
	for _, line := range strings.Split(string(env), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if strings.HasPrefix(name, "GTM_") || strings.HasPrefix(name, "PG") || strings.HasSuffix(name, "_TOKEN") ||
			strings.Contains(line, "invented-database-password") || strings.Contains(line, "invented-token-value") {
			t.Errorf("the opener was given %s", name)
		}
	}
}

// A workspace whose Slack was connected stays pinned to that user after its sign-in is removed: a new
// sign-in for another user is refused before anything is saved.
func TestAPinnedWorkspaceRefusesASignInOfAnotherUser(t *testing.T) {
	d, folder, wall := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect = freeRedirect(t)
	books := registerRecordedFolders(t)
	seedSignIn(t, folder, "acme", *wall, "ready")
	connectRecorded(t, d, books, recordedCards(t)[0], "acme")
	if code, _, stderr := d.line(t, "debug", "slack", "disconnect", "--state-dir", "acme"); code != 0 {
		t.Fatalf("disconnect: exit %d\n%s", code, stderr)
	}
	api.user = "U87654321"
	useBrowser(t, api, "")
	if message := slackRefused(t, d, 3, "debug", "slack", "connect", "--state-dir", "acme", "--client-id", inventedClient,
		"--redirect-uri", api.redirect); !strings.HasPrefix(message, "slack_identity_mismatch: ") {
		t.Errorf("a sign-in of another user: %s", message)
	}
	if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the other user's sign-in was saved: %v", err)
	}
	// The user it was pinned to signs in again.
	api.user = ""
	useBrowser(t, api, "")
	if code, _, stderr := d.line(t, "debug", "slack", "connect", "--state-dir", "acme", "--client-id", inventedClient,
		"--redirect-uri", api.redirect); code != 0 {
		t.Errorf("a sign-in of the pinned user: exit %d\n%s", code, stderr)
	}
}

// detaching is a CRM that runs a command of its own during its first read, as another terminal would.
type detaching struct {
	crmSource
	during func()
}

func (c *detaching) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	if during := c.during; during != nil {
		c.during = nil
		during()
	}
	return c.crmSource.Read(ctx, tool, accountID)
}

// Slack detached while debug slack map reads the CRM leaves no mapping: the mapping is saved only while the
// CRM and Slack are the connections it was made through.
func TestAMappingIsNotSavedAfterSlackIsDetached(t *testing.T) {
	d, folder, wall := slackSetup(t)
	newInventedSlack(t)
	books := registerRecordedFolders(t)
	card := recordedCards(t)[0]
	seedSignIn(t, folder, "acme", *wall, "ready")
	connectRecorded(t, d, books, card, "acme")
	crm := kindsNamed("salesforce")[0]
	crm.crm = func(_ context.Context, o opening) (crmSource, error) {
		return &detaching{demoBook{books[o.workspace.Slug]}, func() {
			if code, _, stderr := d.line(t, "connect", "slack", "--remove", "--state-dir", "acme"); code != 0 {
				t.Errorf("connect slack --remove: exit %d\n%s", code, stderr)
			}
		}}, nil
	}
	if message := slackRefused(t, d, 3, "debug", "slack", "map", "--state-dir", "acme", "--account", card.account,
		"--channel", card.mapped("channel_id"), "--thread-ts", card.mapped("thread_ts")); !strings.Contains(message,
		"connected again") {
		t.Errorf("a map whose Slack was detached meanwhile: %s", message)
	}
	workspace, err := d.owner.Workspace(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := d.owner.SlackMappingOf(ctx, workspace.ID, card.account); saved != "" || err != nil {
		t.Errorf("the mapping was saved: %v", err)
	}
}

// Each request the Slack sign-in commands make runs under a receipt of its step in the workspace the command
// names, committed before the request, as a second connection to the database sees it: connect's code
// exchange and identity check, status --check's refresh and identity check, and disconnect --revoke's
// revocation. A request whose answer is refused leaves its receipt finished as an error. status without
// --check asks Slack nothing and keeps no receipt.
func TestSlackSignInRequestsRunUnderReceipts(t *testing.T) {
	d, _, wall := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect, api.expiresIn = freeRedirect(t), 12*3600
	owner := conn(t, d.ownerURL)
	var pending []string
	api.before = func(string) {
		var tools []string
		if err := owner.QueryRow(ctx, `SELECT coalesce(array_agg(r.tool ORDER BY r.started_at), '{}') FROM receipts r
			JOIN workspaces w ON w.id = r.workspace_id WHERE w.slug = 'acme' AND r.status = 'pending'`).Scan(&tools); err != nil {
			t.Error(err)
		}
		pending = append(pending, strings.Join(tools, ","))
	}
	where := []string{"--state-dir", "acme"}

	b := useBrowser(t, api, "")
	code, stdout, stderr := d.line(t, append([]string{"debug", "slack", "connect", "--client-id", inventedClient,
		"--redirect-uri", api.redirect}, where...)...)
	for range b.answers {
	}
	if code != 0 {
		t.Fatalf("connect: exit %d\n%s%s", code, stdout, stderr)
	}
	if code, stdout, _ = d.line(t, append([]string{"debug", "slack", "status"}, where...)...); code != 0 {
		t.Fatalf("status: exit %d\n%s", code, stdout)
	}
	// Past the access token's expiry, the check refreshes it once.
	*wall = wall.Add(12 * time.Hour)
	if code, stdout, _ = d.line(t, append([]string{"debug", "slack", "status", "--check"}, where...)...); code != 0 {
		t.Fatalf("status --check: exit %d\n%s", code, stdout)
	}
	api.unrevoked = true
	if code, stdout, _ = d.line(t, append([]string{"debug", "slack", "disconnect", "--revoke"}, where...)...); code != 5 {
		t.Fatalf("disconnect --revoke, unconfirmed: exit %d\n%s", code, stdout)
	}

	steps := []string{"slack_sign_in_exchange", "slack_sign_in_identity", "slack_sign_in_refresh",
		"slack_sign_in_identity", "slack_sign_in_revoke"}
	if !slices.Equal(pending, steps) {
		t.Errorf("the receipts pending as each request arrived: %q", pending)
	}
	if got := d.audited(t, "acme"); got != "slack_sign_in_exchange:ok:200: slack_sign_in_identity:ok:200: "+
		"slack_sign_in_refresh:ok:200: slack_sign_in_identity:ok:200: slack_sign_in_revoke:error:200:request_failed" {
		t.Errorf("the receipts: %s", got)
	}
	if n := d.count(t, `SELECT count(DISTINCT r.scope) + count(DISTINCT r.brief_id) FROM receipts r
		JOIN workspaces w ON w.id = r.workspace_id WHERE w.slug = 'acme' AND r.scope = 'slack:read'
		AND r.connection_id IS NULL`); n != 4 {
		t.Errorf("the receipts are not each command's slack:read run of no connection: %d", n)
	}
}

// A new approval is saved only once the person at the command's terminal says yes to the Slack user, team
// and workspace it came from. Whoever loads the one-time start page first can finish the sign-in with an
// approval of their own; any answer but yes saves nothing, though the exchange and the identity check keep
// their receipts.
func TestASignInIsSavedOnlyOnTheOperatorsYes(t *testing.T) {
	d, folder, _ := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect = freeRedirect(t)
	where := []string{"--state-dir", "acme"}
	connect := append([]string{"debug", "slack", "connect", "--client-id", inventedClient, "--redirect-uri", api.redirect}, where...)
	for _, answer := range []string{"no", "", "YES", "yes please"} {
		b := useBrowser(t, api, "")
		asked := answerTerminal(t, answer)
		message := slackRefused(t, d, 3, connect...)
		var answers []string
		for answer := range b.answers {
			answers = append(answers, answer)
		}
		if !strings.HasPrefix(message, "slack_login_unconfirmed: ") || len(answers) != 1 ||
			!strings.Contains(answers[0], "did not finish") {
			t.Errorf("answered %q: %s, the browser was answered %q", answer, message, answers)
		}
		if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("answered %q, the sign-in was saved: %v", answer, err)
		}
		if len(*asked) != 1 || !strings.Contains((*asked)[0], "user "+inventedUser+" in team "+inventedTeam+
			" ("+inventedOrigin+") for app "+inventedApp) || !strings.Contains((*asked)[0], "for workspace acme?") {
			t.Errorf("the question asked: %q", *asked)
		}
	}
	if got := d.audited(t, "acme"); got != strings.TrimSpace(strings.Repeat(
		"slack_sign_in_exchange:ok:200: slack_sign_in_identity:ok:200: ", 4)) {
		t.Errorf("the receipts: %s", got)
	}
	useBrowser(t, api, "")
	if code, stdout, stderr := d.line(t, connect...); code != 0 {
		t.Fatalf("connect with yes: exit %d\n%s%s", code, stdout, stderr)
	}
	if _, err := os.Lstat(filepath.Join(folder, "slack-acme.json")); err != nil {
		t.Errorf("a confirmed sign-in was not saved: %v", err)
	}
}

// The requests' receipts are written before the sign-in changes. With the receipt store refusing, a check
// of a token due for a refresh sends Slack nothing and leaves the saved sign-in as it was, so the next
// check refreshes it without a new login. An exchange whose answer says ok but holds no grant leaves its
// receipt as an error, decided once the answer is read.
func TestASlackSignInChangesNothingBeforeItsReceipt(t *testing.T) {
	d, folder, wall := slackSetup(t)
	api := newInventedSlack(t)
	api.redirect, api.expiresIn = freeRedirect(t), 12*3600
	where := []string{"--state-dir", "acme"}
	useBrowser(t, api, "")
	if code, stdout, stderr := d.line(t, append([]string{"debug", "slack", "connect", "--client-id", inventedClient,
		"--redirect-uri", api.redirect}, where...)...); code != 0 {
		t.Fatalf("connect: exit %d\n%s%s", code, stdout, stderr)
	}
	saved := func() string {
		raw, err := os.ReadFile(filepath.Join(folder, "slack-acme.json"))
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			State string `json:"credential_state"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		return document.State
	}
	owner := conn(t, d.ownerURL)
	role := d.appRole(t)
	if _, err := owner.Exec(ctx, "REVOKE INSERT ON receipts FROM "+role); err != nil {
		t.Fatal(err)
	}
	*wall = wall.Add(12 * time.Hour)
	api.paths = nil
	if code, _, _ := d.line(t, append([]string{"debug", "slack", "status", "--check"}, where...)...); code == 0 {
		t.Error("a check whose receipt was refused passed")
	}
	if len(api.paths) != 0 || saved() != "ready" {
		t.Errorf("with its receipt refused, the check asked Slack for %q and left the sign-in %s", api.paths, saved())
	}
	if _, err := owner.Exec(ctx, "GRANT INSERT ON receipts TO "+role); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := d.line(t, append([]string{"debug", "slack", "status", "--check"}, where...)...); code != 0 {
		t.Errorf("the next check: exit %d\n%s", code, stdout)
	}

	// An exchange answer that says ok and holds no grant.
	other := []string{"--state-dir", "other"}
	api.noGrant = true
	b := useBrowser(t, api, "")
	message := slackRefused(t, d, 5, append([]string{"debug", "slack", "connect", "--client-id", inventedClient,
		"--redirect-uri", api.redirect}, other...)...)
	for range b.answers {
	}
	if !strings.HasPrefix(message, "slack_response_invalid: ") {
		t.Errorf("an exchange with no grant: %s", message)
	}
	if got := d.audited(t, "other"); got != "slack_sign_in_exchange:error:200:request_failed" {
		t.Errorf("the receipts of an exchange with no grant: %s", got)
	}
}
