package cards

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A caller who leaves between the last read and the publication gets no card, and a source that ignores its
// context does not hold the maker, on both stores.

// leaving is a store whose caller leaves at one step: after the last read has finished, after the reads'
// receipts are read back for the evidence, after the seal has started, or as the publication begins. It keeps
// whether the context each publication was given had ended.
type leaving struct {
	*watched
	at       string // "last read", "evidence", "seal" or "publish"
	leave    func()
	finished int
	ended    []bool
}

func (l *leaving) FinishRead(ctx context.Context, ticket receipts.Ticket, versions []receipts.RecordVersion,
	elapsedMS float64) (receipts.Receipt, error) {
	r, err := l.watched.FinishRead(ctx, ticket, versions, elapsedMS)
	// acc-003 has four reads, and Slack is the last.
	if l.finished++; l.at == "last read" && l.finished == 4 {
		l.leave()
	}
	return r, err
}

func (l *leaving) Read(ctx context.Context, workspaceID string, auditIDs []string) ([]receipts.Receipt, error) {
	read, err := l.watched.Read(ctx, workspaceID, auditIDs)
	if l.at == "evidence" {
		l.leave()
	}
	return read, err
}

func (l *leaving) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	r, err := l.watched.Start(ctx, s)
	if l.at == "seal" && s.Tool == receipts.SaveBrief {
		l.leave()
	}
	return r, err
}

func (l *leaving) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	if l.at == "publish" {
		l.leave()
	}
	l.ended = append(l.ended, ctx.Err() != nil)
	return l.watched.Publish(ctx, p)
}

// finishedOK fails the test unless each of the card's four reads kept its receipt, finished ok.
func (w *world) finishedOK(t *testing.T, operation string) {
	t.Helper()
	for n := 1; n <= 4; n++ {
		read, err := w.store.ByOperation(context.Background(), w.workspace, fmt.Sprintf(operation, n))
		if err != nil || read.Status != "ok" {
			t.Fatalf("read %d: %+v (%v)", n, read, err)
		}
	}
}

