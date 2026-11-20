package cards

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Refusals. The messages are briefing._sealed's and selected_briefs.read_brief's where they have one.
var (
	ErrNoCard        = errors.New("No saved card has this brief ID in this state folder.")
	ErrSchema2       = errors.New("This card was saved before cards were sealed. Make the card again.")
	ErrInvalidCard   = errors.New("invalid saved brief evidence")
	ErrLinkedLog     = errors.New("Audit database must not be a symbolic link.")
	ErrSealUnread    = errors.New("This card's seal receipt cannot be read from the audit log of this state folder. A card traces only in the folder that made it.")
	ErrSealPending   = errors.New("This card's seal was never finished: saving it was interrupted. Make the card again.")
	ErrNotSealed     = errors.New("This file is not the card that was sealed: the card or its seal receipt was changed.")
	ErrReads         = errors.New("The sealed card's read receipts are not the ones in the audit log of this state folder.")
	ErrForeign       = errors.New("This card was read from sources this folder is not bound to.")
	ErrUnresolved    = errors.New("A number on this card does not resolve to a record and a receipt of its own card.")
	ErrNotAccepted   = errors.New("brief_not_accepted")
	ErrPreparedShape = errors.New("brief_invalid")
)

// Log is the audit log a card is verified against.
type Log interface {
	// Receipts returns exactly these receipts, as the log's readers return them (audit.read_ids), in the
	// given order, or an error if any is missing.
	Receipts(ctx context.Context, auditIDs []string) ([]*strictjson.Object, error)
}

// StoreLog is a receipts.Store read as a Log.
type StoreLog struct{ Store receipts.Store }

func (l StoreLog) Receipts(ctx context.Context, ids []string) ([]*strictjson.Object, error) {
	found, err := l.Store.Read(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*strictjson.Object, len(found))
	for i, r := range found {
		out[i] = r.Value()
	}
	return out, nil
}

// RowLog is the rows of a Python audit log, each column as SQLite holds it: arguments, request_id and rows
// as JSON text.
type RowLog map[string]*strictjson.Object

// ReadRows reads the rows of an audit log written one JSON object a line.
func ReadRows(data []byte) (RowLog, error) {
	log := RowLog{}
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
		log[text] = row
	}
	return log, scanner.Err()
}

// Receipts is audit.read_ids: 1 to 16 distinct IDs, each row with its JSON columns decoded.
func (l RowLog) Receipts(_ context.Context, ids []string) ([]*strictjson.Object, error) {
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
		decoded := row.Copy()
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

// pyStr is str() of the seal reference a card names.
func pyStr(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case nil:
		return "None"
	case *big.Int:
		return v.String()
	}
	return fmt.Sprint(v)
}

func receiptIDs(snapshot *strictjson.Object) []string {
	var ids []string
	list, _ := get(snapshot, "receipts").([]any)
	for _, value := range list {
		if receipt, ok := value.(*strictjson.Object); ok {
			ids = append(ids, pyStr(get(receipt, "audit_id")))
		}
	}
	return ids
}

