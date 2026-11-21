package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// What the review of the mounted endpoint found, each through the surface's handler on PostgreSQL.

var readScopes = []string{"crm:read", "calls:read", "slack:read"}

// A token opens an account only by its principal's live grant. A principal granted one account reads that
// account with every tool and no other with any: a read of a second account is refused under its receipt
// before any source is read, a search and an account-wide list hold the granted account alone, a brief of
// the second account is neither prepared nor released, and a saved brief of the first stops being released
// when the grant is revoked. With a selection and without.
func TestMountedGrantsLimitEveryTool(t *testing.T) {
	const narrow, none = "bearer-of-the-narrow-principal-0000000000000", "bearer-of-a-principal-with-no-grant-0000000"
	for label, selection := range map[string]cards.Selection{"no selection": nil, "a selection of both": {"acc-003": true, "acc-007": true}} {
		t.Run(label, func(t *testing.T) {
			m := mount(t, selection, nil)
			m.fixture()
			granted := m.grantee("acc-003")
			m.issueFor(granted, narrow, readScopes...)
			m.issueFor(m.grantee(), none, readScopes...)
			tools := []string{"get_account", "list_opportunities", "read_call_notes", "read_slack_messages"}
			if selection != nil {
				tools = append(tools, "prepare_renewal_brief")
			}
			for _, tool := range tools {
				for _, bearer := range []string{narrow, none} {
					response, row := m.call(tool, tool, map[string]any{"account_id": "acc-007"}, bearer)
					if toolError(t, response) != "account_not_granted" || row.label() != tool+" error 200" || len(row.Rows) != 0 ||
						len(m.receipts("parent_id = $1", row.ID)) != 0 || strings.Contains(response.Body.String(), "Juniper") {
						t.Errorf("%s of an ungranted account: %d %s, receipt %+v", tool, response.Code, response.Body, row)
					}
				}
			}
			if made := m.book.made(); len(made) != 0 || m.count("SELECT count(*) FROM cards") != 0 {
				t.Fatalf("reads of an ungranted account reached the book: %v", made)
			}
			for _, c := range []struct {
				tool      string
				arguments map[string]any
				bearer    string
				want      []string
			}{
				{"search_accounts", map[string]any{}, narrow, []string{"crm/accounts/acc-003"}},
				{"search_accounts", map[string]any{"query": "juniper"}, narrow, []string{}},
				{"list_opportunities", map[string]any{}, narrow, []string{"crm/opportunities/opp-003"}},
				{"search_accounts", map[string]any{}, none, []string{}},
				{"list_opportunities", map[string]any{}, none, []string{}},
				{"get_account", map[string]any{"account_id": "acc-003"}, narrow, []string{"crm/accounts/acc-003", "crm/contacts/con-003"}},
				{"list_opportunities", map[string]any{"account_id": "acc-003"}, narrow, []string{"crm/opportunities/opp-003"}},
				{"read_call_notes", map[string]any{"account_id": "acc-003"}, narrow, []string{"calls/call_notes/note-003", "calls/call_notes/note-003b"}},
				{"read_slack_messages", map[string]any{"account_id": "acc-003"}, narrow, []string{"slack/messages/msg-003"}},
			} {
				response, row := m.call(c.tool, c.tool, c.arguments, c.bearer)
				if found := records(t, response); !slices.Equal(found, c.want) || !slices.Equal(row.Rows, c.want) {
					t.Errorf("%s %v: released %v, receipt %v", c.tool, c.arguments, found, row.Rows)
				}
			}
			if selection == nil {
				return
			}
			// A brief of the second account, prepared by a principal that is granted it, is not released to
			// one that is not.
			response, _ := m.call("wide", "prepare_renewal_brief", map[string]any{"account_id": "acc-007"}, bearer("read"))
			others := strings.Split(records(t, response)[0], "/")[1]
			response, row := m.call("others", "read_renewal_brief", map[string]any{"brief_id": others}, narrow)
			if toolError(t, response) != "account_not_granted" || len(row.Rows) != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
				t.Fatalf("a brief of an ungranted account: %d %s", response.Code, response.Body)
			}
			// Its own brief is released while the grant stands, and not after it is revoked.
			response, _ = m.call("own", "prepare_renewal_brief", map[string]any{"account_id": "acc-003"}, narrow)
			own := records(t, response)
			response, _ = m.call("again", "read_renewal_brief", map[string]any{"brief_id": strings.Split(own[0], "/")[1]}, narrow)
			if again := records(t, response); !reflect.DeepEqual(again, own) {
				t.Fatalf("its own brief read again: %v", again)
			}
			must(t, m.store.RevokeAccountGrant(ctx, m.workspace.ID, granted.ID, "acc-003"))
			sources := len(m.book.made())
			for _, tool := range []string{"read_renewal_brief", "get_account", "prepare_renewal_brief"} {
				arguments := map[string]any{"account_id": "acc-003"}
				if tool == "read_renewal_brief" {
					arguments = map[string]any{"brief_id": strings.Split(own[0], "/")[1]}
				}
				response, row := m.call("revoked", tool, arguments, narrow)
				if toolError(t, response) != "account_not_granted" || row.label() != tool+" error 200" || len(row.Rows) != 0 ||
					strings.Contains(response.Body.String(), "Thornfield") {
					t.Errorf("%s after the grant was revoked: %d %s", tool, response.Code, response.Body)
				}
			}
			if len(m.book.made()) != sources {
				t.Fatalf("a revoked grant read %v", m.book.made()[sources:])
			}
		})
	}
}

