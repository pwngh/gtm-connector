// Package salesforcetest is an invented Salesforce org and an invented sf, for tests that read through the
// Salesforce source without a network, a real sf or a credential (the removed Python suite's Org).
package salesforcetest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The invented login every test reads with.
const (
	Alias          = "invented-books"
	Instance       = "https://invented-books.my.salesforce.com"
	Token          = "invented-salesforce-token-for-book-fixtures"
	OrganizationID = "00D54481A5CC257EID"
	QueryPath      = "/services/data/v68.0/query"
	// UserID and Username are the user the login reads as.
	UserID   = "0059F2C41D7EA03XKM"
	Username = "reader@invented-books.example"
	// UserInfoPath is where a token says which user it reads as.
	UserInfoPath = "/services/oauth2/userinfo"
)

// Objects are the four objects the org holds, with the fields each exposes.
var Objects = map[string][]string{
	"Account":     {"Id", "Name", "Industry", "AccountNumber"},
	"Contact":     {"Id", "AccountId", "Name", "Title", "Email"},
	"Opportunity": {"Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"},
	"Contract":    {"Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"},
}

var (
	soql = regexp.MustCompile(`^SELECT ([A-Za-z, ]+) FROM (Account|Contact|Opportunity|Contract) ` +
		`WHERE (Id|AccountId) = '([A-Za-z0-9]{18})' ORDER BY Id LIMIT (101|1001)$`)
	// An account search by a literal part of the name, or of every account, either within a selection.
	search = regexp.MustCompile(`^SELECT Id, Name, Industry, AccountNumber FROM Account` +
		`(?: WHERE (?:Name LIKE '%((?:[^\\']|\\.)*)%')?(?: AND )?(?:Id IN \(([^)]*)\))?)? ORDER BY Name, Id LIMIT 101$`)
	// The opportunities of a selection of accounts.
	selectedOpportunities = regexp.MustCompile(`^SELECT Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, ` +
		`IsWon FROM Opportunity WHERE AccountId IN \(([^)]*)\) ORDER BY Id LIMIT 101$`)
	quotedID = regexp.MustCompile(`'([A-Za-z0-9]{18})'`)
	// The contract a renewal plans from.
	candidate = regexp.MustCompile(`^SELECT Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate ` +
		`FROM Contract WHERE EndDate >= ([0-9]{4}-[0-9]{2}-[0-9]{2}) AND StatusCode IN \('Draft', 'InApproval', ` +
		`'Activated'\) ORDER BY EndDate, Id LIMIT 1$`)
	unescaped = regexp.MustCompile(`\\(.)`)
)

type cursor struct {
	object, account string
	rows            []*strictjson.Object
	start           int
}

// Org is an invented Salesforce org over HTTP: the reader's SOQL, describes and paged results. It is the
// transport a Reader is given.
type Org struct {
	// Token is the bearer the org accepts. Any other gets HTTP 401.
	Token string
	// Edition, Sandbox and ID are the Organization row.
	Edition string
	Sandbox bool
	ID      string
	// UserID and Username are the user every token reads as, unless Users names another for a token.
	UserID, Username string
	Users            map[string][2]string
	// Intercept, when set, may answer a request in the org's place. Nil from it lets the org answer.
	Intercept func(*http.Request) *http.Response

	mu       sync.Mutex
	rows     map[string]map[string][]*strictjson.Object // object, then account ID
	pageSize map[[2]string]int
	cursors  map[string]cursor
	served   int
	// Requests are the requests the org got, each as its path and, for a query, its SOQL.
	Requests []string
	// Unexpected are the requests the org does not serve. A test fails on any.
	Unexpected []string
}

// NewOrg is an empty Enterprise Edition production org.
func NewOrg() *Org {
	o := &Org{Token: Token, Edition: "Enterprise Edition", ID: OrganizationID, UserID: UserID, Username: Username,
		Users: map[string][2]string{}, pageSize: map[[2]string]int{},
		cursors: map[string]cursor{}, rows: map[string]map[string][]*strictjson.Object{}}
	for object := range Objects {
		o.rows[object] = map[string][]*strictjson.Object{}
	}
	return o
}

// Add puts one account in the org, as recorded: {"Account": [row], "Contact": [...], "Opportunity": [...],
// "Contract": [...]}, where an Amount is {"decimal": "its exact text"}. It gives the account's canonical ID.
func (o *Org) Add(rows *strictjson.Object) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	accounts, _ := rows.Get("Account")
	id, _ := accounts.([]any)[0].(*strictjson.Object).Get("Id")
	native := id.(string)
	for object := range Objects {
		list, _ := rows.Get(object)
		held := []*strictjson.Object{}
		for _, row := range list.([]any) {
			held = append(held, row.(*strictjson.Object))
		}
		sort.SliceStable(held, func(i, j int) bool { return idOf(held[i]) < idOf(held[j]) })
		o.rows[object][native] = held
	}
	return "acc-sf-" + native
}

