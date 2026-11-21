package cards

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
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
					Method: "account_id", ResolutionReceiptID: "invented-receipt"}})
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
