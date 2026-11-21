package cards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// limitFile is an invented transcript at both limits of an import: 10,000 units in exactly 2 MiB. Each unit
// is one whole sentence of 200 bytes with its line ending, or its timing line, and the last one is padded to
// the size.
func limitFile(format string) []byte {
	var b strings.Builder
	end := "\n"
	if format == "vtt" {
		b.WriteString("WEBVTT\n\n")
		end = "\n\n"
	}
	at := func(seconds int) string {
		return fmt.Sprintf("%02d:%02d:%02d.000", seconds/3600, seconds/60%60, seconds%60)
	}
	for i := 1; i <= evidence.MaxTotalUnits; i++ {
		start := b.Len()
		if format == "vtt" {
			fmt.Fprintf(&b, "%s --> %s\n", at(i), at(i+1))
		}
		fmt.Fprintf(&b, "Line %05d of the invented call says the renewal moved by a week", i)
		pad := start + 200 - len(end) - 1 - b.Len()
		if i == evidence.MaxTotalUnits {
			pad, end = evidence.MaxRawBytes-1-b.Len(), ""
		}
		b.WriteString(strings.Repeat(" again", pad/6) + strings.Repeat("!", pad%6) + "." + end)
	}
	return []byte(b.String())
}

// A transcript at both limits is imported on PostgreSQL and built into a card. The read that the card makes
// fetches the 2 MiB, parses all 10,000 units again and selects the excerpt again. The times are logged.
func TestATranscriptAtTheLimitsIsImportedAndBuiltIntoACard(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()
	imports := postgres.CallImports{Store: e.store, WorkspaceID: e.workspace}
	sources := demoSources(nil)
	sources.CallsBackend, sources.CallsBinding = "imported", evidence.FixtureCRMBinding()
	for _, format := range []string{"txt", "vtt"} {
		t.Run(format, func(t *testing.T) {
			raw := limitFile(format)
			resolved := e.resolved(t, "acc-003", "")
			start := time.Now()
			parsed, err := calls.Parse(raw, format, false)
			if err != nil || len(raw) != evidence.MaxRawBytes || len(parsed.Units) != evidence.MaxTotalUnits {
				t.Fatalf("the file of %d bytes parses to %v", len(raw), err)
			}
			excerpt, err := calls.SelectExcerpt(parsed, nil)
			if err != nil {
				t.Fatal(err)
			}
			parsing := time.Since(start)
			result, err := imports.Import(ctx, calls.Import{Parsed: parsed, Excerpt: excerpt, ImportedAt: "2027-01-15T12:00:00Z",
				Association: calls.Association{CRMBinding: evidence.FixtureCRMBinding(), AccountID: "acc-003",
					Method: "account_id", ResolutionReceiptID: resolved}})
			if err != nil {
				t.Fatal(err)
			}
			importing := time.Since(start)

			m := &Maker{Receipts: e.receipts, Sources: sources, Connections: e.connections,
				Source: calls.Source{Reads: demoBook(t), Imports: imports, CRMBinding: evidence.FixtureCRMBinding()}}
			start = time.Now()
			sealed, err := m.Brief(ctx, e.brief("limits-"+format))
			if err != nil {
				t.Fatal(err)
			}
			building := time.Since(start)
			quoted := 0
			for _, unit := range sealed.Units {
				if unit.RecordRef == "calls/call_notes/"+get(result, "import_id").(string) {
					quoted++
				}
			}
			if quoted != len(excerpt.Units) {
				t.Errorf("the card quotes %d sentences of the import, want the %d units of its excerpt", quoted, len(excerpt.Units))
			}
			t.Logf("%s, %d units in %d bytes: parsed and selected in %v, imported in %v, card built in %v", format,
				len(parsed.Units), len(raw), parsing.Round(time.Millisecond), importing.Round(time.Millisecond),
				building.Round(time.Millisecond))
		})
	}
}

