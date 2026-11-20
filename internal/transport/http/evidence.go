package httptransport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// Every read here goes through authz.Evidence. A denial is 403
// "not_permitted", never an empty list, and an unknown ID answers the same,
// so a principal learns nothing about what it may not read.

type cardView struct {
	ID            string     `json:"id"`
	Account       string     `json:"account"`
	BriefID       string     `json:"brief_id"`
	Kind          string     `json:"kind"`
	SchemaVersion int        `json:"schema_version"`
	RulesVersion  *string    `json:"rules_version"`
	SHA256        string     `json:"sha256"`
	Size          int        `json:"size"`
	SealReceiptID string     `json:"seal_receipt_id"`
	PublishedAt   string     `json:"published_at"`
	Units         []unitView `json:"units,omitempty"`
	Gaps          []gapView  `json:"gaps,omitempty"`
}

type unitView struct {
	Card            string          `json:"card"`
	N               int             `json:"n"`
	Kind            string          `json:"kind"`
	RecordRef       string          `json:"record_ref"`
	Tool            string          `json:"tool"`
	Shown           string          `json:"shown,omitempty"`
	ReceiptID       *string         `json:"receipt_id"`
	RecordVersionID *string         `json:"record_version_id"`
	Body            json.RawMessage `json:"body"`
	Receipt         *receiptView    `json:"receipt,omitempty"`
}

type gapView struct {
	Code        string          `json:"code"`
	Disposition string          `json:"disposition"`
	Count       *int            `json:"count"`
	ByRole      json.RawMessage `json:"by_role"`
	Text        string          `json:"text"`
}

type receiptView struct {
	ID                   string          `json:"id"`
	OperationID          string          `json:"operation_id"`
	ParentID             *string         `json:"parent_id"`
	Ordinal              *int            `json:"ordinal"`
	PrincipalID          *string         `json:"principal_id"`
	TokenID              string          `json:"token_id"`
	Tool                 string          `json:"tool"`
	Scope                *string         `json:"scope"`
	Account              string          `json:"account"`
	ConnectionGeneration *int64          `json:"connection_generation"`
	Arguments            json.RawMessage `json:"arguments"`
	RequestID            json.RawMessage `json:"request_id"`
	BriefID              *string         `json:"brief_id"`
	Status               string          `json:"status"`
	HTTPStatus           *int            `json:"http_status"`
	RowCount             int             `json:"row_count"`
	Rows                 []string        `json:"rows"`
	ElapsedMS            *float64        `json:"elapsed_ms"`
	Error                *string         `json:"error"`
	StartedAt            string          `json:"started_at"`
	FinishedAt           *string         `json:"finished_at"`
	DeliveredAt          *string         `json:"delivered_at"`
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func stampPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := stamp(*t)
	return &s
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func viewCard(c postgres.Card) cardView {
	return cardView{ID: c.ID, Account: c.AccountRef, BriefID: c.BriefID, Kind: c.Kind,
		SchemaVersion: c.SchemaVersion, RulesVersion: c.RulesVersion, SHA256: hex.EncodeToString(c.SHA256),
		Size: len(c.Bytes), SealReceiptID: c.SealReceiptID, PublishedAt: stamp(c.PublishedAt)}
}

func viewUnit(u postgres.Unit) unitView {
	return unitView{Card: u.CardID, N: u.N, Kind: u.Kind, RecordRef: u.RecordRef, Tool: u.Tool, Shown: u.Shown,
		ReceiptID: optional(u.ReceiptID), RecordVersionID: optional(u.SourceRecordID), Body: u.Body}
}

func viewReceipt(r postgres.Receipt) *receiptView {
	view := &receiptView{ID: r.ID, OperationID: r.OperationID, ParentID: optional(r.ParentID), Ordinal: r.Ordinal,
		PrincipalID: optional(r.PrincipalID), TokenID: r.TokenID, Tool: r.Tool, Scope: optional(r.Scope),
		Account: r.AccountRef, Arguments: r.Arguments, RequestID: r.RequestID, BriefID: optional(r.BriefID),
		Status: r.Status, HTTPStatus: r.HTTPStatus, RowCount: len(r.Rows), Rows: r.Rows, ElapsedMS: r.ElapsedMS,
		Error: r.Error, StartedAt: stamp(r.StartedAt), FinishedAt: stampPtr(r.FinishedAt),
		DeliveredAt: stampPtr(r.DeliveredAt)}
	if r.ConnectionID != "" {
		view.ConnectionGeneration = &r.ConnectionGeneration
	}
	return view
}

// readFailed answers a refused or failed read.
func readFailed(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrNotPermitted):
		writeError(w, http.StatusForbidden, "not_permitted", "Not permitted.")
	default:
		internal(w)
	}
}

type accountView struct {
	Account string `json:"account"`
	Name    string `json:"name"`
}

// accounts lists the accounts the principal may read, by reference.
func (s *Server) accounts(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	accounts, err := s.evidence.Accounts(r.Context(), p)
	if err != nil {
		readFailed(w, err)
		return
	}
	views := make([]accountView, len(accounts))
	for i, a := range accounts {
		// A display name is source text: neutralized before anything shows it.
		name, _, err := evidence.SafeText(a.DisplayName)
		if err != nil {
			internal(w)
			return
		}
		views[i] = accountView{a.Ref, name}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}

func (s *Server) accountCards(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	cards, err := s.evidence.AccountCards(r.Context(), p, r.PathValue("account"))
	if err != nil {
		readFailed(w, err)
		return
	}
	views := make([]cardView, len(cards))
	for i, c := range cards {
		views[i] = viewCard(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": r.PathValue("account"), "cards": views})
}

func (s *Server) card(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	view, err := s.evidence.Card(r.Context(), p, r.PathValue("card"))
	if err != nil {
		readFailed(w, err)
		return
	}
	card := viewCard(view.Card)
	card.Units = make([]unitView, len(view.Units))
	for i, u := range view.Units {
		card.Units[i] = viewUnit(u)
	}
	card.Gaps = make([]gapView, len(view.Gaps))
	for i, g := range view.Gaps {
		card.Gaps[i] = gapView{g.Code, g.Disposition, g.Count, g.ByRole, g.Text}
	}
	writeJSON(w, http.StatusOK, card)
}

// cardBytes gives the stored card exactly, so a client can re-hash it.
func (s *Server) cardBytes(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	view, err := s.evidence.Card(r.Context(), p, r.PathValue("card"))
	if err != nil {
		readFailed(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-SHA256", hex.EncodeToString(view.Card.SHA256))
	w.Write(view.Card.Bytes)
}

func (s *Server) unit(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || strconv.Itoa(n) != r.PathValue("n") {
		writeError(w, http.StatusNotFound, "not_found", "A unit number is a positive integer.")
		return
	}
	view, err := s.evidence.Unit(r.Context(), p, r.PathValue("card"), n)
	if err != nil {
		readFailed(w, err)
		return
	}
	unit := viewUnit(view.Unit)
	if view.Receipt != nil {
		unit.Receipt = viewReceipt(*view.Receipt)
	}
	writeJSON(w, http.StatusOK, unit)
}

func (s *Server) receipt(w http.ResponseWriter, r *http.Request, p authz.Principal) {
	receipt, err := s.evidence.Receipt(r.Context(), p, r.PathValue("receipt"))
	if err != nil {
		readFailed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewReceipt(receipt))
}
