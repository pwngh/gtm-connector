package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Receipt errors. Callers match them with errors.Is.
var (
	// ErrAlreadyFinished: the receipt finished earlier with another outcome.
	ErrAlreadyFinished = errors.New("receipt already finished")
	// ErrLeaseLost: the caller's lease is not the receipt's, or it expired.
	ErrLeaseLost = errors.New("receipt lease not held")
	// ErrOperationReused: the operation ID names a different request.
	ErrOperationReused = errors.New("operation ID already used for a different request")
	// ErrInvalidOperation: a top-level operation ID is empty, too long or holds '#'.
	ErrInvalidOperation = errors.New("an operation ID is 1 to 128 characters without '#'")
	// ErrInProgress: another worker holds the live claim on this read.
	ErrInProgress = errors.New("another worker is running this read")
	// ErrParent: a child read starts only under a pending top-level
	// prepare_renewal_brief receipt, numbered from 1.
	ErrParent = errors.New("a child read needs a pending top-level prepare_renewal_brief receipt and an ordinal from 1")
	// ErrPendingChild: a parent cannot finish while a child read is pending.
	ErrPendingChild = errors.New("a child receipt is still pending")
	// ErrSourceRead: a source read finishes through FinishRead or FailRead.
	ErrSourceRead = errors.New("a source read finishes through FinishRead or FailRead")
	// ErrNotSourceRead: a receipt that reads no source finishes through
	// FinishReceipt.
	ErrNotSourceRead = errors.New("this receipt reads no source; finish it with FinishReceipt")
	// ErrWrongSource: a read named or returned a source other than its connection's.
	ErrWrongSource = errors.New("the read does not match its connection's source")
	// ErrSourceChanged: the connection's generation changed after the read started.
	ErrSourceChanged = errors.New("the source changed after the read started")
	// ErrVersionConflict: one content version of a record arrived with two bodies.
	ErrVersionConflict = errors.New("a record version already exists with a different body")
	// ErrInvalidArguments: a receipt's arguments are not one JSON value with unique keys.
	ErrInvalidArguments = errors.New("a receipt's arguments are one JSON value, each object's keys unique")
	// ErrRecordRows: a receipt that reads no source claimed a record row.
	ErrRecordRows = errors.New("only a source read claims a record row, through FinishRead")
)

// Receipt is one row of the receipts table.
type Receipt struct {
	ID, WorkspaceID, OperationID string
	ParentID                     string
	Ordinal                      *int
	PrincipalID                  string
	TokenID, Tool, Scope         string
	// RequestedAccount is the account the request named; AccountRef is the
	// account of this workspace it resolved to, or "".
	RequestedAccount, AccountRef string
	ConnectionID                 string
	ConnectionGeneration         int64
	Arguments, RequestID         json.RawMessage
	BriefID                      string
	StartedAt                    time.Time
	Status                       string
	HTTPStatus                   *int
	Rows                         []string
	ElapsedMS                    *float64
	Error                        *string
	FinishedAt                   *time.Time
	// LeaseToken is set only for the caller that holds the lease: a new
	// start, or StartReceipt's replay, whose operation ID is the claim.
	LeaseToken     string
	LeaseExpiresAt time.Time
	LeaseFence     int64
	DeliveredAt    *time.Time
	// Replay is set when a start found the receipt committed already.
	Replay bool
}

// Composite is the one tool whose source reads are child reads.
const Composite = "prepare_renewal_brief"