// spoken is an invented transcript of two lines for the demo book's acc-003, parsed and selected, bound to
// the account under the receipt of the read that resolved it.
func spoken(t *testing.T, receipt string) calls.Import {
	t.Helper()
	parsed, err := calls.Parse([]byte("The renewal moved by a week.\nWe will sign on Friday."), "txt", false)
	if err != nil {
		t.Fatal(err)
	}
	excerpt, err := calls.SelectExcerpt(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return calls.Import{Parsed: parsed, Excerpt: excerpt, ImportedAt: "2027-01-15T12:00:00Z",
		Association: calls.Association{CRMBinding: evidence.FixtureCRMBinding(), AccountID: "acc-003",
			Method: "account_id", ResolutionReceiptID: receipt}}
}

// quotes is whether a card has a unit of a record whose reference starts this way.
func quotes(sealed *Sealed, reference string) bool {
	for _, unit := range sealed.Units {
		if strings.HasPrefix(unit.RecordRef, reference) {
			return true
		}
	}
	return false
}

// importedSources is a folder with the demo CRM and imported transcripts.
func importedSources() evidence.Sources {
	sources := demoSources(nil)
	sources.CallsBackend, sources.CallsBinding = "imported", evidence.FixtureCRMBinding()
	return sources
}

// A store of imports belongs to one workspace, and the maker's principal names one. Paired with another
// workspace's store, which holds a transcript for the same account under the same CRM binding, the maker
// makes no read: the first read's receipt finishes as an error, nothing is sealed, and there is no card.
func TestAnotherWorkspacesTranscriptGivesNoCard(t *testing.T) {
	for workspace, built := range map[string]bool{testWorkspace: true, "workspace-2": false} {
		held := &heldImport{workspace: workspace}
		if _, err := held.save(spoken(t, "invented-receipt")); err != nil {
			t.Fatal(err)
		}
		sc := newScope("imports/" + workspace)
		m, store := sc.maker(calls.Source{Reads: demoBook(t), Imports: held, CRMBinding: evidence.FixtureCRMBinding()},
			importedSources())
		sealed, err := m.Brief(context.Background(), options{}.brief("acc-003", sc.token(1)))
		all := store.All()
		if built {
			if err != nil || !quotes(sealed, "calls/call_notes/note-") {
				t.Fatalf("the workspace's own transcript gave %v", err)
			}
			continue
		}
		if sealed != nil || !errors.Is(err, ErrOtherWorkspace) || !errors.Is(err, ErrBriefing) {
			t.Fatalf("another workspace's transcript gave %v, %v", sealed, err)
		}
		if len(all) != 1 || all[0].Tool != "get_account" || all[0].Status != "error" {
			t.Fatalf("the refused pairing left %d receipts, want one failed read", len(all))
		}
	}
}

func TestAnotherWorkspacesTranscriptGivesNoCardOnPostgres(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()
	own, connections := e.workspace, e.connections
	// The other workspace holds a transcript for acc-003, resolved by a read of its own.
	e.open(t, "other", "Another workspace")
	other := postgres.CallImports{Store: e.store, WorkspaceID: e.workspace}
	if err := e.store.EnsureAccount(ctx, e.workspace, "acc-003", "Invented account"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Import(ctx, spoken(t, e.resolved(t, "acc-003", ""))); err != nil {
		t.Fatal(err)
	}
	e.workspace, e.connections = own, connections
	m := &Maker{Receipts: e.receipts, Sources: importedSources(), Connections: e.connections,
		Source: calls.Source{Reads: demoBook(t), Imports: other, CRMBinding: evidence.FixtureCRMBinding()}}
	sealed, err := m.Brief(ctx, e.brief("wrong-pairing"))
	if sealed != nil || !errors.Is(err, ErrOtherWorkspace) || !errors.Is(err, ErrBriefing) {
		t.Fatalf("another workspace's transcript gave %v, %v", sealed, err)
	}
	if n := e.count(t, "SELECT count(*) FROM cards WHERE workspace_id = $1", own); n != 0 {
		t.Errorf("%d cards were published", n)
	}
	if n := e.count(t, `SELECT count(*) FROM receipts WHERE workspace_id = $1 AND status = 'error'
		AND tool = 'get_account' AND error = 'request_failed'`, own); n != 1 {
		t.Errorf("%d failed reads, want the one the maker refused", n)
	}
	if n := e.count(t, "SELECT count(*) FROM receipts WHERE workspace_id = $1", own); n != 1 {
		t.Errorf("%d receipts in the workspace, want the one failed read", n)
	}
	// Its own store, which holds nothing for the account, gives the card, with the gap.
	m.Source = calls.Source{Reads: demoBook(t), Imports: postgres.CallImports{Store: e.store, WorkspaceID: own},
		CRMBinding: evidence.FixtureCRMBinding()}
	if sealed, err = m.Brief(ctx, e.brief("right-pairing")); err != nil ||
		!strings.Contains(sealed.Text, "no transcript is imported for this account") {
		t.Fatalf("the workspace's own store gave %v", err)
	}
}

// A folder with imported transcripts and live Slack: the calls source passes the thread's read through with
// its snapshot, so the card holds the thread. Over a source that gives the messages with no snapshot the
// card is refused, as it is without imported transcripts.
func TestImportedCallsBesideALiveSlackThread(t *testing.T) {
	const key = "gemini/0"
	var row fixtureRow
	for _, line := range oracletest.Lines(t, "cards/slack_threads.jsonl.gz") {
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Key == key {
			break
		}
	}
	if row.Key != key {
		t.Fatalf("no card %s", key)
	}
	s := slackThread(t, row, load(t, row.Input).(*strictjson.Object))
	s.sources.CallsBackend, s.sources.CallsBinding = "imported", evidence.FixtureCRMBinding()
	live := s.source.(thread)
	if live.snapshot == nil || len(live.messages) == 0 {
		t.Fatalf("%s has no thread", key)
	}
	wrapped := func(reads calls.Reads) (*Sealed, error) {
		sc := newScope(s.scope)
		m, _ := sc.maker(calls.Source{Reads: reads, Imports: &heldImport{}, CRMBinding: evidence.FixtureCRMBinding()},
			s.sources)
		return m.Brief(context.Background(), options{IncludeSlack: true}.brief(row.AccountID, sc.token(1)))
	}
	sealed, err := wrapped(live)
	if err != nil {
		t.Fatalf("imported calls over a live thread gave no card: %v", err)
	}
	card := load(t, sealed.Payload).(*strictjson.Object)
	if !sameJSON(get(get(card, "evidence").(*strictjson.Object), "thread_snapshot"), live.snapshot) {
		t.Error("the card does not hold the thread's snapshot")
	}
	if !quotes(sealed, "slack/messages/") || !strings.Contains(sealed.Text, "no transcript is imported for this account") {
		t.Error("the card does not quote the thread beside the gap for calls")
	}
	var rejected *evidence.Rejected
	if sealed, err = wrapped(unsnapped{live.Source, live.messages}); sealed != nil || !errors.As(err, &rejected) {
		t.Fatalf("messages with no snapshot gave %v, %v", sealed, err)
	}
}
