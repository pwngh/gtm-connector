package mcptools

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The two deal tools read the store and no source. A deal is read under its principal's live grant on the
// deal's account and, with a selection, only when that account is selected. A deal that is unknown,
// ungranted or outside the selection gets the one refusal deal_not_permitted, so the refusal says nothing of
// whether the deal exists. Each result is one envelope: the deals and cards it holds are its records, and
// they become the rows of the call's receipt.

// notPermitted is read_deal_card's one refusal for a deal the caller may not read.
const notPermitted = mcp.ToolError("deal_not_permitted")

// evidenceOf is the store's evidence reads, with the grant checks every surface applies.
func (s *server) evidenceOf(inv *mcp.Invocation) (authz.Evidence, authz.Principal) {
	return authz.Evidence{Store: s.Store}, authz.Principal{WorkspaceID: s.WorkspaceID, ID: inv.Principal}
}

// selected reports whether a selection, when one is served, holds the account.
func (s *server) selected(account string) bool {
	_, held := s.Selection[account]
	return s.Selection == nil || held
}

// listDeals is list_deals: the deals of the accounts the caller's principal is granted, open ones first and
// each group by ID, and with a selection only those of selected accounts.
func (s *server) listDeals(ctx context.Context, inv *mcp.Invocation) (*strictjson.Object, error) {
	reads, principal := s.evidenceOf(inv)
	deals, err := reads.Deals(ctx, principal)
	if errors.Is(err, authz.ErrNotPermitted) {
		deals, err = nil, nil // a principal the store no longer serves reads no deal
	}
	if err != nil {
		return nil, err
	}
	records := []any{}
	for _, deal := range deals {
		if !s.selected(deal.AccountRef) {
			continue
		}
		record, err := dealRecord(deal)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return strictjson.NewObject("source", "deals", "records", records, "untrusted_data", true), nil
}

// readDealCard is read_deal_card: one deal and its newest card, whole, with no source read. The card is
// released only while the scopes the caller's bearer carries cover every source the card read, and, with a
// selection, while the account's Slack is selected when the card read Slack.
func (s *server) readDealCard(ctx context.Context, inv *mcp.Invocation, arguments *strictjson.Object) (*strictjson.Object, error) {
	ref, _ := member(arguments, "deal_id").(string)
	reads, principal := s.evidenceOf(inv)
	deal, err := reads.Deal(ctx, principal, ref)
	switch {
	case errors.Is(err, authz.ErrNotPermitted):
		return nil, notPermitted
	case err != nil:
		return nil, err
	case !s.selected(deal.AccountRef):
		return nil, notPermitted
	}
	record, err := dealRecord(deal)
	if err != nil {
		return nil, err
	}
	records := []any{record}
	var card any
	if deal.LatestCard != nil {
		view, err := reads.Card(ctx, principal, deal.LatestCard.ID)
		switch {
		case errors.Is(err, authz.ErrNotPermitted):
			// The grant was revoked between the two reads.
			return nil, notPermitted
		case err != nil:
			return nil, err
		}
		calls, slack, err := cardReads(view)
		if err != nil {
			return nil, err
		}
		if err := s.requireSelected(deal.AccountRef, slack); err != nil {
			return nil, err
		}
		if !slices.Contains(inv.Scopes, "crm:read") || calls && !slices.Contains(inv.Scopes, "calls:read") ||
			slack && !slices.Contains(inv.Scopes, "slack:read") {
			return nil, mcp.ToolError("card_scope_not_covered")
		}
		released, err := cardValue(view)
		if err != nil {
			return nil, err
		}
		card = released
		records = append(records, strictjson.NewObject("id", view.Card.BriefID,
			"record_ref", receipts.Reference(view.Card.BriefID, view.Card.Bytes)))
	}
	return strictjson.NewObject("source", "deals", "records", records, "untrusted_data", true, "deal", record,
		"card", card), nil
}

// dealRecord is a deal as both tools give it. Its record_ref is deals/ID. The account's name is source text,
// neutralized here; nothing else in it came from a source.
func dealRecord(d postgres.Deal) (*strictjson.Object, error) {
	name, _, err := evidence.SafeText(d.AccountName)
	if err != nil {
		return nil, err
	}
	var latest any
	if d.LatestCard != nil {
		latest = strictjson.NewObject("id", d.LatestCard.ID, "brief_id", d.LatestCard.BriefID,
			"published_at", stamp(d.LatestCard.PublishedAt))
	}
	var closed any
	if d.ClosedAt != nil {
		closed = stamp(*d.ClosedAt)
	}
	return strictjson.NewObject("id", d.Ref, "record_ref", "deals/"+d.Ref, "account", d.AccountRef,
		"account_name", name, "state", d.State, "opportunity", orNull(d.OpportunityRef),
		"contract", orNull(d.ContractRef), "renews_on", orNull(d.RenewsOn),
		"registered_at", stamp(d.RegisteredAt), "closed_at", closed, "latest_card", latest), nil
}

// cardReads reports whether a card read calls and Slack: its evidence says the read was available, or one
// of its units was read by that source's tool.
func cardReads(view authz.CardView) (calls, slack bool, err error) {
	value, err := strictjson.Loads(view.Card.Bytes)
	artifact, _ := value.(*strictjson.Object)
	if err != nil || artifact == nil {
		return false, false, errors.New("a card's bytes are not an object")
	}
	snapshot, _ := member(artifact, "evidence").(*strictjson.Object)
	calls = member(snapshot, "notes_status") == "available"
	slack = member(snapshot, "slack_status") == "available"
	for _, unit := range view.Units {
		calls = calls || readSource[unit.Tool] == "calls"
		slack = slack || readSource[unit.Tool] == "slack"
	}
	return calls, slack, nil
}

// cardValue is a card as read_deal_card gives it: its head and seal, the day it is as of and the source each
// system was, its text and units as evidence.AsShownText and evidence.AsShown give them, its gaps as saved,
// and its lines as data (null for a card published before lines were projected).
func cardValue(view authz.CardView) (*strictjson.Object, error) {
	value, err := strictjson.Loads(view.Card.Bytes)
	artifact, _ := value.(*strictjson.Object)
	if err != nil || artifact == nil {
		return nil, errors.New("a card's bytes are not an object")
	}
	text, err := evidence.AsShownText(artifact)
	if err != nil {
		return nil, err
	}
	units := make([]any, len(view.Units))
	for i, u := range view.Units {
		value, err := strictjson.Loads(u.Body)
		body, _ := value.(*strictjson.Object)
		if err != nil || body == nil {
			return nil, errors.New("a unit's body is not an object")
		}
		shown, err := evidence.AsShown(body)
		if err != nil {
			return nil, err
		}
		units[i] = strictjson.NewObject("n", u.N, "kind", u.Kind, "record_ref", u.RecordRef, "tool", u.Tool,
			"shown", orNull(u.Shown), "receipt_id", orNull(u.ReceiptID), "record_version_id", orNull(u.SourceRecordID),
			"body", shown)
	}
	gaps := make([]any, len(view.Gaps))
	for i, g := range view.Gaps {
		var count, byRole any
		if g.Count != nil {
			count = *g.Count
		}
		if len(g.ByRole) > 0 {
			if byRole, err = strictjson.Loads(g.ByRole); err != nil {
				return nil, err
			}
		}
		gaps[i] = strictjson.NewObject("code", g.Code, "disposition", g.Disposition, "count", count,
			"by_role", byRole, "text", g.Text)
	}
	var lines any
	if view.Lines != nil {
		list := make([]any, len(view.Lines))
		for i, l := range view.Lines {
			list[i] = l.Value()
		}
		lines = list
	}
	var sources any
	if view.Sources != nil {
		names := &strictjson.Object{}
		for _, system := range []string{"crm", "calls", "slack"} {
			if backend, named := view.Sources[system]; named {
				names.Set(system, backend)
			}
		}
		sources = names
	}
	var rules any
	if view.Card.RulesVersion != nil {
		rules = *view.Card.RulesVersion
	}
	c := view.Card
	seal := strictjson.NewObject("sha256", hex.EncodeToString(c.SHA256), "receipt_id", c.SealReceiptID,
		"schema_version", c.SchemaVersion, "rules_version", rules, "published_at", stamp(c.PublishedAt))
	return strictjson.NewObject("id", c.ID, "brief_id", c.BriefID, "account", c.AccountRef, "kind", c.Kind,
		"as_of", orNull(view.AsOf), "sources", sources, "text", text, "lines", lines, "units", units,
		"gaps", gaps, "seal", seal), nil
}

// stamp is a time as the API gives one: UTC, RFC 3339, to the microsecond the store keeps.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// orNull is a string, or null when it is empty.
func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}
