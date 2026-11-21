package cards

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The publication and every step that settles a receipt run on a context the caller's cancel does not end
// and a limit does. No receipt is left pending by a cancel, a store that does not answer, a lost
// acknowledgement or a source that panics.

// short is the limit these tests give the maker in place of SettleLimit.
const short = 200 * time.Millisecond

// stalled is a store that does not answer, as a database behind a lock does: its publication, or its
// finishing of a receipt, waits until the context it was given ends. With refusal set its publication fails
// at once with that error instead.
type stalled struct {
	*watched
	publish, finish bool
	refusal         error
}

func (s *stalled) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	switch {
	case s.refusal != nil:
		return receipts.Card{}, s.refusal
	case s.publish:
		<-ctx.Done()
		return receipts.Card{}, fmt.Errorf("no answer from host=db.internal.example: %w", ctx.Err())
	}
	return s.watched.Publish(ctx, p)
}

func (s *stalled) Finish(ctx context.Context, t receipts.Ticket, o receipts.Outcome) (receipts.Receipt, error) {
	if s.finish {
		<-ctx.Done()
		return receipts.Receipt{}, ctx.Err()
	}
	return s.watched.Finish(ctx, t, o)
}

// makes are the two ways a card is made, each under an operation named for the test.
var makes = map[string]func(ctx context.Context, m *Maker, w *world, operation string) (*Sealed, error){
	"brief": func(ctx context.Context, m *Maker, w *world, operation string) (*Sealed, error) {
		return m.Brief(ctx, w.brief(operation))
	},
	"prepare": func(ctx context.Context, m *Maker, w *world, operation string) (*Sealed, error) {
		return m.Prepare(ctx, w.prepare(operation))
	},
}

// sealOf gives the receipt that would seal the card: a brief's save_brief receipt, or a prepared brief's own
// call.
func (w *world) sealOf(kind, operation string) (receipts.Receipt, error) {
	if kind == "brief" {
		operation += "/seal"
	}
	return w.store.ByOperation(context.Background(), w.workspace, operation)
}

// failedSeal fails the test unless the card's seal finished as the failure of its kind, naming no card.
func (w *world) failedSeal(t *testing.T, kind, operation string) {
	t.Helper()
	status, code := 200, "request_failed"
	if kind == "brief" {
		status, code = 500, "brief_save_failed"
	}
	seal, err := w.sealOf(kind, operation)
	if err != nil || seal.Status != "error" || *seal.HTTPStatus != status || *seal.Error != code || len(seal.Rows) != 0 {
		t.Fatalf("the seal: %+v (%v)", seal, err)
	}
}

// A publication that has not finished by the limit gives one fixed error, whatever the store said of its
// wait. The seal finishes as an error and no card is published. The store's own timeout gives the same.
func TestAPublicationPastTheLimitGivesAFixedError(t *testing.T) {
	for name, fresh := range worlds(t) {
		for kind, run := range makes {
			for state, store := range map[string]stalled{
				"the store does not answer": {publish: true},
				"the store gave up":         {refusal: fmt.Errorf("%w: waiting on host=db.internal.example", receipts.ErrTimeout)},
			} {
				t.Run(name+"/"+kind+"/"+state, func(t *testing.T) {
					w := fresh(t)
					store.watched = &watched{Store: w.store}
					m := w.maker(t, &store, demoBook(t))
					m.limit = short
					sealed, err := within(t, func() (*Sealed, error) { return run(context.Background(), m, w, "late") })
					if sealed != nil || err != ErrNotPublished {
						t.Fatalf("a card: %t; %v", sealed != nil, err)
					}
					w.failedSeal(t, kind, "late")
					w.finishedOK(t, map[string]string{"brief": "late/%d", "prepare": "late#%d"}[kind])
				})
			}
		}
	}
}

