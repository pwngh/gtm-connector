// Package receipts holds the audit receipts every tool call, source read and card seal leaves, and the
// publication that seals a card.
//
// What every Store keeps:
//
//   - A receipt is committed as pending, in its own transaction, before the work it records starts. If
//     Start fails, the work does not happen. A start names its workspace, principal, scope and an
//     operation ID; one that names a source connection records the connection's generation at commit.
//     Starting the same operation again gives back the receipt the first start made.
//   - A receipt goes from pending to finished once. Only the holder of its lease finishes it. Finishing it
//     again with the same outcome, as a retry after a lost acknowledgement does, returns the finished
//     receipt; another outcome is refused with ErrFinished.
//   - A source read finishes through FinishRead, which stores the immutable record versions it returned,
//     binds exactly those to the receipt and finishes it with their references, all at once. If the
//     connection's generation changed after the read started, nothing is stored and the receipt finishes
//     as error/409 source_changed.
//   - token_id is the SHA-256 fingerprint of the bearer credential, or "anonymous". No store sees a raw
//     credential.
//   - request_id takes four forms: a JSON-RPC id (an integer or a string), null, "<invalid>" for an id of
//     any other JSON type, and {"kind": "brief_child", "parent_audit_id", "ordinal"} for a read inside a
//     composite call. A child read starts only under a pending top-level prepare_renewal_brief receipt of its
//     workspace, takes its principal and token from it, and (parent, ordinal) is unique.
//   - Only an "ok" receipt carries rows. A scope denial finishes as error/403 and a source with no selection
//     for the account as error/200, both with no rows (Denied, NotSelected).
//   - Publish writes a card's bytes and digest, its unit and gap projections, and finishes its seal receipt
//     with the digest, all at once, after rechecking the principal's grant on the account and the
//     generation of every read the card rests on. A failed recheck writes nothing and finishes the seal as
//     error/403 not_permitted or error/409 source_changed.
//   - Receipts handed in are copied, and receipts handed out are copies: no caller can change a committed
//     receipt through a value it holds.
//
// Memory keeps all of this in process, and its tests show it. A PostgreSQL store must keep the same
// contract: the single finish by a guarded UPDATE ... WHERE status = 'pending' and a trigger, the child
// uniqueness by a unique index on (parent, ordinal), and FinishRead and Publish each as one transaction.
package receipts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Store commits receipts, the record versions reads return, and published cards.
type Store interface {
	// Start commits a pending top-level receipt, such as one tool call or a card's seal.
	Start(ctx context.Context, start Start) (Receipt, error)
	// StartChild commits a pending child read of a pending composite call.
	StartChild(ctx context.Context, start ChildStart) (Receipt, error)
	// FinishRead finishes a read ok, storing and binding exactly these record versions, behind the
	// generation fence. It returns ErrSourceChanged, with the receipt finished as an error, when fenced.
	FinishRead(ctx context.Context, ticket Ticket, versions []RecordVersion, elapsedMS float64) (Receipt, error)
	// Finish finishes a receipt with an outcome that binds no records: a denial, a failure, or a call that
	// read nothing.
	Finish(ctx context.Context, ticket Ticket, outcome Outcome) (Receipt, error)
	// Publish writes a card and finishes its seal in one step (see the package documentation).
	Publish(ctx context.Context, publication Publication) (Card, error)
	// Read returns exactly these receipts of the workspace, in this order, or ErrNotFound.
	Read(ctx context.Context, workspaceID string, auditIDs []string) ([]Receipt, error)
	// ByOperation finds the receipt an operation started, with its lease, so a caller that lost an
	// acknowledgement can learn what was committed and finish it.
	ByOperation(ctx context.Context, operationID string) (Receipt, error)
}

// Errors a Store returns.
var (
	ErrNotFound        = errors.New("receipts: receipt not found")
	ErrFinished        = errors.New("receipts: receipt already finished with another outcome")
	ErrLeaseLost       = errors.New("receipts: the caller does not hold this receipt's lease")
	ErrInvalid         = errors.New("receipts: invalid receipt")
	ErrParent          = errors.New("receipts: a child read needs a pending prepare_renewal_brief receipt as its parent")
	ErrDuplicateChild  = errors.New("receipts: the parent already has a child read with this ordinal")
	ErrOperationReused = errors.New("receipts: the operation ID names a different request")
	ErrSourceChanged   = errors.New("receipts: the source changed after the read started")
	ErrVersionConflict = errors.New("receipts: a record version already exists with a different body")
	ErrNotPermitted    = errors.New("receipts: the principal holds no grant on the account")
)

