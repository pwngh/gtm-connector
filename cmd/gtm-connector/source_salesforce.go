package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Salesforce as a workspace's CRM: an org signed in with sf under an alias, read by internal/sources/salesforce.
//
// connect salesforce --token-env takes the instance address and the token from the variables of the
// process instead (salesforce.InstanceVariable, TokenVariable and TokenFileVariable), for a connector that
// runs where there is no sf, such as a container. Its row's mode is env_token and its binding names no
// alias. Every check is the same, and a first HTTP 401 reads the variables again once in place of sf.
//
// connect salesforce --org ALIAS signs in through sf, checks the org against --environment and, outside
// Developer Edition, the four objects' fields, and saves the alias and the environment with the digest of
// the org's identity: its address, its ID and its environment. The row holds no token, address or ID. Every
// later command signs in again on its first read and reads nothing from an org whose identity is not the
// pinned one. A Salesforce workspace is named with --state-dir: the default workspace holds demo data.
//
// debug salesforce check reports, step by step, what a person needs to set up a connection: the sign-in,
// the environment, the four objects' fields and, in a connected workspace, an account search, the contract
// a renewal plans from and that account's reads, each under a receipt.

// salesforceOptions are how the reader reaches sf and the org. The zero value is the sf on PATH and the
// network. Tests give the invented ones.
var salesforceOptions salesforce.Options

// salesforceFailures are what each code of a failed read means and how to fix it, as the Python CLI says
// them. They are fixed text: no upstream message, address, name or ID.
var salesforceFailures = map[string][2]string{
	"salesforce_invalid_configuration": {
		"Salesforce credentials or the sf CLI configuration are unavailable.",
		"Run sf org login web --alias acme-dev, then repeat with --sf-org acme-dev."},
	"salesforce_edition_required": {
		"The selected org did not report Developer Edition.",
		"Select a Developer Edition alias with --sf-org."},
	"salesforce_environment_mismatch": {
		"The selected org does not match the expected Salesforce environment.",
		"Check the selected alias and repeat with the intended --sf-environment."},
	"salesforce_api_disabled": {
		"Salesforce reports that API access is disabled for this org.",
		"Use an existing API-enabled org and a user with the API Enabled permission."},
	"salesforce_schema_unavailable": {
		"The selected org does not expose all required standard fields.",
		"Run gtm-connector debug salesforce check with the same alias and environment to see the missing fields."},
	"salesforce_auth_required": {
		"The sf CLI login or token refresh did not provide usable credentials.",
		"Install Salesforce CLI if missing. Run sf org login web --alias acme-dev, then repeat with --sf-org acme-dev."},
	"salesforce_cli_timeout": {
		"The Salesforce CLI (sf) did not answer within its time limit, so no login was read.",
		"The computer may be busy. Repeat the command. To time sf alone, run sf org display with the same alias."},
	"salesforce_forbidden": {
		"Salesforce denied this read.",
		"Grant the selected user read access to the required standard objects and fields."},
	"salesforce_rate_limited": {
		"Salesforce refused a request because its API limit was reached.",
		"Wait for API capacity, then repeat the check."},
	"salesforce_result_limit": {
		"The read exceeded the connector's request, row, or response-size bound.",
		"Select a smaller account book or narrower search, then repeat the check."},
	"salesforce_timeout": {
		"The Salesforce read or credential refresh exceeded its time limit.",
		"Check connectivity and sf login, then repeat the check."},
	"salesforce_unavailable": {
		"The Salesforce endpoint could not be reached.",
		"Check connectivity and sf login, then repeat the check."},
	"salesforce_http_error": {
		"Salesforce did not accept the required GET request.",
		"Check that the required standard object is available and readable in this org."},
	"salesforce_invalid_response": {
		"Salesforce returned data outside the supported response shape.",
		"Check access to the listed standard fields, then repeat the check."},
	"salesforce_account_number_ambiguous": {
		"AccountNumber did not resolve to a literal match within the bounded lookup.",
		"Select the account by its canonical ID."},
	"salesforce_pagination_invalid": {
		"Salesforce returned an unsupported result-page locator.",
		"Repeat the check on a smaller account book."},
	"salesforce_invalid_arguments": {
		"A read could not use the returned account or date.",
		"Run gtm-connector debug salesforce check --help and repeat with a valid date."},
	"source_identity_mismatch": {
		"The org behind this alias is not the one this workspace is pinned to, by its address, ID or environment.",
		"Sign in to the pinned org again under the same alias, or connect this one in a workspace of its own " +
			"with --state-dir NAME."},
	"renewal_fields_unmapped": {
		"No eligible Contract has an EndDate on or after the planning date.",
		"Create a Draft Contract with StartDate and ContractTerm; verify its EndDate and read access."},
	"check_failed": {
		"The check could not finish with the available data or local setup.",
		"Check the database and the workspace, then repeat this check."},
}

