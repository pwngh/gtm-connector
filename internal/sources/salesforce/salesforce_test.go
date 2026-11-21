package salesforce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Every test reads an invented org through an invented sf. Nothing here runs the real sf, opens a
// connection or reads a credential.

const (
	account      = "001000000000000AAA"
	otherAccount = "001000000000000BBB"
	queryPrefix  = salesforcetest.QueryPath + " "
)

var loginCommands = [][]string{
	{"org", "display", "--target-org", salesforcetest.Alias, "--json"},
	{"org", "auth", "show-access-token", "--target-org", salesforcetest.Alias, "--json"},
}

func object(t *testing.T, text string) *strictjson.Object {
	t.Helper()
	value, err := strictjson.Loads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value.(*strictjson.Object)
}

// signIn is a Reader of the org, signed in through sf, with its first checks made.
func signIn(t *testing.T, org *salesforcetest.Org, sf *salesforcetest.SF, environment string) *Reader {
	t.Helper()
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: environment, Runner: sf,
		Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PrepareConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reader
}

// sized is an account with this many contacts and contracts.
func sized(t *testing.T, contacts, contracts int) *strictjson.Object {
	rows := object(t, `{"Account": [{"Id": "`+account+`", "Name": "Sized Account", "Industry": "Testing",
		"AccountNumber": null}], "Contact": [], "Opportunity": [], "Contract": []}`)
	var people, terms []any
	for i := 0; i < contacts; i++ {
		people = append(people, strictjson.NewObject("Id", fmt.Sprintf("003%015d", i), "AccountId", account,
			"Name", fmt.Sprintf("Person %d", i), "Title", "Buyer", "Email", nil))
	}
	for i := 0; i < contracts; i++ {
		terms = append(terms, strictjson.NewObject("Id", fmt.Sprintf("800%015d", i), "AccountId", account,
			"Status", "Activated", "StatusCode", "Activated", "StartDate", "2026-06-01", "ContractTerm", 12,
			"EndDate", "2027-06-01"))
	}
	rows.Set("Contact", append([]any{}, people...))
	rows.Set("Contract", append([]any{}, terms...))
	return rows
}

func dumped(t *testing.T, records []*strictjson.Object) []string {
	t.Helper()
	out := make([]string, len(records))
	for i, record := range records {
		text, err := strictjson.Dumps(record, strictjson.Quoted)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = text
	}
	return out
}

func TestSigningInRunsTwoCommandsAndReadsNothing(t *testing.T) {
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	if _, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf, Transport: org}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sf.Commands, loginCommands) {
		t.Fatalf("sf ran %v", sf.Commands)
	}
	if len(org.Requests) != 0 {
		t.Fatalf("the org was asked %v", org.Requests)
	}
}