// TokenID is the receipt fingerprint of a bearer credential: its SHA-256 in
// hex, or "anonymous" when there is none.
func TokenID(raw string) string {
	if raw == "" {
		return "anonymous"
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

const receiptColumns = `r.id, r.workspace_id, r.operation_id, r.parent_id, r.ordinal, r.principal_id,
	r.token_id, r.tool, r.scope, r.requested_account, r.account_ref, r.connection_id, r.connection_generation,
	r.arguments, r.request_id, r.brief_id, r.started_at, r.status, r.http_status, r.rows,
	r.elapsed_ms, r.error, r.finished_at, r.lease_token, r.lease_expires_at, r.lease_fence,
	(SELECT d.delivered_at FROM receipt_deliveries d WHERE d.receipt_id = r.id)`

func scanReceipt(row pgx.Row) (Receipt, error) { return scanReceiptWith(row) }

// scanReceiptWith scans receiptColumns and then the extra columns after them.
func scanReceiptWith(row pgx.Row, extra ...any) (Receipt, error) {
	var r Receipt
	var parent, principal, scope, requested, account, connection, brief *string
	var generation *int64
	var rows []byte
	err := row.Scan(append([]any{&r.ID, &r.WorkspaceID, &r.OperationID, &parent, &r.Ordinal, &principal,
		&r.TokenID, &r.Tool, &scope, &requested, &account, &connection, &generation,
		&r.Arguments, &r.RequestID, &brief, &r.StartedAt, &r.Status, &r.HTTPStatus, &rows,
		&r.ElapsedMS, &r.Error, &r.FinishedAt, &r.LeaseToken, &r.LeaseExpiresAt, &r.LeaseFence,
		&r.DeliveredAt}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	r.ParentID, r.PrincipalID, r.Scope = deref(parent), deref(principal), deref(scope)
	r.RequestedAccount, r.AccountRef = deref(requested), deref(account)
	r.ConnectionID, r.BriefID = deref(connection), deref(brief)
	if generation != nil {
		r.ConnectionGeneration = *generation
	}
	// The rows are a list of references and nothing else: a null among them is no reference.
	if err := strictjson.Decode(rows, &r.Rows); err != nil {
		return Receipt{}, fmt.Errorf("receipt %s rows: %w", r.ID, err)
	}
	return r, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var requestedAccountPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// requestedAccount is how a receipt records the account a request named:
// as given when it is a plain reference, else "<invalid>".
func requestedAccount(given string) *string {
	if given == "" {
		return nil
	}
	if !requestedAccountPattern.MatchString(given) {
		given = "<invalid>"
	}
	return &given
}

// ReceiptStart is what a top-level receipt records before its tool runs.
type ReceiptStart struct {
	WorkspaceID string
	// OperationID stays the same across retries of one request. A retry
	// after a lost acknowledgement gets the receipt the first attempt made,
	// with its lease. It never contains '#'.
	OperationID string
	PrincipalID string // empty when no principal is known
	TokenID     string // TokenID(raw)
	Tool        string
	Scope       string // empty when the tool needs none
	// AccountRef is the account the request named, known or not; the
	// receipt resolves it in the workspace.
	AccountRef string
	// ConnectionID makes the receipt a source read of that connection, as
	// the CLI's reads are: it records the connection's generation, needs
	// Scope to be that source's, and finishes through FinishRead or FailRead.
	ConnectionID string
	// Arguments are kept as given, key order included.
	Arguments json.RawMessage
	RequestID json.RawMessage // a JSON-RPC id, null, or "<invalid>"; nil means null
	BriefID   string
	Lease     time.Duration // how long the caller may take to finish it
}

func (in ReceiptStart) requestID() json.RawMessage {
	if in.RequestID == nil {
		return json.RawMessage("null")
	}
	return in.RequestID
}

// StartReceipt commits a pending receipt in its own transaction, before the
// caller may run the tool. Operation IDs are unique per workspace. The same
// operation ID with the same request (every field but the lease) gives back
// the existing receipt, with its lease and Replay set; with a different
// request it is refused.
func (s *Store) StartReceipt(ctx context.Context, in ReceiptStart) (Receipt, error) {
	if err := checkLease(in.Lease); err != nil {
		return Receipt{}, err
	}
	if in.OperationID == "" || len(in.OperationID) > 128 || strings.Contains(in.OperationID, "#") {
		return Receipt{}, ErrInvalidOperation
	}
	if !validUUID(in.WorkspaceID) || in.ConnectionID != "" && !validUUID(in.ConnectionID) {
		return Receipt{}, ErrNotFound
	}
	if err := checkArguments(in.Arguments); err != nil {
		return Receipt{}, err
	}
	requested := requestedAccount(in.AccountRef)
	// A connection of another workspace matches no row: ErrNotFound below.
	// The insert runs in a transaction under the store's lock and statement
	// timeouts (bound), so it commits only when the caller commits it: an
	// insert its caller stopped waiting for writes nothing.
	var r Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var err error
		r, err = scanReceipt(tx.QueryRow(ctx, `
			INSERT INTO receipts AS r (workspace_id, operation_id, principal_id, token_id, tool, scope,
				requested_account, account_ref, connection_id, connection_generation, arguments, request_id,
				brief_id, lease_token, lease_expires_at)
			SELECT $1, $2, $3, $4, $5, $6, $7,
				(SELECT ref FROM accounts WHERE workspace_id = $1 AND ref = $7),
				c.id, c.generation, $8, $9, $10, gen_random_uuid(), clock_timestamp() + make_interval(secs => $11)
			  FROM (SELECT 1) one
			  LEFT JOIN connections c ON c.workspace_id = $1 AND c.id = $12
			 WHERE $12::uuid IS NULL OR c.id IS NOT NULL
			ON CONFLICT (workspace_id, operation_id) DO NOTHING
			RETURNING `+receiptColumns,
			in.WorkspaceID, in.OperationID, nullable(in.PrincipalID), in.TokenID, in.Tool, nullable(in.Scope),
			requested, string(in.Arguments), in.requestID(), nullable(in.BriefID), in.Lease.Seconds(),
			nullable(in.ConnectionID)))
		if err != nil {
			return err
		}
		return s.at("start:inserted")
	})
	switch {
	case err == nil:
		return r, nil
	case errors.Is(err, errCommitUnknown):
		return s.startStored(ctx, r, err)
	}
	// The operation exists, or the insert was refused: a replay of another
	// request is refused as such, whatever the insert's own refusal.
	existing, lookupErr := s.receiptByOperation(ctx, in.WorkspaceID, in.OperationID)
	switch {
	case lookupErr == nil:
	case sqlState(err) == "GT012":
		return Receipt{}, ErrWrongSource
	case errors.Is(err, ErrNotFound):
		return Receipt{}, lookupErr
	default:
		return Receipt{}, err
	}
	if existing.ParentID != "" ||
		existing.PrincipalID != in.PrincipalID || existing.TokenID != in.TokenID ||
		existing.Tool != in.Tool || existing.Scope != in.Scope ||
		existing.RequestedAccount != deref(requested) || existing.BriefID != in.BriefID ||
		existing.ConnectionID != in.ConnectionID ||
		!sameJSON(existing.Arguments, in.Arguments) || !sameJSON(existing.RequestID, in.requestID()) {
		return Receipt{}, ErrOperationReused
	}
	existing.Replay = true
	return existing, nil
}

// startStored settles a start whose commit got no answer by reading the
// receipt it inserted back by its ID, within recoverWait. When that receipt
// is there, pending under the lease the insert gave, the start committed and
// is returned as committed. Otherwise the start's own error stands.
func (s *Store) startStored(ctx context.Context, inserted Receipt, err error) (Receipt, error) {
	reading, cancel := recovering(ctx)
	defer cancel()
	stored, readErr := s.receiptByID(reading, inserted.ID)
	if readErr != nil || stored.Status != "pending" || stored.LeaseToken != inserted.LeaseToken {
		return Receipt{}, err
	}
	return inserted, nil
}

// checkArguments refuses arguments strictjson would not read back: two
// values, a duplicate key, a number no float holds.
func checkArguments(arguments json.RawMessage) error {
	if _, err := strictjson.Loads(arguments); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return nil
}

// RecordRefusal commits a receipt for a request that is refused before any
// tool runs, and returns it finished: started, then finished as an error.
func (s *Store) RecordRefusal(ctx context.Context, in ReceiptStart, httpStatus int, code string) (Receipt, error) {
	r, err := s.StartReceipt(ctx, in)
	if err != nil {
		return Receipt{}, err
	}
	return s.FinishReceipt(ctx, ReceiptFinish{ReceiptID: r.ID, LeaseToken: r.LeaseToken, Status: "error",
		HTTPStatus: httpStatus, Error: code})
}

// ChildReadStart is what a child read receipt records before the read.
type ChildReadStart struct {
	ParentID     string
	Ordinal      int // from 1
	Tool         string
	Scope        string // the connection's source, as "<source>:read"
	ConnectionID string
	AccountRef   string // empty to take the parent's
	Arguments    json.RawMessage
	BriefID      string // the brief the read is for, or empty
	Lease        time.Duration
}

// StartChildRead commits a pending child receipt in its own transaction,
// before a source read, and gives its lease to this caller only: the read
// runs at most once per receipt. It takes the workspace, principal and token
// from the parent, which must be a pending top-level prepare_renewal_brief
// receipt (else ErrParent), and records the connection's current
// generation. Ordinals start at 1. Its operation ID is the parent's, then
// "#" and the ordinal.
//
// A retry of the same request finds the child already there:
//   - finished: it gets the committed receipt (its outcome stands), even
//     after the parent finished;
//   - pending, with a live lease: ErrInProgress, since another worker reads;
//   - pending, with an expired lease: the worker stopped, so the receipt is
//     finished as 503 interrupted and returned. The read does not run again.
func (s *Store) StartChildRead(ctx context.Context, in ChildReadStart) (Receipt, error) {
	if err := checkLease(in.Lease); err != nil {
		return Receipt{}, err
	}
	if in.Ordinal < 1 || !validUUID(in.ParentID) {
		return Receipt{}, ErrParent
	}
	if err := checkArguments(in.Arguments); err != nil {
		return Receipt{}, err
	}
	if !validUUID(in.ConnectionID) {
		return Receipt{}, ErrNotFound
	}
	if existing, err := s.childByOrdinal(ctx, in.ParentID, in.Ordinal); err == nil {
		return s.replayChild(ctx, existing, in)
	} else if !errors.Is(err, ErrNotFound) {
		return Receipt{}, err
	}
	// The trigger refuses the same; this check names the refusal.
	var composite bool
	err := s.pool.QueryRow(ctx, `SELECT parent_id IS NULL AND tool = $2 AND status = 'pending' FROM receipts
		WHERE id = $1`, in.ParentID, Composite).Scan(&composite)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !composite {
		return Receipt{}, ErrParent
	}
	if err != nil {
		return Receipt{}, err
	}
	requestID, _ := json.Marshal(map[string]any{
		"kind": "brief_child", "parent_audit_id": in.ParentID, "ordinal": in.Ordinal})
	// The insert's trigger waits on the parent's row, so it runs under the
	// store's lock and statement timeouts (bound), as a top-level start does.
	var r Receipt
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var err error
		r, err = scanReceipt(tx.QueryRow(ctx, `
			INSERT INTO receipts AS r (workspace_id, operation_id, parent_id, ordinal, principal_id, token_id,
				tool, scope, requested_account, account_ref, connection_id, connection_generation, arguments,
				request_id, brief_id, lease_token, lease_expires_at)
			SELECT p.workspace_id, p.operation_id || '#' || $2::integer, p.id, $2::integer, p.principal_id,
				p.token_id, $3, $4, coalesce($5, p.requested_account),
				CASE WHEN $5::text IS NULL THEN p.account_ref
				     ELSE (SELECT ref FROM accounts a WHERE a.workspace_id = p.workspace_id AND a.ref = $5) END,
				c.id, c.generation, $6, $7, $10, gen_random_uuid(), clock_timestamp() + make_interval(secs => $8)
			  FROM receipts p
			  JOIN connections c ON c.workspace_id = p.workspace_id AND c.id = $9
			 WHERE p.id = $1
			ON CONFLICT (workspace_id, operation_id) DO NOTHING
			RETURNING `+receiptColumns,
			in.ParentID, in.Ordinal, in.Tool, nullable(in.Scope), requestedAccount(in.AccountRef),
			string(in.Arguments), requestID, in.Lease.Seconds(), in.ConnectionID, nullable(in.BriefID)))
		return err
	})
	switch {
	case errors.Is(err, errCommitUnknown):
		return s.startStored(ctx, r, err)
	case sqlState(err) == "GT006", sqlState(err) == "GT017":
		return Receipt{}, ErrParent
	case sqlState(err) == "GT012":
		return Receipt{}, ErrWrongSource
	case !errors.Is(err, ErrNotFound):
		return r, err
	}
	// A concurrent start won, or the parent or connection does not exist.
	existing, err := s.childByOrdinal(ctx, in.ParentID, in.Ordinal)
	if err != nil {
		return Receipt{}, err
	}
	return s.replayChild(ctx, existing, in)
}

func (s *Store) childByOrdinal(ctx context.Context, parentID string, ordinal int) (Receipt, error) {
	return scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.parent_id = $1 AND r.ordinal = $2`, parentID, ordinal))
}

// replayChild answers a start for a child that exists already.
func (s *Store) replayChild(ctx context.Context, existing Receipt, in ChildReadStart) (Receipt, error) {
	requested := existing.RequestedAccount
	if in.AccountRef != "" {
		requested = deref(requestedAccount(in.AccountRef))
	} else if parent, err := s.receiptByID(ctx, in.ParentID); err == nil {
		requested = parent.RequestedAccount
	}
	if existing.Tool != in.Tool || existing.Scope != in.Scope || existing.ConnectionID != in.ConnectionID ||
		existing.RequestedAccount != requested || existing.BriefID != in.BriefID ||
		!sameJSON(existing.Arguments, in.Arguments) {
		return Receipt{}, ErrOperationReused
	}
	if existing.Status != "pending" {
		existing.LeaseToken, existing.Replay = "", true
		return existing, nil
	}
	if _, err := s.abandon(ctx, existing.ID); err != nil {
		if errors.Is(err, errLeaseLive) {
			return Receipt{}, ErrInProgress
		}
		return Receipt{}, err
	}
	finished, err := s.receiptByID(ctx, existing.ID)
	finished.LeaseToken, finished.Replay = "", true
	return finished, err
}

// ReceiptFinish is a receipt's outcome.
type ReceiptFinish struct {
	ReceiptID, LeaseToken string
	Status                string // "ok" or "error"
	HTTPStatus            int
	Rows                  []string // empty unless Status is "ok"
	ElapsedMS             float64
	Error                 string // empty for none
}

func (f ReceiptFinish) check() error {
	switch {
	case f.Status != "ok" && f.Status != "error":
		return fmt.Errorf("a final receipt status is ok or error, not %q", f.Status)
	case f.Status == "error" && len(f.Rows) > 0:
		return errors.New("a receipt that finishes as an error has no rows")
	case f.HTTPStatus < 100 || f.HTTPStatus > 599:
		return fmt.Errorf("HTTP status %d", f.HTTPStatus)
	case f.ElapsedMS < 0:
		return errors.New("elapsed time is never negative")
	}
	return nil
}

// FinishReceipt finishes a pending receipt that reads no source, once, with
// a guarded UPDATE ... WHERE status = 'pending' that presents the caller's
// lease to the database. Finishing it again with the same outcome (a retry
// after a lost acknowledgement) returns the finished receipt; with another
// outcome it gives ErrAlreadyFinished. A source read gives ErrSourceRead.
func (s *Store) FinishReceipt(ctx context.Context, f ReceiptFinish) (Receipt, error) {
	if err := f.check(); err != nil {
		return Receipt{}, err
	}
	if !validUUID(f.ReceiptID) || !validUUID(f.LeaseToken) {
		return Receipt{}, ErrNotFound
	}
	var finished Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var err error
		finished, err = finishLocked(ctx, tx, f, false)
		return err
	})
	switch {
	case errors.Is(err, errNotPending):
		return s.settled(ctx, f, false)
	case errors.Is(err, errCommitUnknown):
		return s.finishStored(ctx, f, false, err)
	}
	return finished, err
}

// finishStored settles a finish whose commit got no answer by reading the
// receipt back, within recoverWait: stored with this outcome under this
// lease, the finish committed and the stored receipt is returned. Otherwise
// the finish's own error stands.
func (s *Store) finishStored(ctx context.Context, f ReceiptFinish, sourceRead bool, err error) (Receipt, error) {
	reading, cancel := recovering(ctx)
	defer cancel()
	if stored, settledErr := s.settled(reading, f, sourceRead); settledErr == nil {
		return stored, nil
	}
	return Receipt{}, err
}

var (
	errNotPending = errors.New("not pending")
	errLeaseLive  = errors.New("lease live")
)

// finishLocked runs the guarded UPDATE inside tx. The WHERE clause is the
// first guard; the trigger, which checks the presented lease, is the second.
func finishLocked(ctx context.Context, tx pgx.Tx, f ReceiptFinish, sourceRead bool) (Receipt, error) {
	rows := f.Rows
	if rows == nil {
		rows = []string{}
	}
	rowsJSON, err := json.Marshal(rows)
	if err != nil {
		return Receipt{}, err
	}
	r, err := scanReceipt(tx.QueryRow(ctx, `
		UPDATE receipts AS r SET status = $3, http_status = $4, row_count = $5, rows = $6,
			elapsed_ms = $7, error = $8, finished_at = clock_timestamp(), finishing_lease = $2
		 WHERE r.id = $1 AND r.status = 'pending' AND r.lease_token = $2
		   AND r.lease_expires_at > clock_timestamp() AND (r.connection_id IS NOT NULL) = $9
		RETURNING `+receiptColumns,
		f.ReceiptID, f.LeaseToken, f.Status, f.HTTPStatus, len(rows), rowsJSON, f.ElapsedMS, nullable(f.Error),
		sourceRead))
	switch sqlState(err) {
	case "GT005":
		return Receipt{}, ErrPendingChild
	case "GT003":
		return Receipt{}, fmt.Errorf("%w: the database refused the presented lease", ErrLeaseLost)
	case "GT013":
		return Receipt{}, ErrSourceChanged
	case "GT014":
		return Receipt{}, ErrRecordRows
	}
	if errors.Is(err, ErrNotFound) {
		return Receipt{}, errNotPending
	}
	r.LeaseToken = ""
	return r, err
}

// settled explains why a finish matched no pending row: the receipt is of
// the other kind, it finished already (with this outcome or another), the
// lease is not the caller's, or it is unknown.
func (s *Store) settled(ctx context.Context, f ReceiptFinish, sourceRead bool) (Receipt, error) {
	r, err := s.receiptByID(ctx, f.ReceiptID)
	if err != nil {
		return Receipt{}, err
	}
	if (r.ConnectionID != "") != sourceRead {
		if sourceRead {
			return Receipt{}, ErrNotSourceRead
		}
		return Receipt{}, ErrSourceRead
	}
	// The lease token stays on a finished receipt: only its holder hears back.
	if r.Status == "pending" || r.LeaseToken != f.LeaseToken {
		return Receipt{}, ErrLeaseLost
	}
	rows := f.Rows
	if rows == nil {
		rows = []string{}
	}
	if r.Status == f.Status && r.HTTPStatus != nil && *r.HTTPStatus == f.HTTPStatus &&
		slices.Equal(r.Rows, rows) && deref(r.Error) == f.Error {
		r.LeaseToken = ""
		return r, nil
	}
	return Receipt{}, ErrAlreadyFinished
}

// RenewLease extends a live lease held by the caller.
func (s *Store) RenewLease(ctx context.Context, receiptID, leaseToken string, lease time.Duration) (time.Time, error) {
	if err := checkLease(lease); err != nil {
		return time.Time{}, err
	}
	if !validUUID(receiptID) || !validUUID(leaseToken) {
		return time.Time{}, ErrNotFound
	}
	var expires time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE receipts SET lease_expires_at = clock_timestamp() + make_interval(secs => $3), finishing_lease = $2
		 WHERE id = $1 AND status = 'pending' AND lease_token = $2 AND lease_expires_at > clock_timestamp()
		RETURNING lease_expires_at`, receiptID, leaseToken, lease.Seconds()).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) || sqlState(err) == "GT003" {
		return time.Time{}, ErrLeaseLost
	}
	return expires, err
}

