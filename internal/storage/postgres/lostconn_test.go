package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// A receipt's start or finish on a connection the network loses: the rollback after a failed transaction
// ends, and a commit whose answer is lost is read back. A lossy proxy stands between the store and the
// database and stops passing bytes on the connections it holds while their sockets stay open.

// lossy is a TCP proxy to the test database. stop makes the connections it holds now stop passing bytes one
// way or both; connections made later pass bytes as usual.
type lossy struct {
	listener net.Listener
	target   string

	mu     sync.Mutex
	links  []*link
	closed bool
}

// link is one proxied connection. Bytes read on a stopped way are held, never written on.
type link struct {
	mu      sync.Mutex
	moved   *sync.Cond
	stopped [2]bool // toServer, toClient
	closed  bool
	conns   [2]net.Conn
}

const (
	toServer = iota
	toClient
)

func newLossy(t *testing.T, databaseURL string) (*lossy, string) {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	ok(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	ok(t, err)
	p := &lossy{listener: listener, target: parsed.Host}
	go p.serve()
	parsed.Host = listener.Addr().String()
	return p, parsed.String()
}

func (p *lossy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			client.Close()
			continue
		}
		l := &link{conns: [2]net.Conn{client, server}}
		l.moved = sync.NewCond(&l.mu)
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			l.close()
			return
		}
		p.links = append(p.links, l)
		p.mu.Unlock()
		go l.pipe(server, client, toServer)
		go l.pipe(client, server, toClient)
	}
}

func (l *link) pipe(to, from net.Conn, way int) {
	defer l.close()
	buffer := make([]byte, 32<<10)
	for {
		n, err := from.Read(buffer)
		if n > 0 {
			l.mu.Lock()
			for l.stopped[way] && !l.closed {
				l.moved.Wait()
			}
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return
			}
			if _, err := to.Write(buffer[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (l *link) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.conns[0].Close()
		l.conns[1].Close()
		l.moved.Broadcast()
	}
}

// stop stops the connections held now from passing bytes on the given ways.
func (p *lossy) stop(ways ...int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		l.mu.Lock()
		for _, way := range ways {
			l.stopped[way] = true
		}
		l.mu.Unlock()
	}
}

func (p *lossy) close() {
	p.mu.Lock()
	p.closed = true
	links := p.links
	p.mu.Unlock()
	p.listener.Close()
	for _, l := range links {
		l.close()
	}
}

// through opens a store on the fixture's database through a lossy proxy. The proxy closes, ending every
// connection it holds, before the store does.
func through(t *testing.T, f *postgrestest.Fixture) (*postgres.Store, *lossy) {
	t.Helper()
	p, proxied := newLossy(t, f.URL)
	store, err := postgres.Open(ctx, proxied)
	if err != nil {
		p.close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.close()
		store.Close()
	})
	return store, p
}

// within runs call and gives its error. A call still running after a minute fails the test: on a lost
// connection it would never return.
func within(t *testing.T, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Minute):
		t.Fatal("the call did not return on a lost connection")
	}
	return nil
}

// A finish whose transaction fails and whose connection is then lost, its socket still open, returns: the
// rollback waits only so long, and the receipt stays pending.
func TestARollbackOnALostConnectionEnds(t *testing.T) {
	f := postgrestest.New(t)
	store, p := through(t, f)
	read := cliRead(t, f, "lost-rollback", "", "anonymous")
	postgres.SetCrash(store, func(point string) error {
		if point == "read:locked" {
			p.stop(toServer, toClient)
			return errors.New("the transaction failed")
		}
		return nil
	})
	err := within(t, func() error {
		_, err := store.FinishRead(ctx, read.ID, read.LeaseToken, records("lost"), 1)
		return err
	})
	if err == nil {
		t.Fatal("a finish that failed reported success")
	}
	if stored, err := f.Store.Receipt(ctx, f.Workspace.ID, read.ID); err != nil || stored.Status != "pending" {
		t.Fatalf("the receipt: %+v (%v)", stored, err)
	}
}

// A start, and then a finish, whose commit reaches the database but whose answer is lost as the caller's
// time runs out: each reads back what it wrote and reports it as committed, as it is.
func TestACommitWhoseAnswerIsLostIsReadBack(t *testing.T) {
	f := postgrestest.New(t)
	store, p := through(t, f)
	losing := func(at string) {
		postgres.SetCrash(store, func(point string) error {
			if point == at {
				p.stop(toClient)
			}
			return nil
		})
	}

	losing("start:inserted")
	var started postgres.Receipt
	err := within(t, func() error {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var err error
		started, err = store.StartReceipt(bounded, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
			OperationID: "lost-start", PrincipalID: f.Reader.ID, TokenID: "anonymous", Tool: "get_account",
			Scope: "crm:read", AccountRef: postgrestest.Account, ConnectionID: f.Connection.ID,
			Arguments: json.RawMessage(`{"account_id":"acc-003"}`), RequestID: json.RawMessage(`1`), Lease: time.Minute})
		return err
	})
	stored, readErr := f.Store.Receipt(ctx, f.Workspace.ID, started.ID)
	if err != nil || readErr != nil || stored.Status != "pending" || started.LeaseToken == "" {
		t.Fatalf("a start whose commit answer was lost: %v; stored %+v (%v)", err, stored, readErr)
	}

	losing("read:finished")
	var finished postgres.Receipt
	err = within(t, func() error {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var err error
		finished, err = store.FinishRead(bounded, started.ID, started.LeaseToken, records("lost"), 1)
		return err
	})
	if err != nil || finished.Status != "ok" || len(finished.Rows) != 2 {
		t.Fatalf("a finish whose commit answer was lost: %+v (%v)", finished, err)
	}
}