// The two CRM reads of a card: the fixed requests, in order, and each object mapped to its record.
func TestTheCardReadsMapTheFourObjects(t *testing.T) {
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	accountID := org.Add(object(t, `{
		"Account": [{"Id": "`+account+`", "Name": "Invented Account", "Industry": null, "AccountNumber": null}],
		"Contact": [
			{"Id": "003000000000000BBB", "AccountId": "`+account+`", "Name": "Second Person", "Title": null, "Email": null},
			{"Id": "003000000000000AAA", "AccountId": "`+account+`", "Name": "First Person", "Title": "Buyer",
			 "Email": "first@invented.example"}],
		"Opportunity": [
			{"Id": "006000000000000AAA", "AccountId": "`+account+`", "Name": "Renewal", "StageName": "Proposal",
			 "Amount": {"decimal": "2500.50"}, "CloseDate": "2027-02-10", "IsClosed": false, "IsWon": false},
			{"Id": "006000000000000BBB", "AccountId": "`+account+`", "Name": "Whole", "StageName": "Closed Won",
			 "Amount": 12, "CloseDate": "2026-12-01", "IsClosed": true, "IsWon": true},
			{"Id": "006000000000000CCC", "AccountId": "`+account+`", "Name": "No amount", "StageName": "Qualify",
			 "Amount": null, "CloseDate": "2027-03-01", "IsClosed": false, "IsWon": false}],
		"Contract": [{"Id": "800000000000000AAA", "AccountId": "`+account+`", "Status": "Signed",
			"StatusCode": "Activated", "StartDate": null, "ContractTerm": null, "EndDate": "2027-02-20"}]}`))
	reader := signIn(t, org, sf, "production")
	system, records, err := reader.Read(context.Background(), "get_account", accountID)
	if err != nil || system != "crm" {
		t.Fatal(system, err)
	}
	want := []string{
		`{"id": "acc-sf-` + account + `", "source_id": "` + account + `", "backend": "salesforce", ` +
			`"name": "Invented Account", "industry": null, "account_number": null, ` +
			`"record_ref": "crm/accounts/acc-sf-` + account + `"}`,
		`{"id": "con-sf-003000000000000AAA", "source_id": "003000000000000AAA", "backend": "salesforce", ` +
			`"account_id": "acc-sf-` + account + `", "name": "First Person", "title": "Buyer", ` +
			`"email": "first@invented.example", "record_ref": "crm/contacts/con-sf-003000000000000AAA"}`,
		`{"id": "con-sf-003000000000000BBB", "source_id": "003000000000000BBB", "backend": "salesforce", ` +
			`"account_id": "acc-sf-` + account + `", "name": "Second Person", "title": null, "email": null, ` +
			`"record_ref": "crm/contacts/con-sf-003000000000000BBB"}`,
		`{"id": "ctr-sf-800000000000000AAA", "source_id": "800000000000000AAA", "backend": "salesforce", ` +
			`"account_id": "acc-sf-` + account + `", "status": "Signed", "status_code": "Activated", ` +
			`"start_date": null, "end_date": "2027-02-20", "contract_term": null, ` +
			`"record_ref": "crm/contracts/ctr-sf-800000000000000AAA"}`,
	}
	if got := dumped(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("get_account:\n%s", strings.Join(got, "\n"))
	}
	_, records, err = reader.Read(context.Background(), "list_opportunities", accountID)
	if err != nil {
		t.Fatal(err)
	}
	opportunity := func(id, name, stage, day string, closed bool, amount string) string {
		return fmt.Sprintf(`{"id": "opp-sf-%[1]s", "source_id": "%[1]s", "backend": "salesforce", `+
			`"account_id": "acc-sf-%[2]s", "name": "%[3]s", "stage": "%[4]s", "close_date": "%[5]s", `+
			`"is_closed": %[6]t, "is_won": %[6]t, "amount_decimal": %[7]s, "currency": null, `+
			`"record_ref": "crm/opportunities/opp-sf-%[1]s"}`, id, account, name, stage, day, closed, amount)
	}
	want = []string{
		opportunity("006000000000000AAA", "Renewal", "Proposal", "2027-02-10", false, `"2500.50"`),
		opportunity("006000000000000BBB", "Whole", "Closed Won", "2026-12-01", true, `"12"`),
		opportunity("006000000000000CCC", "No amount", "Qualify", "2027-03-01", false, "null"),
	}
	if got := dumped(t, records); !reflect.DeepEqual(got, want) {
		t.Fatalf("list_opportunities:\n%s", strings.Join(got, "\n"))
	}
	owned := " WHERE AccountId = '" + account + "' ORDER BY Id LIMIT 1001"
	requests := []string{
		queryPrefix + "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1",
		"/services/data/v68.0/sobjects/Account/describe",
		"/services/data/v68.0/sobjects/Contact/describe",
		"/services/data/v68.0/sobjects/Opportunity/describe",
		"/services/data/v68.0/sobjects/Contract/describe",
		queryPrefix + "SELECT Id, Name, Industry, AccountNumber FROM Account WHERE Id = '" + account + "' ORDER BY Id LIMIT 101",
		queryPrefix + "SELECT Id, AccountId, Name, Title, Email FROM Contact" + owned,
		queryPrefix + "SELECT Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate FROM Contract" + owned,
		queryPrefix + "SELECT Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, IsWon FROM Opportunity" + owned,
	}
	if !reflect.DeepEqual(org.Requests, requests) {
		t.Fatalf("the requests:\n%s", strings.Join(org.Requests, "\n"))
	}
	if len(org.Unexpected) != 0 || !reflect.DeepEqual(sf.Commands, loginCommands) {
		t.Fatal(org.Unexpected, sf.Commands)
	}

	// An account the org does not hold is not found, and an ID of another form is refused before any request.
	asked := len(org.Requests)
	if _, _, err := reader.Read(context.Background(), "get_account", "acc-sf-"+otherAccount); !errors.Is(err, ErrAccountNotFound) {
		t.Fatal(err)
	}
	if len(org.Requests) != asked+1 {
		t.Fatalf("the requests: %v", org.Requests[asked:])
	}
	for _, id := range []string{"acc-001", "acc-sf-" + account + "' OR Name != '", "acc-sf-", account} {
		for _, tool := range []string{"get_account", "list_opportunities"} {
			if _, _, err := reader.Read(context.Background(), tool, id); codeOf(err) != invalidArguments {
				t.Fatalf("%s %q: %v", tool, id, err)
			}
		}
	}
	if len(org.Requests) != asked+1 {
		t.Fatalf("the requests: %v", org.Requests[asked:])
	}
}

