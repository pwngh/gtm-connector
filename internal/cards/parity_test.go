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

// maker is a Maker under the scope's IDs and clock, with every elapsed time 0, as the oracle runs.
func (s *scope) maker(source Source, sources evidence.Sources) (*Maker, *receipts.Memory, MemoryFiles) {
	store, files := receipts.NewMemory(s.clock, s.uuid), MemoryFiles{}
	return &Maker{Receipts: store, Source: source, Sources: sources, Files: files, NewID: s.uuid,
		Stopwatch: func() func() float64 { return func() float64 { return 0 } }}, store, files
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

func (s noTranscripts) Read(tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_call_notes" {
		return "calls", nil, nil
	}
	return s.Store.Read(tool, accountID)
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
	b := Brief{AccountID: accountID, AsOf: "2027-01-15", Token: token, Scopes: []string{"crm:read"},
		IncludeSlack: o.IncludeSlack}
	if o.AsOf != nil {
		b.AsOf = *o.AsOf
	}
	if o.IncludeCalls == nil || *o.IncludeCalls {
		b.Scopes = append(b.Scopes, "calls:read")
	}
	if o.IncludeSlack && (o.GrantSlack == nil || *o.GrantSlack) {
		b.Scopes = append(b.Scopes, "slack:read")
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

func firstDifference(got, want string) string {
	a, b := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return fmt.Sprintf("line %d: got %q, want %q", i+1, x, y)
		}
	}
	return "no line differs"
}

func load(t *testing.T, data []byte) any {
	t.Helper()
	value, err := strictjson.Loads(data)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func runFamily(t *testing.T, family string, prepare func(row fixtureRow, input *strictjson.Object) setup) {
	mismatches := oracletest.NewMismatches(t, "cards/"+family)
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
		sc := newScope(s.scope)
		m, _, files := sc.maker(s.source, s.sources)
		sealed, err := m.Brief(context.Background(), o.brief(row.AccountID, sc.token(accessToken)))
		if err != nil {
			mismatches.Add("%s: no card: %v", row.Key, err)
			continue
		}
		if string(sealed.Payload) == row.Card {
			identical++
		} else {
			mismatches.Add("%s: card text differs at %s", row.Key, firstDifference(string(sealed.Payload), row.Card))
		}
		if sealed.SHA256 != row.SHA256 || string(files[sealed.BriefID+".json"]) != row.Card {
			mismatches.Add("%s: digest %s, want %s", row.Key, sealed.SHA256, row.SHA256)
		}
		if !evidence.Equal(sealed.Seal.Value(), load(t, row.Seal)) {
			got, _ := strictjson.Dumps(sealed.Seal.Value(), strictjson.Quoted)
			mismatches.Add("%s: seal receipt %s, want %s", row.Key, got, row.Seal)
		}
		want := load(t, []byte(row.Card)).(*strictjson.Object)
		if !evidence.Equal(load(t, sealed.Payload), want) {
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
	"books_slack": 0, "slack_threads": 0, "books_salesforce": 0,
}

func TestCardsFromDemoStores(t *testing.T) {
	book := oracletest.Read(t, "cards/demo_book.json")
	fresh := func() *demo.Store {
		store, err := demo.Load(book)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	t.Run("demo", func(t *testing.T) {
		runFamily(t, "demo", func(row fixtureRow, _ *strictjson.Object) setup {
			// "acc-003 without calls" was made in the scope "demo/acc-003/without calls".
			return setup{scope: "demo/" + strings.Replace(row.Key, " ", "/", 1), source: fresh(), sources: demoSources(nil)}
		})
	})
	for family, prefix := range map[string]string{"books_demo": "books", "varied_demo": "varied"} {
		t.Run(family, func(t *testing.T) {
			runFamily(t, family, func(row fixtureRow, input *strictjson.Object) setup {
				store := fresh()
				rows, _ := input.Get("rows")
				insert(t, store, rows.(*strictjson.Object), "crm", "calls", "slack")
				return setup{scope: prefix + "/demo/" + row.Key, source: store, sources: demoSources(nil)}
			})
		})
	}
	t.Run("cases", func(t *testing.T) {
		runFamily(t, "cases", func(row fixtureRow, input *strictjson.Object) setup {
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

// TestCardsFromRecordedInputs builds the cards of the other families whose recorded inputs hold every row
// the card reads. The rest need a source adapter that is not ported yet, and are counted by reason.
func TestCardsFromRecordedInputs(t *testing.T) {
	book := oracletest.Read(t, "cards/demo_book.json")
	t.Run("books_calls", func(t *testing.T) {
		runFamily(t, "books_calls", func(row fixtureRow, input *strictjson.Object) setup {
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
	// What each skipped card lacks among its recorded inputs.
	reasons := map[string]func(input *strictjson.Object) string{
		"books_salesforce": func(*strictjson.Object) string {
			return "CRM records: org_rows are Salesforce API rows, which only the Salesforce adapter maps"
		},
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
			runFamily(t, family, func(_ fixtureRow, input *strictjson.Object) setup { return setup{skip: reason(input)} })
		})
	}
}

func intOf(v any) int {
	n, _ := v.(interface{ Int64() int64 })
	return int(n.Int64())
}
