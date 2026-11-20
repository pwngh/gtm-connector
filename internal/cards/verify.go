package cards

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Refusals. The messages are briefing._sealed's and selected_briefs.read_brief's codes where they have one.
var (
	ErrNoCard       = errors.New("No saved card has this brief ID in this state folder.")
	ErrBriefID      = errors.New("invalid brief ID")
	ErrSchema2      = errors.New("This card was saved before cards were sealed. Make the card again.")
	ErrInvalidCard  = errors.New("invalid saved brief evidence")
	ErrLinkedLog    = errors.New("Audit database must not be a symbolic link.")
	ErrSealUnread   = errors.New("This card's seal receipt cannot be read from the audit log of this state folder. A card traces only in the folder that made it.")
	ErrSealPending  = errors.New("This card's seal was never finished: saving it was interrupted. Make the card again.")
	ErrNotSealed    = errors.New("This file is not the card that was sealed: the card or its seal receipt was changed.")
	ErrReads        = errors.New("The sealed card's read receipts are not the ones in the audit log of this state folder.")
	ErrForeignReads = errors.New("The sealed brief's reads are not child reads of its own preparation.")
	ErrForeign      = errors.New("This card was read from sources this folder is not bound to.")
	ErrUnresolved   = errors.New("A number on this card does not resolve to a record and a receipt of its own card.")
	// A prepared brief's refusals, by read_renewal_brief's codes.
	ErrBriefNotFound = &CodeError{"brief_not_found"}
	ErrBriefInvalid  = &CodeError{"brief_invalid"}
	ErrNotAccepted   = &CodeError{"brief_not_accepted"}
)

var briefIDPattern = regexp.MustCompile(`^brief-[0-9a-f]{32}$`)

// Log is the audit log a card is verified against.
type Log interface {
	// Receipts returns exactly these receipts, as the log's readers return them (audit.read_ids), in the
	// given order, or an error if any is missing.
	Receipts(ctx context.Context, auditIDs []string) ([]*strictjson.Object, error)
}

// StoreLog is one workspace of a receipts.Store, read as a Log.
type StoreLog struct {
	Store       receipts.Store
	WorkspaceID string
}

func (l StoreLog) Receipts(ctx context.Context, ids []string) ([]*strictjson.Object, error) {
	found, err := l.Store.Read(ctx, l.WorkspaceID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*strictjson.Object, len(found))
	for i, r := range found {
		out[i] = r.Value()
	}
	return out, nil
}

// TrustedRows is an export of a Python audit log's rows, each column as SQLite holds it (arguments,
// request_id and rows as JSON text). It is trusted as given: nothing here checks it against the folder's
// audit.sqlite3.
type TrustedRows map[string]*strictjson.Object

// ErrDuplicateReceipt refuses an export that holds two rows for one audit ID.
var ErrDuplicateReceipt = errors.New("cards: the audit export holds one audit ID twice")

// ReadTrustedRows reads an export written one JSON object a line.
func ReadTrustedRows(data []byte) (TrustedRows, error) {
	log := TrustedRows{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 1<<26)
	for scanner.Scan() {
		value, err := strictjson.Loads(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		row, ok := value.(*strictjson.Object)
		id, _ := row.Get("audit_id")
		text, isText := id.(string)
		if !ok || !isText {
			return nil, errors.New("cards: an audit row has no audit_id")
		}
		if _, seen := log[text]; seen {
			return nil, ErrDuplicateReceipt
		}
		log[text] = row
	}
	return log, scanner.Err()
}

// Receipts is audit.read_ids: 1 to 16 distinct IDs, each row with its JSON columns decoded.
func (l TrustedRows) Receipts(_ context.Context, ids []string) ([]*strictjson.Object, error) {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || len(id) > 64 {
			return nil, errors.New("Select 1 to 16 distinct receipt IDs.")
		}
		seen[id] = true
	}
	if len(ids) < 1 || len(ids) > 16 {
		return nil, errors.New("Select 1 to 16 distinct receipt IDs.")
	}
	out := make([]*strictjson.Object, len(ids))
	for i, id := range ids {
		row, ok := l[id]
		if !ok {
			return nil, receipts.ErrNotFound
		}
		decoded := strictjson.Clone(row).(*strictjson.Object)
		for _, column := range []string{"arguments", "request_id", "rows"} {
			raw, _ := row.Get(column)
			text, ok := raw.(string)
			if !ok {
				return nil, errors.New("Audit receipt contains invalid JSON.")
			}
			value, err := strictjson.LoadsText(text)
			if err != nil {
				return nil, errors.New("Audit receipt contains invalid JSON.")
			}
			decoded.Set(column, value)
		}
		out[i] = decoded
	}
	return out, nil
}

