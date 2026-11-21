package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The import store, as tests/test_calls_import.py's StoreTests hold calls_import.py to it. Every file here is
// invented.

const (
	importedNow   = "2026-11-18T12:00:00Z"
	importedLater = "2026-11-18T13:00:00Z"
)

func crmBinding(backend, digit string) *strictjson.Object {
	return strictjson.NewObject("schema_version", 1, "crm_backend", backend, "identity_sha256", strings.Repeat(digit, 64))
}

var (
	demoBinding       = crmBinding("sqlite", "a")
	salesforceBinding = crmBinding("salesforce", "b")
)

// importing is one import of a test: its file, account, range and times.
type importing struct {
	raw      string
	format   string
	account  string
	span     *calls.Range
	calledAt string
	now      string
	receipt  string
	reassign bool
	binding  *strictjson.Object
	number   string
	speakers bool
}

// imports is a fixture's store of imports, with acc-007 and acc-008 beside the fixture's account.
func imports(t *testing.T, f *postgrestest.Fixture) postgres.CallImports {
	t.Helper()
	for _, account := range []string{"acc-007", "acc-008", "acc-sf-001000000000001AAA", "acc-sf-001000000000002AAA"} {
		ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, account, "Invented account"))
	}
	return postgres.CallImports{Store: f.Store, WorkspaceID: f.Workspace.ID}
}

// resolutions numbers the reads that resolve accounts, so each has an operation of its own.
var resolutions atomic.Int64

// resolved commits the read an import's association names, and gives its ID: a get_account read of the
// workspace's CRM that returned the account, or a search_accounts read for an AccountNumber. change alters
// the read before it starts.
func resolved(t *testing.T, store postgres.CallImports, account, number string, change func(*postgres.ReceiptStart)) postgres.Receipt {
	t.Helper()
	workspace, err := store.Store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	ok(t, err)
	crm, err := store.Store.EnsureConnection(ctx, workspace, "crm", "demo")
	ok(t, err)
	start := postgres.ReceiptStart{WorkspaceID: store.WorkspaceID, TokenID: postgres.TokenID("raw-token"),
		OperationID: fmt.Sprintf("resolve-%d", resolutions.Add(1)), Tool: "get_account", Scope: "crm:read",
		AccountRef: account, ConnectionID: crm.ID, Arguments: json.RawMessage(`{"account_id":"` + account + `"}`),
		Lease: time.Minute}
	if number != "" {
		start.Tool, start.AccountRef = "search_accounts", ""
		start.Arguments = json.RawMessage(`{"account_number":"` + number + `"}`)
	}
	if change != nil {
		change(&start)
	}
	read, err := store.Store.StartReceipt(ctx, start)
	ok(t, err)
	return read
}

// returned finishes a read that resolves an account as returning these accounts.
func returned(t *testing.T, store postgres.CallImports, read postgres.Receipt, accounts ...string) string {
	t.Helper()
	records := make([]postgres.RecordVersion, len(accounts))
	for i, account := range accounts {
		records[i] = postgrestest.Record("accounts", account, "id", account)
	}
	_, err := store.Store.FinishRead(ctx, read.ID, read.LeaseToken, records, 0)
	ok(t, err)
	return read.ID
}

// made is the import a test describes, parsed and selected as debug calls import does before it saves. An
// import with no receipt named gets the receipt of a read that resolved its account.
func made(t *testing.T, store postgres.CallImports, i importing) calls.Import {
	t.Helper()
	if i.raw == "" {
		i.raw = "Invented renewal discussion.\nNext steps remain proposed."
	}
	if i.format == "" {
		i.format = "txt"
	}
	if i.account == "" {
		i.account = "acc-007"
	}
	if i.now == "" {
		i.now = importedNow
	}
	if i.binding == nil {
		i.binding = demoBinding
	}
	if i.receipt == "" {
		i.receipt = returned(t, store, resolved(t, store, i.account, i.number, nil), i.account)
	}
	parsed, err := calls.Parse([]byte(i.raw), i.format, i.speakers)
	ok(t, err)
	excerpt, err := calls.SelectExcerpt(parsed, i.span)
	ok(t, err)
	in := calls.Import{Parsed: parsed, Excerpt: excerpt, ImportedAt: i.now, Reassign: i.reassign,
		Association: calls.Association{CRMBinding: i.binding, AccountID: i.account, Method: "account_id",
			ResolutionReceiptID: i.receipt}}
	if i.number != "" {
		in.Association.Method, in.Association.AccountNumber = "account_number", &i.number
	}
	if i.calledAt != "" {
		in.CalledAt = &i.calledAt
	}
	return in
}

