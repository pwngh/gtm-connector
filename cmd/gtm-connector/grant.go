package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
// to one account of a workspace, and with --revoke ends it; with --disable it ends the person's sessions and
// refuses their sign-ins, and with --enable lets them in again. grants lists the people of a workspace with
// what each may read. The people are those of the issuer serve signs in through, GTM_OIDC_ISSUER, each
// there from their first sign-in with no grant. Each change runs under a receipt committed before it, and
// the change commits with the receipt's finish, so nothing changes without its receipt saying so. A
// refused change keeps its receipt too, finished as an error.

// The receipts' tools.
const (
	grantTool   = "grant_account"
	revokeTool  = "revoke_grant"
	disableTool = "disable_person"
	enableTool  = "enable_person"
)

// accountRef is an account ID as a workspace holds one.
var accountRef = regexp.MustCompile(`^acc-([0-9]{3}|sf-[A-Za-z0-9]{18})$`)

// The refusals of a person the workspace does not have.
var (
	noEmail = refused("No person of this workspace has that verified email. Name a person by subject with " +
		"--subject: gtm-connector grants lists them.")
	noSubject = refused("No person of this workspace has that subject. A person is listed from their first " +
		"sign-in through the identity provider: gtm-connector grants lists them.")
	noWorkspace = refused("No one has signed in to this workspace through the identity provider.")
	nameOne     = refused("Name one person: --to EMAIL or --subject SUBJECT.")
)

// servesIssuer is the issuer serve signs people in through, from the environment serve reads it from. Its
// people are the only ones the grant commands name.
func servesIssuer() (string, error) {
	issuer := os.Getenv("GTM_OIDC_ISSUER")
	if issuer == "" {
		return "", local("set GTM_OIDC_ISSUER to the issuer serve signs people in through; its people are the ones named")
	}
	return issuer, nil
}

