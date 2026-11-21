package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Matches: which deal an account's imported call and mapped Slack thread belong to (postgres.MatchAccount).
// The connector decides each one while its account has an open deal: when a deal is registered, a call is
// imported, a thread is mapped, and before brief reads the account. Every card reads through the same gate:
// a deal's card reads the account's call and thread only when they are matched to that deal, and names the
// matches it read; any other card, of an account with no open deal or made through MCP, never reads one that
// waits for a person or was rejected. Demo call notes and demo Slack messages belong to their account in the
// demo book and need no match.

// matchGate keeps a card's reads of the account's imported call and mapped thread to those it may read, and
// counts what they read for the card's publication (postgres.CardDeal).
type matchGate struct {
	store     *postgres.Store
	workspace string
	read      postgres.CardDeal
}

// allowed is whether the account's current item of a kind may be read, and the item. For a deal, the item
// must be matched to it. With no deal, an item that waits for a person or was rejected is refused, and any
// other is read and counted. An account with no item of the kind is read as before, and says so on the card.
func (g *matchGate) allowed(ctx context.Context, account, kind string) (item string, err error) {
	item, held, err := g.store.AccountItem(ctx, g.workspace, account, kind)
	if err != nil || !held {
		return "", err
	}
	match, decided, err := g.store.CurrentMatch(ctx, g.workspace, kind, item)
	switch {
	case err != nil:
		return "", err
	case g.read.Ref == "" && decided && match.State != "matched":
		return "", cards.ErrNotMatched
	case g.read.Ref == "":
		g.read.Unmatched++
		return item, nil
	case !decided || match.State != "matched" || match.DealRef != g.read.Ref:
		return "", cards.ErrNotMatched
	}
	g.read.Matches = append(g.read.Matches, match.ID)
	return item, nil
}

// callNotes reads the account's call when it may, and refuses records of another import than the one
// allowed: the account's active import changed while it was read.
func (g *matchGate) callNotes(ctx context.Context, calls callsSource, account string) ([]*strictjson.Object, error) {
	item, err := g.allowed(ctx, account, "call")
	if err != nil {
		return nil, err
	}
	records, err := calls.CallNotes(ctx, account)
	for _, record := range records {
		if item == "" || get(record, "record_ref") != "calls/call_notes/"+item {
			return nil, matchChanged
		}
	}
	return records, err
}

// messages reads the account's thread when it may, and refuses a read of another mapping than the one
// allowed: the account's thread was mapped again while it was read.
func (g *matchGate) messages(ctx context.Context, slack slackSource, account string) (messages,
	members []*strictjson.Object, snapshot *strictjson.Object, err error) {
	item, err := g.allowed(ctx, account, "thread")
	if err != nil {
		return nil, nil, nil, err
	}
	messages, members, snapshot, err = slack.Messages(ctx, account)
	if err == nil && snapshot != nil {
		mapping, _ := get(snapshot, "mapping").(*strictjson.Object)
		if item == "" || mapping == nil || get(mapping, "mapping_id") != item {
			return nil, nil, nil, matchChanged
		}
	}
	return messages, members, snapshot, err
}

// settleMatches decides the account's calls and threads the connector has not decided, when it has an open
// deal, as a deal is registered, a call imported or a thread mapped. A decision that cannot be made then is
// made before the deal's next card reads the account, and an undecided item is never read onto a deal's card,
// so a failure here fails nothing that was saved.
func settleMatches(ctx context.Context, store *postgres.Store, workspace, account string) {
	store.MatchAccount(ctx, workspace, account)
}

// matchChanged is a read whose call or thread changed after it was allowed for the card's deal.
var matchChanged = &cards.Diagnostic{Exit: 3, Message: "The account's call or thread changed while it was read " +
	"for its deal; nothing was published. Run the command again."}