// hooked is a store that runs a hook as a saved brief is loaded, which is after the call was admitted.
type hooked struct {
	mcptools.Storage
	loaded func()
}

func (h hooked) CardByBrief(ctx context.Context, workspaceID, briefID string) (postgres.Card, error) {
	h.loaded()
	return h.Storage.CardByBrief(ctx, workspaceID, briefID)
}

// A brief goes by the scopes its caller's bearer carries when each read is made and when the brief is
// released, not by those the call was admitted with. A scope taken away part way through a preparation is
// refused from the next read on, and one taken away after a reread was admitted keeps the brief from being
// released.
func TestMountedABriefGoesByTheScopesTheBearerCarriesNow(t *testing.T) {
	account := map[string]any{"account_id": "acc-003"}
	m := mount(t, cards.Selection{"acc-003": true}, nil)
	const narrowed = "bearer-that-loses-its-scopes-000000000000000000"
	m.issue(narrowed, readScopes...)
	m.book.before = func(tool string) {
		if tool == "list_opportunities" {
			m.rescope(narrowed, "crm:read")
		}
	}
	response, outer := m.call(1, "prepare_renewal_brief", account, narrowed)
	records(t, response)
	var inside []string
	for _, child := range m.receipts("parent_id = $1", outer.ID) {
		inside = append(inside, child.label())
	}
	briefed := at(decode(t, response.Body.Bytes()), "result", "structuredContent", "brief")
	if !reflect.DeepEqual(inside, []string{"get_account ok 200", "list_opportunities ok 200", "read_call_notes error 403",
		"read_slack_messages error 403"}) || !reflect.DeepEqual(m.book.made(), []string{"get_account", "list_opportunities"}) ||
		at(briefed, "notes_status") != "denied" || at(briefed, "slack_status") != "denied" ||
		strings.Contains(response.Body.String(), "note-003") {
		t.Fatalf("a brief whose bearer lost two scopes after its second read: reads %v, book %v, statuses %v %v", inside,
			m.book.made(), at(briefed, "notes_status"), at(briefed, "slack_status"))
	}

	// A brief with call notes, read again by a bearer that loses calls:read between admission and release.
	m = mount(t, cards.Selection{"acc-003": true}, nil)
	m.fixture()
	m.issue(narrowed, readScopes...)
	response, _ = m.call(1, "prepare_renewal_brief", account, bearer("read"))
	brief := strings.Split(records(t, response)[0], "/")[1]
	lose := func() {}
	m.serve(m.selection, func(c *mcptools.Config) { c.Store = hooked{c.Store, func() { lose() }} })
	response, _ = m.call(2, "read_renewal_brief", map[string]any{"brief_id": brief}, narrowed)
	records(t, response)
	lose = func() { m.rescope(narrowed, "crm:read", "slack:read") }
	response, row := m.call(3, "read_renewal_brief", map[string]any{"brief_id": brief}, narrowed)
	if toolError(t, response) != "brief_scope_not_covered" || len(row.Rows) != 0 || strings.Contains(response.Body.String(), "DEAL CARD") {
		t.Fatalf("a brief released past a scope its bearer lost: %d %s", response.Code, response.Body)
	}
	// A bearer that is revoked in that same moment is not released to either.
	m.issue("bearer-revoked-before-release-00000000000000000", readScopes...)
	lose = func() {
		must(t, m.sql("UPDATE machine_tokens SET revoked_at = now() WHERE token_sha256 = sha256($1)", []byte("bearer-revoked-before-release-00000000000000000")))
	}
	response, _ = m.call(4, "read_renewal_brief", map[string]any{"brief_id": brief}, "bearer-revoked-before-release-00000000000000000")
	if toolError(t, response) != "invalid_token" || strings.Contains(response.Body.String(), "DEAL CARD") {
		t.Fatalf("a brief released to a bearer revoked before release: %d %s", response.Code, response.Body)
	}
}

