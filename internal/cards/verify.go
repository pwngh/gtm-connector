package cards

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// The largest files of a folder that are read. Each is checked against its file's size before the file is
// read.
const (
	// MaxCardBytes bounds a card saved by brief or demo, which Python saves at any size. A card's four reads
	// return at most 1 MiB (the account), 1 MiB (its opportunities), 128 KiB (one imported transcript) and
	// 256 KiB (one Slack thread) of records. The card holds each record once, and what it shows of them
	// again in its units and its text, so 64 MiB is over twenty times those records.
	MaxCardBytes = 64 << 20
	// maxAuditRowBytes bounds one row of an audit export. A receipt's largest column is its arguments, which
	// arrive in a request of at most 64 KiB and are written in the row as a JSON string.
	maxAuditRowBytes = 1 << 20
	// maxAuditBytes bounds the whole export. The limits put no bound on how many receipts a folder holds,
	// and the export is read a row at a time, so this stops a file of another kind, not a long log.
	maxAuditBytes = 1 << 30
)

// ErrFileTooLarge refuses a file over the size of what it holds. The file is not read.
var ErrFileTooLarge = errors.New("cards: larger than its kind of file can be")

// ReadTrustedRows reads an export written one JSON object a line, a line at a time. A line over
// maxAuditRowBytes is refused.
func ReadTrustedRows(export io.Reader) (TrustedRows, error) {
	log, _, err := readTrustedLines(export)
	return log, err
}

// readTrustedLines is ReadTrustedRows, with each row's line as the export held it, by audit ID.
func readTrustedLines(export io.Reader) (TrustedRows, map[string][]byte, error) {
	log, lines := TrustedRows{}, map[string][]byte{}
	scanner := bufio.NewScanner(export)
	scanner.Buffer(nil, maxAuditRowBytes)
	for scanner.Scan() {
		value, err := strictjson.Loads(scanner.Bytes())
		if err != nil {
			return nil, nil, err
		}
		row, ok := value.(*strictjson.Object)
		id, _ := row.Get("audit_id")
		text, isText := id.(string)
		if !ok || !isText {
			return nil, nil, errors.New("cards: an audit row has no audit_id")
		}
		if _, seen := log[text]; seen {
			return nil, nil, ErrDuplicateReceipt
		}
		log[text] = row
		lines[text] = append([]byte(nil), scanner.Bytes()...)
	}
	return log, lines, scanner.Err()
}

// RowsFromLines reads rows kept as the lines of an export, by audit ID, as ReadTrustedRows reads the
// export. A line that is not the row of its ID is refused.
func RowsFromLines(lines map[string][]byte) (TrustedRows, error) {
	log := TrustedRows{}
	for id, line := range lines {
		value, err := strictjson.Loads(line)
		if err != nil {
			return nil, err
		}
		row, ok := value.(*strictjson.Object)
		if !ok || get(row, "audit_id") != id {
			return nil, errors.New("cards: an audit row is not the row of its ID")
		}
		log[id] = row
	}
	return log, nil
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
	Payload   []byte   // the bytes that were verified
	ReadIDs   []string // the read receipts the card sealed, in its order
	Units     []UnitBinding
}

func readIDs(reads []receipts.Receipt) []string {
	ids := make([]string, len(reads))
	for i, r := range reads {
		ids[i] = r.AuditID
	}
	return ids
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
		Payload: payload,
		ReadIDs: readIDs(reads), Units: admitted}, nil
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
	return &Admitted{Kind: "selected_brief", BriefID: briefID, AccountID: account, SHA256: sha(payload), Payload: payload,
		SealID: outerID, ReadIDs: readIDs(reads), Units: admitted}, nil
}

// Folder is a Python state folder as a trusted export holds it: the card files, the prepared briefs, and
// the audit log's rows.
type Folder struct {
	Cards    map[string][]byte // briefs/<brief ID>.json
	Prepared map[string][]byte // selected-briefs/<brief ID>.json
	Log      TrustedRows
	// Lines is each row of Log as the export held it, by audit ID.
	Lines map[string][]byte
	// ExportSHA256 is the SHA-256 of the export's bytes, as read.
	ExportSHA256 string
	// tooLarge holds, by sub-folder and brief ID ("briefs/<brief ID>"), the refusal of each file over its
	// size. Such a file was not read.
	tooLarge map[string]error
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
	f := &Folder{Cards: map[string][]byte{}, Prepared: map[string][]byte{}, tooLarge: map[string]error{}}
	// A card file over its size is left unread, and the folder's other cards still open: its card is refused
	// as invalid when it is asked for, as read_renewal_brief refuses a prepared brief over 4 MiB.
	limits := map[string]int64{"briefs": MaxCardBytes, "selected-briefs": MaxArtifactBytes}
	refusals := map[string]error{"briefs": ErrInvalidCard, "selected-briefs": ErrBriefInvalid}
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
			if errors.Is(err, ErrFileTooLarge) {
				f.tooLarge[sub+"/"+name] = refusals[sub]
				continue
			}
			if err != nil {
				return nil, err
			}
			into[name] = data
		}
	}
	export, err := openRegular(filepath.Join(dir, "audit.jsonl"), maxAuditBytes)
	if err != nil {
		return nil, err
	}
	defer export.Close()
	digest := sha256.New()
	if f.Log, f.Lines, err = readTrustedLines(io.TeeReader(io.LimitReader(export, maxAuditBytes+1), digest)); err != nil {
		return nil, err
	}
	f.ExportSHA256 = hex.EncodeToString(digest.Sum(nil))
	// An export that grew past its size while it was read is refused too.
	read, err := export.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if read > maxAuditBytes {
		return nil, fmt.Errorf("%w: audit.jsonl", ErrFileTooLarge)
	}
	return f, nil
}

