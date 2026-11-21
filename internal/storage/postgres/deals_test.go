package postgres_test

import (
	"errors"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// The database keeps a deal's rules for any writer: it binds only what its two reads returned, for its own
// account and by the principal that registered it; it only closes, once; it is never deleted; and a card
// joins only an open deal of its own account.
func TestDealRulesHoldForAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	accountRead := postgrestest.Read(t, f, "get_account", postgrestest.Account, "",
		postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account))
	opportunities := postgrestest.Read(t, f, "list_opportunities", postgrestest.Account, "",
		postgrestest.Record("opportunities", "opp-1", "id", "opp-1"))
	register := func(change func(*postgres.DealRegistration)) error {
		in := postgres.DealRegistration{WorkspaceID: f.Workspace.ID, AccountRef: postgrestest.Account, Word: "invented",
			Year: 2027, OpportunityRef: "opp-1", AccountReadID: accountRead.ID, OpportunitiesReadID: opportunities.ID}
		change(&in)
		_, err := f.Store.RegisterDeal(ctx, in)
		return err
	}
	for name, change := range map[string]func(*postgres.DealRegistration){
		"an opportunity the read did not return": func(in *postgres.DealRegistration) { in.OpportunityRef = "opp-2" },
		"a contract the read did not return":     func(in *postgres.DealRegistration) { in.ContractRef = "ctr-1" },
		"the reads in each other's places": func(in *postgres.DealRegistration) {
			in.AccountReadID, in.OpportunitiesReadID = opportunities.ID, accountRead.ID
		},
		"reads made by another principal": func(in *postgres.DealRegistration) { in.PrincipalID = f.Reader.ID },
	} {
		if err := register(change); !errors.Is(err, postgres.ErrDealBindings) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A principal without a grant on the account registers nothing, whatever its reads returned.
	if err := register(func(in *postgres.DealRegistration) { in.PrincipalID = f.Outsider.ID }); !errors.Is(err,
		postgres.ErrNotPermitted) {
		t.Errorf("an ungranted principal: %v", err)
	}
	if err := register(func(*postgres.DealRegistration) {}); err != nil {
		t.Fatal(err)
	}
	if err := register(func(*postgres.DealRegistration) {}); !errors.Is(err, postgres.ErrDealOpen) {
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
	// A card of another account never joins the deal.
	other := postgrestest.Publish(t, f, "acc-004", f.Reader.ID, "brief-other")
	if err := sql(t, f, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref) VALUES ($1, $2, $3, $4)`,
		f.Workspace.ID, other.Card.ID, "invented-2027", "acc-004"); err == nil {
		t.Errorf("a card of another account joined the deal")
	}
	if _, changed, err := f.Store.CloseDeal(ctx, f.Workspace.ID, "invented-2027"); err != nil || !changed {
		t.Fatalf("close: %v %v", changed, err)
	}
	if err := sql(t, f, `UPDATE deals SET state = 'open', closed_at = NULL`); err == nil {
		t.Errorf("a closed deal opened again")
	}
	if err := sql(t, f, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref) VALUES ($1, $2, $3, $4)`,
		f.Workspace.ID, f.Card.ID, "invented-2027", postgrestest.Account); err == nil {
		t.Errorf("a card joined a closed deal")
	}
	// The account's next deal of the year takes the next number.
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	if deal.Ref != "invented-2027-2" {
		t.Errorf("the next deal is %s", deal.Ref)
	}
}