// An amount keeps the digits and the scale the org wrote, in fixed point. These cases were run through the
// Python adapter, and agree with it.
func TestAnAmountKeepsItsDigits(t *testing.T) {
	for written, want := range map[string]any{"1.5": "1.5", "2500.00": "2500.00", "12": "12", "-0": "0", "-0.0": "-0.0",
		"1E+2": "100", "0e5": "0", "1e-28": "0.0000000000000000000000000001",
		"1234567890123456789012345678": "1234567890123456789012345678", "null": nil} {
		value, err := strictjson.LoadsDecimal([]byte(`{"Amount": ` + written + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := amountText(value.(*strictjson.Object)); err != nil || got != want {
			t.Errorf("%s: %v, %v", written, got, err)
		}
	}
	for _, written := range []string{`"1.5"`, "true", "12345678901234567890123456789", "1e29", "1e-29", "1.2345678901234567890123456789"} {
		value, err := strictjson.LoadsDecimal([]byte(`{"Amount": ` + written + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := amountText(value.(*strictjson.Object)); codeOf(err) != invalidResponse {
			t.Errorf("%s: %v, %v", written, got, err)
		}
	}
	if _, err := amountText(strictjson.NewObject()); codeOf(err) != invalidResponse {
		t.Errorf("no Amount: %v", err)
	}
}

// One account's contacts, opportunities and contracts are bounded at 1,000 rows each, 10 pages a query and
// 12 requests a read. One more is refused whole.
func TestRowPageAndRequestLimits(t *testing.T) {
	for _, c := range []struct {
		name                string
		contacts, contracts int
		contactPage         int
		want                string
		requests            int
	}{
		{"a thousand contacts in ten pages", 1000, 1, 100, "", 12},
		{"a thousand and one contacts", 1001, 1, 0, resultLimit, 2},
		{"eleven pages", 11, 1, 1, resultLimit, 11},
		{"twelve requests", 5, 6, 1, "", 12},
		{"thirteen requests", 6, 6, 1, resultLimit, 12},
	} {
		t.Run(c.name, func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			accountID := org.Add(sized(t, c.contacts, c.contracts))
			if c.contactPage > 0 {
				org.PageSize("Contact", account, c.contactPage)
				org.PageSize("Contract", account, 1)
			}
			reader := signIn(t, org, sf, "production")
			asked := len(org.Requests)
			found, err := reader.GetAccount(context.Background(), accountID)
			if codeOf(err) != c.want || len(org.Requests)-asked != c.requests {
				t.Fatalf("%v after %d requests", err, len(org.Requests)-asked)
			}
			if c.want == "" && (len(found.Contacts) != c.contacts || len(found.Contracts) != c.contracts) {
				t.Fatalf("%d contacts and %d contracts", len(found.Contacts), len(found.Contracts))
			}
			if c.want != "" && found != nil {
				t.Fatal("part of an account was returned")
			}
		})
	}
}

// A first HTTP 401 asks sf for the login once more, confirms the org, and retries. Anything but the same
// org under a token it accepts is refused, and sf is never asked twice in one read.
func TestAnExpiredTokenAsksSFOnce(t *testing.T) {
	const rotated = "invented-rotated-token"
	start := func(t *testing.T) (*salesforcetest.Org, *salesforcetest.SF, *Reader, string) {
		org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
		accountID := org.Add(sized(t, 1, 1))
		reader := signIn(t, org, sf, "production")
		org.Requests = nil
		return org, sf, reader, accountID
	}
	accountQuery := queryPrefix + "SELECT Id, Name, Industry, AccountNumber FROM Account WHERE Id = '" + account +
		"' ORDER BY Id LIMIT 101"
	t.Run("sf gives a token the org accepts", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token, sf.Token = rotated, rotated
		found, err := reader.GetAccount(context.Background(), accountID)
		if err != nil || len(found.Contacts) != 1 {
			t.Fatal(err)
		}
		if len(sf.Commands) != 4 || !reflect.DeepEqual(sf.Commands[2:], loginCommands) {
			t.Fatalf("sf ran %v", sf.Commands)
		}
		// The refused read, the org's identity under the new token, then the read again.
		if len(org.Requests) != 5 || org.Requests[0] != accountQuery || org.Requests[2] != accountQuery ||
			org.Requests[1] != queryPrefix+organizationQuery {
			t.Fatalf("the requests:\n%s", strings.Join(org.Requests, "\n"))
		}
		// The proved token is kept: the next read asks neither sf nor twice.
		if _, err := reader.ListOpportunities(context.Background(), accountID); err != nil || len(org.Requests) != 6 ||
			len(sf.Commands) != 4 {
			t.Fatal(err, org.Requests, sf.Commands)
		}
	})
	t.Run("sf gives the refused token again", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token = rotated
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		// The token the org has just refused is not sent a second time.
		if len(sf.Commands) != 4 || len(org.Requests) != 1 {
			t.Fatal(sf.Commands, org.Requests)
		}
	})
	t.Run("sf now names another org for the alias", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token, sf.Token, sf.OrganizationID = rotated, rotated, "00D000000000000BBB"
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		// The new token is sent nowhere and is not kept.
		if len(org.Requests) != 1 || reader.accessToken != salesforcetest.Token {
			t.Fatalf("a request followed the refusal: %v", org.Requests)
		}
	})
	t.Run("sf gives another instance", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token, sf.Token, sf.Instance = rotated, rotated, "https://invented-other.my.salesforce.com"
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		if len(org.Requests) != 1 {
			t.Fatalf("a request followed the refusal: %v", org.Requests)
		}
	})
	t.Run("the alias now opens another org", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token, sf.Token, org.ID = rotated, rotated, "00D000000000000BBB"
		if _, err := reader.GetAccount(context.Background(), accountID); codeOf(err) != authRequired {
			t.Fatal(err)
		}
		// The token of the other org was not kept: the next read is refused the same way.
		if len(org.Requests) != 2 || reader.accessToken != salesforcetest.Token {
			t.Fatal(org.Requests)
		}
	})
	t.Run("sf has no login", func(t *testing.T) {
		org, sf, reader, accountID := start(t)
		org.Token = rotated
		sf.Answer = func(context.Context, []string) ([]byte, int, error) {
			return []byte(`{"status": 1, "message": "invented refusal naming https://invented-private.my.salesforce.com"}`), 1, nil
		}
		_, err := reader.GetAccount(context.Background(), accountID)
		if codeOf(err) != authRequired || err.Error() != authRequired || len(org.Requests) != 1 {
			t.Fatal(err, org.Requests)
		}
	})
}

