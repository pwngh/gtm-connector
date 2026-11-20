package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotPermitted means the principal holds no live grant on the account.
var ErrNotPermitted = errors.New("not permitted")

// Card is a stored card: its bytes, their digest, and how it was sealed.
type Card struct {
	ID, WorkspaceID, AccountRef, BriefID string
	Kind                                 string // cli, prepared or go
	SchemaVersion                        int
	RulesVersion                         *string
	Bytes                                []byte
	SHA256                               []byte
	SealReceiptID                        string
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
	// PrincipalID's grant on the account is rechecked inside the transaction;
	// empty when the local owner publishes.
	PrincipalID string
	// The seal receipt, started (pending) before the card existed.
	SealReceiptID, SealLeaseToken string
	Kind                          string
	SchemaVersion                 int
	RulesVersion                  *string
	Bytes                         []byte
	Units                         []Unit
	Gaps                          []Gap
	// ReadReceiptIDs are the child reads the card rests on. Each must have
	// finished ok under its connection's current generation.
	ReadReceiptIDs []string
	ElapsedMS      float64
}

// PublishCard is step 3, one transaction: it rechecks the principal's grant
// and the source generations, writes the card bytes and digest, the unit and
// gap projections, and finishes the seal receipt naming the digest. If a
// recheck fails it writes no card, finishes the seal as an error (403
// not_permitted or 409 source_changed) and returns ErrNotPermitted or
// ErrSourceChanged.
func (s *Store) PublishCard(ctx context.Context, in CardPublication) (Card, error) {
	if !validUUID(in.SealReceiptID) || !validUUID(in.SealLeaseToken) {
		return Card{}, ErrNotFound
	}
	digest := sha256.Sum256(in.Bytes)
	seal := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken, Status: "ok",
		HTTPStatus: 200, ElapsedMS: in.ElapsedMS,
		Rows: []string{"briefs/" + in.BriefID + "/" + hex.EncodeToString(digest[:])}}
	card := Card{WorkspaceID: in.WorkspaceID, AccountRef: in.AccountRef, BriefID: in.BriefID, Kind: in.Kind,
		SchemaVersion: in.SchemaVersion, RulesVersion: in.RulesVersion, Bytes: in.Bytes, SHA256: digest[:],
		SealReceiptID: in.SealReceiptID}
	var refused error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var status, account, brief string
		err := tx.QueryRow(ctx, `
			SELECT status, coalesce(account_ref, ''), coalesce(brief_id, '') FROM receipts
			 WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, in.SealReceiptID, in.WorkspaceID).
			Scan(&status, &account, &brief)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "pending" {
			return ErrAlreadyFinished
		}
		if account != in.AccountRef || brief != in.BriefID {
			return fmt.Errorf("the seal receipt names another account or brief")
		}
		refused, err = recheck(ctx, tx, in)
		if err != nil {
			return err
		}
		if refused != nil {
			failure := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken,
				Status: "error", HTTPStatus: 409, ElapsedMS: in.ElapsedMS, Error: "source_changed"}
			if errors.Is(refused, ErrNotPermitted) {
				failure.HTTPStatus, failure.Error = 403, "not_permitted"
			}
			_, err := finishLocked(ctx, tx, failure)
			return err
		}
		if err := s.at("publish:checked"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
				card_bytes, card_sha256, seal_receipt_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, sha256($7), $8)
			RETURNING id, published_at`,
			in.WorkspaceID, in.AccountRef, in.BriefID, in.Kind, in.SchemaVersion, in.RulesVersion,
			in.Bytes, in.SealReceiptID).Scan(&card.ID, &card.PublishedAt); err != nil {
			return err
		}
		for _, u := range in.Units {
			if _, err := tx.Exec(ctx, `
				INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, shown,
					receipt_id, source_record_id, body)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				in.WorkspaceID, card.ID, u.N, u.Kind, u.RecordRef, u.Tool, nullable(u.Shown),
				nullable(u.ReceiptID), nullable(u.SourceRecordID), u.Body); err != nil {
				return fmt.Errorf("unit %d: %w", u.N, err)
			}
		}
		for i, g := range in.Gaps {
			if _, err := tx.Exec(ctx, `
				INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, count, by_role, text)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				in.WorkspaceID, card.ID, i, g.Code, g.Disposition, g.Count, nullJSON(g.ByRole),
				g.Text); err != nil {
				return fmt.Errorf("gap %s: %w", g.Code, err)
			}
		}
		if err := s.at("publish:written"); err != nil {
			return err
		}
		if _, err := finishLocked(ctx, tx, seal); err != nil {
			return err
		}
		return s.at("publish:sealed")
	})
	if err != nil {
		return Card{}, err
	}
	if refused != nil {
		return Card{}, refused
	}
	return card, nil
}

