package mcptools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// recorder is the gate's receipts on the store. It starts the one outer receipt of every call, and it
// finishes it, but for a prepared brief's seal, which the brief's publication finishes with the bytes.
type recorder struct {
	store     receipts.Store
	workspace string
	tokens    Storage
	// sources gives the workspace's sources as they are connected now. Start asks it once for each call.
	sources func(ctx context.Context) (Opened, error)

	mu   sync.Mutex
	open map[string]*pending // by receipt ID
}

// pending is one call's committed receipt until it is finished.
type pending struct {
	receipt receipts.Receipt
	// view is the sources the call reads, opened before its receipt started. refusal is why the call reads
	// none: they could not be opened, or the receipt started under a generation they were not opened under.
	view     Opened
	refusal  error
	versions []receipts.RecordVersion // what a direct read returned, bound when its receipt finishes
	since    time.Time
}

// stale is how long an unfinished call is kept. A receipt's lease is shorter, so by then the store has
// finished it as interrupted.
const stale = 15 * time.Minute

// readSource is the source system each direct read reads.
var readSource = map[string]string{"search_accounts": "crm", "get_account": "crm", "list_opportunities": "crm",
	"read_call_notes": "calls", "read_slack_messages": "slack"}

// Start commits the pending receipt of one call, before the gate checks or runs anything. The receipt names
// the principal the bearer belongs to when the workspace knows the bearer, whether or not it still admits
// anything: that is who presented it, never a grant.
//
// The token lookup and the opening of the sources share at most half the time ctx leaves, so the receipt's
// write keeps the other half however long they take: a call whose sources are slow to open still gets its
// receipt, and is refused under it.
func (r *recorder) Start(ctx context.Context, s mcp.Start) (string, error) {
	looking, cancel := halfway(ctx)
	defer cancel()
	start := receipts.Start{WorkspaceID: r.workspace, OperationID: "mcp-" + receipts.RandomID(),
		TokenID: receipts.Anonymous, Tool: s.Tool, Arguments: s.Arguments, RequestID: requestID(s.RequestID)}
	if s.Token != nil {
		start.TokenID = receipts.Fingerprint(*s.Token)
		digest := sha256.Sum256([]byte(*s.Token))
		token, err := r.tokens.MachineToken(looking, r.workspace, digest[:])
		switch {
		case err == nil:
			start.PrincipalID = token.PrincipalID
		case !errors.Is(err, postgres.ErrNotFound):
			return "", err
		}
	}
	// A receipt holds a tool name of 1 to 100 characters. Any other name is no tool, and is recorded as such.
	if n := utf8.RuneCountInString(start.Tool); n < 1 || n > 100 || strings.ContainsRune(start.Tool, 0) {
		start.Tool = "<invalid>"
	}
	// The sources are opened before the receipt starts, so the receipt names the connection the call reads
	// through. Opening reads the workspace's own rows and no source. A call whose sources cannot be opened
	// still gets its receipt, which names no connection, and is refused under it.
	view, refusal := r.sources(looking)
	if definition, known := mcp.Definitions[start.Tool]; known {
		start.Scope = definition.Scope
		// A direct read is a source read of its connection, so it finishes by binding what it returned.
		start.ConnectionID = view.Connections[readSource[start.Tool]]
		if arguments, ok := s.Arguments.(*strictjson.Object); ok && arguments != nil {
			account, _ := arguments.Get("account_id")
			start.AccountRef, _ = account.(string)
		}
	}
	// The receipt of a prepared brief is the brief's seal, and a seal names no brief of the caller's.
	if s.BriefID != nil && start.Tool != receipts.Composite && !strings.ContainsRune(*s.BriefID, 0) {
		start.BriefID = s.BriefID
	}
	receipt, err := r.store.Start(ctx, start)
	if err != nil {
		return "", err
	}
	if opened, checked := view.Generations[readSource[start.Tool]]; refusal == nil && checked &&
		receipt.ConnectionID != "" && receipt.Generation != opened {
		// The source was connected again or detached after it was opened: what was opened is not read.
		refusal = mcp.ToolError("source_changed")
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.open {
		if now.Sub(p.since) > stale {
			delete(r.open, id)
		}
	}
	r.open[receipt.AuditID] = &pending{receipt: receipt, view: view, refusal: refusal, since: now}
	return receipt.AuditID, nil
}

// requestID is a call's JSON-RPC id as the receipt holds it: null, an integer, a string, or "<invalid>".
func requestID(id any) receipts.RequestID {
	switch v := id.(type) {
	case nil:
		return receipts.Null()
	case string:
		if !strings.ContainsRune(v, 0) {
			return receipts.RPC(v)
		}
	case json.Number:
		if n, ok := new(big.Int).SetString(v.String(), 10); ok {
			return receipts.RPC(n)
		}
	}
	return receipts.Invalid()
}

// Finish finishes a call's receipt once. A released direct read finishes by binding exactly the record
// versions its body returned, and only if those are the rows the reply holds. A receipt its own work has
// finished, as a brief's publication finishes its seal, is confirmed and left as it is: with the same
// outcome, or as a refusal the store recorded in its own words when the reply is an error too.
func (r *recorder) Finish(ctx context.Context, auditID string, f mcp.Finish) error {
	r.mu.Lock()
	p := r.open[auditID]
	r.mu.Unlock()
	if p == nil {
		return errors.New("the receipt is not one this gateway started")
	}
	var err error
	if f.Status == "ok" && p.receipt.ConnectionID != "" {
		bound := make([]string, len(p.versions))
		for i, version := range p.versions {
			bound[i] = version.Ref()
		}
		if !slices.Equal(bound, f.Rows) {
			return errors.New("the reply's rows are not the records the read returned")
		}
		_, err = r.store.FinishRead(ctx, p.receipt.Ticket(), p.versions, f.ElapsedMs)
	} else {
		_, err = r.store.Finish(ctx, p.receipt.Ticket(), receipts.Outcome{Status: f.Status, HTTPStatus: f.HTTPStatus,
			Rows: f.Rows, ElapsedMS: f.ElapsedMs, Error: f.Error})
		if errors.Is(err, receipts.ErrFinished) && f.Status == "error" {
			if stored, readErr := r.store.Read(ctx, r.workspace, []string{auditID}); readErr == nil && stored[0].Status == "error" {
				err = nil
			}
		}
	}
	if err == nil {
		r.mu.Lock()
		delete(r.open, auditID)
		r.mu.Unlock()
	}
	return err
}

// bind keeps the record versions a direct read returned until its receipt finishes.
func (r *recorder) bind(auditID string, versions []receipts.RecordVersion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.open[auditID]
	if p == nil || p.receipt.ConnectionID == "" {
		return errors.New("the call has no pending source read receipt")
	}
	p.versions = versions
	return nil
}

// outer is the pending receipt the gate committed for a call.
func (r *recorder) outer(auditID string) (receipts.Receipt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.open[auditID]
	if p == nil {
		return receipts.Receipt{}, false
	}
	return p.receipt, true
}

// opened is the sources a call reads, as they were opened before its receipt started, or why it reads none.
func (r *recorder) opened(auditID string) (Opened, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.open[auditID]
	if p == nil {
		return Opened{}, errors.New("the call has no pending receipt")
	}
	return p.view, p.refusal
}

// halfway is ctx ending at half the time it has left, or ctx itself, with a cancel, when it has no deadline.
func halfway(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(deadline)/2)
}
