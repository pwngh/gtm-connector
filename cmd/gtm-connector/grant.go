package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The grant commands. grant gives a person who signs in through the company's identity provider read access
// to one account of a workspace, and with --revoke ends it; grants lists the people of a workspace with what
// each may read. A person is there from their first sign-in, with no grant. Each change runs under a
// receipt committed before it, and the change commits with the receipt's finish, so a grant never changes
// without its receipt saying so. A refused change keeps its receipt too, finished as an error.

// The receipts' tools.
const (
	grantTool  = "grant_account"
	revokeTool = "revoke_grant"
)

// accountRef is an account ID as a workspace holds one.
var accountRef = regexp.MustCompile(`^acc-([0-9]{3}|sf-[A-Za-z0-9]{18})$`)

// noPerson is the refusal of a --to that names no person of the workspace.
var noPerson = refused("No person of this workspace has that email or subject. A person is listed from their " +
	"first sign-in through the identity provider: gtm-connector grants lists them.")

// person is the --to of a grant command, read before anything else: an email or a subject, one line of
// at most 320 printable characters with no space.
func (r *run) person() (string, error) {
	to, _ := r.args.text("--to")
	if to == "" || len(to) > 320 || !utf8.ValidString(to) {
		return "", refused("--to is a person's email, or the subject gtm-connector grants lists.")
	}
	for _, c := range to {
		if !unicodetab.IsPrintable(c) || c == ' ' {
			return "", refused("--to is a person's email, or the subject gtm-connector grants lists.")
		}
	}
	return to, nil
}

func asciiLower(text string) string {
	out := []byte(text)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}

// findPerson is the one person of the workspace whose subject is to, or whose email the provider verified
// is to in any ASCII case. An email the provider did not verify finds nobody: the person must be named by
// their subject.
func (r *run) findPerson(people []postgres.Person, to string) (postgres.Person, *failure) {
	var found []postgres.Person
	for _, p := range people {
		if p.Subject == to || (p.EmailVerified && asciiLower(p.Email) == asciiLower(to)) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return postgres.Person{}, noPerson
	case 1:
		return found[0], nil
	}
	return postgres.Person{}, refused("More than one person of this workspace has that email. Name one by the " +
		"subject gtm-connector grants lists.")
}

// grant gives or revokes one person's grant on one account, under a receipt.
func (r *run) grant(ctx context.Context) (any, error) {
	account := r.args.positionals[0]
	if !accountRef.MatchString(account) {
		return nil, refused("ACCOUNT is an account ID such as acc-003.")
	}
	to, err := r.person()
	if err != nil {
		return nil, err
	}
	_, revoke := r.args.text("--revoke")
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	if !r.exists {
		return nil, noPerson
	}
	// An account of the demo book is one the demo workspace holds, whether a command has listed it yet or not.
	if r.workspace.Kind == "demo" {
		if book, err := seededBook(); err == nil {
			for _, a := range book.Accounts() {
				if a.ID == account {
					if err := r.store.EnsureAccount(ctx, r.workspace.ID, a.ID, a.Name); err != nil {
						return nil, err
					}
				}
			}
		} else {
			return nil, err
		}
	}
	tool := grantTool
	if revoke {
		tool = revokeTool
	}
	arguments, err := strictjson.Dumps(strictjson.NewObject("account_id", account, "to", to), strictjson.Prepared)
	if err != nil {
		return nil, err
	}
	operation := make([]byte, 16)
	rand.Read(operation)
	started, err := r.store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: r.workspace.ID,
		OperationID: "grant-" + hex.EncodeToString(operation), TokenID: postgres.TokenID(randomToken()),
		Tool: tool, AccountRef: account, Arguments: []byte(arguments), Lease: time.Minute})
	if err != nil {
		return nil, err
	}
	began := time.Now()
	refuse := func(status int, code string, f *failure) (any, error) {
		if _, err := r.store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: started.ID,
			LeaseToken: started.LeaseToken, Status: "error", HTTPStatus: status, Error: code,
			ElapsedMS: float64(time.Since(began).Microseconds()) / 1000}); err != nil {
			return nil, err
		}
		return nil, f
	}
	people, err := r.store.People(ctx, r.workspace.ID)
	if err != nil {
		return refuse(503, "execution_or_audit_failure", local("The people of this workspace could not be read."))
	}
	p, missing := r.findPerson(people, to)
	if missing != nil {
		status, code := 404, "person_not_found"
		if missing != noPerson {
			status, code = 409, "person_ambiguous"
		}
		return refuse(status, code, missing)
	}
	name, err := r.store.AccountName(ctx, r.workspace.ID, account)
	if errors.Is(err, postgres.ErrNotFound) {
		return refuse(404, "account_not_found", refused("This workspace holds no account by that ID. An account is "+
			"held once a command such as accounts or brief has read it."))
	}
	if err != nil {
		return refuse(503, "execution_or_audit_failure", local("The account could not be read."))
	}
	changed, finished, err := r.store.ChangeGrant(ctx, started, began, r.workspace.ID, p.ID, account, revoke)
	if err != nil {
		return nil, err
	}
	state := "live"
	if revoke {
		state = "revoked"
	}
	// An account's name is the CRM's text, shown inert. The person's name was made inert as they signed in.
	if name, _, err = evidence.SafeText(name); err != nil {
		return nil, err
	}
	return strictjson.NewObject("account", account, "account_name", name, "person", personObject(p),
		"state", state, "changed", changed, "receipt_id", finished.ID), nil
}