// salesforceNext is the command a Salesforce failure names next.
const salesforceNext = "gtm-connector debug salesforce check --help"

// tokenFailures are what the codes that name sf mean and how to fix them, for a token handed to the
// process. They name the variables and never their values.
var tokenFailures = map[string][2]string{
	"salesforce_invalid_configuration": {
		"SALESFORCE_INSTANCE_URL, and SALESFORCE_ACCESS_TOKEN or SALESFORCE_ACCESS_TOKEN_FILE, are missing or invalid.",
		"Give the process an HTTPS My Domain origin in SALESFORCE_INSTANCE_URL, and the token in " +
			"SALESFORCE_ACCESS_TOKEN or in a file named by SALESFORCE_ACCESS_TOKEN_FILE, not both."},
	"salesforce_auth_required": {
		"The token expired or was refused; give the container a new one of the same org and user.",
		"Replace the token in the file SALESFORCE_ACCESS_TOKEN_FILE names, or start the container again with a new " +
			"SALESFORCE_ACCESS_TOKEN."},
	"salesforce_edition_required": {
		"The org of the token did not report Developer Edition.",
		"Give a token of a Developer Edition org, or connect with the intended --environment."},
	"salesforce_environment_mismatch": {
		"The org of the token does not match the expected Salesforce environment.",
		"Check the org of the token and connect again with the intended --environment."},
	"salesforce_schema_unavailable": {
		"The org of the token does not expose all required standard fields.",
		"Run gtm-connector debug salesforce check --state-dir NAME to see the missing fields."},
	"salesforce_timeout": {
		"The Salesforce read exceeded its time limit.",
		"Check connectivity, then repeat the check."},
	"salesforce_unavailable": {
		"The Salesforce endpoint could not be reached.",
		"Check connectivity, then repeat the check."},
	"source_identity_mismatch": {
		"The org or user of this token is not the one this workspace is pinned to, by its address, ID, " +
			"environment or user.",
		"Give the container a token of the pinned org and user, or connect this one in a workspace of its own " +
			"with --state-dir NAME."},
}

// failureAs is a failed read as a command reports it: its code and what it means, exit 2 for a
// configuration that is not one, 3 for an org of another environment and 5 for any other. token words it
// for a token handed to the process. An account the org does not hold is errAccountNotFound. Any other
// error, such as a cancel, is returned as it is.
func failureAs(err error, token bool) error {
	var failed *salesforce.Error
	switch {
	case errors.Is(err, salesforce.ErrAccountNotFound):
		return errAccountNotFound
	case !errors.As(err, &failed):
		return err
	}
	code, exit := failed.Code, 5
	switch code {
	case "salesforce_invalid_configuration":
		exit = 2
	case "salesforce_environment_mismatch":
		exit = 3
	}
	if _, known := salesforceFailures[code]; !known {
		code = "check_failed"
	}
	text := failureText(code, token)[0]
	if failed.Detail != "" {
		text = failed.Detail
	}
	return &failure{code: exit, message: code + ": " + text, next: salesforceNext}
}

// failureText is what a code means and how to fix it, for an sf login or for a token handed to the process.
func failureText(code string, token bool) [2]string {
	if text, own := tokenFailures[code]; token && own {
		return text
	}
	return salesforceFailures[code]
}

// identityElsewhere refuses an org whose identity is not the one the workspace is pinned to.
func identityElsewhere(token bool) *failure {
	text := failureText("source_identity_mismatch", token)
	return &failure{code: 3, next: "gtm-connector connect --help",
		message: "source_identity_mismatch: " + text[0] + " " + text[1] + " Nothing was read."}
}

