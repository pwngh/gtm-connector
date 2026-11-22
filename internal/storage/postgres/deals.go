package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A deal is one renewal of one account, under a readable ID: a word of the account's name, the year it
// renews and a key of its account, such as thornfield-2027-2e3hke. Its bindings are records two CRM reads
// returned, under receipts the deal names, and the database refuses any other (0013_deals.sql). A card
// belongs to the deal that was open for its account when it was published.

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
	// Read are the matches of the calls and threads the newest card read. Deal and ViewDeal fill it.
	Read []Match
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
	// Word and Year start the deal's ID, which dealBase finishes.
	Word string
	Year int
	// OpportunityRef, ContractRef and RenewsOn are as Deal has them.
	OpportunityRef, ContractRef, RenewsOn string
	AccountReadID, OpportunitiesReadID    string
}

var (
	// ErrDealOpen refuses a second open deal for one account.
	ErrDealOpen = errors.New("the account already has an open deal")
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

// dealKey is the key of an account in a deal's ID: the first n characters of the base32 of the SHA-256 of
// the account's ID, in lower case. It depends on the account alone.
func dealKey(account string, n int) string {
	digest := sha256.Sum256([]byte(account))
	return strings.ToLower(base32.StdEncoding.EncodeToString(digest[:]))[:n]
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
		// The ID is word-year-key, where the key depends on the account alone, so no other account's deals
		// change it. Only a clash of six-character keys between two accounts, one in a billion, takes the
		// twelve-character key instead. The account's own next deal of the year adds the lowest free N from 2.
		base := in.Word + "-" + strconv.Itoa(in.Year) + "-" + dealKey(in.AccountRef, 6)
		var clash bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deals WHERE workspace_id = $1
			AND (ref = $2 OR ref LIKE $2 || '-%') AND account_ref <> $3)`,
			in.WorkspaceID, base, in.AccountRef).Scan(&clash); err != nil {
			return err
		}
		if clash {
			base = in.Word + "-" + strconv.Itoa(in.Year) + "-" + dealKey(in.AccountRef, 12)
		}
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
	d, err := scanDeal(s.pool.QueryRow(ctx, `SELECT `+dealColumns+dealFrom+`
		WHERE d.workspace_id = $1 AND d.ref = $2`, workspaceID, ref))
	if err == nil && d.LatestCard != nil {
		d.Read, err = cardMatches(ctx, s.pool, workspaceID, d.LatestCard.ID)
	}
	return d, err
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

// bindOpenDeal binds a card being published to its account's open deal, inside the publication's
// transaction, when the account has one that was registered by the time the card was, with the matches of
// the calls and threads it read. The share lock holds a close off until the publication commits. A card whose
// calls and threads were read for another deal, or with no deal, is refused with ErrDealChanged, and so is one
// whose match was decided again since it was read, and one required to be a deal's whose account's open deal
// is no longer that deal.
func bindOpenDeal(ctx context.Context, tx pgx.Tx, card Card, read *CardDeal) error {
	var ref string
	err := tx.QueryRow(ctx, `SELECT ref FROM deals
		WHERE workspace_id = $1 AND account_ref = $2 AND state = 'open' AND registered_at <= $3 FOR SHARE`,
		card.WorkspaceID, card.AccountRef, card.PublishedAt).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		if read.Required {
			return ErrDealChanged
		}
		return nil
	}
	if err != nil {
		return err
	}
	if read.Ref != "" && read.Ref != ref || read.Ref == "" && (read.Unmatched > 0 || read.Required) {
		return ErrDealChanged
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref) VALUES ($1, $2, $3, $4)`,
		card.WorkspaceID, card.ID, ref, card.AccountRef); err != nil {
		return err
	}
	for _, match := range read.Matches {
		_, err := tx.Exec(ctx, `INSERT INTO deal_card_matches (workspace_id, card_id, match_id) VALUES ($1, $2, $3)`,
			card.WorkspaceID, card.ID, match)
		if sqlState(err) == "GT024" {
			return fmt.Errorf("%w: %w", ErrDealChanged, err)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DealOfBrief is the deal a card belongs to, by its brief ID, or "" for a card of no deal.
func (s *Store) DealOfBrief(ctx context.Context, workspaceID, briefID string) (string, error) {
	if !validUUID(workspaceID) {
		return "", ErrNotFound
	}
	var ref string
	err := s.pool.QueryRow(ctx, `SELECT dc.deal_ref FROM deal_cards dc
		JOIN cards c ON c.workspace_id = dc.workspace_id AND c.id = dc.card_id
		WHERE c.workspace_id = $1 AND c.brief_id = $2`, workspaceID, briefID).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ref, err
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
		if err == nil && deal.LatestCard != nil {
			deal.Read, err = cardMatches(ctx, tx, v.WorkspaceID, deal.LatestCard.ID)
		}
		return err
	})
	return deal, err
}
