package mcp

import (
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A body is refused for its encoding before its depth is counted, and for its depth before anything else
// about it is looked at. So a tool call that is not UTF-8 and is nested past the limit is no JSON text at
// all: with no header to say it is a call it leaves no receipt, on either transport, as before the depth
// was counted in a pass of its own. A well-encoded call nested past the limit keeps its receipt under its
// tool's name, whatever fault sits in the values the scan steps over.
func TestACallThatIsNotUTF8IsNoCallHoweverDeep(t *testing.T) {
	deep := strings.Repeat(`{"a":`, strictjson.MaxDepth+1) + "1" + strings.Repeat("}", strictjson.MaxDepth+1)
	notUTF8 := `{"account_id":"acc-` + "\xff" + `","extra":` + deep + `}`
	faulty := `{"account_id":nope,"extra":` + deep + `}`
	request := func(arguments string) string {
		return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_account","arguments":` + arguments + `}}`
	}
	t.Run("HTTP", func(t *testing.T) {
		h := newHarness(t)
		bearer := [][2]string{{"Authorization", "Bearer unused"}}
		response := h.post([]byte(request(notUTF8)), bearer)
		if reason, _ := reasonOf(t, response); response.Code != 400 || reason != "invalid_json" {
			t.Fatalf("not UTF-8: %d %s", response.Code, response.Body)
		}
		if rows := h.receipts.Read(); len(rows) != 0 || len(h.fx.ran()) != 0 {
			t.Fatalf("a body that is not UTF-8 left receipts %+v", rows)
		}
		// The header says it is a call, so it keeps a receipt, as any refused body does.
		marked := h.post([]byte(request(notUTF8)), append(bearer, [2]string{"Mcp-Method", "tools/call"},
			[2]string{"Mcp-Name", "get_account"}))
		if rows := h.receipts.Read(); marked.Code != 400 || len(rows) != 1 || rows[0].Tool != "get_account" ||
			*rows[0].HTTPStatus != 400 {
			t.Fatalf("marked: %d, receipts %+v", marked.Code, rows)
		}
		response = h.post([]byte(request(faulty)), bearer)
		rows := h.receipts.Read()
		if reason, _ := reasonOf(t, response); response.Code != 400 || reason != "invalid_json" || len(rows) != 2 ||
			rows[1].Tool != "get_account" || rows[1].Status != "error" || *rows[1].HTTPStatus != 400 {
			t.Fatalf("a fault before the deep value: %d %s, receipts %+v", response.Code, response.Body, rows)
		}
	})
	t.Run("stdio", func(t *testing.T) {
		h := newStdio(t, []string{"crm:read"}, nil)
		h.send(legacyStart, legacyReady)
		h.answer(1)
		h.send(call(2, "get_account", notUTF8))
		if refused := h.next(); path(refused, "error", "data", "reason") != "invalid_json" || refused["id"] != nil {
			t.Fatalf("not UTF-8: %.200v", refused)
		}
		if rows := h.receipts.Read(); len(rows) != 0 || len(h.fx.ran()) != 0 {
			t.Fatalf("a line that is not UTF-8 left receipts %+v", rows)
		}
		h.send(call(3, "get_account", faulty))
		if refused := h.next(); path(refused, "error", "data", "reason") != "invalid_json" {
			t.Fatalf("a fault before the deep value: %.200v", refused)
		}
		rows := h.receipts.Read()
		if len(rows) != 1 || rows[0].Tool != "get_account" || rows[0].Status != "error" || *rows[0].HTTPStatus != 400 {
			t.Fatalf("a fault before the deep value: receipts %+v", rows)
		}
		h.send(call(4, "get_account", `{"account_id":"acc-001"}`))
		if served := h.answer(4); path(served, "result", "isError") != false {
			t.Fatalf("after the refused lines: %v", served)
		}
	})
}