// RecordVersion is one record as a read returned it.
type RecordVersion struct {
	Source, Table, RecordID string
	// ContentVersion names this version. Empty means the SHA-256 of Body in
	// hex. Slack messages use their edit timestamp and a digest instead.
	ContentVersion string
	Body           []byte
}

// RecordRef is the version's source/table/id reference.
func (v RecordVersion) RecordRef() string { return v.Source + "/" + v.Table + "/" + v.RecordID }

func (v RecordVersion) version() string {
	if v.ContentVersion != "" {
		return v.ContentVersion
	}
	digest := sha256.Sum256(v.Body)
	return hex.EncodeToString(digest[:])
}

// FinishRead is step 2 of a source read, in one transaction: it stores the
// immutable record versions, binds exactly those versions to the child
// receipt in order, and finishes the receipt ok with their references. The
// database refuses an ok finish whose rows are not exactly its bindings.
//
// It finishes the receipt as an error instead, storing nothing, when:
//   - the connection's generation changed since the read started (409
//     source_changed, ErrSourceChanged);
//   - a record is of another source than the connection's (502
//     wrong_source, ErrWrongSource);
//   - a content version arrived with a different body (409
//     version_conflict, ErrVersionConflict).
//
// A retry after a lost acknowledgement, with the same records, returns the
// finished receipt.
func (s *Store) FinishRead(ctx context.Context, receiptID, leaseToken string, records []RecordVersion, elapsedMS float64) (Receipt, error) {
	if !validUUID(receiptID) || !validUUID(leaseToken) {
		return Receipt{}, ErrNotFound
	}
	refs := make([]string, len(records))
	for i, v := range records {
		refs[i] = v.RecordRef()
	}
	ok := ReceiptFinish{ReceiptID: receiptID, LeaseToken: leaseToken, Status: "ok", HTTPStatus: 200,
		Rows: refs, ElapsedMS: elapsedMS}
	failure := func(status int, code string) ReceiptFinish {
		return ReceiptFinish{ReceiptID: receiptID, LeaseToken: leaseToken, Status: "error", HTTPStatus: status,
			ElapsedMS: elapsedMS, Error: code}
	}
	var finished Receipt
	var refused error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var status, workspaceID, source string
		var connectionID *string
		var generation *int64
		err := tx.QueryRow(ctx, `
			SELECT status, workspace_id, connection_id, connection_generation
			  FROM receipts WHERE id = $1 FOR UPDATE`, receiptID).
			Scan(&status, &workspaceID, &connectionID, &generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "pending" {
			return errNotPending
		}
		if connectionID == nil {
			return ErrNotSourceRead
		}
		var current int64
		// The share lock holds off a generation bump until this commits.
		if err := tx.QueryRow(ctx, `SELECT generation, source FROM connections WHERE id = $1 FOR SHARE`,
			*connectionID).Scan(&current, &source); err != nil {
			return err
		}
		if current != *generation {
			refused = ErrSourceChanged
			finished, err = finishLocked(ctx, tx, failure(409, "source_changed"), true)
			return err
		}
		for _, v := range records {
			if v.Source != source {
				refused = ErrWrongSource
				finished, err = finishLocked(ctx, tx, failure(502, "wrong_source"), true)
				return err
			}
		}
		if err := s.at("read:locked"); err != nil {
			return err
		}
		// A savepoint, so a version conflict can be undone and recorded.
		err = pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
			for i, v := range records {
				id, err := insertVersion(ctx, sp, workspaceID, v)
				if err != nil {
					return err
				}
				if err := s.at("read:record"); err != nil {
					return err
				}
				if _, err := sp.Exec(ctx, `
					INSERT INTO receipt_records (workspace_id, receipt_id, ordinal, source_record_id)
					VALUES ($1, $2, $3, $4)`, workspaceID, receiptID, i, id); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, ErrVersionConflict) {
			refused = ErrVersionConflict
			finished, err = finishLocked(ctx, tx, failure(409, "version_conflict"), true)
			return err
		}
		if err != nil {
			return err
		}
		if err := s.at("read:bound"); err != nil {
			return err
		}
		finished, err = finishLocked(ctx, tx, ok, true)
		if err != nil {
			return err
		}
		return s.at("read:finished")
	})
	switch {
	case errors.Is(err, errNotPending):
		return s.settledRead(ctx, ok, records, failure)
	case errors.Is(err, errCommitUnknown):
		// Read back within recoverWait: an outcome stored under this lease,
		// with these records, is what committed.
		reading, cancel := recovering(ctx)
		defer cancel()
		if stored, settledErr := s.settledRead(reading, ok, records, failure); stored.ID != "" {
			return stored, settledErr
		}
		return Receipt{}, err
	case err != nil:
		return Receipt{}, err
	case refused != nil:
		return finished, refused
	}
	return finished, nil
}

