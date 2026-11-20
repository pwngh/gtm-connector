package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrNotPermitted means the principal may not do or read this. Unknown and
	// ungranted things answer the same.
	ErrNotPermitted = errors.New("not permitted")
	// ErrProvenance: a unit cites a read or record the card may not rest on.
	ErrProvenance = errors.New("a unit cites a read the card may not rest on")
	// ErrProjection: the units and gaps are not exactly the card bytes' own.
	ErrProjection = errors.New("the units and gaps differ from the card's bytes")
	// ErrSealMismatch: the seal receipt is of another account or brief.
	ErrSealMismatch = errors.New("the seal receipt names another account or brief")
)

// Card is a stored card: its bytes, their digest, and how it was sealed.
type Card struct {
	ID, WorkspaceID, AccountRef, BriefID string
	Kind                                 string // cli, prepared or go
	SchemaVersion                        int
	RulesVersion                         *string
	Bytes                                []byte
	SHA256                               []byte
	ParentReceiptID, SealReceiptID       string
	PublishedAt                          time.Time
}

// Unit is one numbered unit of a card, as projected from its sealed bytes.
type Unit struct {
	CardID         string
	N              int
	Kind           string // record or sentence
	RecordRef      string
	Tool           string
	Shown          string // sentence units: yes, cut, later, long or more
	ReceiptID      string // the read receipt, when bound by ID
	SourceRecordID string // the record version, when bound
	Body           json.RawMessage
}

// Gap is one stated gap of a card.
type Gap struct {
	Code, Disposition string
	Count             *int
	ByRole            json.RawMessage
	Text              string
}

// CardPublication is everything step 3 writes together.
type CardPublication struct {
	WorkspaceID, AccountRef, BriefID string
	// ParentReceiptID is the brief's receipt: every read a unit cites is its
	// child. It may be the seal itself: a prepared brief's reads are the
	// seal's children, and a CLI card's are top-level reads of BriefID by the
	// seal's principal and token.
	ParentReceiptID string
	// PrincipalID, when set, must be the seal's principal. The actor is
	// always the seal's principal; a seal without one is the local owner's.
	PrincipalID string
	// The seal receipt, started (pending) before the card existed.
	SealReceiptID, SealLeaseToken string
	Kind                          string
	SchemaVersion                 int
	RulesVersion                  *string
	// Bytes is a JSON object whose "units" and "gaps" arrays are exactly
	// Units' bodies and Gaps.
	Bytes []byte
	Units []Unit
	Gaps  []Gap
	// ReadReceiptIDs are further reads the card rests on, beside those its
	// units cite, such as a denied read the card states. Each must have
	// finished, for the card's account, under the brief, at its connection's
	// current generation; a read a unit cites must have finished ok.
	ReadReceiptIDs []string
	ElapsedMS      float64
}

