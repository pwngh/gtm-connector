// Package sourcehttp is the HTTPS transport a source reader makes for itself when it is given none. Both
// the Salesforce and the Slack reader send a bearer through it, so it is built to put nothing of a request
// or an answer in the process log.
package sourcehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"sync"
	"time"
)

// ConnectTime is how long a connection may take to open, and then how long its TLS handshake may take.
const ConnectTime = 3 * time.Second

// Dial opens the TCP connection to an address.
type Dial func(ctx context.Context, network, address string) (net.Conn, error)

// Direct dials the address itself.
var Direct Dial = (&net.Dialer{Timeout: ConnectTime}).DialContext

// Transport is a transport that checks the server's certificate against roots, where nil is the system's,
// for the name the request is sent to, with TLS 1.2 or later. It takes no proxy and asks for no
// compression.
//
// It speaks HTTP/1.1 only. With HTTP/2 debugging switched on through GODEBUG, net/http logs the value of
// every header it sends, Authorization included. Its HTTP/1.1 code has no such switch, so a bearer cannot
// reach the process log that way.
//
// The one line net/http's HTTP/1.1 code logs by itself holds the first bytes a server sent on a connection
// where no request was waiting. Every request has a connection of its own, so none is left open after an
// answer, and a new connection is not read until its request is being written to it. That line is never
// written.
func Transport(open Dial, roots *x509.CertPool) *http.Transport {
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
			secured := tls.Client(plain, &tls.Config{ServerName: host, RootCAs: roots,
				NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
			limited, cancel := context.WithTimeout(ctx, ConnectTime)
			defer cancel()
			// A handshake that fails, or whose context ends first, leaves no open socket.
			if err := secured.HandshakeContext(limited); err != nil {
				_ = plain.Close()
				return nil, err
			}
			return newAskedFirst(secured), nil
		},
	}
}

// askedFirst is a connection whose reads wait for its first write.
//
// The gate opens when Write is first called, before that call sends anything, and stays open. net/http
// counts a request as waiting before it starts to write it, so by the time its read loop sees a byte the
// request is counted, and the bytes are read as the answer to it. Closing the connection ends a read that
// is still waiting at the gate.
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