// personObject is a person as the grant commands show one.
func personObject(p postgres.Person) *strictjson.Object {
	var email any
	if p.Email != "" {
		email = p.Email
	}
	state := "enabled"
	if p.Disabled {
		state = "disabled"
	}
	return strictjson.NewObject("id", p.ID, "name", p.DisplayName, "email", email, "email_verified", p.EmailVerified,
		"subject", p.Subject, "state", state, "first_signed_in", utcSeconds(p.CreatedAt))
}

// grants lists the people of the workspace, in the order they first signed in, each with every grant they
// hold or held. --to lists one person.
func (r *run) grants(ctx context.Context) (any, error) {
	to := ""
	if _, given := r.args.text("--to"); given {
		var err error
		if to, err = r.person(); err != nil {
			return nil, err
		}
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	listed := []any{}
	if !r.exists {
		if to != "" {
			return nil, noPerson
		}
		return listed, nil
	}
	people, err := r.store.People(ctx, r.workspace.ID)
	if err != nil {
		return nil, err
	}
	if to != "" {
		p, missing := r.findPerson(people, to)
		if missing != nil {
			return nil, missing
		}
		people = []postgres.Person{p}
	}
	for _, p := range people {
		grants := []any{}
		for _, g := range p.Grants {
			state, revoked := "live", any(nil)
			if g.RevokedAt != nil {
				state, revoked = "revoked", utcSeconds(*g.RevokedAt)
			}
			name, _, err := evidence.SafeText(g.AccountName)
			if err != nil {
				return nil, err
			}
			grants = append(grants, strictjson.NewObject("account", g.AccountRef, "account_name", name,
				"state", state, "granted_at", utcSeconds(g.GrantedAt), "revoked_at", revoked))
		}
		o := personObject(p)
		o.Set("grants", grants)
		listed = append(listed, o)
	}
	return listed, nil
}

// grantText is a grant command's result as a person reads it.
func (r *run) grantText(result any) string {
	if listed, ok := result.([]any); ok {
		if len(listed) == 0 {
			return "No one has signed in to this workspace through the identity provider yet."
		}
		var rows [][]any
		for _, item := range listed {
			p := item.(*strictjson.Object)
			who := []any{get(p, "name"), orNone(get(p, "email")), get(p, "subject")}
			grants, _ := get(p, "grants").([]any)
			if len(grants) == 0 {
				rows = append(rows, append(who, "none", "no grant", ""))
			}
			for _, item := range grants {
				g := item.(*strictjson.Object)
				since := get(g, "granted_at")
				if get(g, "state") == "revoked" {
					since = get(g, "revoked_at")
				}
				rows = append(rows, append(append([]any{}, who...), get(g, "account"), get(g, "state"), since))
			}
		}
		return r.screen.table([]string{"PERSON", "EMAIL", "SUBJECT", "ACCOUNT", "STATE", "SINCE"}, rows)
	}
	o := result.(*strictjson.Object)
	p := get(o, "person").(*strictjson.Object)
	who := fmt.Sprintf("%s (%s)", cellText(get(p, "name")), cellText(orNone(get(p, "email"))))
	account := fmt.Sprintf("%s (%s)", cellText(get(o, "account")), cellText(get(o, "account_name")))
	receipt := " Receipt " + cellText(get(o, "receipt_id")) + "."
	var line string
	switch {
	case get(o, "state") == "live" && get(o, "changed") == true:
		line = "Granted " + account + " to " + who + "."
	case get(o, "state") == "live":
		line = who + " already holds " + account + ". Nothing changed."
	case get(o, "changed") == true:
		line = "Revoked " + account + " from " + who + "."
	default:
		line = who + " holds no live grant on " + account + ". Nothing changed."
	}
	return oneLine(line + receipt)
}

func orNone(value any) any {
	if value == nil {
		return "no email"
	}
	return value
}

// grantHelp and grantsHelp are the two commands' help.
var grantHelp = strings.TrimLeft(`
usage: gtm-connector grant [-h] [--json] [--state-dir STATE_DIR] --to PERSON
                           [--revoke]
                           ACCOUNT

Give a person who signs in through the identity provider read access to one
account of a workspace, or with --revoke end it. Each change gets a receipt.

positional arguments:
  ACCOUNT               An account ID the workspace holds, such as acc-003.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --to PERSON           The person's email, which their identity provider
                        verified, or their subject, as grants lists it.
  --revoke              End the person's grant on the account.

A person is listed from their first sign-in, with no grant.
Examples:
  gtm-connector grant acc-003 --to dana@invented.example --state-dir acme
  gtm-connector grant acc-003 --to dana@invented.example --state-dir acme --revoke
`, "\n")

var grantsHelp = strings.TrimLeft(`
usage: gtm-connector grants [-h] [--json] [--state-dir STATE_DIR] [--to PERSON]

List the people who sign in to a workspace through the identity provider, and
every grant each holds or held.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --to PERSON           List only this person, by email or subject.

Example: gtm-connector grants --state-dir acme
`, "\n")