// Admitted is a verified card: its kind, identity, digest, seal and the binding of every unit.
type Admitted struct {
	Kind      string // "card" (saved by brief or demo) or "selected_brief" (saved by prepare_renewal_brief)
	BriefID   string
	AccountID string
	SHA256    string
	SealID    string
	Units     []UnitBinding
}

func get(o *strictjson.Object, key string) any {
	v, _ := o.Get(key)
	return v
}

// decoded reads receipts as validated receipts: a row that is not one (true for a count, a missing column)
// is refused, never compared loosely.
func decoded(values []any) ([]receipts.Receipt, bool) {
	out := make([]receipts.Receipt, len(values))
	for i, value := range values {
		r, err := receipts.Decode(value)
		if err != nil {
			return nil, false
		}
		out[i] = r
	}
	return out, true
}

// sealedReads is the card's read receipts, read from the log and compared exactly with the ones it sealed.
func sealedReads(ctx context.Context, log Log, snapshot *strictjson.Object) ([]receipts.Receipt, bool) {
	list, _ := get(snapshot, "receipts").([]any)
	sealed, ok := decoded(list)
	if !ok {
		return nil, false
	}
	ids := make([]string, len(sealed))
	for i, r := range sealed {
		ids[i] = r.AuditID
	}
	rows, err := log.Receipts(ctx, ids)
	if err != nil || len(rows) != len(sealed) {
		return nil, false
	}
	values := make([]any, len(rows))
	for i, row := range rows {
		values[i] = row
	}
	logged, ok := decoded(values)
	if !ok {
		return nil, false
	}
	for i := range sealed {
		if !sealed[i].Same(logged[i]) {
			return nil, false
		}
	}
	return sealed, true
}

// sameSources is whether a card was read from the sources the folder is bound to.
func sameSources(snapshot *strictjson.Object, s evidence.Sources) bool {
	return evidence.Equal(get(snapshot, "crm_backend"), s.CRMBackend) &&
		evidence.Equal(get(snapshot, "crm_binding"), s.CRMBinding) &&
		evidence.Equal(get(snapshot, "calls_backend"), s.CallsBackend) &&
		evidence.Equal(get(snapshot, "calls_binding"), s.CallsBinding) &&
		evidence.Equal(get(snapshot, "slack_backend"), s.SlackBackend) &&
		evidence.Equal(get(snapshot, "slack_binding"), s.SlackBinding)
}

