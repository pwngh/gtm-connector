// Package receipts holds the audit receipts every source read, tool call and card seal leaves.
//
// The contract, which every Store keeps:
//
//   - A receipt is committed as pending before the work it records starts, and goes from pending to
//     finished exactly once. A second finish, or a finish of an unknown receipt, is refused.
//   - token_id is the SHA-256 fingerprint of the bearer credential, or "anonymous". Raw credentials are
//     never stored; Fingerprint makes the fingerprint.
//   - request_id takes four forms: a JSON-RPC id (an integer or a string), null, "<invalid>" for an id of
//     any other JSON type, or {"kind": "brief_child", "parent_audit_id", "ordinal"} for a read made inside
//     a composite call. A child names a parent that is still pending, and (parent, ordinal) is unique.
//   - Only an "ok" receipt carries rows. A scope denial finishes as error/403 and a source that has no
//     selection for the account as error/200, both with no rows (Denied, NotSelected).
//   - A reply is released only after its receipt finishes. A card's printed read range comes from its read
//     receipts' start times.
//
// Memory keeps the contract in process. A PostgreSQL store keeps the same contract: the single finish by a
// guarded UPDATE ... WHERE status = 'pending' and a trigger, and the child uniqueness by a unique index on
// (parent, ordinal).
package receipts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sync"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Store commits and finishes receipts. Each method is one transaction.
type Store interface {
	// Start commits a pending receipt and returns it with its ID and start time. Nothing it records may
	// start before Start returns; if the store is unavailable, the work does not happen.
	Start(ctx context.Context, start Start) (Receipt, error)
	// Finish finishes a pending receipt once and returns it as finished.
	Finish(ctx context.Context, auditID string, outcome Outcome) (Receipt, error)
	// Read returns exactly these receipts in the given order, or ErrNotFound if any is missing.
	Read(ctx context.Context, auditIDs []string) ([]Receipt, error)
}

// Errors a Store returns.
var (
	ErrNotFound       = errors.New("receipts: receipt not found")
	ErrFinished       = errors.New("receipts: receipt not found or already finalized")
	ErrInvalid        = errors.New("receipts: invalid receipt")
	ErrParent         = errors.New("receipts: a child read needs a pending parent receipt")
	ErrDuplicateChild = errors.New("receipts: the parent already has a child read with this ordinal")
)

// Anonymous is the token_id of a call that carried no credential.
const Anonymous = "anonymous"

// Fingerprint is the token_id of a bearer credential.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

var fingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

type form int

const (
	rpcID form = iota
	null
	child
)

// RequestID is a receipt's request_id.
type RequestID struct {
	form    form
	rpc     any // string or *big.Int
	parent  string
	ordinal int
}

// RPC is a JSON-RPC request id as the gateway records it: an integer or a string as given, and any other
// JSON value as "<invalid>", so a caller cannot imitate the server's own correlation for child reads.
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
		return RequestID{form: rpcID, rpc: new(big.Int).Set(v)}
	}
	return Invalid()
}

// Null is the request_id of a receipt no JSON-RPC request carried, such as a card's seal.
func Null() RequestID { return RequestID{form: null} }

// Invalid is the request_id recorded for an id that is neither an integer nor a string.
func Invalid() RequestID { return RequestID{form: rpcID, rpc: "<invalid>"} }

// Child is the request_id of the ordinal-th source read inside the composite call parent.
func Child(parent string, ordinal int) RequestID {
	return RequestID{form: child, parent: parent, ordinal: ordinal}
}

// Parent is the parent receipt's ID and the ordinal of a child read.
func (r RequestID) Parent() (string, int, bool) { return r.parent, r.ordinal, r.form == child }

// Value is the request_id as the receipt's JSON holds it.
func (r RequestID) Value() any {
	switch r.form {
	case null:
		return nil
	case child:
		return strictjson.NewObject("kind", "brief_child", "parent_audit_id", r.parent, "ordinal", r.ordinal)
	}
	return r.rpc
}

// ParseRequestID reads a request_id from a receipt's JSON.
func ParseRequestID(value any) (RequestID, error) {
	switch v := value.(type) {
	case nil:
		return Null(), nil
	case string:
		return RequestID{form: rpcID, rpc: v}, nil
	case *big.Int:
		return RPC(v), nil
	case *strictjson.Object:
		kind, _ := v.Get("kind")
		parent, _ := v.Get("parent_audit_id")
		ordinal, _ := v.Get("ordinal")
		p, okParent := parent.(string)
		var n int64
		okOrdinal := false
		switch o := ordinal.(type) {
		case *big.Int:
			n, okOrdinal = o.Int64(), o.IsInt64()
		case int:
			n, okOrdinal = int64(o), true
		}
		if len(v.Keys) == 3 && kind == "brief_child" && okParent && okOrdinal && n >= 1 {
			return Child(p, int(n)), nil
		}
	}
	return RequestID{}, fmt.Errorf("%w: request_id", ErrInvalid)
}

