package mcp_test

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// Rule one on a stuck database, over both transports. Each receipt start and finish of the gate waits no
// longer than the settle limit, which these tests shorten. A start that another connection's lock holds past
// it refuses the call, and no tool runs. A finish held past it releases nothing, and the receipt stays
// pending for the sweep of expired leases, which finishes it as interrupted.

// settleLimit stands in for cards.SettleLimit here. storeGivesUp is the store's own lock timeout: a call
// answered before it was ended by the gate's limit, not by the database.
const (
	settleLimit  = 250 * time.Millisecond
	storeGivesUp = 5 * time.Second
)

// hold takes a lock in a transaction of another connection, as a stuck writer holds one. release ends the
// transaction; it runs at the latest when the test ends.
func (m *mounted) hold(statement string, args ...any) (release func()) {
	m.t.Helper()
	conn, err := pgx.Connect(ctx, m.url)
	must(m.t, err)
	_, err = conn.Exec(ctx, "BEGIN")
	must(m.t, err)
	_, err = conn.Exec(ctx, statement, args...)
	must(m.t, err)
	released := false
	release = func() {
		if !released {
			released = true
			conn.Exec(ctx, "ROLLBACK")
			conn.Close(ctx)
		}
	}
	m.t.Cleanup(release)
	return release
}

// answered runs one call and gives how long it took to be answered. A call still unanswered long after the
// store itself would have given up fails the test, with the lock released so the call can end.
func answered(t *testing.T, release func(), do func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	began := time.Now()
	go func() {
		defer close(done)
		do()
	}()
	select {
	case <-done:
		return time.Since(began)
	case <-time.After(6 * storeGivesUp):
		release()
		<-done
		t.Fatal("the call was not answered while the lock was held")
	}
	return 0
}

// stuckStart holds the receipts table, so no receipt can be inserted.
const stuckStart = "LOCK TABLE receipts IN EXCLUSIVE MODE"

// stuckFinish holds the pending receipt of a tool's call as its source is read, after the receipt committed
// and before it can finish. It gives the lock's release.
func stuckFinish(m *mounted, tool string) func() {
	release := func() {}
	m.book.before = func(read string) {
		if read == tool {
			release = m.hold("SELECT 1 FROM receipts WHERE tool = $1 AND status = 'pending' FOR UPDATE", tool)
		}
	}
	return func() { release() }
}

// swept expires the one pending receipt's lease, as time does, and runs the sweep serve, mcp stdio and
// setup run at start. It checks the sweep finished it as interrupted.
func (m *mounted) swept() {
	m.t.Helper()
	pending := m.receipts("status = 'pending'")
	if len(pending) != 1 {
		m.t.Fatalf("pending receipts %+v", pending)
	}
	must(m.t, m.sql(`UPDATE receipts SET lease_expires_at = clock_timestamp(), finishing_lease = lease_token
		WHERE id = $1`, pending[0].ID))
	if n, err := m.store.AbandonExpired(ctx, 100); err != nil || n != 1 {
		m.t.Fatalf("the sweep finished %d (%v)", n, err)
	}
	if row := m.receipts("id = $1", pending[0].ID)[0]; row.label() != pending[0].Tool+" error 503" ||
		row.Error == nil || *row.Error != "interrupted" {
		m.t.Fatalf("the swept receipt %+v", row)
	}
}

func inLimit(elapsed time.Duration) bool { return elapsed >= settleLimit && elapsed < storeGivesUp }

func TestMountedStuckReceiptsOverHTTPS(t *testing.T) {
	account := map[string]any{"account_id": "acc-003"}
	headers := requestHeaders("tools/call", "get_account", bearer("read"))

	t.Run("a start", func(t *testing.T) {
		m := mount(t, cards.Selection{"acc-003": true}, nil)
		m.fixture()
		mcp.SetSettleLimit(m.gateway, settleLimit)
		release := m.hold(stuckStart)
		var response string
		var code int
		elapsed := answered(t, release, func() {
			answer := m.post(callBody(1, "get_account", account), headers)
			code, response = answer.Code, strings.TrimSpace(answer.Body.String())
		})
		release()
		if code != 503 || response != `{"error":"audit_unavailable; call was not executed"}` || !inLimit(elapsed) ||
			len(m.book.made()) != 0 || len(m.outer()) != 0 {
			t.Fatalf("a call whose receipt could not start: %d %s after %s, read %v, receipts %+v", code, response,
				elapsed, m.book.made(), m.outer())
		}
		// The store is free again, and the next call runs under its receipt.
		if response, _ := m.call(2, "get_account", account, bearer("read")); len(records(t, response)) == 0 {
			t.Fatal("no records after the lock was released")
		}
	})

	t.Run("a finish", func(t *testing.T) {
		m := mount(t, cards.Selection{"acc-003": true}, nil)
		m.fixture()
		mcp.SetSettleLimit(m.gateway, settleLimit)
		release := stuckFinish(m, "get_account")
		var response string
		var code int
		elapsed := answered(t, release, func() {
			answer := m.post(callBody(1, "get_account", account), headers)
			code, response = answer.Code, strings.TrimSpace(answer.Body.String())
		})
		release()
		if code != 503 || response != `{"error":"execution_or_audit_unavailable; no result released"}` || !inLimit(elapsed) ||
			m.count("SELECT count(*) FROM receipt_records") != 0 {
			t.Fatalf("a call whose receipt could not finish: %d %s after %s", code, response, elapsed)
		}
		m.swept()
	})
}

