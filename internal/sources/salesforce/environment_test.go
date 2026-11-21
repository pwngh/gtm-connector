package salesforce

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// handed sets the variables of a token handed to the process for this test: the instance address, and the
// token in a file of its own, which the test may rewrite. It gives the file's path.
func handed(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "salesforce-token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(InstanceVariable, salesforcetest.Instance)
	t.Setenv(TokenVariable, "")
	t.Setenv(TokenFileVariable, path)
	return path
}

// fromEnvironment is a Reader of the token the process holds, with its first checks made.
func fromEnvironment(t *testing.T, org *salesforcetest.Org) *Reader {
	t.Helper()
	reader, err := FromEnvironment(context.Background(), Options{Environment: "production", Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PrepareConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reader
}

// The variables are read as the token rule and the address rule of an sf login hold them. Anything else is
// salesforce_invalid_configuration, before any request, and the error holds neither value.
func TestATokenHandedToTheProcessIsChecked(t *testing.T) {
	twenty := strings.Repeat("x", 20)
	file := func(contents string) string {
		path := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name                  string
		instance, token, path string
		accepted              bool
	}{
		{"the token in its variable", salesforcetest.Instance, salesforcetest.Token, "", true},
		{"the token in a file", salesforcetest.Instance, "", file(salesforcetest.Token), true},
		{"a file with a line ending", salesforcetest.Instance, "", file(salesforcetest.Token + "\r\n"), true},
		{"the longest token in a file", salesforcetest.Instance, "", file(strings.Repeat("x", 4096) + "\n"), true},
		{"no variable", "", "", "", false},
		{"no instance", "", salesforcetest.Token, "", false},
		{"no token", salesforcetest.Instance, "", "", false},
		{"both the token and its file", salesforcetest.Instance, salesforcetest.Token, file(salesforcetest.Token), false},
		{"an http address", "http://invented-books.my.salesforce.com", salesforcetest.Token, "", false},
		{"an address with a path", salesforcetest.Instance + "/services", salesforcetest.Token, "", false},
		{"another domain", "https://invented-books.example.com", salesforcetest.Token, "", false},
		{"a short token", salesforcetest.Instance, strings.Repeat("x", 19), "", false},
		{"a token with a space", salesforcetest.Instance, twenty + " x", "", false},
		{"two lines in the file", salesforcetest.Instance, "", file(salesforcetest.Token + "\n\n"), false},
		{"an empty file", salesforcetest.Instance, "", file(""), false},
		{"a file past the longest token", salesforcetest.Instance, "", file(strings.Repeat("x", 4097) + "\n"), false},
		{"a file that is missing", salesforcetest.Instance, "", filepath.Join(t.TempDir(), "missing"), false},
		{"a folder for a file", salesforcetest.Instance, "", t.TempDir(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(InstanceVariable, c.instance)
			t.Setenv(TokenVariable, c.token)
			t.Setenv(TokenFileVariable, c.path)
			org := salesforcetest.NewOrg()
			reader, err := FromEnvironment(context.Background(), Options{Environment: "production", Transport: org})
			switch {
			case c.accepted && err != nil:
				t.Fatalf("refused: %v", err)
			case !c.accepted && (codeOf(err) != invalidConfiguration || err.Error() != invalidConfiguration):
				t.Fatalf("got %v", err)
			case c.accepted && reader.instanceURL != "https://invented-books.my.salesforce.com":
				t.Fatalf("the origin %q", reader.instanceURL)
			}
			if len(org.Requests) != 0 {
				t.Fatalf("FromEnvironment sent %q", org.Requests)
			}
		})
	}
	if _, err := FromEnvironment(context.Background(), Options{Environment: "staging"}); codeOf(err) != invalidConfiguration {
		t.Fatalf("an unknown environment: %v", err)
	}
}

// The first operation learns the org and the user the token reads as, and checks the org against the
// environment, as for an sf login.
func TestATokenHandedToTheProcessIsCheckedAgainstTheOrg(t *testing.T) {
	handed(t, salesforcetest.Token)
	org := salesforcetest.NewOrg()
	reader := fromEnvironment(t, org)
	identity, err := reader.PrepareConnection(context.Background())
	if err != nil || identity.OrganizationID != salesforcetest.OrganizationID || identity.UserID != salesforcetest.UserID ||
		identity.InstanceURL != salesforcetest.Instance {
		t.Fatalf("%+v %v", identity, err)
	}
	// Developer Edition is the default environment, and this org is not one.
	developer, err := FromEnvironment(context.Background(), Options{Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := developer.CheckEnvironment(context.Background()); codeOf(err) != editionRequired {
		t.Fatalf("a production org read as Developer Edition: %v", err)
	}
}

// A first HTTP 401 reads the variables, and the file, once more. A new token of the same org and user is
// kept and the read goes on. The same token, a token of another org or user, and variables that are now
// missing are refused as salesforce_auth_required, and the variables are not read twice in one read.
func TestAnExpiredTokenReadsTheEnvironmentOnce(t *testing.T) {
	const rotated = "invented-rotated-token-in-the-file"
	accountQuery := queryPrefix + "SELECT Id, Name, Industry, AccountNumber, Website FROM Account WHERE Id = '" + account +
		"' ORDER BY Id LIMIT 101"
	start := func(t *testing.T) (*salesforcetest.Org, *Reader, string, string) {
		path := handed(t, salesforcetest.Token)
		org := salesforcetest.NewOrg()
		accountID := org.Add(sized(t, 1, 1))
		reader := fromEnvironment(t, org)
		org.Requests = nil
		return org, reader, accountID, path
	}
	rotate := func(t *testing.T, path, token string) {
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the file holds a new token of the same org and user", func(t *testing.T) {
		org, reader, accountID, path := start(t)
		org.Token = rotated
		rotate(t, path, rotated)
		found, err := reader.GetAccount(context.Background(), accountID)
		if err != nil || len(found.Contacts) != 1 {
			t.Fatal(err)
		}
		// The refused read, the org's identity and the user under the new token, then the read again.
		if len(org.Requests) != 6 || org.Requests[0] != accountQuery || org.Requests[3] != accountQuery ||
			org.Requests[1] != queryPrefix+organizationQuery || org.Requests[2] != salesforcetest.UserInfoPath {
			t.Fatalf("the requests:\n%s", strings.Join(org.Requests, "\n"))
		}
		if reader.accessToken != rotated {
			t.Fatal("the proved token was not kept")
		}
	})
	t.Run("the variable holds a new token", func(t *testing.T) {
		org, reader, accountID, _ := start(t)
		org.Token = rotated
		t.Setenv(TokenFileVariable, "")
		t.Setenv(TokenVariable, rotated)
		if _, err := reader.GetAccount(context.Background(), accountID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the file still holds the refused token", func(t *testing.T) {
		org, reader, accountID, _ := start(t)
		org.Token = rotated
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		// The token the org has just refused is not sent a second time.
		if len(org.Requests) != 1 {
			t.Fatal(org.Requests)
		}
	})
	t.Run("the new token opens another org", func(t *testing.T) {
		org, reader, accountID, path := start(t)
		org.Token, org.ID = rotated, "00D000000000000BBB"
		rotate(t, path, rotated)
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		// The org was asked who it is, and nothing more. The token of the other org is not kept.
		if len(org.Requests) != 2 || reader.accessToken != salesforcetest.Token {
			t.Fatal(org.Requests)
		}
	})
	t.Run("the new token reads as another user", func(t *testing.T) {
		org, reader, accountID, path := start(t)
		org.Token = rotated
		org.Users[rotated] = [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"}
		rotate(t, path, rotated)
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		if len(org.Requests) != 3 || reader.accessToken != salesforcetest.Token {
			t.Fatal(org.Requests)
		}
	})
	t.Run("the new token is for another instance", func(t *testing.T) {
		org, reader, accountID, path := start(t)
		org.Token = rotated
		rotate(t, path, rotated)
		t.Setenv(InstanceVariable, "https://invented-other.my.salesforce.com")
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired || len(org.Requests) != 1 {
			t.Fatal(err, org.Requests)
		}
	})
	for name, change := range map[string]func(t *testing.T, path string){
		"the file is gone":          func(t *testing.T, path string) { os.Remove(path) },
		"the file holds a fragment": func(t *testing.T, path string) { rotate(t, path, "e") },
		"both variables are set":    func(t *testing.T, path string) { t.Setenv(TokenVariable, rotated) },
	} {
		t.Run(name, func(t *testing.T) {
			org, reader, accountID, path := start(t)
			org.Token = rotated
			change(t, path)
			if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired ||
				err.Error() != authRequired || len(org.Requests) != 1 {
				t.Fatal(err, org.Requests)
			}
			// Once the file is put right, the next read takes the new token and goes on. Nothing the
			// refused read saw, such as a fragment "e" found in every answer, is held against it.
			t.Setenv(TokenVariable, "")
			rotate(t, path, rotated)
			if found, err := reader.GetAccount(context.Background(), accountID); err != nil || len(found.Contacts) != 1 {
				t.Fatalf("after the file was put right: %v", err)
			}
		})
	}
}

// A token handed to the process is one the reader holds: an answer that holds it is refused, as for sf.
func TestAnAnswerHoldingAHandedTokenIsRefused(t *testing.T) {
	handed(t, salesforcetest.Token)
	org := salesforcetest.NewOrg()
	org.Add(object(t, `{"Account": [{"Id": "`+account+`", "Name": "Echo `+salesforcetest.Token+`", "Industry": null,
		"AccountNumber": null}], "Contact": [], "Opportunity": [], "Contract": []}`))
	reader := fromEnvironment(t, org)
	if _, err := reader.GetAccount(context.Background(), "acc-sf-"+account); codeOf(err) != invalidResponse {
		t.Fatalf("an answer holding the token gave %v", err)
	}
}
