package cards

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The maker runs only work its own start committed, on both stores. A start that finds its receipt
// committed already reuses that outcome: a finished read gives back what it bound, a pending one is
// another worker's, and a finished or running brief call is never run or settled again.

// world is the maker's surroundings on one store: acc-003 granted to the reader, and the source reads
// counted by tool.
type world struct {
	store       receipts.Store
	workspace   string
	principal   string
	connections map[string]string
	mu          sync.Mutex
	reads       map[string]int
	log         Log
}

func worlds(t *testing.T) map[string]func(t *testing.T) *world {
	return map[string]func(t *testing.T) *world{
		"memory": func(t *testing.T) *world {
			m := receipts.NewMemory(nil, nil)
			connections := map[string]string{"crm": "crm-1", "calls": "calls-1", "slack": "slack-1"}
			for _, id := range connections {
				m.SetGeneration(testWorkspace, id, 1)
			}
			m.Grant(testWorkspace, "reader", "acc-003")
			return &world{store: m, workspace: testWorkspace, principal: "reader", connections: connections,
				reads: map[string]int{}, log: StoreLog{Store: m, WorkspaceID: testWorkspace}}
		},
		"postgres": func(t *testing.T) *world {
			e := newE2E(t)
			return &world{store: e.receipts, workspace: e.workspace, principal: e.reader.ID, connections: e.connections,
				reads: map[string]int{}, log: StoreLog{Store: e.receipts, WorkspaceID: e.workspace}}
		},
	}
}

// counted reads a source and counts each read.
type counted struct {
	Source
	w *world
}

func (c counted) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	c.w.mu.Lock()
	c.w.reads[tool]++
	c.w.mu.Unlock()
	return c.Source.Read(ctx, tool, accountID)
}

func (w *world) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, reads := range w.reads {
		n += reads
	}
	return n
}

// maker makes cards with the same brief ID every time, as a retried command does.
func (w *world) maker(t *testing.T, store receipts.Store, source Source, sources ...string) *Maker {
	s := demoSources(nil)
	if len(sources) > 0 {
		s.SlackBackend = sources[0]
		s.SlackBinding = strictjson.NewObject("schema_version", 1, "provider", "slack",
			"identity_sha256", strings.Repeat("ab", 32))
	}
	return &Maker{Receipts: store, Source: counted{source, w}, Sources: s, Connections: w.connections,
		NewID: func() string { return "00000000-0000-4000-8000-0000000000aa" }}
}

func (w *world) principalOf() Principal {
	return Principal{WorkspaceID: w.workspace, PrincipalID: w.principal, TokenID: receipts.Fingerprint("a bearer"),
		Scopes: []string{"crm:read", "calls:read", "slack:read"}}
}

func (w *world) prepare(operation string) Prepare {
	return Prepare{Principal: w.principalOf(), Selection: Selection{"acc-003": true}, OperationID: operation,
		RequestID: receipts.RPC(1), Arguments: strictjson.NewObject("account_id", "acc-003")}
}

func (w *world) brief(operation string) Brief {
	return Brief{Principal: w.principalOf(), OperationID: operation, AccountID: "acc-003", AsOf: DemoAsOf,
		IncludeSlack: true}
}

// failingSeal is a store whose first save_brief start fails, as a process that stops between the reads and
// the seal does.
type failingSeal struct {
	receipts.Store
	failed bool
}

func (f *failingSeal) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	if s.Tool == receipts.SaveBrief && !f.failed {
		f.failed = true
		return receipts.Receipt{}, errors.New("stopped before the seal")
	}
	return f.Store.Start(ctx, s)
}