// ErrNotRegular refuses a link, or anything else that is not a regular file, in place of a folder's file.
var ErrNotRegular = errors.New("cards: not a regular file")

// betweenCheckAndOpen lets a test change a file after it was checked and before it is opened.
var betweenCheckAndOpen func(path string)

// readRegular reads a file that openRegular opens. A file that grew past limit bytes after it was opened is
// refused too.
func readRegular(path string, limit int64) ([]byte, error) {
	file, err := openRegular(path, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s", ErrFileTooLarge, filepath.Base(path))
	}
	return data, err
}

// openRegular opens a file without following a link, and only if the opened file is the regular file Lstat
// saw, so nothing can be swapped in between the check and the read. A file over limit bytes is refused by
// its size, before any of it is read.
func openRegular(path string, limit int64) (*os.File, error) {
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
	opened, err := file.Stat()
	switch {
	case err != nil:
	case !opened.Mode().IsRegular() || !os.SameFile(checked, opened):
		err = fmt.Errorf("%w: %s changed while it was opened", ErrNotRegular, filepath.Base(path))
	case opened.Size() > limit:
		err = fmt.Errorf("%w: %s", ErrFileTooLarge, filepath.Base(path))
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// BriefIDs lists the brief ID of every card and prepared brief file of the folder, those over their size
// included, in order.
func (f *Folder) BriefIDs() []string {
	seen := map[string]bool{}
	for id := range f.Cards {
		seen[id] = true
	}
	for id := range f.Prepared {
		seen[id] = true
	}
	for key := range f.tooLarge {
		_, id, _ := strings.Cut(key, "/")
		seen[id] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ErrTwoFiles refuses a folder that holds a card and a prepared brief of one brief ID.
var ErrTwoFiles = errors.New("This folder holds a card and a prepared brief with this one brief ID.")

// FileError is a refusal of one card file of a folder, by its brief ID.
type FileError struct {
	BriefID string
	Err     error
}

func (e *FileError) Error() string { return e.BriefID + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// VerifyAll admits every card and prepared brief file of the folder, or none: a file that fails its check
// or is over its size refuses the folder, whichever sub-folder it is in, and so do a card and a prepared
// brief of one brief ID.
func (f *Folder) VerifyAll(ctx context.Context, bound evidence.Sources) ([]*Admitted, error) {
	var all []*Admitted
	for _, id := range f.BriefIDs() {
		var found []*Admitted
		for _, sub := range []string{"briefs", "selected-briefs"} {
			if refusal, ok := f.tooLarge[sub+"/"+id]; ok {
				return nil, &FileError{id, refusal}
			}
			var admitted *Admitted
			var err error
			if payload, ok := f.Cards[id]; ok && sub == "briefs" {
				admitted, err = VerifyCard(ctx, payload, id, f.Log, bound)
			} else if payload, ok := f.Prepared[id]; ok && sub == "selected-briefs" {
				admitted, err = VerifyPrepared(ctx, payload, id, f.Log, bound)
			} else {
				continue
			}
			if err != nil {
				return nil, &FileError{id, err}
			}
			found = append(found, admitted)
		}
		if len(found) > 1 {
			return nil, &FileError{id, ErrTwoFiles}
		}
		all = append(all, found...)
	}
	return all, nil
}

// Verify admits one card or prepared brief of the folder, by its brief ID. The card file is read first, so a
// prepared brief of the same ID is looked at only when there is no card file.
func (f *Folder) Verify(ctx context.Context, briefID string, bound evidence.Sources) (*Admitted, error) {
	if refusal, ok := f.tooLarge["briefs/"+briefID]; ok {
		return nil, refusal
	}
	if payload, ok := f.Cards[briefID]; ok {
		return VerifyCard(ctx, payload, briefID, f.Log, bound)
	}
	if refusal, ok := f.tooLarge["selected-briefs/"+briefID]; ok {
		return nil, refusal
	}
	if payload, ok := f.Prepared[briefID]; ok {
		return VerifyPrepared(ctx, payload, briefID, f.Log, bound)
	}
	return nil, ErrNoCard
}
