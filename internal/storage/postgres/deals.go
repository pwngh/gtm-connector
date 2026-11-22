package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// Close is how the deal closed, or nil while it is open.
	Close *DealClosed
	// LatestCard is the newest card published for the deal, or nil.
	LatestCard *DealCard
	// Read are the matches of the calls and threads the newest card read. Deal and ViewDeal fill it.
	Read []Match
}

// DealClosed is the record of a deal's close.
type DealClosed struct {
	// Basis is person; crm_stage for a deal the connector closed when a card read its opportunity as closed;
	// or unrecorded for a deal closed before closes were recorded, which has nothing else.
	Basis string
	// Reason is the person's reason: their own words, neutralized before they are shown. A crm_stage close
	// has none.
	Reason string
	// Outcome is won or lost for a crm_stage close, and "" otherwise.
	Outcome string
	// By is the principal that closed the deal, or "" for the owner.
	By string
	// ReceiptID is the close_deal receipt.
	ReceiptID string
	// FinalCard is the deal's newest card as it closed, or "" for a deal that had none.
	FinalCard string
}

// DealClose is what CloseDeal records.
type DealClose struct {
	WorkspaceID, Ref string
	// PrincipalID is the person that closes the deal, who must hold a live grant on its account, or "" for
	// the owner.
	PrincipalID string
	// Reason is one line of 1 to 500 characters with no control character.
	Reason string
	// Card is the card this close made as the deal's final card, or "" for a close that made none. With it,
	// the close is this close's or is refused: the deal must still be open, with that card its newest.
	Card string
	// Outcome, won or lost, makes this the connector's close on the CRM's closed stage: Card is the card that
	// read the deal's opportunity as closed, and the close has no reason and no principal.
	Outcome string
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
	// ErrCloseReason refuses a close whose reason is not one line of 1 to 500 characters.
	ErrCloseReason = errors.New("a reason is one line of 1 to 500 characters")
)