func publish(t *testing.T, store postgres.CallImports, i importing) *strictjson.Object {
	t.Helper()
	result, err := store.Import(ctx, made(t, store, i))
	ok(t, err)
	return result
}

func refusedImport(t *testing.T, store postgres.CallImports, i importing, want *calls.Error) {
	t.Helper()
	if _, err := store.Import(ctx, made(t, store, i)); !errors.Is(err, want) {
		t.Fatalf("the import ended with %v, want %v", err, want)
	}
}

// notes is read_call_notes for an account, under a binding.
func notes(t *testing.T, store postgres.CallImports, binding *strictjson.Object, account string) []*strictjson.Object {
	t.Helper()
	records, err := calls.CallNotes(ctx, store, binding, account)
	ok(t, err)
	return records
}

func note(t *testing.T, store postgres.CallImports, account string) *strictjson.Object {
	t.Helper()
	records := notes(t, store, demoBinding, account)
	if len(records) != 1 {
		t.Fatalf("%s reads %d records, want 1", account, len(records))
	}
	return records[0]
}

func at(value any, path ...string) any {
	for _, key := range path {
		value, _ = value.(*strictjson.Object).Get(key)
	}
	return value
}

func text(t *testing.T, value any) string {
	t.Helper()
	encoded, err := strictjson.Dumps(value, strictjson.Quoted)
	ok(t, err)
	return encoded
}

// importRows is every row of the import tables, as text.
func importRows(t *testing.T, f *postgrestest.Fixture) string {
	t.Helper()
	var all []string
	for _, table := range []string{"call_import_stores", "call_import_contents", "call_import_revisions",
		"call_import_numbers", "call_import_active"} {
		all = append(all, table+": "+scalar(t, f.URL,
			`SELECT coalesce(string_agg(x::text, E'\n' ORDER BY x::text), '') FROM `+table+` x`))
	}
	return strings.Join(all, "\n")
}

func TestAnAccountWithNoImportReadsNothing(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	if records := notes(t, store, demoBinding, "acc-007"); len(records) != 0 {
		t.Fatalf("an empty store returned %d records", len(records))
	}
	publish(t, store, importing{})
	if records := notes(t, store, demoBinding, "acc-008"); len(records) != 0 {
		t.Fatalf("an account with no import read %d records", len(records))
	}
	if _, err := calls.CallNotes(ctx, store, demoBinding, "account seven"); !errors.Is(err, calls.ErrInvalid) {
		t.Fatalf("a malformed account ID ended with %v", err)
	}
	if rows := importRows(t, f); strings.Contains(rows, "acc-008") {
		t.Fatalf("a read wrote rows:\n%s", rows)
	}
}