// resolve binds every unit as units.trace resolves it. A card is admitted only when every number resolves.
func resolve(briefID string, snapshot *strictjson.Object, units []any) ([]UnitBinding, error) {
	trace := evidence.NewTrace(snapshot)
	versions := map[*strictjson.Object]string{}
	bindings := make([]UnitBinding, len(units))
	for i := range units {
		receipt, record, ok := trace.Resolve(units, i+1)
		if !ok {
			return nil, ErrUnresolved
		}
		unit, _ := units[i].(*strictjson.Object)
		ref, _ := get(unit, "record_ref").(string)
		receiptID, isText := get(receipt, "audit_id").(string)
		parts := strings.SplitN(ref, "/", 3)
		if !isText || len(parts) != 3 {
			return nil, ErrUnresolved
		}
		version, cached := versions[record]
		if !cached {
			body, err := strictjson.Dumps(record, strictjson.Prepared)
			if err != nil {
				return nil, ErrUnresolved
			}
			version = RecordVersion{Body: []byte(body)}.Version()
			versions[record] = version
		}
		b := UnitBinding{Unit: UnitRef{briefID, i + 1}, ReceiptID: receiptID,
			Record: RecordVersion{Source: parts[0], Table: parts[1], RecordID: parts[2], ContentVersion: version}}
		if text, ok := get(unit, "text").(string); ok && get(unit, "kind") == "sentence" {
			b.TextSHA256 = sha([]byte(text))
		}
		bindings[i] = b
	}
	return bindings, nil
}

// VerifyCard admits a card saved by brief or demo, as briefing._sealed opens one: its bytes must be the
// ones its finished save_brief receipt names, and its read receipts the ones in the log. It also requires
// the seal to be the one this card's save made (200, no error, no request or brief ID, this account and
// brief, the reads' token), and refuses a card read from sources the folder is not bound to and a card
// with a number that does not resolve. Nothing is re-encoded and nothing is written, so verifying again
// gives the same answer.
func VerifyCard(ctx context.Context, payload []byte, briefID string, log Log, bound evidence.Sources) (*Admitted, error) {
	if !briefIDPattern.MatchString(briefID) {
		return nil, ErrBriefID
	}
	value, err := strictjson.Loads(payload)
	if err != nil {
		return nil, ErrInvalidCard
	}
	artifact, _ := value.(*strictjson.Object)
	if artifact != nil && evidence.Equal(get(artifact, "schema_version"), 2) {
		return nil, ErrSchema2
	}
	snapshot, _ := get(artifact, "evidence").(*strictjson.Object)
	units, unitsOK := get(artifact, "units").([]any)
	_, textOK := get(artifact, "text").(string)
	account, accountOK := get(artifact, "account_id").(string)
	if artifact == nil || !evidence.Equal(get(artifact, "schema_version"), 3) ||
		!evidence.Equal(get(artifact, "brief_id"), briefID) || snapshot == nil ||
		!evidence.Equal(get(snapshot, "brief_id"), briefID) || !accountOK ||
		!evidence.Equal(account, get(snapshot, "account_id")) || !unitsOK || !textOK {
		return nil, ErrInvalidCard
	}
	sealID, ok := evidence.PyStr(get(artifact, "sealed_by"))
	if !ok {
		return nil, ErrSealUnread
	}
	found, err := log.Receipts(ctx, []string{sealID})
	if err != nil {
		return nil, ErrSealUnread
	}
	seal, err := receipts.Decode(found[0])
	if err != nil {
		return nil, ErrNotSealed
	}
	if seal.Tool == "save_brief" && seal.Status == "pending" {
		return nil, ErrSealPending
	}
	if seal.Tool != "save_brief" || seal.Status != "ok" || seal.HTTPStatus == nil || *seal.HTTPStatus != 200 ||
		seal.Error != nil || seal.BriefID != nil || seal.RequestID.Value() != nil ||
		len(seal.Rows) != 1 || seal.Rows[0] != Reference(briefID, payload) ||
		!evidence.Equal(seal.Arguments, strictjson.NewObject("account_id", account, "brief_id", briefID)) {
		return nil, ErrNotSealed
	}
	reads, ok := sealedReads(ctx, log, snapshot)
	if !ok {
		return nil, ErrReads
	}
	for _, read := range reads {
		if read.TokenID != seal.TokenID {
			return nil, ErrNotSealed
		}
	}
	if !sameSources(snapshot, bound) {
		return nil, ErrForeign
	}
	admitted, err := resolve(briefID, snapshot, units)
	if err != nil {
		return nil, err
	}
	return &Admitted{Kind: "card", BriefID: briefID, AccountID: account, SHA256: sha(payload), SealID: sealID,
		Units: admitted}, nil
}

