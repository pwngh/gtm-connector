package salesforce

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// An account's Website is read with it, as the record's website, and is null where the org holds none.
func TestGetAccountReadsTheWebsite(t *testing.T) {
	for _, c := range []struct {
		name    string
		row     string
		website any
	}{
		{"a website", `"Website": "https://www.buyer.example/about"`, "https://www.buyer.example/about"},
		{"none", `"Website": null`, nil},
		{"a recorded row without the field", ``, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			fields := `"Id": "` + account + `", "Name": "Invented Account", "Industry": null, "AccountNumber": null`
			if c.row != "" {
				fields += ", " + c.row
			}
			accountID := org.Add(object(t, `{"Account": [{`+fields+`}], "Contact": [], "Opportunity": [], "Contract": []}`))
			reader := signIn(t, org, sf, "production")
			_, records, err := reader.Read(context.Background(), "get_account", accountID)
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			website, present := records[0].Get("website")
			if !present || website != c.website {
				t.Fatalf("website %v (%v)", website, present)
			}
		})
	}
	// A Website of another type is refused.
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	accountID := org.Add(object(t, `{"Account": [{"Id": "`+account+`", "Name": "Invented Account", "Industry": null,
		"AccountNumber": null, "Website": 7}], "Contact": [], "Opportunity": [], "Contract": []}`))
	reader := signIn(t, org, sf, "production")
	if _, _, err := reader.Read(context.Background(), "get_account", accountID); codeOf(err) != invalidResponse {
		t.Fatal(err)
	}
}

// Every Standard user is read, over as many pages as the org sends, up to MaxUsers; an org with more reads
// none and says so with ErrTooManyUsers, after its first page.
func TestListUsersPagesThroughEveryUser(t *testing.T) {
	for _, c := range []struct {
		name  string
		users int
		page  int
		want  error
	}{
		{"4,500 users in pages of 2,000", 4500, 0, nil},
		{"4,500 users in pages of 200", 4500, 200, nil},
		{"MaxUsers users", MaxUsers, 0, nil},
		{"one more than MaxUsers", MaxUsers + 1, 0, ErrTooManyUsers},
	} {
		t.Run(c.name, func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			rows := make([]*strictjson.Object, c.users)
			for i := range rows {
				rows[i] = user(fmt.Sprintf("005%015d", i), fmt.Sprintf("Seller %d", i), nil, true, "Standard")
			}
			org.AddUsers(rows...)
			if c.page > 0 {
				org.PageSize("User", "", c.page)
			}
			reader := signIn(t, org, sf, "production")
			asked := len(org.Requests)
			found, err := reader.ListUsers(context.Background())
			if !errors.Is(err, c.want) || c.want == nil && err != nil {
				t.Fatal(err)
			}
			if c.want == nil && len(found) != c.users || c.want != nil && found != nil {
				t.Fatalf("%d users", len(found))
			}
			if c.want != nil && len(org.Requests)-asked != 1 {
				t.Fatalf("the guard read %d pages", len(org.Requests)-asked)
			}
		})
	}
}