// sameReads is whether the log holds exactly the read receipts the card sealed.
func sameReads(ctx context.Context, log Log, snapshot *strictjson.Object) bool {
	read, err := log.Receipts(ctx, receiptIDs(snapshot))
	if err != nil {
		return false
	}
	values := make([]any, len(read))
	for i, r := range read {
		values[i] = r
	}
	return evidence.Equal(values, get(snapshot, "receipts"))
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
	records := map[string]*strictjson.Object{}
	list, _ := get(snapshot, "records").([]any)
	for _, value := range list {
		if record, ok := value.(*strictjson.Object); ok {
			if ref, ok := get(record, "record_ref").(string); ok && records[ref] == nil {
				records[ref] = record
			}
		}
	}
	bindings := make([]UnitBinding, len(units))
	for i := range units {
		receiptID, ok := evidence.TraceNumber(snapshot, units, i+1)
		if !ok {
			return nil, ErrUnresolved
		}
		unit := units[i].(*strictjson.Object)
		ref := get(unit, "record_ref").(string)
		parts := strings.SplitN(ref, "/", 3)
		version, err := contentVersion(records[ref])
		if err != nil || len(parts) != 3 {
			return nil, ErrUnresolved
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
// ones its finished save_brief receipt names, and its read receipts the ones in the log. It also refuses a
// card read from sources the folder is not bound to, and a card with a number that does not resolve.
// Nothing is re-encoded and nothing is written, so verifying again gives the same answer.
func VerifyCard(ctx context.Context, payload []byte, briefID string, log Log, linked bool,
	bound evidence.Sources) (*Admitted, error) {
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
	if artifact == nil || !evidence.Equal(get(artifact, "schema_version"), 3) ||
		!evidence.Equal(get(artifact, "brief_id"), briefID) || snapshot == nil ||
		!evidence.Equal(get(snapshot, "brief_id"), briefID) ||
		!evidence.Equal(get(artifact, "account_id"), get(snapshot, "account_id")) || !unitsOK || !textOK {
		return nil, ErrInvalidCard
	}
	if linked {
		return nil, ErrLinkedLog
	}
	reference := Reference(briefID, payload)
	sealID := pyStr(get(artifact, "sealed_by"))
	found, err := log.Receipts(ctx, []string{sealID})
	if err != nil {
		return nil, ErrSealUnread
	}
	seal := found[0]
	if get(seal, "tool") == "save_brief" && get(seal, "status") == "pending" {
		return nil, ErrSealPending
	}
	if get(seal, "tool") != "save_brief" || get(seal, "status") != "ok" ||
		!evidence.Equal(get(seal, "rows"), []any{reference}) {
		return nil, ErrNotSealed
	}
	if !sameReads(ctx, log, snapshot) {
		return nil, ErrReads
	}
	if !sameSources(snapshot, bound) {
		return nil, ErrForeign
	}
	bindings, err := resolve(briefID, snapshot, units)
	if err != nil {
		return nil, err
	}
	account, _ := get(artifact, "account_id").(string)
	return &Admitted{Kind: "card", BriefID: briefID, AccountID: account, SHA256: sha(payload), SealID: sealID,
		Units: bindings}, nil
}

var preparedKeys = []string{"kind", "schema_version", "brief_id", "account_id", "as_of", "prepared_by",
	"bindings", "slack_scopes", "sources", "text", "units", "gaps", "evidence"}

// VerifyPrepared admits a brief saved by prepare_renewal_brief, as read_renewal_brief accepts one: its
// outer receipt finished ok for these exact bytes, this account and this token, and its bindings are the
// folder's. It also checks its read receipts against the log, as VerifyCard does, and that every number
// resolves. A brief prepared before gaps were saved has none, and is admitted.
func VerifyPrepared(ctx context.Context, payload []byte, briefID string, log Log, linked bool,
	bound evidence.Sources) (*Admitted, error) {
	value, err := strictjson.Loads(payload)
	if err != nil {
		return nil, ErrPreparedShape
	}
	artifact, _ := value.(*strictjson.Object)
	if artifact == nil {
		return nil, ErrPreparedShape
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
	gaps, hasGaps := artifact.Get("gaps")
	_, gapsOK := gaps.([]any)
	outerID, outerOK := get(preparedBy, "outer_audit_id").(string)
	if !shape || get(artifact, "kind") != "selected_brief" || !evidence.Equal(get(artifact, "schema_version"), 3) {
		if evidence.Equal(get(artifact, "schema_version"), 2) {
			return nil, ErrSchema2
		}
		return nil, ErrPreparedShape
	}
	if !evidence.Equal(get(artifact, "brief_id"), briefID) || snapshot == nil ||
		!evidence.Equal(get(snapshot, "brief_id"), briefID) ||
		!evidence.Equal(get(snapshot, "account_id"), get(artifact, "account_id")) || preparedBy == nil ||
		!outerOK || !textOK || !unitsOK || hasGaps && !gapsOK {
		return nil, ErrPreparedShape
	}
	if linked {
		return nil, ErrLinkedLog
	}
	reference := Reference(briefID, payload)
	found, err := log.Receipts(ctx, []string{outerID})
	if err != nil {
		return nil, ErrSealUnread
	}
	outer := found[0]
	arguments, _ := get(outer, "arguments").(*strictjson.Object)
	if get(outer, "tool") == "prepare_renewal_brief" && get(outer, "status") == "pending" {
		return nil, ErrSealPending
	}
	if get(outer, "tool") != "prepare_renewal_brief" || get(outer, "status") != "ok" ||
		!evidence.Equal(get(outer, "http_status"), 200) || !evidence.Equal(get(outer, "rows"), []any{reference}) ||
		arguments == nil || !evidence.Equal(get(arguments, "account_id"), get(artifact, "account_id")) ||
		!evidence.Equal(get(outer, "token_id"), get(preparedBy, "token_id")) {
		return nil, ErrNotSealed
	}
	if !sameReads(ctx, log, snapshot) {
		return nil, ErrReads
	}
	if !evidence.Equal(get(artifact, "bindings"), bindings(bound)) || !sameSources(snapshot, bound) {
		return nil, ErrForeign
	}
	admitted, err := resolve(briefID, snapshot, units)
	if err != nil {
		return nil, err
	}
	account, _ := get(artifact, "account_id").(string)
	return &Admitted{Kind: "selected_brief", BriefID: briefID, AccountID: account, SHA256: sha(payload),
		SealID: outerID, Units: admitted}, nil
}

// Folder is a Python state folder as a verifier reads it: the card files, the prepared briefs, and the
// audit log's rows.
type Folder struct {
	Cards     map[string][]byte // briefs/<brief ID>.json
	Prepared  map[string][]byte // selected-briefs/<brief ID>.json
	Log       Log
	LinkedLog bool
}

// OpenFolder reads a folder whose audit log is given as its rows, audit.jsonl, one receipt a line. Every
// file must be a regular file and not a link; a linked audit log is read but vouches for nothing.
func OpenFolder(dir string) (*Folder, error) {
	f := &Folder{Cards: map[string][]byte{}, Prepared: map[string][]byte{}}
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
			data, err := regular(filepath.Join(dir, sub, entry.Name()))
			if err != nil {
				return nil, err
			}
			into[name] = data
		}
	}
	path := filepath.Join(dir, "audit.jsonl")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	f.LinkedLog = info.Mode()&os.ModeSymlink != 0
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if f.Log, err = ReadRows(data); err != nil {
		return nil, err
	}
	return f, nil
}

func regular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cards: %s is not a regular file", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// Verify admits one card or prepared brief of the folder, by its brief ID.
func (f *Folder) Verify(ctx context.Context, briefID string, bound evidence.Sources) (*Admitted, error) {
	if payload, ok := f.Cards[briefID]; ok {
		return VerifyCard(ctx, payload, briefID, f.Log, f.LinkedLog, bound)
	}
	if payload, ok := f.Prepared[briefID]; ok {
		return VerifyPrepared(ctx, payload, briefID, f.Log, f.LinkedLog, bound)
	}
	return nil, ErrNoCard
}