// A step that settles a receipt is given up at the limit too: the maker returns the publication's own
// error, and does not wait for a store that will not finish the seal.
func TestASettlingStepPastTheLimitIsGivenUp(t *testing.T) {
	refused := errors.New("the store refused the card")
	for name, fresh := range worlds(t) {
		for kind, run := range makes {
			t.Run(name+"/"+kind, func(t *testing.T) {
				w := fresh(t)
				store := &stalled{watched: &watched{Store: w.store}, finish: true, refusal: refused}
				m := w.maker(t, store, demoBook(t))
				m.limit = short
				sealed, err := within(t, func() (*Sealed, error) { return run(context.Background(), m, w, "unsettled") })
				if sealed != nil || !errors.Is(err, refused) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
			})
		}
	}
}

// session opens another connection to the world's PostgreSQL database, closed when the test ends.
func (w *world) session(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), w.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// hold takes a lock in a transaction of another session and keeps it until the test ends.
func (w *world) hold(t *testing.T, statement string, arguments ...any) {
	t.Helper()
	conn := w.session(t)
	if _, err := conn.Exec(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

// onceBlocked runs then once some session waits for a lock on table: the store's statement has reached the
// lock another session holds. It gives up, with a test error, after ten seconds.
func (w *world) onceBlocked(t *testing.T, table string, then func()) {
	t.Helper()
	conn := w.session(t)
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			var waiting int
			err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks
				WHERE relation = to_regclass($1) AND NOT granted`, table).Scan(&waiting)
			if err != nil {
				t.Errorf("looking for a session that waits on %s: %v", table, err)
				return
			}
			if waiting > 0 {
				then()
				return
			}
		}
		t.Errorf("no session waited on %s", table)
	}()
}

func postgresWorld(t *testing.T) *world { return worlds(t)["postgres"](t) }

// On PostgreSQL, a publication that waits on a lock another session holds is given up at the limit: the
// reader's grant is locked, which the publication checks under a share lock. The seal, which that session
// does not hold, finishes as an error, and no card is stored.
func TestAPublicationBehindALockIsGivenUpOnPostgreSQL(t *testing.T) {
	for kind, run := range makes {
		t.Run(kind, func(t *testing.T) {
			w := postgresWorld(t)
			w.hold(t, "SELECT 1 FROM account_grants WHERE principal_id = $1 FOR UPDATE", w.principal)
			store := &watched{Store: w.store}
			m := w.maker(t, store, demoBook(t))
			m.limit = short
			sealed, err := within(t, func() (*Sealed, error) { return run(context.Background(), m, w, "locked") })
			if sealed != nil || err != ErrNotPublished || store.published != 1 {
				t.Fatalf("a card: %t; %v; %d publications", sealed != nil, err, store.published)
			}
			w.failedSeal(t, kind, "locked")
			var stored int
			if err := w.session(t).QueryRow(context.Background(), "SELECT count(*) FROM cards").Scan(&stored); err != nil ||
				stored != 0 {
				t.Fatalf("%d cards stored (%v)", stored, err)
			}
		})
	}
}

// interrupted is a store that runs a step of its own around one call, the n-th FinishRead or the seal's
// start: before runs before the call reaches the store, and after gives what the maker is told of the call,
// in place of the store's answer.
type interrupted struct {
	*watched
	read   int // the FinishRead to interrupt, from 1; 0 for none
	seal   bool
	before func()
	after  func(receipts.Receipt, error) (receipts.Receipt, error)
	reads  int
}

func (i *interrupted) around(call func() (receipts.Receipt, error)) (receipts.Receipt, error) {
	if i.before != nil {
		i.before()
	}
	r, err := call()
	if i.after != nil {
		return i.after(r, err)
	}
	return r, err
}

func (i *interrupted) FinishRead(ctx context.Context, ticket receipts.Ticket, versions []receipts.RecordVersion,
	elapsedMS float64) (receipts.Receipt, error) {
	call := func() (receipts.Receipt, error) { return i.watched.FinishRead(ctx, ticket, versions, elapsedMS) }
	if i.reads++; i.reads != i.read {
		return call()
	}
	return i.around(call)
}

func (i *interrupted) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	call := func() (receipts.Receipt, error) { return i.watched.Start(ctx, s) }
	if !i.seal || s.Tool != receipts.SaveBrief {
		return call()
	}
	// Only the maker's first start of the seal is interrupted, not the one that takes it back.
	i.seal = false
	return i.around(call)
}

// lost makes a call's acknowledgement go missing as the caller leaves: the store did the work, and the maker
// is told only that its context was cancelled.
func lost(leave func()) func(receipts.Receipt, error) (receipts.Receipt, error) {
	return func(receipts.Receipt, error) (receipts.Receipt, error) {
		leave()
		return receipts.Receipt{}, context.Canceled
	}
}

// A caller who leaves while a read's versions are being bound leaves no read pending. On PostgreSQL the
// binding waits on a lock another session holds on the record versions, the cancel ends it there with
// nothing bound, and the read finishes as an error, which needs no lock on that table.
func TestACancelDuringFinishReadLeavesNoReadPendingOnPostgreSQL(t *testing.T) {
	for kind, run := range makes {
		t.Run(kind, func(t *testing.T) {
			w := postgresWorld(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &interrupted{watched: &watched{Store: w.store}, read: 2}
			store.before = func() {
				w.hold(t, "LOCK TABLE source_records IN SHARE ROW EXCLUSIVE MODE")
				w.onceBlocked(t, "source_records", cancel)
			}
			m := w.maker(t, store, demoBook(t))
			sealed, err := within(t, func() (*Sealed, error) { return run(ctx, m, w, "unbound") })
			if sealed != nil || !errors.Is(err, context.Canceled) || store.published != 0 {
				t.Fatalf("a card: %t; %v; %d publications", sealed != nil, err, store.published)
			}
			operation := map[string]string{"brief": "unbound/2", "prepare": "unbound#2"}[kind]
			read, err := w.store.ByOperation(context.Background(), w.workspace, operation)
			if err != nil || read.Status != "error" || *read.HTTPStatus != 200 || *read.Error != "request_failed" ||
				len(read.Rows) != 0 {
				t.Fatalf("the read: %+v (%v)", read, err)
			}
			if w.total() != 2 {
				t.Fatalf("the reads: %v", w.reads)
			}
			if kind == "brief" {
				if seal, err := w.sealOf(kind, "unbound"); !errors.Is(err, receipts.ErrNotFound) {
					t.Fatalf("a seal was started: %+v (%v)", seal, err)
				}
			} else {
				w.failedSeal(t, kind, "unbound")
			}
		})
	}
}

// A read whose versions were bound, with the acknowledgement lost as the caller left, stands finished ok.
// The caller has gone, so no card is made and no seal started.
func TestAFinishedReadWhoseAcknowledgementWasLostStands(t *testing.T) {
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			w := fresh(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &interrupted{watched: &watched{Store: w.store}, read: 2, after: lost(cancel)}
			sealed, err := w.maker(t, store, demoBook(t)).Brief(ctx, w.brief("bound"))
			if sealed != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrBriefing) || store.published != 0 {
				t.Fatalf("a card: %t; %v; %d publications", sealed != nil, err, store.published)
			}
			read, err := w.store.ByOperation(context.Background(), w.workspace, "bound/2")
			if err != nil || read.Status != "ok" || len(read.Rows) == 0 {
				t.Fatalf("the read: %+v (%v)", read, err)
			}
			if seal, err := w.sealOf("brief", "bound"); !errors.Is(err, receipts.ErrNotFound) {
				t.Fatalf("a seal was started: %+v (%v)", seal, err)
			}
		})
	}
}

// A seal whose start committed, with the acknowledgement lost as the caller left, is not left pending: the
// maker finds it by its operation, takes it, and finishes it as the save failure.
func TestASealWhoseStartWasCancelledIsFinished(t *testing.T) {
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			w := fresh(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &interrupted{watched: &watched{Store: w.store}, seal: true, after: lost(cancel)}
			sealed, err := w.maker(t, store, demoBook(t)).Brief(ctx, w.brief("unsealed"))
			if sealed != nil || !errors.Is(err, context.Canceled) || store.published != 0 {
				t.Fatalf("a card: %t; %v; %d publications", sealed != nil, err, store.published)
			}
			w.failedSeal(t, "brief", "unsealed")
			w.finishedOK(t, "unsealed/%d")
		})
	}
}

// On PostgreSQL, a cancel while the seal's start waits on a lock another session holds on the receipts ends
// the insert there. Nothing committed, so the maker's lookup finds no seal and starts none to fail.
func TestACancelDuringSealStartLeavesNoSealOnPostgreSQL(t *testing.T) {
	w := postgresWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &interrupted{watched: &watched{Store: w.store}, seal: true}
	store.before = func() {
		w.hold(t, "LOCK TABLE receipts IN SHARE ROW EXCLUSIVE MODE")
		w.onceBlocked(t, "receipts", cancel)
	}
	m := w.maker(t, store, demoBook(t))
	sealed, err := within(t, func() (*Sealed, error) { return m.Brief(ctx, w.brief("unstarted")) })
	if sealed != nil || !errors.Is(err, context.Canceled) || store.published != 0 {
		t.Fatalf("a card: %t; %v; %d publications", sealed != nil, err, store.published)
	}
	if seal, err := w.sealOf("brief", "unstarted"); !errors.Is(err, receipts.ErrNotFound) {
		t.Fatalf("a seal was left: %+v (%v)", seal, err)
	}
	w.finishedOK(t, "unstarted/%d")
}

// unacknowledged is a store whose first publication commits and then reports an error, as a connection that
// drops after the commit does.
type unacknowledged struct {
	*watched
	dropped bool
}

func (u *unacknowledged) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	card, err := u.watched.Publish(ctx, p)
	if err != nil || u.dropped {
		return card, err
	}
	u.dropped = true
	return receipts.Card{}, errors.New("the connection to host=db.internal.example was reset")
}

// A card that the store committed is the caller's, even when the publication reported an error: the seal
// stands finished ok, so the maker asks for the card again and returns it.
func TestACommittedCardWhoseAcknowledgementWasLostIsReturned(t *testing.T) {
	for name, fresh := range worlds(t) {
		for kind, run := range makes {
			t.Run(name+"/"+kind, func(t *testing.T) {
				w := fresh(t)
				store := &unacknowledged{watched: &watched{Store: w.store}}
				sealed, err := run(context.Background(), w.maker(t, store, demoBook(t)), w, "committed")
				if err != nil || sealed == nil || sealed.Seal.Status != "ok" || len(sealed.Payload) == 0 {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				seal, err := w.sealOf(kind, "committed")
				if err != nil || seal.Status != "ok" || seal.AuditID != sealed.Seal.AuditID || len(seal.Rows) != 1 {
					t.Fatalf("the seal: %+v (%v)", seal, err)
				}
			})
		}
	}
}

// panicText is what a panicking source carries. It stands for text that must not leave the source.
const panicText = "panic: token for host=db.internal.example"

// panicking is a source whose read of one tool panics. With leave set, the caller leaves in the same
// instant: the read ends its caller's context and panics at once, so the maker finds the cancel and the
// read's answer ready together and may take either first.
type panicking struct {
	Source
	tool  string
	leave func()
}

func (p *panicking) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool != p.tool {
		return p.Source.Read(ctx, tool, accountID)
	}
	if p.leave != nil {
		p.leave()
	}
	panic(panicText)
}

// recovered runs run and gives what it returned, or the panic it let through.
func recovered(run func() (*Sealed, error)) (sealed *Sealed, err error, panicked any) {
	defer func() { panicked = recover() }()
	sealed, err = run()
	return sealed, err, nil
}

// A source that panics is a read that failed. The panic does not reach the caller, the read's receipt
// finishes as an error, no card is made, and nothing the panic carried is in the error or the receipt.
func TestASourceThatPanicsIsAFailedRead(t *testing.T) {
	for name, fresh := range worlds(t) {
		for kind, run := range makes {
			t.Run(name+"/"+kind, func(t *testing.T) {
				w := fresh(t)
				store := &watched{Store: w.store}
				source := &panicking{Source: demoBook(t), tool: "read_call_notes"}
				m := w.maker(t, store, source)
				sealed, err, panicked := recovered(func() (*Sealed, error) { return run(context.Background(), m, w, "panicked") })
				if panicked != nil {
					t.Fatalf("the panic reached the caller: %v", panicked)
				}
				if sealed != nil || !errors.Is(err, ErrSourcePanicked) || !errors.Is(err, ErrBriefing) ||
					strings.Contains(err.Error(), "db.internal.example") {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				w.stoppedAt(t, store, map[string]string{"brief": "panicked/3", "prepare": "panicked#3"}[kind], 200)
				if kind == "prepare" {
					w.failedSeal(t, kind, "panicked")
				}
			})
		}
	}
}

// together is a caller's context for a read that panics as the caller leaves. Once armed, it does not hand
// the maker its Done channel until the source has panicked and the read's answer has had a moment to arrive.
// A select takes its channels as it is entered, so the maker's wait then finds the cancel and the answer
// ready together, and takes either first.
type together struct {
	context.Context
	armed    atomic.Bool
	panicked chan struct{} // closed by the source as it ends the context and panics
}

func (c *together) Done() <-chan struct{} {
	if c.armed.Load() {
		<-c.panicked
		time.Sleep(time.Millisecond)
	}
	return c.Context.Done()
}

// arming is a store that arms the context as the panicking read's receipt starts. That is in the maker's own
// goroutine, after the read before it was taken and before the maker waits for this one, so the maker's wait
// for the panicking read is the first to ask for the Done channel.
type arming struct {
	*watched
	ctx *together
}

func (a *arming) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	if s.Tool == "read_call_notes" {
		a.ctx.armed.Store(true)
	}
	return a.watched.Start(ctx, s)
}

// A source that panics as its context ends is settled the same in both orders: the maker may take the read's
// answer first, or the cancel. Either way the read's receipt finishes as an error, the panic stays in the
// source's goroutine, and the caller gets the cancel. Which it takes is a coin toss each time both are ready,
// so the card is made many times over to meet both. The memory store alone runs this: a database driver
// would wait on the same Done channel.
func TestASourceThatPanicsAsItsContextEndsIsSettledInBothOrders(t *testing.T) {
	w := worlds(t)["memory"]
	for range 200 {
		w := w(t)
		inner, cancel := context.WithCancel(context.Background())
		ctx := &together{Context: inner, panicked: make(chan struct{})}
		store := &arming{watched: &watched{Store: w.store}, ctx: ctx}
		source := &panicking{Source: demoBook(t), tool: "read_call_notes", leave: func() {
			close(ctx.panicked)
			cancel()
		}}
		m := w.maker(t, store, source)
		sealed, err, panicked := recovered(func() (*Sealed, error) { return m.Brief(ctx, w.brief("ending")) })
		if panicked != nil {
			t.Fatalf("the panic reached the caller: %v", panicked)
		}
		if sealed != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrBriefing) ||
			strings.Contains(err.Error(), "db.internal.example") {
			t.Fatalf("a card: %t; %v", sealed != nil, err)
		}
		w.stoppedAt(t, store.watched, "ending/3", 200)
	}
}