func TestARepeatedImportIsTheSameRevisionAndAChangedOneIsNew(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	raw := "WEBVTT\n\nopening\n00:00.000 --> 00:04.000\n<v Morgan>We need a revised seat count.</v>\n\n" +
		"00:03.500 --> 00:06.000\nThe renewal is approaching.\n\n00:06.000 --> 00:09.000\nSend the order form.\n\n" +
		"closing\n00:09.000 --> 00:12.000\nThank you.\n"
	first := publish(t, store, importing{raw: raw, format: "vtt", span: &calls.Range{First: 1, Last: 1}})
	saved := text(t, note(t, store, "acc-007"))
	if at(first, "status") != "imported" || at(first, "created") != true || at(first, "reassigned") != false {
		t.Fatalf("the first import returned %s", text(t, first))
	}
	// The ID is the SHA-256 of the manifest's canonical text, which the row holds.
	manifest := scalar(t, f.URL, "SELECT manifest FROM call_import_revisions")
	sum := sha256.Sum256([]byte(manifest))
	if at(first, "import_id") != "note-"+hex.EncodeToString(sum[:]) {
		t.Fatalf("the import ID %v is not the digest of its manifest", at(first, "import_id"))
	}

	second := returned(t, store, resolved(t, store, "acc-007", "", nil), "acc-007")
	retry := publish(t, store, importing{raw: raw, format: "vtt", span: &calls.Range{First: 1, Last: 1}, now: importedLater,
		receipt: second})
	if at(retry, "import_id") != at(first, "import_id") || at(retry, "created") != false ||
		at(retry, "status") != "unchanged" || at(retry, "imported_at") != importedNow ||
		at(retry, "resolution_receipt_id") != second || at(first, "resolution_receipt_id") == second {
		t.Fatalf("the repeated import returned %s", text(t, retry))
	}
	if again := text(t, note(t, store, "acc-007")); again != saved {
		t.Fatalf("the repeated import changed the record to %s", again)
	}

	changed := publish(t, store, importing{raw: raw, format: "vtt", span: &calls.Range{First: 3, Last: 3}, now: importedLater})
	if at(changed, "import_id") == at(first, "import_id") {
		t.Fatal("another range reused the revision")
	}
	inspected, err := store.Inspect(ctx, demoBinding, at(first, "import_id").(string), false)
	ok(t, err)
	if at(inspected, "body") != "<v Morgan>We need a revised seat count.</v>\nThe renewal is approaching." {
		t.Fatalf("the earlier revision reads %s", text(t, inspected))
	}
	if at(inspected, "import", "active") != false || at(note(t, store, "acc-007"), "id") != at(changed, "import_id") {
		t.Fatal("the account does not read its newest revision")
	}
	timed := publish(t, store, importing{raw: raw, format: "vtt", span: &calls.Range{First: 3, Last: 3}, calledAt: importedNow})
	if at(timed, "import_id") == at(changed, "import_id") {
		t.Fatal("another call time reused the revision")
	}
	named := "Dana Crest: We will sign.\nMorgan Reed: Good."
	plain := publish(t, store, importing{raw: named})
	stated := publish(t, store, importing{raw: named, speakers: true})
	if at(plain, "import_id") == at(stated, "import_id") || at(stated, "speaker_names") != true ||
		at(note(t, store, "acc-007"), "manifest", "speaker_names") != true {
		t.Fatal("stating speaker names did not make a revision that says so")
	}
	if contents, revisions := scalar(t, f.URL, "SELECT count(*)::text FROM call_import_contents"),
		scalar(t, f.URL, "SELECT count(*)::text FROM call_import_revisions"); contents != "2" || revisions != "5" {
		t.Fatalf("%s files and %s revisions are stored, want 2 and 5", contents, revisions)
	}
}

func TestContentMovesToAnotherAccountOnlyWhenReassigned(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	first := publish(t, store, importing{})
	original := text(t, note(t, store, "acc-007"))
	before := importRows(t, f)
	refusedImport(t, store, importing{account: "acc-008"}, calls.ErrReassignRequired)
	if importRows(t, f) != before {
		t.Fatal("a refused import changed rows")
	}
	second := publish(t, store, importing{account: "acc-008", reassign: true, now: importedLater})
	if at(second, "reassigned") != true || len(notes(t, store, demoBinding, "acc-007")) != 0 ||
		text(t, at(note(t, store, "acc-008"), "manifest", "association_generation")) != "2" {
		t.Fatalf("the reassigned import returned %s", text(t, second))
	}
	refusedImport(t, store, importing{}, calls.ErrReassignRequired)
	third := publish(t, store, importing{reassign: true, now: importedLater})
	if at(third, "import_id") == at(first, "import_id") ||
		text(t, at(note(t, store, "acc-007"), "manifest", "association_generation")) != "3" ||
		len(notes(t, store, demoBinding, "acc-008")) != 0 {
		t.Fatalf("moving the content back returned %s", text(t, third))
	}
	// The first revision is still there to inspect, as it was read.
	inspected, err := store.Inspect(ctx, demoBinding, at(first, "import_id").(string), false)
	ok(t, err)
	if !strings.Contains(original, text(t, at(inspected, "body"))) || at(inspected, "import", "active") != false {
		t.Fatalf("the first revision reads %s", text(t, inspected))
	}
}

