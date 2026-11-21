package demo

import (
	"bytes"
	"context"
	"errors"
	"math/big"
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