// PublishCard is step 3, one transaction. Under the locked seal it checks
// the seal's principal (its live grant, or the owner when it has none), the
// projection of the bytes, and every read the card rests on; then it writes
// the card bytes and digest, the unit and gap projections, and finishes the
// seal naming the digest. The database checks the same at commit.
//
// If a check fails it writes no card, finishes the seal as an error and
// returns ErrNotPermitted (403), ErrSourceChanged (409), ErrProvenance or
// ErrProjection (422). A retry after a lost acknowledgement returns the card.
func (s *Store) PublishCard(ctx context.Context, in CardPublication) (Card, error) {
	if !validUUID(in.WorkspaceID) || !validUUID(in.SealReceiptID) || !validUUID(in.SealLeaseToken) ||
		!validUUID(in.ParentReceiptID) {
		return Card{}, ErrNotFound
	}
	digest := sha256.Sum256(in.Bytes)
	seal := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken, Status: "ok",
		HTTPStatus: 200, ElapsedMS: in.ElapsedMS,
		Rows: []string{"briefs/" + in.BriefID + "/" + hex.EncodeToString(digest[:])}}
	card := Card{WorkspaceID: in.WorkspaceID, AccountRef: in.AccountRef, BriefID: in.BriefID, Kind: in.Kind,
		SchemaVersion: in.SchemaVersion, RulesVersion: in.RulesVersion, Bytes: in.Bytes, SHA256: digest[:],
		ParentReceiptID: in.ParentReceiptID, SealReceiptID: in.SealReceiptID}
	var refused error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var status, account, brief string
		var principal *string
		var leaseHeld bool
		err := tx.QueryRow(ctx, `
			SELECT status, coalesce(account_ref, ''), coalesce(brief_id, ''), principal_id,
			       lease_token = $3 AND lease_expires_at > clock_timestamp()
			  FROM receipts WHERE id = $1 AND workspace_id = $2 AND parent_id IS NULL FOR UPDATE`,
			in.SealReceiptID, in.WorkspaceID, in.SealLeaseToken).Scan(&status, &account, &brief, &principal, &leaseHeld)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case status != "pending":
			return errNotPending
		case !leaseHeld:
			return ErrLeaseLost
		case account != in.AccountRef || brief != "" && brief != in.BriefID:
			return ErrSealMismatch
		}
		refused, err = s.recheck(ctx, tx, in, principal)
		if err != nil {
			return err
		}
		if refused != nil {
			failure := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken,
				Status: "error", HTTPStatus: 422, ElapsedMS: in.ElapsedMS, Error: "provenance"}
			switch {
			case errors.Is(refused, ErrNotPermitted):
				failure.HTTPStatus, failure.Error = 403, "not_permitted"
			case errors.Is(refused, ErrSourceChanged):
				failure.HTTPStatus, failure.Error = 409, "source_changed"
			case errors.Is(refused, ErrProjection):
				failure.Error = "projection"
			}
			_, err := finishLocked(ctx, tx, failure, false)
			return err
		}
		if err := s.at("publish:checked"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
				card_bytes, card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, sha256($7), $8, $9, $10, $11)
			RETURNING id, published_at`,
			in.WorkspaceID, in.AccountRef, in.BriefID, in.Kind, in.SchemaVersion, in.RulesVersion,
			in.Bytes, in.ParentReceiptID, in.SealReceiptID, len(in.Units), len(in.Gaps)).
			Scan(&card.ID, &card.PublishedAt); err != nil {
			return err
		}
		// One round trip for the projections.
		batch := &pgx.Batch{}
		for _, u := range in.Units {
			batch.Queue(`
				INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, shown,
					receipt_id, source_record_id, body)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				in.WorkspaceID, card.ID, u.N, u.Kind, u.RecordRef, u.Tool, nullable(u.Shown),
				nullable(u.ReceiptID), nullable(u.SourceRecordID), u.Body)
		}
		for i, g := range in.Gaps {
			batch.Queue(`
				INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, count, by_role, text)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				in.WorkspaceID, card.ID, i, g.Code, g.Disposition, g.Count, nullJSON(g.ByRole), g.Text)
		}
		results := tx.SendBatch(ctx, batch)
		for _, u := range in.Units {
			if _, err := results.Exec(); err != nil {
				results.Close()
				return fmt.Errorf("unit %d: %w", u.N, err)
			}
		}
		for _, g := range in.Gaps {
			if _, err := results.Exec(); err != nil {
				results.Close()
				return fmt.Errorf("gap %s: %w", g.Code, err)
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
		if err := s.at("publish:written"); err != nil {
			return err
		}
		if _, err := finishLocked(ctx, tx, seal, false); err != nil {
			return err
		}
		return s.at("publish:sealed")
	})
	switch {
	case errors.Is(err, errNotPending):
		// A retry after a lost acknowledgement: the same bytes under this seal.
		published, lookupErr := s.cardBySeal(ctx, in.WorkspaceID, in.SealReceiptID)
		if lookupErr == nil && bytes.Equal(published.SHA256, digest[:]) {
			return published, nil
		}
		return Card{}, ErrAlreadyFinished
	case sqlState(err) == "GT009":
		return Card{}, fmt.Errorf("%w: %v", ErrProvenance, err)
	case err != nil:
		return Card{}, err
	case refused != nil:
		return Card{}, refused
	}
	return card, nil
}

// recheck holds share locks on the actor's grant, the brief receipt, the
// cited reads and their connections until commit, so neither a revocation
// nor a source change can land in between. It returns a refusal, or nil.
func (s *Store) recheck(ctx context.Context, tx pgx.Tx, in CardPublication, principal *string) (refusal, err error) {
	if in.PrincipalID != "" && (principal == nil || *principal != in.PrincipalID) {
		return ErrNotPermitted, nil
	}
	if principal != nil {
		var granted bool
		err := tx.QueryRow(ctx, `
			SELECT true FROM account_grants g
			  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
			 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
			   AND g.revoked_at IS NULL AND p.disabled_at IS NULL
			   FOR SHARE OF g, p`, in.WorkspaceID, *principal, in.AccountRef).Scan(&granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted, nil
		}
		if err != nil {
			return nil, err
		}
	}
	var parentFits bool
	err = tx.QueryRow(ctx, `
		SELECT account_ref IS NOT DISTINCT FROM $3 AND principal_id IS NOT DISTINCT FROM $4
		  FROM receipts WHERE id = $1 AND workspace_id = $2 AND parent_id IS NULL FOR SHARE`,
		in.ParentReceiptID, in.WorkspaceID, in.AccountRef, principal).Scan(&parentFits)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !parentFits {
		return ErrProvenance, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkProjection(in.Bytes, in.Units, in.Gaps); err != nil {
		return err, nil
	}
	// Each read, and whether a unit cites it.
	reads := map[string]bool{}
	for _, id := range in.ReadReceiptIDs {
		if _, seen := reads[id]; !seen {
			reads[id] = false
		}
	}
	for _, u := range in.Units {
		if u.ReceiptID != "" {
			reads[u.ReceiptID] = true
		} else if in.Kind == "go" || u.SourceRecordID != "" {
			return ErrProvenance, nil
		}
		if in.Kind == "go" && u.SourceRecordID == "" {
			return ErrProvenance, nil
		}
	}
	for id, cited := range reads {
		if !validUUID(id) {
			return ErrProvenance, nil
		}
		var status string
		var fits, current bool
		// gtm_read_of_card, before the card exists.
		err := tx.QueryRow(ctx, `
			SELECT r.status, coalesce(r.account_ref IS NOT DISTINCT FROM $3 AND (r.parent_id = $4
			         OR (r.parent_id IS NULL AND $4 = $5 AND r.brief_id = $6
			             AND r.principal_id IS NOT DISTINCT FROM s.principal_id AND r.token_id = s.token_id)), false),
			       c.generation = r.connection_generation
			  FROM receipts r
			  JOIN connections c ON c.id = r.connection_id
			  JOIN receipts s ON s.id = $5
			 WHERE r.id = $1 AND r.workspace_id = $2
			   FOR SHARE OF r, c`, id, in.WorkspaceID, in.AccountRef, in.ParentReceiptID, in.SealReceiptID,
			in.BriefID).Scan(&status, &fits, &current)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (!fits || status == "pending" || cited && status != "ok") {
			return ErrProvenance, nil
		}
		if err != nil {
			return nil, err
		}
		if !current {
			return ErrSourceChanged, nil
		}
	}
	// Each bound unit's record version is one its read bound, under its
	// reference: one round trip for them all.
	batch := &pgx.Batch{}
	var bound []Unit
	for _, u := range in.Units {
		if u.SourceRecordID == "" {
			continue
		}
		if !validUUID(u.SourceRecordID) {
			return ErrProvenance, nil
		}
		bound = append(bound, u)
		batch.Queue(`
			SELECT v.source || '/' || v.record_table || '/' || v.record_id
			  FROM receipt_records b JOIN source_records v ON v.id = b.source_record_id
			 WHERE b.workspace_id = $1 AND b.receipt_id = $2 AND b.source_record_id = $3`,
			in.WorkspaceID, u.ReceiptID, u.SourceRecordID)
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for _, u := range bound {
		var ref string
		err := results.QueryRow().Scan(&ref)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && ref != u.RecordRef {
			return ErrProvenance, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, results.Close()
}

// checkProjection requires the units and gaps to be exactly the card
// bytes' "units" and "gaps": unit n is element n-1, and its kind, record_ref,
// tool and shown columns are its body's.
func checkProjection(raw []byte, units []Unit, gaps []Gap) error {
	var doc struct {
		Units []json.RawMessage `json:"units"`
		Gaps  []json.RawMessage `json:"gaps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Units == nil || doc.Gaps == nil {
		return fmt.Errorf("%w: the bytes are not a JSON object with units and gaps", ErrProjection)
	}
	if len(doc.Units) != len(units) || len(doc.Gaps) != len(gaps) {
		return fmt.Errorf("%w: counts differ", ErrProjection)
	}
	for _, u := range units {
		if u.N < 1 || u.N > len(units) || !sameJSON(doc.Units[u.N-1], u.Body) {
			return fmt.Errorf("%w: unit %d", ErrProjection, u.N)
		}
		var fields struct {
			Kind, RecordRef, Tool, Shown *string
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(u.Body, &body) != nil {
			return fmt.Errorf("%w: unit %d", ErrProjection, u.N)
		}
		for key, target := range map[string]**string{"kind": &fields.Kind, "record_ref": &fields.RecordRef,
			"tool": &fields.Tool, "shown": &fields.Shown} {
			var value string
			if raw, ok := body[key]; ok && json.Unmarshal(raw, &value) == nil {
				*target = &value
			}
		}
		if deref(fields.Kind) != u.Kind || deref(fields.RecordRef) != u.RecordRef || deref(fields.Tool) != u.Tool ||
			(u.Kind == "sentence" && deref(fields.Shown) != u.Shown) {
			return fmt.Errorf("%w: unit %d's columns differ from its body", ErrProjection, u.N)
		}
	}
	for i, g := range gaps {
		want, _ := json.Marshal(map[string]any{"code": g.Code, "disposition": g.Disposition, "count": g.Count,
			"by_role": nullJSON(g.ByRole), "text": g.Text})
		if !sameJSON(doc.Gaps[i], want) {
			return fmt.Errorf("%w: gap %s", ErrProjection, g.Code)
		}
	}
	return nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

const cardColumns = `c.id, c.workspace_id, c.account_ref, c.brief_id, c.kind, c.schema_version,
	c.rules_version, c.card_bytes, c.card_sha256, c.parent_receipt_id, c.seal_receipt_id, c.published_at`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.AccountRef, &c.BriefID, &c.Kind, &c.SchemaVersion,
		&c.RulesVersion, &c.Bytes, &c.SHA256, &c.ParentReceiptID, &c.SealReceiptID, &c.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, ErrNotFound
	}
	return c, err
}

