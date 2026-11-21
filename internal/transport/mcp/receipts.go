package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Start is what a receipt records before the call may run, as audit.AuditLog.start takes it.
type Start struct {
	Token     *string // the caller's credential as sent; nil records "anonymous"
	Tool      string
	Arguments any // the arguments as the request carried them: a strictjson value, in the caller's key order
	RequestID any // nil, an integer, a string, "<invalid>", or a brief_child object
	BriefID   *string
}

// Finish is how a receipt ends, as audit.AuditLog.finish takes it.
type Finish struct {
	Status     string // "ok" or "error", never "pending"
	HTTPStatus int
	Rows       []string
	ElapsedMs  float64
	Error      *string
}

// Recorder commits a pending receipt before a call runs and finishes it exactly once. The real store is
// internal/receipts; this package only needs the two operations.
type Recorder interface {
	Start(ctx context.Context, start Start) (string, error)
	Finish(ctx context.Context, auditID string, finish Finish) error
}

// Receipt is one row of the in-package recorder, shaped as audit.read returns rows.
type Receipt struct {
	AuditID    string
	StartedAt  time.Time
	TokenID    string
	Tool       string
	Arguments  any
	RequestID  any
	BriefID    *string
	Status     string
	HTTPStatus *int
	RowCount   int
	Rows       []string
	ElapsedMs  float64
	Error      *string
}

// MemoryRecorder keeps receipts in memory for the spike's tests. FailStart and FailFinish make the next
// operations fail, as an unavailable store would.
type MemoryRecorder struct {
	mu         sync.Mutex
	rows       []*Receipt
	next       int
	NewID      func(n int) string
	Now        func() time.Time
	FailStart  bool
	FailFinish bool
	// BeforeFinish, when set, runs at the start of every Finish, outside the lock; a test can block in it.
	BeforeFinish func(auditID string)
}

var errUnavailable = errors.New("receipt store unavailable")

func (m *MemoryRecorder) Start(_ context.Context, s Start) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailStart {
		return "", errUnavailable
	}
	m.next++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", m.next)
	if m.NewID != nil {
		id = m.NewID(m.next)
	}
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now()
	}
	tokenID := "anonymous"
	if s.Token != nil {
		tokenID = fingerprint(*s.Token)
	}
	m.rows = append(m.rows, &Receipt{AuditID: id, StartedAt: now, TokenID: tokenID, Tool: s.Tool,
		Arguments: plain(s.Arguments), RequestID: s.RequestID, BriefID: s.BriefID, Status: "pending", Rows: []string{}})
	return id, nil
}

func (m *MemoryRecorder) Finish(_ context.Context, auditID string, f Finish) error {
	if m.BeforeFinish != nil {
		m.BeforeFinish(auditID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Status == "" || f.Status == "pending" {
		return errors.New("a final status must not be empty or pending")
	}
	if m.FailFinish {
		return errUnavailable
	}
	for _, row := range m.rows {
		if row.AuditID == auditID {
			if row.Status != "pending" {
				return errors.New("receipt already finished")
			}
			status := f.HTTPStatus
			row.Status, row.HTTPStatus, row.RowCount, row.Rows, row.ElapsedMs, row.Error =
				f.Status, &status, len(f.Rows), append([]string{}, f.Rows...), f.ElapsedMs, f.Error
			return nil
		}
	}
	return errors.New("receipt not found")
}

// Read returns copies of the receipts in insertion order.
func (m *MemoryRecorder) Read() []Receipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Receipt, len(m.rows))
	for i, row := range m.rows {
		out[i] = *row
	}
	return out
}