// settledRead answers a FinishRead whose receipt finished already.
func (s *Store) settledRead(ctx context.Context, ok ReceiptFinish, records []RecordVersion,
	failure func(int, string) ReceiptFinish) (Receipt, error) {
	r, err := s.settled(ctx, ok, true)
	if errors.Is(err, ErrAlreadyFinished) {
		for code, refusal := range map[string]error{"source_changed": ErrSourceChanged,
			"wrong_source": ErrWrongSource, "version_conflict": ErrVersionConflict} {
			status := 409
			if code == "wrong_source" {
				status = 502
			}
			if same, err := s.settled(ctx, failure(status, code), true); err == nil {
				return same, refusal
			}
		}
		return Receipt{}, err
	}
	if err != nil {
		return Receipt{}, err
	}
	// Same references; the retry must also carry the same versions.
	bound, err := s.ReceiptRecords(ctx, r.WorkspaceID, r.ID)
	if err != nil {
		return Receipt{}, err
	}
	for i, v := range bound {
		if v.version() != records[i].version() || !bytes.Equal(v.Body, records[i].Body) {
			return Receipt{}, ErrAlreadyFinished
		}
	}
	return r, nil
}

// FailRead finishes a source read whose read failed, as an error with no
// rows: 403 for a denial, or whatever status and code the reader gave.
func (s *Store) FailRead(ctx context.Context, receiptID, leaseToken string, httpStatus int, code string, elapsedMS float64) (Receipt, error) {
	f := ReceiptFinish{ReceiptID: receiptID, LeaseToken: leaseToken, Status: "error", HTTPStatus: httpStatus,
		ElapsedMS: elapsedMS, Error: code}
	if err := f.check(); err != nil {
		return Receipt{}, err
	}
	if !validUUID(receiptID) || !validUUID(leaseToken) {
		return Receipt{}, ErrNotFound
	}
	var finished Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var err error
		finished, err = finishLocked(ctx, tx, f, true)
		return err
	})
	switch {
	case errors.Is(err, errNotPending):
		return s.settled(ctx, f, true)
	case errors.Is(err, errCommitUnknown):
		return s.finishStored(ctx, f, true, err)
	}
	return finished, err
}

