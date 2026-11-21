package sourcehttp

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A connection gives nothing a server sent until a write to it has begun, and gives it from then on.
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
	// Nothing takes the request yet, so the write below has begun and has sent nothing when the read
	// comes through: the gate opens when Write is called, not when it returns.
	wrote := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("GET"))
		wrote <- err
	}()
	select {
	case got := <-read:
		if got != "unasked" {
			t.Fatalf("read %q once the request was being written", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was read while the request was being written")
	}
	if _, err := io.ReadFull(far, make([]byte, 3)); err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
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
