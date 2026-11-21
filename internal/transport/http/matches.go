package httptransport

import (
	"errors"
	"net/http"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// The matches routes: which deal an account's imported call or mapped Slack thread belongs to. GET /v1/matches
// lists the matches waiting for a person, and POST /v1/matches/{match} decides one. A match of an account the
// principal holds no live grant on answers 403 "not_permitted", as an unknown match does.

type matchView struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Item        string   `json:"item"`
	Account     string   `json:"account"`
	AccountName string   `json:"account_name"`
	At          *string  `json:"at"`
	Candidates  []string `json:"candidates"`
	State       string   `json:"state"`
	Deal        *string  `json:"deal"`
	Basis       string   `json:"basis"`
	DecidedBy   *string  `json:"decided_by"`
	DecidedAt   string   `json:"decided_at"`
	Supersedes  *string  `json:"supersedes"`
	Receipt     string   `json:"receipt"`
	Superseded  bool     `json:"superseded"`
	Stale       bool     `json:"stale"`
}

// viewMatch is a match as the routes give it. The account's name is source text, neutralized here.
func viewMatch(m postgres.Match) (matchView, error) {
	name, _, err := evidence.SafeText(m.AccountName)
	if err != nil {
		return matchView{}, err
	}
	return matchView{ID: m.ID, Kind: m.Kind, Item: m.Item, Account: m.AccountRef, AccountName: name,
		At: stampPtr(m.At), Candidates: m.Candidates, State: m.State, Deal: optional(m.DealRef), Basis: m.Basis,
		DecidedBy: optional(m.DecidedBy), DecidedAt: stamp(m.DecidedAt), Supersedes: optional(m.Supersedes),
		Receipt: m.ReceiptID, Superseded: m.Superseded, Stale: m.Stale}, nil
}

// matches lists the matches waiting for a person on the accounts the principal holds a live grant on, or
// with ?all=1 each of their current decisions, so a wrong one can be found and decided again.
func (s *Server) matches(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	matches, err := s.evidence.Matches(r.Context(), p, r.URL.Query().Get("all") == "1")
	if err != nil {
		readFailed(w, err)
		return
	}
	views := make([]matchView, len(matches))
	for i, m := range matches {
		if views[i], err = viewMatch(m); err != nil {
			internal(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": views})
}

type decideRequest struct {
	Decision string `json:"decision"`
	Deal     string `json:"deal"`
}

// decideMatch decides a match as the signed-in person: "match" puts its call or thread on one deal of its
// account, and "reject" keeps it off every card. It is a write, admitted by the session's CSRF token.
func (s *Server) decideMatch(w http.ResponseWriter, r *http.Request, admitted admission) {
	var request decideRequest
	if !readJSON(w, r, &request) {
		return
	}
	switch {
	case request.Decision == "match" && request.Deal != "", request.Decision == "reject" && request.Deal == "":
	default:
		writeError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object with the documented fields.")
		return
	}
	p := authz.Principal{WorkspaceID: admitted.principal.WorkspaceID, ID: admitted.principal.ID}
	m, err := s.evidence.DecideMatch(r.Context(), p, r.PathValue("match"), request.Deal)
	switch {
	case errors.Is(err, authz.ErrNotPermitted):
		writeError(w, http.StatusForbidden, "not_permitted", "Not permitted.")
		return
	case errors.Is(err, postgres.ErrMatchDeal):
		writeError(w, http.StatusBadRequest, "invalid_deal", "The deal is not one of the match's account.")
		return
	case errors.Is(err, postgres.ErrMatchDecided):
		writeError(w, http.StatusConflict, "match_decided",
			"The match was decided again meanwhile. Decide its current one.")
		return
	case err != nil:
		internal(w)
		return
	}
	view, err := viewMatch(m)
	if err != nil {
		internal(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"match": view})
}
