package salesforce

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// user is one invented User row as the org holds it.
func user(id, name string, email any, active bool, kind string) *strictjson.Object {
	return strictjson.NewObject("Id", id, "Name", name, "Email", email, "IsActive", active, "UserType", kind)
}

const usersRequest = queryPrefix + "SELECT Id, Name, Email, IsActive FROM User WHERE UserType = 'Standard' " +
	"ORDER BY Id LIMIT 50001"

// list_users reads the org's Standard users, active or not, by ID, each with its record_ref. A portal or
// community user is never asked for: the query itself names UserType = 'Standard'.
func TestListUsersReadsStandardUsers(t *testing.T) {
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	org.AddUsers(
		user("005000000000000CCC", "Avery Linden", nil, true, "Standard"),
		user("005000000000000AAA", "Morgan Reed", "morgan@seller.example", true, "Standard"),
		user("005000000000000BBB", "Quinn Moss", "quinn@seller.example", false, "Standard"),
		user("005000000000000DDD", "Dana Crest", "dana@buyer.example", true, "PowerCustomerSuccess"),
		user("005000000000000EEE", "Partner Person", "partner@partner.example", true, "PowerPartner"))
	reader := signIn(t, org, sf, "production")
	asked := len(org.Requests)
	system, records, err := reader.Read(context.Background(), "list_users", "acc-sf-"+account)
	if err != nil || system != "crm" {
		t.Fatal(system, err)
	}
	row := func(id, name, email string, active bool) string {
		return fmt.Sprintf(`{"id": "usr-sf-%[1]s", "source_id": "%[1]s", "backend": "salesforce", "name": "%[2]s", `+
			`"email": %[3]s, "is_active": %[4]t, "record_ref": "crm/users/usr-sf-%[1]s"}`, id, name, email, active)
	}
	want := []string{
		row("005000000000000AAA", "Morgan Reed", `"morgan@seller.example"`, true),
		row("005000000000000BBB", "Quinn Moss", `"quinn@seller.example"`, false),
		row("005000000000000CCC", "Avery Linden", "null", true),
	}
	if got := dumped(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("list_users:\n%s", strings.Join(got, "\n"))
	}
	if got := org.Requests[asked:]; !reflect.DeepEqual(got, []string{usersRequest}) || len(org.Unexpected) != 0 {
		t.Fatalf("the requests: %v %v", got, org.Unexpected)
	}

	// The read is made for one account's card and checks the form of its ID before any request.
	asked = len(org.Requests)
	for _, id := range []string{"acc-001", "acc-sf-" + account + "' OR Name != '", "acc-sf-", account} {
		if _, _, err := reader.Read(context.Background(), "list_users", id); codeOf(err) != invalidArguments {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if len(org.Requests) != asked {
		t.Fatalf("the requests: %v", org.Requests[asked:])
	}
}

// A User row outside the shape is refused whole: a name that is not a string, an email of another type,
// an IsActive that is not a boolean, or an ID that is not one.
func TestListUsersRefusesAMistypedRow(t *testing.T) {
	for name, row := range map[string]*strictjson.Object{
		"null name":      user("005000000000000AAA", "", nil, true, "Standard"),
		"numeric email":  user("005000000000000AAA", "Morgan Reed", 7, true, "Standard"),
		"text IsActive":  strictjson.NewObject("Id", "005000000000000AAA", "Name", "Morgan Reed", "Email", nil, "IsActive", "true", "UserType", "Standard"),
		"short ID":       user("005000000000AAA", "Morgan Reed", nil, true, "Standard"),
		"missing Active": strictjson.NewObject("Id", "005000000000000AAA", "Name", "Morgan Reed", "Email", nil, "UserType", "Standard"),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "null name" {
				row.Set("Name", nil)
			}
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			org.AddUsers(row)
			reader := signIn(t, org, sf, "production")
			if found, err := reader.ListUsers(context.Background()); codeOf(err) != invalidResponse || found != nil {
				t.Fatal(found, err)
			}
		})
	}
}
