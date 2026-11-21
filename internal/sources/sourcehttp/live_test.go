package sourcehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// Direct, and every transport made with it, dials nothing in a process whose main did not set Live: a
// listener on loopback is never reached, and a request fails before its host is looked up.
func TestNothingIsDialledUnlessTheProcessIsLive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := Direct(context.Background(), "tcp", listener.Addr().String()); !errors.Is(err, ErrNotLive) {
		t.Fatalf("Direct: %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://"+listener.Addr().String()+"/", nil)
	if _, err := Transport(Direct, nil).RoundTrip(request); !errors.Is(err, ErrNotLive) {
		t.Fatalf("the transport: %v", err)
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(50 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("a connection reached the listener")
	}
}
