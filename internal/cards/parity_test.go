package cards

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// scope reproduces tools/oracle/freeze.py's Scope: IDs, tokens and receipt times that depend on the
// scope's name alone. Receipts start at 2027-01-15T15:00:00Z, one second apart.
type scope struct {
	space    uint32
	ids      int
	receipts int
}

func newScope(name string) *scope {
	sum := sha256.Sum256([]byte(name))
	return &scope{space: binary.BigEndian.Uint32(sum[:4])}
}

func (s *scope) uuid() string {
	s.ids++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", s.space, s.ids)
}

// token is the scope's n-th secrets.token_urlsafe(32).
func (s *scope) token(n int) string {
	base := fmt.Sprintf("t%08x%08x", s.space, n)
	return strings.Repeat(base, 43/len(base)+1)[:43]
}

func (s *scope) clock() time.Time {
	s.receipts++
	return time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC).Add(time.Duration(s.receipts-1) * time.Second)
}

const testWorkspace = "workspace-1"

// still is a clock that stands still, so every elapsed time is 0, as the oracle runs.
func still() time.Time { return time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC) }

// maker is a Maker under the scope's IDs and clock, reading through connections at generation 1.
func (s *scope) maker(source Source, sources evidence.Sources) (*Maker, *receipts.Memory) {
	store := receipts.NewMemory(s.clock, s.uuid)
	connections := map[string]string{"crm": "crm-1", "calls": "calls-1", "slack": "slack-1"}
	for _, id := range connections {
		store.SetGeneration(testWorkspace, id, 1)
	}
	return &Maker{Receipts: store, Source: source, Sources: sources, Connections: connections, NewID: s.uuid,
		Now: still}, store
}

// owner is the local owner running a command, under the bearer the CLI minted for it.
func owner(token string, scopes ...string) Principal {
	return Principal{WorkspaceID: testWorkspace, TokenID: receipts.Fingerprint(token), Scopes: scopes}
}

// accessToken is the token a card's reads are made under: the scope's fourth token, after the PKCE
// verifier, the state and the authorization code. A scope that also starts the connector draws the
// owner secret first, so its access token is the fifth.
const accessToken = 4

func demoSources(sellers []string) evidence.Sources {
	return evidence.Sources{CRMBackend: "sqlite", CRMBinding: evidence.FixtureCRMBinding(), CallsBackend: "fixture",
		SlackBackend: "fixture", Sellers: sellers}
}