// PageSize sets how many rows of one object the org sends per page for one account. It is 2,000 otherwise,
// as in a real org.
func (o *Org) PageSize(object, native string, size int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pageSize[[2]string{object, native}] = size
}

func idOf(row *strictjson.Object) string {
	id, _ := row.Get("Id")
	text, _ := id.(string)
	return text
}

// Respond is a JSON response with this status.
func Respond(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// RoundTrip answers one request of the reader.
func (o *Org) RoundTrip(request *http.Request) (*http.Response, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	path, query := request.URL.Path, request.URL.Query().Get("q")
	o.Requests = append(o.Requests, strings.TrimSuffix(path+" "+query, " "))
	if o.Intercept != nil {
		if response := o.Intercept(request); response != nil {
			response.Request = request
			return response, nil
		}
	}
	response := o.answer(request, path, query)
	response.Request = request
	return response, nil
}

func (o *Org) answer(request *http.Request, path, query string) *http.Response {
	if request.Header.Get("Authorization") != "Bearer "+o.Token {
		return Respond(401, `[{"errorCode": "INVALID_SESSION_ID", "message": "invented"}]`)
	}
	if request.Method != http.MethodGet || request.URL.Scheme != "https" || "https://"+request.URL.Host != Instance {
		return o.refuse(request)
	}
	if path == UserInfoPath && query == "" {
		user, named := o.Users[strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")]
		if !named {
			user = [2]string{o.UserID, o.Username}
		}
		return Respond(200, encode(strictjson.NewObject("sub", "https://login.salesforce.com/id/"+o.ID+"/"+user[0],
			"user_id", user[0], "organization_id", o.ID, "preferred_username", user[1], "name", "Invented Reader")))
	}
	parts := strings.Split(path, "/")
	if object := parts[len(parts)-2]; strings.HasSuffix(path, "/describe") && Objects[object] != nil {
		fields := []any{}
		for _, field := range Objects[object] {
			fields = append(fields, strictjson.NewObject("name", field))
		}
		return Respond(200, encode(strictjson.NewObject("name", object, "queryable", true, "fields", fields)))
	}
	var page cursor
	switch held, paging := o.cursors[path]; {
	case paging:
		delete(o.cursors, path)
		page = held
	case path != QueryPath:
		return o.refuse(request)
	case query == "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1":
		page = cursor{object: "Organization", rows: []*strictjson.Object{
			strictjson.NewObject("Id", o.ID, "OrganizationType", o.Edition, "IsSandbox", o.Sandbox)}}
	case search.MatchString(query):
		asked := search.FindStringSubmatch(query)
		page = cursor{object: "Account", rows: o.named(asked[1], selected(asked[2]))}
	case selectedOpportunities.MatchString(query):
		var rows []*strictjson.Object
		for account := range selected(selectedOpportunities.FindStringSubmatch(query)[1]) {
			rows = append(rows, o.rows["Opportunity"][account]...)
		}
		sort.SliceStable(rows, func(i, j int) bool { return idOf(rows[i]) < idOf(rows[j]) })
		page = cursor{object: "Opportunity", rows: rows[:min(len(rows), 101)]}
	case candidate.MatchString(query):
		page = cursor{object: "Contract", rows: o.ending(candidate.FindStringSubmatch(query)[1])}
	default:
		asked := soql.FindStringSubmatch(query)
		if asked == nil || asked[1] != strings.Join(Objects[asked[2]], ", ") ||
			(asked[3] == "Id") != (asked[2] == "Account") || (asked[5] == "101") != (asked[2] == "Account") {
			return o.refuse(request)
		}
		// ORDER BY Id LIMIT 1001 for an account's records, and totalSize counts what the limit let through.
		rows := o.rows[asked[2]][asked[4]]
		if len(rows) > 1001 {
			rows = rows[:1001]
		}
		page = cursor{object: asked[2], account: asked[4], rows: rows}
	}
	size, sized := o.pageSize[[2]string{page.object, page.account}]
	if !sized {
		size = 2000
	}
	end := min(page.start+size, len(page.rows))
	records := []any{}
	for _, row := range page.rows[page.start:end] {
		record := strictjson.NewObject("attributes", strictjson.NewObject("type", page.object,
			"url", "/services/data/v68.0/sobjects/"+page.object+"/"+idOf(row)))
		for i, key := range row.Keys {
			record.Set(key, row.Values[i])
		}
		records = append(records, record)
	}
	document := strictjson.NewObject("totalSize", len(page.rows), "done", end >= len(page.rows), "records", records)
	if end < len(page.rows) {
		o.served++
		next := fmt.Sprintf("%s/01gInvented%04d-%d", QueryPath, o.served, end)
		document.Set("nextRecordsUrl", next)
		page.start = end
		o.cursors[next] = page
	}
	return Respond(200, encode(document))
}

// selected is the account IDs of an IN list, or nil for none given.
func selected(list string) map[string]bool {
	if list == "" {
		return nil
	}
	ids := map[string]bool{}
	for _, quoted := range quotedID.FindAllStringSubmatch(list, -1) {
		ids[quoted[1]] = true
	}
	return ids
}

// named is the accounts whose name holds this escaped LIKE text, in any letter case, among the selected ones
// when there is a selection, by name and then ID, and at most 101 of them, as a search reads them.
func (o *Org) named(escaped string, within map[string]bool) []*strictjson.Object {
	part := unicodetab.LowerString(unescaped.ReplaceAllString(escaped, "$1"))
	var found []*strictjson.Object
	for account, rows := range o.rows["Account"] {
		if within != nil && !within[account] {
			continue
		}
		for _, row := range rows {
			name, _ := row.Get("Name")
			if text, _ := name.(string); strings.Contains(unicodetab.LowerString(text), part) {
				found = append(found, row)
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		a, _ := found[i].Get("Name")
		b, _ := found[j].Get("Name")
		if a != b {
			return a.(string) < b.(string)
		}
		return idOf(found[i]) < idOf(found[j])
	})
	return found[:min(len(found), 101)]
}

// ending is the one Draft, InApproval or Activated contract with the earliest EndDate on or after the day,
// ties broken by ID, or none.
func (o *Org) ending(day string) []*strictjson.Object {
	var first *strictjson.Object
	var firstEnd string
	for _, rows := range o.rows["Contract"] {
		for _, row := range rows {
			value, _ := row.Get("EndDate")
			status, _ := row.Get("StatusCode")
			end, dated := value.(string)
			if !dated || end < day || status != "Draft" && status != "InApproval" && status != "Activated" {
				continue
			}
			if first == nil || end < firstEnd || end == firstEnd && idOf(row) < idOf(first) {
				first, firstEnd = row, end
			}
		}
	}
	if first == nil {
		return []*strictjson.Object{}
	}
	return []*strictjson.Object{first}
}

// refuse keeps a request this org does not know for the test to fail on, and answers it with no data.
func (o *Org) refuse(request *http.Request) *http.Response {
	o.Unexpected = append(o.Unexpected, request.Method+" "+request.URL.String())
	return Respond(400, `[{"errorCode": "MALFORMED_QUERY", "message": "unexpected"}]`)
}

// encode writes a value as JSON. {"decimal": text} is written as that number, digit for digit.
func encode(value any) string {
	switch v := value.(type) {
	case *strictjson.Object:
		if text, marked := v.Get("decimal"); marked && len(v.Keys) == 1 {
			return text.(string)
		}
		members := make([]string, len(v.Keys))
		for i, key := range v.Keys {
			members[i] = strictjson.Quote(key) + ": " + encode(v.Values[i])
		}
		return "{" + strings.Join(members, ", ") + "}"
	case []any:
		items := make([]string, len(v))
		for i, item := range v {
			items[i] = encode(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	}
	text, err := strictjson.Dumps(value, strictjson.Quoted)
	if err != nil {
		panic(err)
	}
	return text
}

// SF is an invented sf holding one login. It answers the two commands a login read runs, and keeps every
// command it was given.
type SF struct {
	// Instance and Token are what the login holds now.
	Instance, Token string
	// OrganizationID and Username are the org and the user sf org display names for the alias.
	OrganizationID, Username string
	// Answer, when set, answers a command in the login's place.
	Answer func(ctx context.Context, arguments []string) (output []byte, status int, err error)

	mu       sync.Mutex
	Commands [][]string
}

// NewSF is the invented login of Alias.
func NewSF() *SF {
	return &SF{Instance: Instance, Token: Token, OrganizationID: OrganizationID, Username: Username}
}

// Run answers sf org display and sf org auth show-access-token for Alias as sf --json does, and fails any
// other command as sf fails one it cannot run.
func (s *SF) Run(ctx context.Context, arguments []string) ([]byte, int, error) {
	s.mu.Lock()
	s.Commands = append(s.Commands, append([]string(nil), arguments...))
	instance, token, named, username, answer := s.Instance, s.Token, s.OrganizationID, s.Username, s.Answer
	s.mu.Unlock()
	if answer != nil {
		return answer(ctx, arguments)
	}
	result := map[string]*strictjson.Object{
		"org display --target-org " + Alias + " --json": strictjson.NewObject("id", named,
			"instanceUrl", instance, "username", username, "alias", Alias, "connectedStatus", "Connected"),
		"org auth show-access-token --target-org " + Alias + " --json": strictjson.NewObject("accessToken", token),
	}[strings.Join(arguments, " ")]
	if result == nil {
		return []byte(`{"status": 1, "name": "NamedOrgNotFoundError", "exitCode": 1}`), 1, nil
	}
	return []byte(encode(strictjson.NewObject("status", 0, "result", result, "warnings", []any{}))), 0, nil
}