// Anonymous is the token_id of a call that carried no credential.
const Anonymous = "anonymous"

// Composite is the one tool whose reads are child reads.
const Composite = "prepare_renewal_brief"

// Fingerprint is the token_id of a bearer credential.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validTokenID(s string) bool {
	if s == Anonymous {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type form int

const (
	null form = iota // the zero RequestID is null
	rpcID
	child
)

// RequestID is a receipt's request_id. The zero value is null.
type RequestID struct {
	form    form
	rpc     any // string or *big.Int
	parent  string
	ordinal int
}

// RPC is a JSON-RPC request id as the gateway records it: an integer or a string as given, null for nil
// (a typed nil integer included), and "<invalid>" for any other JSON value, so a caller cannot imitate the
// server's own correlation for child reads.
func RPC(id any) RequestID {
	switch v := id.(type) {
	case nil:
		return Null()
	case string:
		return RequestID{form: rpcID, rpc: v}
	case int:
		return RequestID{form: rpcID, rpc: big.NewInt(int64(v))}
	case int64:
		return RequestID{form: rpcID, rpc: big.NewInt(v)}
	case *big.Int:
		if v == nil {
			return Null()
		}
		return RequestID{form: rpcID, rpc: new(big.Int).Set(v)}
	}
	return Invalid()
}

// Null is the request_id of a receipt no JSON-RPC request carried, such as a card's seal.
func Null() RequestID { return RequestID{} }

// Invalid is the request_id recorded for an id that is neither an integer nor a string.
func Invalid() RequestID { return RequestID{form: rpcID, rpc: "<invalid>"} }

// Child is the request_id of the ordinal-th source read inside the composite call parent. A Store refuses
// an empty parent or an ordinal below 1.
func Child(parent string, ordinal int) RequestID {
	return RequestID{form: child, parent: parent, ordinal: ordinal}
}

// Parent is the parent receipt's ID and the ordinal of a child read.
func (r RequestID) Parent() (string, int, bool) { return r.parent, r.ordinal, r.form == child }

// Value is the request_id as the receipt's JSON holds it; a new value on every call.
func (r RequestID) Value() any {
	switch r.form {
	case child:
		return strictjson.NewObject("kind", "brief_child", "parent_audit_id", r.parent, "ordinal", r.ordinal)
	case rpcID:
		return strictjson.Clone(r.rpc)
	}
	return nil
}

func (r RequestID) clone() RequestID {
	r.rpc = strictjson.Clone(r.rpc)
	return r
}

// asInt is an integer of a JSON value, never a bool.
func asInt(v any) (*big.Int, bool) {
	switch n := v.(type) {
	case *big.Int:
		return n, n != nil
	case int:
		return big.NewInt(int64(n)), true
	case int64:
		return big.NewInt(n), true
	}
	return nil, false
}

// ParseRequestID reads a request_id from a receipt's JSON.
func ParseRequestID(value any) (RequestID, error) {
	switch v := value.(type) {
	case nil:
		return Null(), nil
	case string:
		return RequestID{form: rpcID, rpc: v}, nil
	case *big.Int:
		if v != nil {
			return RPC(v), nil
		}
		return Null(), nil
	case *strictjson.Object:
		kind, _ := v.Get("kind")
		parent, _ := v.Get("parent_audit_id")
		p, okParent := parent.(string)
		ordinal, _ := v.Get("ordinal")
		n, okOrdinal := asInt(ordinal)
		if len(v.Keys) == 3 && kind == "brief_child" && okParent && p != "" && okOrdinal && n.IsInt64() &&
			n.Int64() >= 1 && n.Int64() <= 1<<31 {
			return Child(p, int(n.Int64())), nil
		}
	}
	return RequestID{}, fmt.Errorf("%w: request_id", ErrInvalid)
}

// Start is what a top-level receipt records before its work starts.
type Start struct {
	WorkspaceID string
	OperationID string // the same across retries of one request
	PrincipalID string // empty when no principal is known
	TokenID     string // Fingerprint of the bearer, or Anonymous
	Tool        string
	Scope       string // empty when the tool needs none
	AccountRef  string // empty when the call names no account
	// ConnectionID names the source connection a read uses; its generation is recorded at commit.
	ConnectionID string
	Arguments    any // a JSON value, usually an *strictjson.Object
	RequestID    RequestID
	BriefID      *string
}

// ChildStart is what a child read records before the read. The workspace, principal and token are the
// parent's.
type ChildStart struct {
	Parent       string
	Ordinal      int // from 1
	Tool         string
	Scope        string
	ConnectionID string
	AccountRef   string
	Arguments    any
	BriefID      *string
}

// Ticket is what finishes a receipt: its ID and the lease only its starter holds.
type Ticket struct{ AuditID, Lease string }

// Outcome is how a receipt finishes.
type Outcome struct {
	Status     string // "ok" or "error"
	HTTPStatus int
	Rows       []string // only an ok receipt has rows
	ElapsedMS  float64
	Error      *string
}

func failed(status int, code string) Outcome {
	return Outcome{Status: "error", HTTPStatus: status, Rows: []string{}, Error: &code}
}

// Denied finishes a read the grant does not cover: error/403 with no rows.
func Denied() Outcome { return failed(403, "request_failed") }

// NotSelected finishes a read of a source that has no selection for the account: error/200 with no rows.
func NotSelected() Outcome { return failed(200, "request_failed") }

// Failed finishes work that failed with this HTTP status and error code, with no rows.
func Failed(status int, code string) Outcome { return failed(status, code) }

func (o Outcome) check() error {
	switch {
	case o.Status != "ok" && o.Status != "error":
		return fmt.Errorf("%w: a final status is ok or error", ErrInvalid)
	case o.Status != "ok" && len(o.Rows) > 0:
		return fmt.Errorf("%w: only an ok receipt has rows", ErrInvalid)
	case o.HTTPStatus < 100 || o.HTTPStatus > 599:
		return fmt.Errorf("%w: HTTP status %d", ErrInvalid, o.HTTPStatus)
	}
	return nil
}

// RecordVersion is one record as a read returned it: identified by source, table, record ID and content
// version, with the body it was read as.
type RecordVersion struct {
	Source, Table, RecordID string
	// ContentVersion names this version. Empty means the SHA-256 of Body in hex.
	ContentVersion string
	Body           []byte
}

// Ref is the version's source/table/id reference.
func (v RecordVersion) Ref() string { return v.Source + "/" + v.Table + "/" + v.RecordID }

// Version is the content version, the digest of the body when none is given.
func (v RecordVersion) Version() string {
	if v.ContentVersion != "" {
		return v.ContentVersion
	}
	sum := sha256.Sum256(v.Body)
	return hex.EncodeToString(sum[:])
}

func (v RecordVersion) clone() RecordVersion {
	v.Body = append([]byte(nil), v.Body...)
	return v
}

// Receipt is one receipt: the audit log's columns, then what Go records beside them.
type Receipt struct {
	AuditID    string
	StartedAt  time.Time
	TokenID    string
	Tool       string
	Arguments  any
	RequestID  RequestID
	BriefID    *string
	Status     string // "pending", "ok" or "error"
	HTTPStatus *int
	Rows       []string
	ElapsedMS  *float64
	Error      *string

	WorkspaceID, OperationID, PrincipalID string
	Scope, AccountRef, ConnectionID       string
	Generation                            int64
	// Lease finishes the receipt. Start, StartChild and ByOperation give it; Read does not.
	Lease string
}

// Ticket is what finishes this receipt.
func (r Receipt) Ticket() Ticket { return Ticket{r.AuditID, r.Lease} }

// StartedAtText is started_at as the log writes it: UTC, with microseconds only when there are any.
func StartedAtText(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000000Z")
}

func copyPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// clone is a receipt that shares nothing with r.
func (r Receipt) clone() Receipt {
	r.Arguments = strictjson.Clone(r.Arguments)
	r.RequestID = r.RequestID.clone()
	r.BriefID, r.HTTPStatus, r.ElapsedMS, r.Error =
		copyPointer(r.BriefID), copyPointer(r.HTTPStatus), copyPointer(r.ElapsedMS), copyPointer(r.Error)
	r.Rows = append([]string{}, r.Rows...)
	return r
}

// Value is the receipt as the audit log's readers return it, its members in the log's column order. It
// is new on every call.
func (r Receipt) Value() *strictjson.Object {
	rows := make([]any, len(r.Rows))
	for i, row := range r.Rows {
		rows[i] = row
	}
	var brief, status, elapsed, errorCode any
	if r.BriefID != nil {
		brief = *r.BriefID
	}
	if r.HTTPStatus != nil {
		status = *r.HTTPStatus
	}
	if r.ElapsedMS != nil {
		elapsed = *r.ElapsedMS
	}
	if r.Error != nil {
		errorCode = *r.Error
	}
	return strictjson.NewObject(
		"audit_id", r.AuditID, "started_at", StartedAtText(r.StartedAt), "token_id", r.TokenID, "tool", r.Tool,
		"arguments", strictjson.Clone(r.Arguments), "request_id", r.RequestID.Value(), "brief_id", brief,
		"status", r.Status, "http_status", status, "row_count", len(r.Rows), "rows", rows,
		"elapsed_ms", elapsed, "error", errorCode)
}

// Same is whether two receipts hold the same audit log columns, compared exactly as JSON.
func (r Receipt) Same(o Receipt) bool {
	a, errA := strictjson.Dumps(r.Value(), strictjson.Prepared)
	b, errB := strictjson.Dumps(o.Value(), strictjson.Prepared)
	return errA == nil && errB == nil && a == b
}

var columns = []string{"audit_id", "started_at", "token_id", "tool", "arguments", "request_id", "brief_id",
	"status", "http_status", "row_count", "rows", "elapsed_ms", "error"}

// Decode reads a receipt from its JSON, as the log's readers return it, and refuses anything a receipt
// cannot hold: a missing or extra column, a started_at the log does not write, a count that is not an
// integer (true is not 1), a row_count other than the number of rows, or rows on a receipt that is not ok.
func Decode(value any) (Receipt, error) {
	o, ok := value.(*strictjson.Object)
	bad := func(column string) (Receipt, error) {
		return Receipt{}, fmt.Errorf("%w: %s", ErrInvalid, column)
	}
	if !ok || len(o.Keys) != len(columns) {
		return bad("columns")
	}
	get := map[string]any{}
	for _, key := range columns {
		v, found := o.Get(key)
		if !found {
			return bad(key)
		}
		get[key] = v
	}
	var r Receipt
	var okText bool
	if r.AuditID, okText = get["audit_id"].(string); !okText || r.AuditID == "" {
		return bad("audit_id")
	}
	started, okText := get["started_at"].(string)
	at, err := time.Parse(time.RFC3339Nano, started)
	if !okText || err != nil || StartedAtText(at) != started {
		return bad("started_at")
	}
	r.StartedAt = at.UTC()
	if r.TokenID, okText = get["token_id"].(string); !okText || !validTokenID(r.TokenID) {
		return bad("token_id")
	}
	if r.Tool, okText = get["tool"].(string); !okText || r.Tool == "" {
		return bad("tool")
	}
	if _, err := strictjson.Dumps(get["arguments"], strictjson.Quoted); err != nil {
		return bad("arguments")
	}
	r.Arguments = strictjson.Clone(get["arguments"])
	if r.RequestID, err = ParseRequestID(get["request_id"]); err != nil {
		return bad("request_id")
	}
	if brief, isText := get["brief_id"].(string); isText {
		r.BriefID = &brief
	} else if get["brief_id"] != nil {
		return bad("brief_id")
	}
	if r.Status, okText = get["status"].(string); !okText || !slices.Contains([]string{"pending", "ok", "error"}, r.Status) {
		return bad("status")
	}
	if get["http_status"] != nil {
		n, isInt := asInt(get["http_status"])
		if !isInt || !n.IsInt64() || n.Int64() < 100 || n.Int64() > 599 {
			return bad("http_status")
		}
		status := int(n.Int64())
		r.HTTPStatus = &status
	}
	rows, isList := get["rows"].([]any)
	if !isList {
		return bad("rows")
	}
	r.Rows = []string{}
	for _, row := range rows {
		text, isText := row.(string)
		if !isText {
			return bad("rows")
		}
		r.Rows = append(r.Rows, text)
	}
	count, isInt := asInt(get["row_count"])
	if !isInt || !count.IsInt64() || count.Int64() != int64(len(r.Rows)) || r.Status != "ok" && len(r.Rows) > 0 {
		return bad("row_count")
	}
	switch v := get["elapsed_ms"].(type) {
	case nil:
	case float64:
		r.ElapsedMS = &v
	default:
		n, isInt := asInt(v)
		if !isInt || !n.IsInt64() {
			return bad("elapsed_ms")
		}
		f := float64(n.Int64())
		r.ElapsedMS = &f
	}
	if code, isText := get["error"].(string); isText {
		r.Error = &code
	} else if get["error"] != nil {
		return bad("error")
	}
	return r, nil
}

// UnitRow is one unit of a published card, as its projection holds it.
type UnitRow struct {
	N                     int
	Kind, RecordRef, Tool string
	Shown                 string // a sentence's shown; empty for a record unit
	ReceiptID             string // the read receipt that returned its record
	ContentVersion        string // the version of that record
	Value                 []byte // the unit as the card holds it, as JSON
}

// GapRow is one line under NOT READ of a published card.
type GapRow struct {
	Code, Disposition, Text string
	Count                   *big.Int
	ByRole                  []byte // JSON, nil for none
}

// Publication is everything Publish writes together.
type Publication struct {
	WorkspaceID, AccountRef, BriefID string
	// PrincipalID's grant on the account is rechecked; empty when the local owner publishes.
	PrincipalID   string
	Seal          Ticket // started, pending, before the card existed
	Kind          string // "card" or "selected_brief"
	SchemaVersion int
	Bytes         []byte
	Units         []UnitRow
	Gaps          []GapRow
	Reads         []string // the read receipts the card rests on
	ElapsedMS     float64
}

// Card is a published card.
type Card struct {
	WorkspaceID, AccountRef, BriefID, Kind string
	Bytes                                  []byte
	SHA256                                 string
	Seal                                   Receipt
	Units                                  []UnitRow
	Gaps                                   []GapRow
}

// Reference is the row a seal names: briefs/<brief ID>/<digest of the bytes>.
func Reference(briefID string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return "briefs/" + briefID + "/" + hex.EncodeToString(sum[:])
}

func (c Card) clone() Card {
	c.Bytes = append([]byte(nil), c.Bytes...)
	c.Seal = c.Seal.clone()
	c.Units = append([]UnitRow(nil), c.Units...)
	for i := range c.Units {
		c.Units[i].Value = append([]byte(nil), c.Units[i].Value...)
	}
	c.Gaps = append([]GapRow(nil), c.Gaps...)
	for i := range c.Gaps {
		if c.Gaps[i].Count != nil {
			c.Gaps[i].Count = new(big.Int).Set(c.Gaps[i].Count)
		}
		c.Gaps[i].ByRole = append([]byte(nil), c.Gaps[i].ByRole...)
	}
	return c
}

// ---- Memory ----

// Memory is a Store in process. Its clock and ID source can be fixed, as the oracle fixes them.
type Memory struct {
	mu          sync.Mutex
	now         func() time.Time
	newID       func() string
	order       []string
	byID        map[string]*Receipt
	byOperation map[string]string
	children    map[string]string // parent and ordinal, to the child
	generations map[string]int64  // by connection
	grants      map[[3]string]bool
	versions    map[[5]string][]byte
	bound       map[string][]RecordVersion
	cards       map[string]Card
}

// NewMemory makes an empty store. A nil clock reads the wall clock to the microsecond; nil IDs are random
// version 4 UUIDs.
func NewMemory(now func() time.Time, newID func() string) *Memory {
	if now == nil {
		now = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	}
	if newID == nil {
		newID = RandomID
	}
	return &Memory{now: now, newID: newID, byID: map[string]*Receipt{}, byOperation: map[string]string{},
		children: map[string]string{}, generations: map[string]int64{}, grants: map[[3]string]bool{},
		versions: map[[5]string][]byte{}, bound: map[string][]RecordVersion{}, cards: map[string]Card{}}
}

// RandomID is a random version 4 UUID in its usual text form.
func RandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// SetGeneration sets a connection's generation; a change of source bumps it.
func (m *Memory) SetGeneration(connectionID string, generation int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generations[connectionID] = generation
}

// Grant gives a principal a grant on an account; Revoke takes it back.
func (m *Memory) Grant(workspaceID, principalID, accountRef string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[[3]string{workspaceID, principalID, accountRef}] = true
}

func (m *Memory) Revoke(workspaceID, principalID, accountRef string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.grants, [3]string{workspaceID, principalID, accountRef})
}

