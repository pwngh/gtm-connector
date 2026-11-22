package httptransport

import (
	"context"
	"errors"
	"net/http"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// The deal routes: a deal is one renewal of one account under a readable ID. They take the same session,
// grants, Host check and caching as /api/v1. A deal of an account the principal holds no live grant on
// answers 403 "not_permitted", as an unknown deal does.

var (
	// ErrSourceFailed is a registration whose CRM read failed, or returned no account a deal can be made
	// from. The read's receipt says why, and the answer does not.
	ErrSourceFailed = errors.New("the CRM read failed")
	// ErrNoCRM is a registration in a workspace with no CRM this server can read. Nothing was read, so no
	// receipt was written.
	ErrNoCRM = errors.New("no CRM is connected")
	// ErrDealClosed is a refresh of a closed deal, which keeps its cards and gets no new one.
	ErrDealClosed = errors.New("the deal is closed")
	// ErrNoCard is a refresh whose evidence failed a check, so no card was made. The deal's last card stays.
	ErrNoCard = errors.New("the evidence failed a check")
)

// DealRegistry registers a deal for a signed-in principal: it reads the account and its opportunities
// through the workspace's CRM, each under a receipt that names the principal, and saves the deal. A
// principal without a live grant on the account gets authz.ErrNotPermitted, and a workspace with no CRM it
// can read gets ErrNoCRM.
//
// RefreshDeal reads every source of an open deal again as the principal and publishes a new card for it:
// each read and the seal name the principal, whose grant on the deal's account is checked before any source
// is opened, before each read and as the card is published. An unknown deal, or one of an account the
// principal holds no live grant on, gets authz.ErrNotPermitted; a closed deal ErrDealClosed; a source that
// was connected again during the read receipts.ErrSourceChanged; a deal or match that changed during it
// postgres.ErrDealChanged; a failed read ErrSourceFailed; and evidence that fails a check ErrNoCard. None
// of those publishes a card.
type DealRegistry interface {
	RegisterDeal(ctx context.Context, p authz.Principal, account string) (postgres.Deal, error)
	RefreshDeal(ctx context.Context, p authz.Principal, deal string) error
}

type dealView struct {
	ID           string        `json:"id"`
	Account      string        `json:"account"`
	AccountName  string        `json:"account_name"`
	State        string        `json:"state"`
	Opportunity  *string       `json:"opportunity"`
	Contract     *string       `json:"contract"`
	RenewsOn     *string       `json:"renews_on"`
	RegisteredBy *string       `json:"registered_by"`
	RegisteredAt string        `json:"registered_at"`
	ClosedAt     *string       `json:"closed_at"`
	Close        *dealClose    `json:"close"`
	Reads        dealReads     `json:"reads"`
	LatestCard   *dealCardView `json:"latest_card"`
}

// dealClose is how a deal closed. A deal closed before closes were recorded has the basis alone.
type dealClose struct {
	Basis     string  `json:"basis"`
	Reason    *string `json:"reason"`
	ClosedBy  *string `json:"closed_by"`
	Receipt   *string `json:"receipt"`
	FinalCard *string `json:"final_card"`
}

type dealReads struct {
	GetAccount        string `json:"get_account"`
	ListOpportunities string `json:"list_opportunities"`
}

type dealCardView struct {
	ID          string `json:"id"`
	BriefID     string `json:"brief_id"`
	PublishedAt string `json:"published_at"`
}

// viewDeal is a deal as the routes give it. The account's name is source text, and the reason it closed for
// a person's words: both are neutralized here.
func viewDeal(d postgres.Deal) (dealView, error) {
	name, _, err := evidence.SafeText(d.AccountName)
	if err != nil {
		return dealView{}, err
	}
	view := dealView{ID: d.Ref, Account: d.AccountRef, AccountName: name, State: d.State,
		Opportunity: optional(d.OpportunityRef), Contract: optional(d.ContractRef), RenewsOn: optional(d.RenewsOn),
		RegisteredBy: optional(d.RegisteredBy), RegisteredAt: stamp(d.RegisteredAt), ClosedAt: stampPtr(d.ClosedAt),
		Reads: dealReads{d.AccountReadID, d.OpportunitiesReadID}}
	if d.LatestCard != nil {
		view.LatestCard = &dealCardView{d.LatestCard.ID, d.LatestCard.BriefID, stamp(d.LatestCard.PublishedAt)}
	}
	if d.Close != nil {
		reason, _, err := evidence.SafeText(d.Close.Reason)
		if err != nil {
			return dealView{}, err
		}
		view.Close = &dealClose{Basis: d.Close.Basis, Reason: optional(reason), ClosedBy: optional(d.Close.By),
			Receipt: optional(d.Close.ReceiptID), FinalCard: optional(d.Close.FinalCard)}
	}
	return view, nil
}

// deals lists the deals the principal may read: those of the accounts it holds a live grant on.
func (s *Server) deals(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	deals, err := s.evidence.Deals(r.Context(), p)
	if err != nil {
		readFailed(w, err)
		return
	}
	views := make([]dealView, len(deals))
	for i, d := range deals {
		if views[i], err = viewDeal(d); err != nil {
			internal(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deals": views})
}

// deal is one deal and its newest card, whole, as GET /api/v1/cards/{card} gives it, or null when brief has
// made none for it. The card is read under the same grant check as that route.
func (s *Server) deal(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	deal, err := s.evidence.Deal(r.Context(), p, r.PathValue("deal"))
	if err != nil {
		readFailed(w, err)
		return
	}
	view, err := viewDeal(deal)
	if err != nil {
		internal(w)
		return
	}
	var card *cardDetail
	if deal.LatestCard != nil {
		read, err := s.evidence.Card(r.Context(), p, deal.LatestCard.ID)
		if err != nil {
			readFailed(w, err)
			return
		}
		detail, err := detailOf(read)
		if err != nil {
			internal(w)
			return
		}
		card = &detail
	}
	read := make([]matchView, len(deal.Read))
	for i, m := range deal.Read {
		if read[i], err = viewMatch(m); err != nil {
			internal(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deal": view, "card": card, "matches": read})
}

// refreshDeal reads an open deal's sources again and answers the deal as GET gives it, with its new card. It
// is a write, admitted by the session's CSRF token, and takes no body.
func (s *Server) refreshDeal(w http.ResponseWriter, r *http.Request, admitted admission) {
	if s.cfg.Deals == nil {
		writeError(w, http.StatusNotFound, "not_found", "Deals are not registered here.")
		return
	}
	p := authz.Principal{WorkspaceID: admitted.principal.WorkspaceID, ID: admitted.principal.ID}
	err := s.cfg.Deals.RefreshDeal(r.Context(), p, r.PathValue("deal"))
	switch {
	case errors.Is(err, authz.ErrNotPermitted):
		writeError(w, http.StatusForbidden, "not_permitted", "Not permitted.")
	case errors.Is(err, ErrDealClosed):
		writeError(w, http.StatusConflict, "deal_closed", "This deal is closed. It keeps its cards and gets no new one.")
	case errors.Is(err, receipts.ErrSourceChanged):
		writeError(w, http.StatusConflict, "source_changed",
			"A source was connected again while it was read. No card was published; send the request again.")
	case errors.Is(err, postgres.ErrDealChanged):
		writeError(w, http.StatusConflict, "deal_changed",
			"The deal or its matches changed while the card was read. No card was published; send the request again.")
	case errors.Is(err, ErrNoCRM):
		writeError(w, http.StatusConflict, "crm_not_connected",
			"This workspace has no CRM the server can read. No card was published.")
	case errors.Is(err, ErrSourceFailed):
		writeError(w, http.StatusBadGateway, "source_failed",
			"A read failed. No card was published, and the deal's last card stays; the read's receipt says why.")
	case errors.Is(err, ErrNoCard):
		writeError(w, http.StatusUnprocessableEntity, "evidence_refused",
			"The evidence failed a check. No card was published, and the deal's last card stays.")
	case err != nil:
		internal(w)
	default:
		s.deal(w, r, p)
	}
}

type registerRequest struct {
	Account string `json:"account"`
}

// registerDeal registers a deal for an account the signed-in principal holds a live grant on. It is a write,
// admitted by the session's CSRF token.
func (s *Server) registerDeal(w http.ResponseWriter, r *http.Request, admitted admission) {
	if s.cfg.Deals == nil {
		writeError(w, http.StatusNotFound, "not_found", "Deals are not registered here.")
		return
	}
	var request registerRequest
	if !readJSON(w, r, &request) {
		return
	}
	if request.Account == "" {
		writeError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object with the documented fields.")
		return
	}
	p := authz.Principal{WorkspaceID: admitted.principal.WorkspaceID, ID: admitted.principal.ID}
	deal, err := s.cfg.Deals.RegisterDeal(r.Context(), p, request.Account)
	switch {
	case errors.Is(err, authz.ErrNotPermitted):
		writeError(w, http.StatusForbidden, "not_permitted", "Not permitted.")
		return
	case errors.Is(err, postgres.ErrDealOpen):
		writeError(w, http.StatusConflict, "deal_open", "This account already has an open deal.")
		return
	case errors.Is(err, receipts.ErrSourceChanged):
		writeError(w, http.StatusConflict, "source_changed",
			"The CRM was connected again while it was read. No deal was registered; send the request again.")
		return
	case errors.Is(err, ErrNoCRM):
		writeError(w, http.StatusConflict, "crm_not_connected",
			"This workspace has no CRM the server can read. No deal was registered.")
		return
	case errors.Is(err, ErrSourceFailed):
		writeError(w, http.StatusBadGateway, "source_failed",
			"The CRM read failed. No deal was registered; the read's receipt says why.")
		return
	case err != nil:
		internal(w)
		return
	}
	view, err := viewDeal(deal)
	if err != nil {
		internal(w)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"deal": view})
}