func demoBook(t *testing.T) *demo.Store {
	t.Helper()
	store, err := demo.Load(oracletest.Read(t, "cards/demo_book.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// insert loads rows recorded as {"crm": [[table, [values]], ...], "calls": [...], "slack": [...]}.
func insert(t *testing.T, store *demo.Store, rows *strictjson.Object, databases ...string) {
	t.Helper()
	for _, database := range databases {
		list, _ := rows.Get(database)
		for _, entry := range list.([]any) {
			pair := entry.([]any)
			if err := store.Insert(database, pair[0].(string), pair[1].([]any)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// noTranscripts is the imported calls source of a folder with nothing imported for the account.
type noTranscripts struct{ *demo.Store }

func (s noTranscripts) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_call_notes" {
		return "calls", nil, nil
	}
	return s.Store.Read(ctx, tool, accountID)
}

type fixtureRow struct {
	Key       string          `json:"key"`
	AccountID string          `json:"account_id"`
	Options   json.RawMessage `json:"options"`
	Input     json.RawMessage `json:"input"`
	SHA256    string          `json:"sha256"`
	Card      string          `json:"card"`
	Seal      json.RawMessage `json:"seal"`
}

type options struct {
	IncludeCalls *bool   `json:"include_calls"`
	IncludeSlack bool    `json:"include_slack"`
	GrantSlack   *bool   `json:"grant_slack"`
	AsOf         *string `json:"as_of"`
}

// brief is create_brief's request for these options: crm:read, calls:read unless left out, and slack:read
// when Slack is asked for and granted.
func (o options) brief(accountID, token string) Brief {
	b := Brief{Principal: owner(token, "crm:read"), OperationID: "brief " + accountID, AccountID: accountID,
		AsOf: DemoAsOf, IncludeSlack: o.IncludeSlack}
	if o.AsOf != nil {
		b.AsOf = *o.AsOf
	}
	if o.IncludeCalls == nil || *o.IncludeCalls {
		b.Principal.Scopes = append(b.Principal.Scopes, "calls:read")
	}
	if o.IncludeSlack && (o.GrantSlack == nil || *o.GrantSlack) {
		b.Principal.Scopes = append(b.Principal.Scopes, "slack:read")
	}
	return b
}

// setup is how one fixture card is made: its scope, the source it reads, and the folder's sources.
type setup struct {
	scope   string
	source  Source
	sources evidence.Sources
	skip    string // why this card is not built from its recorded inputs
}

func load(t *testing.T, data []byte) any {
	t.Helper()
	value, err := strictjson.Loads(data)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// cardStore is where a family's cards are built and published.
type cardStore interface {
	// maker is the Maker for one card, reading through connections at generation 1, and the request it makes.
	maker(t *testing.T, sc *scope, s setup, row fixtureRow, b Brief) (*Maker, Brief)
	// published checks what the store holds for a card the maker sealed, and gives the bytes, digest and seal
	// to compare with the oracle's.
	published(t *testing.T, sealed *Sealed, s setup, row fixtureRow, mismatches *oracletest.Mismatches) (
		payload []byte, sha string, seal receipts.Receipt)
}

// memoryCards builds each card on its own Memory store, under the scope's IDs and clock, as the oracle did.
type memoryCards struct{ stores map[string]*receipts.Memory }

func newMemoryCards(*testing.T) cardStore { return &memoryCards{stores: map[string]*receipts.Memory{}} }

func (c *memoryCards) maker(_ *testing.T, sc *scope, s setup, _ fixtureRow, b Brief) (*Maker, Brief) {
	m, store := sc.maker(s.source, s.sources)
	c.stores[s.scope] = store
	return m, b
}

func (c *memoryCards) published(_ *testing.T, sealed *Sealed, s setup, row fixtureRow,
	mismatches *oracletest.Mismatches) ([]byte, string, receipts.Receipt) {
	published, _ := c.stores[s.scope].Published(testWorkspace, sealed.BriefID)
	if string(published.Bytes) != string(sealed.Payload) {
		mismatches.Add("%s: the published bytes are not the sealed ones", row.Key)
	}
	// The published projections, type for type, are the oracle card's units and gaps.
	want, err := receipts.ParsePayload([]byte(row.Card))
	if err != nil || len(published.Units) != len(want.Units) || len(published.Gaps) != len(want.Gaps) {
		mismatches.Add("%s: %d units and %d gaps published", row.Key, len(published.Units), len(published.Gaps))
		return sealed.Payload, sealed.SHA256, sealed.Seal
	}
	for i, u := range published.Units {
		value, err := strictjson.Loads(u.Value)
		if err != nil || !sameJSON(value, want.Units[i]) {
			mismatches.Add("%s: published unit %d is not the oracle's", row.Key, i+1)
		}
	}
	for i, g := range published.Gaps {
		var count, byRole any
		if g.Count != nil {
			count = g.Count
		}
		if g.ByRole != nil {
			byRole, _ = strictjson.Loads(g.ByRole)
		}
		if !sameJSON(strictjson.NewObject("code", g.Code, "disposition", g.Disposition, "count", count,
			"by_role", byRole, "text", g.Text), want.Gaps[i]) {
			mismatches.Add("%s: published gap %d is not the oracle's", row.Key, i+1)
		}
	}
	return sealed.Payload, sealed.SHA256, sealed.Seal
}

func runFamily(t *testing.T, family string, newStore func(t *testing.T) cardStore,
	prepare func(row fixtureRow, input *strictjson.Object) setup) {
	mismatches := oracletest.NewMismatches(t, "cards/"+family)
	var cards cardStore
	checked, identical, skipped := 0, 0, map[string]int{}
	for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
		var row fixtureRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		input, _ := load(t, row.Input).(*strictjson.Object)
		s := prepare(row, input)
		if s.skip != "" {
			skipped[s.skip]++
			continue
		}
		mismatches.Row()
		checked++
		var o options
		if err := json.Unmarshal(row.Options, &o); err != nil {
			t.Fatal(err)
		}
		if cards == nil {
			cards = newStore(t)
		}
		sc := newScope(s.scope)
		m, b := cards.maker(t, sc, s, row, o.brief(row.AccountID, sc.token(accessToken)))
		sealed, err := m.Brief(context.Background(), b)
		if err != nil {
			mismatches.Add("%s: no card: %v", row.Key, err)
			continue
		}
		payload, sha, sealReceipt := cards.published(t, sealed, s, row, mismatches)
		if string(payload) == row.Card {
			identical++
		} else {
			mismatches.Add("%s: card text differs at %s", row.Key, oracletest.FirstDifference(string(payload), row.Card))
		}
		if sha != row.SHA256 {
			mismatches.Add("%s: digest %s, want %s", row.Key, sha, row.SHA256)
		}
		if seal, err := receipts.Decode(load(t, row.Seal)); err != nil || !sealReceipt.Same(seal) {
			got, _ := strictjson.Dumps(sealReceipt.Value(), strictjson.Quoted)
			mismatches.Add("%s: seal receipt %s, want %s", row.Key, got, row.Seal)
		}
		want := load(t, []byte(row.Card)).(*strictjson.Object)
		if !evidence.Equal(load(t, payload), want) {
			mismatches.Add("%s: card JSON differs as values", row.Key)
		}
		if units, _ := want.Get("units"); !evidence.Equal(evidence.UnitValues(sealed.Units), units) {
			mismatches.Add("%s: units differ", row.Key)
		}
		if gaps, _ := want.Get("gaps"); !evidence.Equal(evidence.GapValues(sealed.Gaps), gaps) {
			mismatches.Add("%s: gaps differ", row.Key)
		}
	}
	t.Logf("%s: %d cards checked, %d byte-identical, %d differ; not built from inputs: %v", family, checked,
		identical, mismatches.Count(), skipped)
	if want := builtCards[family]; checked != want {
		t.Errorf("%s: %d cards built, want %d; a card that stops being built must not pass as skipped", family,
			checked, want)
	}
}

// builtCards pins how many cards of each family Go builds. Raise a count when a port builds more.
var builtCards = map[string]int{
	"demo": 33, "books_demo": 78, "varied_demo": 46, "cases": 19, "books_calls": 32,
	"books_slack": 0, "slack_threads": 0, "books_salesforce": 78,
}

func TestCardsFromDemoStores(t *testing.T) { demoStoreFamilies(t, newMemoryCards) }

func TestCardsFromRecordedInputs(t *testing.T) { recordedInputFamilies(t, newMemoryCards) }

// demoStoreFamilies builds the families whose cards read the demo stores, as seeded or with rows inserted.
func demoStoreFamilies(t *testing.T, newStore func(*testing.T) cardStore) {
	book := oracletest.Read(t, "cards/demo_book.json")
	fresh := func() *demo.Store {
		store, err := demo.Load(book)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	t.Run("demo", func(t *testing.T) {
		runFamily(t, "demo", newStore, func(row fixtureRow, _ *strictjson.Object) setup {
			// "acc-003 without calls" was made in the scope "demo/acc-003/without calls".
			return setup{scope: "demo/" + strings.Replace(row.Key, " ", "/", 1), source: fresh(), sources: demoSources(nil)}
		})
	})
	for family, prefix := range map[string]string{"books_demo": "books", "varied_demo": "varied"} {
		t.Run(family, func(t *testing.T) {
			runFamily(t, family, newStore, func(row fixtureRow, input *strictjson.Object) setup {
				store := fresh()
				rows, _ := input.Get("rows")
				insert(t, store, rows.(*strictjson.Object), "crm", "calls", "slack")
				return setup{scope: prefix + "/demo/" + row.Key, source: store, sources: demoSources(nil)}
			})
		})
	}
	t.Run("cases", func(t *testing.T) {
		runFamily(t, "cases", newStore, func(row fixtureRow, input *strictjson.Object) setup {
			store, sellers := fresh(), []string(nil)
			if inserted, ok := input.Get("inserted"); ok {
				insert(t, store, inserted.(*strictjson.Object), "crm", "calls", "slack")
				document, _ := input.Get("sellers.json")
				list, _ := document.(*strictjson.Object).Get("sellers")
				for _, seller := range list.([]any) {
					sellers = append(sellers, seller.(string))
				}
			}
			return setup{scope: "cases/" + row.Key, source: store, sources: demoSources(sellers)}
		})
	})
}

// recordedInputFamilies builds the cards of the other families whose recorded inputs hold every row the card
// reads. The rest need a source adapter that is not ported yet, and are counted by reason.
func recordedInputFamilies(t *testing.T, newStore func(*testing.T) cardStore) {
	book := oracletest.Read(t, "cards/demo_book.json")
	t.Run("books_calls", func(t *testing.T) {
		runFamily(t, "books_calls", newStore, func(row fixtureRow, input *strictjson.Object) setup {
			if _, ok := input.Get("transcript"); ok {
				return setup{skip: "imported transcript: needs the transcript parser and import store"}
			}
			store, err := demo.Load(book)
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := input.Get("rows")
			insert(t, store, rows.(*strictjson.Object), "crm")
			name, _ := input.Get("book")
			index, _ := input.Get("index")
			sources := demoSources(nil)
			sources.CallsBackend, sources.CallsBinding = "imported", evidence.FixtureCRMBinding()
			return setup{scope: fmt.Sprintf("books/calls/%s/%d", name, 300+intOf(index)),
				source: noTranscripts{store}, sources: sources}
		})
	})
	t.Run("books_salesforce", func(t *testing.T) { salesforceBooks(t, newStore, book) })
	// What each skipped card lacks among its recorded inputs.
	reasons := map[string]func(input *strictjson.Object) string{
		"books_slack": func(input *strictjson.Object) string {
			if thread, _ := input.Get("thread"); thread == nil {
				return "no thread mapped: the Slack connection's binding (identity_sha256) is not recorded"
			}
			return "Slack records and thread_snapshot: thread holds API messages, which only the Slack reader maps"
		},
		"slack_threads": func(*strictjson.Object) string {
			return "Slack records and thread_snapshot: rows are API messages, which only the Slack reader maps"
		},
	}
	for family, reason := range reasons {
		t.Run(family, func(t *testing.T) {
			runFamily(t, family, newStore, func(_ fixtureRow, input *strictjson.Object) setup {
				return setup{skip: reason(input)}
			})
		})
	}
}

func intOf(v any) int {
	n, _ := v.(interface{ Int64() int64 })
	return int(n.Int64())
}
