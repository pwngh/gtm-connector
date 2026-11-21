package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// ReceiptStore is receipts.Store on this database, so the card maker reads,
// seals and publishes through the same rows and triggers as every other
// writer. A receipt's ID and start time are the database's. The card a
// publication writes is of kind "go": every unit binds its read receipt and
// the record version that read returned.
type ReceiptStore struct {
	Store *Store
	// Lease is how long a started receipt's holder has to finish it before
	// AbandonExpired may take it over. Zero is five minutes.
	Lease time.Duration
}

var _ receipts.Store = (*ReceiptStore)(nil)

// noLease stands for a lease that is not a UUID: no receipt holds it.
const noLease = "00000000-0000-0000-0000-000000000000"

func (rs *ReceiptStore) lease() time.Duration {
	if rs.Lease == 0 {
		return 5 * time.Minute
	}
	return rs.Lease
}

func (rs *ReceiptStore) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	if err := s.Check(); err != nil {
		return receipts.Receipt{}, err
	}
	arguments, err := encodeJSON(s.Arguments)
	if err != nil {
		return receipts.Receipt{}, err
	}
	requestID, err := encodeJSON(s.RequestID.Value())
	if err != nil {
		return receipts.Receipt{}, err
	}
	in := ReceiptStart{WorkspaceID: s.WorkspaceID, OperationID: s.OperationID, PrincipalID: s.PrincipalID,
		TokenID: s.TokenID, Tool: s.Tool, Scope: s.Scope, AccountRef: s.AccountRef, ConnectionID: s.ConnectionID,
		Arguments: arguments, RequestID: requestID, Lease: rs.lease()}
	if s.BriefID != nil {
		in.BriefID = *s.BriefID
	}
	r, err := rs.Store.StartReceipt(ctx, in)
	return converted(r, err)
}

func (rs *ReceiptStore) StartChild(ctx context.Context, s receipts.ChildStart) (receipts.Receipt, error) {
	if err := s.Check(); err != nil {
		return receipts.Receipt{}, err
	}
	arguments, err := encodeJSON(s.Arguments)
	if err != nil {
		return receipts.Receipt{}, err
	}
	in := ChildReadStart{ParentID: s.Parent, Ordinal: s.Ordinal, Tool: s.Tool, Scope: s.Scope,
		ConnectionID: s.ConnectionID, AccountRef: s.AccountRef, Arguments: arguments, Lease: rs.lease()}
	if s.BriefID != nil {
		in.BriefID = *s.BriefID
	}
	r, err := rs.Store.StartChildRead(ctx, in)
	if errors.Is(err, ErrOperationReused) {
		return receipts.Receipt{}, fmt.Errorf("%w: %w", receipts.ErrDuplicateChild, err)
	}
	return converted(r, err)
}

func (rs *ReceiptStore) Finish(ctx context.Context, t receipts.Ticket, o receipts.Outcome) (receipts.Receipt, error) {
	if err := o.Check(); err != nil {
		return receipts.Receipt{}, err
	}
	if !validUUID(t.AuditID) {
		return receipts.Receipt{}, receipts.ErrNotFound
	}
	lease := ticketLease(t)
	code := ""
	if o.Error != nil {
		code = *o.Error
	}
	r, err := rs.Store.FinishReceipt(ctx, ReceiptFinish{ReceiptID: t.AuditID, LeaseToken: lease, Status: o.Status,
		HTTPStatus: o.HTTPStatus, Rows: o.Rows, ElapsedMS: o.ElapsedMS, Error: code})
	if errors.Is(err, ErrSourceRead) {
		// A source read finishes ok only through FinishRead, which binds its rows.
		if o.Status == "ok" {
			return receipts.Receipt{}, fmt.Errorf("%w: %w", receipts.ErrInvalid, err)
		}
		r, err = rs.Store.FailRead(ctx, t.AuditID, lease, o.HTTPStatus, code, o.ElapsedMS)
	}
	return converted(r, err)
}