// named is the person a grant command names, read before anything else: an email with --to or a subject
// with --subject, one line of at most 320 printable characters with no space. Neither is "" for both; a
// command that must name one refuses that.
func (r *run) named(required bool) (to, subject string, err error) {
	to, byEmail := r.args.text("--to")
	subject, bySubject := r.args.text("--subject")
	if byEmail && bySubject || required && !byEmail && !bySubject {
		return "", "", nameOne
	}
	given := to + subject
	if (byEmail || bySubject) && (given == "" || len(given) > 320 || !utf8.ValidString(given)) {
		return "", "", refused("--to and --subject are one line of printable text, with no space.")
	}
	for _, c := range given {
		if !unicodetab.IsPrintable(c) || c == ' ' {
			return "", "", refused("--to and --subject are one line of printable text, with no space.")
		}
	}
	return to, subject, nil
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

// findPerson is the one person whose subject is subject, or whose email the provider verified is to in any
// ASCII case, with the HTTP status a refusal's receipt records. An email the provider did not verify names
// nobody, and an email two people share says which subjects it matched.
func findPerson(people []postgres.Person, to, subject string) (postgres.Person, int, *failure) {
	if subject != "" {
		for _, p := range people {
			if p.Subject == subject {
				return p, 0, nil
			}
		}
		return postgres.Person{}, 404, noSubject
	}
	var found []string
	var match postgres.Person
	for _, p := range people {
		if p.EmailVerified && asciiLower(p.Email) == asciiLower(to) {
			found, match = append(found, p.Subject), p
		}
	}
	switch len(found) {
	case 0:
		return postgres.Person{}, 404, noEmail
	case 1:
		return match, 0, nil
	}
	list := strings.Join(found[:len(found)-1], ", ") + " and " + found[len(found)-1]
	return postgres.Person{}, 409, refused("More than one person of this workspace has that verified email: the "+
		"subjects %s. Name one with --subject.", list)
}

// grant makes one change, under a receipt: a person's grant on an account, given or revoked, or the
// person disabled or enabled.
func (r *run) grant(ctx context.Context) (any, error) {
	_, revoke := r.args.text("--revoke")
	_, disable := r.args.text("--disable")
	_, enable := r.args.text("--enable")
	person := disable || enable
	account := ""
	if len(r.args.positionals) > 0 {
		account = r.args.positionals[0]
	}
	switch {
	case disable && enable:
		return nil, refused("--disable and --enable are two changes; give one.")
	case person && revoke:
		return nil, refused("--revoke ends a grant on an account; --disable and --enable change the person.")
	case person && account != "":
		return nil, refused("--disable and --enable change the person, and take no ACCOUNT.")
	case !person && account == "":
		return nil, refused("the following arguments are required: ACCOUNT")
	case !person && !accountRef.MatchString(account):
		return nil, refused("ACCOUNT is an account ID such as acc-003.")
	}
	to, subject, err := r.named(true)
	if err != nil {
		return nil, err
	}
	issuer, err := servesIssuer()
	if err != nil {
		return nil, err
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	if !r.exists {
		return nil, noWorkspace
	}
	// An account of the demo book is one the demo workspace holds, whether a command has listed it yet or not.
	if account != "" && r.workspace.Kind == "demo" {
		book, err := seededBook()
		if err != nil {
			return nil, err
		}
		for _, a := range book.Accounts() {
			if a.ID == account {
				if err := r.store.EnsureAccount(ctx, r.workspace.ID, a.ID, a.Name); err != nil {
					return nil, err
				}
			}
		}
	}
	tool := map[bool]string{false: grantTool, true: revokeTool}[revoke]
	if person {
		tool = map[bool]string{false: enableTool, true: disableTool}[disable]
	}
	arguments := strictjson.NewObject()
	if account != "" {
		arguments.Set("account_id", account)
	}
	if subject != "" {
		arguments.Set("subject", subject)
	} else {
		arguments.Set("to", to)
	}
	text, err := strictjson.Dumps(arguments, strictjson.Prepared)
	if err != nil {
		return nil, err
	}
	operation := make([]byte, 16)
	rand.Read(operation)
	started, err := r.store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: r.workspace.ID,
		OperationID: "grant-" + hex.EncodeToString(operation), TokenID: postgres.TokenID(randomToken()),
		Tool: tool, AccountRef: account, Arguments: []byte(text), Lease: time.Minute})
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
	people, err := r.store.People(ctx, r.workspace.ID, issuer)
	if err != nil {
		return refuse(503, "execution_or_audit_failure", local("The people of this workspace could not be read."))
	}
	p, status, missing := findPerson(people, to, subject)
	if missing != nil {
		code := map[int]string{404: "person_not_found", 409: "person_ambiguous"}[status]
		return refuse(status, code, missing)
	}
	if person {
		changed, ended, finished, err := r.store.ChangePerson(ctx, started, began, r.workspace.ID, p.ID, disable)
		if err != nil {
			return nil, err
		}
		shown := personObject(p)
		shown.Set("state", map[bool]string{false: "enabled", true: "disabled"}[disable])
		return strictjson.NewObject("person", shown, "changed", changed, "sessions_ended", ended,
			"receipt_id", finished.ID), nil
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
	// An account's name is the CRM's text, shown inert. The person's name was made inert as they signed in.
	if name, _, err = evidence.SafeText(name); err != nil {
		return nil, err
	}
	return strictjson.NewObject("account", account, "account_name", name, "person", personObject(p),
		"state", map[bool]string{false: "live", true: "revoked"}[revoke], "changed", changed,
		"receipt_id", finished.ID), nil
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
// hold or held. --to or --subject lists one person.
func (r *run) grants(ctx context.Context) (any, error) {
	to, subject, err := r.named(false)
	if err != nil {
		return nil, err
	}
	issuer, err := servesIssuer()
	if err != nil {
		return nil, err
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	listed := []any{}
	if !r.exists {
		if to != "" || subject != "" {
			return nil, noWorkspace
		}
		return listed, nil
	}
	people, err := r.store.People(ctx, r.workspace.ID, issuer)
	if err != nil {
		return nil, err
	}
	if to != "" || subject != "" {
		p, _, missing := findPerson(people, to, subject)
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
			if get(p, "state") == "disabled" {
				rows = append(rows, append(append([]any{}, who...), "all", "disabled", ""))
			}
		}
		return r.screen.table([]string{"PERSON", "EMAIL", "SUBJECT", "ACCOUNT", "STATE", "SINCE"}, rows)
	}
	o := result.(*strictjson.Object)
	p := get(o, "person").(*strictjson.Object)
	who := fmt.Sprintf("%s (%s)", cellText(get(p, "name")), cellText(orNone(get(p, "email"))))
	receipt := " Receipt " + cellText(get(o, "receipt_id")) + "."
	changed := get(o, "changed") == true
	var line string
	if _, isPerson := o.Get("sessions_ended"); isPerson {
		ended := cellText(get(o, "sessions_ended"))
		switch {
		case get(p, "state") == "disabled" && changed:
			line = "Disabled " + who + ": " + ended + " of their sessions ended, and they cannot sign in until --enable."
		case get(p, "state") == "disabled":
			line = who + " was disabled already; " + ended + " of their sessions ended."
		case changed:
			line = "Enabled " + who + ". They can sign in again, with the grants they hold."
		default:
			line = who + " was enabled already. Nothing changed."
		}
		return oneLine(line + receipt)
	}
	account := fmt.Sprintf("%s (%s)", cellText(get(o, "account")), cellText(get(o, "account_name")))
	switch {
	case get(o, "state") == "live" && changed:
		line = "Granted " + account + " to " + who + "."
	case get(o, "state") == "live":
		line = who + " already holds " + account + ". Nothing changed."
	case changed:
		line = "Revoked " + account + " from " + who + "."
	default:
		line = who + " holds no live grant on " + account + ". Nothing changed."
	}
	return oneLine(line + receipt)
}

// grantHelp and grantsHelp are the two commands' help.
var grantHelp = strings.TrimLeft(`
usage: gtm-connector grant [-h] [--json] [--state-dir STATE_DIR] [--to EMAIL]
                           [--subject SUBJECT] [--revoke] [--disable]
                           [--enable]
                           [ACCOUNT]

Give a person who signs in through the identity provider read access to one
account of a workspace, or with --revoke end it. --disable ends the person's
sessions at once and refuses their sign-ins until --enable. Each change gets a
receipt.

positional arguments:
  ACCOUNT               An account ID the workspace holds, such as acc-003.
                        --disable and --enable take none.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --to EMAIL            The person, by the email their identity provider
                        verified.
  --subject SUBJECT     The person, by their subject, as grants lists it.
  --revoke              End the person's grant on the account.
  --disable             End the person's sessions and refuse their sign-ins.
  --enable              Let a disabled person sign in again.

The people are those of the issuer in GTM_OIDC_ISSUER, as serve has it, each
listed from their first sign-in with no grant.
Examples:
  gtm-connector grant acc-003 --to dana@invented.example --state-dir acme
  gtm-connector grant acc-003 --subject 00u-dana --state-dir acme --revoke
  gtm-connector grant --disable --to dana@invented.example --state-dir acme
`, "\n")

var grantsHelp = strings.TrimLeft(`
usage: gtm-connector grants [-h] [--json] [--state-dir STATE_DIR] [--to EMAIL]
                            [--subject SUBJECT]

List the people who sign in to a workspace through the identity provider, and
every grant each holds or held.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --to EMAIL            List only this person, by a verified email.
  --subject SUBJECT     List only this person, by subject.

The people are those of the issuer in GTM_OIDC_ISSUER, as serve has it.
Example: gtm-connector grants --state-dir acme
`, "\n")