func TestReassignmentLeavesANewerImportOfOtherContentActive(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	publish(t, store, importing{raw: "First call."})
	newer := publish(t, store, importing{raw: "Different later call."})
	publish(t, store, importing{raw: "First call.", account: "acc-008", reassign: true})
	if at(note(t, store, "acc-007"), "id") != at(newer, "import_id") || at(note(t, store, "acc-008"), "body") != "First call." {
		t.Fatal("reassigning one file changed what the other account reads")
	}
}

func TestAnAccountNumberMovesOnlyWhenReassignedAndStaysOutOfTheManifest(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	one, two := "acc-sf-001000000000001AAA", "acc-sf-001000000000002AAA"
	sf := func(i importing) importing {
		i.binding = salesforceBinding
		return i
	}
	first := publish(t, store, sf(importing{raw: "First.", account: one, number: "INVENTED-1"}))
	// The same request under another private selector is the same revision.
	retry := publish(t, store, sf(importing{raw: "First.", account: one, number: "INVENTED-2", now: importedLater}))
	if at(retry, "import_id") != at(first, "import_id") || at(retry, "created") != false {
		t.Fatalf("another AccountNumber made %s", text(t, retry))
	}
	record := notes(t, store, salesforceBinding, one)[0]
	if encoded := text(t, record); strings.Contains(encoded, "INVENTED") || at(record, "manifest", "association", "method") != "account_number" {
		t.Fatalf("the record holds the private selector: %s", encoded)
	}
	if numbers := scalar(t, f.URL, `SELECT string_agg(account_number || '=' || account_ref, ' ' ORDER BY account_number)
		FROM call_import_numbers`); numbers != "INVENTED-1="+one+" INVENTED-2="+one {
		t.Fatalf("the numbers are %s", numbers)
	}
	before := importRows(t, f)
	refusedImport(t, store, sf(importing{raw: "Changed bytes.", account: two, number: "INVENTED-1"}), calls.ErrAssociationChanged)
	if importRows(t, f) != before {
		t.Fatal("a refused import changed rows")
	}
	moved := publish(t, store, sf(importing{raw: "Changed bytes.", account: two, number: "INVENTED-1", reassign: true}))
	if at(moved, "reassigned") != true || at(notes(t, store, salesforceBinding, two)[0], "account_id") != two {
		t.Fatalf("the reassigned number returned %s", text(t, moved))
	}
}

func TestAnImportIsRefusedBeforeAnyRowIsWritten(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	empty := importRows(t, f)
	receipt := returned(t, store, resolved(t, store, "acc-007", "", nil), "acc-007")
	refusedImport(t, store, importing{account: "acc-099", receipt: "00000000-0000-4000-8000-000000000001"},
		calls.ErrAccountNotFound)
	elsewhere := postgres.CallImports{Store: f.Store, WorkspaceID: "00000000-0000-4000-8000-000000000000"}
	refusedImport(t, elsewhere, importing{receipt: "00000000-0000-4000-8000-000000000001"}, calls.ErrAccountNotFound)

	in := made(t, store, importing{raw: "Original."})
	forged := *in.Parsed
	forged.Units = append([]calls.Unit(nil), forged.Units...)
	forged.Units[0].Text = "Forged."
	in.Parsed = &forged
	if _, err := store.Import(ctx, in); !errors.Is(err, calls.ErrInvalid) {
		t.Fatalf("a forged parse ended with %v", err)
	}
	in = made(t, store, importing{raw: "Original."})
	shortened := *in.Excerpt
	shortened.Body = "Forged."
	in.Excerpt = &shortened
	if _, err := store.Import(ctx, in); !errors.Is(err, calls.ErrInvalid) {
		t.Fatalf("a forged excerpt ended with %v", err)
	}
	for _, stamp := range []string{"2026-11-18", "2026-11-18T12:00:00", "2026-11-18T12:00:00+01:00", "2026-13-18T12:00:00Z"} {
		refusedImport(t, store, importing{calledAt: stamp, receipt: receipt}, calls.ErrInvalid)
		refusedImport(t, store, importing{now: stamp, receipt: receipt}, calls.ErrInvalid)
	}
	for _, bad := range []importing{{receipt: " "}, {account: "acc-sf-001000000000001AAA", receipt: receipt},
		{number: "INVENTED-1", receipt: receipt}, {binding: crmBinding("other", "a"), receipt: receipt}} {
		refusedImport(t, store, bad, calls.ErrAssociationInvalid)
	}
	long := importing{binding: salesforceBinding, account: "acc-sf-001000000000001AAA", number: strings.Repeat("9", 41),
		receipt: receipt}
	refusedImport(t, store, long, calls.ErrAssociationInvalid)
	if importRows(t, f) != empty {
		t.Fatalf("a refused import wrote rows:\n%s", importRows(t, f))
	}
}