// roundTrip is a transport made of one function.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// An answer outside the supported shape gives no record, only a fixed code. The error holds nothing of the
// answer or of the request.
func TestAnswersOutsideTheShapeAreRefused(t *testing.T) {
	with := func(status int, body string, headers ...string) *http.Response {
		response := salesforcetest.Respond(status, body)
		for i := 0; i+1 < len(headers); i += 2 {
			response.Header.Set(headers[i], headers[i+1])
		}
		return response
	}
	one := func(row string) string { return `{"totalSize": 1, "done": true, "records": [` + row + `]}` }
	opportunity := func(change string) string {
		row := `{"Id": "006000000000000AAA", "AccountId": "` + account + `", "Name": "Renewal", "StageName": "Proposal", ` +
			`"Amount": 1.5, "CloseDate": "2027-02-10", "IsClosed": false, "IsWon": false}`
		field, _, _ := strings.Cut(change, ":")
		start := strings.Index(row, field+":")
		end := start + strings.IndexAny(row[start:], ",}")
		return one(row[:start] + change + row[end:])
	}
	for _, c := range []struct {
		name   string
		answer *http.Response
		want   string
	}{
		{"a page that is not JSON", with(200, "<html>Sign in</html>", "Content-Type", "text/html; charset=utf-8"), invalidResponse},
		{"JSON labelled as a page", with(200, opportunity(`"Name": "Renewal"`), "Content-Type", "text/html"), invalidResponse},
		{"a compressed answer", with(200, opportunity(`"Name": "Renewal"`), "Content-Encoding", "gzip"), invalidResponse},
		{"a key given twice", with(200, `{"totalSize": 0, "totalSize": 0, "done": true, "records": []}`), invalidResponse},
		{"a total that is not the rows", with(200, `{"totalSize": 1, "done": true, "records": []}`), invalidResponse},
		{"a next page outside the query path", with(200, `{"totalSize": 2, "done": false, "records": [`+
			`{"Id": "006000000000000AAA"}], "nextRecordsUrl": "https://elsewhere.example/query/01g"}`), paginationInvalid},
		{"a row of another account", with(200, opportunity(`"AccountId": "`+otherAccount+`"`)), invalidResponse},
		{"an amount as text", with(200, opportunity(`"Amount": "1.5"`)), invalidResponse},
		{"an amount of 29 digits", with(200, opportunity(`"Amount": 12345678901234567890123456789`)), invalidResponse},
		{"an amount with an exponent past 28", with(200, opportunity(`"Amount": 1e29`)), invalidResponse},
		{"a day in another form", with(200, opportunity(`"CloseDate": "20270210"`)), invalidResponse},
		{"an outcome that is not a boolean", with(200, opportunity(`"IsWon": 0`)), invalidResponse},
		{"the request limit", with(403, `[{"errorCode": "REQUEST_LIMIT_EXCEEDED", "message": "invented"}]`), rateLimited},
		{"the API switched off", with(403, `[{"errorCode": "API_DISABLED_FOR_ORG"}]`), apiDisabled},
		{"another denial", with(403, `<html>Denied</html>`, "Content-Type", "text/html"), forbidden},
		{"too many requests", with(429, `[]`), rateLimited},
		{"a server error", with(500, `[{"errorCode": "UNKNOWN_EXCEPTION", "message": "invented private text"}]`), httpError},
		{"a redirect", with(302, ``, "Location", "https://elsewhere.example/"), httpError},
		{"no answer", nil, unavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			accountID := org.Add(sized(t, 0, 0))
			asked := 0
			transport := roundTrip(func(request *http.Request) (*http.Response, error) {
				if !strings.Contains(request.URL.Query().Get("q"), "FROM Opportunity") {
					return org.RoundTrip(request)
				}
				if asked++; c.answer == nil {
					return nil, errors.New("dial tcp: lookup invented-books.my.salesforce.com: no such host")
				}
				return c.answer, nil
			})
			reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production",
				Runner: sf, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			records, err := reader.ListOpportunities(context.Background(), accountID)
			if records != nil || err == nil || err.Error() != c.want {
				t.Fatalf("%d records; %v", len(records), err)
			}
			if asked != 1 || len(org.Unexpected) != 0 {
				t.Fatalf("asked %d times; %v", asked, org.Unexpected)
			}
		})
	}
}

