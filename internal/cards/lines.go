package cards

import (
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// lineRows are a card's lines as its publication writes them. A line that does not encode is written with
// no value, which publication refuses, so the card is not published.
func lineRows(lines []evidence.Line) []receipts.LineRow {
	rows := make([]receipts.LineRow, len(lines))
	for i, l := range lines {
		value, err := strictjson.Dumps(l.Value(), strictjson.Prepared)
		rows[i] = receipts.LineRow{Section: l.Section}
		if err == nil {
			rows[i].Value = []byte(value)
		}
	}
	return rows
}