func TestImportsAreBoundToOneCRM(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	item := publish(t, store, importing{})
	other := crmBinding("sqlite", "f")
	refusedImport(t, store, importing{raw: "Another file.", binding: other}, calls.ErrSourceMismatch)
	if _, err := calls.CallNotes(ctx, store, other, "acc-007"); !errors.Is(err, calls.ErrSourceMismatch) {
		t.Fatalf("a read under another binding ended with %v", err)
	}
	if _, err := store.List(ctx, other, nil, 100, nil); !errors.Is(err, calls.ErrSourceMismatch) {
		t.Fatalf("a listing under another binding ended with %v", err)
	}
	if _, err := store.Inspect(ctx, other, at(item, "import_id").(string), false); !errors.Is(err, calls.ErrSourceMismatch) {
		t.Fatalf("an inspection under another binding ended with %v", err)
	}
	if _, err := store.Inspect(ctx, nil, at(item, "import_id").(string), false); !errors.Is(err, calls.ErrSourceMismatch) {
		t.Fatalf("an inspection under no binding ended with %v", err)
	}
}

func TestAFailedImportLeavesEveryRowAsItWas(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	publish(t, store, importing{})
	before := importRows(t, f)
	postgres.SetCrash(f.Store, func(point string) error {
		if point == "call import pointers" {
			return errors.New("stopped before the pointers")
		}
		return nil
	})
	_, err := store.Import(ctx, made(t, store, importing{raw: "A second file.", account: "acc-008"}))
	if !errors.Is(err, calls.ErrIO) {
		t.Fatalf("an import that stopped ended with %v", err)
	}
	if _, err := store.Import(ctx, made(t, store, importing{account: "acc-008", reassign: true})); !errors.Is(err, calls.ErrIO) {
		t.Fatalf("a reassignment that stopped ended with %v", err)
	}
	postgres.SetCrash(f.Store, nil)
	if importRows(t, f) != before || at(note(t, store, "acc-007"), "account_id") != "acc-007" {
		t.Fatal("an import that stopped left rows behind")
	}
}

func TestStoredImportsCannotBeChangedByAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	publish(t, store, importing{})
	publish(t, store, importing{raw: "A second file.", account: "acc-008"})
	for name, statement := range map[string]string{
		"other bytes":               "UPDATE call_import_contents SET raw = 'Changed private source.'",
		"a generation skipped":      "UPDATE call_import_contents SET generation = generation + 2",
		"an account moved in place": "UPDATE call_import_contents SET account_ref = 'acc-003'",
		"bytes deleted":             "DELETE FROM call_import_contents",
		"bytes truncated":           "TRUNCATE call_import_contents CASCADE",
		"another manifest":          "UPDATE call_import_revisions SET manifest = '{}'",
		"another request key":       "UPDATE call_import_revisions SET request_key = repeat('0', 64)",
		"a revision deleted":        "DELETE FROM call_import_revisions",
		"another binding":           "UPDATE call_import_stores SET crm_binding = '{}'",
	} {
		if got := code(sql(t, f, statement)); got != "GT010" {
			t.Errorf("%s: SQLSTATE %q, want GT010", name, got)
		}
	}
	// A row is held to its own digest when it is written, too.
	for name, statement := range map[string]string{
		"bytes under another digest": `INSERT INTO call_import_contents (workspace_id, content_sha256, raw, account_ref,
			generation) SELECT workspace_id, repeat('0', 64), 'Other bytes.', 'acc-007', 1 FROM call_import_stores`,
		"a manifest under another ID": `INSERT INTO call_import_revisions (workspace_id, import_id, request_key,
			content_sha256, account_ref, manifest) SELECT workspace_id, 'note-' || repeat('0', 64), repeat('1', 64),
			content_sha256, account_ref, '{}' FROM call_import_revisions LIMIT 1`,
	} {
		if got := code(sql(t, f, statement)); got != "23514" {
			t.Errorf("%s: SQLSTATE %q, want a check violation", name, got)
		}
	}
	// An account reads only a revision of its own.
	if got := code(sql(t, f, `UPDATE call_import_active a SET import_id = b.import_id FROM call_import_active b
		WHERE a.account_ref = 'acc-007' AND b.account_ref = 'acc-008'`)); got != "23503" {
		t.Errorf("another account's revision: SQLSTATE %q, want a foreign key violation", got)
	}
	if at(note(t, store, "acc-007"), "account_id") != "acc-007" {
		t.Fatal("the stored import no longer reads")
	}
}

