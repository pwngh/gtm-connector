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
	"time"

	"github.com/jackc/pgx/v5"
)

// Receipt errors. Callers match them with errors.Is.
var (
	// ErrAlreadyFinished: the receipt finished earlier with another outcome.
	ErrAlreadyFinished = errors.New("receipt already finished")
	// ErrLeaseLost: the caller's lease expired or was taken over.
	ErrLeaseLost = errors.New("receipt lease expired or taken over")
	// ErrOperationReused: the operation ID names a different request.
	ErrOperationReused = errors.New("operation ID already used for a different request")
	// ErrOrdinalTaken: the parent already has a child with this ordinal.
	ErrOrdinalTaken = errors.New("the parent receipt already has a child with this ordinal")
	// ErrParentFinished: a child read cannot start under a finished parent.
	ErrParentFinished = errors.New("the parent receipt is not pending")
	// ErrPendingChild: a parent cannot finish while a child read is pending.
	ErrPendingChild = errors.New("a child receipt is still pending")
	// ErrSourceChanged: the connection's generation changed after the read started.
	ErrSourceChanged = errors.New("the source changed after the read started")
	// ErrVersionConflict: one content version of a record arrived with two bodies.
	ErrVersionConflict = errors.New("a record version already exists with a different body")
)

// Receipt is one row of the receipts table.
type Receipt struct {
	ID, WorkspaceID, OperationID string
	ParentID                     string
	Ordinal                      *int
	PrincipalID                  string
	TokenID, Tool, Scope         string
	AccountRef                   string
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
	LeaseToken                   string
	LeaseExpiresAt               time.Time
	LeaseFence                   int64
	DeliveredAt                  *time.Time
}

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
	r.token_id, r.tool, r.scope, r.account_ref, r.connection_id, r.connection_generation,
	r.arguments, r.request_id, r.brief_id, r.started_at, r.status, r.http_status, r.rows,
	r.elapsed_ms, r.error, r.finished_at, r.lease_token, r.lease_expires_at, r.lease_fence,
	(SELECT d.delivered_at FROM receipt_deliveries d WHERE d.receipt_id = r.id)`

func scanReceipt(row pgx.Row) (Receipt, error) {
	var r Receipt
	var parent, principal, scope, account, connection, brief *string
	var generation *int64
	var rows []byte
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.OperationID, &parent, &r.Ordinal, &principal,
		&r.TokenID, &r.Tool, &scope, &account, &connection, &generation,
		&r.Arguments, &r.RequestID, &brief, &r.StartedAt, &r.Status, &r.HTTPStatus, &rows,
		&r.ElapsedMS, &r.Error, &r.FinishedAt, &r.LeaseToken, &r.LeaseExpiresAt, &r.LeaseFence,
		&r.DeliveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	r.ParentID, r.PrincipalID, r.Scope = deref(parent), deref(principal), deref(scope)
	r.AccountRef, r.ConnectionID, r.BriefID = deref(account), deref(connection), deref(brief)
	if generation != nil {
		r.ConnectionGeneration = *generation
	}
	if err := json.Unmarshal(rows, &r.Rows); err != nil {
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

// ReceiptStart is what a top-level receipt records before its tool runs.
type ReceiptStart struct {
	WorkspaceID string
	// OperationID stays the same across retries of one request. A retry
	// after a lost acknowledgement gets the receipt the first attempt made.
	OperationID string
	PrincipalID string // empty when no principal is known
	TokenID     string // TokenID(raw)
	Tool        string
	Scope       string // empty when the tool needs none
	AccountRef  string // empty when the request names no account
	Arguments   json.RawMessage
	RequestID   json.RawMessage // a JSON-RPC id, null, or "<invalid>"; nil means null
	BriefID     string
	Lease       time.Duration // how long the caller may take to finish it
}

// StartReceipt commits a pending receipt in its own transaction, before the
// caller may run the tool. The same operation ID with the same request gives
// back the existing receipt; with a different request it is refused.
func (s *Store) StartReceipt(ctx context.Context, in ReceiptStart) (Receipt, error) {
	if err := checkLease(in.Lease); err != nil {
		return Receipt{}, err
	}
	requestID := in.RequestID
	if requestID == nil {
		requestID = json.RawMessage("null")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO receipts AS r (workspace_id, operation_id, principal_id, token_id, tool, scope,
			account_ref, arguments, request_id, brief_id, lease_token, lease_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, gen_random_uuid(),
			clock_timestamp() + make_interval(secs => $11))
		ON CONFLICT (operation_id) DO NOTHING
		RETURNING `+receiptColumns,
		in.WorkspaceID, in.OperationID, nullable(in.PrincipalID), in.TokenID, in.Tool, nullable(in.Scope),
		nullable(in.AccountRef), in.Arguments, requestID, nullable(in.BriefID), in.Lease.Seconds())
	r, err := scanReceipt(row)
	if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	existing, err := s.ReceiptByOperation(ctx, in.OperationID)
	if err != nil {
		return Receipt{}, err
	}
	if existing.WorkspaceID != in.WorkspaceID || existing.ParentID != "" ||
		existing.PrincipalID != in.PrincipalID || existing.TokenID != in.TokenID ||
		existing.Tool != in.Tool || existing.AccountRef != in.AccountRef ||
		!sameJSON(existing.Arguments, in.Arguments) {
		return Receipt{}, ErrOperationReused
	}
	return existing, nil
}

