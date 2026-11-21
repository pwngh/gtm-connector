package calls

import (
	"math/big"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// contextUnits is how many adjacent units an excerpt adds on each side of the requested range.
const contextUnits = 1

// provenanceReserve is the room a record keeps for everything but its quoted text and units.
const provenanceReserve = 8192

// Range is a run of units, counted from 1, with both ends included.
type Range struct{ First, Last int }

func (r Range) value() *strictjson.Object {
	return strictjson.NewObject("first", r.First, "last", r.Last)
}

// Excerpt is the units of a transcript that a card may quote: the requested range and one adjacent unit on
// each side where there is one.
type Excerpt struct {
	UnitKind      string // lines or cues
	SelectionMode string // default or explicit
	Requested     Range
	Cited         Range
	TotalUnits    int
	Units         []Unit
	Body          string
}

// value is a unit as a manifest and a command's output hold it.
func (u Unit) value() *strictjson.Object {
	var id, start, end any
	if u.CueID != nil {
		id = *u.CueID
	}
	if u.StartMS != nil {
		start, end = big.NewInt(*u.StartMS), big.NewInt(*u.EndMS)
	}
	return strictjson.NewObject("index", u.Index, "text", u.Text, "cue_id", id, "start_ms", start, "end_ms", end)
}

func unitValues(units []Unit) []any {
	values := make([]any, len(units))
	for i, unit := range units {
		values[i] = unit.value()
	}
	return values
}

// excerpt is calls_import._excerpt: the units from first to last with their context.
func excerpt(parsed *Parsed, first, last int, mode string) *Excerpt {
	total := len(parsed.Units)
	cited := Range{max(1, first-contextUnits), min(total, last+contextUnits)}
	units := append([]Unit(nil), parsed.Units[cited.First-1:cited.Last]...)
	quoted := make([]*strictjson.Object, len(units))
	for i, unit := range units {
		quoted[i] = unit.value()
	}
	kind := "cues"
	if parsed.Format == "txt" {
		kind = "lines"
	}
	return &Excerpt{UnitKind: kind, SelectionMode: mode, Requested: Range{first, last}, Cited: cited, TotalUnits: total,
		Units: units, Body: evidence.QuotedUnits(quoted)}
}

// fits is calls_import._excerpt_fits: the limits on the units, the quoted text and the saved record.
func (e *Excerpt) fits() bool {
	if len(e.Units) > evidence.MaxReturnedUnits || len(e.Body) > evidence.MaxQuoteBytes {
		return false
	}
	encoded, err := strictjson.Dumps(strictjson.NewObject("body", e.Body, "units", unitValues(e.Units)),
		evidence.CanonicalASCII)
	return err == nil && len(encoded) <= evidence.MaxRecordBytes-provenanceReserve
}

// SelectExcerpt is calls_import.select_excerpt: whole units with one adjacent unit on each side. With no
// requested range it is the first twenty units, or fewer where they exceed a limit. A requested range that
// exceeds a limit is refused and never shortened.
func SelectExcerpt(parsed *Parsed, requested *Range) (*Excerpt, error) {
	if parsed == nil {
		return nil, ErrInvalid
	}
	total := len(parsed.Units)
	if parsed.Format != "txt" && parsed.Format != "vtt" && parsed.Format != "srt" || total < 1 ||
		total > evidence.MaxTotalUnits {
		return nil, ErrInvalid
	}
	if requested != nil {
		if requested.First < 1 || requested.Last > total || requested.First > requested.Last {
			return nil, ErrInvalid
		}
		// The count is checked before the units are copied, so a range of the whole file costs nothing.
		if requested.Last-requested.First+1 > evidence.MaxReturnedUnits {
			return nil, ErrExcerptTooLarge
		}
		selected := excerpt(parsed, requested.First, requested.Last, "explicit")
		if !selected.fits() {
			return nil, ErrExcerptTooLarge
		}
		return selected, nil
	}
	for last := min(evidence.DefaultUnits, total); last > 0; last-- {
		if selected := excerpt(parsed, 1, last, "default"); selected.fits() {
			return selected, nil
		}
	}
	return nil, ErrExcerptTooLarge
}
