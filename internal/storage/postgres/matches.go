package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A match says which deal an account's call or Slack thread belongs to (0014_deal_matches.sql). The item is
// the account's active call import or its mapped thread. While the account has an open deal, the connector
// decides each item once, from the item's time: the deal that holds that time takes it (each deal holds the
// times from its registration to the next deal's), and an item with no time fits every deal of the account,
// so when the account has had more than one deal it waits for a person and reaches no card.
// A person may decide any item again, onto one deal of its account or onto none. Each decision is a row with
// its own receipt, and the latest decision of an item is its current one.

// Match is one decision of an item.
type Match struct {
	WorkspaceID, ID string
	// Kind is "call" for an import revision (note-...) and "thread" for a Slack mapping (map-...).
	Kind, Item, AccountRef string
	// AccountName is the account's name as the CRM gave it: source text, neutralized before it is shown.
	AccountName string
	// At is the item's time: the call's, or the thread's parent message. Nil for a call with none.
	At *time.Time
	// Candidates are the deals of the account that hold the item's time, oldest first.
	Candidates []string
	// State is "matched", "waiting" or "rejected". DealRef is the deal of a matched item.
	State, DealRef string
	// Basis is why: "only_deal", "within_deal", "time_unknown" or "person".
	Basis string
	// DecidedBy is the principal who decided, or "" for the connector or the owner.
	DecidedBy string
	// Supersedes is the decision this one replaced, or "".
	Supersedes string
	ReceiptID  string
	DecidedAt  time.Time
	// Superseded is whether a later decision of the item replaced this one.
	Superseded bool
	// Stale is whether the item is no longer the account's current import or mapping, so no card reads it.
	Stale bool
}

// MatchDecision is a person's decision of a match.
type MatchDecision struct {
	WorkspaceID string
	// PrincipalID is the person who decides, who needs a live grant on the item's account, or "" for the owner.
	PrincipalID string
	MatchID     string
	// DealRef is the deal of the item's account it is matched to, or "" to keep it off every card.
	DealRef string
}

var (
	// ErrMatchDecided refuses a decision of a match a later decision has superseded.
	ErrMatchDecided = errors.New("the match was decided again")
	// ErrMatchDeal refuses a decision onto a deal that is not of the item's account.
	ErrMatchDeal = errors.New("the deal is not of the match's account")
	// ErrDealChanged refuses a card whose reads were made for another deal than the one open when it was
	// published, or one that read a call or thread whose match changed meanwhile. Nothing was published.
	ErrDealChanged = errors.New("the account's deal or its matches changed while the card was read")
)

const matchColumns = `m.workspace_id, m.id, m.kind, m.item, m.account_ref, a.display_name, m.at, m.candidates,
	m.state, coalesce(m.deal_ref, ''), m.basis, coalesce(m.decided_by::text, ''), coalesce(m.supersedes::text, ''),
	m.receipt_id, m.decided_at,
	EXISTS (SELECT 1 FROM deal_matches n WHERE n.workspace_id = m.workspace_id AND n.supersedes = m.id),
	NOT CASE m.kind
		WHEN 'call' THEN EXISTS (SELECT 1 FROM call_import_active c WHERE c.workspace_id = m.workspace_id
			AND c.account_ref = m.account_ref AND c.import_id = m.item)
		ELSE EXISTS (SELECT 1 FROM slack_mappings s WHERE s.workspace_id = m.workspace_id
			AND s.account_ref = m.account_ref AND s.mapping::jsonb ->> 'mapping_id' = m.item) END`

const matchFrom = ` FROM deal_matches m JOIN accounts a ON a.workspace_id = m.workspace_id AND a.ref = m.account_ref`

func scanMatch(row pgx.Row) (Match, error) {
	var m Match
	err := row.Scan(&m.WorkspaceID, &m.ID, &m.Kind, &m.Item, &m.AccountRef, &m.AccountName, &m.At, &m.Candidates,
		&m.State, &m.DealRef, &m.Basis, &m.DecidedBy, &m.Supersedes, &m.ReceiptID, &m.DecidedAt, &m.Superseded,
		&m.Stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return Match{}, ErrNotFound
	}
	return m, err
}

func collectMatches(rows pgx.Rows, err error) ([]Match, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Match, error) { return scanMatch(row) })
}

// matchItem is an item of an account the connector has not decided.
type matchItem struct {
	kind, item string
	at         *time.Time
}

