package demo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

func book(t *testing.T) *Store {
	t.Helper()
	s, err := Load(oracletest.Read(t, "cards/demo_book.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTheBuiltInBookIsTheOracles(t *testing.T) {
	if !bytes.Equal(Book, oracletest.Read(t, "cards/demo_book.json")) {
		t.Fatal("book.json differs from testdata/oracle/cards/demo_book.json; copy it again")
	}
	s, err := Seeded()
	if err != nil {
		t.Fatal(err)
	}
	accounts := s.Accounts()
	if len(accounts) != 30 || accounts[2] != (Account{"acc-003", "Thornfield Wren LLP"}) {
		t.Fatalf("accounts: %v", accounts)
	}
}

func refs(records []*strictjson.Object) []string {
	var out []string
	for _, r := range records {
		ref, _ := r.Get("record_ref")
		out = append(out, ref.(string))
	}
	return out
}

func TestReads(t *testing.T) {
	s, ctx := book(t), context.Background()
	system, records, err := s.Read(ctx, "get_account", "acc-003")
	if err != nil || system != "crm" {
		t.Fatal(system, err)
	}
	if got := refs(records); len(got) != 2 || got[0] != "crm/accounts/acc-003" || got[1] != "crm/contacts/con-003" {
		t.Fatalf("get_account: %v", got)
	}
	account := records[0]
	if want := []string{"id", "name", "industry", "renewal_date", "arr_cents", "currency", "risk_status",
		"risk_reason", "record_ref"}; len(account.Keys) != len(want) || account.Keys[8] != want[8] || account.Keys[4] != want[4] {
		t.Fatalf("columns: %v", account.Keys)
	}
	if arr, _ := account.Get("arr_cents"); arr.(*big.Int).Sign() <= 0 {
		t.Fatalf("arr_cents: %v", arr)
	}
	_, notes, _ := s.Read(ctx, "read_call_notes", "acc-003")
	if got := refs(notes); len(got) != 2 || got[0] != "calls/call_notes/note-003" || got[1] != "calls/call_notes/note-003b" {
		t.Fatalf("call notes by ID: %v", got)
	}
	if _, _, err := s.Read(ctx, "get_account", "acc-999"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("an unknown account: %v", err)
	}
	if _, messages, err := s.Read(ctx, "read_slack_messages", "acc-005"); err != nil || len(messages) != 0 {
		t.Fatalf("an account with no message: %v %v", messages, err)
	}
}

// The searches and lists below, with what store.search_accounts and store.list_opportunities answered to
// each on the seeded demo CRM: names matched and ordered without ASCII case, then by ID, a selection applied
// before the order.
func TestSearchAndOpportunitiesAsThePythonStore(t *testing.T) {
	s := book(t)
	ids := func(records []*strictjson.Object) string {
		out := ""
		for _, r := range records {
			id, _ := r.Get("id")
			out += " " + id.(string)[4:]
		}
		return out
	}
	two := []string{"acc-003", "acc-001"}
	for _, c := range []struct {
		query    string
		accounts []string
		want     string
	}{
		{"", nil, " 001 017 028 022 011 024 005 018 002 025 021 009 006 029 007 015 019 014 008 027 010 016 013 030 012 020 023 003 004 026"},
		{"a", nil, " 001 017 028 022 005 018 025 021 009 006 029 007 015 019 014 008 027 010 012 020 023 004 026"},
		{"VALE", nil, " 001"}, {"llp", nil, " 003"}, {"Alder Vale Packaging", nil, " 001"}, {"zz", nil, ""},
		{"", two, " 001 003"}, {"thorn", two, " 003"}, {"thorn", []string{"acc-001"}, ""},
	} {
		records, more := s.Search(c.query, c.accounts)
		if got := ids(records); got != c.want || more {
			t.Errorf("search %q in %v:%s (more %v)", c.query, c.accounts, got, more)
		}
	}
	found, _ := s.Search("Alder Vale Packaging", nil)
	if text, _ := strictjson.Dumps(found[0], strictjson.Prepared); text != `{"arr_cents":2050000,"currency":"USD",`+
		`"id":"acc-001","industry":"Manufacturing","name":"Alder Vale Packaging","record_ref":"crm/accounts/acc-001",`+
		`"renewal_date":"2027-02-01","risk_reason":"","risk_status":"healthy"}` {
		t.Errorf("a found account: %s", text)
	}
	if all := s.Opportunities(nil); len(all) != 30 || ids(all[:3]) != " 001 002 003" || ids(all[29:]) != " 030" {
		t.Errorf("every opportunity:%s", ids(all))
	}
	if got := ids(s.Opportunities(two)); got != " 001 003" {
		t.Errorf("the opportunities of two accounts:%s", got)
	}
	// One more than a search returns: the first hundred by name, and the word that more matched.
	for i := 0; i < SearchRows-29; i++ {
		row := []any{fmt.Sprintf("acc-%03d", 900-i), fmt.Sprintf("Zeta %03d", i), "", "2027-03-01", nil, nil, "healthy", ""}
		if err := s.Insert("crm", "accounts", row); err != nil {
			t.Fatal(err)
		}
	}
	records, more := s.Search("", nil)
	if last, _ := records[len(records)-1].Get("name"); len(records) != SearchRows || !more || last != "Zeta 069" {
		t.Errorf("a search past the bound: %d rows, more %v, last %v", len(records), more, last)
	}
}

func TestInsert(t *testing.T) {
	s := book(t)
	row := []any{"msg-900-001", "acc-003", "acct-x", "Quinn Moss", "2027-01-14T16:00:00Z", "Later.", big.NewInt(0)}
	if err := s.Insert("slack", "messages", row); err != nil {
		t.Fatal(err)
	}
	_, messages, _ := s.Read(context.Background(), "read_slack_messages", "acc-003")
	if got := refs(messages); len(got) != 2 || got[0] != "slack/messages/msg-003" || got[1] != "slack/messages/msg-900-001" {
		t.Fatalf("messages by ID: %v", got)
	}
	if err := s.Insert("slack", "messages", row); err == nil {
		t.Fatal("a second row with one ID was inserted")
	}
	if err := s.Insert("slack", "messages", row[:3]); err == nil {
		t.Fatal("a short row was inserted")
	}
	if err := s.Insert("crm", "accounts", []any{"acc-900", "", "", "", 1.5, nil, "", ""}); err == nil {
		t.Fatal("a float was inserted")
	}
	for _, id := range []any{big.NewInt(900), nil} {
		if err := s.Insert("crm", "accounts", []any{id, "", "", "", nil, nil, "", ""}); err == nil {
			t.Fatalf("an account with the ID %v was inserted", id)
		}
	}
}

// A dump nested past the reader's limit is refused, as a dump that is not JSON is.
func TestLoadRefusesADumpNestedTooDeep(t *testing.T) {
	levels := strictjson.MaxDepth + 1
	deep := strings.Repeat(`{"crm":`, levels) + "1" + strings.Repeat("}", levels)
	if s, err := Load([]byte(deep)); s != nil || !errors.Is(err, strictjson.ErrTooDeep) {
		t.Fatalf("got %v, %v", s, err)
	}
}
