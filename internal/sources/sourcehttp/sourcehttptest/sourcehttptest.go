// Package sourcehttptest is what a test needs to run a source reader's own transport against a TLS server on
// loopback: a certificate for the name the reader asks for, and a dial that goes to the test server. Nothing
// leaves the machine.
package sourcehttptest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
)

// Identity is a certificate made for one host name, and the roots that trust it.
func Identity(t testing.TB, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
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

// To dials the test server at address, whatever address is asked for.
func To(address string) sourcehttp.Dial {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

// Counting is a dial that adds to taken every byte read from a connection it opens: what a client took from
// the network, whatever it then did with it.
func Counting(open sourcehttp.Dial, taken *atomic.Int64) sourcehttp.Dial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := open(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return counted{conn, taken}, nil
	}
}

type counted struct {
	net.Conn
	taken *atomic.Int64
}

func (c counted) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.taken.Add(int64(n))
	return n, err
}

// Flood is a TLS server on loopback that answers every request with a JSON body of no stated length: it
// opens with prefix and goes on, in pieces, for size bytes or until the client hangs up. The transport given
// with it is net/http's own, with connections it keeps, and reaches that server whatever host is asked for.
// taken counts the bytes the client read from the network, and stopped is closed when the server has
// finished its first answer.
func Flood(t testing.TB, prefix string, size int) (transport *http.Transport, taken *atomic.Int64, stopped <-chan struct{}) {
	t.Helper()
	ended, once := make(chan struct{}), sync.Once{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer once.Do(func() { close(ended) })
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(prefix))
		piece := bytes.Repeat([]byte("x"), 32<<10)
		for written := 0; written < size; written += len(piece) {
			if _, err := w.Write(piece); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	taken = &atomic.Int64{}
	transport = &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
		DialContext:     Counting(To(server.Listener.Addr().String()), taken),
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport, taken, ended
}

// Logged keeps what the process logs while a test runs.
type Logged struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (l *Logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

// Size is how many bytes were logged.
func (l *Logged) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Len()
}

// Capture sends the standard logger to a Logged until the test ends.
func Capture(t testing.TB) *Logged {
	kept := &Logged{}
	log.SetOutput(kept)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return kept
}