// failureCode is the code a Salesforce failure opens with, or "" for any other error.
func failureCode(err error) string {
	var known *failure
	if !errors.As(err, &known) {
		return ""
	}
	code, _, _ := strings.Cut(known.message, ": ")
	if _, listed := salesforceFailures[code]; !listed {
		return ""
	}
	return code
}

// orgAlias is the refusal of a missing --org, or of one that cannot be an sf alias, such as a username or
// an access token. What was given is not repeated.
const orgAlias = "connect salesforce needs --org ALIAS, the alias of an org signed in with sf org login web " +
	"--alias ALIAS: 1 to 64 letters, digits, dots, hyphens or underscores. A username or a token is not an alias. " +
	"Where there is no sf, give --token-env instead."

// salesforceCRM is one org as a workspace's CRM. It signs in on its first read and counts the sf commands it
// runs and the requests it sends.
type salesforceCRM struct {
	alias, environment string
	// token is a token handed to the process, read from its variables, in place of the alias's sf login.
	token bool
	// pinned is the identity digest of the workspace's row, or "" while connect attaches it.
	pinned string
	// audit runs each request to sf or the org under a receipt of its own, as connect's are, or is nil.
	audit func(ctx context.Context, step string, request func(context.Context) error) error

	mu sync.Mutex
	// login is the reader once sf has given the login, and reader is the same once the org is checked.
	login, reader *salesforce.Reader

	commands, requests atomic.Int64
}

// openSalesforce is the opener of the salesforce kind: an sf alias (mode sf) or a token handed to the
// process (mode env_token). It reads the row and nothing else, and no variable: those are read on the
// first read, as sf is run then.
func openSalesforce(_ context.Context, o opening) (crmSource, error) {
	environment, _ := get(o.binding, "environment").(string)
	if o.connection.Mode == "env_token" {
		if get(o.binding, "credentials") != "environment" || !validEnvironment(environment) ||
			o.connection.IdentitySHA256 == "" {
			return nil, local("This workspace's Salesforce connection names no environment or identity. " +
				"Connect it again with gtm-connector connect salesforce --token-env.")
		}
		return &salesforceCRM{token: true, environment: environment, pinned: o.connection.IdentitySHA256}, nil
	}
	alias, _ := get(o.binding, "org").(string)
	if alias == "" || !validEnvironment(environment) || o.connection.IdentitySHA256 == "" {
		return nil, local("This workspace's Salesforce connection names no sf alias, environment or identity. " +
			"Connect it again with gtm-connector connect salesforce.")
	}
	return &salesforceCRM{alias: alias, environment: environment, pinned: o.connection.IdentitySHA256}, nil
}

func validEnvironment(environment string) bool {
	return environment == "developer" || environment == "sandbox" || environment == "production"
}

// attachSalesforce is what connect salesforce checks: the alias, the sign-in, the environment and, outside
// Developer Edition, the four objects' fields, each sf command and each request under a receipt of its own.
// It reads no record. The row is saved with the alias and the
// environment, and pinned to the org's identity and the user it reads as. The alias is saved, and so shown,
// only once sf has named a login under it.
//
// With --token-env the instance address and the token are the variables of the process, and the row is
// saved as env_token with no alias. The values are never saved or shown.
func attachSalesforce(ctx context.Context, a attaching) (attached, error) {
	alias, given := a.args.text("--org")
	_, token := a.args.text("--token-env")
	switch {
	case given && token:
		return attached{}, refused("--org and --token-env are two ways to sign in. Give one.")
	case !token && (!given || !salesforce.ValidAlias(alias)):
		return attached{}, refused(orgAlias)
	}
	if _, named := a.args.text("--state-dir"); !named {
		return attached{}, refused("Salesforce is connected in a workspace of its own: the default one holds demo " +
			"data. Give connect salesforce --state-dir NAME, and every later command the same --state-dir NAME.")
	}
	environment := "developer"
	if chosen, given := a.args.text("--environment"); given {
		environment = chosen
	}
	// Each request to sf or to the org runs under a receipt of its own step, committed first:
	// connect_salesforce_login_display, _login_token, _org, _userinfo and _describe_account and the rest.
	crm := &salesforceCRM{alias: alias, environment: environment, token: token,
		audit: func(ctx context.Context, step string, request func(context.Context) error) error {
			return a.step(ctx, "connect_salesforce_"+step, request)
		}}
	identity, err := crm.identify(ctx, true)
	switch {
	case err != nil:
		return attached{}, err
	case token:
		return attached{mode: "env_token", binding: strictjson.NewObject("credentials", "environment",
			"environment", environment), identity: identity}, nil
	}
	return attached{binding: strictjson.NewObject("org", alias, "environment", environment), identity: identity}, nil
}