func (rs *ReceiptStore) FinishRead(ctx context.Context, t receipts.Ticket, versions []receipts.RecordVersion,
	elapsedMS float64) (receipts.Receipt, error) {
	records := make([]RecordVersion, len(versions))
	for i, v := range versions {
		if v.Source == "" || v.Table == "" || v.RecordID == "" {
			return receipts.Receipt{}, fmt.Errorf("%w: a record version needs its source, table and ID",
				receipts.ErrInvalid)
		}
		records[i] = RecordVersion{Source: v.Source, Table: v.Table, RecordID: v.RecordID,
			ContentVersion: v.ContentVersion, Body: v.Body}
	}
	if !validUUID(t.AuditID) {
		return receipts.Receipt{}, receipts.ErrNotFound
	}
	r, err := rs.Store.FinishRead(ctx, t.AuditID, ticketLease(t), records, elapsedMS)
	if err != nil {
		return receipts.Receipt{}, receiptError(err)
	}
	return converted(r, nil)
}

func (rs *ReceiptStore) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	if !validUUID(p.WorkspaceID) || !validUUID(p.Seal.AuditID) {
		return receipts.Card{}, receipts.ErrNotFound
	}
	units := make([]Unit, len(p.Units))
	var cited []string
	for _, u := range p.Units {
		if validUUID(u.ReceiptID) {
			cited = append(cited, u.ReceiptID)
		}
	}
	bound, err := rs.Store.versionIDs(ctx, p.WorkspaceID, cited)
	if err != nil {
		return receipts.Card{}, err
	}
	for i, u := range p.Units {
		units[i] = Unit{N: u.N, Kind: u.Kind, RecordRef: u.RecordRef, Tool: u.Tool, Shown: u.Shown,
			ReceiptID: u.ReceiptID, Body: append(json.RawMessage(nil), u.Value...),
			SourceRecordID: bound[[3]string{u.ReceiptID, u.RecordRef, u.ContentVersion}]}
	}
	gaps := make([]Gap, len(p.Gaps))
	for i, g := range p.Gaps {
		gaps[i] = Gap{Code: g.Code, Disposition: g.Disposition, Text: g.Text,
			ByRole: append(json.RawMessage(nil), g.ByRole...)}
		if g.Count != nil {
			if !g.Count.IsInt64() || g.Count.Int64() > math.MaxInt32 {
				return receipts.Card{}, fmt.Errorf("%w: gap %s counts past what a card stores", receipts.ErrInvalid, g.Code)
			}
			n := int(g.Count.Int64())
			gaps[i].Count = &n
		}
	}
	lines := make([]Line, len(p.Lines))
	for i, l := range p.Lines {
		lines[i] = Line{Section: l.Section, Body: append(json.RawMessage(nil), l.Value...)}
	}
	var rules *string
	if p.RulesVersion != "" {
		rules = &p.RulesVersion
	}
	if p.Kind != "card" && p.Kind != "selected_brief" {
		return receipts.Card{}, fmt.Errorf("%w: a publication is a card or a selected_brief", receipts.ErrInvalid)
	}
	card, err := rs.Store.PublishCard(ctx, CardPublication{WorkspaceID: p.WorkspaceID, AccountRef: p.AccountRef,
		BriefID: p.BriefID, ParentReceiptID: p.Seal.AuditID, PrincipalID: p.PrincipalID,
		SealReceiptID: p.Seal.AuditID, SealLeaseToken: ticketLease(p.Seal), Kind: "go",
		SchemaVersion: p.SchemaVersion, RulesVersion: rules, Bytes: p.Bytes, Units: units, Gaps: gaps, Lines: lines,
		ReadReceiptIDs: p.Reads, ElapsedMS: p.ElapsedMS})
	if err != nil {
		return receipts.Card{}, receiptError(err)
	}
	return rs.committed(ctx, card)
}

