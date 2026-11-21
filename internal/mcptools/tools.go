package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// notice is what read_renewal_brief says about a brief it releases (selected_briefs.NOTICE).
const notice = "This saved brief was not rechecked against what the source systems show now. " +
	"Only the caller's current scopes, the current selection, and the current source bindings were checked."

// run is the gate's tool body. The gate has admitted the call, checked its scope and its arguments, and
// committed its receipt, which the call names in its invocation.
func (s *server) run(ctx context.Context, call mcp.Call) (json.RawMessage, error) {
	value, err := strictjson.Loads(call.Arguments)
	arguments, _ := value.(*strictjson.Object)
	if err != nil || arguments == nil || call.Invocation == nil {
		return nil, errors.New("a call needs its arguments and its invocation")
	}
	var result *strictjson.Object
	switch call.Tool {
	case "list_deals":
		result, err = s.listDeals(ctx, call.Invocation)
	case "read_deal_card":
		result, err = s.readDealCard(ctx, call.Invocation, arguments)
	}
	if dealTools[call.Tool] {
		if err != nil {
			return nil, err
		}
		encoded, err := strictjson.Dumps(result, strictjson.Quoted)
		return json.RawMessage(encoded), err
	}
	// What the call reads was opened before its receipt started. A workspace whose sources could not be
	// opened, or were changed since, is refused here, under that receipt, before anything is read.
	view, err := s.recorder.opened(call.Invocation.AuditID)
	if err != nil {
		return nil, err
	}
	switch call.Tool {
	case "prepare_renewal_brief":
		result, err = s.prepare(ctx, view, call.Invocation, arguments)
	case "read_renewal_brief":
		result, err = s.reread(ctx, view, call.Invocation, arguments)
	default:
		result, err = s.read(ctx, view, call, arguments)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := strictjson.Dumps(result, strictjson.Quoted)
	return json.RawMessage(encoded), err
}

// requireSelected is gateway._require_selected: with a selection, an account outside it is refused before
// any source is read, and so is a Slack read of an account whose Slack is not selected.
func (s *server) requireSelected(account string, slack bool) error {
	if s.Selection == nil {
		return nil
	}
	includesSlack, selected := s.Selection[account]
	switch {
	case !selected:
		return mcp.ToolError("account_not_selected")
	case slack && !includesSlack:
		return mcp.ToolError("slack_not_selected")
	}
	return nil
}

// granted are the accounts the caller's principal holds a live grant on, as the store has them now. A token
// opens a source by its scope and an account by its principal's grant, and it needs both: no tool reads,
// lists or releases anything of an account outside this set. A principal the store no longer serves has none.
func (s *server) granted(ctx context.Context, inv *mcp.Invocation) (map[string]bool, error) {
	accounts, err := s.Store.ViewAccounts(ctx, postgres.Viewer{WorkspaceID: s.WorkspaceID, PrincipalID: inv.Principal})
	if err != nil && !errors.Is(err, postgres.ErrNotPermitted) {
		return nil, err
	}
	granted := map[string]bool{}
	for _, account := range accounts {
		granted[account.Ref] = true
	}
	return granted, nil
}

// requireGranted refuses an account the caller's principal holds no live grant on, before any source is read.
func (s *server) requireGranted(ctx context.Context, inv *mcp.Invocation, account string) error {
	granted, err := s.granted(ctx, inv)
	switch {
	case err != nil:
		return err
	case !granted[account]:
		return mcp.ToolError("account_not_granted")
	}
	return nil
}

// readable are the accounts a search or an account-wide list may hold for this caller, in order: those its
// principal is granted, and with a selection only the selected ones among them. It is never nil, so the
// source is always asked for these accounts and no others.
func (s *server) readable(ctx context.Context, inv *mcp.Invocation) ([]string, error) {
	granted, err := s.granted(ctx, inv)
	if err != nil {
		return nil, err
	}
	accounts := []string{}
	for account := range granted {
		if _, selected := s.Selection[account]; s.Selection == nil || selected {
			accounts = append(accounts, account)
		}
	}
	slices.Sort(accounts)
	return accounts, nil
}

// read is one of the five direct reads: the records, from the tool's own source, bound to the call's receipt.
func (s *server) read(ctx context.Context, view Opened, call mcp.Call, arguments *strictjson.Object) (*strictjson.Object, error) {
	account, given := arguments.Get("account_id")
	accountID, _ := account.(string)
	var records []*strictjson.Object
	more := false
	if err := ctx.Err(); err != nil {
		return nil, err // the caller has gone: nothing is read
	}
	var refusal mcp.ToolError
	switch call.Tool {
	case "search_accounts":
		query, _ := arguments.Get("query")
		text, _ := query.(string)
		var number *string
		if asked, _ := arguments.Get("account_number"); asked != nil {
			exact, _ := asked.(string)
			number = &exact
		}
		accounts, err := s.readable(ctx, call.Invocation)
		if err != nil {
			return nil, err
		}
		if records, more, err = view.Source.Search(ctx, text, number, accounts); errors.As(err, &refusal) {
			return nil, refusal
		} else if err != nil {
			return nil, err
		}
	case "list_opportunities":
		if !given || account == nil {
			// With no account, the grants and the selection limit the list itself.
			accounts, err := s.readable(ctx, call.Invocation)
			if err != nil {
				return nil, err
			}
			if records, err = view.Source.Opportunities(ctx, accounts); errors.As(err, &refusal) {
				return nil, refusal
			} else if err != nil {
				return nil, err
			}
			break
		}
		fallthrough
	case "get_account", "read_call_notes", "read_slack_messages":
		if err := s.requireSelected(accountID, call.Tool == "read_slack_messages"); err != nil {
			return nil, err
		}
		if err := s.requireGranted(ctx, call.Invocation, accountID); err != nil {
			return nil, err
		}
		switch {
		case call.Tool == "read_call_notes" && view.Sources.CallsBackend == "none":
			return nil, mcp.ToolError("calls_not_connected")
		case call.Tool == "read_slack_messages" && view.Sources.SlackBackend == "none":
			return nil, mcp.ToolError("slack_not_connected")
		}
		system, found, err := view.Source.Read(ctx, call.Tool, accountID)
		switch {
		case errors.Is(err, demo.ErrAccountNotFound):
			return nil, mcp.ToolError("account not found")
		case errors.As(err, &refusal):
			return nil, refusal
		case err != nil:
			return nil, err
		case system != readSource[call.Tool]:
			return nil, fmt.Errorf("%s was read from %s", call.Tool, system)
		}
		records = found
	default:
		return nil, fmt.Errorf("no tool %q", call.Tool)
	}
	versions, err := cards.RecordVersions(records)
	if err != nil {
		return nil, err
	}
	if err := s.recorder.bind(call.Invocation.AuditID, versions); err != nil {
		return nil, err
	}
	rows := make([]any, len(records))
	for i, record := range records {
		rows[i] = record
	}
	result := strictjson.NewObject("source", readSource[call.Tool], "records", rows, "untrusted_data", true)
	if more {
		// Present only when true, so a complete result keeps its shape.
		result.Set("has_more", true)
	}
	return result, nil
}

// principal is the caller as the card maker sees it: who the gate admitted, whose bearer is verified again
// before each read and before the brief is published, with the scopes it carries then, and whose rate window
// each read is counted against.
func (s *server) principal(inv *mcp.Invocation) cards.Principal {
	return cards.Principal{WorkspaceID: s.WorkspaceID, PrincipalID: inv.Principal, TokenID: inv.TokenID,
		Scopes: inv.Scopes, Verify: inv.Current, Charge: inv.Charge}
}

// prepare is prepare_renewal_brief. The maker runs under the receipt the gate committed for the call and
// starts no other; that receipt, finished with the saved bytes' digest, is the brief's seal.
func (s *server) prepare(ctx context.Context, view Opened, inv *mcp.Invocation, arguments *strictjson.Object) (*strictjson.Object, error) {
	outer, found := s.recorder.outer(inv.AuditID)
	if !found {
		return nil, errors.New("the call has no pending receipt")
	}
	// Refused before the first read, as a direct read of the account is. The publication checks the grant
	// once more, in its own transaction.
	account, _ := member(arguments, "account_id").(string)
	if err := s.requireSelected(account, false); err != nil {
		return nil, err
	}
	if err := s.requireGranted(ctx, inv, account); err != nil {
		return nil, err
	}
	maker := &cards.Maker{Receipts: s.Receipts, Source: view.Source, Sources: view.Sources, Connections: view.Connections,
		Generations: view.Generations, NewID: s.NewID, Now: s.Now}
	sealed, err := maker.Prepare(ctx, cards.Prepare{Principal: s.principal(inv), Selection: s.Selection,
		Outer: &outer, Arguments: arguments})
	var code *cards.CodeError
	switch {
	case errors.As(err, &code):
		return nil, mcp.ToolError(code.Code)
	case err != nil:
		// The brief was built and could not be saved. The store's own words stay in its receipt.
		return nil, mcp.ToolError("brief_storage_failed")
	}
	value, err := strictjson.Loads(sealed.Payload)
	artifact, _ := value.(*strictjson.Object)
	if err != nil || artifact == nil {
		return nil, errors.New("the saved brief does not read back")
	}
	snapshot, _ := member(artifact, "evidence").(*strictjson.Object)
	brief, err := released(sealed.BriefID, artifact)
	if err != nil {
		return nil, err
	}
	brief.Set("notes_status", member(snapshot, "notes_status"))
	brief.Set("slack_status", member(snapshot, "slack_status"))
	return envelope(sealed.BriefID, sealed.Payload, brief), nil
}

// reread is read_renewal_brief (selected_briefs.read_brief): a saved brief again, with no source read. The
// brief counts only if its own outer call finished ok for these exact bytes, and it is released only while
// the selection, the source bindings, the principal's grant on its account and the scopes the caller's
// bearer carries at that moment still cover what it holds.
func (s *server) reread(ctx context.Context, view Opened, inv *mcp.Invocation, arguments *strictjson.Object) (*strictjson.Object, error) {
	id, _ := member(arguments, "brief_id").(string)
	card, err := s.Store.CardByBrief(ctx, s.WorkspaceID, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, mcp.ToolError("brief_not_found")
	} else if err != nil {
		return nil, err
	}
	if payload, err := receipts.ParsePayload(card.Bytes); err != nil || !payload.Prepared {
		// A card saved by brief is not a prepared brief, and this tool reads only those.
		return nil, mcp.ToolError("brief_not_found")
	}
	_, verifyErr := cards.VerifyPrepared(ctx, card.Bytes, id, cards.StoreLog{Store: s.Receipts, WorkspaceID: s.WorkspaceID}, view.Sources)
	var code *cards.CodeError
	switch {
	case verifyErr == nil || errors.Is(verifyErr, cards.ErrForeign):
		// A changed binding is refused below, after the selection and the scopes, in read_brief's order.
	case errors.As(verifyErr, &code):
		return nil, mcp.ToolError(code.Code)
	default:
		return nil, mcp.ToolError("brief_not_accepted")
	}
	value, err := strictjson.Loads(card.Bytes)
	artifact, _ := value.(*strictjson.Object)
	if err != nil || artifact == nil {
		return nil, mcp.ToolError("brief_invalid")
	}
	account, _ := member(artifact, "account_id").(string)
	snapshot, _ := member(artifact, "evidence").(*strictjson.Object)
	notes := member(snapshot, "notes_status") == "available"
	slack := member(snapshot, "slack_status") == "available"
	if err := s.requireSelected(account, slack); err != nil {
		return nil, err
	}
	covered := func(scopes []string) bool {
		return slices.Contains(scopes, "crm:read") && (!notes || slices.Contains(scopes, "calls:read")) &&
			(!slack || slices.Contains(scopes, "slack:read"))
	}
	if !covered(inv.Scopes) {
		return nil, mcp.ToolError("brief_scope_not_covered")
	}
	if verifyErr != nil {
		return nil, mcp.ToolError("brief_source_changed")
	}
	// The release itself: the grant and the bearer are read again now, and the brief goes by what they are
	// at this moment, not by what the call was admitted with.
	if err := s.requireGranted(ctx, inv, account); err != nil {
		return nil, err
	}
	scopes, live := inv.Current()
	switch {
	case !live:
		return nil, mcp.ToolError("invalid_token")
	case !covered(scopes):
		return nil, mcp.ToolError("brief_scope_not_covered")
	}
	brief, err := released(id, artifact)
	if err != nil {
		return nil, mcp.ToolError("brief_invalid")
	}
	brief.Set("provider_rechecked", false)
	brief.Set("notice", notice)
	return envelope(id, card.Bytes, brief), nil
}

// member is a member's value, or nil for a missing member or a missing object.
func member(object *strictjson.Object, key string) any {
	if object == nil {
		return nil
	}
	value, _ := object.Get(key)
	return value
}

// released is what both brief tools return of a saved brief: its gaps as saved, and its text and units as
// evidence.AsShownText and evidence.AsShown give them. The brief's sealed bytes are its record.
func released(id string, artifact *strictjson.Object) (*strictjson.Object, error) {
	text, err := evidence.AsShownText(artifact)
	if err != nil {
		return nil, err
	}
	saved, _ := member(artifact, "units").([]any)
	units := make([]any, len(saved))
	for i, value := range saved {
		units[i] = value
		if unit, ok := value.(*strictjson.Object); ok {
			shown, err := evidence.AsShown(unit)
			if err != nil {
				return nil, err
			}
			units[i] = shown
		}
	}
	return strictjson.NewObject("brief_id", id, "account_id", member(artifact, "account_id"),
		"as_of", member(artifact, "as_of"), "text", text, "units", units,
		"gaps", member(artifact, "gaps"), "sources", member(artifact, "sources")), nil
}

// envelope is a brief tool's result. Its one record is the saved brief, named by the digest of its bytes.
func envelope(id string, payload []byte, brief *strictjson.Object) *strictjson.Object {
	record := strictjson.NewObject("id", id, "record_ref", cards.Reference(id, payload))
	return strictjson.NewObject("source", "brief", "records", []any{record}, "untrusted_data", true, "brief", brief)
}