func checkArguments(arguments any) error {
	if _, err := strictjson.Dumps(arguments, strictjson.Quoted); err != nil {
		return fmt.Errorf("%w: arguments: %v", ErrInvalid, err)
	}
	return nil
}

func sameJSON(a, b any) bool {
	x, errA := strictjson.Dumps(a, strictjson.Prepared)
	y, errB := strictjson.Dumps(b, strictjson.Prepared)
	return errA == nil && errB == nil && x == y
}

// commit stores a copy of a new pending receipt, so nothing the caller handed in is shared. The caller
// holds the lock.
func (m *Memory) commit(r Receipt) (Receipt, error) {
	id := m.newID()
	if _, taken := m.byID[id]; taken || id == "" {
		return Receipt{}, fmt.Errorf("%w: audit ID %q", ErrInvalid, id)
	}
	r.AuditID, r.StartedAt, r.Status, r.Rows, r.Lease = id, m.now(), "pending", []string{}, RandomID()
	if r.ConnectionID != "" {
		generation, known := m.generations[r.ConnectionID]
		if !known {
			return Receipt{}, fmt.Errorf("%w: unknown connection", ErrNotFound)
		}
		r.Generation = generation
	}
	stored := r.clone()
	m.byID[id] = &stored
	m.order = append(m.order, id)
	m.byOperation[r.OperationID] = id
	return stored.clone(), nil
}

