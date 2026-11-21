package salesforce

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// These tests run the reader's own transport against a TLS server on loopback that stands in for the
// invented instance. The dial is redirected to it and its certificate is made here; nothing leaves the
// machine.

// identity is a certificate for the invented instance's name, and the roots that trust it.
func identity(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(salesforcetest.Instance, "https://")
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// to dials the test server whatever address is asked for.
func to(address string) dial {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

// answered is the org's answer to a request a server received.
func answered(org *salesforcetest.Org, r *http.Request) (*http.Response, []byte) {
	asked := r.Clone(r.Context())
	asked.URL.Scheme, asked.URL.Host = "https", r.Host
	answer, _ := org.RoundTrip(asked)
	body, _ := io.ReadAll(answer.Body)
	return answer, body
}

// overTLS is a Reader of the org through the reader's own transport, and a server that offers HTTP/2
// before HTTP/1.1. seen is given every request the server received.
func overTLS(t *testing.T, org *salesforcetest.Org, seen func(*http.Request)) (*Reader, *httptest.Server) {
	t.Helper()
	certificate, roots := identity(t)
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
	// A handshake the reader refuses is the server's to log, and it logs nowhere.
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2", "http/1.1"}}
	server.StartTLS()
	t.Cleanup(server.Close)
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production",
		Runner: salesforcetest.NewSF(), Transport: ownTransport(to(server.Listener.Addr().String()), roots)})
	if err != nil {
		t.Fatal(err)
	}
	return reader, server
}