// committed is a published card as the store holds it: its bytes, its seal,
// and its projections as written, whatever a retry sent.
func (rs *ReceiptStore) committed(ctx context.Context, card Card) (receipts.Card, error) {
	seal, err := rs.Store.Receipt(ctx, card.WorkspaceID, card.SealReceiptID)
	if err != nil {
		return receipts.Card{}, receiptError(err)
	}
	sealed, err := converted(seal, nil)
	if err != nil {
		return receipts.Card{}, err
	}
	kind := "card"
	payload, err := receipts.ParsePayload(card.Bytes)
	if err != nil {
		return receipts.Card{}, err
	}
	if payload.Prepared {
		kind = "selected_brief"
	}
	out := receipts.Card{WorkspaceID: card.WorkspaceID, AccountRef: card.AccountRef, BriefID: card.BriefID,
		Kind: kind, Bytes: append([]byte(nil), card.Bytes...), SHA256: hex.EncodeToString(card.SHA256), Seal: sealed}
	rows, err := rs.Store.pool.Query(ctx, `
		SELECT u.n, u.kind, u.record_ref, u.tool, coalesce(u.shown, ''), coalesce(u.receipt_id::text, ''),
		       coalesce(v.content_version, '')
		  FROM evidence_units u LEFT JOIN source_records v ON v.id = u.source_record_id
		 WHERE u.workspace_id = $1 AND u.card_id = $2 ORDER BY u.n`, card.WorkspaceID, card.ID)
	if err != nil {
		return receipts.Card{}, err
	}
	out.Units, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (receipts.UnitRow, error) {
		var u receipts.UnitRow
		err := row.Scan(&u.N, &u.Kind, &u.RecordRef, &u.Tool, &u.Shown, &u.ReceiptID, &u.ContentVersion)
		return u, err
	})
	if err != nil {
		return receipts.Card{}, err
	}
	// A unit's value is its element of the sealed bytes, which its row equals.
	for i := range out.Units {
		if i >= len(payload.Units) {
			return receipts.Card{}, fmt.Errorf("card %s holds more units than its bytes", card.ID)
		}
		value, err := strictjson.Dumps(payload.Units[i], strictjson.Prepared)
		if err != nil {
			return receipts.Card{}, err
		}
		out.Units[i].Value = []byte(value)
	}
	stored, err := gaps(ctx, rs.Store.pool, card.WorkspaceID, card.ID)
	if err != nil {
		return receipts.Card{}, err
	}
	for _, g := range stored {
		row := receipts.GapRow{Code: g.Code, Disposition: g.Disposition, Text: g.Text}
		if g.Count != nil {
			row.Count = big.NewInt(int64(*g.Count))
		}
		if len(g.ByRole) > 0 {
			row.ByRole = []byte(g.ByRole)
		}
		out.Gaps = append(out.Gaps, row)
	}
	held, err := lines(ctx, rs.Store.pool, card.WorkspaceID, card.ID)
	if err != nil {
		return receipts.Card{}, err
	}
	// A line's value is written as the maker encodes it; jsonb keeps it as data, not as those bytes.
	for _, l := range held {
		value, err := strictjson.Loads(l.Body)
		if err != nil {
			return receipts.Card{}, err
		}
		encoded, err := strictjson.Dumps(value, strictjson.Prepared)
		if err != nil {
			return receipts.Card{}, err
		}
		out.Lines = append(out.Lines, receipts.LineRow{Section: l.Section, Value: []byte(encoded)})
	}
	return out, nil
}

