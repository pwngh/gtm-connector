// Package salesforce reads standard Account, Contact, Opportunity and Contract records of one Salesforce
// org through REST v68.0, under the credentials of an existing sf CLI login. It never writes to
// Salesforce.
//
// FromSF asks sf for the instance address and the token of one alias and keeps both in memory.
// FromEnvironment takes them from the variables of the process instead, for a connector with no sf. Every
// read is one bounded operation: 20 seconds of read time, 12 GET attempts, 1 MiB of response data and 10
// pages a query. The first operation checks the org against the selected environment and, outside
// Developer Edition, that the four objects are queryable and expose the mapped fields. A first HTTP 401 in
// an operation asks sf, or the variables, for credentials once more, confirms the org and the user are the
// same ones, and retries.
//
// A failure is an *Error holding one fixed code. It never holds an upstream message, a URL or a credential.
// An answer that holds a token of the read is refused whole, so a token never becomes a record. A read whose
// context ends returns that context's error, and ends sf with anything sf started.
package salesforce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/sources/echo"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

const (
	apiVersion        = "v68.0"
	queryPath         = "/services/data/" + apiVersion + "/query"
	organizationQuery = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"
	accountFields     = "Id, Name, Industry, AccountNumber"
	contactFields     = "Id, AccountId, Name, Title, Email"
	contractFields    = "Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate"
	opportunityFields = "Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, IsWon"
	// usersQuery reads the org's Standard users. A portal or community user can be a customer, so only a
	// Standard user is read as one of the seller's own people.
	usersQuery = "SELECT Id, Name, Email, IsActive FROM User WHERE UserType = 'Standard' ORDER BY Id LIMIT %d"
)

// MaxUsers is how many Standard users a users read returns. An org with more is refused whole.
const MaxUsers = 2000

// The bounds of one operation.
const (
	maxRows = 100
	// One account's contacts, opportunities or contracts. Enterprise accounts hold hundreds of contacts.
	accountRows   = 1000
	maxPages      = 10
	maxRequests   = 12
	maxBytes      = 1024 * 1024
	operationTime = 20 * time.Second
	requestTime   = 5 * time.Second
)

// The codes a read fails with.
const (
	invalidConfiguration = "salesforce_invalid_configuration"
	invalidArguments     = "salesforce_invalid_arguments"
	editionRequired      = "salesforce_edition_required"
	authRequired         = "salesforce_auth_required"
	cliTimeout           = "salesforce_cli_timeout"
	rateLimited          = "salesforce_rate_limited"
	forbidden            = "salesforce_forbidden"
	httpError            = "salesforce_http_error"
	unavailable          = "salesforce_unavailable"
	timedOut             = "salesforce_timeout"
	invalidResponse      = "salesforce_invalid_response"
	resultLimit          = "salesforce_result_limit"
	paginationInvalid    = "salesforce_pagination_invalid"
	environmentMismatch  = "salesforce_environment_mismatch"
	apiDisabled          = "salesforce_api_disabled"
	schemaUnavailable    = "salesforce_schema_unavailable"
	accountNumberUnclear = "salesforce_account_number_ambiguous"
)

// Error is a failed read: one fixed diagnostic code. Upstream messages and credentials stay private.
type Error struct {
	Code string
	// Detail is a fixed sentence that says more than the code, such as the mode of a token file refused
	// for it, or "". It never holds a value read from a source, a path or a credential.
	Detail string
}

func (e *Error) Error() string { return e.Code }

func refuse(code string) error { return &Error{Code: code} }

// codeOf is the code of a failed read, or "" for any other error.
func codeOf(err error) string {
	var failed *Error
	if errors.As(err, &failed) {
		return failed.Code
	}
	return ""
}

// ErrAccountNotFound is get_account's refusal of an account the org does not return.
var ErrAccountNotFound = errors.New("account not found")

// required is the fields each object must expose outside Developer Edition, in the order they are checked.
var required = []struct {
	object string
	fields []string
}{
	{"Account", []string{"Id", "Name", "Industry", "AccountNumber"}},
	{"Contact", []string{"Id", "AccountId", "Name", "Title", "Email"}},
	{"Opportunity", []string{"Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"}},
	{"Contract", []string{"Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"}},
}

