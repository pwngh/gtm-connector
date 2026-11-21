package evidence

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The lines Build makes for the recorded cards the web app's tests read, kept in testdata/card_lines.jsonl.gz
// as GET /api/v1/cards/{card} serves them: one row per card, with its family, its key and its lines. The
// web tests read a recorded card's lines from that file, so they never hand-write one.

// linesFamilies is the card families whose lines the file holds: the three the deal card's tests read, and
// the Salesforce book, whose cards count to a contract end or have none and have no ARR line.
var linesFamilies = []string{"demo", "cases", "varied_demo", "books_salesforce"}

// recordedLinesFile is every row of the file, built from the recorded cards.
func recordedLinesFile(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, family := range linesFamilies {
		for _, card := range recordedCards(t, family) {
			lines := make([]any, len(card.lines))
			for i, l := range card.lines {
				lines[i] = l.Value()
			}
			row, err := strictjson.Dumps(strictjson.NewObject("family", family, "key", card.key, "lines", lines),
				strictjson.Prepared)
			if err != nil {
				t.Fatal(err)
			}
			out.WriteString(row + "\n")
		}
	}
	return out.Bytes()
}

// TestRecordedLinesFile holds testdata/card_lines.jsonl.gz to the lines Build makes now. With
// GTM_WRITE_CARD_LINES=1 it writes the file instead.
func TestRecordedLinesFile(t *testing.T) {
	path := filepath.Join(filepath.Dir(filepath.Dir(oracletest.Path(t, "manifest.json"))), "card_lines.jsonl.gz")
	want := recordedLinesFile(t)
	if os.Getenv("GTM_WRITE_CARD_LINES") == "1" {
		var zipped bytes.Buffer
		writer := gzip.NewWriter(&zipped)
		if _, err := writer.Write(want); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, zipped.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := got.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("testdata/card_lines.jsonl.gz is not the lines Build makes; run this test with GTM_WRITE_CARD_LINES=1 "+
			"(first difference: %s)", oracletest.FirstDifference(got.String(), string(want)))
	}
}