// The transport a Reader makes for itself speaks HTTP/1.1 to a server that would rather speak HTTP/2, takes
// no proxy and opens one connection a request.
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
	reader, _ = overTLS(t, salesforcetest.NewOrg(), func(r *http.Request) {
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
	// The Organization row, then the four objects' fields.
	if want := strings.Fields(strings.Repeat("HTTP/1.1 ", 5)); !slices.Equal(protocols, want) || len(connections) != 5 {
		t.Fatalf("the server was asked over %v on %d connections", protocols, len(connections))
	}
}

// The transport's own TLS dial keeps the certificate checks: a server whose certificate is not trusted, or
// is for another name, is never sent a request.
func TestTheReadersOwnTransportChecksTheCertificate(t *testing.T) {
	var asked atomic.Int32
	_, server := overTLS(t, salesforcetest.NewOrg(), func(*http.Request) { asked.Add(1) })
	_, trusted := identity(t)
	for name, given := range map[string]struct {
		instance string
		roots    *x509.CertPool
	}{
		"a certificate no root signed":   {salesforcetest.Instance, trusted},
		"a certificate for another name": {"https://invented-other.my.salesforce.com", server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs},
	} {
		sf := salesforcetest.NewSF()
		sf.Instance = given.instance
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf,
			Transport: ownTransport(to(server.Listener.Addr().String()), given.roots)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != unavailable || asked.Load() != 0 {
			t.Fatalf("%s: the code %q after %d requests", name, codeOf(err), asked.Load())
		}
	}
}

// With HTTP/2 debugging on, net/http logs every header an HTTP/2 request sends. The switch is read when the
// process starts, so the read runs in a second copy of this test with the switch set, and nothing that copy
// writes may hold the bearer.
func TestTheBearerIsNotInTheHTTP2DebugLog(t *testing.T) {
	const child = "GTM_TEST_HTTP2_DEBUG_READ"
	if os.Getenv(child) == "1" {
		reader, _ := overTLS(t, salesforcetest.NewOrg(), func(*http.Request) {})
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

// logged keeps what the process logs while a test runs.
type logged struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

func (l *logged) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Len()
}

func capture(t *testing.T) *logged {
	kept := &logged{}
	log.SetOutput(kept)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return kept
}

// net/http logs the first bytes a server sends on a connection where no request is waiting. A server that
// sends such bytes, after an answer or before a request, gets none of them into the log.
func TestWhatAServerSendsUnaskedIsNotLogged(t *testing.T) {
	const unasked = "HTTP/1.1 500 unasked " + salesforcetest.Token + "\r\n\r\n"
	certificate, roots := identity(t)
	settings := &tls.Config{Certificates: []tls.Certificate{certificate}}
	reader := func(t *testing.T, address string) *Reader {
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: salesforcetest.NewSF(),
			Transport: ownTransport(to(address), roots)})
		if err != nil {
			t.Fatal(err)
		}
		return reader
	}
	// hungUp waits for a connection to be closed by the reader. net/http writes its line before it closes
	// the connection, so by then the line is in the log or will never be.
	hungUp := func(t *testing.T, closed chan struct{}) {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the reader left a connection open")
		}
	}

	t.Run("after an answer", func(t *testing.T) {
		kept := capture(t)
		org := salesforcetest.NewOrg()
		org.Edition = "Developer Edition"
		closed := make(chan struct{}, 1)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, body := answered(org, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			// The answer and the bytes after it leave in one write, so they arrive together.
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s%s",
				len(body), body, unasked)
			_, _ = io.Copy(io.Discard, conn)
			closed <- struct{}{}
		}))
		server.TLS = settings.Clone()
		server.StartTLS()
		defer server.Close()
		if _, err := reader(t, server.Listener.Addr().String()).PrepareConnection(context.Background()); err != nil {
			t.Fatalf("the read was refused with %q", codeOf(err))
		}
		hungUp(t, closed)
		if kept.size() != 0 {
			t.Fatalf("%d bytes were logged", kept.size())
		}
	})

	t.Run("before a request", func(t *testing.T) {
		kept := capture(t)
		// net/http logs these bytes only when they arrive before it has counted the request, which it
		// seldom loses to a server on loopback. TestAConnectionIsNotReadBeforeItsRequest shows why it
		// cannot lose here; this shows what the reader makes of such a server.
		listener, err := tls.Listen("tcp", "127.0.0.1:0", settings.Clone())
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		closed := make(chan struct{}, 1)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				// The server speaks as soon as the handshake is done, and never answers the request.
				if conn.(*tls.Conn).Handshake() == nil {
					_, _ = conn.Write([]byte(unasked))
					_, _ = io.Copy(io.Discard, conn)
				}
				_ = conn.Close()
				closed <- struct{}{}
			}
		}()
		reader := reader(t, listener.Addr().String())
		for range 20 {
			// What the server sent is read as its answer to the request, and is not a usable one.
			if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != httpError {
				t.Fatalf("the read gave the code %q", codeOf(err))
			}
			hungUp(t, closed)
		}
		if kept.size() != 0 {
			t.Fatalf("%d bytes were logged", kept.size())
		}
	})
}

// A connection of the reader's own transport gives nothing a server sent until the request is written.
func TestAConnectionIsNotReadBeforeItsRequest(t *testing.T) {
	near, far := net.Pipe()
	defer far.Close()
	conn := newAskedFirst(near)
	go func() { _, _ = far.Write([]byte("unasked")) }()
	read := make(chan string, 1)
	go func() {
		buffer := make([]byte, 16)
		n, _ := conn.Read(buffer)
		read <- string(buffer[:n])
	}()
	select {
	case got := <-read:
		t.Fatalf("%d bytes were read before anything was written", len(got))
	case <-time.After(50 * time.Millisecond):
	}
	go func() { _, _ = io.ReadFull(far, make([]byte, 3)) }()
	if _, err := conn.Write([]byte("GET")); err != nil {
		t.Fatal(err)
	}
	if got := <-read; got != "unasked" {
		t.Fatalf("read %q after the request", got)
	}

	// A connection that is closed unused ends a read that was waiting on it.
	near, far = net.Pipe()
	defer far.Close()
	conn = newAskedFirst(near)
	failed := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		failed <- err
	}()
	_ = conn.Close()
	if err := <-failed; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("the read of a closed connection gave %v", err)
	}
}