func insertVersion(ctx context.Context, tx pgx.Tx, workspaceID string, v RecordVersion) (string, error) {
	version := v.version()
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO source_records (workspace_id, source, record_table, record_id, content_version,
			raw_body, body_sha256)
		VALUES ($1, $2, $3, $4, $5, $6, sha256($6))
		ON CONFLICT (workspace_id, source, record_table, record_id, content_version) DO NOTHING
		RETURNING id`, workspaceID, v.Source, v.Table, v.RecordID, version, v.Body).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `
		SELECT id, body_sha256 FROM source_records
		 WHERE workspace_id = $1 AND source = $2 AND record_table = $3 AND record_id = $4
		   AND content_version = $5`, workspaceID, v.Source, v.Table, v.RecordID, version).
		Scan(&id, &stored); err != nil {
		return "", err
	}
	digest := sha256.Sum256(v.Body)
	if !bytes.Equal(stored, digest[:]) {
		return "", ErrVersionConflict
	}
	return id, nil
}

// ReadFailure is an error a source read returns to finish its receipt with
// a given status and code. Any other error finishes it as 200 request_failed,
// as the oracle does for a failed tool. RunChildRead also returns one for a
// read that finished as an error earlier.
type ReadFailure struct {
	HTTPStatus int
	Code       string
}

func (e *ReadFailure) Error() string { return e.Code }

// RunChildRead runs one source read under its own receipt, step by step:
//  1. a pending child receipt is committed, and only this caller holds its
//     lease; if that fails the read does not run;
//  2. the read runs;
//  3. FinishRead stores and binds its record versions and finishes the receipt,
//     or a failed read finishes it as an error with no rows.
//
// The records are returned only once their receipt has finished ok. A retry
// of a read that finished returns its committed outcome without reading.
func (s *Store) RunChildRead(ctx context.Context, in ChildReadStart,
	read func(context.Context) ([]RecordVersion, error)) (Receipt, []RecordVersion, error) {
	receipt, err := s.StartChildRead(ctx, in)
	if err != nil {
		return Receipt{}, nil, err
	}
	if receipt.Status != "pending" {
		if receipt.Status != "ok" {
			return receipt, nil, &ReadFailure{HTTPStatus: *receipt.HTTPStatus, Code: deref(receipt.Error)}
		}
		versions, err := s.ReceiptRecords(ctx, receipt.WorkspaceID, receipt.ID)
		return receipt, versions, err
	}
	started := time.Now()
	records, readErr := read(ctx)
	elapsed := float64(time.Since(started).Microseconds()) / 1000
	if readErr != nil {
		status, code := 200, "request_failed"
		var typed *ReadFailure
		if errors.As(readErr, &typed) {
			status, code = typed.HTTPStatus, typed.Code
		}
		finished, err := s.FailRead(ctx, receipt.ID, receipt.LeaseToken, status, code, elapsed)
		if err != nil {
			return Receipt{}, nil, errors.Join(readErr, err)
		}
		return finished, nil, readErr
	}
	finished, err := s.FinishRead(ctx, receipt.ID, receipt.LeaseToken, records, elapsed)
	if err != nil {
		return finished, nil, err
	}
	return finished, records, nil
}

// ReceiptByOperation finds a receipt of a workspace by its operation ID,
// which is how a caller that lost an acknowledgement learns what was
// committed. It gives no lease: starting the operation again does.
func (s *Store) ReceiptByOperation(ctx context.Context, workspaceID, operationID string) (Receipt, error) {
	r, err := s.receiptByOperation(ctx, workspaceID, operationID)
	r.LeaseToken = ""
	return r, err
}

func (s *Store) receiptByOperation(ctx context.Context, workspaceID, operationID string) (Receipt, error) {
	if !validUUID(workspaceID) {
		return Receipt{}, ErrNotFound
	}
	return scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.workspace_id = $1 AND r.operation_id = $2`,
		workspaceID, operationID))
}