func (s *Store) cardBySeal(ctx context.Context, workspaceID, sealID string) (Card, error) {
	return scanCard(s.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards c
		WHERE c.workspace_id = $1 AND c.seal_receipt_id = $2`, workspaceID, sealID))
}

// Card reads one card with its bytes, for the owner.
func (s *Store) Card(ctx context.Context, workspaceID, cardID string) (Card, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return Card{}, ErrNotFound
	}
	return scanCard(s.pool.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.id = $2`, workspaceID, cardID))
}

const unitColumns = `u.card_id, u.n, u.kind, u.record_ref, u.tool, coalesce(u.shown, ''),
	coalesce(u.receipt_id::text, ''), coalesce(u.source_record_id::text, ''), u.body`

func scanUnit(row pgx.Row) (Unit, error) {
	var u Unit
	err := row.Scan(&u.CardID, &u.N, &u.Kind, &u.RecordRef, &u.Tool, &u.Shown, &u.ReceiptID,
		&u.SourceRecordID, &u.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Unit{}, ErrNotFound
	}
	return u, err
}

// Units lists a card's units in number order, for the owner.
func (s *Store) Units(ctx context.Context, workspaceID, cardID string) ([]Unit, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return nil, ErrNotFound
	}
	return units(ctx, s.pool, workspaceID, cardID)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func units(ctx context.Context, q querier, workspaceID, cardID string) ([]Unit, error) {
	rows, err := q.Query(ctx, `SELECT `+unitColumns+` FROM evidence_units u
		WHERE u.workspace_id = $1 AND u.card_id = $2 ORDER BY u.n`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Unit, error) { return scanUnit(row) })
}

func gaps(ctx context.Context, q querier, workspaceID, cardID string) ([]Gap, error) {
	rows, err := q.Query(ctx, `SELECT code, disposition, count, by_role, text FROM gaps
		WHERE workspace_id = $1 AND card_id = $2 ORDER BY ordinal`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Gap, error) {
		var g Gap
		err := row.Scan(&g.Code, &g.Disposition, &g.Count, &g.ByRole, &g.Text)
		return g, err
	})
}
