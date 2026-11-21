package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The invented sign-in of tests/test_slack_auth.py. No value here was ever issued by Slack.
const (
	testClient     = "123456789.987654321"
	testRedirect   = "http://localhost:8765/slack/callback"
	accessToken    = "invented-private-access-token"
	refreshToken   = "invented-private-refresh-token"
	newAccessToken = "invented-replacement-access-token"
	newRefresh     = "invented-replacement-refresh-token"
	testOrigin     = "https://invented-workspace.slack.com"
)

var testIdentity = Identity{AppID: "A12345678", TeamID: "T12345678", UserID: "U12345678"}

// testClock stands still unless a test advances it.
type testClock struct {
	wall time.Time
	tick time.Duration
}

func newClock() *testClock {
	return &testClock{wall: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), tick: 1000 * time.Second}
}

func (c *testClock) clock() Clock {
	return Clock{Now: func() time.Time { return c.wall }, Monotonic: func() time.Duration { return c.tick }}
}

func (c *testClock) advance(d time.Duration) {
	c.wall = c.wall.Add(d)
	c.tick += d
}

// signIn is a saved sign-in obtained and verified at now, or two hours before it when expired.
func signIn(now time.Time, rotating, expired bool) *Credentials {
	obtained := now
	if expired {
		obtained = now.Add(-2 * time.Hour)
	}
	document := &Credentials{ClientID: testClient, RedirectURI: testRedirect, Identity: testIdentity,
		WorkspaceOrigin: testOrigin, RequestedScopes: []string{"channels:history"},
		GrantedScopes: []string{"channels:history"}, State: ready, AccessToken: NewSecret(accessToken),
		AccessObtainedAt: obtained, LastVerifiedAt: obtained}
	if rotating {
		document.RefreshToken = NewSecret(refreshToken)
		document.RefreshObtainedAt = obtained
		document.AccessExpiresAt = obtained.Add(time.Hour)
	}
	return document
}

// connection is a sign-in store in memory. It keeps every document saved, in order.
type connection struct {
	saved    *Credentials
	saves    []Credentials
	holds    int
	holdErr  error // what Hold refuses with, when set
	saveErr  error // what Save fails with, when set
	whenHeld func()
	waitOn   int // the Hold, counted from one, that waits for its context to end and fails with its error
	// loadWaits and saveWaits make a Load or a Save wait for its context to end and fail with its error.
	loadWaits, saveWaits bool
	// saveContexts are, for each Save, whether its context had ended when it was called, and whether it
	// carried a deadline.
	saveContexts []saveContext
}

type saveContext struct{ ended, bounded bool }

func (c *connection) Hold(ctx context.Context, held func(Saved) error) error {
	if c.holdErr != nil {
		return c.holdErr
	}
	c.holds++
	if c.holds == c.waitOn {
		<-ctx.Done()
		return ctx.Err()
	}
	if c.whenHeld != nil {
		c.whenHeld()
	}
	return held(c)
}

