package salesforce

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp/sourcehttptest"
)

// These tests run the reader over the transport it makes for itself, against a TLS server on loopback that
// stands in for the invented instance. The dial is redirected to it and its certificate is made here;
// nothing leaves the machine. What the transport does on its own is tested in sourcehttp.

// answered is the org's answer to a request a server received.
func answered(org *salesforcetest.Org, r *http.Request) (*http.Response, []byte) {
	asked := r.Clone(r.Context())
	asked.URL.Scheme, asked.URL.Host = "https", r.Host
	answer, _ := org.RoundTrip(asked)
	body, _ := io.ReadAll(answer.Body)
	return answer, body
}

// overTLS is a Reader of the org through the reader's own kind of transport, and a server that offers
// HTTP/2 before HTTP/1.1. seen is given every request the server received.
func overTLS(t *testing.T, org *salesforcetest.Org, seen func(*http.Request)) *Reader {
	t.Helper()
	certificate, roots := sourcehttptest.Identity(t, strings.TrimPrefix(salesforcetest.Instance, "https://"))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen(r)
		answer, body := answered(org, r)
		for name, values := range answer.Header {
			w.Header()[name] = values
		}
		w.WriteHeader(answer.StatusCode)
		_, _ = w.Write(body)
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2", "http/1.1"}}
	server.StartTLS()
	t.Cleanup(server.Close)
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production",
		Runner:    salesforcetest.NewSF(),
		Transport: sourcehttp.Transport(sourcehttptest.To(server.Listener.Addr().String()), roots)})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// The transport a Reader makes for itself is the one of sourcehttp: it speaks HTTP/1.1 to a server that
// would rather speak HTTP/2, takes no proxy and opens one connection a request.
func TestTheReadersOwnTransportSpeaksHTTP1(t *testing.T) {
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: salesforcetest.NewSF()})
	if err != nil {
		t.Fatal(err)
	}
	own, isHTTP := reader.client.Transport.(*http.Transport)
	if !isHTTP || own.Proxy != nil || own.ForceAttemptHTTP2 || own.Protocols == nil || !own.Protocols.HTTP1() ||
		own.Protocols.HTTP2() || own.Protocols.UnencryptedHTTP2() || !own.DisableKeepAlives || !own.DisableCompression ||
		own.TLSClientConfig != nil || own.DialTLSContext == nil {
		t.Fatal("the transport may speak HTTP/2, keep a connection, take a proxy or change certificate checks")
	}

	var mu sync.Mutex
	var protocols []string
	connections := map[string]bool{}
	reader = overTLS(t, salesforcetest.NewOrg(), func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		protocols = append(protocols, r.Proto)
		// Each connection comes from a port of its own.
		connections[r.RemoteAddr] = true
	})
	if _, err := reader.PrepareConnection(context.Background()); err != nil {
		t.Fatalf("the read was refused with %q", codeOf(err))
	}
	mu.Lock()
	defer mu.Unlock()
	// The Organization row, the user, then the four objects' fields.
	if want := strings.Fields(strings.Repeat("HTTP/1.1 ", 6)); !slices.Equal(protocols, want) || len(connections) != 6 {
		t.Fatalf("the server was asked over %v on %d connections", protocols, len(connections))
	}
}

// An answer that passes the operation's 1 MiB is refused, and nothing more of it is taken from the
// connection. The transport here is net/http's own with connections it keeps, which reads up to 256 KiB more
// of a body that is closed early unless the request is ended first.
func TestNothingIsTakenFromTheConnectionPastTheLimit(t *testing.T) {
	transport, taken, stopped := sourcehttptest.Flood(t, `{"totalSize": 1, "done": true, "ignored": "`, 2*maxBytes)
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: salesforcetest.NewSF(),
		Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != resultLimit {
		t.Fatalf("an answer past the limit gave the code %q", codeOf(err))
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
	if past := taken.Load() - maxBytes; past > 64<<10 {
		t.Fatalf("%d bytes were taken from the connection, %d past the limit", taken.Load(), past)
	}
}

// With HTTP/2 debugging on, net/http logs every header an HTTP/2 request sends. The switch is read when the
// process starts, so the read runs in a second copy of this test with the switch set, and nothing that copy
// writes may hold the bearer.
func TestTheBearerIsNotInTheHTTP2DebugLog(t *testing.T) {
	const child = "GTM_TEST_HTTP2_DEBUG_READ"
	if os.Getenv(child) == "1" {
		reader := overTLS(t, salesforcetest.NewOrg(), func(*http.Request) {})
		if _, err := reader.PrepareConnection(context.Background()); err != nil {
			t.Fatalf("the read was refused with %q", codeOf(err))
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestTheBearerIsNotInTheHTTP2DebugLog$", "-test.count=1")
	command.Env = append(os.Environ(), child+"=1", "GODEBUG=http2debug=2")
	output, err := command.CombinedOutput()
	shown := bytes.ReplaceAll(output, []byte(salesforcetest.Token), []byte("TOKEN"))
	switch {
	case bytes.Contains(output, []byte(salesforcetest.Token)):
		t.Fatalf("the bearer was logged in %d bytes of output", len(output))
	case err != nil:
		t.Fatalf("the read under http2debug=2 failed: %v\n%s", err, shown)
	case bytes.Contains(output, []byte("http2: ")):
		t.Fatalf("HTTP/2 was spoken:\n%s", shown)
	}
}
