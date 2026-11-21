package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// A deal is one renewal of one account, under a readable ID: a word of the account's name and the year it
// renews, such as thornfield-2027. Its bindings are records two CRM reads returned, under receipts the deal
// names, and the database refuses any other (0012_deals.sql). A card belongs to the deal it was published
// for.

// Deal is one deal of a workspace.
type Deal struct {
	WorkspaceID, Ref, AccountRef string
	// AccountName is the account's name as the CRM gave it: source text, neutralized before it is shown.
	AccountName string
	// OpportunityRef and ContractRef are the record IDs the deal binds, or "".
	OpportunityRef, ContractRef string
	// RenewsOn is the day the deal renews, YYYY-MM-DD, or "" when the CRM gave none.
	RenewsOn string
	State    string // open or closed
	// RegisteredBy is the principal that registered the deal, or "" for the owner.
	RegisteredBy string
	// AccountReadID and OpportunitiesReadID are the receipts of the two reads the bindings came from.
	AccountReadID, OpportunitiesReadID string
	RegisteredAt                       time.Time
	ClosedAt                           *time.Time
	// LatestCard is the newest card published for the deal, or nil.
	LatestCard *DealCard
}

// DealCard names one card of a deal.
type DealCard struct {
	ID, BriefID string
	PublishedAt time.Time
}

// DealRegistration is what RegisterDeal saves.
type DealRegistration struct {
	WorkspaceID string
	// PrincipalID is the principal that registers the deal, who must hold a live grant on the account, or ""
	// for the owner.
	PrincipalID string
	AccountRef  string
	// Word and Year make the deal's ID, word-year. A clash takes the lowest free number after it.
	Word string
	Year int
	// OpportunityRef, ContractRef and RenewsOn are as Deal has them.
	OpportunityRef, ContractRef, RenewsOn string
	AccountReadID, OpportunitiesReadID    string
}

var (
	// ErrDealOpen refuses a second open deal for one account.
	ErrDealOpen = errors.New("the account already has an open deal")
	// ErrDealClosed refuses a card for a deal that is no longer open.
	ErrDealClosed = errors.New("the deal is not open")
	// ErrDealBindings refuses a deal that binds what its reads did not return.
	ErrDealBindings = errors.New("the deal binds records its reads did not return")
)

// dealRefWord is the form of the word a deal's ID starts with.
func dealRefWord(word string) bool {
	if len(word) < 1 || len(word) > 40 {
		return false
	}
	for _, c := range []byte(word) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

const dealColumns = `d.workspace_id, d.ref, d.account_ref, a.display_name, coalesce(d.opportunity_ref, ''),
	coalesce(d.contract_ref, ''), coalesce(to_char(d.renews_on, 'YYYY-MM-DD'), ''), d.state,
	coalesce(d.registered_by::text, ''), d.account_read_id, d.opportunities_read_id, d.registered_at, d.closed_at,
	l.id, l.brief_id, l.published_at`

// dealFrom joins a deal to its account's name and to its newest card.
const dealFrom = ` FROM deals d
	JOIN accounts a ON a.workspace_id = d.workspace_id AND a.ref = d.account_ref
	LEFT JOIN LATERAL (
		SELECT c.id, c.brief_id, c.published_at FROM deal_cards dc
		  JOIN cards c ON c.workspace_id = dc.workspace_id AND c.id = dc.card_id
		 WHERE dc.workspace_id = d.workspace_id AND dc.deal_ref = d.ref
		 ORDER BY c.published_at DESC, c.id DESC LIMIT 1) l ON true`

func scanDeal(row pgx.Row) (Deal, error) {
	var d Deal
	var cardID, briefID *string
	var published *time.Time
	err := row.Scan(&d.WorkspaceID, &d.Ref, &d.AccountRef, &d.AccountName, &d.OpportunityRef, &d.ContractRef,
		&d.RenewsOn, &d.State, &d.RegisteredBy, &d.AccountReadID, &d.OpportunitiesReadID, &d.RegisteredAt,
		&d.ClosedAt, &cardID, &briefID, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deal{}, ErrNotFound
	}
	if cardID != nil {
		d.LatestCard = &DealCard{ID: *cardID, BriefID: deref(briefID), PublishedAt: *published}
	}
	return d, err
}

func collectDeals(rows pgx.Rows, err error) ([]Deal, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Deal, error) { return scanDeal(row) })
}

