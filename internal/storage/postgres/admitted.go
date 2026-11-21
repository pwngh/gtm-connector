package postgres

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
)

// AdmittedCard is a card the Python package made, as a workspace keeps it: its bytes as the folder held
// them, and the ID of the Python receipt that sealed them.
type AdmittedCard struct {
	BriefID   string
	Kind      string // "cli" (saved by brief or demo) or "prepared" (saved by prepare_renewal_brief)
	AccountID string
	Bytes     []byte
	SealID    string
}

// AdmittedFolder is what admitting one folder added: the cards and receipts that were not in the workspace.
type AdmittedFolder struct {
	Cards, Receipts []string
}

// ErrAdmittedDiffers refuses a folder whose card or receipt has the ID of one the workspace already holds
// with other bytes, or the brief ID of a card made here, and a card whose seal does not name its bytes.
var ErrAdmittedDiffers = errors.New("the workspace holds another card or receipt with an ID this folder uses, " +
	"or a card does not rest on a seal of its bytes")

// Admit writes verified Python cards and the receipt rows they rest on into a workspace, in one
// transaction. A card or receipt the workspace already holds with the same bytes is left as it is, so
// admitting a folder again adds nothing. rows holds each receipt's row as the audit log's export held it,
// by audit ID.
func (s *Store) Admit(ctx context.Context, workspaceID string, cards []AdmittedCard, rows map[string][]byte) (AdmittedFolder, error) {
	var added AdmittedFolder
	if !validUUID(workspaceID) {
		return added, ErrNotFound
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		added = AdmittedFolder{}
		for _, id := range slices.Sorted(maps.Keys(rows)) {
			row := rows[id]
			if !validUUID(id) {
				return ErrAdmittedDiffers
			}
			var made bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM receipts WHERE workspace_id = $1 AND id = $2)`,
				workspaceID, id).Scan(&made); err != nil {
				return err
			}
			if made {
				return ErrAdmittedDiffers
			}
			tag, err := tx.Exec(ctx, `INSERT INTO admitted_receipts (workspace_id, audit_id, row_text)
				VALUES ($1, $2, $3) ON CONFLICT (workspace_id, audit_id) DO NOTHING`, workspaceID, id, string(row))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				added.Receipts = append(added.Receipts, id)
				continue
			}
			var held string
			if err := tx.QueryRow(ctx, `SELECT row_text FROM admitted_receipts WHERE workspace_id = $1 AND audit_id = $2`,
				workspaceID, id).Scan(&held); err != nil {
				return err
			}
			if held != string(row) {
				return ErrAdmittedDiffers
			}
		}
		for _, c := range cards {
			tag, err := tx.Exec(ctx, `INSERT INTO admitted_cards
				(workspace_id, brief_id, kind, account_ref, card_bytes, card_sha256, seal_audit_id)
				VALUES ($1, $2, $3, $4, $5, sha256($5), $6) ON CONFLICT (workspace_id, brief_id) DO NOTHING`,
				workspaceID, c.BriefID, c.Kind, c.AccountID, c.Bytes, c.SealID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				added.Cards = append(added.Cards, c.BriefID)
				continue
			}
			held, err := s.admittedCard(ctx, tx, workspaceID, c.BriefID)
			if err != nil {
				return err
			}
			if !bytes.Equal(held.Bytes, c.Bytes) || held.SealID != c.SealID || held.Kind != c.Kind {
				return ErrAdmittedDiffers
			}
		}
		return nil
	})
	if code := sqlState(err); code == "GT009" {
		return AdmittedFolder{}, ErrAdmittedDiffers
	}
	if err != nil {
		return AdmittedFolder{}, err
	}
	return added, nil
}

// AdmittedCardByBrief finds an admitted card of a workspace by its brief ID.
func (s *Store) AdmittedCardByBrief(ctx context.Context, workspaceID, briefID string) (AdmittedCard, error) {
	if !validUUID(workspaceID) {
		return AdmittedCard{}, ErrNotFound
	}
	return s.admittedCard(ctx, s.pool, workspaceID, briefID)
}

func (s *Store) admittedCard(ctx context.Context, q querier, workspaceID, briefID string) (AdmittedCard, error) {
	c := AdmittedCard{BriefID: briefID}
	err := q.QueryRow(ctx, `SELECT kind, account_ref, card_bytes, seal_audit_id::text FROM admitted_cards
		WHERE workspace_id = $1 AND brief_id = $2`, workspaceID, briefID).Scan(&c.Kind, &c.AccountID, &c.Bytes, &c.SealID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdmittedCard{}, ErrNotFound
	}
	return c, err
}

// AdmittedReceipts gives the rows of these admitted receipts of a workspace, by audit ID, as the audit
// log's export held them. An ID the workspace does not hold is left out.
func (s *Store) AdmittedReceipts(ctx context.Context, workspaceID string, auditIDs []string) (map[string][]byte, error) {
	found := map[string][]byte{}
	if !validUUID(workspaceID) {
		return found, nil
	}
	var valid []string
	for _, id := range auditIDs {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT audit_id::text, row_text FROM admitted_receipts
		WHERE workspace_id = $1 AND audit_id = ANY ($2::uuid[])`, workspaceID, valid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, row string
		if err := rows.Scan(&id, &row); err != nil {
			return nil, err
		}
		found[id] = []byte(row)
	}
	return found, rows.Err()
}