// accountItems are the account's current items, with their times: its active call import and its mapped
// thread. undecided keeps those the connector has not decided yet.
func accountItems(ctx context.Context, q querier, workspaceID, account string, undecided bool) ([]matchItem, error) {
	rows, err := q.Query(ctx, `
		SELECT * FROM (
			SELECT 'call' AS kind, a.import_id AS item, gtm_call_time(v.manifest) AS at FROM call_import_active a
			  JOIN call_import_revisions v ON v.workspace_id = a.workspace_id AND v.import_id = a.import_id
			 WHERE a.workspace_id = $1 AND a.account_ref = $2
			UNION ALL
			SELECT 'thread', s.mapping::jsonb ->> 'mapping_id', gtm_slack_time(s.thread_ts) FROM slack_mappings s
			 WHERE s.workspace_id = $1 AND s.account_ref = $2 AND s.mapping IS NOT NULL) i
		 WHERE NOT $3 OR NOT EXISTS (SELECT 1 FROM deal_matches m
		                             WHERE m.workspace_id = $1 AND m.kind = i.kind AND m.item = i.item)
		 ORDER BY kind`, workspaceID, account, undecided)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (matchItem, error) {
		var i matchItem
		return i, row.Scan(&i.kind, &i.item, &i.at)
	})
}

// decisionRun names the receipts of one run of decisions: decide_match receipts under the run ID, match-
// and 32 hex digits, each under a token made for the run alone.
type decisionRun struct {
	id, token string
	n         int
}

func newDecisionRun() *decisionRun {
	raw := make([]byte, 48)
	rand.Read(raw)
	token := sha256.Sum256(raw[16:])
	return &decisionRun{id: "match-" + hex.EncodeToString(raw[:16]), token: hex.EncodeToString(token[:])}
}

// start commits the pending receipt of the run's next decision, naming what it decides.
func (s *Store) startDecision(ctx context.Context, run *decisionRun, workspaceID, principal, account string,
	arguments map[string]any) (Receipt, error) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return Receipt{}, err
	}
	run.n++
	return s.StartReceipt(ctx, ReceiptStart{WorkspaceID: workspaceID, OperationID: fmt.Sprintf("%s/%d", run.id, run.n),
		PrincipalID: principal, TokenID: run.token, Tool: "decide_match", AccountRef: account, Arguments: encoded,
		RequestID: json.RawMessage(fmt.Sprint(run.n)), BriefID: run.id, Lease: time.Minute})
}

// failDecision finishes a decision's receipt as an error, on a context of its own, so it is never left pending.
func (s *Store) failDecision(ctx context.Context, r Receipt, status int, code string) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.FinishReceipt(settle, ReceiptFinish{ReceiptID: r.ID, LeaseToken: r.LeaseToken, Status: "error",
		HTTPStatus: status, Error: code})
	return err
}