// matchValue is a match as the commands and the API give it. The account's name is source text, shown inert.
func matchValue(m postgres.Match) (*strictjson.Object, error) {
	name, _, err := evidence.SafeText(m.AccountName)
	if err != nil {
		return nil, err
	}
	orNull := func(text string) any {
		if text == "" {
			return nil
		}
		return text
	}
	var at any
	if m.At != nil {
		at = m.At.UTC().Format(time.RFC3339Nano)
	}
	candidates := make([]any, len(m.Candidates))
	for i, c := range m.Candidates {
		candidates[i] = c
	}
	return strictjson.NewObject("id", m.ID, "kind", m.Kind, "item", m.Item, "account", m.AccountRef,
		"account_name", name, "at", at, "candidates", candidates, "state", m.State, "deal", orNull(m.DealRef),
		"basis", m.Basis, "decided_by", orNull(m.DecidedBy), "decided_at", utcSeconds(m.DecidedAt),
		"supersedes", orNull(m.Supersedes), "receipt", m.ReceiptID, "superseded", m.Superseded, "stale", m.Stale), nil
}

// listMatches lists the workspace's matches that wait for a person, or with --all its every current decision.
func (r *run) listMatches(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	listed := []any{}
	if !r.exists {
		return listed, nil
	}
	_, every := r.args.text("--all")
	matches, err := r.store.Matches(ctx, r.workspace.ID, every)
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		value, err := matchValue(m)
		if err != nil {
			return nil, err
		}
		listed = append(listed, value)
	}
	return listed, nil
}

// errNoMatch is a match ID the workspace does not hold.
var errNoMatch = &failure{code: 2, message: "No match of this workspace has that ID.", next: "gtm-connector deal matches --all"}

// decideMatch decides a match as the owner: onto the deal --deal names, or with --reject onto none.
func (r *run) decideMatch(ctx context.Context) (any, error) {
	deal, matched := r.args.text("--deal")
	_, rejected := r.args.text("--reject")
	if matched == rejected {
		return nil, refused("Give the deal with --deal DEAL_ID, or --reject to keep it off every card.")
	}
	if matched && !dealID.MatchString(deal) {
		return nil, refused("A deal ID is a word, the year it renews and a key of its account, such as " +
			"thornfield-2027-2e3hke.")
	}
	if !receiptID.MatchString(r.args.positionals[0]) {
		return nil, errNoMatch
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	if !r.exists {
		return nil, errNoMatch
	}
	m, err := r.store.DecideMatch(ctx, postgres.MatchDecision{WorkspaceID: r.workspace.ID,
		MatchID: r.args.positionals[0], DealRef: deal})
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return nil, errNoMatch
	case errors.Is(err, postgres.ErrMatchDeal):
		return nil, &failure{code: 2, message: "That deal is not of the match's account.", next: "gtm-connector deal list" + r.here()}
	case errors.Is(err, postgres.ErrMatchDecided):
		return nil, &failure{code: 3, message: "The match was decided again meanwhile. Decide its current one.",
			next: "gtm-connector deal matches --all" + r.here()}
	case err != nil:
		return nil, err
	}
	return matchValue(m)
}

// matchesText is deal matches and deal decide as a person reads them.
func (r *run) matchesText(result any) string {
	listed, isList := result.([]any)
	if !isList {
		listed = []any{result}
	}
	if len(listed) == 0 {
		return "No match waits for a person."
	}
	rows := make([][]any, len(listed))
	for i, value := range listed {
		m := value.(*strictjson.Object)
		var candidates []string
		for _, c := range get(m, "candidates").([]any) {
			candidates = append(candidates, c.(string))
		}
		state := get(m, "state").(string)
		if get(m, "stale") == true {
			state += " (stale)"
		}
		rows[i] = []any{get(m, "id"), get(m, "kind"), get(m, "item"), get(m, "account"), state,
			orNone(get(m, "deal")), oneLine(strings.Join(candidates, ", "))}
	}
	return r.screen.table([]string{"ID", "KIND", "ITEM", "ACCOUNT", "STATE", "DEAL", "CANDIDATES"}, rows)
}
