package mcp

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stdioSession is ServeStdio on real pipes, under a context the test cancels, with its output read only when
// the test reads it.
type stdioSession struct {
	t        *testing.T
	receipts *MemoryRecorder
	fx       *fixture
	in       *os.File // the client's end of stdin
	out      *os.File // the client's end of stdout
	cancel   context.CancelFunc
	done     chan error
}

func newStdioSession(t *testing.T, change func(*StdioConfig)) *stdioSession {
	t.Helper()
	s := &stdioSession{t: t, receipts: &MemoryRecorder{}, done: make(chan error, 1)}
	s.fx = newFixture(t, s.receipts)
	expires := testWall + 3600
	machine := "fixture-machine-principal-bearer-value"
	config := StdioConfig{Token: machine, Resource: testResource, RateLimit: 60, WindowSeconds: 60,
		Verify:   verifierFor(map[string]*Token{machine: {Scopes: []string{"crm:read"}, ExpiresAt: &expires, Resource: testResource}}),
		Receipts: s.receipts, Run: s.fx.Run, Clock: func() float64 { return 100 }, Wall: func() float64 { return testWall }}
	if change != nil {
		change(&config)
	}
	serverIn, clientIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientOut, serverOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.in, s.out = clientIn, clientOut
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		s.done <- ServeStdio(ctx, config, serverIn, serverOut)
		serverOut.Close()
	}()
	t.Cleanup(func() {
		cancel()
		clientIn.Close()
		clientOut.Close()
	})
	return s
}

func (s *stdioSession) send(lines ...string) {
	s.t.Helper()
	for _, line := range lines {
		if _, err := io.WriteString(s.in, line+"\n"); err != nil {
			s.t.Fatal(err)
		}
	}
}

// stopped waits for ServeStdio to return.
func (s *stdioSession) stopped(within time.Duration) {
	s.t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		s.t.Fatalf("the server did not stop within %s", within)
	}
}

// noPending fails when any receipt is still pending.
func (s *stdioSession) noPending() {
	s.t.Helper()
	for _, row := range s.receipts.Read() {
		if row.Status == "pending" {
			s.t.Fatalf("a committed receipt was left pending: %+v", row)
		}
	}
}

// A stop that comes while a line's receipt is committed but its call is not yet pending waits for the line,
// so the receipt is finished, not left pending.
func TestStdioStopFinishesAReceiptCommittedAsItStops(t *testing.T) {
	committed, release := make(chan string, 1), make(chan struct{})
	s := newStdioSession(t, nil)
	s.receipts.AfterStart = func(id string) {
		committed <- id
		<-release
	}
	go io.Copy(io.Discard, s.out)
	s.send(legacyStart, legacyReady, call(2, "get_account", `{"account_id":"acc-001"}`))
	<-committed
	s.cancel()
	time.Sleep(200 * time.Millisecond)
	close(release)
	s.stopped(10 * time.Second)
	time.Sleep(200 * time.Millisecond)
	rows := s.receipts.Read()
	if len(rows) != 1 {
		t.Fatalf("%d receipts", len(rows))
	}
	s.noPending()
	if len(s.fx.ran()) != 0 {
		t.Fatalf("ran %v", s.fx.ran())
	}
}

// A stop waits for a line being admitted only as long as StopWait.
func TestStdioStopWaitIsBounded(t *testing.T) {
	committed, release := make(chan string, 1), make(chan struct{})
	defer close(release)
	s := newStdioSession(t, func(c *StdioConfig) { c.StopWait = 100 * time.Millisecond })
	s.receipts.AfterStart = func(id string) {
		committed <- id
		<-release
	}
	go io.Copy(io.Discard, s.out)
	s.send(call(2, "get_account", `{"account_id":"acc-001"}`))
	<-committed
	s.cancel()
	s.stopped(5 * time.Second)
}

// A client that closes its end of stdout ends the session even while its stdin stays open, and the call in
// flight gets a finished receipt.
func TestStdioClosedOutputEndsTheSession(t *testing.T) {
	s := newStdioSession(t, nil)
	s.out.Close()
	s.send(legacyStart, legacyReady, call(2, "get_account", `{"account_id":"acc-001"}`))
	s.stopped(10 * time.Second)
	s.noPending()
}