// Receipt reads one receipt of a workspace, for the owner, without its lease.
func (s *Store) Receipt(ctx context.Context, workspaceID, receiptID string) (Receipt, error) {
	if !validUUID(workspaceID) || !validUUID(receiptID) {
		return Receipt{}, ErrNotFound
	}
	r, err := scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.workspace_id = $1 AND r.id = $2`,
		workspaceID, receiptID))
	r.LeaseToken = ""
	return r, err
}

func (s *Store) receiptByID(ctx context.Context, receiptID string) (Receipt, error) {
	return scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.id = $1`, receiptID))
}

// ReceiptRecords gives the record versions bound to a receipt, in order.
func (s *Store) ReceiptRecords(ctx context.Context, workspaceID, receiptID string) ([]RecordVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.source, v.record_table, v.record_id, v.content_version, v.raw_body
		  FROM receipt_records b
		  JOIN source_records v ON v.workspace_id = b.workspace_id AND v.id = b.source_record_id
		 WHERE b.workspace_id = $1 AND b.receipt_id = $2
		 ORDER BY b.ordinal`, workspaceID, receiptID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RecordVersion, error) {
		var v RecordVersion
		err := row.Scan(&v.Source, &v.Table, &v.RecordID, &v.ContentVersion, &v.Body)
		return v, err
	})
}

// MarkDelivered records that a finished receipt's reply reached the caller.
// Marking it twice keeps the first time.
func (s *Store) MarkDelivered(ctx context.Context, receiptID string) error {
	if !validUUID(receiptID) {
		return ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO receipt_deliveries (receipt_id, workspace_id)
		SELECT id, workspace_id FROM receipts WHERE id = $1
		ON CONFLICT (receipt_id) DO NOTHING`, receiptID)
	if sqlState(err) == "GT008" {
		return errors.New("a pending receipt cannot be delivered")
	}
	return err
}

