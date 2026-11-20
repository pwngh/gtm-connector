// Package authz decides what a signed-in principal may read. Every evidence
// read goes through Evidence, which checks a live account grant before it
// reads a card, a unit or a receipt, so an ungranted principal gets neither a
// unit nor its receipt. A denial is ErrNotPermitted, never an empty result.
package authz

import (
	"context"
	"errors"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

var (
	// ErrNotPermitted: the principal holds no live grant for what it asked.
	ErrNotPermitted = errors.New("not permitted")
	// ErrNotFound: nothing by that ID exists in the principal's workspace.
	ErrNotFound = errors.New("not found")
)

// Principal is who is asking: a principal of one workspace.
type Principal struct {
	WorkspaceID, ID string
}

// Store is the storage Evidence reads through.
type Store interface {
	HasAccountGrant(ctx context.Context, workspaceID, principalID, accountRef string) (bool, error)
	AccountExists(ctx context.Context, workspaceID, accountRef string) (bool, error)
	CardAccount(ctx context.Context, workspaceID, cardID string) (string, error)
	ReceiptAccount(ctx context.Context, workspaceID, receiptID string) (string, error)
	AccountCards(ctx context.Context, workspaceID, accountRef string) ([]postgres.Card, error)
	Card(ctx context.Context, workspaceID, cardID string) (postgres.Card, error)
	Units(ctx context.Context, workspaceID, cardID string) ([]postgres.Unit, error)
	Unit(ctx context.Context, workspaceID, cardID string, n int) (postgres.Unit, error)
	Gaps(ctx context.Context, workspaceID, cardID string) ([]postgres.Gap, error)
	Receipt(ctx context.Context, workspaceID, receiptID string) (postgres.Receipt, error)
}

// Evidence is the read side of cards, units and receipts, with grants enforced.
type Evidence struct {
	Store Store
}

// requireAccount allows a read on account only with a live grant.
func (e Evidence) requireAccount(ctx context.Context, p Principal, account string) error {
	if account == "" {
		// A receipt about no account is the owner's to read, not a browser's.
		return ErrNotPermitted
	}
	granted, err := e.Store.HasAccountGrant(ctx, p.WorkspaceID, p.ID, account)
	if err != nil {
		return err
	}
	if !granted {
		return ErrNotPermitted
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// AccountCards lists an account's cards.
func (e Evidence) AccountCards(ctx context.Context, p Principal, account string) ([]postgres.Card, error) {
	exists, err := e.Store.AccountExists(ctx, p.WorkspaceID, account)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	if err := e.requireAccount(ctx, p, account); err != nil {
		return nil, err
	}
	return e.Store.AccountCards(ctx, p.WorkspaceID, account)
}

// CardView is a card with its units and gaps.
type CardView struct {
	Card  postgres.Card
	Units []postgres.Unit
	Gaps  []postgres.Gap
}

// Card reads a card, its units and its gaps.
func (e Evidence) Card(ctx context.Context, p Principal, cardID string) (CardView, error) {
	account, err := e.Store.CardAccount(ctx, p.WorkspaceID, cardID)
	if err != nil {
		return CardView{}, notFound(err)
	}
	if err := e.requireAccount(ctx, p, account); err != nil {
		return CardView{}, err
	}
	card, err := e.Store.Card(ctx, p.WorkspaceID, cardID)
	if err != nil {
		return CardView{}, notFound(err)
	}
	units, err := e.Store.Units(ctx, p.WorkspaceID, cardID)
	if err != nil {
		return CardView{}, err
	}
	gaps, err := e.Store.Gaps(ctx, p.WorkspaceID, cardID)
	if err != nil {
		return CardView{}, err
	}
	return CardView{card, units, gaps}, nil
}

// UnitView is one unit with the receipt it was read under, when bound.
type UnitView struct {
	Unit    postgres.Unit
	Receipt *postgres.Receipt
}

// Unit reads one unit of a card and its read receipt.
func (e Evidence) Unit(ctx context.Context, p Principal, cardID string, n int) (UnitView, error) {
	account, err := e.Store.CardAccount(ctx, p.WorkspaceID, cardID)
	if err != nil {
		return UnitView{}, notFound(err)
	}
	if err := e.requireAccount(ctx, p, account); err != nil {
		return UnitView{}, err
	}
	unit, err := e.Store.Unit(ctx, p.WorkspaceID, cardID, n)
	if err != nil {
		return UnitView{}, notFound(err)
	}
	view := UnitView{Unit: unit}
	if unit.ReceiptID != "" {
		receipt, err := e.Receipt(ctx, p, unit.ReceiptID)
		if err != nil {
			return UnitView{}, err
		}
		view.Receipt = &receipt
	}
	return view, nil
}

// Receipt reads one receipt; the principal needs a grant on its account.
func (e Evidence) Receipt(ctx context.Context, p Principal, receiptID string) (postgres.Receipt, error) {
	account, err := e.Store.ReceiptAccount(ctx, p.WorkspaceID, receiptID)
	if err != nil {
		return postgres.Receipt{}, notFound(err)
	}
	if err := e.requireAccount(ctx, p, account); err != nil {
		return postgres.Receipt{}, err
	}
	receipt, err := e.Store.Receipt(ctx, p.WorkspaceID, receiptID)
	return receipt, notFound(err)
}