// Start is what a receipt records before its work starts.
type Start struct {
	TokenID   string // Fingerprint of the bearer, or Anonymous
	Tool      string
	Arguments any // a JSON value, usually an *strictjson.Object
	RequestID RequestID
	BriefID   *string
}

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

// Receipt is one row of the audit log.
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
}

// StartedAtText is started_at as the log writes it: UTC, with microseconds only when there are any.
func StartedAtText(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000000Z")
}

// Value is the receipt as the audit log's readers return it, its members in the log's column order.
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
		"arguments", r.Arguments, "request_id", r.RequestID.Value(), "brief_id", brief, "status", r.Status,
		"http_status", status, "row_count", len(r.Rows), "rows", rows, "elapsed_ms", elapsed, "error", errorCode)
}

func (r Receipt) clone() Receipt {
	r.Rows = append([]string(nil), r.Rows...)
	return r
}

// Memory is an in-process Store. Its clock and ID source can be fixed, as the oracle fixes them.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	newID    func() string
	order    []string
	byID     map[string]*Receipt
	children map[[2]any]string
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
	return &Memory{now: now, newID: newID, byID: map[string]*Receipt{}, children: map[[2]any]string{}}
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

func validStart(s Start) error {
	if s.Tool == "" || s.TokenID != Anonymous && !fingerprint.MatchString(s.TokenID) {
		return fmt.Errorf("%w: a receipt needs a tool and a token fingerprint", ErrInvalid)
	}
	if _, err := strictjson.Dumps(s.Arguments, strictjson.Quoted); err != nil {
		return fmt.Errorf("%w: arguments: %v", ErrInvalid, err)
	}
	if parent, ordinal, ok := s.RequestID.Parent(); ok && (parent == "" || ordinal < 1) {
		return fmt.Errorf("%w: a child read needs its parent and an ordinal from 1", ErrInvalid)
	}
	return nil
}

func (m *Memory) Start(_ context.Context, s Start) (Receipt, error) {
	if err := validStart(s); err != nil {
		return Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]any{}
	if parent, ordinal, ok := s.RequestID.Parent(); ok {
		if p, found := m.byID[parent]; !found || p.Status != "pending" {
			return Receipt{}, ErrParent
		}
		key = [2]any{parent, ordinal}
		if _, taken := m.children[key]; taken {
			return Receipt{}, ErrDuplicateChild
		}
	}
	id := m.newID()
	if _, taken := m.byID[id]; taken || id == "" {
		return Receipt{}, fmt.Errorf("%w: audit ID %q", ErrInvalid, id)
	}
	r := &Receipt{AuditID: id, StartedAt: m.now(), TokenID: s.TokenID, Tool: s.Tool, Arguments: s.Arguments,
		RequestID: s.RequestID, BriefID: s.BriefID, Status: "pending", Rows: []string{}}
	m.byID[id] = r
	m.order = append(m.order, id)
	if key[0] != nil {
		m.children[key] = id
	}
	return r.clone(), nil
}

func (m *Memory) Finish(_ context.Context, auditID string, o Outcome) (Receipt, error) {
	if o.Status != "ok" && o.Status != "error" {
		return Receipt{}, fmt.Errorf("%w: a final status is ok or error", ErrInvalid)
	}
	if o.Status != "ok" && len(o.Rows) > 0 {
		return Receipt{}, fmt.Errorf("%w: only an ok receipt has rows", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, found := m.byID[auditID]
	if !found || r.Status != "pending" {
		return Receipt{}, ErrFinished
	}
	status, elapsed := o.HTTPStatus, o.ElapsedMS
	r.Status, r.HTTPStatus, r.ElapsedMS, r.Error = o.Status, &status, &elapsed, o.Error
	r.Rows = append([]string{}, o.Rows...)
	return r.clone(), nil
}

func (m *Memory) Read(_ context.Context, auditIDs []string) ([]Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := make([]Receipt, 0, len(auditIDs))
	for _, id := range auditIDs {
		r, ok := m.byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		found = append(found, r.clone())
	}
	return found, nil
}

// All is every receipt in the order it was started.
func (m *Memory) All() []Receipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]Receipt, len(m.order))
	for i, id := range m.order {
		all[i] = m.byID[id].clone()
	}
	return all
}