// RegisterDeal saves a deal under the workspace's lock, which keeps two registrations from taking one ID.
// A principal needs a live grant on the account, or it gets ErrNotPermitted. An account with an open deal
// gives ErrDealOpen, and bindings the reads did not return give ErrDealBindings.
func (s *Store) RegisterDeal(ctx context.Context, in DealRegistration) (Deal, error) {
	if !validUUID(in.WorkspaceID) || in.PrincipalID != "" && !validUUID(in.PrincipalID) {
		return Deal{}, ErrNotFound
	}
	if !dealRefWord(in.Word) || in.Year < 1000 || in.Year > 9999 {
		return Deal{}, fmt.Errorf("a deal's ID is a word of 1 to 40 letters and digits and a year, not %q and %d",
			in.Word, in.Year)
	}
	var ref string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var locked bool
		err := tx.QueryRow(ctx, `SELECT true FROM workspaces WHERE id = $1 FOR UPDATE`, in.WorkspaceID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if in.PrincipalID != "" {
			err := tx.QueryRow(ctx, `
				SELECT true FROM account_grants g
				  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
				 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
				   AND g.revoked_at IS NULL AND p.disabled_at IS NULL
				   FOR SHARE OF g, p`, in.WorkspaceID, in.PrincipalID, in.AccountRef).Scan(&locked)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotPermitted
			}
			if err != nil {
				return err
			}
		}
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deals
			WHERE workspace_id = $1 AND account_ref = $2 AND state = 'open')`,
			in.WorkspaceID, in.AccountRef).Scan(&open); err != nil {
			return err
		}
		if open {
			return ErrDealOpen
		}
		// The ID is word-year, or word-year-N with the lowest N from 2 that no deal of the workspace holds.
		base := in.Word + "-" + strconv.Itoa(in.Year)
		rows, err := tx.Query(ctx, `SELECT ref FROM deals WHERE workspace_id = $1 AND (ref = $2 OR ref LIKE $2 || '-%')`,
			in.WorkspaceID, base)
		if err != nil {
			return err
		}
		taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		held := map[string]bool{}
		for _, t := range taken {
			held[t] = true
		}
		ref = base
		for n := 2; held[ref]; n++ {
			ref = base + "-" + strconv.Itoa(n)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO deals (workspace_id, ref, account_ref, opportunity_ref, contract_ref, renews_on,
				registered_by, account_read_id, opportunities_read_id)
			VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8, $9)`,
			in.WorkspaceID, ref, in.AccountRef, nullable(in.OpportunityRef), nullable(in.ContractRef),
			nullable(in.RenewsOn), nullable(in.PrincipalID), in.AccountReadID, in.OpportunitiesReadID)
		return err
	})
	switch {
	case sqlState(err) == "GT021":
		return Deal{}, fmt.Errorf("%w: %v", ErrDealBindings, err)
	case sqlState(err) == "23505":
		// deals_one_open: another registration of this account committed first.
		return Deal{}, ErrDealOpen
	case err != nil:
		return Deal{}, err
	}
	return s.Deal(ctx, in.WorkspaceID, ref)
}

// Deal is one deal of a workspace, or ErrNotFound.
func (s *Store) Deal(ctx context.Context, workspaceID, ref string) (Deal, error) {
	if !validUUID(workspaceID) {
		return Deal{}, ErrNotFound
	}
	return scanDeal(s.pool.QueryRow(ctx, `SELECT `+dealColumns+dealFrom+`
		WHERE d.workspace_id = $1 AND d.ref = $2`, workspaceID, ref))
}

// Deals lists a workspace's deals: the open ones first, each group by ID.
func (s *Store) Deals(ctx context.Context, workspaceID string) ([]Deal, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	return collectDeals(s.pool.Query(ctx, `SELECT `+dealColumns+dealFrom+`
		WHERE d.workspace_id = $1 ORDER BY d.state DESC, d.ref`, workspaceID))
}