func TestAReplayedStartDoesNotRunAgain(t *testing.T) {
	ctx := context.Background()
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("a prepared brief run again after it finished", func(t *testing.T) {
				w := fresh(t)
				m := w.maker(t, w.store, demoBook(t))
				first, err := m.Prepare(ctx, w.prepare("prepare-once"))
				if err != nil {
					t.Fatal(err)
				}
				before := w.total()
				_, err = m.Prepare(ctx, w.prepare("prepare-once"))
				if !errors.Is(err, receipts.ErrFinished) || w.total() != before {
					t.Fatalf("the rerun: %v, %d reads after %d", err, w.total(), before)
				}
				outer := must(w.store.ByOperation(ctx, w.workspace, "prepare-once"))
				if !outer.Same(first.Seal) {
					t.Fatalf("the rerun changed the call: %+v", outer)
				}
			})
			t.Run("a prepared brief another worker is running", func(t *testing.T) {
				w := fresh(t)
				p := w.prepare("prepare-running")
				// The other worker's start; it holds the call and has read nothing yet.
				must(w.store.Start(ctx, receipts.Start{WorkspaceID: w.workspace, OperationID: p.OperationID,
					PrincipalID: w.principal, TokenID: p.Principal.TokenID, Tool: receipts.Composite, Scope: "crm:read",
					AccountRef: "acc-003", Arguments: p.Arguments, RequestID: p.RequestID}))
				_, err := w.maker(t, w.store, demoBook(t)).Prepare(ctx, p)
				if !errors.Is(err, receipts.ErrInProgress) || w.total() != 0 {
					t.Fatalf("the second worker: %v, %d reads", err, w.total())
				}
				if outer := must(w.store.ByOperation(ctx, w.workspace, p.OperationID)); outer.Status != "pending" {
					t.Fatalf("the second worker settled the call as %s", outer.Status)
				}
			})
			t.Run("a card whose reads finished before a stop is sealed without reading again", func(t *testing.T) {
				w := fresh(t)
				stopping := &failingSeal{Store: w.store}
				_, err := w.maker(t, stopping, demoBook(t)).Brief(ctx, w.brief("brief-resumed"))
				if err == nil || !strings.Contains(err.Error(), "stopped before the seal") || w.total() != 4 {
					t.Fatalf("the first run: %v, %d reads", err, w.total())
				}
				sealed, err := w.maker(t, w.store, demoBook(t)).Brief(ctx, w.brief("brief-resumed"))
				if err != nil || w.total() != 4 {
					t.Fatalf("the rerun: %v, %d reads", err, w.total())
				}
				if _, err := VerifyCard(ctx, sealed.Payload, sealed.BriefID, w.log, demoSources(nil)); err != nil {
					t.Fatal(err)
				}
				// Run again once sealed: nothing runs.
				_, err = w.maker(t, w.store, demoBook(t)).Brief(ctx, w.brief("brief-resumed"))
				if !errors.Is(err, receipts.ErrFinished) || w.total() != 4 {
					t.Fatalf("a run after the seal: %v, %d reads", err, w.total())
				}
			})
		})
	}
}

// notSelected is the live Slack source of a folder with no thread mapped for the account.
type notSelected struct{ Source }

func (n notSelected) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_slack_messages" {
		return "", nil, ErrNotSelected
	}
	return n.Source.Read(ctx, tool, accountID)
}

// A read with no selection for the account is listed with the card on both stores: it finished error/200,
// and the card says Slack was not selected.
func TestANotSelectedReadIsListedWithTheCard(t *testing.T) {
	ctx := context.Background()
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			w := fresh(t)
			sealed, err := w.maker(t, w.store, notSelected{demoBook(t)}, "slack").Brief(ctx, w.brief("not-selected"))
			if err != nil {
				t.Fatal(err)
			}
			payload, err := receipts.ParsePayload(sealed.Payload)
			if err != nil {
				t.Fatal(err)
			}
			last := payload.Receipts[len(payload.Receipts)-1]
			if get(last, "tool") != "read_slack_messages" || get(last, "status") != "error" ||
				!sameJSON(get(last, "http_status"), 200) || !strings.Contains(string(sealed.Payload), "slack_not_selected") {
				t.Fatalf("the Slack read: %v", last)
			}
		})
	}
}

// failing is a source whose Slack read fails.
type failing struct{ Source }

func (f failing) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_slack_messages" {
		return "", nil, errors.New("the read did not finish")
	}
	return f.Source.Read(ctx, tool, accountID)
}

// A Slack read that failed gives no card, and neither does the same command run again: its receipt reads
// the same as a read with no selection, so the replay must not make a card that says nothing was selected.
func TestAFailedReadGivesNoCardWhenTheCommandRunsAgain(t *testing.T) {
	ctx := context.Background()
	for name, fresh := range worlds(t) {
		t.Run(name, func(t *testing.T) {
			w := fresh(t)
			maker := w.maker(t, w.store, failing{demoBook(t)}, "slack")
			for attempt := 1; attempt <= 2; attempt++ {
				if sealed, err := maker.Brief(ctx, w.brief("failed-read")); err == nil {
					t.Fatalf("attempt %d made a card: %s", attempt, sealed.Payload)
				}
			}
		})
	}
}

// must is the value of a call that cannot fail here; a failure panics, which fails the test with its stack.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