func (m *Memory) Start(_ context.Context, s Start) (Receipt, error) {
	if s.WorkspaceID == "" || s.OperationID == "" || s.Tool == "" || !validTokenID(s.TokenID) {
		return Receipt{}, fmt.Errorf("%w: a receipt needs a workspace, an operation, a tool and a token fingerprint",
			ErrInvalid)
	}
	if _, _, isChild := s.RequestID.Parent(); isChild {
		return Receipt{}, fmt.Errorf("%w: a child read starts with StartChild", ErrInvalid)
	}
	if err := checkArguments(s.Arguments); err != nil {
		return Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, found := m.byOperation[s.OperationID]; found {
		existing := m.byID[id]
		if existing.WorkspaceID != s.WorkspaceID || existing.PrincipalID != s.PrincipalID ||
			existing.TokenID != s.TokenID || existing.Tool != s.Tool || existing.AccountRef != s.AccountRef ||
			!sameJSON(existing.Arguments, s.Arguments) {
			return Receipt{}, ErrOperationReused
		}
		return existing.clone(), nil
	}
	return m.commit(Receipt{TokenID: s.TokenID, Tool: s.Tool, Arguments: s.Arguments,
		RequestID: s.RequestID, BriefID: s.BriefID, WorkspaceID: s.WorkspaceID,
		OperationID: s.OperationID, PrincipalID: s.PrincipalID, Scope: s.Scope, AccountRef: s.AccountRef,
		ConnectionID: s.ConnectionID})
}

func (m *Memory) StartChild(_ context.Context, s ChildStart) (Receipt, error) {
	if s.Parent == "" || s.Ordinal < 1 || s.Tool == "" {
		return Receipt{}, fmt.Errorf("%w: a child read needs its parent, an ordinal from 1 and a tool", ErrInvalid)
	}
	if err := checkArguments(s.Arguments); err != nil {
		return Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, found := m.byID[s.Parent]
	if !found {
		return Receipt{}, ErrParent
	}
	if _, _, parentIsChild := parent.RequestID.Parent(); parent.Tool != Composite || parentIsChild {
		return Receipt{}, ErrParent
	}
	operation := fmt.Sprintf("%s#%d", parent.OperationID, s.Ordinal)
	if id, taken := m.byOperation[operation]; taken {
		existing := m.byID[id]
		if existing.Tool != s.Tool || existing.ConnectionID != s.ConnectionID || !sameJSON(existing.Arguments, s.Arguments) {
			return Receipt{}, ErrDuplicateChild
		}
		return existing.clone(), nil
	}
	if parent.Status != "pending" {
		return Receipt{}, ErrParent
	}
	account := s.AccountRef
	if account == "" {
		account = parent.AccountRef
	}
	r, err := m.commit(Receipt{TokenID: parent.TokenID, Tool: s.Tool, Arguments: s.Arguments,
		RequestID: Child(parent.AuditID, s.Ordinal), BriefID: s.BriefID,
		WorkspaceID: parent.WorkspaceID, OperationID: operation, PrincipalID: parent.PrincipalID, Scope: s.Scope,
		AccountRef: account, ConnectionID: s.ConnectionID})
	if err == nil {
		m.children[operation] = r.AuditID
	}
	return r, err
}

// pending finds a receipt its lease holder may finish. The caller holds the lock.
func (m *Memory) pending(t Ticket) (*Receipt, error) {
	r, found := m.byID[t.AuditID]
	switch {
	case !found:
		return nil, ErrNotFound
	case r.Lease != t.Lease:
		return nil, ErrLeaseLost
	}
	return r, nil
}

// settle is a finish of a receipt that already finished: the same outcome returns it, another is refused.
func settle(r *Receipt, o Outcome) (Receipt, error) {
	rows := o.Rows
	if rows == nil {
		rows = []string{}
	}
	if r.Status == o.Status && *r.HTTPStatus == o.HTTPStatus && slices.Equal(r.Rows, rows) &&
		(r.Error == nil) == (o.Error == nil) && (r.Error == nil || *r.Error == *o.Error) {
		return r.clone(), nil
	}
	return Receipt{}, ErrFinished
}

func (m *Memory) finish(r *Receipt, o Outcome) Receipt {
	status, elapsed := o.HTTPStatus, o.ElapsedMS
	r.Status, r.HTTPStatus, r.ElapsedMS, r.Error = o.Status, &status, &elapsed, copyPointer(o.Error)
	r.Rows = append([]string{}, o.Rows...)
	return r.clone()
}

func (m *Memory) Finish(_ context.Context, t Ticket, o Outcome) (Receipt, error) {
	if err := o.check(); err != nil {
		return Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.pending(t)
	if err != nil {
		return Receipt{}, err
	}
	if r.Status != "pending" {
		return settle(r, o)
	}
	if r.Tool == Composite {
		for operation, id := range m.children {
			if m.byID[id].Status == "pending" && strings.HasPrefix(operation, r.OperationID+"#") {
				return Receipt{}, fmt.Errorf("%w: a child read is still pending", ErrInvalid)
			}
		}
	}
	return m.finish(r, o), nil
}

var sourceChanged = Failed(409, "source_changed")

func (m *Memory) FinishRead(_ context.Context, t Ticket, versions []RecordVersion, elapsedMS float64) (Receipt, error) {
	refs := make([]string, len(versions))
	for i, v := range versions {
		if v.Source == "" || v.Table == "" || v.RecordID == "" {
			return Receipt{}, fmt.Errorf("%w: a record version needs its source, table and ID", ErrInvalid)
		}
		refs[i] = v.Ref()
	}
	ok := Outcome{Status: "ok", HTTPStatus: 200, Rows: refs, ElapsedMS: elapsedMS}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.pending(t)
	if err != nil {
		return Receipt{}, err
	}
	if r.Status != "pending" {
		if _, err := settle(r, sourceChanged); err == nil {
			return Receipt{}, ErrSourceChanged
		}
		finished, err := settle(r, ok)
		if err != nil {
			return Receipt{}, err
		}
		bound := m.bound[r.AuditID]
		for i, v := range bound {
			if v.Version() != versions[i].Version() || !bytes.Equal(v.Body, versions[i].Body) {
				return Receipt{}, ErrFinished
			}
		}
		return finished, nil
	}
	if r.ConnectionID != "" && m.generations[r.ConnectionID] != r.Generation {
		changed := sourceChanged
		changed.ElapsedMS = elapsedMS
		m.finish(r, changed)
		return Receipt{}, ErrSourceChanged
	}
	for _, v := range versions {
		key := [5]string{r.WorkspaceID, v.Source, v.Table, v.RecordID, v.Version()}
		if body, found := m.versions[key]; found && !bytes.Equal(body, v.Body) {
			return Receipt{}, ErrVersionConflict
		}
	}
	stored := make([]RecordVersion, len(versions))
	for i, v := range versions {
		m.versions[[5]string{r.WorkspaceID, v.Source, v.Table, v.RecordID, v.Version()}] = append([]byte(nil), v.Body...)
		stored[i] = v.clone()
	}
	m.bound[r.AuditID] = stored
	return m.finish(r, ok), nil
}

// Records is the record versions bound to a read receipt, in order.
func (m *Memory) Records(workspaceID, auditID string) ([]RecordVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, found := m.byID[auditID]
	if !found || r.WorkspaceID != workspaceID {
		return nil, ErrNotFound
	}
	out := make([]RecordVersion, len(m.bound[auditID]))
	for i, v := range m.bound[auditID] {
		out[i] = v.clone()
	}
	return out, nil
}

func (m *Memory) Publish(_ context.Context, p Publication) (Card, error) {
	reference := Reference(p.BriefID, p.Bytes)
	ok := Outcome{Status: "ok", HTTPStatus: 200, Rows: []string{reference}, ElapsedMS: p.ElapsedMS}
	m.mu.Lock()
	defer m.mu.Unlock()
	seal, err := m.pending(p.Seal)
	if err != nil {
		return Card{}, err
	}
	if seal.WorkspaceID != p.WorkspaceID || seal.AccountRef != p.AccountRef {
		return Card{}, fmt.Errorf("%w: the seal receipt names another workspace or account", ErrInvalid)
	}
	if seal.Status != "pending" {
		card, published := m.cards[p.BriefID]
		if _, err := settle(seal, ok); err != nil || !published || !bytes.Equal(card.Bytes, p.Bytes) {
			return Card{}, ErrFinished
		}
		return card.clone(), nil
	}
	if _, taken := m.cards[p.BriefID]; taken {
		return Card{}, fmt.Errorf("%w: brief %s is published already", ErrInvalid, p.BriefID)
	}
	refuse := func(o Outcome, err error) (Card, error) {
		o.ElapsedMS = p.ElapsedMS
		m.finish(seal, o)
		return Card{}, err
	}
	if p.PrincipalID != "" && !m.grants[[3]string{p.WorkspaceID, p.PrincipalID, p.AccountRef}] {
		return refuse(Failed(403, "not_permitted"), ErrNotPermitted)
	}
	for _, id := range p.Reads {
		read, found := m.byID[id]
		if !found || read.WorkspaceID != p.WorkspaceID || read.Status == "pending" {
			return Card{}, fmt.Errorf("%w: read %s is not a finished read of this workspace", ErrInvalid, id)
		}
		if read.ConnectionID != "" && m.generations[read.ConnectionID] != read.Generation {
			return refuse(sourceChanged, ErrSourceChanged)
		}
	}
	card := Card{WorkspaceID: p.WorkspaceID, AccountRef: p.AccountRef, BriefID: p.BriefID, Kind: p.Kind,
		Bytes: p.Bytes, SHA256: reference[len(reference)-64:], Units: p.Units, Gaps: p.Gaps}
	card.Seal = m.finish(seal, ok)
	stored := card.clone()
	m.cards[p.BriefID] = stored
	return stored.clone(), nil
}

// Published is a card Publish wrote, by brief ID.
func (m *Memory) Published(briefID string) (Card, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	card, found := m.cards[briefID]
	return card.clone(), found
}

func (m *Memory) Read(_ context.Context, workspaceID string, auditIDs []string) ([]Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := make([]Receipt, 0, len(auditIDs))
	for _, id := range auditIDs {
		r, ok := m.byID[id]
		if !ok || r.WorkspaceID != workspaceID {
			return nil, ErrNotFound
		}
		out := r.clone()
		out.Lease = ""
		found = append(found, out)
	}
	return found, nil
}

func (m *Memory) ByOperation(_ context.Context, operationID string) (Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, found := m.byOperation[operationID]
	if !found {
		return Receipt{}, ErrNotFound
	}
	return m.byID[id].clone(), nil
}

// All is every receipt in the order it was started, without leases.
func (m *Memory) All() []Receipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]Receipt, len(m.order))
	for i, id := range m.order {
		all[i] = m.byID[id].clone()
		all[i].Lease = ""
	}
	return all
}