func TestACallerWhoLeftBeforeThePublicationGetsNoCard(t *testing.T) {
	for name, fresh := range worlds(t) {
		for _, at := range []string{"last read", "evidence", "seal"} {
			t.Run(name+"/"+at+"/brief", func(t *testing.T) {
				w := fresh(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &leaving{watched: &watched{Store: w.store}, at: at, leave: cancel}
				sealed, err := w.maker(t, store, demoBook(t)).Brief(ctx, w.brief("brief-left"))
				if sealed != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				if store.published != 0 {
					t.Fatalf("%d cards published", store.published)
				}
				w.finishedOK(t, "brief-left/%d")
				seal, err := w.store.ByOperation(context.Background(), w.workspace, "brief-left/seal")
				if at != "seal" {
					// The caller had left before the seal would start, so none was.
					if !errors.Is(err, receipts.ErrNotFound) {
						t.Fatalf("a seal was started: %+v (%v)", seal, err)
					}
					return
				}
				// The caller left once the seal had started. It finished as the save failure, with no card.
				if err != nil || seal.Status != "error" || *seal.HTTPStatus != 500 || *seal.Error != "brief_save_failed" ||
					len(seal.Rows) != 0 {
					t.Fatalf("the seal: %+v (%v)", seal, err)
				}
			})
		}
		for _, at := range []string{"last read", "evidence"} {
			t.Run(name+"/"+at+"/prepare", func(t *testing.T) {
				w := fresh(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &leaving{watched: &watched{Store: w.store}, at: at, leave: cancel}
				sealed, err := w.maker(t, store, demoBook(t)).Prepare(ctx, w.prepare("prepare-left"))
				if sealed != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				if store.published != 0 {
					t.Fatalf("%d cards published", store.published)
				}
				w.finishedOK(t, "prepare-left#%d")
				// The call's own receipt would be the seal; it finished as an error.
				outer := must(w.store.ByOperation(context.Background(), w.workspace, "prepare-left"))
				if outer.Status != "error" || *outer.Error != "request_failed" || len(outer.Rows) != 0 {
					t.Fatalf("the call: %+v", outer)
				}
			})
		}
	}
}

// Once the publication has begun, the caller's cancel no longer reaches it: the store is given a context
// that has not ended, and the card is published and sealed.
func TestAPublicationThatHasBegunCompletes(t *testing.T) {
	for name, fresh := range worlds(t) {
		for kind, run := range map[string]func(ctx context.Context, m *Maker, w *world) (*Sealed, error){
			"brief": func(ctx context.Context, m *Maker, w *world) (*Sealed, error) {
				return m.Brief(ctx, w.brief("begun"))
			},
			"prepare": func(ctx context.Context, m *Maker, w *world) (*Sealed, error) {
				return m.Prepare(ctx, w.prepare("begun"))
			},
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				w := fresh(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &leaving{watched: &watched{Store: w.store}, at: "publish", leave: cancel}
				sealed, err := run(ctx, w.maker(t, store, demoBook(t)), w)
				if err != nil || sealed == nil || sealed.Seal.Status != "ok" {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				if ctx.Err() == nil || len(store.ended) != 1 || store.ended[0] {
					t.Fatalf("the caller left: %t; the publications' contexts had ended: %v", ctx.Err() != nil, store.ended)
				}
			})
		}
	}
}

// stuck is a source whose read of one tool ignores its context, as a call with no timeout does: it waits
// to be let go, then answers with the records.
type stuck struct {
	Source
	tool     string
	entered  chan struct{} // closed when the read starts to wait
	release  chan struct{} // closed to let it answer
	returned chan struct{} // closed when it has returned
	once     sync.Once
}

// stick makes the read of tool a stuck one. It is let go when the test ends, if not before.
func stick(t *testing.T, source Source, tool string) *stuck {
	s := &stuck{Source: source, tool: tool, entered: make(chan struct{}), release: make(chan struct{}),
		returned: make(chan struct{})}
	t.Cleanup(s.let)
	return s
}

func (s *stuck) let() { s.once.Do(func() { close(s.release) }) }

// answer lets the read go and waits until it has returned.
func (s *stuck) answer() {
	s.let()
	<-s.returned
}

func (s *stuck) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool != s.tool {
		return s.Source.Read(ctx, tool, accountID)
	}
	close(s.entered)
	defer close(s.returned)
	<-s.release
	return s.Source.Read(context.WithoutCancel(ctx), tool, accountID)
}

// within gives what run returned, or fails the test when run has not returned in five seconds.
func within(t *testing.T, run func() (*Sealed, error)) (*Sealed, error) {
	t.Helper()
	type result struct {
		sealed *Sealed
		err    error
	}
	done := make(chan result, 1)
	go func() {
		sealed, err := run()
		done <- result{sealed, err}
	}()
	select {
	case r := <-done:
		return r.sealed, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("the maker is still waiting for the read")
		return nil, nil
	}
}

// A read that ignores a cancel is not waited for. The maker returns, the read's receipt finishes as an error,
// no later read runs, and nothing is sealed or published; the read's late answer changes none of it.
func TestACancelDoesNotWaitForAStuckRead(t *testing.T) {
	states := []string{"while the read is stuck", "after its late answer"}
	for name, fresh := range worlds(t) {
		t.Run(name+"/brief", func(t *testing.T) {
			w := fresh(t)
			store, source := &watched{Store: w.store}, stick(t, demoBook(t), "read_call_notes")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-source.entered; cancel() }()
			m := w.maker(t, store, source)
			sealed, err := within(t, func() (*Sealed, error) { return m.Brief(ctx, w.brief("brief-stuck")) })
			if sealed != nil || !errors.Is(err, ErrBriefing) || !errors.Is(err, context.Canceled) {
				t.Fatalf("a card: %t; %v", sealed != nil, err)
			}
			for _, state := range states {
				w.stoppedAt(t, store, "brief-stuck/3", 200)
				_, err := w.store.ByOperation(context.Background(), w.workspace, "brief-stuck/seal")
				if !errors.Is(err, receipts.ErrNotFound) {
					t.Fatalf("%s a seal was started: %v", state, err)
				}
				source.answer()
			}
		})
		t.Run(name+"/prepare", func(t *testing.T) {
			w := fresh(t)
			store, source := &watched{Store: w.store}, stick(t, demoBook(t), "read_call_notes")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-source.entered; cancel() }()
			m := w.maker(t, store, source)
			sealed, err := within(t, func() (*Sealed, error) { return m.Prepare(ctx, w.prepare("prepare-stuck")) })
			if sealed != nil || !errors.Is(err, ErrSourceFailed) || !errors.Is(err, context.Canceled) {
				t.Fatalf("a card: %t; %v", sealed != nil, err)
			}
			for _, state := range states {
				w.stoppedAt(t, store, "prepare-stuck#3", 200)
				outer := must(w.store.ByOperation(context.Background(), w.workspace, "prepare-stuck"))
				if outer.Status != "error" || *outer.Error != "request_failed" || len(outer.Rows) != 0 {
					t.Fatalf("%s the call: %+v", state, outer)
				}
				source.answer()
			}
		})
	}
}

// The card's deadline does not wait for a stuck read either: its receipt finishes as error/504.
func TestTheDeadlineDoesNotWaitForAStuckRead(t *testing.T) {
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			w := fresh(t)
			// The clock stands 25 ms short of the deadline from the read before the stuck one.
			var late atomic.Int64
			book := during{demoBook(t), func(tool string) {
				if tool == "list_opportunities" {
					late.Store(int64(BriefDeadline - 25*time.Millisecond))
				}
			}}
			store, source := &watched{Store: w.store}, stick(t, book, "read_call_notes")
			m := w.maker(t, store, source)
			m.Now = func() time.Time { return still().Add(time.Duration(late.Load())) }
			sealed, err := within(t, func() (*Sealed, error) {
				return m.Prepare(context.Background(), w.prepare("prepare-stuck-late"))
			})
			if sealed != nil || !errors.Is(err, ErrDeadline) {
				t.Fatalf("a card: %t; %v", sealed != nil, err)
			}
			for range 2 {
				w.stoppedAt(t, store, "prepare-stuck-late#3", 504)
				source.answer()
			}
		})
	}
}
