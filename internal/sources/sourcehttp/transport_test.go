package sourcehttp_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp/sourcehttptest"
)

// Every test asks an invented host, over a dial that goes to a TLS server on loopback.
const (
	host   = "invented-source.example"
	bearer = "invented-bearer-for-transport-tests"
)

// ask sends one GET with the bearer and gives the status, or the error.
func ask(ctx context.Context, transport http.RoundTripper, name string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+name+"/thing", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	return response.StatusCode, err
}

// served is a server with a certificate for host that offers HTTP/2 before HTTP/1.1, and the roots that
// trust it. configure may change the server before it starts.
func served(t *testing.T, handler http.Handler, configure func(*httptest.Server)) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	certificate, roots := sourcehttptest.Identity(t, host)
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	// A handshake the client refuses is the server's to log, and it logs nowhere.
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2", "http/1.1"}}
	if configure != nil {
		configure(server)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, roots
}

// The transport speaks HTTP/1.1 to a server that would rather speak HTTP/2, on one connection a request,
// and names the host it asks for in the handshake.
func TestOneConnectionARequestOverHTTP1(t *testing.T) {
	var mu sync.Mutex
	var protocols, names []string
	connections := map[string]bool{}
	server, roots := served(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		protocols = append(protocols, r.Proto)
		// Each connection comes from a port of its own.
		connections[r.RemoteAddr] = true
	}), func(server *httptest.Server) {
		server.TLS.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			defer mu.Unlock()
			names = append(names, hello.ServerName)
			return nil, nil
		}
	})
	transport := sourcehttp.Transport(sourcehttptest.To(server.Listener.Addr().String()), roots)
	if transport.Proxy != nil || transport.ForceAttemptHTTP2 || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() ||
		transport.Protocols.UnencryptedHTTP2() || !transport.DisableKeepAlives || !transport.DisableCompression ||
		transport.TLSClientConfig != nil {
		t.Fatal("the transport may speak HTTP/2, keep a connection, take a proxy or change certificate checks")
	}
	for range 3 {
		if status, err := ask(context.Background(), transport, host); err != nil || status != 200 {
			t.Fatalf("status %d, %v", status, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"HTTP/1.1", "HTTP/1.1", "HTTP/1.1"}; !slices.Equal(protocols, want) || len(connections) != 3 {
		t.Fatalf("the server was asked over %v on %d connections", protocols, len(connections))
	}
	if want := []string{host, host, host}; !slices.Equal(names, want) {
		t.Fatalf("the handshakes named %v", names)
	}
}

// A server whose certificate is not trusted, or is for another name, or that speaks no TLS later than 1.1,
// is never sent a request.
func TestTheCertificateAndTheTLSVersionAreChecked(t *testing.T) {
	asked := 0
	var mu sync.Mutex
	count := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked++
	})
	server, roots := served(t, count, nil)
	old, oldRoots := served(t, count, func(server *httptest.Server) {
		server.TLS.MinVersion, server.TLS.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
	})
	_, others := sourcehttptest.Identity(t, host)
	for name, given := range map[string]struct {
		server *httptest.Server
		roots  *x509.CertPool
		name   string
	}{
		"a certificate no root signed":   {server, others, host},
		"a certificate for another name": {server, roots, "invented-other.example"},
		"a server that stops at TLS 1.1": {old, oldRoots, host},
	} {
		transport := sourcehttp.Transport(sourcehttptest.To(given.server.Listener.Addr().String()), given.roots)
		if status, err := ask(context.Background(), transport, given.name); err == nil {
			t.Errorf("%s: the request was answered with %d", name, status)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 0 {
		t.Fatalf("%d requests reached a server", asked)
	}
}

// A handshake whose context ends before the server answers leaves no open socket behind.
func TestAHandshakeThatIsStoppedClosesItsSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan int64, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// The server takes the client's first message and never answers it. The copy ends when the client
		// closes its side.
		taken, _ := io.Copy(io.Discard, conn)
		closed <- taken
	}()
	_, roots := sourcehttptest.Identity(t, host)
	transport := sourcehttp.Transport(sourcehttptest.To(listener.Addr().String()), roots)
	// net/http gives a dial a context of its own, which it ends when no request wants the connection. The
	// dial is called here as net/http calls it, with a context that ends while the server is silent.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := transport.DialTLSContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err == nil {
		_ = conn.Close()
		t.Fatal("a handshake the server never answered gave a connection")
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("the dial returned %v after its context ended", waited)
	}
	select {
	case taken := <-closed:
		if taken == 0 {
			t.Fatal("the handshake never began")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the socket was left open")
	}
}

// net/http logs the first bytes a server sends on a connection where no request is waiting. A server that
// sends such bytes, after an answer or before a request, gets none of them into the log.
func TestWhatAServerSendsUnaskedIsNotLogged(t *testing.T) {
	const unasked = "HTTP/1.1 500 unasked " + bearer + "\r\nContent-Length: 0\r\n\r\n"
	certificate, roots := sourcehttptest.Identity(t, host)
	settings := &tls.Config{Certificates: []tls.Certificate{certificate}}
	// hungUp waits for a connection to be closed by the client. net/http writes its line before it closes
	// the connection, so by then the line is in the log or will never be.
	hungUp := func(t *testing.T, closed chan struct{}) {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the client left a connection open")
		}
	}

	t.Run("after an answer", func(t *testing.T) {
		kept := sourcehttptest.Capture(t)
		closed := make(chan struct{}, 1)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			// The answer and the bytes after it leave in one write, so they arrive together.
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}%s", unasked)
			_, _ = io.Copy(io.Discard, conn)
			closed <- struct{}{}
		}))
		server.TLS = settings.Clone()
		server.StartTLS()
		defer server.Close()
		transport := sourcehttp.Transport(sourcehttptest.To(server.Listener.Addr().String()), roots)
		if status, err := ask(context.Background(), transport, host); err != nil || status != 200 {
			t.Fatalf("status %d, %v", status, err)
		}
		hungUp(t, closed)
		if kept.Size() != 0 {
			t.Fatalf("%d bytes were logged", kept.Size())
		}
	})

	t.Run("before a request", func(t *testing.T) {
		// net/http logs these bytes only when they arrive before it has counted the request, which it
		// seldom loses to a server on loopback. TestAConnectionIsNotReadBeforeItsRequest shows why it
		// cannot lose here; this shows what a client makes of such a server.
		kept := sourcehttptest.Capture(t)
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
		transport := sourcehttp.Transport(sourcehttptest.To(listener.Addr().String()), roots)
		for range 20 {
			// What the server sent is read as its answer to the request.
			if status, _ := ask(context.Background(), transport, host); status != 500 {
				t.Fatalf("the request was answered with %d", status)
			}
			hungUp(t, closed)
		}
		if kept.Size() != 0 {
			t.Fatalf("%d bytes were logged", kept.Size())
		}
	})
}