var (
	hostPattern    = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+my\.salesforce\.com$`)
	locatorPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(queryPath) + `/[A-Za-z0-9_-]{1,128}$`)
	fieldPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,255}$`)
)

func asciiAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// nativeID is an 18-character Salesforce ID, or a refusal of the response that held anything else.
func nativeID(value any) (string, error) {
	id, ok := value.(string)
	if !ok || len(id) != 18 {
		return "", refuse(invalidResponse)
	}
	for i := 0; i < len(id); i++ {
		if !asciiAlnum(id[i]) {
			return "", refuse(invalidResponse)
		}
	}
	return id, nil
}

// accountNative is the Salesforce ID inside a canonical account ID, acc-sf- followed by 18 characters.
func accountNative(accountID string) (string, error) {
	native, found := strings.CutPrefix(accountID, "acc-sf-")
	if _, err := nativeID(native); !found || err != nil {
		return "", refuse(invalidArguments)
	}
	return native, nil
}

// selectedIDs is a fixed IN clause from at most 100 canonical account IDs. A nil selection is none, and
// gives "".
func selectedIDs(accountIDs []string, field string) (string, error) {
	if accountIDs == nil {
		return "", nil
	}
	if len(accountIDs) < 1 || len(accountIDs) > 100 {
		return "", refuse(invalidArguments)
	}
	seen := map[string]bool{}
	var native []string
	for _, accountID := range accountIDs {
		id, err := accountNative(accountID)
		if err != nil {
			return "", err
		}
		if !seen[id] {
			seen[id] = true
			native = append(native, "'"+id+"'")
		}
	}
	sort.Strings(native)
	return field + " IN (" + strings.Join(native, ", ") + ")", nil
}

