package mcp

import (
	"strings"
	"testing"
)

// The deal tools are the Go gate's own: the Python gateway had neither, so these vectors pin the Go gate's
// answers. Each is replayed on the gate over the fixture, and at the mounted endpoint on PostgreSQL, where no
// deal has the vectors' IDs.

func TestDealToolVectors(t *testing.T) { checkVectors(t, newHarness(t), dealVectors()) }

func dealVectors() []vector {
	invalid := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32602,"message":"invalid tool arguments","data":{"reason":"invalid_arguments"}}}`
	}
	toolError := func(id, tool, code string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":"Error executing tool ` + tool + `: ` + code + `"}],` +
			`"isError":true,"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"gtm-connector","version":"0.2.0"}}}}`
	}
	scope := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":1004,"message":"insufficient_scope: crm:read required","data":{"reason":"insufficient_scope"}}}`
	}
	calls := func(name string) [][2]string {
		return replace(vectorHeaders("tools/call", name), "Authorization", "Bearer "+tokenValue("calls"))
	}
	unknown := "unknown-2027-abcdef"
	return []vector{
		{"list_deals", callBody("201", "list_deals", `{}`, vectorMeta), vectorHeaders("tools/call", "list_deals"), 200, "released", "list_deals ok 200"},
		{"list_deals with no arguments member", []byte(`{"jsonrpc":"2.0","id":202,"method":"tools/call","params":{"name":"list_deals","_meta":` + vectorMeta + `}}`),
			vectorHeaders("tools/call", "list_deals"), 200, "released", "list_deals ok 200"},
		{"list_deals with an argument", callBody("203", "list_deals", `{"account_id":"acc-003"}`, vectorMeta), vectorHeaders("tools/call", "list_deals"),
			400, invalid("203"), "list_deals error 400"},
		{"list_deals without crm:read", callBody("204", "list_deals", `{}`, vectorMeta), calls("list_deals"), 403, scope("204"), "list_deals error 403"},
		{"read_deal_card of an unknown deal", callBody("205", "read_deal_card", `{"deal_id":"`+unknown+`"}`, vectorMeta),
			vectorHeaders("tools/call", "read_deal_card"), 200, toolError("205", "read_deal_card", "deal_not_permitted"), "read_deal_card error 200"},
		{"read_deal_card of an ID of no deal's form", callBody("206", "read_deal_card", `{"deal_id":"acc-003"}`, vectorMeta),
			vectorHeaders("tools/call", "read_deal_card"), 200, toolError("206", "read_deal_card", "deal_not_permitted"), "read_deal_card error 200"},
		{"read_deal_card without deal_id", callBody("207", "read_deal_card", `{}`, vectorMeta), vectorHeaders("tools/call", "read_deal_card"),
			400, invalid("207"), "read_deal_card error 400"},
		{"read_deal_card with a blank deal_id", callBody("208", "read_deal_card", `{"deal_id":"  "}`, vectorMeta), vectorHeaders("tools/call", "read_deal_card"),
			400, invalid("208"), "read_deal_card error 400"},
		{"read_deal_card with a deal_id too long", callBody("209", "read_deal_card", `{"deal_id":"`+strings.Repeat("a", 81)+`"}`, vectorMeta),
			vectorHeaders("tools/call", "read_deal_card"), 400, invalid("209"), "read_deal_card error 400"},
		{"read_deal_card with a null deal_id", callBody("210", "read_deal_card", `{"deal_id":null}`, vectorMeta), vectorHeaders("tools/call", "read_deal_card"),
			400, invalid("210"), "read_deal_card error 400"},
		{"read_deal_card without crm:read", callBody("211", "read_deal_card", `{"deal_id":"`+unknown+`"}`, vectorMeta), calls("read_deal_card"),
			403, scope("211"), "read_deal_card error 403"},
	}
}
