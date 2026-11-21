package slack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp/sourcehttptest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// cancelled runs a read whose answers never finish, cancels it shortly after, and requires that it returns
// the cancel at once, with no result.
func cancelled(t *testing.T, b *bench, replies []reply, auth ...reply) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.slack.replies, b.slack.auth = replies, auth
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	read, err := b.reader.ReadSlackMessages(ctx, accountID)
	if read != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancel", err)
	}
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("the read returned %v after the cancel", waited)
	}
	if last := b.last(); last.Code != "cancelled" || last.ReturnedMessages != 0 {
		t.Fatalf("report %+v", last)
	}
}

func TestACancelStopsTheRead(t *testing.T) {
	t.Run("before anything is sent", func(t *testing.T) {
		b := newBench(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b.slack.replies = []reply{page(msg(parentTS))}
		read, err := b.reader.ReadSlackMessages(ctx, accountID)
		if read != nil || !errors.Is(err, context.Canceled) || len(b.slack.sent) != 0 || b.last().APICalls != 0 {
			t.Fatalf("got %v after %d requests", err, len(b.slack.sent))
		}
	})
	t.Run("while a page is awaited", func(t *testing.T) {
		cancelled(t, newBench(t), []reply{{hang: true}})
	})
	t.Run("while a later page arrives", func(t *testing.T) {
		b := newBench(t)
		cancelled(t, b, []reply{page(msg(parentTS), cursor("next")), {status: 200, stall: true, body: `{"ok":true,"messages":[`}})
		if b.last().Pages != 1 || b.last().MessageResponseBytes == 0 {
			t.Fatalf("report %+v", b.last())
		}
	})
	t.Run("during the wait after a 429", func(t *testing.T) {
		b := newBench(t)
		b.reader.sleep = nil
		cancelled(t, b, []reply{status(429, "", "Retry-After", "5"), page(msg(parentTS))})
		if len(b.slack.sent) != 1 {
			t.Fatalf("%d requests", len(b.slack.sent))
		}
	})
	t.Run("while the token is refreshed", func(t *testing.T) {
		b := newBench(t)
		cancelled(t, b, []reply{ok(expiredAnswer)}, reply{hang: true})
		// The refresh token may have reached Slack, so the sign-in stays saved as needing a login.
		if b.state() != pending {
			t.Fatalf("saved %s", b.state())
		}
	})
	t.Run("while the identity is checked", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved.State = unchecked
		cancelled(t, b, nil, reply{hang: true})
		if b.state() != unchecked || len(b.saved.saves) != 0 {
			t.Fatalf("saved %s", b.state())
		}
	})
}