// dealRef is the form of a deal's ID, as the deals table checks it.
var dealRef = regexp.MustCompile(`^[a-z0-9]{1,40}-[0-9]{4}-([a-z2-7]{6}|[a-z2-7]{12})(-[1-9][0-9]{0,5})?$`)

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
	coalesce(d.close_basis, ''), coalesce(d.close_reason, ''), coalesce(d.closed_by::text, ''),
	coalesce(d.close_receipt_id::text, ''), coalesce(d.final_card_id::text, ''), coalesce(d.close_outcome, ''),
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
	var closed DealClosed
	err := row.Scan(&d.WorkspaceID, &d.Ref, &d.AccountRef, &d.AccountName, &d.OpportunityRef, &d.ContractRef,
		&d.RenewsOn, &d.State, &d.RegisteredBy, &d.AccountReadID, &d.OpportunitiesReadID, &d.RegisteredAt,
		&d.ClosedAt, &closed.Basis, &closed.Reason, &closed.By, &closed.ReceiptID, &closed.FinalCard, &closed.Outcome,
		&cardID, &briefID, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deal{}, ErrNotFound
	}
	if closed.Basis != "" {
		d.Close = &closed
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

// CloseReason is a reason as a close records it: trimmed, one line of 1 to 500 characters with no control
// character, or ErrCloseReason.
func CloseReason(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 500 {
		return "", ErrCloseReason
	}
	for _, c := range text {
		if unicode.IsControl(c) || c == '\u2028' || c == '\u2029' {
			return "", ErrCloseReason
		}
	}
	return text, nil
}

// startClose commits the pending close_deal receipt of one close, naming the deal, the basis, the reason and
// the final card, and for a close on the CRM's closed stage the opportunity and the outcome. account is ""
// for a close that is refused before the deal is known to the caller.
func (s *Store) startClose(ctx context.Context, in DealClose, account, finalCard, opportunity string) (Receipt, error) {
	named := map[string]any{"deal": in.Ref, "basis": "person", "reason": nullable(in.Reason),
		"final_card": nullable(finalCard)}
	if in.Outcome != "" {
		named["basis"], named["opportunity"], named["outcome"] = "crm_stage", nullable(opportunity), in.Outcome
	}
	encoded, err := json.Marshal(named)
	if err != nil {
		return Receipt{}, err
	}
	run := newDecisionRun()
	id := "close-" + strings.TrimPrefix(run.id, "match-")
	return s.StartReceipt(ctx, ReceiptStart{WorkspaceID: in.WorkspaceID, OperationID: id + "/1",
		PrincipalID: in.PrincipalID, TokenID: run.token, Tool: "close_deal", AccountRef: account, Arguments: encoded,
		RequestID: json.RawMessage("1"), BriefID: id, Lease: time.Minute})
}

// CloseDeal closes an open deal for a person or the owner, with their reason, and gives it with changed
// true. changed is true once the close has committed, also when reading the deal back then fails: the error
// is then of that read, and the deal is closed. The close gets a close_deal receipt, committed before the deal changes and finished ok in the
// transaction that closes it, which names the deal's final card: its newest card as it closes, or none. A
// deal that is closed already is given as it is, with no receipt, unless the close names the card it made
// (Card): then the close is its own or is refused, and a deal that is closed already, or whose newest card is
// another, gives ErrDealChanged under a receipt finished as an error. A person needs a live grant on the deal's
// account, checked again as the close commits: an unknown deal, or one of an account they hold no grant on,
// gets ErrNotPermitted under a receipt refused as error/403 that names no account, and ErrNotFound for the
// owner. A deal that got a newer card, or closed, while this close ran gives ErrDealChanged under a receipt
// finished as an error, and the deal is as that left it.
//
// With Outcome, the close is the connector's, on the CRM's closed stage: the card (Card) read the deal's
// opportunity as closed, won or lost. It has no reason and names no principal, and its receipt names the
// opportunity and the outcome. A deal that binds no opportunity gives ErrDealChanged.
func (s *Store) CloseDeal(ctx context.Context, in DealClose) (d Deal, changed bool, err error) {
	if !validUUID(in.WorkspaceID) || in.PrincipalID != "" && !validUUID(in.PrincipalID) {
		return Deal{}, false, ErrNotPermitted
	}
	switch {
	case in.Outcome == "":
		if in.Reason, err = CloseReason(in.Reason); err != nil {
			return Deal{}, false, err
		}
	case in.Outcome != "won" && in.Outcome != "lost", in.Reason != "", in.PrincipalID != "", in.Card == "":
		return Deal{}, false, errors.New("a close on the CRM's stage has an outcome and a card, and no reason or person")
	}
	if in.Card != "" && !validUUID(in.Card) {
		return Deal{}, false, ErrDealChanged
	}
	before, err := s.Deal(ctx, in.WorkspaceID, in.Ref)
	allowed := err == nil
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Deal{}, false, err
	case in.PrincipalID != "":
		if allowed, err = s.HasAccountGrant(ctx, in.WorkspaceID, in.PrincipalID, before.AccountRef); err != nil {
			return Deal{}, false, err
		}
	}
	if !allowed {
		if !dealRef.MatchString(in.Ref) {
			in.Ref = "<invalid>"
		}
		refused, err := s.startClose(ctx, in, "", "", "")
		if err != nil {
			return Deal{}, false, err
		}
		if err := s.failDecision(ctx, refused, 403, "not_permitted"); err != nil {
			return Deal{}, false, err
		}
		if in.PrincipalID == "" {
			return Deal{}, false, ErrNotFound
		}
		return Deal{}, false, ErrNotPermitted
	}
	if before.State != "open" && in.Card == "" {
		return before, false, nil
	}
	final := in.Card
	if final == "" && before.LatestCard != nil {
		final = before.LatestCard.ID
	}
	if in.Outcome != "" && before.OpportunityRef == "" {
		return Deal{}, false, ErrDealChanged
	}
	receipt, err := s.startClose(ctx, in, before.AccountRef, final, before.OpportunityRef)
	if err != nil {
		return Deal{}, false, err
	}
	basis := "person"
	if in.Outcome != "" {
		basis = "crm_stage"
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.at("close:started"); err != nil {
			return err
		}
		if in.PrincipalID != "" {
			var live bool
			err := tx.QueryRow(ctx, `
				SELECT true FROM account_grants g
				  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
				 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
				   AND g.revoked_at IS NULL AND p.disabled_at IS NULL
				   FOR SHARE OF g, p`, in.WorkspaceID, in.PrincipalID, before.AccountRef).Scan(&live)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotPermitted
			}
			if err != nil {
				return err
			}
		}
		// The update lock waits for every card being bound to the deal, and holds off any other.
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM deals WHERE workspace_id = $1 AND ref = $2 FOR UPDATE`,
			in.WorkspaceID, in.Ref).Scan(&state); err != nil {
			return err
		}
		var newest string
		err := tx.QueryRow(ctx, `SELECT c.id::text FROM deal_cards dc
			  JOIN cards c ON c.workspace_id = dc.workspace_id AND c.id = dc.card_id
			 WHERE dc.workspace_id = $1 AND dc.deal_ref = $2
			 ORDER BY c.published_at DESC, c.id DESC LIMIT 1`, in.WorkspaceID, in.Ref).Scan(&newest)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if state != "open" || newest != final {
			return ErrDealChanged
		}
		if _, err := finishLocked(ctx, tx, ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken,
			Status: "ok", HTTPStatus: 200}, false); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE deals SET state = 'closed', closed_at = clock_timestamp(),
			close_basis = $7, close_reason = $3, closed_by = $4, close_receipt_id = $5, final_card_id = $6,
			close_outcome = $8
			WHERE workspace_id = $1 AND ref = $2`, in.WorkspaceID, in.Ref, nullable(in.Reason), nullable(in.PrincipalID),
			receipt.ID, nullable(final), basis, nullable(in.Outcome))
		return err
	})
	if err != nil {
		status, code := 500, "request_failed"
		switch {
		case errors.Is(err, ErrNotPermitted):
			status, code = 403, "not_permitted"
		case errors.Is(err, ErrDealChanged):
			status, code = 409, "deal_changed"
		}
		return Deal{}, false, errors.Join(err, s.failDecision(ctx, receipt, status, code))
	}
	// The close is committed: changed is true whatever the read that follows gives.
	if err := s.at("close:committed"); err != nil {
		return Deal{}, true, err
	}
	d, err = s.Deal(ctx, in.WorkspaceID, in.Ref)
	return d, true, err
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
