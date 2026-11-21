package salesforce

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// A login is the org and the user it reads as. sf org display names the user, and the org says which user
// a token reads as: a token of another user of the same org is refused, from the first two sf commands and
// after any refresh, and nothing is read with it.
func TestALoginReadsAsOneUser(t *testing.T) {
	const rotated = "invented-rotated-token"
	other := [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"}
	t.Run("the token of the first login is another user's", func(t *testing.T) {
		org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
		org.Users[salesforcetest.Token] = other
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production", Runner: sf,
			Transport: org})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != authRequired || len(org.Requests) != 2 {
			t.Fatalf("%v after %v", err, org.Requests)
		}
	})
	for name, change := range map[string]func(*salesforcetest.Org, *salesforcetest.SF){
		"sf now names another user":           func(_ *salesforcetest.Org, sf *salesforcetest.SF) { sf.Username = other[1] },
		"the new token reads as another user": func(org *salesforcetest.Org, _ *salesforcetest.SF) { org.Users[rotated] = other },
	} {
		t.Run(name, func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			accountID := org.Add(sized(t, 1, 1))
			reader := signIn(t, org, sf, "production")
			org.Requests = nil
			org.Token, sf.Token = rotated, rotated
			change(org, sf)
			if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
				t.Fatal(err)
			}
			for _, request := range org.Requests {
				if strings.Contains(request, "FROM Contact") {
					t.Fatalf("the other user's token read records: %v", org.Requests)
				}
			}
			if reader.accessToken != salesforcetest.Token {
				t.Fatal("the other user's token was kept")
			}
		})
	}
	// The pin holds the user, and the card's binding of the org stays what the Python package recorded.
	one := Identity{salesforcetest.Instance, salesforcetest.OrganizationID, "production", salesforcetest.UserID}
	two := one
	two.UserID = other[0]
	if one.Pin() == two.Pin() || !reflect.DeepEqual(one.Binding(), two.Binding()) {
		t.Fatal("two users of one org have one pin, or the org's binding names the user")
	}
}

// Every token a Reader has held stays in its echo check for as long as it lives: after a refresh from A to B
// in one read, an answer of a later read that holds A is refused.
func TestATokenOfAnEarlierReadNeverBecomesARecord(t *testing.T) {
	const rotated = "invented-rotated-token"
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	accountID := org.Add(sized(t, 1, 1))
	reader := signIn(t, org, sf, "production")
	org.Token, sf.Token = rotated, rotated
	if _, err := reader.GetAccount(context.Background(), accountID); err != nil {
		t.Fatal(err)
	}
	org.Intercept = func(request *http.Request) *http.Response {
		if !strings.Contains(request.URL.Query().Get("q"), "FROM Opportunity") {
			return nil
		}
		return salesforcetest.Respond(200, `{"totalSize": 1, "done": true, "records": [{"Id": "006000000000000AAA", `+
			`"AccountId": "`+account+`", "Name": "`+salesforcetest.Token+`", "StageName": "Proposal", "Amount": null, `+
			`"CloseDate": "2027-02-10", "IsClosed": false, "IsWon": false}]}`)
	}
	if records, err := reader.ListOpportunities(context.Background(), accountID); codeOf(err) != invalidResponse {
		t.Fatalf("%d records; %v", len(records), err)
	}
}