// MatchAccount decides each of the account's items the connector has not decided, while the account has an
// open deal, and gives the decisions it made. Each gets a decide_match receipt, committed before the
// decision, that names the item, the decision and the deal; the decision and the receipt's finish commit
// together, with the account's deals held as they were read. An item another run decided meanwhile is left
// to that decision.
func (s *Store) MatchAccount(ctx context.Context, workspaceID, account string) ([]Match, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	if _, open, err := s.OpenDeal(ctx, workspaceID, account); err != nil || !open {
		return nil, err
	}
	items, err := accountItems(ctx, s.pool, workspaceID, account, true)
	if err != nil {
		return nil, err
	}
	run := newDecisionRun()
	var made []Match
	for _, item := range items {
		var candidates []string
		var deals int
		if err := s.pool.QueryRow(ctx, `SELECT gtm_match_candidates($1, $2, $3, clock_timestamp()),
			(SELECT count(*) FROM deals WHERE workspace_id = $1 AND account_ref = $2)`,
			workspaceID, account, item.at).Scan(&candidates, &deals); err != nil {
			return made, err
		}
		state, deal, basis := "waiting", "", "time_unknown"
		switch {
		case len(candidates) == 0:
			continue // the account's deal closed meanwhile
		case len(candidates) == 1 && deals == 1:
			state, deal, basis = "matched", candidates[0], "only_deal"
		case len(candidates) == 1:
			state, deal, basis = "matched", candidates[0], "within_deal"
		}
		arguments := map[string]any{"kind": item.kind, "item": item.item, "decision": state, "deal": nullable(deal)}
		receipt, err := s.startDecision(ctx, run, workspaceID, "", account, arguments)
		if err != nil {
			return made, err
		}
		var id string
		err = s.inTx(ctx, func(tx pgx.Tx) error {
			// A close or a registration of the account's deals waits for this decision, which is made on the
			// deals as they are now.
			if _, err := tx.Exec(ctx, `SELECT 1 FROM deals WHERE workspace_id = $1 AND account_ref = $2 FOR SHARE`,
				workspaceID, account); err != nil {
				return err
			}
			var now []string
			if err := tx.QueryRow(ctx, `SELECT gtm_match_candidates($1, $2, $3, clock_timestamp())`, workspaceID, account,
				item.at).Scan(&now); err != nil {
				return err
			}
			if fmt.Sprint(now) != fmt.Sprint(candidates) {
				return ErrDealChanged
			}
			if _, err := finishLocked(ctx, tx, ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken,
				Status: "ok", HTTPStatus: 200}, false); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `
				INSERT INTO deal_matches (workspace_id, kind, item, account_ref, at, candidates, state, deal_ref,
					basis, receipt_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
				workspaceID, item.kind, item.item, account, item.at, candidates, state, nullable(deal), basis,
				receipt.ID).Scan(&id)
		})
		if err != nil {
			failed := s.failDecision(ctx, receipt, 409, "source_changed")
			if sqlState(err) == "23505" && failed == nil {
				continue // decided by another run
			}
			return made, errors.Join(err, failed)
		}
		m, err := s.Match(ctx, workspaceID, id)
		if err != nil {
			return made, err
		}
		made = append(made, m)
	}
	return made, nil
}

// Match is one decision of a workspace, or ErrNotFound.
func (s *Store) Match(ctx context.Context, workspaceID, id string) (Match, error) {
	if !validUUID(workspaceID) || !validUUID(id) {
		return Match{}, ErrNotFound
	}
	return scanMatch(s.pool.QueryRow(ctx, `SELECT `+matchColumns+matchFrom+` WHERE m.workspace_id = $1 AND m.id = $2`,
		workspaceID, id))
}

// listed keeps the current decisions a list shows: with every, all of them; otherwise those that wait for a
// person while their item is still the account's current one, which a card could read.
const listed = ` AND NOT EXISTS (SELECT 1 FROM deal_matches n WHERE n.workspace_id = m.workspace_id AND n.supersedes = m.id)
	AND ($2 OR m.state = 'waiting' AND CASE m.kind
		WHEN 'call' THEN EXISTS (SELECT 1 FROM call_import_active c WHERE c.workspace_id = m.workspace_id
			AND c.account_ref = m.account_ref AND c.import_id = m.item)
		ELSE EXISTS (SELECT 1 FROM slack_mappings s WHERE s.workspace_id = m.workspace_id
			AND s.account_ref = m.account_ref AND s.mapping::jsonb ->> 'mapping_id' = m.item) END)
	ORDER BY m.decided_at, m.id`

// Matches lists a workspace's current decisions that wait for a person on an item a card could still read,
// or with every all of its current decisions, oldest first.
func (s *Store) Matches(ctx context.Context, workspaceID string, every bool) ([]Match, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	return collectMatches(s.pool.Query(ctx, `SELECT `+matchColumns+matchFrom+` WHERE m.workspace_id = $1`+listed,
		workspaceID, every))
}

// CurrentMatch is the current decision of an item, and false when the connector has not decided it.
func (s *Store) CurrentMatch(ctx context.Context, workspaceID, kind, item string) (Match, bool, error) {
	if !validUUID(workspaceID) {
		return Match{}, false, nil
	}
	m, err := scanMatch(s.pool.QueryRow(ctx, `SELECT `+matchColumns+matchFrom+`
		WHERE m.workspace_id = $1 AND m.kind = $2 AND m.item = $3
		  AND NOT EXISTS (SELECT 1 FROM deal_matches n WHERE n.workspace_id = m.workspace_id AND n.supersedes = m.id)`,
		workspaceID, kind, item))
	if errors.Is(err, ErrNotFound) {
		return Match{}, false, nil
	}
	return m, err == nil, err
}

// AccountItem is the account's current item of a kind, "call" or "thread", and false when it has none.
func (s *Store) AccountItem(ctx context.Context, workspaceID, account, kind string) (string, bool, error) {
	if !validUUID(workspaceID) {
		return "", false, nil
	}
	items, err := accountItems(ctx, s.pool, workspaceID, account, false)
	for _, i := range items {
		if i.kind == kind {
			return i.item, true, err
		}
	}
	return "", false, err
}

// ViewMatches lists the matches of the accounts the principal holds a live grant on as Matches lists a
// workspace's. A principal that is unknown, of another workspace, or disabled gets ErrNotPermitted.
func (s *Store) ViewMatches(ctx context.Context, v Viewer, every bool) ([]Match, error) {
	if !validUUID(v.WorkspaceID) || !validUUID(v.PrincipalID) {
		return nil, ErrNotPermitted
	}
	var matches []Match
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
		matches, err = collectMatches(tx.Query(ctx, `SELECT `+matchColumns+matchFrom+`
			JOIN account_grants g ON g.workspace_id = m.workspace_id AND g.account_ref = m.account_ref
			WHERE m.workspace_id = $1 AND g.principal_id = $3 AND g.revoked_at IS NULL`+listed,
			v.WorkspaceID, every, v.PrincipalID))
		return err
	})
	return matches, err
}

// DecideMatch records a decision of a match by a person or the owner, and gives it. The match must be its
// item's current decision; a person needs a live grant on the item's account, checked again as the decision
// commits. A decision always gets a decide_match receipt: an unknown match, or one of an account the person
// holds no live grant on, gets ErrNotPermitted under a receipt refused as error/403 that names no account,
// and ErrNotFound for the owner. A deal that is not of the item's account gives ErrMatchDeal, and a match
// decided again meanwhile ErrMatchDecided, each under a receipt finished as an error.
func (s *Store) DecideMatch(ctx context.Context, in MatchDecision) (Match, error) {
	if !validUUID(in.WorkspaceID) || in.PrincipalID != "" && !validUUID(in.PrincipalID) {
		return Match{}, ErrNotPermitted
	}
	decision := "rejected"
	if in.DealRef != "" {
		decision = "matched"
	}
	run := newDecisionRun()
	asked := in.MatchID
	if !validUUID(asked) {
		asked = "<invalid>"
	}
	arguments := map[string]any{"match": asked, "decision": decision, "deal": nullable(in.DealRef)}
	before, err := s.Match(ctx, in.WorkspaceID, in.MatchID)
	allowed := err == nil
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Match{}, err
	case in.PrincipalID != "":
		if allowed, err = s.HasAccountGrant(ctx, in.WorkspaceID, in.PrincipalID, before.AccountRef); err != nil {
			return Match{}, err
		}
	}
	if !allowed {
		refused, err := s.startDecision(ctx, run, in.WorkspaceID, in.PrincipalID, "", arguments)
		if err != nil {
			return Match{}, err
		}
		if err := s.failDecision(ctx, refused, 403, "not_permitted"); err != nil {
			return Match{}, err
		}
		if in.PrincipalID == "" {
			return Match{}, ErrNotFound
		}
		return Match{}, ErrNotPermitted
	}
	arguments["kind"], arguments["item"] = before.Kind, before.Item
	receipt, err := s.startDecision(ctx, run, in.WorkspaceID, in.PrincipalID, before.AccountRef, arguments)
	if err != nil {
		return Match{}, err
	}
	var id string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
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
		if in.DealRef != "" {
			var found bool
			err := tx.QueryRow(ctx, `SELECT true FROM deals WHERE workspace_id = $1 AND ref = $2 AND account_ref = $3`,
				in.WorkspaceID, in.DealRef, before.AccountRef).Scan(&found)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrMatchDeal
			}
			if err != nil {
				return err
			}
		}
		// The update lock makes a card being published that names this match, or another decision of it, wait
		// for this one, and this one for them.
		var superseded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deal_matches WHERE workspace_id = $1 AND supersedes = $2)
			FROM deal_matches WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, in.WorkspaceID, before.ID).
			Scan(&superseded); err != nil {
			return err
		}
		if superseded {
			return ErrMatchDecided
		}
		if _, err := finishLocked(ctx, tx, ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken,
			Status: "ok", HTTPStatus: 200}, false); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO deal_matches (workspace_id, kind, item, account_ref, at, candidates, state, deal_ref, basis,
				decided_by, supersedes, receipt_id)
			SELECT workspace_id, kind, item, account_ref, at, candidates, $3, $4, 'person', $5, id, $6
			  FROM deal_matches WHERE workspace_id = $1 AND id = $2
			RETURNING id`,
			in.WorkspaceID, before.ID, decision, nullable(in.DealRef), nullable(in.PrincipalID), receipt.ID).Scan(&id)
	})
	if err != nil {
		status, code := 409, "already_decided"
		switch {
		case errors.Is(err, ErrNotPermitted):
			status, code = 403, "not_permitted"
		case errors.Is(err, ErrMatchDeal):
			status, code = 400, "invalid_deal"
		case errors.Is(err, ErrMatchDecided):
		case sqlState(err) == "23505":
			err = ErrMatchDecided
		default:
			status, code = 500, "request_failed"
		}
		return Match{}, errors.Join(err, s.failDecision(ctx, receipt, status, code))
	}
	return s.Match(ctx, in.WorkspaceID, id)
}

// cardMatches are the matches a deal card read.
func cardMatches(ctx context.Context, q querier, workspaceID, cardID string) ([]Match, error) {
	return collectMatches(q.Query(ctx, `SELECT `+matchColumns+matchFrom+`
		JOIN deal_card_matches l ON l.workspace_id = m.workspace_id AND l.match_id = m.id
		WHERE l.workspace_id = $1 AND l.card_id = $2 ORDER BY m.kind, m.item`, workspaceID, cardID))
}

// CardDeal is what a card's reads were made for, which its publication binds to the account's open deal.
type CardDeal struct {
	// Ref is the open deal the card's calls and threads were read for, or "" when the account had none.
	Ref string
	// Matches are the matches of the calls and threads the card read, each matched to Ref.
	Matches []string
	// Unmatched counts the calls and threads the card read while the account had no open deal.
	Unmatched int
}