// broken is a body that fails part way through, as a connection that drops does.
type broken struct{ sent bool }

func (b *broken) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("the connection dropped")
	}
	b.sent = true
	return copy(p, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_`), nil
}

// A call the store cannot record as it was given, or that cannot be read at all, is still refused as a bad
// request under one finished receipt, with the store healthy: arguments with a NUL character, which
// PostgreSQL holds in no JSON text, a body that fails as it is read, and a body nested too deep to parse.
func TestMountedEveryRefusedCallLeavesItsReceipt(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	read := bearer("read")
	note := `{"unrecorded": "the arguments hold a NUL character"}`
	for label, c := range map[string]struct{ tool, arguments string }{
		"a name of a known tool":   {"get_account", `{"account_id":"acc-003","a\u0000b":"x"}`},
		"the only name":            {"get_account", `{"\u0000":"acc-003"}`},
		"a value of a known tool":  {"search_accounts", `{"query":"a\u0000b"}`},
		"the account":              {"get_account", `{"account_id":"acc-003\u0000"}`},
		"an unknown tool, nested":  {"nope", `{"list":[{"deep":{"a\u0000":1}}]}`},
		"an unknown tool, a value": {"nope", `{"list":[{"deep":["\u0000"]}]}`},
	} {
		before := len(m.outer())
		body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + c.tool + `","arguments":` + c.arguments + `,"_meta":` + requestMeta + `}}`
		response := m.post([]byte(body), requestHeaders("tools/call", c.tool, read))
		rows := m.outer()[before:]
		if response.Code != 400 || refusal(t, response) != "invalid_arguments" || len(rows) != 1 ||
			rows[0].label() != c.tool+" error 400" || !sameJSON(t, []byte(rows[0].Arguments), []byte(note)) {
			t.Errorf("%s: %d %s, receipts %+v", label, response.Code, response.Body, rows)
		}
	}
	// A body that fails as it is read: a request its header marks as a call keeps its receipt.
	read1 := len(m.book.made())
	before := len(m.outer())
	dropped := m.sendFrom(ctx, http.MethodPost, "/mcp", mountedHost, &broken{}, requestHeaders("tools/call", "get_account", read))
	rows := m.outer()[before:]
	if dropped.Code != 400 || refusal(t, dropped) != "invalid_json" || len(rows) != 1 || rows[0].label() != "get_account error 400" ||
		!strings.Contains(rows[0].Arguments, "unparsed_sha256") || rows[0].TokenID != receipts.Fingerprint(read) {
		t.Errorf("a marked call whose body fails: %d %s, receipts %+v", dropped.Code, dropped.Body, rows)
	}
	if unmarked := m.sendFrom(ctx, http.MethodPost, "/mcp", mountedHost, &broken{}, requestHeaders("tools/list", "", read)); unmarked.Code != 400 ||
		len(m.outer()) != before+1 {
		t.Errorf("a body that fails and is marked as no call: %d, %d receipts", unmarked.Code, len(m.outer())-before-1)
	}

	// A call nested past the limit, with no header to say it is a call, keeps its receipt under its tool.
	deep := strings.Repeat(`{"a":`, strictjson.MaxDepth) + "1" + strings.Repeat("}", strictjson.MaxDepth)
	before = len(m.outer())
	nested := m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_call_notes","arguments":{"account_id":"acc-003","x":`+deep+`}}}`),
		[][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + read}})
	rows = m.outer()[before:]
	if nested.Code != 400 || refusal(t, nested) != "invalid_json" || len(rows) != 1 || rows[0].label() != "read_call_notes error 400" ||
		!strings.Contains(rows[0].Arguments, "unparsed_sha256") {
		t.Errorf("a call nested past the limit: %d %s, receipts %+v", nested.Code, nested.Body, rows)
	}
	if listed := m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":`+deep+`}}`),
		[][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + read}}); listed.Code != 400 || len(m.outer()) != before+1 {
		t.Errorf("a listing nested past the limit: %d, %d receipts", listed.Code, len(m.outer())-before-1)
	}
	if len(m.book.made()) != read1 {
		t.Fatalf("a refused call read %v", m.book.made()[read1:])
	}
}

// publishing is a receipt store that runs a hook as a publication starts.
type publishing struct {
	receipts.Store
	starts func()
}