// recheck holds share locks on the grant and the connections until commit,
// so neither a revocation nor a source change can land in between. refusal
// is ErrNotPermitted or ErrSourceChanged when publishing must not go ahead.
func recheck(ctx context.Context, tx pgx.Tx, in CardPublication) (refusal, err error) {
	if in.PrincipalID != "" {
		var granted bool
		err := tx.QueryRow(ctx, `
			SELECT true FROM account_grants
			 WHERE workspace_id = $1 AND principal_id = $2 AND account_ref = $3 AND revoked_at IS NULL
			   FOR SHARE`, in.WorkspaceID, in.PrincipalID, in.AccountRef).Scan(&granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted, nil
		}
		if err != nil {
			return nil, err
		}
	}
	for _, id := range in.ReadReceiptIDs {
		if !validUUID(id) {
			return nil, ErrNotFound
		}
		var status string
		var connection *string
		var generation *int64
		err := tx.QueryRow(ctx, `
			SELECT status, connection_id, connection_generation FROM receipts
			 WHERE id = $1 AND workspace_id = $2`, id, in.WorkspaceID).Scan(&status, &connection, &generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if status != "ok" {
			return ErrSourceChanged, nil
		}
		if connection == nil {
			continue
		}
		var current int64
		if err := tx.QueryRow(ctx, `SELECT generation FROM connections WHERE id = $1 FOR SHARE`,
			*connection).Scan(&current); err != nil {
			return nil, err
		}
		if current != *generation {
			return ErrSourceChanged, nil
		}
	}
	return nil, nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

const cardColumns = `c.id, c.workspace_id, c.account_ref, c.brief_id, c.kind, c.schema_version,
	c.rules_version, c.card_bytes, c.card_sha256, c.seal_receipt_id, c.published_at`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.AccountRef, &c.BriefID, &c.Kind, &c.SchemaVersion,
		&c.RulesVersion, &c.Bytes, &c.SHA256, &c.SealReceiptID, &c.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, ErrNotFound
	}
	return c, err
}

// CardAccount gives the account a card belongs to, without its contents.
func (s *Store) CardAccount(ctx context.Context, workspaceID, cardID string) (string, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return "", ErrNotFound
	}
	var account string
	err := s.pool.QueryRow(ctx, `SELECT account_ref FROM cards WHERE workspace_id = $1 AND id = $2`,
		workspaceID, cardID).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return account, err
}

// ReceiptAccount gives the account a receipt is about ("" for none).
func (s *Store) ReceiptAccount(ctx context.Context, workspaceID, receiptID string) (string, error) {
	if !validUUID(workspaceID) || !validUUID(receiptID) {
		return "", ErrNotFound
	}
	var account *string
	err := s.pool.QueryRow(ctx, `SELECT account_ref FROM receipts WHERE workspace_id = $1 AND id = $2`,
		workspaceID, receiptID).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return deref(account), err
}

// AccountExists reports whether the workspace has this account.
func (s *Store) AccountExists(ctx context.Context, workspaceID, accountRef string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE workspace_id = $1 AND ref = $2)`,
		workspaceID, accountRef).Scan(&exists)
	return exists, err
}

// Card reads one card with its bytes.
func (s *Store) Card(ctx context.Context, workspaceID, cardID string) (Card, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return Card{}, ErrNotFound
	}
	return scanCard(s.pool.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.id = $2`, workspaceID, cardID))
}

// AccountCards lists an account's cards, newest first.
func (s *Store) AccountCards(ctx context.Context, workspaceID, accountRef string) ([]Card, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cardColumns+` FROM cards c
		WHERE c.workspace_id = $1 AND c.account_ref = $2 ORDER BY c.published_at DESC, c.id`,
		workspaceID, accountRef)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Card, error) { return scanCard(row) })
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

// Units lists a card's units in number order.
func (s *Store) Units(ctx context.Context, workspaceID, cardID string) ([]Unit, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+unitColumns+` FROM evidence_units u
		WHERE u.workspace_id = $1 AND u.card_id = $2 ORDER BY u.n`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Unit, error) { return scanUnit(row) })
}

// Unit reads one unit of a card.
func (s *Store) Unit(ctx context.Context, workspaceID, cardID string, n int) (Unit, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return Unit{}, ErrNotFound
	}
	return scanUnit(s.pool.QueryRow(ctx, `SELECT `+unitColumns+` FROM evidence_units u
		WHERE u.workspace_id = $1 AND u.card_id = $2 AND u.n = $3`, workspaceID, cardID, n))
}

// Gaps lists a card's gaps in card order.
func (s *Store) Gaps(ctx context.Context, workspaceID, cardID string) ([]Gap, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT code, disposition, count, by_role, text FROM gaps
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