// ChildReadStart is what a child read receipt records before the read.
type ChildReadStart struct {
	ParentID     string
	Ordinal      int
	Tool         string
	Scope        string
	ConnectionID string
	AccountRef   string // empty to take the parent's
	Arguments    json.RawMessage
	Lease        time.Duration
}

// StartChildRead commits a pending child receipt in its own transaction,
// before a source read. It takes the workspace, principal and token from the
// parent, which must be pending, and records the connection's current
// generation. Its operation ID is the parent's, then "#" and the ordinal, so
// a retry finds it again.
func (s *Store) StartChildRead(ctx context.Context, in ChildReadStart) (Receipt, error) {
	if err := checkLease(in.Lease); err != nil {
		return Receipt{}, err
	}
	if in.Ordinal < 0 {
		return Receipt{}, errors.New("a child ordinal is never negative")
	}
	if !validUUID(in.ParentID) || !validUUID(in.ConnectionID) {
		return Receipt{}, ErrNotFound
	}
	requestID, _ := json.Marshal(map[string]any{
		"kind": "brief_child", "parent_audit_id": in.ParentID, "ordinal": in.Ordinal})
	row := s.pool.QueryRow(ctx, `
		INSERT INTO receipts AS r (workspace_id, operation_id, parent_id, ordinal, principal_id, token_id,
			tool, scope, account_ref, connection_id, connection_generation, arguments, request_id,
			lease_token, lease_expires_at)
		SELECT p.workspace_id, p.operation_id || '#' || $2::integer, p.id, $2::integer, p.principal_id, p.token_id,
			$3, $4, coalesce($5, p.account_ref), c.id, c.generation, $6, $7,
			gen_random_uuid(), clock_timestamp() + make_interval(secs => $8)
		  FROM receipts p
		  JOIN connections c ON c.workspace_id = p.workspace_id AND c.id = $9
		 WHERE p.id = $1
		ON CONFLICT (operation_id) DO NOTHING
		RETURNING `+receiptColumns,
		in.ParentID, in.Ordinal, in.Tool, nullable(in.Scope), nullable(in.AccountRef), in.Arguments,
		requestID, in.Lease.Seconds(), in.ConnectionID)
	r, err := scanReceipt(row)
	switch {
	case sqlState(err) == "GT006":
		return Receipt{}, ErrParentFinished
	case sqlState(err) == "23505":
		return Receipt{}, ErrOrdinalTaken
	case !errors.Is(err, ErrNotFound):
		return r, err
	}
	// Either the operation exists already, or the parent or connection does not.
	var operation string
	if err := s.pool.QueryRow(ctx, `SELECT operation_id || '#' || $2::integer FROM receipts WHERE id = $1`,
		in.ParentID, in.Ordinal).Scan(&operation); errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	} else if err != nil {
		return Receipt{}, err
	}
	existing, err := s.ReceiptByOperation(ctx, operation)
	if err != nil {
		return Receipt{}, err
	}
	if existing.Tool != in.Tool || existing.ConnectionID != in.ConnectionID ||
		!sameJSON(existing.Arguments, in.Arguments) {
		return Receipt{}, ErrOperationReused
	}
	return existing, nil
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