func (c *connection) Load(ctx context.Context) (*Credentials, error) {
	if c.loadWaits {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.saved == nil {
		return nil, nil
	}
	document := c.saved.copy()
	return &document, nil
}

func (c *connection) Save(ctx context.Context, document *Credentials) error {
	_, bounded := ctx.Deadline()
	c.saveContexts = append(c.saveContexts, saveContext{ctx.Err() != nil, bounded})
	if c.saveWaits {
		<-ctx.Done()
		return ctx.Err()
	}
	if c.saveErr != nil {
		return c.saveErr
	}
	kept := document.copy()
	c.saved = &kept
	c.saves = append(c.saves, kept)
	return nil
}

// states are the credential states saved so far, in order.
func (c *connection) states() []string {
	var states []string
	for _, document := range c.saves {
		states = append(states, document.State)
	}
	return states
}

// mappings is a mapping store in memory, by account.
type mappings map[string]*strictjson.Object

func (m mappings) Mapping(_ context.Context, accountID string) (*strictjson.Object, error) {
	if found, selected := m[accountID]; selected {
		return found, nil
	}
	return nil, refuse("slack_mapping_missing")
}

func binding(t testing.TB, identity Identity) *strictjson.Object {
	t.Helper()
	bound, err := IdentityBinding(identity)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func parse(t testing.TB, document string) any {
	t.Helper()
	value, err := strictjson.Loads([]byte(document))
	if err != nil {
		t.Fatalf("%v in %s", err, document)
	}
	return value
}

func dump(t testing.TB, value any) string {
	t.Helper()
	text, err := strictjson.Dumps(value, strictjson.DumpOptions{Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// member walks an object by keys and fails the test on a missing one.
func member(t testing.TB, value any, keys ...string) any {
	t.Helper()
	for _, key := range keys {
		o, isObject := value.(*strictjson.Object)
		found, present := o.Get(key)
		if !isObject || !present {
			t.Fatalf("no member %q", key)
		}
		value = found
	}
	return value
}

// reply is one scripted answer: a status, headers and a body, or a transport failure.
type reply struct {
	status int
	header http.Header
	body   string
	err    error
	hang   bool // no response comes: the transport waits for the request to be cancelled
	stall  bool // the body stops after its text and waits for the request to be cancelled
	// stream, when set, is the body instead of the text.
	stream io.ReadCloser
}

// stalled is a body that gives its text and then waits for the request's context to end.
type stalled struct {
	ctx  context.Context
	text io.Reader
}

func (s *stalled) Read(p []byte) (int, error) {
	if n, _ := s.text.Read(p); n > 0 {
		return n, nil
	}
	<-s.ctx.Done()
	return 0, s.ctx.Err()
}

func (s *stalled) Close() error { return nil }

func ok(document string) reply { return reply{status: 200, body: document} }

func status(code int, body string, header ...string) reply {
	r := reply{status: code, body: body, header: http.Header{}}
	for i := 0; i+1 < len(header); i += 2 {
		r.header.Set(header[i], header[i+1])
	}
	return r
}

// sent is one request as the transport saw it.
type sent struct {
	method, url, path, query string
	header                   http.Header
	body                     string
}

// Format prints a request for a failure message, under any verb, without the tokens it carried.
func (s sent) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "%s %s %v %q", s.method, s.url, shown(s.header), formShown(s.body))
}

// shown is a request's headers with the bearer left out.
func shown(header http.Header) http.Header {
	printed := header.Clone()
	if printed.Get("Authorization") != "" {
		printed.Set("Authorization", "Bearer "+redacted)
	}
	return printed
}

// formShown is a request's form with the refresh token left out.
func formShown(form string) string {
	members := strings.Split(form, "&")
	for i, member := range members {
		if name, _, found := strings.Cut(member, "="); found && name == "refresh_token" {
			members[i] = name + "=" + redacted
		}
	}
	return strings.Join(members, "&")
}

// script is a transport that answers page requests and sign-in requests from two queues, and never
// reaches a network.
type script struct {
	t       testing.TB
	replies []reply
	auth    []reply
	sent    []sent
	before  func(sent) // runs before each answer, as another process or a slow network would
}

func (s *script) RoundTrip(request *http.Request) (*http.Response, error) {
	seen := sent{method: request.Method, url: request.URL.String(), path: request.URL.Path,
		query: request.URL.RawQuery, header: request.Header.Clone()}
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		seen.body = string(raw)
	}
	if request.URL.Scheme != "https" || request.URL.Host != "slack.com" {
		s.t.Errorf("a request left for %s://%s", request.URL.Scheme, request.URL.Host)
	}
	s.sent = append(s.sent, seen)
	if s.before != nil {
		s.before(seen)
	}
	queue := &s.auth
	if strings.HasSuffix(seen.path, "conversations.replies") {
		queue = &s.replies
	}
	if len(*queue) == 0 {
		s.t.Errorf("unexpected request %s %s", seen.method, seen.path)
		return nil, errors.New("unexpected request")
	}
	next := (*queue)[0]
	*queue = (*queue)[1:]
	if next.hang {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	if next.err != nil {
		return nil, next.err
	}
	return next.response(request), nil
}

func (r reply) response(request *http.Request) *http.Response {
	header := r.header.Clone()
	if header == nil {
		header = http.Header{}
	}
	body := r.stream
	if r.stall {
		body = &stalled{request.Context(), strings.NewReader(r.body)}
	}
	if body == nil {
		body = io.NopCloser(strings.NewReader(r.body))
		if header.Get("Content-Length") == "" {
			header.Set("Content-Length", fmt.Sprint(len(r.body)))
		}
	}
	return &http.Response{StatusCode: r.status, Header: header, Body: body, Request: request}
}

func (s *script) paths() []string {
	var paths []string
	for _, request := range s.sent {
		paths = append(paths, request.path)
	}
	return paths
}

func codeOf(err error) string {
	if err == nil {
		return "ok"
	}
	if reason := code(err); reason != "" {
		return reason
	}
	return err.Error()
}

// stream is a body with no stated length.
func stream(text string) io.ReadCloser { return io.NopCloser(strings.NewReader(text)) }
