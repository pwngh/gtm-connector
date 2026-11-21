package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// linkCard binds a card to a deal as any writer could, with SQL.
func linkCard(t *testing.T, f *postgrestest.Fixture, card, deal, account string) error {
	t.Helper()
	return sql(t, f, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref) VALUES ($1, $2, $3, $4)`,
		f.Workspace.ID, card, deal, account)
}

// The database keeps a deal's rules for any writer: it binds only what its two reads returned, for its own
// account and by the principal that registered it; it only closes, once; it is never deleted; and a card
// joins only an open deal of its own account, and only one published while that deal was open.
func TestDealRulesHoldForAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	accountRead := postgrestest.Read(t, f, "get_account", postgrestest.Account, "",
		postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account))
	opportunities := postgrestest.Read(t, f, "list_opportunities", postgrestest.Account, "",
		postgrestest.Record("opportunities", "opp-1", "id", "opp-1"))
	register := func(change func(*postgres.DealRegistration)) (postgres.Deal, error) {
		in := postgres.DealRegistration{WorkspaceID: f.Workspace.ID, AccountRef: postgrestest.Account, Word: "invented",
			Year: 2027, OpportunityRef: "opp-1", AccountReadID: accountRead.ID, OpportunitiesReadID: opportunities.ID}
		change(&in)
		return f.Store.RegisterDeal(ctx, in)
	}
	for name, change := range map[string]func(*postgres.DealRegistration){
		"an opportunity the read did not return": func(in *postgres.DealRegistration) { in.OpportunityRef = "opp-2" },
		"a contract the read did not return":     func(in *postgres.DealRegistration) { in.ContractRef = "ctr-1" },
		"the reads in each other's places": func(in *postgres.DealRegistration) {
			in.AccountReadID, in.OpportunitiesReadID = opportunities.ID, accountRead.ID
		},
		"reads made by another principal": func(in *postgres.DealRegistration) { in.PrincipalID = f.Reader.ID },
	} {
		if _, err := register(change); !errors.Is(err, postgres.ErrDealBindings) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A principal without a grant on the account registers nothing, whatever its reads returned.
	if _, err := register(func(in *postgres.DealRegistration) { in.PrincipalID = f.Outsider.ID }); !errors.Is(err,
		postgres.ErrNotPermitted) {
		t.Errorf("an ungranted principal: %v", err)
	}
	deal, err := register(func(*postgres.DealRegistration) {})
	if err != nil {
		t.Fatal(err)
	}
	if deal.Ref != "invented-2027-2e3hke" {
		t.Errorf("the deal is %s", deal.Ref)
	}
	if _, err := register(func(*postgres.DealRegistration) {}); !errors.Is(err, postgres.ErrDealOpen) {
		t.Errorf("a second open deal: %v", err)
	}

	for _, statement := range []string{
		`UPDATE deals SET renews_on = '2028-01-01'`,
		`UPDATE deals SET state = 'closed', closed_at = now(), opportunity_ref = NULL`,
		`DELETE FROM deals`,
		`TRUNCATE deals CASCADE`,
	} {
		if err := sql(t, f, statement); err == nil {
			t.Errorf("%s was allowed", statement)
		}
	}
	// The fixture's card was published before the deal opened, and a card of another account is not the deal's.
	if err := linkCard(t, f, f.Card.ID, deal.Ref, postgrestest.Account); err == nil {
		t.Errorf("a card published before the deal opened joined it")
	}
	other := postgrestest.Publish(t, f, "acc-004", f.Reader.ID, "brief-other")
	if err := linkCard(t, f, other.Card.ID, deal.Ref, "acc-004"); err == nil {
		t.Errorf("a card of another account joined the deal")
	}
	during := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-during")
	if err := linkCard(t, f, during.Card.ID, deal.Ref, postgrestest.Account); err != nil {
		t.Errorf("a card published while the deal was open: %v", err)
	}
	if _, changed, err := f.Store.CloseDeal(ctx, f.Workspace.ID, deal.Ref); err != nil || !changed {
		t.Fatalf("close: %v %v", changed, err)
	}
	if err := sql(t, f, `UPDATE deals SET state = 'open', closed_at = NULL`); err == nil {
		t.Errorf("a closed deal opened again")
	}
	after := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-after")
	if err := linkCard(t, f, after.Card.ID, deal.Ref, postgrestest.Account); err == nil {
		t.Errorf("a card joined a closed deal")
	}
	// The account's next deal of the year takes the next number.
	if next := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented"); next.Ref != "invented-2027-2e3hke-2" {
		t.Errorf("the next deal is %s", next.Ref)
	}
}

// A deal's ID depends on its own account alone: an account whose name starts with the same word gets the
// same ID whether or not the other account has a deal, so a person granted one learns nothing of the other.
func TestADealIDNamesNoOtherAccount(t *testing.T) {
	ids := map[bool]string{}
	for _, beside := range []bool{false, true} {
		f := postgrestest.New(t)
		if err := f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Invented account"); err != nil {
			t.Fatal(err)
		}
		if beside {
			postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
		}
		ids[beside] = postgrestest.RegisterDeal(t, f, "acc-004", "", "invented").Ref
	}
	if ids[false] != "invented-2027-3fag4m" || ids[true] != ids[false] {
		t.Errorf("acc-004's deal is %s alone and %s beside acc-003's", ids[false], ids[true])
	}
}

// A restore runs the deal rules on the rows it loads: a card bound to a deal it was published after the
// deal closed is refused.
func TestARestoreRefusesACardOutsideItsDeal(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	if _, _, err := f.Store.CloseDeal(ctx, f.Workspace.ID, deal.Ref); err != nil {
		t.Fatal(err)
	}
	after := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-after")
	ok(t, sql(t, f, `ALTER TABLE deal_cards DISABLE TRIGGER USER`))
	ok(t, linkCard(t, f, after.Card.ID, deal.Ref, postgrestest.Account))
	ok(t, sql(t, f, `ALTER TABLE deal_cards ENABLE TRIGGER USER`))
	files := dump(t, f)
	fresh, _ := freshStoreURL(t)
	err := fresh.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data))
	if err == nil || !strings.Contains(err.Error(), "outside the time deal") {
		t.Errorf("the restore: %v", err)
	}
}