func TestDeadlinesInRealTime(t *testing.T) {
	t.Run("the caller's deadline", func(t *testing.T) {
		b := newBench(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		b.slack.replies = []reply{{hang: true}}
		read, err := b.reader.ReadSlackMessages(ctx, accountID)
		if read != nil || codeOf(err) != "slack_timeout" || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the read's own deadline", func(t *testing.T) {
		// The mapping takes all but 40 milliseconds of the 20 seconds, and the page never comes.
		b := newBench(t)
		b.reader.Mappings = slow{b.reader.Mappings, func() { b.clock.advance(operationTime - 40*time.Millisecond) }}
		started := time.Now()
		b.refuses("slack_timeout", []reply{{hang: true}})
		if waited := time.Since(started); waited > 3*time.Second {
			t.Fatalf("the read returned after %v", waited)
		}
	})
	t.Run("a body that stops arriving", func(t *testing.T) {
		b := newBench(t)
		b.reader.idle = 30 * time.Millisecond
		started := time.Now()
		b.refuses("slack_timeout", []reply{{status: 200, stall: true, body: `{"ok":true,`}})
		if waited := time.Since(started); waited > 3*time.Second || b.last().MessageResponseBytes != 11 {
			t.Fatalf("the read returned after %v: %+v", waited, b.last())
		}
	})
}

// slow is a mapping store that takes time, by the test's clock.
type slow struct {
	Mappings
	wait func()
}

func (s slow) Mapping(ctx context.Context, accountID string) (*strictjson.Object, error) {
	s.wait()
	return s.Mappings.Mapping(ctx, accountID)
}

// waiting is a mapping store that waits for its context to end and fails with its error.
type waiting struct{}

func (waiting) Mapping(ctx context.Context, _ string) (*strictjson.Object, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A store that waits on the context it is given is stopped when the read's 20 seconds end, and the read is
// refused as timed out. The caller gave no deadline of its own.
func TestAStoreThatWaitsIsStoppedAtTheDeadline(t *testing.T) {
	// almostOver is a bench whose read has 40 milliseconds left from its first moment on.
	almostOver := func(t *testing.T) *bench {
		b := newBench(t)
		started, asked := b.clock.tick, 0
		b.reader.Clock.Monotonic = func() time.Duration {
			if asked++; asked == 1 {
				return started
			}
			return started + operationTime - 40*time.Millisecond
		}
		return b
	}
	run := func(t *testing.T, b *bench, ctx context.Context, replies ...reply) error {
		t.Helper()
		b.slack.replies = replies
		done := make(chan error, 1)
		go func() {
			read, err := b.reader.ReadSlackMessages(ctx, accountID)
			if read != nil {
				err = errors.New("the read gave a result")
			}
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			t.Fatal("the read was still waiting on a store 3 seconds after its deadline")
			return nil
		}
	}
	t.Run("the mapping store", func(t *testing.T) {
		b := almostOver(t)
		b.reader.Mappings = waiting{}
		if err := run(t, b, context.Background()); codeOf(err) != "slack_timeout" || b.last().Phase != "mapping" ||
			b.saved.holds != 0 {
			t.Fatalf("got %v: %+v", codeOf(err), b.last())
		}
	})
	t.Run("the sign-in store", func(t *testing.T) {
		b := almostOver(t)
		b.saved.waitOn = 1
		if err := run(t, b, context.Background()); codeOf(err) != "slack_timeout" ||
			b.last().Phase != "credential_binding" || len(b.slack.sent) != 0 {
			t.Fatalf("got %v: %+v", codeOf(err), b.last())
		}
	})
	t.Run("the sign-in store when a refused token is set aside", func(t *testing.T) {
		b := almostOver(t)
		b.saved.waitOn = 2
		err := run(t, b, context.Background(), ok(`{"ok":false,"error":"invalid_auth"}`))
		if codeOf(err) != "slack_timeout" || b.last().Phase != "thread_read" || len(b.slack.sent) != 1 {
			t.Fatalf("got %v: %+v", codeOf(err), b.last())
		}
	})
	t.Run("a store that answers after the deadline", func(t *testing.T) {
		// Each store gives up when its context ends and returns no error, as if it had found nothing or
		// done its work. What it returned is not believed.
		b := almostOver(t)
		b.reader.Mappings = gaveUp{}
		if err := run(t, b, context.Background()); codeOf(err) != "slack_timeout" || b.last().Phase != "mapping" {
			t.Fatalf("the mapping store: got %v: %+v", codeOf(err), b.last())
		}
		b = almostOver(t)
		b.reader.Connection = gaveUp{}
		if err := run(t, b, context.Background()); codeOf(err) != "slack_timeout" || b.last().Phase != "credential_binding" {
			t.Fatalf("the sign-in store: got %v: %+v", codeOf(err), b.last())
		}
	})
	t.Run("a load that waits", func(t *testing.T) {
		b := almostOver(t)
		b.saved.loadWaits = true
		if err := run(t, b, context.Background()); codeOf(err) != "slack_timeout" ||
			b.last().Phase != "credential_binding" || len(b.slack.sent) != 0 {
			t.Fatalf("got %v: %+v", codeOf(err), b.last())
		}
	})
	t.Run("a save that waits", func(t *testing.T) {
		// The save that sets the sign-in aside before its refresh token is spent never returns in time, so
		// the refresh token is never sent.
		b := almostOver(t)
		b.saved.saveWaits = true
		err := run(t, b, context.Background(), ok(expiredAnswer))
		if codeOf(err) != "slack_timeout" || !slices.Equal(b.slack.paths(), []string{repliesPath}) {
			t.Fatalf("got %v after the requests %v", codeOf(err), b.slack.paths())
		}
	})
	t.Run("a store that was not waiting keeps its own failure", func(t *testing.T) {
		b := almostOver(t)
		b.saved.holdErr = errors.New("disk full")
		if err := run(t, b, context.Background()); codeOf(err) != "slack_storage_failed" {
			t.Fatalf("got %v", codeOf(err))
		}
	})
	t.Run("the caller's cancel stays a cancel", func(t *testing.T) {
		for name, change := range map[string]func(*bench){
			"the mapping store": func(b *bench) { b.reader.Mappings = waiting{} },
			"the sign-in store": func(b *bench) { b.saved.waitOn = 1 },
		} {
			b := newBench(t)
			change(b)
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(30*time.Millisecond, cancel)
			err := run(t, b, ctx)
			timer.Stop()
			cancel()
			if !errors.Is(err, context.Canceled) || b.last().Code != "cancelled" {
				t.Fatalf("%s: got %v: %+v", name, err, b.last())
			}
		}
	})
	t.Run("the caller's deadline is a timeout", func(t *testing.T) {
		b := newBench(t)
		b.reader.Mappings = waiting{}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if err := run(t, b, ctx); codeOf(err) != "slack_timeout" {
			t.Fatalf("got %v", codeOf(err))
		}
	})
}

// gaveUp is a store that waits for its context to end and then returns with no error: no mapping, and a
// sign-in it never opened.
type gaveUp struct{}

func (gaveUp) Mapping(ctx context.Context, _ string) (*strictjson.Object, error) {
	<-ctx.Done()
	return nil, nil
}

func (gaveUp) Hold(ctx context.Context, _ func(Saved) error) error {
	<-ctx.Done()
	return nil
}

// Every Load and Save is given a context that ends at the read's deadline. A Save's context does not end
// with the caller's cancel: the pair a refresh gave is saved though the caller left while it was fetched.
func TestASaveOutlivesTheCallersCancel(t *testing.T) {
	b := newBench(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.slack.before = func(request sent) {
		if request.path == refreshPath {
			cancel()
		}
	}
	b.slack.replies, b.slack.auth = []reply{ok(expiredAnswer)}, []reply{ok(replacement), ok(identified)}
	read, err := b.reader.ReadSlackMessages(ctx, accountID)
	if read != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancel", err)
	}
	// Set aside before the refresh, then saved as the unverified replacement. The identity check that
	// would follow is never sent.
	if want := []string{pending, unchecked}; !slices.Equal(b.saved.states(), want) ||
		b.saved.saved.AccessToken != NewSecret(newAccessToken) || !slices.Equal(b.slack.paths(), []string{repliesPath, refreshPath}) {
		t.Fatalf("saved states %v after the requests %v", b.saved.states(), b.slack.paths())
	}
	for i, given := range b.saved.saveContexts {
		if given.ended || !given.bounded {
			t.Errorf("save %d: its context had ended (%t) or had no deadline (%t)", i, given.ended, !given.bounded)
		}
	}
}

// The transport the reader makes for itself is the one of sourcehttp, as the Salesforce reader's is: HTTP/1.1
// only, one connection a request, and a connection that is not read before its request is written.
func TestTheReadersOwnTransport(t *testing.T) {
	own, isHTTP := (&Reader{}).transport().(*http.Transport)
	if !isHTTP || own.Proxy != nil || own.TLSClientConfig != nil || own.ResponseHeaderTimeout != readTimeout {
		t.Fatal("the transport takes a proxy from the environment or changes TLS verification")
	}
	if own.Protocols == nil || !own.Protocols.HTTP1() || own.Protocols.HTTP2() || own.Protocols.UnencryptedHTTP2() ||
		own.ForceAttemptHTTP2 || !own.DisableKeepAlives || own.DialTLSContext == nil || own.DialContext != nil {
		t.Fatal("the transport may speak HTTP/2, keep a connection, or dial without the gate on its reads")
	}

	// A read over it, against a server that would rather speak HTTP/2.
	var mu sync.Mutex
	var protocols, names []string
	connections := map[string]bool{}
	certificate, roots := sourcehttptest.Identity(t, "slack.com")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protocols = append(protocols, r.Proto)
		connections[r.RemoteAddr] = true
		mu.Unlock()
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"ok":true,"messages":[` + msg(parentTS) + `],"response_metadata":{"next_cursor":"two"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"messages":[` + msg(replyTS) + `]}`))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2", "http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			defer mu.Unlock()
			names = append(names, hello.ServerName)
			return nil, nil
		}}
	server.StartTLS()
	defer server.Close()
	b := newBench(t)
	b.reader.Transport = newTransport(sourcehttptest.To(server.Listener.Addr().String()), roots)
	read, err := b.reader.ReadSlackMessages(context.Background(), accountID)
	if err != nil || len(read.Records) != 2 {
		t.Fatalf("read: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"HTTP/1.1", "HTTP/1.1"}; !slices.Equal(protocols, want) || len(connections) != 2 ||
		!slices.Equal(names, []string{"slack.com", "slack.com"}) {
		t.Fatalf("the server was asked over %v on %d connections, named %v", protocols, len(connections), names)
	}
}

// A page that passes the byte limit is refused, and nothing more of it is taken from the connection. The
// transport here is net/http's own with connections it keeps, which reads up to 256 KiB more of a body that
// is closed early unless the request is ended first.
func TestNothingIsTakenFromTheConnectionPastTheLimit(t *testing.T) {
	transport, taken, stopped := sourcehttptest.Flood(t, `{"ok":true,"messages":[],"ignored":"`, 2*maxResponseBytes)
	b := newBench(t)
	b.reader.Transport = transport
	read, err := b.reader.ReadSlackMessages(context.Background(), accountID)
	if read != nil || codeOf(err) != "slack_resource_limit" || b.last().ResponseBytes != maxResponseBytes+1 {
		t.Fatalf("got %v: %+v", codeOf(err), b.last())
	}
	// The server stops when the connection is closed, and nothing is read from a closed connection.
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was left open")
	}
	// The bytes counted are the encrypted ones, with the handshake, the headers and what TLS and the
	// chunks add, and a record or two the transport had already read. 64 KiB covers those; 256 KiB more
	// would not fit.
	if past := taken.Load() - maxResponseBytes; past > 64<<10 {
		t.Fatalf("%d bytes were taken from the connection, %d past the limit", taken.Load(), past)
	}
}

// TestOverAConnection runs the reader through net/http's own transport against a TLS server on loopback
// that stands in for slack.com. The dial is redirected to it; nothing leaves the machine.
func TestOverAConnection(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var stopped atomic.Bool
	first := `{"ok":true,"messages":[` + msg(parentTS) + `],"has_more":true,"response_metadata":{"next_cursor":"two"}}`
	second := `{"ok":true,"messages":[` + msg(replyTS) + `],"has_more":false}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.Host+r.URL.RequestURI())
		mu.Unlock()
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Authorization") != "Bearer "+accessToken {
			t.Errorf("headers %v", shown(r.Header))
		}
		flusher := w.(http.Flusher)
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(first))
		case "two":
			// The second page comes in pieces and states no length.
			for _, piece := range []string{second[:10], second[10:40], second[40:]} {
				_, _ = w.Write([]byte(piece))
				flusher.Flush()
			}
		case "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "dribble":
			_, _ = w.Write([]byte(`{"ok":true,`))
			flusher.Flush()
			<-r.Context().Done()
			stopped.Store(true)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	address := server.Listener.Addr().String()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	fresh := func() *bench {
		b := newBench(t)
		b.reader.Transport = transport
		return b
	}

	b := fresh()
	read, err := b.reader.ReadSlackMessages(context.Background(), accountID)
	if err != nil || len(read.Records) != 2 {
		t.Fatalf("read: %v", err)
	}
	if got := count(t, read, "response_bytes"); got != len(first)+len(second) || count(t, read, "pages") != 2 {
		t.Fatalf("counts %s, and the server sent %d bytes", dump(t, member(t, read.Snapshot, "counts")), len(first)+len(second))
	}
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		taken := seen
		seen = nil
		return taken
	}
	base := "GET slack.com/api/conversations.replies?channel=C12345678&ts=1700000000.000001&latest=1795003200.000000&inclusive=true&limit="
	if got, want := requests(), []string{base + "100", base + "99&cursor=two"}; !slices.Equal(got, want) {
		t.Fatalf("the server saw %v", got)
	}

	// A redirect is refused and never followed.
	b = fresh()
	b.reader.Transport = rewrite{transport, "redirect"}
	_, err = b.reader.ReadSlackMessages(context.Background(), accountID)
	if got := requests(); codeOf(err) != "slack_response_invalid" || len(got) != 1 {
		t.Fatalf("redirect: %v after %v", err, got)
	}

	// A cancel closes a response that is still arriving.
	b = fresh()
	b.reader.Transport = rewrite{transport, "dribble"}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := b.reader.ReadSlackMessages(ctx, accountID); !errors.Is(err, context.Canceled) {
		t.Fatalf("dribble: %v", err)
	}
	for wait := 0; !stopped.Load(); wait++ {
		if wait > 200 {
			t.Fatal("the server never saw the connection close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.last().MessageResponseBytes != 11 {
		t.Fatalf("report %+v", b.last())
	}
}

// rewrite sends every request with a cursor that names the answer the test server gives.
type rewrite struct {
	next   http.RoundTripper
	cursor string
}

func (r rewrite) RoundTrip(request *http.Request) (*http.Response, error) {
	if !strings.Contains(request.URL.RawQuery, "cursor=") {
		request = request.Clone(request.Context())
		request.URL.RawQuery += "&cursor=" + r.cursor
	}
	return r.next.RoundTrip(request)
}
