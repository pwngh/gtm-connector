// Package authz decides what a signed-in principal may read. Every evidence
// read goes through Evidence. Storage checks a live account grant inside the
// read's own transaction, holding it until the read is done, so an
// ungranted principal gets neither a unit nor its receipt and a revocation
// cannot land in between. Unknown and ungranted things both answer
// ErrNotPermitted, never an empty result.
package authz

import (
	"context"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// ErrNotPermitted is the one refusal: the principal holds no live grant for
// what it asked, or nothing by that ID is visible to it.
var ErrNotPermitted = postgres.ErrNotPermitted

// Principal is who is asking: a principal of one workspace.
type Principal struct {
	WorkspaceID, ID string
}

func (p Principal) viewer() postgres.Viewer {
	return postgres.Viewer{WorkspaceID: p.WorkspaceID, PrincipalID: p.ID}
}

// Store is the storage Evidence reads through.
type Store interface {
	ViewAccounts(ctx context.Context, v postgres.Viewer) ([]postgres.AccountView, error)
	ViewAccountCards(ctx context.Context, v postgres.Viewer, accountRef string) ([]postgres.Card, error)
	ViewCard(ctx context.Context, v postgres.Viewer, cardID string) (postgres.CardView, error)
	ViewUnit(ctx context.Context, v postgres.Viewer, cardID string, n int) (postgres.Unit, *postgres.Receipt, string, error)
	ViewReceipt(ctx context.Context, v postgres.Viewer, receiptID string) (postgres.Receipt, error)
	ViewDeals(ctx context.Context, v postgres.Viewer) ([]postgres.Deal, error)
	ViewDeal(ctx context.Context, v postgres.Viewer, ref string) (postgres.Deal, error)
}

// Evidence is the read side of cards, units and receipts, with grants enforced.
type Evidence struct {
	Store Store
}

// Accounts lists the accounts the principal holds a live grant on.
func (e Evidence) Accounts(ctx context.Context, p Principal) ([]postgres.AccountView, error) {
	return e.Store.ViewAccounts(ctx, p.viewer())
}

// AccountCards lists an account's cards.
func (e Evidence) AccountCards(ctx context.Context, p Principal, account string) ([]postgres.Card, error) {
	return e.Store.ViewAccountCards(ctx, p.viewer(), account)
}

// CardView is one card as every rendering of it takes it: its head; the day
// it is as of and the source each system was, as its bytes say; its units
// and gaps; its lines under ATTENTION and RECORD VALUES as data; and its
// sealed text whole. A rendering reads values from Lines and never from Text.
// Lines is nil for a card published before lines were projected.
type CardView struct {
	Card    postgres.Card
	AsOf    string
	Sources map[string]string
	Units   []postgres.Unit
	Gaps    []postgres.Gap
	Lines   []evidence.Line
	Text    string
}

// Card reads a card, its units, its gaps and its lines. A card one of whose
// units rests on a read of another account is refused whole: a grant on this
// account does not open the other's records.
func (e Evidence) Card(ctx context.Context, p Principal, cardID string) (CardView, error) {
	view, err := e.Store.ViewCard(ctx, p.viewer(), cardID)
	if err != nil {
		return CardView{}, err
	}
	for _, account := range view.ReceiptAccounts {
		if account != view.Card.AccountRef {
			return CardView{}, ErrNotPermitted
		}
	}
	return CardView{Card: view.Card, AsOf: view.AsOf, Sources: view.Sources, Units: view.Units, Gaps: view.Gaps,
		Lines: view.Lines, Text: view.Text}, nil
}

// UnitView is one unit with the receipt it was read under, when bound.
type UnitView struct {
	Unit    postgres.Unit
	Receipt *postgres.Receipt
}

// Unit reads one unit of a card and its read receipt, which must be of the
// card's account.
func (e Evidence) Unit(ctx context.Context, p Principal, cardID string, n int) (UnitView, error) {
	unit, receipt, account, err := e.Store.ViewUnit(ctx, p.viewer(), cardID, n)
	if err != nil {
		return UnitView{}, err
	}
	if receipt != nil && receipt.AccountRef != account {
		return UnitView{}, ErrNotPermitted
	}
	return UnitView{unit, receipt}, nil
}

// Receipt reads one receipt; the principal needs a grant on its account.
func (e Evidence) Receipt(ctx context.Context, p Principal, receiptID string) (postgres.Receipt, error) {
	return e.Store.ViewReceipt(ctx, p.viewer(), receiptID)
}

// Deals lists the deals of the accounts the principal holds a live grant on.
func (e Evidence) Deals(ctx context.Context, p Principal) ([]postgres.Deal, error) {
	return e.Store.ViewDeals(ctx, p.viewer())
}

// Deal reads one deal; the principal needs a live grant on its account.
func (e Evidence) Deal(ctx context.Context, p Principal, ref string) (postgres.Deal, error) {
	return e.Store.ViewDeal(ctx, p.viewer(), ref)
}