// The first read checks the org against the selected environment, and outside Developer Edition the fields.
func TestTheOrgMustBeTheSelectedEnvironment(t *testing.T) {
	for _, c := range []struct {
		environment, edition string
		sandbox              bool
		want                 string
		requests             int
	}{
		{"developer", "Developer Edition", false, "", 1},
		{"developer", "Enterprise Edition", false, editionRequired, 1},
		{"developer", "Developer Edition", true, environmentMismatch, 1},
		{"sandbox", "Enterprise Edition", true, "", 5},
		{"sandbox", "Enterprise Edition", false, environmentMismatch, 1},
		{"production", "Enterprise Edition", false, "", 5},
		{"production", "Developer Edition", false, environmentMismatch, 1},
		{"production", "Enterprise Edition", true, environmentMismatch, 1},
		{"production", " Enterprise Edition", false, invalidResponse, 1},
	} {
		t.Run(fmt.Sprintf("%s reads %q, sandbox %t", c.environment, c.edition, c.sandbox), func(t *testing.T) {
			org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
			org.Edition, org.Sandbox = c.edition, c.sandbox
			reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: c.environment,
				Runner: sf, Transport: org})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := reader.PrepareConnection(context.Background())
			if codeOf(err) != c.want || len(org.Requests) != c.requests {
				t.Fatalf("%v after %v", err, org.Requests)
			}
			want := Identity{salesforcetest.Instance, salesforcetest.OrganizationID, c.environment}
			if c.want == "" && identity != want {
				t.Fatalf("the identity: %+v", identity)
			}
			// The checks that passed are not made again.
			if _, err := reader.PrepareConnection(context.Background()); c.want == "" && (err != nil || len(org.Requests) != c.requests) {
				t.Fatalf("%v after %v", err, org.Requests)
			}
		})
	}
	t.Run("an environment that is none of the three", func(t *testing.T) {
		sf := salesforcetest.NewSF()
		_, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "staging", Runner: sf})
		if codeOf(err) != invalidConfiguration || len(sf.Commands) != 0 {
			t.Fatal(err, sf.Commands)
		}
	})
	t.Run("an object without a mapped field", func(t *testing.T) {
		org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
		transport := roundTrip(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/Contract/describe") {
				return salesforcetest.Respond(200, `{"name": "Contract", "queryable": true, "fields": [{"name": "Id"}]}`), nil
			}
			return org.RoundTrip(request)
		})
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production", Runner: sf,
			Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != schemaUnavailable {
			t.Fatal(err)
		}
	})
}

