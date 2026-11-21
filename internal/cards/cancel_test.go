package cards

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A cancel, or the card's deadline, reaches a source read that is waiting. The read returns, its receipt
// finishes as an error, no later read runs, and no card is published or sealed, on both stores.

// blocking is a source whose read of one tool waits until its context is done, as a slow source call does.
// With answers set it then returns the records all the same, as a source that finished late would.
type blocking struct {
	Source
	tool     string
	answers  bool
	entered  chan struct{} // closed when the read starts to wait
	returned chan struct{} // closed when it has returned
}

func block(source Source, tool string, answers bool) *blocking {
	return &blocking{Source: source, tool: tool, answers: answers, entered: make(chan struct{}),
		returned: make(chan struct{})}
}

func (b *blocking) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool != b.tool {
		return b.Source.Read(ctx, tool, accountID)
	}
	close(b.entered)
	defer close(b.returned)
	<-ctx.Done()
	if b.answers {
		return b.Source.Read(ctx, tool, accountID)
	}
	return "", nil, ctx.Err()
}

// whenWaiting runs then once the slow read is waiting, or not at all if ctx ends first.
func (b *blocking) whenWaiting(ctx context.Context, then func()) {
	go func() {
		select {
		case <-b.entered:
			then()
		case <-ctx.Done():
		}
	}()
}

// watched counts the cards a store is asked to publish.
type watched struct {
	receipts.Store
	published int
}

func (w *watched) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	w.published++
	return w.Store.Publish(ctx, p)
}

// stopped checks a card that stopped at its read of call notes, the third: the read returned, its receipt
// finished as an error with this HTTP status and no rows, Slack was never read, and nothing was published.
func (w *world) stopped(t *testing.T, store *watched, source *blocking, readOperation string, status int) {
	t.Helper()
	select {
	case <-source.returned:
	default:
		t.Fatal("the read has not returned")
	}
	read, err := w.store.ByOperation(context.Background(), w.workspace, readOperation)
	if err != nil {
		t.Fatal(err)
	}
	if read.Tool != "read_call_notes" || read.Status != "error" || *read.HTTPStatus != status ||
		*read.Error != "request_failed" || len(read.Rows) != 0 {
		t.Fatalf("the stopped read: %+v", read)
	}
	if w.total() != 3 || w.reads["read_slack_messages"] != 0 {
		t.Fatalf("the reads: %v", w.reads)
	}
	if store.published != 0 {
		t.Fatalf("%d cards published", store.published)
	}
}

func TestACancelStopsAWaitingRead(t *testing.T) {
	for name, fresh := range worlds(t) {
		for _, answers := range []bool{false, true} {
			kind := map[bool]string{false: "a read that fails", true: "a read that answers late"}[answers]
			t.Run(name+"/"+kind+"/brief", func(t *testing.T) {
				w := fresh(t)
				store, source := &watched{Store: w.store}, block(demoBook(t), "read_call_notes", answers)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				source.whenWaiting(ctx, cancel)
				sealed, err := w.maker(t, store, source).Brief(ctx, w.brief("brief-cancelled"))
				if sealed != nil || !errors.Is(err, ErrBriefing) || !errors.Is(err, context.Canceled) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				w.stopped(t, store, source, "brief-cancelled/3", 200)
				_, err = w.store.ByOperation(context.Background(), w.workspace, "brief-cancelled/seal")
				if !errors.Is(err, receipts.ErrNotFound) {
					t.Fatalf("a seal was started: %v", err)
				}
			})
			t.Run(name+"/"+kind+"/prepare", func(t *testing.T) {
				w := fresh(t)
				store, source := &watched{Store: w.store}, block(demoBook(t), "read_call_notes", answers)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				source.whenWaiting(ctx, cancel)
				sealed, err := w.maker(t, store, source).Prepare(ctx, w.prepare("prepare-cancelled"))
				if sealed != nil || !errors.Is(err, ErrSourceFailed) || !errors.Is(err, context.Canceled) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				w.stopped(t, store, source, "prepare-cancelled#3", 200)
				// The call's own receipt would be the seal; it finished as an error.
				outer := must(w.store.ByOperation(context.Background(), w.workspace, "prepare-cancelled"))
				if outer.Status != "error" || *outer.Error != "request_failed" || len(outer.Rows) != 0 {
					t.Fatalf("the call: %+v", outer)
				}
			})
		}
	}
}