var preparedKeys = []string{"kind", "schema_version", "brief_id", "account_id", "as_of", "prepared_by",
	"bindings", "slack_scopes", "sources", "text", "units", "gaps", "evidence"}

// VerifyPrepared admits a brief saved by prepare_renewal_brief, as read_renewal_brief accepts one: at most
// 4 MiB, its outer receipt finished ok for these exact bytes, this account and this token, and its bindings
// the folder's. It also checks that every read it sealed is in the log and is a child read of that outer
// receipt under the same token, for this account and brief, with its own ordinal, and that every number
// resolves. A brief prepared before gaps were saved has none, and is admitted.
func VerifyPrepared(ctx context.Context, payload []byte, briefID string, log Log, bound evidence.Sources) (*Admitted, error) {
	if !briefIDPattern.MatchString(briefID) {
		return nil, ErrBriefNotFound
	}
	if len(payload) > MaxArtifactBytes {
		return nil, ErrBriefInvalid
	}
	value, err := strictjson.Loads(payload)
	if err != nil {
		return nil, ErrBriefInvalid
	}
	artifact, _ := value.(*strictjson.Object)
	if artifact == nil {
		return nil, ErrBriefInvalid
	}
	keys := map[string]bool{"gaps": true}
	for _, key := range artifact.Keys {
		keys[key] = true
	}
	shape := len(keys) == len(preparedKeys)
	for _, key := range preparedKeys {
		shape = shape && keys[key]
	}
	snapshot, _ := get(artifact, "evidence").(*strictjson.Object)
	preparedBy, _ := get(artifact, "prepared_by").(*strictjson.Object)
	units, unitsOK := get(artifact, "units").([]any)
	_, textOK := get(artifact, "text").(string)
	account, accountOK := get(artifact, "account_id").(string)
	gaps, hasGaps := artifact.Get("gaps")
	_, gapsOK := gaps.([]any)
	outerID, outerOK := get(preparedBy, "outer_audit_id").(string)
	if !shape || get(artifact, "kind") != "selected_brief" || !evidence.Equal(get(artifact, "schema_version"), 3) {
		// The one deliberate difference: read_renewal_brief says brief_invalid here.
		if evidence.Equal(get(artifact, "schema_version"), 2) {
			return nil, ErrSchema2
		}
		return nil, ErrBriefInvalid
	}
	if !evidence.Equal(get(artifact, "brief_id"), briefID) || snapshot == nil ||
		!evidence.Equal(get(snapshot, "brief_id"), briefID) || !accountOK ||
		!evidence.Equal(get(snapshot, "account_id"), account) || preparedBy == nil ||
		!outerOK || !textOK || !unitsOK || hasGaps && !gapsOK {
		return nil, ErrBriefInvalid
	}
	found, err := log.Receipts(ctx, []string{outerID})
	if err != nil {
		return nil, ErrNotAccepted
	}
	outer, err := receipts.Decode(found[0])
	if err != nil {
		return nil, ErrNotAccepted
	}
	if outer.Tool == receipts.Composite && outer.Status == "pending" {
		return nil, ErrSealPending
	}
	arguments, _ := outer.Arguments.(*strictjson.Object)
	if outer.Tool != receipts.Composite || outer.Status != "ok" || outer.HTTPStatus == nil || *outer.HTTPStatus != 200 ||
		len(outer.Rows) != 1 || outer.Rows[0] != Reference(briefID, payload) || arguments == nil ||
		!evidence.Equal(get(arguments, "account_id"), account) ||
		!evidence.Equal(outer.TokenID, get(preparedBy, "token_id")) {
		return nil, ErrNotAccepted
	}
	reads, ok := sealedReads(ctx, log, snapshot)
	if !ok {
		return nil, ErrReads
	}
	ordinals := map[int]bool{}
	for _, read := range reads {
		parent, ordinal, isChild := read.RequestID.Parent()
		if !isChild || parent != outerID || ordinals[ordinal] || read.TokenID != outer.TokenID ||
			read.BriefID == nil || *read.BriefID != briefID ||
			!evidence.Equal(read.Arguments, strictjson.NewObject("account_id", account)) {
			return nil, ErrForeignReads
		}
		ordinals[ordinal] = true
	}
	if !evidence.Equal(get(artifact, "bindings"), bindings(bound)) || !sameSources(snapshot, bound) {
		return nil, ErrForeign
	}
	admitted, err := resolve(briefID, snapshot, units)
	if err != nil {
		return nil, err
	}
	return &Admitted{Kind: "selected_brief", BriefID: briefID, AccountID: account, SHA256: sha(payload),
		SealID: outerID, Units: admitted}, nil
}

