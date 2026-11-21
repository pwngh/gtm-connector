package cards

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// usersOverLimit is a Salesforce org whose users are more than a card reads.
type usersOverLimit struct{ besideDemo }

func (s usersOverLimit) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "list_users" {
		return "crm", nil, ErrNotSelected
	}
	return s.besideDemo.Read(ctx, tool, accountID)
}

// An org with more users than a card reads gives a card without them: its users read finishes as an error
// with no rows, and the card says the users were not read.
func TestACardIsMadeWithoutUsersOverTheLimit(t *testing.T) {
	ctx := context.Background()
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	reader, err := salesforce.FromSF(ctx, salesforcetest.Alias, salesforce.Options{Environment: "production",
		Runner: sf, Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := reader.PrepareConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var row fixtureRow
	if err := json.Unmarshal(oracletest.Lines(t, "cards/books_salesforce.jsonl.gz")[0], &row); err != nil {
		t.Fatal(err)
	}
	rows, _ := load(t, row.Input).(*strictjson.Object).Get("org_rows")
	org.Add(rows.(*strictjson.Object))
	sc := newScope("users-over-limit")
	m, store := sc.maker(usersOverLimit{besideDemo{reader, demoBook(t)}}, salesforceSources(identity))
	sealed, err := m.Brief(ctx, Brief{Principal: owner(sc.token(accessToken), "crm:read", "calls:read", "slack:read"),
		OperationID: "brief", AccountID: row.AccountID, AsOf: DemoAsOf})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sealed.Text, "CRM: the org's users were not read") {
		t.Errorf("the card does not say its users were not read:\n%s", sealed.Text)
	}
	card := load(t, sealed.Payload).(*strictjson.Object)
	snapshot := get(card, "evidence").(*strictjson.Object)
	if people := get(snapshot, "people").(*strictjson.Object); get(people, "users") != evidence.UsersNotRead {
		t.Errorf("the evidence's users read is %v", get(people, "users"))
	}
	for _, value := range get(snapshot, "receipts").([]any) {
		receipt := value.(*strictjson.Object)
		if get(receipt, "tool") == "list_users" && (get(receipt, "status") != "error" || len(get(receipt, "rows").([]any)) != 0) {
			t.Errorf("the users read finished as %v", receipt)
		}
	}
	if _, err := VerifyCard(ctx, sealed.Payload, sealed.BriefID, StoreLog{Store: store, WorkspaceID: testWorkspace},
		salesforceSources(identity)); err != nil {
		t.Errorf("the card does not verify: %v", err)
	}
}