// A search is literal text inside a fixed query, and a selection limits the query itself.
func TestSearchesAreFixedQueries(t *testing.T) {
	rows := func(n int, number string) string {
		var records []string
		for i := 0; i < n; i++ {
			records = append(records, fmt.Sprintf(`{"Id": "001%015d", "Name": "Account %d", "Industry": null, `+
				`"AccountNumber": %s}`, i, i, number))
		}
		return fmt.Sprintf(`{"totalSize": %d, "done": true, "records": [%s]}`, n, strings.Join(records, ", "))
	}
	first, second := "acc-sf-001000000000000000", "acc-sf-001000000000000001"
	search := func(answer string) (*Reader, *[]string) {
		org, sf, asked := salesforcetest.NewOrg(), salesforcetest.NewSF(), []string{}
		transport := roundTrip(func(request *http.Request) (*http.Response, error) {
			query := request.URL.Query().Get("q")
			if !strings.Contains(query, " WHERE ") {
				return org.RoundTrip(request)
			}
			asked = append(asked, query)
			return salesforcetest.Respond(200, answer), nil
		})
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		org.Edition = "Developer Edition"
		return reader, &asked
	}
	ctx := context.Background()
	t.Run("a name", func(t *testing.T) {
		reader, asked := search(rows(101, "null"))
		accounts, more, err := reader.SearchAccounts(ctx, `50%_O'Brien\`, nil, nil)
		if err != nil || len(accounts) != 100 || !more {
			t.Fatal(len(accounts), more, err)
		}
		want := `SELECT Id, Name, Industry, AccountNumber FROM Account WHERE Name LIKE '%50\%\_O\'Brien\\%' ` +
			`ORDER BY Name, Id LIMIT 101`
		if !reflect.DeepEqual(*asked, []string{want}) {
			t.Fatalf("asked %v", *asked)
		}
	})
	t.Run("an account number within a selection", func(t *testing.T) {
		reader, asked := search(rows(1, `"A-1"`))
		number := "A-1"
		accounts, more, err := reader.SearchAccounts(ctx, "", &number, []string{second, first, second})
		if err != nil || len(accounts) != 1 || more {
			t.Fatal(len(accounts), more, err)
		}
		want := `SELECT Id, Name, Industry, AccountNumber FROM Account WHERE AccountNumber = 'A-1' AND ` +
			`Id IN ('001000000000000000', '001000000000000001') ORDER BY Id LIMIT 2`
		if !reflect.DeepEqual(*asked, []string{want}) {
			t.Fatalf("asked %v", *asked)
		}
	})
	t.Run("an account number the org matched loosely", func(t *testing.T) {
		reader, _ := search(rows(1, `"a-1"`))
		number := "A-1"
		if _, _, err := reader.SearchAccounts(ctx, "", &number, nil); codeOf(err) != accountNumberUnclear {
			t.Fatal(err)
		}
	})
	t.Run("an account outside the selection", func(t *testing.T) {
		reader, _ := search(rows(2, "null"))
		if _, _, err := reader.SearchAccounts(ctx, "Account", nil, []string{first}); codeOf(err) != invalidResponse {
			t.Fatal(err)
		}
	})
	t.Run("arguments that are refused before any request", func(t *testing.T) {
		reader, asked := search(rows(0, "null"))
		number, blank := "A-1", " "
		for name, err := range map[string]error{
			"a number with a name":    failure(reader.SearchAccounts(ctx, "Account", &number, nil)),
			"a blank number":          failure(reader.SearchAccounts(ctx, "", &blank, nil)),
			"a control character":     failure(reader.SearchAccounts(ctx, "a\x00b", nil, nil)),
			"201 characters":          failure(reader.SearchAccounts(ctx, strings.Repeat("a", 201), nil, nil)),
			"an empty selection":      failure(reader.SearchAccounts(ctx, "", nil, []string{})),
			"a selection of demo IDs": failure(reader.SearchAccounts(ctx, "", nil, []string{"acc-001"})),
		} {
			if codeOf(err) != invalidArguments {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if len(*asked) != 0 {
			t.Fatalf("asked %v", *asked)
		}
	})
	t.Run("opportunities across a selection", func(t *testing.T) {
		row := `{"Id": "006000000000000AAA", "AccountId": "001000000000000000", "Name": "Renewal", ` +
			`"StageName": "Proposal", "Amount": null, "CloseDate": "2027-02-10", "IsClosed": false, "IsWon": false}`
		reader, asked := search(`{"totalSize": 1, "done": true, "records": [` + row + `]}`)
		found, err := reader.ListAllOpportunities(ctx, []string{second, first})
		if err != nil || len(found) != 1 {
			t.Fatal(err)
		}
		want := `SELECT Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, IsWon FROM Opportunity ` +
			`WHERE AccountId IN ('001000000000000000', '001000000000000001') ORDER BY Id LIMIT 101`
		if !reflect.DeepEqual(*asked, []string{want}) {
			t.Fatalf("asked %v", *asked)
		}
		if _, err := reader.ListAllOpportunities(ctx, []string{second}); codeOf(err) != invalidResponse {
			t.Fatal(err)
		}
	})
}

func failure(_ []*strictjson.Object, _ bool, err error) error { return err }

// The address sf gives must be an HTTPS My Domain origin, and the token printable ASCII. These cases were
// run through the Python adapter, and agree with it.
func TestTheInstanceAndTokenAreChecked(t *testing.T) {
	long := "https://" + strings.Repeat("a", 60)
	long += "." + strings.Repeat("a", 60) + "." + strings.Repeat("a", 60) + "." + strings.Repeat("a", 60) + ".my.salesforce.com"
	for address, want := range map[string]string{
		"https://invented-books.my.salesforce.com":                  "https://invented-books.my.salesforce.com",
		"HTTPS://Invented-Books.My.Salesforce.Com":                  "https://invented-books.my.salesforce.com",
		"https://invented-books.my.salesforce.com:443":              "https://invented-books.my.salesforce.com",
		"https://invented-books.MY.SALESFORCE.COM:443":              "https://invented-books.my.salesforce.com",
		"https://Koo.my.salesforce.com":                             "https://koo.my.salesforce.com",
		"https://a.b.my.salesforce.com":                             "https://a.b.my.salesforce.com",
		"https://" + strings.Repeat("a", 63) + ".my.salesforce.com": "https://" + strings.Repeat("a", 63) + ".my.salesforce.com",
		"https://" + strings.Repeat("a", 64) + ".my.salesforce.com": "",
		long: "",
		"http://invented-books.my.salesforce.com":          "",
		"https://invented-books.my.salesforce.com/":        "",
		"https://invented-books.my.salesforce.com:8443":    "",
		"https://invented-books.my.salesforce.com:0443":    "",
		"https://invented-books.my.salesforce.com:443:443": "",
		"https://invented-books.my.salesforce.com:":        "",
		"https://user@invented-books.my.salesforce.com":    "",
		"https://my.salesforce.com":                        "",
		"https://invented-books.my.salesforce.com.example": "",
		"https://-a.my.salesforce.com":                     "",
		"https://a-.my.salesforce.com":                     "",
		"https://invented_books.my.salesforce.com":         "",
		"https://invented-books.my.salesforce.com?x=1":     "",
		"https://invented-books.my.salesforce.com#f":       "",
		" https://invented-books.my.salesforce.com":        "",
		"https://a.my.salesforce.com\n":                    "",
		"https://invented­books.my.salesforce.com":         "",
		"https://İnvented.my.salesforce.com":               "",
		"":                                                 "",
	} {
		origin, err := configure(address, "t")
		if origin != want || (err == nil) != (want != "") || err != nil && codeOf(err) != invalidConfiguration {
			t.Errorf("%q: %q, %v", address, origin, err)
		}
	}
	for token, accepted := range map[string]bool{"t": true, "!~": true, strings.Repeat("x", 4096): true, "": false,
		"a b": false, "aé": false, "a\x7f": false, strings.Repeat("x", 4097): false} {
		if _, err := configure(salesforcetest.Instance, token); (err == nil) != accepted {
			t.Errorf("a token of %d bytes: %v", len(token), err)
		}
	}
	// What sf gives that fails these checks is a login that cannot be used, not a setting to correct.
	sf := salesforcetest.NewSF()
	sf.Instance = "https://invented-books.my.salesforce.com/services"
	if _, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf}); codeOf(err) != authRequired {
		t.Fatal(err)
	}
}
