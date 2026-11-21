package cards

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
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

// importedCalls is a folder whose calls are imported transcripts: the book whose state folder the oracle
// kept the imports in, the store its other reads go to, and the import the oracle made in the card's scope,
// with what that import returned. An account with no transcript has no import.
type importedCalls struct {
	book   string
	reads  *demo.Store
	made   *calls.Import
	result *strictjson.Object
}

// recordedImport is the import the oracle made before one card, from its recorded inputs: the file's text
// through the parser and the default excerpt, bound to the account under the scope's second ID, which is the
// receipt of the read that resolved the account.
func recordedImport(t *testing.T, row fixtureRow, transcript *strictjson.Object, scopeName string) *calls.Import {
	t.Helper()
	parsed, err := calls.Parse([]byte(get(transcript, "text").(string)), get(transcript, "format").(string),
		get(transcript, "speaker_names").(bool))
	if err != nil {
		t.Fatalf("%s: the recorded transcript does not parse: %v", row.Key, err)
	}
	excerpt, err := calls.SelectExcerpt(parsed, nil)
	if err != nil {
		t.Fatalf("%s: the recorded transcript has no excerpt: %v", row.Key, err)
	}
	resolved := newScope(scopeName)
	resolved.uuid()
	made := &calls.Import{Parsed: parsed, Excerpt: excerpt, ImportedAt: get(transcript, "imported_at").(string),
		Association: calls.Association{CRMBinding: evidence.FixtureCRMBinding(), AccountID: row.AccountID,
			Method: "account_id", ResolutionReceiptID: resolved.uuid()}}
	if called, ok := get(transcript, "called_at").(string); ok {
		made.CalledAt = &called
	}
	return made
}

// source is the folder's source over a store of imports, after the import the oracle made is saved by save.
// What the import returns is what the Python import returned, and what read_call_notes then returns for the
// account is the record the oracle card's evidence holds, member for member in the same order: the imported
// transcript, or nothing.
func (f *importedCalls) source(t *testing.T, row fixtureRow, imports calls.Imports,
	save func(calls.Import) (*strictjson.Object, error)) Source {
	t.Helper()
	if f.made != nil {
		result, err := save(*f.made)
		if err != nil {
			t.Fatalf("%s: the recorded transcript is not imported: %v", row.Key, err)
		}
		if !sameOrder(result, f.result) {
			got, _ := strictjson.Dumps(result, strictjson.Quoted)
			want, _ := strictjson.Dumps(f.result, strictjson.Quoted)
			t.Fatalf("%s: the import returned %s, the recorded import %s", row.Key, got, want)
		}
	}
	source := calls.Source{Reads: f.reads, Imports: imports, CRMBinding: evidence.FixtureCRMBinding()}
	_, read, err := source.Read(context.Background(), "read_call_notes", row.AccountID)
	recorded := recordedRows(t, row, "calls/call_notes")
	if err != nil || len(read) != len(recorded) || f.made == nil && len(read) != 0 || f.made != nil && len(read) != 1 {
		t.Fatalf("%s: read_call_notes returned %d records (%v), the card holds %d", row.Key, len(read), err, len(recorded))
	}
	for i, record := range read {
		if !sameOrder(record, recorded[i]) {
			t.Fatalf("%s: the record of the import is not the one the card's evidence holds", row.Key)
		}
	}
	return source
}

// sameOrder compares two JSON values type for type, with the members of every object in the same order.
func sameOrder(a, b any) bool {
	x, errA := strictjson.Dumps(a, strictjson.Quoted)
	y, errB := strictjson.Dumps(b, strictjson.Quoted)
	return errA == nil && errB == nil && x == y
}

// heldImport is a store of imports that holds the one revision an import makes of content no account held
// before, at its first generation. The memory families read through it, with no database. It is the test
// workspace's store unless it names another.
type heldImport struct {
	active    *calls.Active
	workspace string
}

func (h *heldImport) Workspace() string {
	if h.workspace == "" {
		return testWorkspace
	}
	return h.workspace
}

func (h *heldImport) save(made calls.Import) (*strictjson.Object, error) {
	if err := made.Check(); err != nil {
		return nil, err
	}
	revision, manifest, err := made.Revision(1)
	if err != nil {
		return nil, err
	}
	h.active = &calls.Active{Revision: revision, ContentAccount: revision.AccountID, ContentGeneration: 1}
	return calls.Result(manifest, revision.ImportID, true, false, made.Association.ResolutionReceiptID), nil
}

func (h *heldImport) ActiveImport(_ context.Context, _ *strictjson.Object, accountID string) (*calls.Active, error) {
	if h.active == nil || h.active.AccountID != accountID {
		return nil, nil
	}
	return h.active, nil
}

// thread is the live Slack source of a folder: the messages of the thread mapped to the account and the
// snapshot of their read, as the oracle card's evidence records them, or no thread mapped. The Slack reader
// that made them is not ported yet, so they are served as recorded.
type thread struct {
	Source
	messages []*strictjson.Object
	snapshot *strictjson.Object // nil when no thread is mapped to the account
}

