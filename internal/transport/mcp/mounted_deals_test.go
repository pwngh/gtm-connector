package mcp_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The deal tools where serve mounts them, on PostgreSQL: a deal registered from two CRM reads of the MCP
// clients' principal, and its card prepared over MCP and bound to it in its publication, as brief binds one.

// bindToDeals publishes each prepared brief as brief publishes a card: bound to its account's open deal.
func bindToDeals(c *mcptools.Config) {
	c.Receipts = &postgres.ReceiptStore{Store: c.Store.(*postgres.Store), OpenDeal: true}
}

// The deal tools' vectors get the same status, reply and receipt at the mounted endpoint as at the gate.
func TestMountedDealVectors(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	t.Logf("%d deal tool vectors replayed at the mounted endpoint",
		mcp.ReplayDealVectors(t, mcp.Target{Post: m.post, Receipts: m.outerLabels}))
}

// register registers an open deal of an account from two reads made over MCP with this bearer, whose
// principal is the machine principal.
func (m *mounted) register(account, word, bearer string) postgres.Deal {
	m.t.Helper()
	_, accountRead := m.call("register account", "get_account", map[string]any{"account_id": account}, bearer)
	_, opportunities := m.call("register opportunities", "list_opportunities", map[string]any{"account_id": account}, bearer)
	deal, err := m.store.RegisterDeal(ctx, postgres.DealRegistration{WorkspaceID: m.workspace.ID,
		PrincipalID: m.machine.ID, AccountRef: account, Word: word, Year: 2027,
		AccountReadID: accountRead.ID, OpportunitiesReadID: opportunities.ID})
	must(m.t, err)
	return deal
}

// structured is a released result's structured content; anything else fails the test.
func structured(t *testing.T, body []byte) map[string]any {
	t.Helper()
	result := at(decode(t, body), "result")
	content, ok := at(result, "structuredContent").(map[string]any)
	if at(result, "isError") != false || !ok || content["untrusted_data"] != true || content["source"] != "deals" {
		t.Fatalf("not a released deal result: %s", body)
	}
	return content
}