// options are the reader's options, with what it runs and sends counted.
func (c *salesforceCRM) options() salesforce.Options {
	o := salesforceOptions
	o.Environment = c.environment
	if o.Runner == nil {
		o.Runner = salesforce.Exec{}
	}
	if o.Transport == nil {
		o.Transport = salesforce.DefaultTransport()
	}
	o.Runner = countedRunner{o.Runner, &c.commands}
	o.Transport = countedTransport{o.Transport, &c.requests}
	o.Audit = c.audit
	return o
}

// failed is a failed read as a command reports it, worded for how this CRM signs in.
func (c *salesforceCRM) failed(err error) error { return failureAs(err, c.token) }

// signIn reads the login once: the alias's through sf, or the token handed to the process.
func (c *salesforceCRM) signIn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signInLocked(ctx)
}

func (c *salesforceCRM) signInLocked(ctx context.Context) error {
	if c.login != nil {
		return nil
	}
	var login *salesforce.Reader
	var err error
	if c.token {
		login, err = salesforce.FromEnvironment(ctx, c.options())
	} else {
		login, err = salesforce.FromSF(ctx, c.alias, c.options())
	}
	if err != nil {
		return c.failed(err)
	}
	c.login = login
	return nil
}

// identify signs in and checks the org and the user against the environment and, with fields outside
// Developer Edition, the four objects' fields. It gives the pin: the digest of the org's identity and the
// user's ID. In a workspace, a login that is not the pinned one is refused, and one that is becomes the
// reader every read uses. A login that fails any of this is let go.
func (c *salesforceCRM) identify(ctx context.Context, fields bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.signInLocked(ctx); err != nil {
		return "", err
	}
	check := c.login.CheckEnvironment
	if fields {
		check = c.login.PrepareConnection
	}
	identity, err := check(ctx)
	pin := identity.Pin()
	switch {
	case err != nil:
		err = c.failed(err)
	case c.pinned != "" && pin != c.pinned:
		err = identityElsewhere(c.token)
	}
	if err != nil {
		// The login is let go with what it checked, so the next read signs in again and checks a new
		// login from the start: a token file put right, or an alias signed in again, is read then.
		c.login = nil
		return "", err
	}
	c.reader = c.login
	return pin, nil
}

// signedIn is the reader of the pinned org, signed in and checked on the first read. The pin is compared
// before any object is described or any record read, so another org is read nothing, and an error of its
// schema cannot hide that it is another org.
func (c *salesforceCRM) signedIn(ctx context.Context) (*salesforce.Reader, error) {
	c.mu.Lock()
	reader := c.reader
	c.mu.Unlock()
	if reader != nil {
		return reader, nil
	}
	if _, err := c.identify(ctx, false); err != nil {
		return nil, err
	}
	return c.reader, nil
}

func (c *salesforceCRM) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, err
	}
	_, records, err := reader.Read(ctx, tool, accountID)
	if errors.Is(err, salesforce.ErrTooManyUsers) {
		// An org with more users than a card reads gives a card without them, which says so.
		return nil, cards.ErrNotSelected
	}
	return records, c.failed(err)
}

func (c *salesforceCRM) SearchAccounts(ctx context.Context, query string) ([]*strictjson.Object, bool, error) {
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, false, err
	}
	accounts, more, err := reader.SearchAccounts(ctx, query, nil, nil)
	return accounts, more, c.failed(err)
}

