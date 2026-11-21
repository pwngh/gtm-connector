package cards

import (
	"context"
	"errors"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// What admitting a Python card checks beyond its seal, since the folder supplies the seal too.
var (
	// ErrExportLine refuses a receipt line that is not the receipt the card embeds, byte for byte, or not
	// written as the export writes a receipt.
	ErrExportLine = errors.New("A receipt in audit.jsonl is not the receipt the card embeds, or not written as " +
		"the export writes it.")
	// ErrUnsupportedQuotation refuses a card with a quotation that is not a sentence of the record it cites
	// under that speaker, as today's rules split and neutralize the record.
	ErrUnsupportedQuotation = errors.New("A quotation on this card is not a sentence its record holds under that " +
		"speaker.")
)

// ExportLine is a receipt as one line of an audit log's export: the audit log's columns in the receipt's
// own order, compact, with arguments, request_id and rows as the JSON text the log stores them as.
func ExportLine(receipt *strictjson.Object) (string, error) {
	row := receipt.Copy()
	for _, column := range []string{"arguments", "request_id", "rows"} {
		value, _ := receipt.Get(column)
		text, err := strictjson.Dumps(value, strictjson.Quoted)
		if err != nil {
			return "", err
		}
		row.Set(column, text)
	}
	return strictjson.Dumps(row, strictjson.Canonical)
}

// CheckExport checks every line of a verified card's receipts byte for byte. Each line must be written the
// one way the export writes a receipt (ExportLine of its own row), so no byte can change how it is written.
// Each read's row must then be the receipt the card embeds, which the card's digest covers: equal in every
// member, type and nested value, written in one fixed order on both sides, since a prepared brief embeds its
// receipts with their members sorted. What of the seal no digest in the folder covers, its time and
// duration, the export's own digest covers (Folder.ExportSHA256).
func (f *Folder) CheckExport(a *Admitted) error {
	value, err := strictjson.Loads(a.Payload)
	if err != nil {
		return ErrInvalidCard
	}
	snapshot, _ := get(value.(*strictjson.Object), "evidence").(*strictjson.Object)
	embedded, _ := get(snapshot, "receipts").([]any)
	if len(embedded) != len(a.ReadIDs) {
		return ErrExportLine
	}
	rows, err := f.Log.Receipts(context.Background(), append([]string{a.SealID}, a.ReadIDs...))
	if err != nil {
		return ErrExportLine
	}
	for i, row := range rows {
		id := a.SealID
		if i > 0 {
			id = a.ReadIDs[i-1]
		}
		if line, err := ExportLine(row); err != nil || line != string(f.Lines[id]) {
			return ErrExportLine
		}
		if i == 0 {
			continue
		}
		logged, err := strictjson.Dumps(row, strictjson.Prepared)
		sealed, sealedErr := strictjson.Dumps(embedded[i-1], strictjson.Prepared)
		if err != nil || sealedErr != nil || logged != sealed {
			return ErrExportLine
		}
	}
	return nil
}

// CheckQuotations checks that every quotation on a card is a sentence its record holds under that speaker:
// the card's units are built again from the evidence it sealed, under today's rules, and each sealed
// sentence, as today's rules show it, must be one of them, with the same record, span, speaker and text.
func CheckQuotations(payload []byte) error {
	value, err := strictjson.Loads(payload)
	card, _ := value.(*strictjson.Object)
	if err != nil || card == nil {
		return ErrInvalidCard
	}
	snapshot, _ := get(card, "evidence").(*strictjson.Object)
	sealed, _ := get(card, "units").([]any)
	if snapshot == nil {
		return ErrInvalidCard
	}
	built, _, _, _, err := evidence.Build(snapshot)
	if err != nil {
		return ErrUnsupportedQuotation
	}
	sentences := map[string][]*strictjson.Object{}
	for _, unit := range built {
		if unit.Kind == "sentence" {
			sentences[unit.RecordRef] = append(sentences[unit.RecordRef], unit.Value())
		}
	}
	for _, value := range sealed {
		unit, _ := value.(*strictjson.Object)
		if get(unit, "kind") != "sentence" {
			continue
		}
		shown, err := evidence.AsShown(unit)
		if err != nil {
			return ErrUnsupportedQuotation
		}
		ref, _ := get(shown, "record_ref").(string)
		held := false
		for _, candidate := range sentences[ref] {
			if evidence.Equal(get(candidate, "text"), get(shown, "text")) &&
				evidence.Equal(get(candidate, "speaker"), get(shown, "speaker")) &&
				evidence.Equal(get(candidate, "span"), get(shown, "span")) {
				held = true
				break
			}
		}
		if !held {
			return ErrUnsupportedQuotation
		}
	}
	return nil
}

// ImportedTranscript is an imported transcript a card quotes: its account and the SHA-256 of its bytes, as
// the record's manifest names them.
type ImportedTranscript struct {
	AccountID, ContentSHA256 string
}

// ImportedTranscripts lists the imported transcripts a card's units cite.
func ImportedTranscripts(payload []byte) ([]ImportedTranscript, error) {
	value, err := strictjson.Loads(payload)
	card, _ := value.(*strictjson.Object)
	if err != nil || card == nil {
		return nil, ErrInvalidCard
	}
	cited := map[string]bool{}
	units, _ := get(card, "units").([]any)
	for _, value := range units {
		if ref, ok := get(value.(*strictjson.Object), "record_ref").(string); ok {
			cited[ref] = true
		}
	}
	snapshot, _ := get(card, "evidence").(*strictjson.Object)
	records, _ := get(snapshot, "records").([]any)
	var found []ImportedTranscript
	seen := map[ImportedTranscript]bool{}
	for _, value := range records {
		record, _ := value.(*strictjson.Object)
		ref, _ := get(record, "record_ref").(string)
		if record == nil || !cited[ref] || get(record, "record_kind") != "imported_transcript" {
			continue
		}
		manifest, _ := get(record, "manifest").(*strictjson.Object)
		account, accountOK := get(record, "account_id").(string)
		content, contentOK := get(manifest, "content_sha256").(string)
		if !accountOK || !contentOK {
			return nil, ErrInvalidCard
		}
		transcript := ImportedTranscript{account, content}
		if !seen[transcript] {
			seen[transcript] = true
			found = append(found, transcript)
		}
	}
	return found, nil
}