// A token reads the deals its principal is granted and a deal's newest card, whole: the card's sealed text
// byte for byte, its lines, units and gaps as the store holds them, and its seal. The receipt's rows are the
// deal and the card, and each call leaves one outer receipt.
func TestMountedDealCard(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	read := bearer("read")
	deal := m.register("acc-003", "thornfield", read)
	other := m.register("acc-001", "alder", read) // of an account outside the selection
	m.serve(cards.Selection{"acc-003": true, "acc-007": false}, bindToDeals)

	// Before a card is published the deal is listed, and its card is null.
	response, row := m.call(1, "read_deal_card", map[string]any{"deal_id": deal.Ref}, read)
	content := structured(t, response.Body.Bytes())
	if content["card"] != nil || at(content, "deal", "id") != deal.Ref || !reflect.DeepEqual(row.Rows, []string{"deals/" + deal.Ref}) {
		t.Fatalf("a deal with no card: %s, receipt %+v", response.Body, row)
	}

	prepared, outer := m.call(2, "prepare_renewal_brief", map[string]any{"account_id": "acc-003"}, read)
	if outer.Status != "ok" {
		t.Fatalf("prepare: %s", prepared.Body)
	}
	cardRef := records(t, prepared)[0]
	briefID := strings.Split(cardRef, "/")[1]
	stored, err := m.store.CardByBrief(ctx, m.workspace.ID, briefID)
	must(t, err)
	value, err := strictjson.Loads(stored.Bytes)
	must(t, err)
	sealedText, _ := value.(*strictjson.Object).Get("text")

	before := len(m.book.made())
	response, row = m.call(3, "read_deal_card", map[string]any{"deal_id": deal.Ref}, read)
	content = structured(t, response.Body.Bytes())
	card, _ := content["card"].(map[string]any)
	if card == nil || card["text"] != sealedText || card["id"] != stored.ID || card["brief_id"] != briefID ||
		at(card, "seal", "sha256") != hex.EncodeToString(stored.SHA256) || at(card, "seal", "receipt_id") != outer.ID ||
		at(content, "deal", "latest_card", "brief_id") != briefID {
		t.Fatalf("the deal's card: %s", response.Body)
	}
	if !reflect.DeepEqual(row.Rows, []string{"deals/" + deal.Ref, cardRef}) || !reflect.DeepEqual(records(t, response), row.Rows) ||
		row.Scope == nil || *row.Scope != "crm:read" || row.Principal == nil || *row.Principal != m.machine.ID ||
		len(m.receipts("parent_id = $1", row.ID)) != 0 || len(m.book.made()) != before {
		t.Fatalf("read_deal_card's receipt %+v, book read %v", row, m.book.made()[before:])
	}
	// The units, gaps and lines are the store's own.
	units, _ := card["units"].([]any)
	lines, _ := card["lines"].([]any)
	gaps, _ := card["gaps"].([]any)
	if len(units) != m.count("SELECT count(*) FROM evidence_units WHERE card_id = $1", stored.ID) ||
		len(gaps) != m.count("SELECT count(*) FROM gaps WHERE card_id = $1", stored.ID) ||
		len(lines) != m.count("SELECT count(*) FROM card_lines WHERE card_id = $1", stored.ID) || len(lines) == 0 {
		t.Fatalf("units %d, gaps %d, lines %d", len(units), len(gaps), len(lines))
	}
	renewal := false
	for _, line := range lines {
		if at(line, "countdown") != nil {
			renewal = at(line, "countdown", "date") != nil && reflect.DeepEqual(at(line, "cites"), []any{json.Number("1")})
		}
	}
	if !renewal || at(units[0], "n") != json.Number("1") || at(units[0], "receipt_id") == nil || at(units[0], "body", "n") != json.Number("1") {
		t.Fatalf("lines %v, first unit %v", lines, units[0])
	}
	// The result holds no evidence, no token fingerprint and no principal.
	for _, absent := range []string{`"evidence"`, receipts.Fingerprint(read), m.machine.ID, "registered_by"} {
		if strings.Contains(response.Body.String(), absent) {
			t.Errorf("read_deal_card gave %s", absent)
		}
	}

	// list_deals gives the selected account's deal, not the other's.
	response, row = m.call(4, "list_deals", map[string]any{}, read)
	if got := records(t, response); !reflect.DeepEqual(got, []string{"deals/" + deal.Ref}) || !reflect.DeepEqual(row.Rows, got) {
		t.Fatalf("list_deals: %s", response.Body)
	}

	// Without the selection every granted deal is listed, open ones first and each group by ID.
	m.serve(nil, nil)
	response, _ = m.call(5, "list_deals", map[string]any{}, read)
	if got := records(t, response); !reflect.DeepEqual(got, []string{"deals/" + other.Ref, "deals/" + deal.Ref}) {
		t.Fatalf("list_deals without a selection: %v", got)
	}
	// The card read Slack, so a selection that leaves the account's Slack out releases no card.
	m.serve(cards.Selection{"acc-003": false}, nil)
	if response, row := m.call(6, "read_deal_card", map[string]any{"deal_id": deal.Ref}, read); toolError(t, response) != "slack_not_selected" ||
		row.label() != "read_deal_card error 200" || len(row.Rows) != 0 {
		t.Fatalf("a card whose Slack is not selected: %s", response.Body)
	}
}