// Search is the MCP tools' search_accounts: the accounts among these whose name holds the query, or with
// a number the one with exactly that AccountNumber. No account to search within reads nothing.
func (c *salesforceCRM) Search(ctx context.Context, query string, number *string,
	accounts []string) ([]*strictjson.Object, bool, error) {
	if len(accounts) == 0 {
		return []*strictjson.Object{}, false, nil
	}
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, false, err
	}
	found, more, err := reader.SearchAccounts(ctx, query, number, accounts)
	return referenced(found), more, c.failed(err)
}

// Opportunities is the MCP tools' list_opportunities with no account: the opportunities of these accounts.
func (c *salesforceCRM) Opportunities(ctx context.Context, accounts []string) ([]*strictjson.Object, error) {
	if len(accounts) == 0 {
		return []*strictjson.Object{}, nil
	}
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, err
	}
	found, err := reader.ListAllOpportunities(ctx, accounts)
	records := make([]*strictjson.Object, len(found))
	for i, opportunity := range found {
		records[i] = opportunity.Copy()
		records[i].Set("record_ref", "crm/opportunities/"+cellText(get(opportunity, "id")))
	}
	return records, c.failed(err)
}

// countedRunner counts the sf commands a reader runs, and countedTransport the requests it sends.
type countedRunner struct {
	salesforce.Runner
	n *atomic.Int64
}

func (r countedRunner) Run(ctx context.Context, arguments []string) ([]byte, int, error) {
	r.n.Add(1)
	return r.Runner.Run(ctx, arguments)
}

type countedTransport struct {
	http.RoundTripper
	n *atomic.Int64
}

func (t countedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.n.Add(1)
	return t.RoundTripper.RoundTrip(request)
}

// debug salesforce check.

var debugSalesforceArguments = arguments{options: []option{{name: "--sf-org", value: true},
	{name: "--sf-environment", value: true, choices: []string{"developer", "sandbox", "production"}},
	{name: "--as-of", value: true}}, optional: "action", choices: []string{"check"}, help: debugSalesforceHelp}

const debugSalesforceHelp = `usage: gtm-connector debug salesforce [-h] [--json] [--state-dir STATE_DIR]
                                     [--sf-org SF_ORG]
                                     [--sf-environment {developer,sandbox,production}]
                                     [--as-of AS_OF]
                                     {check}

Check a Salesforce connection without printing org records.

positional arguments:
  {check}               Run the private, read-only setup check.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Check the Salesforce org this workspace is connected
                        to. With --sf-org, the workspace it will be connected
                        in, made when missing. Every step's requests run
                        under receipts kept there.
  --sf-org SF_ORG       Check this signed-in sf CLI alias before it is
                        connected: the sign-in, the environment and the
                        fields. No record is read.
  --sf-environment {developer,sandbox,production}
                        With --sf-org: require this environment (default:
                        developer).
  --as-of AS_OF         Use YYYY-MM-DD for renewal planning (default: today in
                        UTC).

Examples:
  gtm-connector debug salesforce check --sf-org acme-dev --state-dir acme-dev
  gtm-connector debug salesforce check --state-dir acme-dev
`