// abandon takes over one pending receipt whose lease expired and finishes it
// as 503 interrupted, in one transaction. errLeaseLive means the lease is
// still held.
func (s *Store) abandon(ctx context.Context, id string) (Receipt, error) {
	var finished Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var err error
		finished, err = abandonLocked(ctx, tx, id)
		return err
	})
	return finished, err
}

func abandonLocked(ctx context.Context, tx pgx.Tx, id string) (Receipt, error) {
	var token string
	var sourceRead bool
	err := tx.QueryRow(ctx, `
		UPDATE receipts SET lease_token = gen_random_uuid(), lease_fence = lease_fence + 1,
			lease_expires_at = clock_timestamp() + interval '1 minute'
		 WHERE id = $1 AND status = 'pending' AND lease_expires_at <= clock_timestamp()
		RETURNING lease_token, connection_id IS NOT NULL`, id).Scan(&token, &sourceRead)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, errLeaseLive
	}
	if err != nil {
		return Receipt{}, err
	}
	return finishLocked(ctx, tx, ReceiptFinish{ReceiptID: id, LeaseToken: token,
		Status: "error", HTTPStatus: 503, Error: "interrupted"}, sourceRead)
}

// AbandonExpired finishes, as 503 "interrupted", pending receipts whose
// lease expired: their worker stopped between committing the receipt and
// finishing it. Children go before parents; a parent with a child still
// leased is left for a later pass. It returns how many it finished.
func (s *Store) AbandonExpired(ctx context.Context, limit int) (int, error) {
	finished := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM receipts
			 WHERE status = 'pending' AND lease_expires_at <= clock_timestamp()
			 ORDER BY parent_id IS NULL, started_at
			 LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
				_, err := abandonLocked(ctx, sp, id)
				return err
			})
			if err == nil {
				finished++
			} else if !errors.Is(err, ErrPendingChild) {
				return err
			}
		}
		return nil
	})
	return finished, err
}

