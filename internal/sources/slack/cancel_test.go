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

func TestTheReadersOwnTransport(t *testing.T) {
	own, isHTTP := (&Reader{}).transport().(*http.Transport)
	if !isHTTP || own.Proxy != nil || own.TLSClientConfig != nil || own.ResponseHeaderTimeout != readTimeout {
		t.Fatalf("the transport takes a proxy from the environment or changes TLS verification: %+v", own)
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
			t.Errorf("headers %v", r.Header)
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