// A brief's child read waits on its parent's row as it starts. Behind a lock on that row it gives up at the
// store's lock timeout, writes nothing, and starts once the lock is gone.
func TestAChildStartGivesUpOnItsLockedParent(t *testing.T) {
	f := postgrestest.New(t)
	postgres.SetPublishTimeouts(f.Store, 200*time.Millisecond, time.Minute)
	brief := parent(t, f, "locked-parent", time.Minute)
	holder := session(t, f.URL)
	_, err := holder.Exec(ctx, "BEGIN")
	ok(t, err)
	_, err = holder.Exec(ctx, "SELECT 1 FROM receipts WHERE id = $1 FOR UPDATE", brief.ID)
	ok(t, err)

	err = within(t, func() error {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err := f.Store.StartChildRead(bounded, childStart(f, brief, 1, time.Minute))
		return err
	})
	if code(err) != "55P03" {
		t.Fatalf("a child start behind its locked parent: %v", err)
	}
	if n := count(t, f, "SELECT count(*) FROM receipts WHERE parent_id = $1", brief.ID); n != 0 {
		t.Fatalf("%d children written", n)
	}
	_, err = holder.Exec(ctx, "ROLLBACK")
	ok(t, err)
	child(t, f, brief, 1, time.Minute)
}

// Every pooled connection carries the store's limits on a lost connection: the server ends a session idle
// in a transaction, and both ends send TCP keepalives. A transaction whose connection the network loses, its
// socket still open, is ended by the server once it has waited past the limit, and its row lock is free.
func TestTheServerEndsATransactionLeftIdle(t *testing.T) {
	f := postgrestest.New(t)
	store, _ := through(t, f)
	conn, err := postgres.Pool(store).Acquire(ctx)
	ok(t, err)
	for name, want := range map[string]string{"idle_in_transaction_session_timeout": "30s",
		"tcp_keepalives_idle": "30", "tcp_keepalives_interval": "10", "tcp_keepalives_count": "3"} {
		var got string
		ok(t, conn.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&got))
		if got != want {
			t.Errorf("%s is %s, not %s", name, got, want)
		}
	}
	raw, err := conn.Conn().PgConn().Conn().(*net.TCPConn).SyscallConn()
	ok(t, err)
	ok(t, raw.Control(func(fd uintptr) {
		// SO_KEEPALIVE reads as any value but 0 when it is on.
		if on, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE); err != nil || on == 0 {
			t.Errorf("the connector's keepalives are off (%v)", err)
		}
		for name, option := range map[string][3]int{"TCP_KEEPINTVL": {unix.IPPROTO_TCP, unix.TCP_KEEPINTVL, 10},
			"TCP_KEEPCNT": {unix.IPPROTO_TCP, unix.TCP_KEEPCNT, 3}} {
			if got, err := unix.GetsockoptInt(int(fd), option[0], option[1]); err != nil || got != option[2] {
				t.Errorf("the connector's %s is %d (%v), not %d", name, got, err, option[2])
			}
		}
	}))
	conn.Release()

	// A shorter limit, so the test does not wait half a minute.
	p, proxied := newLossy(t, f.URL)
	idle, err := postgres.OpenIdle(ctx, proxied, 500*time.Millisecond)
	ok(t, err)
	t.Cleanup(func() {
		p.close()
		idle.Close()
	})
	held, err := postgres.Pool(idle).Acquire(ctx)
	ok(t, err)
	lost := held.Hijack()
	_, err = lost.Exec(ctx, "BEGIN")
	ok(t, err)
	_, err = lost.Exec(ctx, "SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE", f.Workspace.ID)
	ok(t, err)
	var pid int
	ok(t, lost.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	p.stop(toServer, toClient)

	other := session(t, f.URL)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var sessions int
		ok(t, other.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1", pid).Scan(&sessions))
		if sessions == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server kept the lost transaction's session")
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, err = other.Exec(ctx, "BEGIN")
	ok(t, err)
	_, err = other.Exec(ctx, "SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE NOWAIT", f.Workspace.ID)
	if err != nil {
		t.Fatalf("the lost transaction's lock is still held: %v", err)
	}
	_, err = other.Exec(ctx, "ROLLBACK")
	ok(t, err)
	p.close()
	closing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_ = lost.Close(closing)
}