func checkLease(lease time.Duration) error {
	if lease <= 0 || lease > time.Hour {
		return errors.New("a receipt lease is between zero and one hour")
	}
	return nil
}

// sameJSON compares two JSON values as strictjson reads them, as every
// receipts.Store compares a replayed request: key order and spacing aside,
// an integer apart from a number with a fraction, and a number by its value
// (1.0 is 1.00). A value strictjson refuses is the same as nothing.
func sameJSON(stored, given json.RawMessage) bool {
	canonical := func(raw json.RawMessage) (string, bool) {
		value, err := strictjson.Loads(raw)
		if err != nil {
			return "", false
		}
		text, err := strictjson.Dumps(value, strictjson.Prepared)
		return text, err == nil
	}
	a, okA := canonical(stored)
	b, okB := canonical(given)
	return okA && okB && a == b
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validUUID(s string) bool { return uuidPattern.MatchString(s) }

// ReceiptRecordIDs gives the record version IDs a read bound, by row reference.
func (s *Store) ReceiptRecordIDs(ctx context.Context, workspaceID, receiptID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.source || '/' || v.record_table || '/' || v.record_id, v.id::text
		  FROM receipt_records b JOIN source_records v ON v.id = b.source_record_id
		 WHERE b.workspace_id = $1 AND b.receipt_id = $2`, workspaceID, receiptID)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	var ref, id string
	_, err = pgx.ForEachRow(rows, []any{&ref, &id}, func() error { ids[ref] = id; return nil })
	return ids, err
}