func (p publishing) Publish(ctx context.Context, publication receipts.Publication) (receipts.Card, error) {
	p.starts()
	return p.Store.Publish(ctx, publication)
}

// A caller who leaves while its call runs is answered nothing, and its receipt is finished all the same,
// never left pending: as an error, with nothing read after it left and nothing published, unless the
// publication of its brief had already started, which then runs to its end and seals the receipt.
func TestMountedACallerWhoLeaves(t *testing.T) {
	account := map[string]any{"account_id": "acc-003"}
	leave := func(m *mounted, during string) (func(), context.Context) {
		caller, cancel := context.WithCancel(ctx)
		m.book.before = func(tool string) {
			if tool == during {
				cancel()
			}
		}
		return cancel, caller
	}

	// A direct read whose caller leaves as the source is read.
	m := mount(t, cards.Selection{"acc-003": true}, nil)
	m.fixture()
	cancel, caller := leave(m, "get_account")
	gone := m.sendFrom(caller, http.MethodPost, "/mcp", mountedHost, strings.NewReader(string(callBody(1, "get_account", account))),
		requestHeaders("tools/call", "get_account", bearer("read")))
	cancel()
	rows := m.outer()
	if gone.Code != 503 || strings.Contains(gone.Body.String(), "Thornfield") || len(rows) != 1 || rows[0].label() != "get_account error 503" ||
		len(rows[0].Rows) != 0 || m.count("SELECT count(*) FROM receipt_records") != 0 {
		t.Fatalf("a read whose caller left: %d %s, receipts %+v", gone.Code, gone.Body, rows)
	}

	// A brief whose caller leaves during its second read: no later read, no card.
	cancel, caller = leave(m, "list_opportunities")
	gone = m.sendFrom(caller, http.MethodPost, "/mcp", mountedHost, strings.NewReader(string(callBody(2, "prepare_renewal_brief", account))),
		requestHeaders("tools/call", "prepare_renewal_brief", bearer("read")))
	cancel()
	outer := m.outer()[1]
	var inside []string
	for _, child := range m.receipts("parent_id = $1", outer.ID) {
		inside = append(inside, child.label())
	}
	if gone.Code != 503 || outer.Status != "error" || len(outer.Rows) != 0 || m.count("SELECT count(*) FROM cards") != 0 ||
		!reflect.DeepEqual(inside, []string{"get_account ok 200", "list_opportunities error 200"}) ||
		!reflect.DeepEqual(m.book.made(), []string{"get_account", "get_account", "list_opportunities"}) {
		t.Fatalf("a brief whose caller left: %d %s, outer %+v, reads %v, book %v", gone.Code, gone.Body, outer, inside, m.book.made())
	}

	// A brief whose caller leaves as its publication starts: the publication runs to its end.
	m = mount(t, cards.Selection{"acc-003": true}, nil)
	m.fixture()
	caller, cancel = context.WithCancel(ctx)
	m.serve(m.selection, func(c *mcptools.Config) { c.Receipts = publishing{c.Receipts, cancel} })
	gone = m.sendFrom(caller, http.MethodPost, "/mcp", mountedHost, strings.NewReader(string(callBody(3, "prepare_renewal_brief", account))),
		requestHeaders("tools/call", "prepare_renewal_brief", bearer("read")))
	outer = m.outer()[0]
	if gone.Code != 503 || strings.Contains(gone.Body.String(), "DEAL CARD") || outer.label() != "prepare_renewal_brief ok 200" ||
		len(outer.Rows) != 1 || m.count("SELECT count(*) FROM cards WHERE seal_receipt_id = $1", outer.ID) != 1 ||
		len(m.receipts("parent_id = $1 AND status = 'ok'", outer.ID)) != 4 {
		t.Fatalf("a brief whose caller left as it was published: %d %s, outer %+v", gone.Code, gone.Body, outer)
	}
	// The brief it sealed is there for the caller's next call.
	response, _ := m.call(4, "read_renewal_brief", map[string]any{"brief_id": strings.Split(outer.Rows[0], "/")[1]}, bearer("read"))
	if again := records(t, response); !reflect.DeepEqual(again, outer.Rows) {
		t.Fatalf("the brief sealed as its caller left: %v", again)
	}
}