// OpenDeal is the account's open deal, and false when it has none.
func (s *Store) OpenDeal(ctx context.Context, workspaceID, accountRef string) (Deal, bool, error) {
	if !validUUID(workspaceID) {
		return Deal{}, false, nil
	}
	d, err := scanDeal(s.pool.QueryRow(ctx, `SELECT `+dealColumns+dealFrom+`
		WHERE d.workspace_id = $1 AND d.account_ref = $2 AND d.state = 'open'`, workspaceID, accountRef))
	if errors.Is(err, ErrNotFound) {
		return Deal{}, false, nil
	}
	return d, err == nil, err
}

// CloseDeal closes an open deal and gives it, with changed true. A closed deal is given as it is.
func (s *Store) CloseDeal(ctx context.Context, workspaceID, ref string) (d Deal, changed bool, err error) {
	if !validUUID(workspaceID) {
		return Deal{}, false, ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE deals SET state = 'closed', closed_at = clock_timestamp()
		WHERE workspace_id = $1 AND ref = $2 AND state = 'open'`, workspaceID, ref)
	if err != nil {
		return Deal{}, false, err
	}
	d, err = s.Deal(ctx, workspaceID, ref)
	return d, tag.RowsAffected() == 1, err
}

// bindDealCard binds a card being published to its deal, inside the publication's transaction. A deal
// that is not open, or of another account, gives ErrDealClosed and the card is not published.
func bindDealCard(ctx context.Context, tx pgx.Tx, card Card, dealRef string) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref)
		SELECT $1, $2, ref, account_ref FROM deals
		 WHERE workspace_id = $1 AND ref = $3 AND account_ref = $4 AND state = 'open'`,
		card.WorkspaceID, card.ID, dealRef, card.AccountRef)
	switch {
	case sqlState(err) == "GT022":
		return ErrDealClosed
	case err != nil:
		return err
	case tag.RowsAffected() != 1:
		return ErrDealClosed
	}
	return nil
}

// ViewDeals lists the deals of the accounts the principal holds a live grant on, as Deals orders them. A
// principal that is unknown, of another workspace, or disabled gets ErrNotPermitted.
func (s *Store) ViewDeals(ctx context.Context, v Viewer) ([]Deal, error) {
	if !validUUID(v.WorkspaceID) || !validUUID(v.PrincipalID) {
		return nil, ErrNotPermitted
	}
	var deals []Deal
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
		// The share lock on each grant holds a revocation off until the list is read.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM account_grants
			WHERE workspace_id = $1 AND principal_id = $2 AND revoked_at IS NULL FOR SHARE`,
			v.WorkspaceID, v.PrincipalID); err != nil {
			return err
		}
		deals, err = collectDeals(tx.Query(ctx, `SELECT `+dealColumns+dealFrom+`
			JOIN account_grants g ON g.workspace_id = d.workspace_id AND g.account_ref = d.account_ref
			WHERE d.workspace_id = $1 AND g.principal_id = $2 AND g.revoked_at IS NULL
			ORDER BY d.state DESC, d.ref`, v.WorkspaceID, v.PrincipalID))
		return err
	})
	return deals, err
}

// ViewDeal reads one deal; the principal needs a live grant on its account.
func (s *Store) ViewDeal(ctx context.Context, v Viewer, ref string) (Deal, error) {
	var deal Deal
	err := s.view(ctx, v, func(tx pgx.Tx) (string, error) {
		var account string
		err := tx.QueryRow(ctx, `SELECT account_ref FROM deals WHERE workspace_id = $1 AND ref = $2`,
			v.WorkspaceID, ref).Scan(&account)
		return account, err
	}, func(tx pgx.Tx) error {
		var err error
		deal, err = scanDeal(tx.QueryRow(ctx, `SELECT `+dealColumns+dealFrom+`
			WHERE d.workspace_id = $1 AND d.ref = $2`, v.WorkspaceID, ref))
		return err
	})
	return deal, err
}