// A client that stops reading ends the session once a write has waited WriteTimeout, even while its stdin
// stays open, and no receipt is left pending.
func TestStdioClientThatStopsReadingEndsTheSession(t *testing.T) {
	s := newStdioSession(t, func(c *StdioConfig) { c.WriteTimeout = 200 * time.Millisecond })
	s.send(legacyStart, legacyReady)
	go func() {
		for i := 2; i < 400; i++ {
			if _, err := io.WriteString(s.in, `{"jsonrpc":"2.0","id":`+strconv.Itoa(i)+`,"method":"tools/list"}`+"\n"); err != nil {
				return
			}
		}
		io.WriteString(s.in, call(400, "get_account", `{"account_id":"acc-001"}`)+"\n")
	}()
	s.stopped(20 * time.Second)
	s.noPending()
}

// The 65,536-byte limit is the line as written, as HTTP measures the body: a valid call padded with spaces
// past it is refused with its receipt and runs nothing.
func TestStdioPaddedCallIsMeasuredAsWritten(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"}}` +
		strings.Repeat(" ", 70000) + `}`)
	if refused := h.answer(2); path(refused, "error", "data", "reason") != "request_too_large" {
		t.Fatalf("%.300v", refused)
	}
	rows := h.receipts.Read()
	if len(h.fx.ran()) != 0 || len(rows) != 1 || rows[0].Tool != "get_account" || rows[0].Status != "error" || *rows[0].HTTPStatus != 413 {
		t.Fatalf("ran %v receipts %+v", h.fx.ran(), rows)
	}
}

// A tool call past the 4 MiB frame gets one error receipt under its tool name, found in the line's start; a
// long line that is no call gets none. The next line is served.
func TestStdioOversizedCallKeepsAReceipt(t *testing.T) {
	h := newStdio(t, []string{"crm:read"}, nil)
	h.send(legacyStart, legacyReady)
	h.answer(1)
	h.send(call(2, "search_accounts", `{"query":"`+strings.Repeat("a", 5<<20)+`"}`), strings.Repeat(" ", 5<<20)+"{}")
	if huge := h.next(); path(huge, "error", "data", "reason") != "request_too_large" {
		t.Fatalf("%.300v", huge)
	}
	if spaces := h.next(); path(spaces, "error", "data", "reason") != "request_too_large" {
		t.Fatalf("%.300v", spaces)
	}
	rows := h.receipts.Read()
	if len(h.fx.ran()) != 0 || len(rows) != 1 || rows[0].Tool != "search_accounts" || rows[0].Status != "error" ||
		*rows[0].HTTPStatus != 413 || path(rows[0].Arguments, "unrecorded") != "the line exceeds 4194304 bytes" {
		t.Fatalf("ran %v receipts %+v", h.fx.ran(), rows)
	}
	h.send(call(3, "get_account", `{"account_id":"acc-001"}`))
	if served := h.answer(3); path(served, "result", "isError") != false {
		t.Fatalf("after an oversized call: %v", served)
	}
}

// prefixCall finds the method and tool name in a line's start, in either order, and stops at a value the
// start cuts off.
func TestPrefixCall(t *testing.T) {
	for _, c := range []struct {
		start, method, name string
		named               bool
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"q":"aaa`, "tools/call", "get_account", true},
		{`{"params":{"arguments":{"x":1},"name":"read_call_notes"},"method":"tools/call","id":"aaa`, "tools/call", "read_call_notes", true},
		{`{"method":"tools/call","params":{"arguments":{"q":"aaaa`, "tools/call", "", false},
		{`{"method":"tools/li`, "", "", false},
		{`[{"method":"tools/call"`, "", "", false},
	} {
		method, name, named := prefixCall([]byte(c.start))
		if method != c.method || name != c.name || named != c.named {
			t.Errorf("%s: %q %q %v", c.start, method, name, named)
		}
	}
}
