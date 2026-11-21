package salesforce

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"sync"
	"time"
)

// connectTime is how long a connection may take to open, and then how long its TLS handshake may take.
const connectTime = 3 * time.Second

// dial opens the TCP connection to an address.
type dial func(ctx context.Context, network, address string) (net.Conn, error)

// direct dials the instance itself.
var direct dial = (&net.Dialer{Timeout: connectTime}).DialContext

// ownTransport is the transport of a Reader that was given none. It checks the instance's certificate
// against roots, where nil is the system's, takes no proxy, and asks for no compression.
//
// It speaks HTTP/1.1 only. With HTTP/2 debugging switched on through GODEBUG, net/http logs the value of
// every header it sends, Authorization included. Its HTTP/1.1 code has no such switch, so the bearer cannot
// reach the process log that way.
//
// The one line net/http's HTTP/1.1 code logs by itself holds the first bytes a server sent on a connection
// where no request was waiting. Every request has a connection of its own, so none is left open after an
// answer, and a new connection is not read until its request is written to it. That line is never written.
func ownTransport(open dial, roots *x509.CertPool) *http.Transport {
	only := new(http.Protocols)
	only.SetHTTP1(true)
	return &http.Transport{
		Protocols:          only,
		DisableKeepAlives:  true,
		DisableCompression: true,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			plain, err := open(ctx, network, address)
			if err != nil {
				return nil, err
			}
			secured := tls.Client(plain, &tls.Config{ServerName: host, RootCAs: roots, NextProtos: []string{"http/1.1"}})
			limited, cancel := context.WithTimeout(ctx, connectTime)
			defer cancel()
			if err := secured.HandshakeContext(limited); err != nil {
				_ = plain.Close()
				return nil, err
			}
			return newAskedFirst(secured), nil
		},
	}
}

// askedFirst is a connection that is read only once something has been written to it.
type askedFirst struct {
	net.Conn
	asked, closed chan struct{}
	write, close  sync.Once
}

func newAskedFirst(conn net.Conn) *askedFirst {
	return &askedFirst{Conn: conn, asked: make(chan struct{}), closed: make(chan struct{})}
}

func (c *askedFirst) Read(p []byte) (int, error) {
	select {
	case <-c.asked:
	case <-c.closed:
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

func (c *askedFirst) Write(p []byte) (int, error) {
	c.write.Do(func() { close(c.asked) })
	return c.Conn.Write(p)
}

func (c *askedFirst) Close() error {
	c.close.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