// FinishReceipt finishes a pending receipt once, with a guarded UPDATE ...
// WHERE status = 'pending'. The caller must hold a live lease. Finishing it
// again with the same outcome (a retry after a lost acknowledgement) returns
// the finished receipt; with another outcome it gives ErrAlreadyFinished.
func (s *Store) FinishReceipt(ctx context.Context, f ReceiptFinish) (Receipt, error) {
	if err := f.check(); err != nil {
		return Receipt{}, err
	}
	if !validUUID(f.ReceiptID) || !validUUID(f.LeaseToken) {
		return Receipt{}, ErrNotFound
	}
	var finished Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		finished, err = finishLocked(ctx, tx, f)
		return err
	})
	if errors.Is(err, errNotPending) {
		return s.settled(ctx, f)
	}
	return finished, err
}

var errNotPending = errors.New("not pending")

// finishLocked runs the guarded UPDATE inside tx.
func finishLocked(ctx context.Context, tx pgx.Tx, f ReceiptFinish) (Receipt, error) {
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
			elapsed_ms = $7, error = $8, finished_at = clock_timestamp()
		 WHERE r.id = $1 AND r.status = 'pending' AND r.lease_token = $2
		   AND r.lease_expires_at > clock_timestamp()
		RETURNING `+receiptColumns,
		f.ReceiptID, f.LeaseToken, f.Status, f.HTTPStatus, len(rows), rowsJSON, f.ElapsedMS, nullable(f.Error)))
	switch {
	case sqlState(err) == "GT005":
		return Receipt{}, ErrPendingChild
	case sqlState(err) == "GT003":
		return Receipt{}, ErrLeaseLost
	case errors.Is(err, ErrNotFound):
		return Receipt{}, errNotPending
	}
	return r, err
}

// settled explains why a finish matched no pending row: the receipt finished
// already (with this outcome or another), the lease was lost, or it is unknown.
func (s *Store) settled(ctx context.Context, f ReceiptFinish) (Receipt, error) {
	r, err := s.receiptByID(ctx, f.ReceiptID)
	if err != nil {
		return Receipt{}, err
	}
	if r.Status == "pending" {
		return Receipt{}, ErrLeaseLost
	}
	rows := f.Rows
	if rows == nil {
		rows = []string{}
	}
	if r.Status == f.Status && r.HTTPStatus != nil && *r.HTTPStatus == f.HTTPStatus &&
		slices.Equal(r.Rows, rows) && deref(r.Error) == f.Error {
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
		UPDATE receipts SET lease_expires_at = clock_timestamp() + make_interval(secs => $3)
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
// receipt in order, and finishes the receipt ok with their references. If the
// connection's generation changed since the read started, it stores nothing,
// finishes the receipt as an error (409, source_changed) and gives
// ErrSourceChanged. A retry after a lost acknowledgement, with the same
// records, returns the finished receipt.
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
	changed := ReceiptFinish{ReceiptID: receiptID, LeaseToken: leaseToken, Status: "error", HTTPStatus: 409,
		ElapsedMS: elapsedMS, Error: "source_changed"}
	var finished Receipt
	var fenced bool
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var status, workspaceID string
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
		if connectionID != nil {
			var current int64
			// The share lock holds off a generation bump until this commits.
			if err := tx.QueryRow(ctx, `SELECT generation FROM connections WHERE id = $1 FOR SHARE`,
				*connectionID).Scan(&current); err != nil {
				return err
			}
			if current != *generation {
				fenced = true
				finished, err = finishLocked(ctx, tx, changed)
				return err
			}
		}
		if err := s.at("read:locked"); err != nil {
			return err
		}
		for i, v := range records {
			id, err := insertVersion(ctx, tx, workspaceID, v)
			if err != nil {
				return err
			}
			if err := s.at("read:record"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO receipt_records (workspace_id, receipt_id, ordinal, source_record_id)
				VALUES ($1, $2, $3, $4)`, workspaceID, receiptID, i, id); err != nil {
				return err
			}
		}
		if err := s.at("read:bound"); err != nil {
			return err
		}
		finished, err = finishLocked(ctx, tx, ok)
		if err != nil {
			return err
		}
		return s.at("read:finished")
	})
	switch {
	case errors.Is(err, errNotPending):
		r, settleErr := s.settled(ctx, ok)
		if errors.Is(settleErr, ErrAlreadyFinished) {
			if _, sameChange := s.settled(ctx, changed); sameChange == nil {
				return Receipt{}, ErrSourceChanged
			}
		}
		if settleErr != nil {
			return Receipt{}, settleErr
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
	case err != nil:
		return Receipt{}, err
	case fenced:
		return finished, ErrSourceChanged
	}
	return finished, nil
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
// as the oracle does for a failed tool.
type ReadFailure struct {
	HTTPStatus int
	Code       string
}

func (e *ReadFailure) Error() string { return e.Code }

// RunChildRead runs one source read under its own receipt, step by step:
//  1. a pending child receipt is committed; if that fails the read does not run;
//  2. the read runs;
//  3. FinishRead stores and binds its record versions and finishes the receipt,
//     or a failed read finishes it as an error with no rows.
//
// The records are returned only once their receipt has finished ok.
func (s *Store) RunChildRead(ctx context.Context, in ChildReadStart,
	read func(context.Context) ([]RecordVersion, error)) (Receipt, []RecordVersion, error) {
	receipt, err := s.StartChildRead(ctx, in)
	if err != nil {
		return Receipt{}, nil, err
	}
	if receipt.Status != "pending" {
		// A retry of a read that already finished: its outcome stands.
		if receipt.Status != "ok" {
			return receipt, nil, fmt.Errorf("the read finished as %s: %s", receipt.Status, deref(receipt.Error))
		}
		versions, err := s.ReceiptRecords(ctx, receipt.WorkspaceID, receipt.ID)
		return receipt, versions, err
	}
	started := time.Now()
	records, readErr := read(ctx)
	elapsed := float64(time.Since(started).Microseconds()) / 1000
	if readErr != nil {
		failure := ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken, Status: "error",
			HTTPStatus: 200, ElapsedMS: elapsed, Error: "request_failed"}
		var typed *ReadFailure
		if errors.As(readErr, &typed) {
			failure.HTTPStatus, failure.Error = typed.HTTPStatus, typed.Code
		}
		finished, err := s.FinishReceipt(ctx, failure)
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

// ReceiptByOperation finds a receipt by its operation ID, which is how a
// caller that lost an acknowledgement learns what was committed.
func (s *Store) ReceiptByOperation(ctx context.Context, operationID string) (Receipt, error) {
	return scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.operation_id = $1`, operationID))
}

// Receipt reads one receipt of a workspace.
func (s *Store) Receipt(ctx context.Context, workspaceID, receiptID string) (Receipt, error) {
	if !validUUID(workspaceID) || !validUUID(receiptID) {
		return Receipt{}, ErrNotFound
	}
	return scanReceipt(s.pool.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM receipts r WHERE r.workspace_id = $1 AND r.id = $2`,
		workspaceID, receiptID))
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
				var token string
				if err := sp.QueryRow(ctx, `
					UPDATE receipts SET lease_token = gen_random_uuid(), lease_fence = lease_fence + 1,
						lease_expires_at = clock_timestamp() + interval '1 minute'
					 WHERE id = $1 AND status = 'pending' AND lease_expires_at <= clock_timestamp()
					RETURNING lease_token`, id).Scan(&token); err != nil {
					return err
				}
				_, err := finishLocked(ctx, sp, ReceiptFinish{ReceiptID: id, LeaseToken: token,
					Status: "error", HTTPStatus: 503, Error: "interrupted"})
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

// sameJSON compares two JSON values as values: key order and spacing aside,
// as jsonb stores them.
func sameJSON(stored, given json.RawMessage) bool {
	decode := func(raw json.RawMessage) ([]byte, bool) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		encoded, err := json.Marshal(value)
		return encoded, err == nil
	}
	a, okA := decode(stored)
	b, okB := decode(given)
	return okA && okB && bytes.Equal(a, b)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validUUID(s string) bool { return uuidPattern.MatchString(s) }
