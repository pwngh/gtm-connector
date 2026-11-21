package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
)

// AdmittedCard is a card the Python package made, as a workspace keeps it: its bytes as the folder held
// them, the ID of the Python receipt that sealed them, and the operator's word it was admitted on.
type AdmittedCard struct {
	BriefID   string
	Kind      string // "cli" (saved by brief or demo) or "prepared" (saved by prepare_renewal_brief)
	AccountID string
	Bytes     []byte
	SealID    string
	Word      OperatorWord
}

// OperatorWord is what the operator vouched for when admitting a folder, since nothing offline can check
// it: the CRM identity its cards were read from, and the SHA-256 of the audit log export it came with.
type OperatorWord struct {
	FromOrg, ExportSHA256 string
}

// AdmittedFolder is what admitting one folder added: the cards and receipts that were not in the workspace.
type AdmittedFolder struct {
	Cards, Receipts []string
}

// ErrAdmittedDiffers refuses a folder whose card or receipt has the ID of one the workspace already holds
// with other bytes, or the brief ID of a card made here, and a card the database's rules refuse.
var ErrAdmittedDiffers = errors.New("the workspace holds another card or receipt with an ID this folder uses, " +
	"or a card does not rest on its seal and reads")

// Admit writes verified Python cards and the receipt rows they rest on into a workspace, on the operator's
// word, in one transaction. A card or receipt the workspace already holds with the same bytes is left as
// it is, so admitting a folder again adds nothing. rows holds each receipt's row as the audit log's export
// held it, by audit ID.
func (s *Store) Admit(ctx context.Context, workspaceID string, word OperatorWord, cards []AdmittedCard,
	rows map[string][]byte) (AdmittedFolder, error) {
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
			tag, err := tx.Exec(ctx, `INSERT INTO admitted_receipts (workspace_id, audit_id, row_text, from_org)
				VALUES ($1, $2, $3, $4) ON CONFLICT (workspace_id, audit_id) DO NOTHING`,
				workspaceID, id, string(row), word.FromOrg)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				added.Receipts = append(added.Receipts, id)
				continue
			}
			var held, from string
			if err := tx.QueryRow(ctx, `SELECT row_text, from_org FROM admitted_receipts
				WHERE workspace_id = $1 AND audit_id = $2`, workspaceID, id).Scan(&held, &from); err != nil {
				return err
			}
			if held != string(row) || from != word.FromOrg {
				return ErrAdmittedDiffers
			}
		}
		for _, c := range cards {
			tag, err := tx.Exec(ctx, `INSERT INTO admitted_cards (workspace_id, brief_id, kind, account_ref,
				card_bytes, card_sha256, seal_audit_id, from_org, export_sha256)
				VALUES ($1, $2, $3, $4, $5, sha256($5), $6, $7, $8) ON CONFLICT (workspace_id, brief_id) DO NOTHING`,
				workspaceID, c.BriefID, c.Kind, c.AccountID, c.Bytes, c.SealID, word.FromOrg, word.ExportSHA256)
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
			if !bytes.Equal(held.Bytes, c.Bytes) || held.SealID != c.SealID || held.Kind != c.Kind ||
				held.Word.FromOrg != word.FromOrg {
				return ErrAdmittedDiffers
			}
		}
		return nil
	})
	if code := sqlState(err); code == "GT009" || code == "23514" {
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
	err := q.QueryRow(ctx, `SELECT kind, account_ref, card_bytes, seal_audit_id::text, from_org, export_sha256
		FROM admitted_cards WHERE workspace_id = $1 AND brief_id = $2`, workspaceID, briefID).
		Scan(&c.Kind, &c.AccountID, &c.Bytes, &c.SealID, &c.Word.FromOrg, &c.Word.ExportSHA256)
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

// AdmittedReceiptFrom is the CRM identity an admitted receipt was admitted on the operator's word for.
func (s *Store) AdmittedReceiptFrom(ctx context.Context, workspaceID, auditID string) (string, error) {
	if !validUUID(workspaceID) || !validUUID(auditID) {
		return "", ErrNotFound
	}
	var from string
	err := s.pool.QueryRow(ctx, `SELECT from_org FROM admitted_receipts WHERE workspace_id = $1 AND audit_id = $2`,
		workspaceID, auditID).Scan(&from)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return from, err
}

// HoldsImportedTranscript is whether a workspace holds an imported transcript for an account whose stored
// bytes hash to contentSHA256.
func (s *Store) HoldsImportedTranscript(ctx context.Context, workspaceID, accountRef, contentSHA256 string) (bool, error) {
	if !validUUID(workspaceID) {
		return false, nil
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT raw FROM call_import_contents
		WHERE workspace_id = $1 AND account_ref = $2 AND content_sha256 = $3`, workspaceID, accountRef, contentSHA256).
		Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == contentSHA256, nil
}