func (s thread) ReadThread(context.Context, string) ([]*strictjson.Object, *strictjson.Object, error) {
	if s.snapshot == nil {
		return nil, nil, ErrNotSelected
	}
	return s.messages, s.snapshot, nil
}

// recordedThread is the live Slack source of one oracle card, over the store its other reads go to. The
// Slack connection's binding is recorded only in the card's evidence, so the folder's sources take it from
// there. served is the recorded input: the thread's messages as the Slack API returned them, each of which
// must be the message the card holds at its place.
func recordedThread(t *testing.T, row fixtureRow, input *strictjson.Object, store Source, served any,
	sources *evidence.Sources) Source {
	t.Helper()
	card, _ := load(t, []byte(row.Card)).(*strictjson.Object)
	sealed := get(card, "evidence").(*strictjson.Object)
	sources.SlackBinding = get(sealed, "slack_binding")
	messages := recordedRows(t, row, "slack/messages")
	snapshot, _ := get(sealed, "thread_snapshot").(*strictjson.Object)
	api, _ := served.([]any)
	if len(api) != len(messages) || (snapshot == nil) != (len(api) == 0) ||
		snapshot != nil && get(snapshot, "cutoff_ts") != get(input, "cutoff") {
		t.Fatalf("%s: the card does not hold the thread its inputs record", row.Key)
	}
	for i, message := range messages {
		given := api[i].(*strictjson.Object)
		if get(get(message, "slack_message").(*strictjson.Object), "ts") != get(given, "ts") ||
			get(message, "body") != get(given, "text") {
			t.Fatalf("%s: message %d is not the one its inputs record", row.Key, i+1)
		}
	}
	return thread{store, messages, snapshot}
}

// liveFolder is a folder whose Slack is live: the demo CRM and calls with the account's rows, and the thread
// the card recorded.
func liveFolder(t *testing.T, row fixtureRow, input, rows *strictjson.Object, served any, tables ...string) setup {
	t.Helper()
	store := demoBook(t)
	insert(t, store, rows, tables...)
	sources := demoSources(nil)
	sources.SlackBackend = "slack"
	return setup{source: recordedThread(t, row, input, store, served, &sources), sources: sources}
}

// slackThread is how one card of the slack_threads family is made. Its thread was mapped in the card's own
// scope, which spent three IDs, one receipt time and four tokens there, resolving the account.
func slackThread(t *testing.T, row fixtureRow, input *strictjson.Object) setup {
	t.Helper()
	s := liveFolder(t, row, input, strictjson.NewObject("crm", get(input, "crm")), get(input, "rows"), "crm")
	s.scope, s.ids, s.receipts, s.tokens = "slack_threads/"+row.Key, 3, 1, 4
	return s
}

// recordedRows are the records of one source table that an oracle card's evidence holds, in their order.
func recordedRows(t *testing.T, row fixtureRow, table string) []*strictjson.Object {
	t.Helper()
	card, _ := load(t, []byte(row.Card)).(*strictjson.Object)
	evidence, _ := card.Get("evidence")
	records, _ := evidence.(*strictjson.Object).Get("records")
	var found []*strictjson.Object
	for _, value := range records.([]any) {
		record := value.(*strictjson.Object)
		if ref, _ := record.Get("record_ref"); strings.HasPrefix(ref.(string), table+"/") {
			found = append(found, record)
		}
	}
	return found
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
	// calls is set for a folder of imported transcripts. Its source is made by the card store, which holds
	// the imports.
	calls *importedCalls
	// What the scope spent before the card, on work the oracle did in it first: IDs, receipt times and
	// tokens. An import of a transcript resolves its account under a receipt of its own.
	ids, receipts, tokens int
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
	// lines gives the lines the store holds for a card the maker sealed, as JSON, in order.
	lines(t *testing.T, sealed *Sealed, s setup) []receipts.LineRow
}

// memoryCards builds each card on its own Memory store, under the scope's IDs and clock, as the oracle did.
type memoryCards struct{ stores map[string]*receipts.Memory }

func newMemoryCards(*testing.T) cardStore { return &memoryCards{stores: map[string]*receipts.Memory{}} }