// likeLiteral is search text as the inside of a SOQL LIKE pattern that matches it literally.
func likeLiteral(value string) (string, error) {
	if utf8.RuneCountInString(value) > 200 {
		return "", refuse(invalidArguments)
	}
	var b strings.Builder
	for _, c := range value {
		switch {
		case c < 32 || c == 127:
			return "", refuse(invalidArguments)
		case c == '\\' || c == '\'' || c == '%' || c == '_':
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String(), nil
}

// validAccountSearch is tool_definitions.valid_account_search: an exact account number cannot be combined
// with a name query, and is not normalized.
func validAccountSearch(query string, accountNumber *string) bool {
	if accountNumber == nil {
		return true
	}
	if query != "" || utf8.RuneCountInString(*accountNumber) > 40 {
		return false
	}
	blank := true
	for _, c := range *accountNumber {
		if c < 32 || c >= 127 && c <= 159 {
			return false
		}
		blank = blank && unicodetab.IsSpace(c)
	}
	return !blank
}

// configure checks an instance address and a token, and gives the origin every request goes to. The address
// must be an HTTPS My Domain origin ending in .my.salesforce.com, with no path, query, credentials or
// nonstandard port. The token must be 20 to 4,096 characters, each a printable ASCII character other than a space:
// a shorter one would be found in every answer the org gives.
func configure(instanceURL, accessToken string) (origin string, err error) {
	if utf8.RuneCountInString(instanceURL) > 512 {
		return "", refuse(invalidConfiguration)
	}
	for _, c := range instanceURL {
		if c <= 32 {
			return "", refuse(invalidConfiguration)
		}
	}
	// The address may differ from its origin only by letter case.
	host, found := strings.CutPrefix(unicodetab.LowerString(instanceURL), "https://")
	host = strings.TrimSuffix(host, ":443")
	if !found || len(host) > 253 || !hostPattern.MatchString(host) {
		return "", refuse(invalidConfiguration)
	}
	if !echo.Usable(accessToken, maxToken) {
		return "", refuse(invalidConfiguration)
	}
	return "https://" + host, nil
}

// maxToken is the longest token a reader takes.
const maxToken = 4096

func validEnvironment(environment string) bool {
	return environment == "developer" || environment == "sandbox" || environment == "production"
}

// Options are how a Reader is made. The zero value reads a Developer Edition org over the network with the
// sf on PATH.
type Options struct {
	// Environment is developer (the default), sandbox or production.
	Environment string
	// Runner runs sf. Nil is Exec{}, the sf on PATH.
	Runner Runner
	// Transport carries the HTTPS requests. Nil dials the instance with certificate checks and no proxy, over
	// HTTP/1.1 with one connection a request.
	Transport http.RoundTripper
	// Now is the clock the operation budget is read from. Nil is time.Now.
	Now func() time.Time
}

// Reader reads one org. It runs one operation at a time.
type Reader struct {
	environment string
	instanceURL string
	client      *http.Client
	// credentials give the login again on a first HTTP 401: sf for an alias, or the process environment.
	credentials loginSource
	now         func() time.Time
	// turn holds the one operation that is running. The fields below belong to it.
	turn            chan struct{}
	accessToken     string
	editionVerified bool
	organizationID  string
	// signedInTo is the org ID sf org display named for the alias, and username the user it named. The token
	// must open that org as that user. A token handed to the process names neither.
	signedInTo, username string
	// userID is the ID of the user the first token read as. Every later token must read as the same user.
	userID  string
	checked map[string]bool
	// tokens are every token this Reader has held or been given, for as long as it lives. An answer that
	// holds any of them is refused.
	tokens echo.Tokens
}

// FromSF captures the credentials of an existing sf login, by its alias, and makes the Reader of that org.
// It runs sf org display and sf org auth show-access-token, and reads nothing from the org.
func FromSF(ctx context.Context, alias string, o Options) (*Reader, error) {
	environment := o.Environment
	if environment == "" {
		environment = "developer"
	}
	if !validEnvironment(environment) {
		return nil, refuse(invalidConfiguration)
	}
	runner := o.Runner
	if runner == nil {
		runner = Exec{}
	}
	login, err := newCredentials(alias, runner)
	if err != nil {
		return nil, err
	}
	given, err := login.read(ctx)
	if err != nil {
		return nil, err
	}
	origin, err := configure(given.instanceURL, given.accessToken)
	if err != nil || !plainLabel(given.username) {
		// What sf gave is not a usable login.
		return nil, refuse(authRequired)
	}
	return newReader(environment, origin, given, login, o), nil
}

// DefaultTransport is the transport a Reader makes when it is given none: sourcehttp's, which dials out
// only in a process whose main allowed it (sourcehttp.Live).
func DefaultTransport() http.RoundTripper { return sourcehttp.Transport(sourcehttp.Direct, nil) }

// operation runs one bounded read, after the checks every first read makes: the org against the selected
// environment, and outside Developer Edition the four objects' fields.
func (r *Reader) operation(ctx context.Context, run func(o *operation) error) error {
	return r.bounded(ctx, true, run)
}

// bounded is operation, with the fields of the four objects checked or not.
func (r *Reader) bounded(ctx context.Context, capabilities bool, run func(o *operation) error) error {
	select {
	case r.turn <- struct{}{}:
	case <-ctx.Done():
		return stopped(ctx)
	}
	defer func() { <-r.turn }()
	o := &operation{reader: r, ctx: ctx, token: r.accessToken, deadline: r.now().Add(operationTime)}
	if !r.editionVerified {
		if err := o.firstCheck(); err != nil {
			return err
		}
		// A token sf gave during this first check is kept only now that it has proved the org.
		if o.unproved != "" {
			r.accessToken, o.unproved = o.unproved, ""
		}
		r.editionVerified = true
	}
	if capabilities && r.environment != "developer" {
		if err := o.checkCapabilities(); err != nil {
			return err
		}
	}
	return run(o)
}

// firstCheck reads the Organization row and checks it against the selected environment, then asks which
// user the token reads as. A token that expires between the two, so that the org answers the first and
// refuses the second, is replaced once, as any first HTTP 401 of an operation is, and both checks run again
// under the replacement, inside the same operation's budget.
func (o *operation) firstCheck() error {
	for {
		organization, err := o.query(organizationQuery, 1)
		if err == nil {
			err = o.reader.validateEnvironment(organization)
		}
		if err == nil {
			err = o.checkUser()
		}
		if err != errUserRefused {
			return err
		}
		token, err := o.refresh()
		if err != nil {
			return err
		}
		// Kept only once both checks pass under it.
		o.unproved = token
	}
}

// validateEnvironment checks the Organization row against the selected environment, and that the org is the
// one this Reader saw before.
func (r *Reader) validateEnvironment(organization []*strictjson.Object) error {
	var edition any
	if len(organization) == 1 {
		edition, _ = organization[0].Get("OrganizationType")
	}
	if r.environment == "developer" && edition != "Developer Edition" {
		return refuse(editionRequired)
	}
	name, ok := edition.(string)
	if len(organization) != 1 || !ok || !plainLabel(name) {
		return refuse(invalidResponse)
	}
	sandboxValue, _ := organization[0].Get("IsSandbox")
	sandbox, isBool := sandboxValue.(bool)
	idValue, _ := organization[0].Get("Id")
	organizationID, err := nativeID(idValue)
	if err != nil || !isBool || !strings.HasPrefix(organizationID, "00D") {
		return refuse(invalidResponse)
	}
	switch {
	case r.environment == "developer" && sandbox,
		r.environment == "sandbox" && !sandbox,
		r.environment == "production" && (sandbox || name == "Developer Edition"):
		return refuse(environmentMismatch)
	case r.organizationID != "" && organizationID != r.organizationID,
		// The token opens another org than the one sf org display named for the alias.
		r.signedInTo != "" && organizationID != r.signedInTo:
		return refuse(authRequired)
	}
	r.organizationID = organizationID
	return nil
}

// plainLabel is an edition name of 1 to 128 printable characters with no space at either end.
func plainLabel(name string) bool {
	runes := []rune(name)
	if len(runes) < 1 || len(runes) > 128 || unicodetab.IsSpace(runes[0]) || unicodetab.IsSpace(runes[len(runes)-1]) {
		return false
	}
	for _, c := range runes {
		if !unicodetab.IsPrintable(c) {
			return false
		}
	}
	return true
}

// Identity is what pins a folder to one Salesforce connection: the org, and the user it is read as.
type Identity struct {
	InstanceURL    string
	OrganizationID string
	Environment    string
	UserID         string
}

// Pin is what a workspace is pinned to: the SHA-256 of the identity with the ID of the user it is read as,
// in the canonical encoding. Another user of the same org has another pin.
func (i Identity) Pin() string {
	value := i.Value()
	value.Set("user_id", i.UserID)
	canonical, err := strictjson.Dumps(value, strictjson.Prepared)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Value is the identity as crm-connection.json holds it.
func (i Identity) Value() *strictjson.Object {
	return strictjson.NewObject("schema_version", 1, "backend", "salesforce", "instance_url", i.InstanceURL,
		"organization_id", i.OrganizationID, "environment", i.Environment)
}

// Binding is the CRM binding a card records: the digest of the identity in the canonical encoding
// (runtime._calls_binding).
func (i Identity) Binding() *strictjson.Object {
	canonical, err := strictjson.Dumps(i.Value(), strictjson.Prepared)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256([]byte(canonical))
	return strictjson.NewObject("schema_version", 1, "crm_backend", "salesforce",
		"identity_sha256", hex.EncodeToString(sum[:]))
}

// identity is the org and the user this Reader has checked.
func (r *Reader) identity() Identity {
	return Identity{InstanceURL: r.instanceURL, OrganizationID: r.organizationID, Environment: r.environment,
		UserID: r.userID}
}

// PrepareConnection gives the org's identity once the environment and schema checks pass. It reads no record.
func (r *Reader) PrepareConnection(ctx context.Context) (Identity, error) {
	var identity Identity
	err := r.operation(ctx, func(*operation) error {
		identity = r.identity()
		return nil
	})
	return identity, err
}

// CheckEnvironment gives the org's identity once it is checked against the selected environment. It
// describes no object and reads no record, so that the fields of each object can be checked apart.
func (r *Reader) CheckEnvironment(ctx context.Context) (Identity, error) {
	var identity Identity
	err := r.bounded(ctx, false, func(*operation) error {
		identity = r.identity()
		return nil
	})
	return identity, err
}

// Objects are the four objects the reader maps, in the order they are checked.
func Objects() []string {
	names := make([]string, len(required))
	for i, object := range required {
		names[i] = object.object
	}
	return names
}

// MissingFields describes one of the four objects and gives how many fields of it are mapped, and those it
// does not expose, in the order they are mapped. An object that is not queryable is salesforce_forbidden.
func (r *Reader) MissingFields(ctx context.Context, object string) (mapped int, missing []string, err error) {
	err = r.bounded(ctx, false, func(o *operation) error {
		for _, each := range required {
			if each.object != object {
				continue
			}
			names, err := o.describe(object)
			if err != nil {
				return err
			}
			mapped, missing = len(each.fields), []string{}
			for _, field := range each.fields {
				if !names[field] {
					missing = append(missing, field)
				}
			}
			return nil
		}
		return refuse(invalidArguments)
	})
	return mapped, missing, err
}

// RenewalCandidate reads the Contract with the earliest EndDate on or after the day, among Draft,
// InApproval and Activated contracts, ties broken by ID: the contract a card of its account plans from,
// with its record_ref. It is nil when the org holds none.
func (r *Reader) RenewalCandidate(ctx context.Context, asOf string) (*strictjson.Object, error) {
	if _, err := evidence.Day(asOf); err != nil {
		return nil, refuse(invalidArguments)
	}
	var candidate *strictjson.Object
	err := r.operation(ctx, func(o *operation) error {
		rows, err := o.query("SELECT "+contractFields+" FROM Contract WHERE EndDate >= "+asOf+
			" AND StatusCode IN ('Draft', 'InApproval', 'Activated') ORDER BY EndDate, Id LIMIT 1", 1)
		if err != nil || len(rows) == 0 {
			return err
		}
		related, _ := rows[0].Get("AccountId")
		account, err := nativeID(related)
		if err != nil {
			return err
		}
		record, err := contractRecord(rows[0], account)
		if err != nil {
			return err
		}
		ends, _ := record.Get("end_date")
		if end, _ := ends.(string); end == "" || end < asOf {
			return refuse(invalidResponse)
		}
		candidate = referenced("contracts", []*strictjson.Object{record})[0]
		return nil
	})
	return candidate, err
}

// Account is one account with its contacts and its contracts, each ordered by ID.
type Account struct {
	Record    *strictjson.Object
	Contacts  []*strictjson.Object
	Contracts []*strictjson.Object
}

// GetAccount reads one account, its contacts and its contracts in one operation. An account the org does not
// return is nil.
func (r *Reader) GetAccount(ctx context.Context, accountID string) (*Account, error) {
	native, err := accountNative(accountID)
	if err != nil {
		return nil, err
	}
	var account *Account
	err = r.operation(ctx, func(o *operation) error {
		rows, err := o.query("SELECT "+accountFields+" FROM Account WHERE Id = '"+native+"' ORDER BY Id LIMIT 101", 1)
		if err != nil || len(rows) == 0 {
			return err
		}
		if id, _ := rows[0].Get("Id"); id != native {
			return refuse(invalidResponse)
		}
		record, err := accountRecord(rows[0])
		if err != nil {
			return err
		}
		found := &Account{Record: record}
		owned := fmt.Sprintf("WHERE AccountId = '%s' ORDER BY Id LIMIT %d", native, accountRows+1)
		if found.Contacts, err = o.records("SELECT "+contactFields+" FROM Contact "+owned, accountRows,
			func(row *strictjson.Object) (*strictjson.Object, error) { return contactRecord(row, native) }); err != nil {
			return err
		}
		if found.Contracts, err = o.records("SELECT "+contractFields+" FROM Contract "+owned, accountRows,
			func(row *strictjson.Object) (*strictjson.Object, error) { return contractRecord(row, native) }); err != nil {
			return err
		}
		account = found
		return nil
	})
	return account, err
}

// ListOpportunities reads the opportunities of one account, ordered by ID.
func (r *Reader) ListOpportunities(ctx context.Context, accountID string) ([]*strictjson.Object, error) {
	native, err := accountNative(accountID)
	if err != nil {
		return nil, err
	}
	return r.opportunities(ctx, " WHERE AccountId = '"+native+"'", accountRows, native, nil)
}

// ListAllOpportunities reads at most 100 opportunities across accounts, ordered by ID. A selection limits
// the query itself to its accounts; nil is no selection.
func (r *Reader) ListAllOpportunities(ctx context.Context, selected []string) ([]*strictjson.Object, error) {
	where, err := selectedIDs(selected, "AccountId")
	if err != nil {
		return nil, err
	}
	if where != "" {
		where = " WHERE " + where
	}
	return r.opportunities(ctx, where, maxRows, "", selected)
}

func (r *Reader) opportunities(ctx context.Context, where string, limit int, native string,
	selected []string) ([]*strictjson.Object, error) {
	var found []*strictjson.Object
	err := r.operation(ctx, func(o *operation) error {
		var err error
		found, err = o.records(
			fmt.Sprintf("SELECT %s FROM Opportunity%s ORDER BY Id LIMIT %d", opportunityFields, where, limit+1), limit,
			func(row *strictjson.Object) (*strictjson.Object, error) { return opportunityRecord(row, native) })
		if err != nil {
			return err
		}
		if selected != nil {
			for _, record := range found {
				if accountID, _ := record.Get("account_id"); !among(accountID, selected) {
					return refuse(invalidResponse)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func among(value any, list []string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, item := range list {
		if item == text {
			return true
		}
	}
	return false
}

// SearchAccounts gives the accounts whose name holds the query as literal text, the first 100 by name and
// then ID, and whether more match. With an account number it gives the accounts with exactly that
// AccountNumber. A selection limits the query to its accounts; nil is no selection.
func (r *Reader) SearchAccounts(ctx context.Context, query string, accountNumber *string,
	selected []string) (accounts []*strictjson.Object, more bool, err error) {
	if !validAccountSearch(query, accountNumber) {
		return nil, false, refuse(invalidArguments)
	}
	selection, err := selectedIDs(selected, "Id")
	if err != nil {
		return nil, false, err
	}
	where, order, limit := "", "Name, Id", maxRows+1
	if accountNumber != nil {
		escaped := strings.ReplaceAll(strings.ReplaceAll(*accountNumber, `\`, `\\`), `'`, `\'`)
		where, order, limit = " WHERE AccountNumber = '"+escaped+"'", "Id", 2
	} else {
		escaped, err := likeLiteral(query)
		if err != nil {
			return nil, false, err
		}
		// The row past the bound is read only to learn that it exists.
		if query != "" {
			where = " WHERE Name LIKE '%" + escaped + "%'"
		}
	}
	if selection != "" && where != "" {
		where += " AND " + selection
	} else if selection != "" {
		where = " WHERE " + selection
	}
	err = r.operation(ctx, func(o *operation) error {
		var err error
		accounts, err = o.records(
			fmt.Sprintf("SELECT %s FROM Account%s ORDER BY %s LIMIT %d", accountFields, where, order, limit), limit,
			accountRecord)
		if err != nil {
			return err
		}
		for _, account := range accounts {
			if id, _ := account.Get("id"); selected != nil && !among(id, selected) {
				return refuse(invalidResponse)
			}
		}
		if accountNumber != nil {
			for _, account := range accounts {
				if number, _ := account.Get("account_number"); number == nil {
					return refuse(invalidResponse)
				}
			}
			for _, account := range accounts {
				if number, _ := account.Get("account_number"); number != *accountNumber {
					return refuse(accountNumberUnclear)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(accounts) > maxRows {
		return accounts[:maxRows], true, nil
	}
	return accounts, false, nil
}

// ListUsers reads the org's Standard users, active or not, ordered by ID: at most MaxUsers. An org with more
// is refused with salesforce_result_limit.
func (r *Reader) ListUsers(ctx context.Context) ([]*strictjson.Object, error) {
	var found []*strictjson.Object
	err := r.operation(ctx, func(o *operation) error {
		var err error
		found, err = o.records(fmt.Sprintf(usersQuery, MaxUsers+1), MaxUsers, userRecord)
		return err
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Read is one of the CRM reads a card makes, as the gateway's tool bodies give them: get_account is the
// account, then its contacts, then its contracts, list_opportunities its opportunities, and list_users the
// org's Standard users, each record with its record_ref. list_users is made for one account's card, so it
// checks the form of that account's ID, but the users it returns are the org's.
func (r *Reader) Read(ctx context.Context, tool, accountID string) (system string, records []*strictjson.Object, err error) {
	switch tool {
	case "get_account":
		account, err := r.GetAccount(ctx, accountID)
		if err != nil {
			return "", nil, err
		}
		if account == nil {
			return "", nil, ErrAccountNotFound
		}
		records = referenced("accounts", []*strictjson.Object{account.Record})
		records = append(records, referenced("contacts", account.Contacts)...)
		return "crm", append(records, referenced("contracts", account.Contracts)...), nil
	case "list_opportunities":
		opportunities, err := r.ListOpportunities(ctx, accountID)
		if err != nil {
			return "", nil, err
		}
		return "crm", referenced("opportunities", opportunities), nil
	case "list_users":
		if _, err := accountNative(accountID); err != nil {
			return "", nil, err
		}
		users, err := r.ListUsers(ctx)
		if err != nil {
			return "", nil, err
		}
		return "crm", referenced("users", users), nil
	}
	return "", nil, fmt.Errorf("salesforce: no read %q", tool)
}

// referenced gives each record its record_ref, crm/<table>/<id>.
func referenced(table string, records []*strictjson.Object) []*strictjson.Object {
	out := make([]*strictjson.Object, len(records))
	for i, record := range records {
		id, _ := record.Get("id")
		out[i] = record.Copy()
		out[i].Set("record_ref", fmt.Sprintf("crm/%s/%s", table, id))
	}
	return out
}

// text is a member that must be a string, or null where the field may be empty.
func text(row *strictjson.Object, field string, nullable bool) (any, error) {
	value, ok := row.Get(field)
	if _, isText := value.(string); ok && (isText || nullable && value == nil) {
		return value, nil
	}
	return nil, refuse(invalidResponse)
}

// day is a member that must be a real day written YYYY-MM-DD, or null where the field may be empty.
func day(row *strictjson.Object, field string, nullable bool) (any, error) {
	value, err := text(row, field, nullable)
	if written, ok := value.(string); ok {
		if _, err := evidence.Day(written); err != nil {
			return nil, refuse(invalidResponse)
		}
	}
	return value, err
}

// owner is a row's own ID, after checking that it belongs to the account that was asked for.
func owner(row *strictjson.Object, account string) (string, error) {
	id, _ := row.Get("Id")
	native, err := nativeID(id)
	if err != nil {
		return "", err
	}
	accountID, _ := row.Get("AccountId")
	if related, err := nativeID(accountID); err != nil || related != account {
		return "", refuse(invalidResponse)
	}
	return native, nil
}

// fields reads each named member with its rule and stops at the first refusal.
type fields struct {
	row *strictjson.Object
	err error
}

func (f *fields) text(field string, nullable bool) any {
	value, err := text(f.row, field, nullable)
	if f.err == nil {
		f.err = err
	}
	return value
}

func (f *fields) day(field string, nullable bool) any {
	value, err := day(f.row, field, nullable)
	if f.err == nil {
		f.err = err
	}
	return value
}

func accountRecord(row *strictjson.Object) (*strictjson.Object, error) {
	id, _ := row.Get("Id")
	native, err := nativeID(id)
	if err != nil {
		return nil, err
	}
	f := fields{row: row}
	record := strictjson.NewObject("id", "acc-sf-"+native, "source_id", native, "backend", "salesforce",
		"name", f.text("Name", false), "industry", f.text("Industry", true),
		"account_number", f.text("AccountNumber", true))
	return record, f.err
}

// userRecord maps one User: its ID, name, email and whether it is active.
func userRecord(row *strictjson.Object) (*strictjson.Object, error) {
	id, _ := row.Get("Id")
	native, err := nativeID(id)
	if err != nil {
		return nil, err
	}
	active, _ := row.Get("IsActive")
	if _, isBool := active.(bool); !isBool {
		return nil, refuse(invalidResponse)
	}
	f := fields{row: row}
	record := strictjson.NewObject("id", "usr-sf-"+native, "source_id", native, "backend", "salesforce",
		"name", f.text("Name", false), "email", f.text("Email", true), "is_active", active)
	return record, f.err
}

func contactRecord(row *strictjson.Object, account string) (*strictjson.Object, error) {
	native, err := owner(row, account)
	if err != nil {
		return nil, err
	}
	f := fields{row: row}
	record := strictjson.NewObject("id", "con-sf-"+native, "source_id", native, "backend", "salesforce",
		"account_id", "acc-sf-"+account, "name", f.text("Name", false), "title", f.text("Title", true),
		"email", f.text("Email", true))
	return record, f.err
}

func contractRecord(row *strictjson.Object, account string) (*strictjson.Object, error) {
	native, err := owner(row, account)
	if err != nil {
		return nil, err
	}
	f := fields{row: row}
	statusCode := f.text("StatusCode", false)
	if statusCode != "Draft" && statusCode != "InApproval" && statusCode != "Activated" {
		return nil, refuse(invalidResponse)
	}
	startDate, endDate := f.day("StartDate", true), f.day("EndDate", true)
	// A term is null or a whole number of months that fits 32 bits.
	term, has := row.Get("ContractTerm")
	if months, ok := term.(*big.Int); !has || term != nil && (!ok || !months.IsInt64() ||
		months.Int64() < -(1<<31) || months.Int64() >= 1<<31) {
		return nil, refuse(invalidResponse)
	}
	record := strictjson.NewObject("id", "ctr-sf-"+native, "source_id", native, "backend", "salesforce",
		"account_id", "acc-sf-"+account, "status", f.text("Status", false), "status_code", statusCode,
		"start_date", startDate, "end_date", endDate, "contract_term", term)
	return record, f.err
}

// opportunityRecord maps one Opportunity. With an account, the row must belong to it; without one, the row
// may belong to any account or to none.
func opportunityRecord(row *strictjson.Object, account string) (*strictjson.Object, error) {
	id, _ := row.Get("Id")
	native, err := nativeID(id)
	if err != nil {
		return nil, err
	}
	var accountID any
	related, has := row.Get("AccountId")
	if !has {
		return nil, refuse(invalidResponse)
	}
	if related != nil {
		relatedID, err := nativeID(related)
		if err != nil {
			return nil, err
		}
		related, accountID = relatedID, "acc-sf-"+relatedID
	}
	if account != "" && related != account {
		return nil, refuse(invalidResponse)
	}
	amount, err := amountText(row)
	if err != nil {
		return nil, err
	}
	f := fields{row: row}
	closeDate := f.day("CloseDate", false)
	closed, _ := row.Get("IsClosed")
	won, _ := row.Get("IsWon")
	_, closedIsBool := closed.(bool)
	_, wonIsBool := won.(bool)
	if !closedIsBool || !wonIsBool {
		return nil, refuse(invalidResponse)
	}
	record := strictjson.NewObject("id", "opp-sf-"+native, "source_id", native, "backend", "salesforce",
		"account_id", accountID, "name", f.text("Name", false), "stage", f.text("StageName", false),
		"close_date", closeDate, "is_closed", closed, "is_won", won, "amount_decimal", amount, "currency", nil)
	return record, f.err
}

// amountText is an Amount as a decimal string in fixed point, or nil for null. A number of more than 28
// digits, or with an exponent past 28 either way, is refused.
func amountText(row *strictjson.Object) (any, error) {
	amount, has := row.Get("Amount")
	if !has {
		return nil, refuse(invalidResponse)
	}
	var decimal strictjson.Decimal
	switch number := amount.(type) {
	case nil:
		return nil, nil
	case *big.Int:
		decimal = strictjson.Decimal{Negative: number.Sign() < 0, Coefficient: new(big.Int).Abs(number).String()}
	case strictjson.Decimal:
		decimal = number
	default:
		return nil, refuse(invalidResponse)
	}
	if len(decimal.Coefficient) > 28 || decimal.Exponent > 28 || decimal.Exponent < -28 {
		return nil, refuse(invalidResponse)
	}
	return decimal.Fixed(), nil
}
