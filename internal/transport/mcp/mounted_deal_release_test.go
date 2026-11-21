package mcp_test

import (
	"context"
	"crypto/sha256"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// What read_deal_card checks as it releases a card, as read_renewal_brief checks a saved brief: the card was
// read from the sources the workspace is bound to now, and the caller's bearer still verifies and still
// carries the scopes the card needs.

// cardAt is a mounted endpoint with a deal of acc-003 whose newest card was prepared over MCP with the "read"
// fixture bearer, served again with change.
func cardAt(t *testing.T, change func(*mcptools.Config)) (*mounted, postgres.Deal) {
	t.Helper()
	m := mount(t, nil, nil)
	m.fixture()
	deal := m.register("acc-003", "thornfield", bearer("read"))
	m.serve(cards.Selection{"acc-003": true}, bindToDeals)
	if _, outer := m.call("prepare", "prepare_renewal_brief", map[string]any{"account_id": "acc-003"}, bearer("read")); outer.Status != "ok" {
		t.Fatal("prepare failed")
	}
	m.serve(cards.Selection{"acc-003": true}, change)
	return m, deal
}

// onCard is a store that runs a hook as a deal's card is read, after the call was admitted.
type onCard struct {
	mcptools.Storage
	reading func()
}

func (o onCard) ViewCard(ctx context.Context, v postgres.Viewer, cardID string) (postgres.CardView, error) {
	o.reading()
	return o.Storage.ViewCard(ctx, v, cardID)
}

// A card read from other bindings than the workspace's now is refused as read_renewal_brief refuses one.
func TestMountedDealCardFromOtherSources(t *testing.T) {
	m, deal := cardAt(t, func(c *mcptools.Config) {
		moved := demoSources
		moved.CallsBackend = "none"
		c.Sources = moved
	})
	response, row := m.call(1, "read_deal_card", map[string]any{"deal_id": deal.Ref}, bearer("read"))
	if toolError(t, response) != "brief_source_changed" || len(row.Rows) != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
		t.Fatalf("a card of other sources: %s", response.Body)
	}
}

// The bearer is verified again before the card is released: one revoked, or narrowed, while the call runs
// gets no card.
func TestMountedDealCardRereadsTheBearer(t *testing.T) {
	read := bearer("read")
	digest := sha256.Sum256([]byte(read))
	for _, c := range []struct {
		label, statement, want string
	}{
		{"revoked", "UPDATE machine_tokens SET revoked_at = now() WHERE token_sha256 = $1", "invalid_token"},
		{"narrowed", "UPDATE machine_tokens SET scopes = ARRAY['crm:read'] WHERE token_sha256 = $1", "card_scope_not_covered"},
	} {
		t.Run(c.label, func(t *testing.T) {
			var m *mounted
			m, deal := cardAt(t, func(config *mcptools.Config) {
				bindToDeals(config)
				config.Store = onCard{Storage: config.Store, reading: func() { must(t, m.sql(c.statement, digest[:])) }}
			})
			response, row := m.call(1, "read_deal_card", map[string]any{"deal_id": deal.Ref}, read)
			if toolError(t, response) != c.want || len(row.Rows) != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
				t.Fatalf("a bearer %s during the call: %s", c.label, response.Body)
			}
		})
	}
}

// The deal tools read no source, so they do not open one: a workspace whose CRM cannot be opened still lists
// its deals and gives their cards.
func TestMountedDealToolsOpenNoSource(t *testing.T) {
	opened := 0
	m, deal := cardAt(t, func(c *mcptools.Config) {
		c.Source, c.Sources, c.Connections = nil, evidence.Sources{}, nil
		c.Open = func(context.Context) (mcptools.Opened, error) {
			opened++
			return mcptools.Opened{}, mcp.ToolError("crm_not_connected")
		}
		c.Bound = func(context.Context) (evidence.Sources, error) { return demoSources, nil }
	})
	if got := records(t, m.callOK(1, "list_deals", map[string]any{})); len(got) != 1 {
		t.Fatalf("list_deals %v", got)
	}
	if got := records(t, m.callOK(2, "read_deal_card", map[string]any{"deal_id": deal.Ref})); len(got) != 2 {
		t.Fatalf("read_deal_card %v", got)
	}
	if opened != 0 {
		t.Fatalf("the deal tools opened the sources %d times", opened)
	}
	// Another tool still opens them, and is refused under its receipt.
	if response, _ := m.call(3, "get_account", map[string]any{"account_id": "acc-003"}, bearer("read")); toolError(t, response) != "crm_not_connected" || opened != 1 {
		t.Fatalf("get_account: %s, opened %d", response.Body, opened)
	}
}

// callOK is call with the "read" fixture bearer.
func (m *mounted) callOK(id any, tool string, arguments any) *httptest.ResponseRecorder {
	response, _ := m.call(id, tool, arguments, bearer("read"))
	return response
}

// The receipt of read_deal_card names the deal's account when the deal exists, granted or not, and none for
// an unknown deal.
func TestMountedDealCardReceiptNamesTheAccount(t *testing.T) {
	m, deal := cardAt(t, nil)
	_, row := m.call(1, "read_deal_card", map[string]any{"deal_id": deal.Ref}, bearer("read"))
	if n := m.count("SELECT count(*) FROM receipts WHERE id = $1 AND account_ref = 'acc-003' AND requested_account = 'acc-003'", row.ID); n != 1 {
		t.Errorf("the receipt of a read of %s does not name acc-003", deal.Ref)
	}
	_, row = m.call(2, "read_deal_card", map[string]any{"deal_id": "thornfield-2027-zzzzzz"}, bearer("read"))
	if n := m.count("SELECT count(*) FROM receipts WHERE id = $1 AND account_ref IS NULL AND requested_account IS NULL", row.ID); n != 1 {
		t.Errorf("the receipt of a read of an unknown deal names an account")
	}
}
