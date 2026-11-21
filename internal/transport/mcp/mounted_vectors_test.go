package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/oracletest/speakerrules"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The request vectors recorded from the Python gateway, replayed over HTTP at the mounted endpoint: the gate's
// own tables, the write vectors, the three prepared briefs and the six reads of a saved brief in
// testdata/oracle/folders. Each test logs how many it replayed.

// Every vector of the gate's tables gets the recorded status, reply and receipt from the mounted endpoint.
func TestMountedRecordedVectors(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	replayed := mcp.ReplayRecordedVectors(t, mcp.Target{Post: m.post, Receipts: m.outerLabels})
	t.Logf("%d of %d recorded request vectors replayed at the mounted endpoint", replayed, replayed)
	if len(m.book.made()) == 0 {
		t.Fatal("no vector read the demo book")
	}
}

// failing is a receipt store whose Finish, or whose Start, fails while it is told to, as an unavailable
// store would.
type failing struct {
	receipts.Store
	down, downAtStart bool
}

func (f *failing) Start(ctx context.Context, start receipts.Start) (receipts.Receipt, error) {
	if f.downAtStart {
		return receipts.Receipt{}, errors.New("the receipt store is unavailable")
	}
	return f.Store.Start(ctx, start)
}

func (f *failing) FinishRead(ctx context.Context, ticket receipts.Ticket, versions []receipts.RecordVersion, elapsedMS float64) (receipts.Receipt, error) {
	if f.down {
		return receipts.Receipt{}, errors.New("the receipt store is unavailable")
	}
	return f.Store.FinishRead(ctx, ticket, versions, elapsedMS)
}

func (f *failing) Finish(ctx context.Context, ticket receipts.Ticket, outcome receipts.Outcome) (receipts.Receipt, error) {
	if f.down {
		return receipts.Receipt{}, errors.New("the receipt store is unavailable")
	}
	return f.Store.Finish(ctx, ticket, outcome)
}