// Folder is a Python state folder as a trusted export holds it: the card files, the prepared briefs, and
// the audit log's rows.
type Folder struct {
	Cards    map[string][]byte // briefs/<brief ID>.json
	Prepared map[string][]byte // selected-briefs/<brief ID>.json
	Log      TrustedRows
}

// OpenTrustedExport reads a folder whose audit log is exported as its rows, audit.jsonl, one receipt a line,
// beside the audit database it was exported from. The rows are trusted as the log; the database is not read.
// A link in place of the export or the database is refused, and every file is opened without following a
// link and must be the regular file that was checked.
func OpenTrustedExport(dir string) (*Folder, error) {
	for _, name := range []string{"audit.jsonl", "audit.sqlite3", "audit.sqlite3.gz"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) && name != "audit.jsonl" {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrLinkedLog
		}
	}
	f := &Folder{Cards: map[string][]byte{}, Prepared: map[string][]byte{}}
	// A card saved by brief has no size cap; a prepared brief over its cap is read one byte past it, and
	// refused.
	limits := map[string]int64{"briefs": 1 << 30, "selected-briefs": MaxArtifactBytes}
	for sub, into := range map[string]map[string][]byte{"briefs": f.Cards, "selected-briefs": f.Prepared} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name, ok := strings.CutSuffix(entry.Name(), ".json")
			if !ok {
				continue
			}
			data, err := readRegular(filepath.Join(dir, sub, entry.Name()), limits[sub])
			if err != nil {
				return nil, err
			}
			into[name] = data
		}
	}
	data, err := readRegular(filepath.Join(dir, "audit.jsonl"), 1<<30)
	if err != nil {
		return nil, err
	}
	if f.Log, err = ReadTrustedRows(data); err != nil {
		return nil, err
	}
	return f, nil
}

// ErrNotRegular refuses a link, or anything else that is not a regular file, in place of a folder's file.
var ErrNotRegular = errors.New("cards: not a regular file")

// betweenCheckAndOpen lets a test change a file after it was checked and before it is opened.
var betweenCheckAndOpen func(path string)

// readRegular opens a file without following a link and reads it only if the opened file is the regular
// file Lstat saw, so nothing can be swapped in between the check and the read. A file over limit bytes is
// read up to one byte past it.
func readRegular(path string, limit int64) ([]byte, error) {
	checked, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !checked.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
	}
	if betweenCheckAndOpen != nil {
		betweenCheckAndOpen(path)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNotRegular, filepath.Base(path), err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return nil, fmt.Errorf("%w: %s changed while it was opened", ErrNotRegular, filepath.Base(path))
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

// Verify admits one card or prepared brief of the folder, by its brief ID.
func (f *Folder) Verify(ctx context.Context, briefID string, bound evidence.Sources) (*Admitted, error) {
	if payload, ok := f.Cards[briefID]; ok {
		return VerifyCard(ctx, payload, briefID, f.Log, bound)
	}
	if payload, ok := f.Prepared[briefID]; ok {
		return VerifyPrepared(ctx, payload, briefID, f.Log, bound)
	}
	return nil, ErrNoCard
}