func (c *memoryCards) maker(t *testing.T, sc *scope, s setup, row fixtureRow, b Brief) (*Maker, Brief) {
	if s.calls != nil {
		held := &heldImport{}
		s.source = s.calls.source(t, row, held, held.save)
	}
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

func (c *memoryCards) lines(_ *testing.T, sealed *Sealed, s setup) []receipts.LineRow {
	published, _ := c.stores[s.scope].Published(testWorkspace, sealed.BriefID)
	return published.Lines
}

// storedLines checks the lines a store holds for a card against the lines the recorded card's text prints
// under ATTENTION and RECORD VALUES. It gives how many signal and value lines matched, byte for byte.
func storedLines(t *testing.T, rows []receipts.LineRow, recorded string, mismatches *oracletest.Mismatches,
	key string) (signals, values int) {
	t.Helper()
	card, _ := load(t, []byte(recorded)).(*strictjson.Object)
	text, _ := card.Get("text")
	lines := make([]evidence.Line, len(rows))
	for i, row := range rows {
		value, err := strictjson.Loads(row.Value)
		if err == nil {
			lines[i], err = evidence.ParseLine(value)
		}
		if err != nil || lines[i].Section != row.Section {
			mismatches.Add("%s: stored line %d is not a line: %v", key, i+1, err)
			return 0, 0
		}
	}
	printedSignals, printedValues := evidence.PrintedLines(text.(string))
	if got, want := evidence.RenderLines(lines), slices.Concat(printedSignals, printedValues); !slices.Equal(got, want) {
		mismatches.Add("%s: the stored lines render %q, the recorded text prints %q", key, got, want)
		return 0, 0
	}
	return len(printedSignals), len(printedValues)
}

func runFamily(t *testing.T, family string, newStore func(t *testing.T) cardStore,
	prepare func(row fixtureRow, input *strictjson.Object) setup) {
	mismatches := oracletest.NewMismatches(t, "cards/"+family)
	var cards cardStore
	checked, identical, skipped := 0, 0, map[string]int{}
	signalLines, valueLines := 0, 0
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
		sc.ids, sc.receipts = s.ids, s.receipts
		m, b := cards.maker(t, sc, s, row, o.brief(row.AccountID, sc.token(s.tokens+accessToken)))
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
		signals, values := storedLines(t, cards.lines(t, sealed, s), row.Card, mismatches, row.Key)
		signalLines, valueLines = signalLines+signals, valueLines+values
	}
	t.Logf("%s: %d cards checked, %d byte-identical, %d differ; not built from inputs: %v", family, checked,
		identical, mismatches.Count(), skipped)
	t.Logf("%s: the stored lines render the recorded text's %d signal and %d value lines", family, signalLines,
		valueLines)
	if want := builtCards[family]; checked != want {
		t.Errorf("%s: %d cards built, want %d; a card that stops being built must not pass as skipped", family,
			checked, want)
	}
}

// builtCards pins how many cards of each family Go builds. Raise a count when a port builds more.
var builtCards = map[string]int{
	"demo": 33, "books_demo": 78, "varied_demo": 46, "cases": 19, "books_calls": 82,
	"books_slack": 78, "slack_threads": 36, "books_salesforce": 78,
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

// importedTranscripts is how many books_calls cards read a transcript the oracle imported. Each recorded
// file is in the card's inputs, so every one of them is parsed, imported and read back here.
const importedTranscripts = 50

// recordedInputFamilies builds the cards of the other families. A card of books_calls reads the calls source
// over the import made here from its recorded file. A card that read a live Slack thread reads the records
// its recorded evidence holds, checked against its recorded inputs. A Salesforce card reads its recorded rows
// through the Go reader, served by an invented org.
func recordedInputFamilies(t *testing.T, newStore func(*testing.T) cardStore) {
	book := oracletest.Read(t, "cards/demo_book.json")
	t.Run("books_calls", func(t *testing.T) {
		imported := 0
		runFamily(t, "books_calls", newStore, func(row fixtureRow, input *strictjson.Object) setup {
			store, err := demo.Load(book)
			if err != nil {
				t.Fatal(err)
			}
			insert(t, store, get(input, "rows").(*strictjson.Object), "crm")
			name := get(input, "book").(string)
			sources := demoSources(nil)
			sources.CallsBackend, sources.CallsBinding = "imported", evidence.FixtureCRMBinding()
			folder := &importedCalls{book: name, reads: store}
			transcript, _ := get(input, "transcript").(*strictjson.Object)
			if transcript == nil {
				return setup{scope: fmt.Sprintf("books/calls/%s/%d", name, 300+intOf(get(input, "index"))), calls: folder,
					sources: sources}
			}
			// The import ran in the card's scope and spent two IDs, one receipt time and four tokens there,
			// resolving the account.
			imported++
			scopeName := "books/calls/" + row.Key
			folder.made, folder.result = recordedImport(t, row, transcript, scopeName), get(transcript, "import").(*strictjson.Object)
			return setup{scope: scopeName, calls: folder, sources: sources, ids: 2, receipts: 1, tokens: 4}
		})
		if imported != importedTranscripts {
			t.Errorf("%d recorded transcripts imported, want %d", imported, importedTranscripts)
		}
	})
	t.Run("books_slack", func(t *testing.T) {
		runFamily(t, "books_slack", newStore, func(row fixtureRow, input *strictjson.Object) setup {
			s := liveFolder(t, row, input, get(input, "rows").(*strictjson.Object), get(input, "thread"), "crm", "calls")
			s.scope = fmt.Sprintf("books/slack/%s/%d", get(input, "book"), 300+intOf(get(input, "index")))
			return s
		})
	})
	t.Run("slack_threads", func(t *testing.T) {
		runFamily(t, "slack_threads", newStore, func(row fixtureRow, input *strictjson.Object) setup {
			return slackThread(t, row, input)
		})
	})
	t.Run("books_salesforce", func(t *testing.T) { salesforceBooks(t, newStore, book) })
}

func intOf(v any) int {
	n, _ := v.(interface{ Int64() int64 })
	return int(n.Int64())
}