// debugSalesforce runs debug salesforce and gives its exit code. A failed check prints its report and then
// fails with exit 4.
func debugSalesforce(ctx context.Context, tokens []string, s screen) int {
	const command = "debug salesforce check"
	args, err := debugSalesforceArguments.parse(tokens)
	switch {
	case err != nil:
		return s.fail(command, refused("%s", err))
	case args.help:
		fmt.Fprint(s.out, debugSalesforceHelp)
		return 0
	case len(args.positionals) == 0:
		return s.fail(command, refused("the following arguments are required: action"))
	}
	r := &run{command: command, args: args, screen: s, name: defaultWorkspace}
	if name, given := args.text("--state-dir"); given {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	report, err := r.salesforceCheck(ctx)
	if err != nil {
		return s.fail(command, r.failed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(report, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
	} else {
		rows := [][]any{}
		steps, _ := get(report, "steps").([]any)
		for _, value := range steps {
			step := value.(*strictjson.Object)
			row := []any{}
			for _, key := range []string{"name", "status", "count", "duration_ms", "api_calls", "sf_cli_invocations",
				"message", "fix"} {
				row = append(row, get(step, key))
			}
			rows = append(rows, row)
		}
		fmt.Fprintln(s.out, s.table([]string{"STEP", "STATUS", "COUNT", "MS", "API GETS", "SF COMMANDS", "DETAIL", "FIX"},
			rows))
	}
	if get(report, "status") != "passed" {
		return s.fail(command, &failure{code: 4, next: salesforceNext,
			message: "Salesforce checks failed. Apply the fix beside each failed step."})
	}
	return 0
}

// missingFields is an object that does not expose every mapped field.
type missingFields struct {
	object string
	fields []string
}

func (m *missingFields) Error() string { return m.object + " does not expose required fields" }

// errNoCandidate is an org with no contract a renewal can plan from.
var errNoCandidate = errors.New("renewal_fields_unmapped")

// salesforceCheck runs the check's steps and gives its report. With --sf-org it checks an org before it is
// connected and reads no record and no workspace. Without it, it checks the org of a workspace connected to
// Salesforce, and reads its records under receipts, as accounts and inspect do. A step whose earlier step
// failed is not run. The report holds counts, codes and fixed text: no record, name, ID or credential.
func (r *run) salesforceCheck(ctx context.Context) (*strictjson.Object, error) {
	asOf, dated := r.args.text("--as-of")
	if !dated {
		asOf = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := evidence.Day(asOf); err != nil {
		return nil, refused("The planning date must use YYYY-MM-DD.")
	}
	alias, before := r.args.text("--sf-org")
	_, chosen := r.args.text("--sf-environment")
	_, named := r.args.text("--state-dir")
	switch {
	case chosen && !before:
		return nil, refused("--sf-environment goes with --sf-org. A connected workspace is checked in the " +
			"environment it was connected with.")
	case before && !salesforce.ValidAlias(alias):
		return nil, refused("--sf-org takes the alias of an org signed in with sf org login web --alias ALIAS: 1 " +
			"to 64 letters, digits, dots, hyphens or underscores. A username or a token is not an alias.")
	case before && !named:
		return nil, refused("--sf-org checks an org before it is connected, under receipts kept in the workspace " +
			"it will be connected in. Give --state-dir NAME too: the default workspace holds demo data.")
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	var (
		crm  *salesforceCRM
		from *reads
	)
	if before {
		var err error
		if from, err = r.unconnected(ctx); err != nil {
			return nil, err
		}
		environment, _ := r.args.text("--sf-environment")
		crm = &salesforceCRM{alias: alias, environment: cmp.Or(environment, "developer")}
	} else {
		var err error
		if from, err = r.reads(ctx); err != nil {
			return nil, err
		}
		if from.kinds["crm"].provider != "salesforce" {
			return nil, refused("debug salesforce check reads a workspace connected to Salesforce, and this one is " +
				"not. Give it --state-dir NAME of one, or --sf-org ALIAS to check an org before it is connected.")
		}
		opened, err := from.crm(ctx)
		if err != nil {
			return nil, err
		}
		var own bool
		if crm, own = opened.(*salesforceCRM); !own {
			return nil, local("This workspace's CRM is not read as Salesforce in this build.")
		}
	}

	// Every request the check makes to sf or to the org runs under a receipt of its step, committed first.
	c := &checkRun{from: from, run: "check-" + strings.ReplaceAll(receipts.RandomID(), "-", ""),
		token: receipts.Fingerprint(randomToken()), scope: "crm:read"}
	steps, passed := []any{}, true
	var aborted error
	step := func(name string, ready bool, operation func() (int, string, error)) bool {
		row := strictjson.NewObject("name", name, "status", "fail", "count", 0, "code", "prerequisite_failed",
			"message", "Not run because a required earlier step failed.",
			"fix", "Apply the fix for the earlier failed step, then repeat the check.")
		commands, requests, started := crm.commands.Load(), crm.requests.Load(), time.Now()
		if ready && aborted == nil {
			r.checkProgress(name, "running", nil)
			count, message, err := operation()
			var missing *missingFields
			switch code := failureCode(err); {
			case err == nil:
				row.Set("status", "pass")
				row.Set("count", count)
				row.Set("code", "ok")
				row.Set("message", message)
				row.Set("fix", "")
			case ctx.Err() != nil:
				aborted = err
			case errors.As(err, &missing):
				row.Set("code", "missing_fields")
				row.Set("message", missing.object+" does not expose required fields: "+
					strings.Join(missing.fields, ", ")+".")
				row.Set("fix", "Grant read access to these standard fields, then repeat the check.")
			default:
				if errors.Is(err, errNoCandidate) {
					code = "renewal_fields_unmapped"
				}
				if code == "" {
					code = "check_failed"
				}
				row.Set("code", code)
				row.Set("message", failureText(code, crm.token)[0])
				row.Set("fix", failureText(code, crm.token)[1])
			}
		}
		elapsed, _ := strconv.ParseFloat(strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64), 64)
		row.Set("duration_ms", elapsed)
		row.Set("api_calls", crm.requests.Load()-requests)
		row.Set("sf_cli_invocations", crm.commands.Load()-commands)
		ok := get(row, "status") == "pass"
		if aborted == nil {
			steps = append(steps, row)
			r.checkProgress(name, get(row, "status").(string), get(row, "count"))
		}
		passed = passed && ok
		return ok
	}

	signedIn := step("credentials", true, func() (int, string, error) {
		return 2, "Instance and token were loaded in memory.", c.step(ctx, "check_salesforce_login", nil,
			func(ctx context.Context) error { return crm.signIn(ctx) })
	})
	name, confirmed := "environment", "The selected "+crm.environment+" environment was confirmed."
	if crm.environment == "developer" {
		name, confirmed = "edition", "Developer Edition was confirmed."
	}
	schema := step(name, signedIn, func() (int, string, error) {
		return 1, confirmed, c.step(ctx, "check_salesforce_org", nil, func(ctx context.Context) error {
			_, err := crm.identify(ctx, false)
			return err
		})
	})
	checked := schema
	for _, object := range salesforce.Objects() {
		exposed := step(strings.ToLower(object)+"_fields", schema, func() (int, string, error) {
			mapped := 0
			err := c.step(ctx, "check_salesforce_fields", strictjson.NewObject("object", object),
				func(ctx context.Context) error {
					var missing []string
					var err error
					mapped, missing, err = crm.login.MissingFields(ctx, object)
					if err == nil && len(missing) > 0 {
						return &missingFields{object, missing}
					}
					return crm.failed(err)
				})
			return mapped, "Required " + object + " fields are exposed by describe.", err
		})
		checked = checked && exposed
	}
	if !before {
		step("account_search", checked, func() (int, string, error) {
			found, _, err := from.searchAccounts(ctx, "")
			return len(found), "The bounded Account search completed.", err
		})
		var account string
		candidate := step("renewal_candidate", checked, func() (int, string, error) {
			var err error
			account, err = c.renewalCandidate(ctx, crm, asOf)
			return 1, "A dated renewal-planning candidate was found.", err
		})
		step("account_read", candidate, func() (int, string, error) {
			found, ok, err := from.account(ctx, account)
			if err != nil || !ok {
				return 0, "", cmp.Or(err, errNoCandidate)
			}
			contacts, _ := get(found, "contacts").([]any)
			contracts, _ := get(found, "contracts").([]any)
			return 1 + len(contacts) + len(contracts), "Account, Contact, and Contract reads completed.", nil
		})
		step("opportunity_read", candidate, func() (int, string, error) {
			read, err := from.ownerRead(ctx, "list_opportunities", strictjson.NewObject("account_id", account),
				func(ctx context.Context) ([]*strictjson.Object, error) {
					return crm.Read(ctx, "list_opportunities", account)
				})
			return len(read), "The bounded Opportunity read completed.", err
		})
	}
	if aborted != nil {
		return nil, aborted
	}
	status := "failed"
	if passed {
		status = "passed"
	}
	var requests, commands int64
	for _, value := range steps {
		requests += get(value.(*strictjson.Object), "api_calls").(int64)
		commands += get(value.(*strictjson.Object), "sf_cli_invocations").(int64)
	}
	return strictjson.NewObject("schema_version", 1, "status", status, "as_of", asOf, "steps", steps,
		"api_calls", requests, "sf_cli_invocations", commands,
		"api_call_definition", "Connector REST GET attempts, including retries. sf CLI internal HTTP requests "+
			"and other org activity are not counted.",
		"sf_cli_definition", "Attempted sf subprocess invocations, including failed launches."), nil
}

// checkRun is one run of a check or a sign-in: its receipts, each a call of one of its steps in the run, in
// order, under its scope. A step's tool name is no MCP tool, so it carries its scope itself.
type checkRun struct {
	from              *reads
	run, token, scope string
	requests          int
}

// call reads under a receipt of the check, committed before the read.
func (c *checkRun) call(ctx context.Context, tool string, arguments *strictjson.Object,
	read func(context.Context) ([]*strictjson.Object, error)) ([]*strictjson.Object, error) {
	if arguments == nil {
		arguments = &strictjson.Object{}
	}
	c.requests++
	_, records, err := c.from.call(ctx, toolCall{token: c.token, scopes: []string{c.scope}, run: c.run,
		request: c.requests, tool: tool, scope: c.scope, arguments: arguments}, read)
	return records, err
}

// step is a call that returns no record: a sign-in, the org's identity, or an object's fields.
func (c *checkRun) step(ctx context.Context, tool string, arguments *strictjson.Object,
	run func(context.Context) error) error {
	_, err := c.call(ctx, tool, arguments, func(ctx context.Context) ([]*strictjson.Object, error) {
		return []*strictjson.Object{}, run(ctx)
	})
	return err
}

// unconnected is the workspace --sf-org checks an org in before it is connected: one for real sources whose
// CRM is not connected, made when it does not exist. Its receipts name no connection.
func (r *run) unconnected(ctx context.Context) (*reads, error) {
	if r.exists {
		if r.workspace.Kind != "live" {
			return nil, &failure{code: 3, message: "This workspace holds demo data, and a real source never shares " +
				"a workspace with it. Check Salesforce in a workspace of its own with --state-dir NAME."}
		}
		rows, err := r.store.Connections(ctx, r.workspace.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Source == "crm" && row.Mode != "none" {
				return nil, refused("This workspace's CRM is connected. Check it with debug salesforce check " +
					"--state-dir NAME alone, which reads the org it is pinned to.")
			}
		}
	} else {
		slug := workspaceSlug(r.name)
		var err error
		if r.workspace, err = r.store.EnsureWorkspace(ctx, slug, "live", "Workspace "+slug); err != nil {
			return nil, err
		}
		r.exists = true
	}
	return &reads{store: r.store, workspace: r.workspace, rows: map[string]postgres.Connection{},
		kinds: map[string]*sourceKind{}, bindings: map[string]*strictjson.Object{}}, nil
}

// renewalCandidate reads, under a receipt of the check, the contract a renewal plans from, and gives its
// account. The receipt binds the contract it returned.
func (c *checkRun) renewalCandidate(ctx context.Context, crm *salesforceCRM, asOf string) (string, error) {
	records, err := c.call(ctx, "find_renewal_candidate", strictjson.NewObject("as_of", asOf),
		func(ctx context.Context) ([]*strictjson.Object, error) {
			reader, err := crm.signedIn(ctx)
			if err != nil {
				return nil, err
			}
			contract, err := reader.RenewalCandidate(ctx, asOf)
			if err != nil || contract == nil {
				return []*strictjson.Object{}, crm.failed(err)
			}
			return []*strictjson.Object{contract}, nil
		})
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", errNoCandidate
	}
	account, _ := get(records[0], "account_id").(string)
	return account, nil
}

// checkProgress says on stderr which step of the check runs and how it ended: a line of text, or with --json an
// object with one progress key.
func (r *run) checkProgress(step, status string, count any) {
	if r.screen.json {
		r.screen.err.Write([]byte(mustLine(strictjson.NewObject("progress", strictjson.NewObject(
			"command", r.command, "step", step, "status", status, "count", count))) + "\n"))
		return
	}
	line := "Salesforce " + strings.ReplaceAll(step, "_", " ") + ": " + status + "."
	if count != nil {
		line += " Count: " + cellText(count) + "."
	}
	fmt.Fprintln(r.screen.err, line)
}

// mustLine is a value as one line of ASCII JSON.
func mustLine(value any) string {
	text, _ := strictjson.Dumps(value, strictjson.DumpOptions{EnsureASCII: true})
	return text
}
