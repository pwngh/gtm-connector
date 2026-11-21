package calls

import (
	"context"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Active is the import an account's cards read: its revision, and the account and generation its content is
// associated with now.
type Active struct {
	Revision
	ContentAccount    string
	ContentGeneration int64
}

// Imports is a store of imports, bound to one CRM, as the calls source reads it.
type Imports interface {
	// ActiveImport gives the account's active import with the bytes of its file, or nil when the account
	// has none. A store bound to another CRM refuses with ErrSourceMismatch.
	ActiveImport(ctx context.Context, binding *strictjson.Object, accountID string) (*Active, error)
}

// Reads is the other reads of a folder's sources, which the calls source passes through.
type Reads interface {
	Read(ctx context.Context, tool, accountID string) (system string, records []*strictjson.Object, err error)
}

// Source is the reads of a folder whose calls are imported transcripts: read_call_notes reads the account's
// active import, and every other read goes to Reads.
type Source struct {
	Reads
	Imports    Imports
	CRMBinding *strictjson.Object
}

// Read makes one source read. ctx is the read's own context: the store's query ends when it is done.
func (s Source) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool != "read_call_notes" {
		return s.Reads.Read(ctx, tool, accountID)
	}
	records, err := CallNotes(ctx, s.Imports, s.CRMBinding, accountID)
	return "calls", records, err
}

// CallNotes is ImportedCallsReader.read_call_notes: the record of the account's active import, checked
// against its stored bytes, or no record when nothing is imported for the account. Demo call notes are
// never returned in its place.
func CallNotes(ctx context.Context, imports Imports, binding *strictjson.Object, accountID string) (
	[]*strictjson.Object, error) {
	backend, bound := evidence.CRMBackend(binding)
	if !bound || !evidence.ValidAccountID(accountID, backend == "salesforce") {
		return nil, ErrInvalid
	}
	active, err := imports.ActiveImport(ctx, binding, accountID)
	if err != nil || active == nil {
		return nil, err
	}
	opened, err := active.Open(binding)
	if err != nil {
		return nil, err
	}
	if member(opened.Record, "account_id") != accountID || active.ContentAccount != accountID ||
		active.ContentGeneration != integer(member(opened.Manifest, "association_generation")) {
		return nil, ErrCorrupt
	}
	opened.Record.Set("record_ref", "calls/call_notes/"+active.ImportID)
	return []*strictjson.Object{opened.Record}, nil
}
