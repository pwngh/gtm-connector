package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Viewer is a principal reading evidence.
type Viewer struct {
	WorkspaceID, PrincipalID string
}

// CardView is a card with its units, its gaps, and the account of each
// unit's read receipt (by unit number).
type CardView struct {
	Card            Card
	Units           []Unit
	Gaps            []Gap
	ReceiptAccounts map[int]string
}

// The View methods read evidence for a principal. Each runs in one
// transaction that first finds the account and takes a share lock on the
// principal's live grant on it (and on the principal), so a revocation or a
// disabling waits until the read is done. Anything unknown, of another
// workspace, or ungranted gives the same ErrNotPermitted.

func (s *Store) view(ctx context.Context, v Viewer, account func(pgx.Tx) (string, error), read func(pgx.Tx) error) error {
	if !validUUID(v.WorkspaceID) || !validUUID(v.PrincipalID) {
		return ErrNotPermitted
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		ref, err := account(tx)
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) || err == nil && ref == "" {
			return ErrNotPermitted
		}
		if err != nil {
			return err
		}
		var granted bool
		err = tx.QueryRow(ctx, `
			SELECT true FROM account_grants g
			  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
			 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
			   AND g.revoked_at IS NULL AND p.disabled_at IS NULL
			   FOR SHARE OF g, p`, v.WorkspaceID, v.PrincipalID, ref).Scan(&granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted
		}
		if err != nil {
			return err
		}
		if err := s.at("view:granted"); err != nil {
			return err
		}
		return read(tx)
	})
}

func cardAccount(ctx context.Context, v Viewer, cardID string) func(pgx.Tx) (string, error) {
	return func(tx pgx.Tx) (string, error) {
		if !validUUID(cardID) {
			return "", ErrNotFound
		}
		var account string
		err := tx.QueryRow(ctx, `SELECT account_ref FROM cards WHERE workspace_id = $1 AND id = $2`,
			v.WorkspaceID, cardID).Scan(&account)
		return account, err
	}
}