func TestOnlyFullInspectionShowsTextOutsideTheExcerpt(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	sentinel := "UNSELECTED_INVENTED_SENTINEL"
	lines := append(append(make([]string, 0, 61), repeated("Selected.", 30)...), sentinel)
	raw := strings.Join(append(lines, repeated("Unselected.", 30)...), "\n")
	item := publish(t, store, importing{raw: raw})
	id := at(item, "import_id").(string)
	normal, err := store.Inspect(ctx, demoBinding, id, false)
	ok(t, err)
	listing, err := store.List(ctx, demoBinding, nil, 100, nil)
	ok(t, err)
	for _, shown := range []any{item, note(t, store, "acc-007"), normal, listing} {
		if encoded := text(t, shown); strings.Contains(encoded, sentinel) || strings.Contains(encoded, "original_text") {
			t.Fatalf("text outside the excerpt is shown: %s", encoded)
		}
	}
	if at(normal, "import", "active") != true || len(at(normal, "units").([]any)) != 21 {
		t.Fatalf("the inspection reads %s", text(t, normal))
	}
	full, err := store.Inspect(ctx, demoBinding, id, true)
	ok(t, err)
	if at(full, "original_text") != raw || len(at(full, "units").([]any)) != 61 {
		t.Fatal("a full inspection does not show the whole file")
	}
	if _, err := store.Inspect(ctx, demoBinding, "note-"+strings.Repeat("0", 64), false); !errors.Is(err, calls.ErrNotFound) {
		t.Fatalf("an unknown import ended with %v", err)
	}
	if _, err := store.Inspect(ctx, demoBinding, "note-1", false); !errors.Is(err, calls.ErrInvalid) {
		t.Fatalf("a malformed import ID ended with %v", err)
	}
}

func repeated(line string, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = line
	}
	return lines
}

// Imports of one workspace run one at a time: the same request made eight times at once saves one revision.
func TestTheSameImportAtOnceIsSavedOnce(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	in := made(t, store, importing{})
	type outcome struct {
		result *strictjson.Object
		err    error
	}
	outcomes := make(chan outcome, 8)
	for range 8 {
		go func() {
			result, err := store.Import(ctx, in)
			outcomes <- outcome{result, err}
		}()
	}
	created := 0
	for range 8 {
		o := <-outcomes
		ok(t, o.err)
		if at(o.result, "created") == true {
			created++
		}
	}
	if revisions := scalar(t, f.URL, "SELECT count(*)::text FROM call_import_revisions"); created != 1 || revisions != "1" {
		t.Fatalf("%d imports were new and %s revisions are stored, want 1 and 1", created, revisions)
	}
}

func TestAFileAtTheSizeLimitKeepsItsRecordSmall(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	prefix := strings.Repeat("Selected first line.\n", 22)
	publish(t, store, importing{raw: prefix + strings.Repeat("x", evidence.MaxRawBytes-len(prefix))})
	record := note(t, store, "acc-007")
	if text(t, at(record, "manifest", "raw_byte_count")) != fmt.Sprint(evidence.MaxRawBytes) ||
		len(text(t, record)) >= 10_000 || len(at(record, "manifest", "units").([]any)) != 21 {
		t.Fatalf("the record of a file at the limit is %d bytes", len(text(t, record)))
	}
}