func (rs *ReceiptStore) Read(ctx context.Context, workspaceID string, auditIDs []string) ([]receipts.Receipt, error) {
	out := make([]receipts.Receipt, len(auditIDs))
	for i, id := range auditIDs {
		r, err := rs.Store.Receipt(ctx, workspaceID, id)
		if err != nil {
			return nil, receiptError(err)
		}
		r.LeaseToken = ""
		if out[i], err = converted(r, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (rs *ReceiptStore) Records(ctx context.Context, workspaceID, auditID string) ([]receipts.RecordVersion, error) {
	if _, err := rs.Store.Receipt(ctx, workspaceID, auditID); err != nil {
		return nil, receiptError(err)
	}
	bound, err := rs.Store.ReceiptRecords(ctx, workspaceID, auditID)
	if err != nil {
		return nil, err
	}
	out := make([]receipts.RecordVersion, len(bound))
	for i, v := range bound {
		out[i] = receipts.RecordVersion{Source: v.Source, Table: v.Table, RecordID: v.RecordID,
			ContentVersion: v.ContentVersion, Body: v.Body}
	}
	return out, nil
}

func (rs *ReceiptStore) ByOperation(ctx context.Context, workspaceID, operationID string) (receipts.Receipt, error) {
	return converted(rs.Store.ReceiptByOperation(ctx, workspaceID, operationID))
}

// versionIDs gives the record version IDs these reads bound, by read,
// reference and content version.
func (s *Store) versionIDs(ctx context.Context, workspaceID string, reads []string) (map[[3]string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.receipt_id::text, v.source || '/' || v.record_table || '/' || v.record_id, v.content_version, v.id::text
		  FROM receipt_records b JOIN source_records v ON v.id = b.source_record_id
		 WHERE b.workspace_id = $1 AND b.receipt_id = ANY($2::uuid[])`, workspaceID, reads)
	if err != nil {
		return nil, err
	}
	ids := map[[3]string]string{}
	var read, ref, version, id string
	_, err = pgx.ForEachRow(rows, []any{&read, &ref, &version, &id}, func() error {
		ids[[3]string{read, ref, version}] = id
		return nil
	})
	return ids, err
}

// AuditValue is the receipt as the audit log's readers return it, as a card
// embeds it.
func (r Receipt) AuditValue() (*strictjson.Object, error) {
	converted, err := converted(r, nil)
	if err != nil {
		return nil, err
	}
	return converted.Value(), nil
}

func ticketLease(t receipts.Ticket) string {
	if !validUUID(t.Lease) {
		return noLease
	}
	return t.Lease
}

func encodeJSON(value any) (json.RawMessage, error) {
	text, err := strictjson.Dumps(value, strictjson.Quoted)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", receipts.ErrInvalid, err)
	}
	return json.RawMessage(text), nil
}

// converted is a stored receipt as receipts.Receipt, or err in the
// contract's terms.
func converted(r Receipt, err error) (receipts.Receipt, error) {
	if err != nil {
		return receipts.Receipt{}, receiptError(err)
	}
	arguments, err := strictjson.Loads(r.Arguments)
	if err != nil {
		return receipts.Receipt{}, fmt.Errorf("receipt %s arguments: %w", r.ID, err)
	}
	value, err := strictjson.Loads(r.RequestID)
	if err != nil {
		return receipts.Receipt{}, fmt.Errorf("receipt %s request_id: %w", r.ID, err)
	}
	requestID, err := receipts.ParseRequestID(value)
	if err != nil {
		return receipts.Receipt{}, fmt.Errorf("receipt %s: %w", r.ID, err)
	}
	out := receipts.Receipt{AuditID: r.ID, StartedAt: r.StartedAt.UTC(), TokenID: r.TokenID, Tool: r.Tool,
		Arguments: arguments, RequestID: requestID, Status: r.Status, HTTPStatus: r.HTTPStatus,
		Rows: append([]string{}, r.Rows...), ElapsedMS: r.ElapsedMS, Error: r.Error, WorkspaceID: r.WorkspaceID,
		OperationID: r.OperationID, PrincipalID: r.PrincipalID, Scope: r.Scope, AccountRef: r.RequestedAccount,
		ConnectionID: r.ConnectionID, Generation: r.ConnectionGeneration, Lease: r.LeaseToken, Replay: r.Replay}
	if r.BriefID != "" {
		brief := r.BriefID
		out.BriefID = &brief
	}
	return out, nil
}

// receiptError keeps err and adds the receipts error a caller of
// receipts.Store matches.
func receiptError(err error) error {
	for _, pair := range []struct{ ours, theirs error }{
		{ErrNotFound, receipts.ErrNotFound},
		{ErrAlreadyFinished, receipts.ErrFinished},
		{ErrLeaseLost, receipts.ErrLeaseLost},
		{ErrOperationReused, receipts.ErrOperationReused},
		{ErrSourceChanged, receipts.ErrSourceChanged},
		{ErrVersionConflict, receipts.ErrVersionConflict},
		{ErrNotPermitted, receipts.ErrNotPermitted},
		{ErrParent, receipts.ErrParent},
		{ErrInProgress, receipts.ErrInProgress},
		{ErrInvalidOperation, receipts.ErrInvalid},
		{ErrPendingChild, receipts.ErrInvalid},
		{ErrWrongSource, receipts.ErrInvalid},
		{ErrNotSourceRead, receipts.ErrInvalid},
		{ErrProvenance, receipts.ErrProvenance},
		{ErrProjection, receipts.ErrProjection},
		{ErrSealMismatch, receipts.ErrInvalid},
		{ErrInvalidArguments, receipts.ErrInvalid},
		{ErrRecordRows, receipts.ErrInvalid},
	} {
		if errors.Is(err, pair.ours) {
			return fmt.Errorf("%w: %w", pair.theirs, err)
		}
	}
	if sqlState(err) == "23505" {
		return fmt.Errorf("%w: %w", receipts.ErrInvalid, err) // a brief published already
	}
	return err
}