// An unknown deal, a deal of an account the principal is not granted, and a deal outside the selection get
// one refusal, under a receipt, and the reply says nothing more of the deal. A token whose scopes do not
// cover the sources the card read gets no card. Host, scope and expiry are refused by the gate first.
func TestMountedDealCardRefusals(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	read := bearer("read")
	deal := m.register("acc-003", "thornfield", read)
	unselected := m.register("acc-007", "juniper", read)
	m.serve(cards.Selection{"acc-003": true}, bindToDeals)
	if _, outer := m.call(1, "prepare_renewal_brief", map[string]any{"account_id": "acc-003"}, read); outer.Status != "ok" {
		t.Fatal("prepare failed")
	}
	narrow := "bearer-of-a-principal-granted-acc-001-only-000"
	m.issueFor(m.grantee("acc-001"), narrow, "crm:read", "calls:read", "slack:read")

	same := ""
	for _, c := range []struct {
		label, deal, bearer string
	}{
		{"an unknown deal", "thornfield-2027-zzzzzz", read},
		{"a deal the principal is not granted", deal.Ref, narrow},
		{"a deal outside the selection", unselected.Ref, read},
	} {
		before := len(m.book.made())
		response, row := m.call(7, "read_deal_card", map[string]any{"deal_id": c.deal}, c.bearer)
		body := strings.ReplaceAll(response.Body.String(), row.ID, "AUDIT")
		if toolError(t, response) != "deal_not_permitted" || row.label() != "read_deal_card error 200" || len(row.Rows) != 0 ||
			strings.Contains(body, c.deal) || strings.Contains(body, "acc-") || len(m.book.made()) != before {
			t.Errorf("%s: %s, receipt %+v", c.label, response.Body, row)
		}
		if same == "" {
			same = body
		} else if body != same {
			t.Errorf("%s answered %s, not %s", c.label, body, same)
		}
	}
	response, _ := m.call(20, "list_deals", map[string]any{}, narrow)
	if got := records(t, response); len(got) != 0 {
		t.Errorf("list_deals of a principal granted no deal's account: %v", got)
	}

	// The card read calls and Slack: a token with crm:read alone, or without slack:read, gets no card.
	crmOnly, noSlack := "bearer-with-crm-read-only-0000000000000000000", "bearer-with-crm-and-calls-000000000000000000"
	m.issue(crmOnly, "crm:read")
	m.issue(noSlack, "crm:read", "calls:read")
	for i, bearer := range []string{crmOnly, noSlack} {
		if response, row := m.call(30+i, "read_deal_card", map[string]any{"deal_id": deal.Ref}, bearer); toolError(t, response) !=
			"card_scope_not_covered" || len(row.Rows) != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
			t.Errorf("%s: %s", bearer, response.Body)
		}
	}

	// The gate refuses before the tool runs: another Host, a token without crm:read, an expired token.
	call := callBody(40, "read_deal_card", map[string]any{"deal_id": deal.Ref})
	before := len(m.outer())
	host := m.send(http.MethodPost, "/mcp", "evil.example", call, requestHeaders("tools/call", "read_deal_card", read))
	scope := m.post(call, requestHeaders("tools/call", "read_deal_card", bearer("calls")))
	m.wall = func() float64 { return float64(time.Now().Add(2*time.Hour).UnixMicro()) / 1e6 }
	expired := m.post(call, requestHeaders("tools/call", "read_deal_card", read))
	m.wall = nil
	var labels []string
	for _, row := range m.outer()[before:] {
		labels = append(labels, row.label())
		if len(row.Rows) != 0 {
			t.Errorf("a refusal bound rows: %+v", row)
		}
	}
	if refusal(t, host) != "untrusted_origin_or_host" || refusal(t, scope) != "insufficient_scope" ||
		refusal(t, expired) != "invalid_token" || !reflect.DeepEqual(labels, []string{"read_deal_card error 403",
		"read_deal_card error 403", "read_deal_card error 401"}) {
		t.Fatalf("gate refusals: %d %d %d, receipts %v", host.Code, scope.Code, expired.Code, labels)
	}
	for _, response := range []string{host.Body.String(), scope.Body.String(), expired.Body.String()} {
		if strings.Contains(response, "DEAL CARD") || strings.Contains(response, "acc-003") {
			t.Errorf("a refusal said something of the deal: %s", response)
		}
	}
	if slices.Contains(m.book.made(), "read_deal_card") {
		t.Fatal("a deal tool read the book")
	}
}