// stdioSession is mcp stdio's server on the mounted database, as the machine principal that holds bearer.
type stdioSession struct {
	in    *io.PipeWriter
	lines chan []byte
	done  chan error
}

func (m *mounted) stdio(raw string) *stdioSession {
	m.t.Helper()
	config, err := mcptools.Stdio(m.tools(nil), raw)
	must(m.t, err)
	mcp.SetStdioSettleLimit(&config, settleLimit)
	clientIn, serverIn := io.Pipe()
	serverOut, clientOut := io.Pipe()
	s := &stdioSession{in: serverIn, lines: make(chan []byte, 16), done: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(serverOut)
		for scanner.Scan() {
			s.lines <- append([]byte{}, scanner.Bytes()...)
		}
		close(s.lines)
	}()
	go func() {
		s.done <- mcp.ServeStdio(ctx, config, clientIn, clientOut)
		clientOut.Close()
	}()
	m.t.Cleanup(func() {
		serverIn.Close()
		<-s.done
	})
	opened := make(chan string, 1)
	go func() {
		opened <- s.ask(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	}()
	select {
	case line := <-opened:
		if !strings.Contains(line, `"result"`) {
			m.t.Fatalf("initialize: %s", line)
		}
	case <-time.After(6 * storeGivesUp):
		m.t.Fatal("initialize was not answered")
	}
	if _, err := io.WriteString(s.in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		m.t.Fatal(err)
	}
	return s
}

// ask sends one line and gives the server's next line as written, or "" when the session ended. The caller
// bounds how long it waits.
func (s *stdioSession) ask(line string) string {
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		return ""
	}
	return string(<-s.lines)
}

// call is a tool call's line.
func call(id int, tool string, arguments any) string {
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments}})
	return string(encoded)
}

// failure is the error message of an answer, "" for a result, or the whole line for anything else.
func failure(t *testing.T, line string) string {
	t.Helper()
	if line == "" {
		return "the session ended"
	}
	answer := decode(t, []byte(line))
	message, _ := at(answer, "error", "message").(string)
	if message == "" && at(answer, "result") == nil {
		return line
	}
	return message
}

func TestMountedStuckReceiptsOverStdio(t *testing.T) {
	account := map[string]any{"account_id": "acc-003"}

	t.Run("a start", func(t *testing.T) {
		m := mount(t, cards.Selection{"acc-003": true}, nil)
		m.fixture()
		s := m.stdio(bearer("read"))
		release := m.hold(stuckStart)
		var line string
		elapsed := answered(t, release, func() { line = s.ask(call(1, "get_account", account)) })
		release()
		if message := failure(t, line); message != "audit_unavailable; call was not executed" || !inLimit(elapsed) ||
			len(m.book.made()) != 0 || len(m.outer()) != 0 {
			t.Fatalf("a call whose receipt could not start: %q after %s, read %v, receipts %+v", message, elapsed,
				m.book.made(), m.outer())
		}
		// The store is free again, and the next call runs under its receipt.
		answered(t, release, func() { line = s.ask(call(2, "get_account", account)) })
		if message := failure(t, line); message != "" || len(m.book.made()) != 1 {
			t.Fatalf("the call after the lock was released: %q", message)
		}
	})

	t.Run("a finish", func(t *testing.T) {
		m := mount(t, cards.Selection{"acc-003": true}, nil)
		m.fixture()
		s := m.stdio(bearer("read"))
		release := stuckFinish(m, "get_account")
		var line string
		elapsed := answered(t, release, func() { line = s.ask(call(1, "get_account", account)) })
		release()
		if message := failure(t, line); message != "execution_or_audit_unavailable; no result released" || !inLimit(elapsed) ||
			m.count("SELECT count(*) FROM receipt_records") != 0 {
			t.Fatalf("a call whose receipt could not finish: %q after %s", message, elapsed)
		}
		m.swept()
	})
}