// A request that repeats the credential it presents, in its id, its tool name, its arguments or its brief
// id, is refused in fixed words that do not repeat it, and its receipt holds none of them. Without this the
// id came back in the reply and went, with the arguments, into the receipt.
func TestMountedARequestThatRepeatsItsCredential(t *testing.T) {
	m := mount(t, nil, nil)
	m.fixture()
	const unknown = "never-issued-bearer-that-is-repeated-0000000"
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"the request repeats the credential it presents","data":{"reason":"credential_in_request"}}}`
	for _, secret := range []string{bearer("read"), unknown} {
		quoted := `"` + secret + `"`
		meta := requestMeta[:len(requestMeta)-1]
		for label, c := range map[string]struct{ id, name, arguments, meta string }{
			"the id":                    {quoted, `"get_account"`, `{"account_id":"acc-003"}`, requestMeta},
			"inside the id":             {`"id-` + secret + `-1"`, `"get_account"`, `{"account_id":"acc-003"}`, requestMeta},
			"an argument":               {`1`, `"get_account"`, `{"account_id":` + quoted + `}`, requestMeta},
			"inside an argument":        {`2`, `"search_accounts"`, `{"query":"x ` + secret + `"}`, requestMeta},
			"an argument's name":        {`3`, `"get_account"`, `{` + quoted + `:"acc-003"}`, requestMeta},
			"nested in an argument":     {`4`, `"nope"`, `{"list":[1,{"deep":[` + quoted + `]}]}`, requestMeta},
			"written with escapes":      {`5`, `"get_account"`, `{"account_id":"\u00` + fmt.Sprintf("%02x", secret[0]) + secret[1:] + `"}`, requestMeta},
			"the tool name":             {`6`, quoted, `{}`, requestMeta},
			"the brief id of the call":  {`7`, `"get_account"`, `{"account_id":"acc-003"}`, meta + `,"gtm.demo/briefId":` + quoted + `}`},
			"the id and every argument": {quoted, `"get_account"`, `{"account_id":` + quoted + `,` + quoted + `:1}`, requestMeta},
		} {
			before := len(m.outer())
			body := `{"jsonrpc":"2.0","id":` + c.id + `,"method":"tools/call","params":{"name":` + c.name + `,"arguments":` + c.arguments + `,"_meta":` + c.meta + `}}`
			name := strings.Trim(c.name, `"`)
			response := m.post([]byte(body), requestHeaders("tools/call", name, secret))
			rows := m.outer()[before:]
			if response.Code != 400 || !sameJSON(t, response.Body.Bytes(), []byte(want)) {
				t.Errorf("%s: %d %.300s", label, response.Code, response.Body)
			}
			if len(rows) != 1 || rows[0].Status != "error" || rows[0].HTTP != 400 || rows[0].RequestID != `"<invalid>"` ||
				!sameJSON(t, []byte(rows[0].Arguments), []byte(`{"unrecorded": "the request repeated its credential"}`)) || rows[0].Brief != nil ||
				rows[0].TokenID != receipts.Fingerprint(secret) {
				t.Errorf("%s: receipts %+v", label, rows)
			}
		}
		// A request that is no call is refused the same way, with no receipt.
		before := len(m.outer())
		listing := m.post([]byte(`{"jsonrpc":"2.0","id":`+quoted+`,"method":"tools/list","params":{"_meta":`+requestMeta+`}}`),
			requestHeaders("tools/list", "", secret))
		if listing.Code != 400 || !sameJSON(t, listing.Body.Bytes(), []byte(want)) || len(m.outer()) != before {
			t.Errorf("a listing whose id is the bearer: %d %.300s", listing.Code, listing.Body)
		}
		// A body no parser reads, under a header that names the bearer as the tool.
		unread := m.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`), requestHeaders("tools/call", secret, secret))
		if rows := m.outer()[before:]; unread.Code != 400 || len(rows) != 1 || rows[0].Tool != "<invalid>" {
			t.Errorf("an unread body under the bearer as its tool name: %d, receipts %+v", unread.Code, rows)
		}
	}
	kept := m.dump(`SELECT coalesce(string_agg(r::text, E'\n'), '') FROM receipts r`)
	answered := strings.Join(m.said, "\n")
	for _, secret := range []string{bearer("read"), unknown} {
		if strings.Contains(kept, secret) || strings.Contains(answered, secret) {
			t.Errorf("the credential %.12s... was kept (%v) or answered (%v)", secret, strings.Contains(kept, secret), strings.Contains(answered, secret))
		}
	}
	if made := m.book.made(); len(made) != 0 || len(m.gateway.Windows()) != 0 {
		t.Fatalf("a request that repeats its credential read %v and was charged %v", made, m.gateway.Windows())
	}
	// A request that does not repeat it is served as before.
	response, _ := m.call(9, "get_account", map[string]any{"account_id": "acc-003"}, bearer("read"))
	records(t, response)
}