func TestImportsAreListedInPagesByAccount(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	a := publish(t, store, importing{raw: "First."})
	b := publish(t, store, importing{raw: "Second."})
	publish(t, store, importing{raw: "Other account.", account: "acc-008"})
	account := "acc-007"
	first, err := store.List(ctx, demoBinding, &account, 1, nil)
	ok(t, err)
	next, isText := at(first, "next_after").(string)
	if len(at(first, "imports").([]any)) != 1 || !isText {
		t.Fatalf("the first page is %s", text(t, first))
	}
	second, err := store.List(ctx, demoBinding, &account, 1, &next)
	ok(t, err)
	if at(second, "next_after") != nil || len(at(second, "imports").([]any)) != 1 {
		t.Fatalf("the second page is %s", text(t, second))
	}
	active := map[any]any{}
	for _, page := range []*strictjson.Object{first, second} {
		row := at(page, "imports").([]any)[0]
		active[at(row, "import_id")] = at(row, "active")
	}
	if active[at(a, "import_id")] != false || active[at(b, "import_id")] != true {
		t.Fatalf("the pages mark %v active", active)
	}
	all, err := store.List(ctx, demoBinding, nil, 100, nil)
	ok(t, err)
	if len(at(all, "imports").([]any)) != 3 || at(all, "next_after") != nil {
		t.Fatalf("the whole listing is %s", text(t, all))
	}
	malformed, unknown := "note-1", "account seven"
	for name, list := range map[string]func() (*strictjson.Object, error){
		"a page of 101":       func() (*strictjson.Object, error) { return store.List(ctx, demoBinding, nil, 101, nil) },
		"a page of none":      func() (*strictjson.Object, error) { return store.List(ctx, demoBinding, nil, 0, nil) },
		"a malformed start":   func() (*strictjson.Object, error) { return store.List(ctx, demoBinding, nil, 100, &malformed) },
		"a malformed account": func() (*strictjson.Object, error) { return store.List(ctx, demoBinding, &unknown, 100, nil) },
		"no binding":          func() (*strictjson.Object, error) { return store.List(ctx, nil, nil, 100, nil) },
	} {
		if _, err := list(); !errors.Is(err, calls.ErrInvalid) {
			t.Errorf("%s ended with %v", name, err)
		}
	}
}

// The server's role imports, reads and reassigns: none of it deletes a row.
func TestTheAppRoleImportsAndReads(t *testing.T) {
	f := postgrestest.New(t)
	imports(t, f)
	name := "gtm_app_" + strings.ReplaceAll(f.Workspace.ID[:8], "-", "")
	ok(t, f.Store.EnsureAppRole(ctx, name, "app-password-for-this-test"))
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), f.URL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		conn.Exec(context.Background(), "DROP OWNED BY "+name)
		conn.Exec(context.Background(), "DROP ROLE "+name)
	})
	parsed, err := pgx.ParseConfig(f.URL)
	ok(t, err)
	app, err := postgres.Open(ctx, fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", name,
		"app-password-for-this-test", parsed.Host, parsed.Port, parsed.Database))
	ok(t, err)
	defer app.Close()
	store := postgres.CallImports{Store: app, WorkspaceID: f.Workspace.ID}
	publish(t, store, importing{})
	publish(t, store, importing{account: "acc-008", reassign: true})
	if len(notes(t, store, demoBinding, "acc-007")) != 0 || at(note(t, store, "acc-008"), "account_id") != "acc-008" {
		t.Fatal("the app role did not move the import")
	}
}