func TestTheDeadlineStopsAWaitingRead(t *testing.T) {
	for name, fresh := range worlds(t) {
		for _, answers := range []bool{false, true} {
			kind := map[bool]string{false: "a read that fails", true: "a read that answers late"}[answers]
			t.Run(name+"/"+kind, func(t *testing.T) {
				w := fresh(t)
				// The clock stands still until the read before the slow one, and from then on stands 25 ms
				// short of the deadline, so the slow read has that long.
				var late atomic.Int64
				book := during{demoBook(t), func(tool string) {
					if tool == "list_opportunities" {
						late.Store(int64(BriefDeadline - 25*time.Millisecond))
					}
				}}
				store, source := &watched{Store: w.store}, block(book, "read_call_notes", answers)
				m := w.maker(t, store, source)
				m.Now = func() time.Time { return still().Add(time.Duration(late.Load())) }
				// A maker that does not bound the read is cancelled instead, which is not the deadline.
				ctx, cancel := context.WithCancel(context.Background())
				defer time.AfterFunc(10*time.Second, cancel).Stop()
				sealed, err := m.Prepare(ctx, w.prepare("prepare-late"))
				if sealed != nil || !errors.Is(err, ErrDeadline) {
					t.Fatalf("a card: %t; %v", sealed != nil, err)
				}
				w.stopped(t, store, source, "prepare-late#3", 504)
				outer := must(w.store.ByOperation(context.Background(), w.workspace, "prepare-late"))
				if outer.Status != "error" || *outer.Error != "request_failed" || len(outer.Rows) != 0 {
					t.Fatalf("the call: %+v", outer)
				}
			})
		}
	}
}

// seen is a source that keeps the deadline each read was given.
type seen struct {
	Source
	deadlines []time.Duration // the time left; -1 for a read with no deadline
}

func (s *seen) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	s.deadlines = append(s.deadlines, left)
	return s.Source.Read(ctx, tool, accountID)
}

// A read that returns in time builds the same card as before: a brief's reads carry no deadline, a prepared
// brief's carry the time left of its 45 seconds, and the bytes are those a plain source gives.
func TestAReadInTimeBuildsTheSameCard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	all := []string{"crm:read", "calls:read", "slack:read"}
	brief := Brief{Principal: owner("a bearer", all...), OperationID: "brief", AccountID: "acc-007", AsOf: DemoAsOf,
		IncludeSlack: true}
	prepare := prepareOf("a bearer", "acc-007", true, strictjson.NewObject("account_id", "acc-007"), all...)
	for name, run := range map[string]func(m *Maker) (*Sealed, error){
		"brief":   func(m *Maker) (*Sealed, error) { return m.Brief(ctx, brief) },
		"prepare": func(m *Maker) (*Sealed, error) { return m.Prepare(ctx, prepare) },
	} {
		t.Run(name, func(t *testing.T) {
			plain, _ := newScope("in time").maker(demoBook(t), demoSources(nil))
			want, err := run(plain)
			if err != nil {
				t.Fatal(err)
			}
			source := &seen{Source: demoBook(t)}
			m, _ := newScope("in time").maker(source, demoSources(nil))
			got, err := run(m)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Payload, want.Payload) || !got.Seal.Same(want.Seal) {
				t.Fatal("the card differs")
			}
			if len(source.deadlines) != 4 {
				t.Fatalf("the reads: %v", source.deadlines)
			}
			for _, left := range source.deadlines {
				if bounded := left > 0 && left <= BriefDeadline; bounded != (name == "prepare") {
					t.Fatalf("the deadlines: %v", source.deadlines)
				}
			}
		})
	}
}