// ViewCard reads a card, its units and gaps.
func (s *Store) ViewCard(ctx context.Context, v Viewer, cardID string) (CardView, error) {
	var view CardView
	err := s.view(ctx, v, cardAccount(ctx, v, cardID), func(tx pgx.Tx) error {
		var err error
		if view.Card, err = scanCard(tx.QueryRow(ctx,
			`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.id = $2`,
			v.WorkspaceID, cardID)); err != nil {
			return err
		}
		if view.Units, err = units(ctx, tx, v.WorkspaceID, cardID); err != nil {
			return err
		}
		if view.Gaps, err = gaps(ctx, tx, v.WorkspaceID, cardID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT u.n, coalesce(r.account_ref, '') FROM evidence_units u
			  JOIN receipts r ON r.workspace_id = u.workspace_id AND r.id = u.receipt_id
			 WHERE u.workspace_id = $1 AND u.card_id = $2`, v.WorkspaceID, cardID)
		if err != nil {
			return err
		}
		view.ReceiptAccounts = map[int]string{}
		var n int
		var account string
		_, err = pgx.ForEachRow(rows, []any{&n, &account}, func() error {
			view.ReceiptAccounts[n] = account
			return nil
		})
		return err
	})
	return view, err
}

// ViewUnit reads one unit of a card and, when bound, its read receipt.
func (s *Store) ViewUnit(ctx context.Context, v Viewer, cardID string, n int) (Unit, *Receipt, string, error) {
	var unit Unit
	var receipt *Receipt
	var cardAccountRef string
	account := cardAccount(ctx, v, cardID)
	err := s.view(ctx, v, func(tx pgx.Tx) (string, error) {
		ref, err := account(tx)
		cardAccountRef = ref
		return ref, err
	}, func(tx pgx.Tx) error {
		var err error
		unit, err = scanUnit(tx.QueryRow(ctx, `SELECT `+unitColumns+` FROM evidence_units u
			WHERE u.workspace_id = $1 AND u.card_id = $2 AND u.n = $3`, v.WorkspaceID, cardID, n))
		if errors.Is(err, ErrNotFound) {
			return ErrNotPermitted
		}
		if err != nil || unit.ReceiptID == "" {
			return err
		}
		r, err := scanReceipt(tx.QueryRow(ctx, `SELECT `+receiptColumns+` FROM receipts r
			WHERE r.workspace_id = $1 AND r.id = $2`, v.WorkspaceID, unit.ReceiptID))
		r.LeaseToken = ""
		receipt = &r
		return err
	})
	return unit, receipt, cardAccountRef, err
}

// ViewReceipt reads one receipt; it needs a grant on the receipt's account.
// A receipt about no account is the owner's alone.
func (s *Store) ViewReceipt(ctx context.Context, v Viewer, receiptID string) (Receipt, error) {
	var receipt Receipt
	err := s.view(ctx, v, func(tx pgx.Tx) (string, error) {
		if !validUUID(receiptID) {
			return "", ErrNotFound
		}
		var account *string
		err := tx.QueryRow(ctx, `SELECT account_ref FROM receipts WHERE workspace_id = $1 AND id = $2`,
			v.WorkspaceID, receiptID).Scan(&account)
		return deref(account), err
	}, func(tx pgx.Tx) error {
		var err error
		receipt, err = scanReceipt(tx.QueryRow(ctx, `SELECT `+receiptColumns+` FROM receipts r
			WHERE r.workspace_id = $1 AND r.id = $2`, v.WorkspaceID, receiptID))
		receipt.LeaseToken = ""
		return err
	})
	return receipt, err
}

// ViewAccountCards lists an account's cards, newest first.
func (s *Store) ViewAccountCards(ctx context.Context, v Viewer, accountRef string) ([]Card, error) {
	var cards []Card
	err := s.view(ctx, v, func(tx pgx.Tx) (string, error) {
		var account string
		err := tx.QueryRow(ctx, `SELECT ref FROM accounts WHERE workspace_id = $1 AND ref = $2`,
			v.WorkspaceID, accountRef).Scan(&account)
		return account, err
	}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+cardColumns+` FROM cards c
			WHERE c.workspace_id = $1 AND c.account_ref = $2 ORDER BY c.published_at DESC, c.id`,
			v.WorkspaceID, accountRef)
		if err != nil {
			return err
		}
		cards, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Card, error) { return scanCard(row) })
		return err
	})
	return cards, err
}

// AccountView is an account a principal holds a live grant on.
type AccountView struct {
	Ref, DisplayName string
}

// ViewAccounts lists the accounts the principal holds a live grant on, by
// reference. A principal that is unknown, of another workspace, or disabled
// gets ErrNotPermitted; one with no grant gets none.
func (s *Store) ViewAccounts(ctx context.Context, v Viewer) ([]AccountView, error) {
	if !validUUID(v.WorkspaceID) || !validUUID(v.PrincipalID) {
		return nil, ErrNotPermitted
	}
	var accounts []AccountView
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var live bool
		err := tx.QueryRow(ctx, `SELECT true FROM principals WHERE workspace_id = $1 AND id = $2
			AND disabled_at IS NULL FOR SHARE`, v.WorkspaceID, v.PrincipalID).Scan(&live)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT a.ref, a.display_name FROM account_grants g
			  JOIN accounts a ON a.workspace_id = g.workspace_id AND a.ref = g.account_ref
			 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.revoked_at IS NULL
			 ORDER BY a.ref`, v.WorkspaceID, v.PrincipalID)
		if err != nil {
			return err
		}
		accounts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountView, error) {
			var a AccountView
			err := row.Scan(&a.Ref, &a.DisplayName)
			return a, err
		})
		return err
	})
	return accounts, err
}