// The write vectors, recorded while the Python gateway served log_activity. The mounted endpoint refuses each
// with one finished receipt and reads nothing: a read grant gets 403 crm_write_disabled, and the recorded
// write bearers, which the store cannot hold, get 401. The recorded listings come back without the write tool.
func TestMountedWriteVectors(t *testing.T) {
	activity := map[string]any{"account_id": "acc-001", "kind": "renewal_call",
		"body": "Fixture owner requested a renewal call.", "idempotency_key": "fixture-write-001"}
	changed := map[string]any{"account_id": "acc-001", "kind": "renewal_call", "body": "Changed fixture payload.",
		"idempotency_key": "fixture-write-001"}
	missing := map[string]any{"account_id": "acc-missing", "kind": "k", "body": "b", "idempotency_key": "i"}
	// Each write vector's bearer and arguments, as its Python test sent them.
	sent := map[string]struct {
		bearer    string
		arguments map[string]any
	}{
		"test_scopes_separate_both_sources_and_writes: log_activity with crm:read":                {"crm", activity},
		"test_scopes_separate_both_sources_and_writes: log_activity with calls:read":              {"calls", activity},
		"test_every_tool_attempt_has_one_complete_audit_row: attempt 5":                           {"write", activity},
		"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: first":           {"write", activity},
		"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: retry":           {"write", activity},
		"test_idempotent_write_returns_one_activity_and_rejects_changed_payload: changed payload": {"write", changed},
		"test_duplicate_write_attempts_also_consume_rate_limit: first":                            {"write", activity},
		"test_unknown_account_write_returns_an_explicit_audited_tool_error":                       {"write", missing},
		"test_write_committed_before_audit_failure_is_reused_on_retry: first":                     {"write", activity},
		"test_write_committed_before_audit_failure_is_reused_on_retry: retry":                     {"write", activity},
	}
	var store *failing
	m := mount(t, nil, func(c *mcptools.Config) {
		// A short lease, so the receipt the failing store leaves pending can be finished as interrupted.
		store = &failing{Store: &postgres.ReceiptStore{Store: c.Store.(*postgres.Store), Lease: time.Second}}
		c.Receipts = store
	})
	m.fixture()
	// The store holds no token that carries crm:write, so the recorded write bearers are unknown to it.
	for _, name := range []string{"write", "full"} {
		digest := []byte(strings.Repeat("w", 31) + name[:1])
		err := m.sql(`INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, expires_at)
			VALUES ($1, $2, $3, $4, now() + interval '1 hour')`, m.workspace.ID, m.machine.ID, digest, mcp.FixtureScopes()[name])
		if err == nil || !strings.Contains(err.Error(), "23514") {
			t.Fatalf("the store took a token with the scopes of %q: %v", name, err)
		}
	}
	answers := mcp.WriteVectorAnswers()
	names := make([]string, 0, len(answers))
	for name := range answers {
		names = append(names, name)
	}
	sort.Strings(names)
	// Three listings were not recorded over this transport: the Inspector's, which its own job replays, and
	// the two the stdio bridge's tests made.
	elsewhere := []string{mcp.InspectorListing,
		"test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens: tools/list",
		"test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input: tools/list"}
	names = slices.DeleteFunc(names, func(name string) bool { return slices.Contains(elsewhere, name) })
	if len(names)+len(elsewhere) != len(answers) {
		t.Fatalf("a write vector named as recorded elsewhere is not on the list: %v", elsewhere)
	}
	replayed, id := 0, 0
	for _, name := range names {
		want := answers[name]
		id++
		if want == mcp.WantUnlisted {
			listing := m.list(mcp.FixtureBearer("read"))
			var listed []string
			tools, _ := at(decode(t, listing.Body.Bytes()), "result", "tools").([]any)
			for _, tool := range tools {
				listed = append(listed, at(tool, "name").(string))
			}
			if listing.Code != 200 || !reflect.DeepEqual(listed, mcp.ListedTools()) || strings.Contains(listing.Body.String(), "write") {
				t.Errorf("%s: %d %s", name, listing.Code, listing.Body)
			}
			replayed++
			continue
		}
		vector, known := sent[name]
		if !known {
			t.Errorf("write vector %q is not replayed here", name)
			continue
		}
		before := len(m.outer())
		store.down = want == mcp.WantWithheld
		response := m.post(callBody(id, mcp.WriteTool, vector.arguments),
			requestHeaders("tools/call", mcp.WriteTool, mcp.FixtureBearer(vector.bearer)))
		store.down = false
		rows := m.outer()[before:]
		if len(rows) != 1 || rows[0].Tool != mcp.WriteTool || len(rows[0].Rows) != 0 {
			t.Fatalf("%s: receipts %+v", name, rows)
		}
		got, code := refusal(t, response), fmt.Sprint(at(decode(t, response.Body.Bytes()), "error", "code"))
		switch want {
		case mcp.WantRefused:
			if response.Code != 403 || got != "crm_write_disabled" || code != "1005" || rows[0].label() != "log_activity error 403" ||
				response.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("%s: %d %s, receipt %s", name, response.Code, response.Body, rows[0].label())
			}
		case mcp.WantUnauthorized:
			if response.Code != 401 || got != "invalid_token" || code != "1001" || rows[0].label() != "log_activity error 401" ||
				strings.Contains(response.Header().Get("WWW-Authenticate"), "scope=") {
				t.Errorf("%s: %d %s, receipt %s", name, response.Code, response.Body, rows[0].label())
			}
		case mcp.WantWithheld:
			if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"execution_or_audit_unavailable; no result released"}` ||
				rows[0].Status != "pending" {
				t.Errorf("%s: %d %s, receipt %+v", name, response.Code, response.Body, rows[0])
			}
			// Once its lease has run out, the interrupted receipt is finished, as serve's housekeeping does.
			time.Sleep(1100 * time.Millisecond)
			if n, err := m.store.AbandonExpired(ctx, 10); err != nil || n != 1 {
				t.Fatalf("%s: %d receipts abandoned (%v)", name, n, err)
			}
		}
		replayed++
	}
	t.Logf("%d of %d write vectors recorded over HTTP replayed at the mounted endpoint, each refused with its receipt or listed without the write tool",
		replayed, len(names))
	if replayed != len(names) || len(m.book.made()) != 0 {
		t.Fatalf("%d of %d write vectors replayed; the book was read %v", replayed, len(names), m.book.made())
	}
}

// Receipt IDs, start times and so the digest of a saved brief are PostgreSQL's, where the recorder's were
// fixed, and a test bearer is not the recorder's. Before a reply is compared with the recording, exactly
// those values are put back: the digest in the brief's record reference, the card's "Sources read" line,
// which states the first and last start time, the day a contact's title was read, and the receipt ID in
// _meta.
var (
	referencePattern = regexp.MustCompile(`briefs/brief-[0-9a-f]{32}/[0-9a-f]{64}`)
	readLinePattern  = regexp.MustCompile(`Sources read [0-9TZ:-]+( to [0-9TZ:-]+)?\.`)
	auditPattern     = regexp.MustCompile(`"gtm\.demo/auditId": ?"[0-9a-f-]{36}"`)
	titleReadPattern = regexp.MustCompile(` read [0-9]{4}-[0-9]{2}-[0-9]{2} \[`)
)

func putBack(t *testing.T, got, recorded []byte) []byte {
	t.Helper()
	for _, pattern := range []*regexp.Regexp{referencePattern, readLinePattern, auditPattern, titleReadPattern} {
		want := pattern.Find(recorded)
		if want == nil {
			if pattern.Match(got) {
				t.Fatalf("the reply holds %s, which the recording does not", pattern)
			}
			continue
		}
		if !pattern.Match(got) {
			t.Fatalf("the reply holds no %s: %.300s", pattern, got)
		}
		got = pattern.ReplaceAllLiteral(got, want)
	}
	return got
}

// oracleID is the UUID the recorder drew for a brief ID.
func oracleID(briefID string) string {
	hex := strings.TrimPrefix(briefID, "brief-")
	return hex[:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:]
}

// auditRow is one row of the recorder's audit log. Its arguments, request ID and rows are JSON values, which
// a folder's audit.jsonl holds as the text of its SQLite columns and reads.jsonl holds decoded.
type auditRow struct {
	AuditID   string   `json:"audit_id"`
	Tool      string   `json:"tool"`
	Arguments column   `json:"arguments"`
	RequestID column   `json:"request_id"`
	BriefID   *string  `json:"brief_id"`
	Status    string   `json:"status"`
	HTTP      int      `json:"http_status"`
	RowsValue column   `json:"rows"`
	Rows      []string `json:"-"`
	Error     *string  `json:"error"`
	TokenID   string   `json:"token_id"`
}

// column is a JSON value, given as it is or as a string that holds its text.
type column json.RawMessage

func (c *column) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil && json.Valid([]byte(text)) {
		data = []byte(text)
	}
	*c = append(column{}, data...)
	return nil
}

func readAuditRow(t *testing.T, line []byte) auditRow {
	t.Helper()
	var row auditRow
	must(t, json.Unmarshal(line, &row))
	must(t, json.Unmarshal(row.RowsValue, &row.Rows))
	return row
}

func sameJSON[A, B ~[]byte](t *testing.T, a A, b B) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		t.Fatalf("not JSON: %s | %s", a, b)
	}
	return reflect.DeepEqual(x, y)
}

// sameReceipts checks the rows a call wrote against the recorder's audit rows, in order: one outer receipt
// and then each read inside it, numbered from 1 under that receipt, all under the caller's own bearer. The
// IDs, the bearer and the digest in a brief's row are the store's own and are not compared.
func sameReceipts(t *testing.T, got []receipt, want []auditRow, bearer string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d receipts were written; the recording holds %d: %+v", len(got), len(want), got)
	}
	outer := got[0]
	for i, row := range got {
		recorded := want[i]
		problems := []string{}
		check := func(what string, ok bool) {
			if !ok {
				problems = append(problems, what)
			}
		}
		check("tool", row.Tool == recorded.Tool)
		check("status", row.Status == recorded.Status && row.HTTP == recorded.HTTP)
		check("error", (row.Error == nil) == (recorded.Error == nil) && (row.Error == nil || *row.Error == *recorded.Error))
		check("arguments", sameJSON(t, []byte(row.Arguments), recorded.Arguments))
		check("brief_id", (row.Brief == nil) == (recorded.BriefID == nil) && (row.Brief == nil || *row.Brief == *recorded.BriefID))
		check("token", row.TokenID == receipts.Fingerprint(bearer))
		check("principal", row.Principal != nil && outer.Principal != nil && *row.Principal == *outer.Principal)
		check("row count", len(row.Rows) == len(recorded.Rows))
		for j := range min(len(row.Rows), len(recorded.Rows)) {
			check("rows", referencePattern.ReplaceAllString(row.Rows[j], "") == referencePattern.ReplaceAllString(recorded.Rows[j], ""))
		}
		if i == 0 {
			check("outer request_id", sameJSON(t, []byte(row.RequestID), recorded.RequestID) && row.Parent == nil)
		} else {
			child := fmt.Sprintf(`{"kind": "brief_child", "parent_audit_id": %q, "ordinal": %d}`, outer.ID, i)
			var ordinal struct {
				Kind    string `json:"kind"`
				Ordinal int    `json:"ordinal"`
			}
			json.Unmarshal([]byte(recorded.RequestID), &ordinal)
			check("child request_id", sameJSON(t, []byte(row.RequestID), []byte(child)) && ordinal.Kind == "brief_child" &&
				ordinal.Ordinal == i && row.Parent != nil && *row.Parent == outer.ID)
		}
		if len(problems) > 0 {
			t.Errorf("receipt %d differs in %v:\n got %+v\nwant %+v", i, problems, row, recorded)
		}
	}
}

func auditRows(t *testing.T, folder string) []auditRow {
	t.Helper()
	var rows []auditRow
	for _, line := range oracletest.Lines(t, "folders/"+folder+"/audit.jsonl") {
		rows = append(rows, readAuditRow(t, line))
	}
	return rows
}

type recordedBrief struct {
	Selection json.RawMessage `json:"selection"`
	Scopes    []string        `json:"scopes"`
	Result    json.RawMessage `json:"result"`
}

// prepared mounts a recorded folder's selection, with the recorder's brief ID, and a bearer of its scopes.
func prepared(t *testing.T, folder string) (*mounted, recordedBrief, []auditRow, string) {
	t.Helper()
	var recorded recordedBrief
	oracletest.JSON(t, "folders/"+folder+"/tool_result.json", &recorded)
	selection, err := cards.ParseSelection(recorded.Selection, "sqlite")
	must(t, err)
	rows := auditRows(t, folder)
	briefID := *rows[1].BriefID
	m := mount(t, selection, func(c *mcptools.Config) { c.NewID = func() string { return oracleID(briefID) } })
	bearer := "bearer-for-" + folder + "-0000000000000000000000000"
	m.issue(bearer, recorded.Scopes...)
	return m, recorded, rows, bearer
}

// todaysRules is a recorded result as a brief made now gives it. The recorded briefs were sealed under
// rules version 3 and before the speaker rules: the brief the folder saved, made again under them, stands
// in the result for the recorded one.
func todaysRules(t *testing.T, folder string, result []byte) []byte {
	t.Helper()
	saved, err := os.ReadDir(oracletest.Path(t, "folders/"+folder+"/selected-briefs"))
	if err != nil || len(saved) != 1 {
		t.Fatalf("the folder %s does not hold one saved brief: %v", folder, err)
	}
	payload := oracletest.Read(t, "folders/"+folder+"/selected-briefs/"+saved[0].Name())
	rebuilt, err := speakerrules.Payload(oracletest.TodaysRules(payload, evidence.SanitizerVersion))
	must(t, err)
	card, err := strictjson.Loads(rebuilt)
	must(t, err)
	reply, err := speakerrules.Reply(oracletest.TodaysRules(result, evidence.SanitizerVersion), card.(*strictjson.Object))
	must(t, err)
	return reply
}

// The three recorded prepare_renewal_brief calls (folders/prepared, prepared_as_of and prepared_no_slack),
// made at the mounted endpoint: the result the assistant received, as todaysRules gives it, and one outer
// receipt with one receipt for each read inside it, as the recorder's audit log holds them.
func TestMountedPreparedBriefs(t *testing.T) {
	replayed := 0
	for _, folder := range []string{"prepared", "prepared_as_of", "prepared_no_slack"} {
		t.Run(folder, func(t *testing.T) {
			m, recorded, rows, bearer := prepared(t, folder)
			var arguments map[string]any
			must(t, json.Unmarshal([]byte(rows[0].Arguments), &arguments))
			response, outer := m.call(1, "prepare_renewal_brief", arguments, bearer)
			records(t, response)
			structured, err := json.Marshal(at(decode(t, response.Body.Bytes()), "result", "structuredContent"))
			must(t, err)
			want := todaysRules(t, folder, recorded.Result)
			if got := putBack(t, structured, want); !sameJSON(t, got, want) {
				t.Fatalf("the result differs from the recording:\n got %.900s\nwant %.900s", got, want)
			}
			// The text content is the same result, as JSON indented by two.
			text := at(decode(t, response.Body.Bytes()), "result", "content").([]any)[0].(map[string]any)["text"].(string)
			if !sameJSON(t, []byte(text), structured) || !strings.HasPrefix(text, "{\n  \"source\": \"brief\",\n  \"records\": [\n    {\n      \"id\": ") {
				t.Fatalf("the text content is not the result: %.200s", text)
			}
			written := m.receipts("id = $1 OR parent_id = $1", outer.ID)
			sameReceipts(t, written, rows, bearer)
			if total := m.count("SELECT count(*) FROM receipts"); total != len(rows) {
				t.Fatalf("%d receipts in all; the recording holds %d", total, len(rows))
			}
			replayed++
		})
	}
	t.Logf("%d of 3 recorded prepare_renewal_brief calls replayed at the mounted endpoint", replayed)
}

// changedBytes is a store whose saved brief reads back with a newline appended, as the recorder appended one
// to the saved file. The cards table itself refuses any change to a card's bytes.
type changedBytes struct{ mcptools.Storage }

func (c changedBytes) CardByBrief(ctx context.Context, workspaceID, briefID string) (postgres.Card, error) {
	card, err := c.Storage.CardByBrief(ctx, workspaceID, briefID)
	card.Bytes = append(append([]byte{}, card.Bytes...), '\n')
	return card, err
}

// The six recorded read_renewal_brief calls (folders/prepared/reads.jsonl), made at the mounted endpoint
// against a brief prepared there: read again, a missing scope, changed bytes under the seal, a token one
// second before and at its expiry, and a changed selection. Each reply and each receipt is the recording's.
func TestMountedPreparedFolderReads(t *testing.T) {
	type read struct {
		Label     string          `json:"label"`
		Scopes    []string        `json:"scopes"`
		Arguments map[string]any  `json:"arguments"`
		Status    int             `json:"http_status"`
		Response  json.RawMessage `json:"response"`
		Receipts  []auditRow      `json:"receipts"`
	}
	var reads []read
	for _, line := range oracletest.Lines(t, "folders/prepared/reads.jsonl") {
		var r read
		must(t, json.Unmarshal(line, &r))
		for i := range r.Receipts {
			must(t, json.Unmarshal(r.Receipts[i].RowsValue, &r.Receipts[i].Rows))
		}
		reads = append(reads, r)
	}
	replayed := 0
	for _, r := range reads {
		t.Run(r.Label, func(t *testing.T) {
			m, _, rows, preparer := prepared(t, "prepared")
			var arguments map[string]any
			must(t, json.Unmarshal([]byte(rows[0].Arguments), &arguments))
			prepare, _ := m.call(1, "prepare_renewal_brief", arguments, preparer)
			records(t, prepare)
			sources := len(m.book.made())

			bearer := "bearer-for-the-read-000000000000000000000000000"
			m.issue(bearer, r.Scopes...)
			expires := m.expiry(bearer)
			switch r.Label {
			case "changed seal":
				if err := m.sql("UPDATE cards SET card_bytes = card_bytes || '\\x0a'::bytea"); err == nil || !strings.Contains(err.Error(), "GT010") {
					t.Fatalf("the cards table took changed bytes: %v", err)
				}
				m.serve(m.selection, func(c *mcptools.Config) { c.Store = changedBytes{c.Store} })
			case "token one second before expiry":
				m.wall = func() float64 { return expires - 1 }
			case "token at expiry":
				m.wall = func() float64 { return expires }
			case "changed selection":
				m.serve(cards.Selection{"acc-003": true}, nil)
			}
			var id json.Number
			must(t, json.Unmarshal(mustRaw(t, r.Response, "id"), &id))
			response, row := m.call(id, "read_renewal_brief", r.Arguments, bearer)
			if response.Code != r.Status {
				t.Fatalf("status %d, want %d: %s", response.Code, r.Status, response.Body)
			}
			// The brief read is the one prepared here, under today's rules.
			want := todaysRules(t, "prepared", r.Response)
			if got := putBack(t, response.Body.Bytes(), want); !sameJSON(t, got, want) {
				t.Fatalf("the reply differs from the recording:\n got %.900s\nwant %.900s", got, want)
			}
			sameReceipts(t, []receipt{row}, r.Receipts, bearer)
			if len(m.book.made()) != sources {
				t.Fatalf("reading a saved brief read a source: %v", m.book.made()[sources:])
			}
			replayed++
		})
	}
	t.Logf("%d of %d recorded read_renewal_brief calls replayed at the mounted endpoint", replayed, len(reads))
}

func mustRaw(t *testing.T, document json.RawMessage, key string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	must(t, json.Unmarshal(document, &object))
	return object[key]
}