// An import's association names the read that resolved its account. The store takes it only when it is this
// workspace's own finished read of the CRM that returned the account.
func TestAnImportNeedsTheReadThatResolvedItsAccount(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	empty := importRows(t, f)
	sf, one, two := salesforceBinding, "acc-sf-001000000000001AAA", "acc-sf-001000000000002AAA"

	other, err := f.Store.EnsureWorkspace(ctx, "other", "demo", "Another workspace")
	ok(t, err)
	ok(t, f.Store.EnsureAccount(ctx, other.ID, "acc-007", "Invented account"))
	crm, err := f.Store.EnsureConnection(ctx, other, "crm", "demo")
	ok(t, err)
	calling, err := f.Store.EnsureConnection(ctx, f.Workspace, "calls", "demo")
	ok(t, err)
	failed := resolved(t, store, "acc-007", "", nil)
	_, err = f.Store.FailRead(ctx, failed.ID, failed.LeaseToken, 200, "request_failed", 0)
	ok(t, err)
	refused, err := f.Store.RecordRefusal(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "refused",
		TokenID: postgres.TokenID("raw-token"), Tool: "get_account", AccountRef: "acc-007",
		Arguments: json.RawMessage(`{"account_id":"acc-007"}`), Lease: time.Minute}, 403, "insufficient_scope")
	ok(t, err)

	for name, c := range map[string]struct {
		receipt string
		in      importing
	}{
		"a receipt that is not an ID":            {"invented-receipt-one", importing{}},
		"an ID that names no receipt":            {"00000000-0000-4000-8000-000000000001", importing{}},
		"a read that is still running":           {resolved(t, store, "acc-007", "", nil).ID, importing{}},
		"a read that failed":                     {failed.ID, importing{}},
		"a call refused before any read":         {refused.ID, importing{}},
		"a read of another account":              {returned(t, store, resolved(t, store, "acc-008", "", nil), "acc-008"), importing{}},
		"a read that did not return the account": {returned(t, store, resolved(t, store, "acc-007", "", nil)), importing{}},
		"another tool's read of the account": {returned(t, store, resolved(t, store, "acc-007", "", func(s *postgres.ReceiptStart) {
			s.Tool = "list_opportunities"
		}), "acc-007"), importing{}},
		"a read of the calls source": {returned(t, store, resolved(t, store, "acc-007", "", func(s *postgres.ReceiptStart) {
			s.Scope, s.ConnectionID = "calls:read", calling.ID
		})), importing{}},
		"another workspace's read": {returned(t, store, resolved(t, store, "acc-007", "", func(s *postgres.ReceiptStart) {
			s.WorkspaceID, s.ConnectionID = other.ID, crm.ID
		}), "acc-007"), importing{}},
		"a search for an account ID": {returned(t, store, resolved(t, store, "acc-007", "INVENTED-1", nil), "acc-007"),
			importing{}},
		"a get_account for an AccountNumber": {returned(t, store, resolved(t, store, one, "", nil), one),
			importing{binding: sf, account: one, number: "INVENTED-1"}},
		"a search that returned another account": {returned(t, store, resolved(t, store, one, "INVENTED-1", nil), two),
			importing{binding: sf, account: one, number: "INVENTED-1"}},
	} {
		c.in.receipt = c.receipt
		if _, err := store.Import(ctx, made(t, store, c.in)); !errors.Is(err, calls.ErrAssociationInvalid) {
			t.Errorf("%s: the import ended with %v", name, err)
		}
	}
	if importRows(t, f) != empty {
		t.Fatalf("a refused import wrote rows:\n%s", importRows(t, f))
	}
	// The read that resolved the account is taken, and so is a search that returned it for its number.
	receipt := returned(t, store, resolved(t, store, "acc-007", "", nil), "acc-007")
	if result := publish(t, store, importing{receipt: receipt}); at(result, "resolution_receipt_id") != receipt {
		t.Fatalf("the import returned %s", text(t, result))
	}
}

// A revision found by its request key is of the bytes its row names. One that points at other bytes than
// the request's is corrupt, and the request does not become its repeat.
func TestARevisionOfOtherBytesIsNotARepeat(t *testing.T) {
	f := postgrestest.New(t)
	store := imports(t, f)
	publish(t, store, importing{raw: "Other bytes."})
	in := made(t, store, importing{raw: "The file of the request."})
	revision, _, err := in.Revision(1)
	ok(t, err)
	// A row no import writes: the request's own manifest and key, over the bytes of the other file.
	ok(t, sql(t, f, `INSERT INTO call_import_revisions (workspace_id, import_id, request_key, content_sha256, account_ref,
		manifest) SELECT workspace_id, $1, $2, content_sha256, account_ref, $3 FROM call_import_contents`,
		revision.ImportID, revision.RequestKey, revision.Manifest))
	before := importRows(t, f)
	if _, err := store.Import(ctx, in); !errors.Is(err, calls.ErrCorrupt) {
		t.Fatalf("the import ended with %v", err)
	}
	if importRows(t, f) != before {
		t.Fatal("the refused import changed rows")
	}
}
