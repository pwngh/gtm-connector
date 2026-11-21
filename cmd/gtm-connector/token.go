package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The token command issues, lists and revokes the bearer tokens an MCP client presents at serve's /mcp, as
// connect APP --scope did for an HTTP client (gtm_connector/connections.py). A token is for one workspace,
// carries only the read scopes it is given, and expires: after five minutes by default, as the Python
// authority's grants did, and after at most an hour. It belongs to the workspace's MCP clients principal,
// which is granted every account of the connected book it was never granted, so a grant revoked for it stays
// revoked. The token is printed once, when it is made. The store keeps only its SHA-256, which is the token
// ID its receipts name and the CALLER token list prints.

var tokenActions = map[string]arguments{
	"new": {options: []option{
		{name: "--scope", value: true, repeat: true, required: true, choices: mcp.ReadScopes},
		{name: "--minutes", value: true}}, help: tokenNewHelp},
	"list":   {help: tokenListHelp},
	"revoke": {required: "ID", help: tokenRevokeHelp},
}

// uuidText is a token ID as token list prints it.
var uuidText = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// The lifetime of a token unless --minutes gives another, and the longest it may have.
const (
	tokenMinutes    = 5
	tokenMaxMinutes = 60
)

// runToken runs the token command and gives its exit code.
func runToken(ctx context.Context, tokens []string, s screen) int {
	if len(tokens) == 0 {
		return s.fail("token", refused("the following arguments are required: action"))
	}
	if tokens[0] == "-h" || tokens[0] == "--help" {
		fmt.Fprint(s.out, tokenHelp)
		return 0
	}
	action := tokens[0]
	spec, known := tokenActions[action]
	if !known {
		return s.fail("token", refused("argument action: invalid choice (choose from 'new', 'list', 'revoke')"))
	}
	command := "token " + action
	args, err := spec.parse(tokens[1:])
	if err != nil {
		return s.fail(command, refused("%s", err))
	}
	if args.help {
		fmt.Fprint(s.out, spec.help)
		return 0
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
	var result any
	switch action {
	case "new":
		result, err = r.newToken(ctx)
	case "list":
		result, err = r.listTokens(ctx)
	default:
		result, err = r.revokeToken(ctx)
	}
	if err != nil {
		return s.fail(command, r.failed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
		return 0
	}
	fmt.Fprintln(s.out, r.tokenText(result))
	return 0
}

// newToken makes a token for the workspace's MCP clients principal and gives it, with what it opens.
func (r *run) newToken(ctx context.Context) (any, error) {
	minutes := tokenMinutes
	if text, given := r.args.text("--minutes"); given {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > tokenMaxMinutes || strings.TrimSpace(text) != text {
			return nil, refused("argument --minutes: a whole number of minutes from 1 to %d", tokenMaxMinutes)
		}
		minutes = n
	}
	c, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, mountError(err)
	}
	principal, err := grantClients(ctx, r.store, r.workspace, from)
	if err != nil {
		return nil, clientsError(err)
	}
	raw := randomToken()
	digest := sha256.Sum256([]byte(raw))
	issued, err := r.store.CreateMachineToken(ctx, principal, digest[:], scopeSet(r.args.lists["--scope"]),
		time.Duration(minutes)*time.Minute)
	if err != nil {
		return nil, mountError(err)
	}
	result := tokenObject(issued, time.Now())
	result.Keys = append([]string{"token", "workspace"}, result.Keys...)
	result.Values = append([]any{raw, r.workspace.Slug}, result.Values...)
	result.Set("url", c.publicOrigin+"/mcp")
	return result, nil
}

// listTokens lists the workspace's machine tokens, oldest first.
func (r *run) listTokens(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	listed := []any{}
	if !r.exists {
		return listed, nil
	}
	tokens, err := r.store.MachineTokens(ctx, r.workspace.ID)
	if err != nil {
		return nil, mountError(err)
	}
	now := time.Now()
	for _, t := range tokens {
		listed = append(listed, tokenObject(t, now))
	}
	return listed, nil
}

// revokeToken revokes one token of the workspace by the ID token list prints.
func (r *run) revokeToken(ctx context.Context) (any, error) {
	id := r.args.positionals[0]
	if !uuidText.MatchString(id) {
		return nil, refused("A token ID is the UUID that gtm-connector token list prints.")
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	if !r.exists {
		return nil, refused("No token in this workspace has that ID.")
	}
	revoked, err := r.store.RevokeMachineToken(ctx, r.workspace.ID, id)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return nil, refused("No token in this workspace has that ID.")
	case err != nil:
		return nil, mountError(err)
	}
	return tokenObject(revoked, time.Now()), nil
}

// tokenObject is a token as the command shows it, never the token itself: caller is its SHA-256, the token
// ID its receipts name.
func tokenObject(t postgres.ClientToken, now time.Time) *strictjson.Object {
	state := "live"
	switch {
	case t.RevokedAt != nil:
		state = "revoked"
	case !t.ExpiresAt.After(now):
		state = "expired"
	}
	scopes := make([]any, len(t.Scopes))
	for i, scope := range t.Scopes {
		scopes[i] = scope
	}
	var revoked any
	if t.RevokedAt != nil {
		revoked = utcSeconds(*t.RevokedAt)
	}
	return strictjson.NewObject("id", t.ID, "scopes", scopes, "state", state, "created_at", utcSeconds(t.CreatedAt),
		"expires_at", utcSeconds(t.ExpiresAt), "revoked_at", revoked, "caller", t.Fingerprint)
}

func utcSeconds(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// tokenText is a result as a person reads it.
func (r *run) tokenText(result any) string {
	switch v := result.(type) {
	case []any:
		if len(v) == 0 {
			return "No tokens. Make one with gtm-connector token new --scope crm:read" + r.here() + "."
		}
		rows := make([][]any, len(v))
		for i, item := range v {
			t := item.(*strictjson.Object)
			rows[i] = []any{get(t, "id"), joinedScopes(t), get(t, "state"), get(t, "expires_at"), get(t, "caller")}
		}
		return r.screen.table([]string{"ID", "SCOPES", "STATE", "EXPIRES", "CALLER"}, rows)
	}
	t := result.(*strictjson.Object)
	if _, made := t.Get("token"); !made {
		return fmt.Sprintf("Revoked token %s. A client presenting it is refused from now on.", get(t, "id"))
	}
	return strings.Join([]string{
		fmt.Sprint(get(t, "token")),
		"",
		"This is the bearer token. It is shown once and not stored: keep it out of files others can read,",
		"commits and model prompts.",
		fmt.Sprintf("ID %s, for the workspace %s, with %s, expires %s.", get(t, "id"), get(t, "workspace"),
			joinedScopes(t), get(t, "expires_at")),
		fmt.Sprintf("Send it as Authorization: Bearer TOKEN to %s, which serve answers for the demo workspace.",
			get(t, "url")),
		"Its calls: gtm-connector audit --caller " + fmt.Sprint(get(t, "caller")) + r.here(),
		"Revoke it: gtm-connector token revoke " + fmt.Sprint(get(t, "id")) + r.here(),
	}, "\n")
}

func joinedScopes(t *strictjson.Object) string {
	scopes, _ := get(t, "scopes").([]any)
	names := make([]string, len(scopes))
	for i, scope := range scopes {
		names[i] = fmt.Sprint(scope)
	}
	return strings.Join(names, " ")
}

const tokenHelp = `usage: gtm-connector token [-h] {new,list,revoke} ...

Issue, list and revoke the bearer tokens MCP clients present at serve's /mcp.

positional arguments:
  {new,list,revoke}
    new                 Make a token and print it once.
    list                List the tokens of a workspace.
    revoke              Revoke a token by its ID.

options:
  -h, --help            show this help message and exit

Each action takes --json, --state-dir WORKSPACE and --help.
Example: gtm-connector token new --scope crm:read
`

const tokenNewHelp = `usage: gtm-connector token new [-h] [--json] [--state-dir STATE_DIR]
                               --scope {crm:read,calls:read,slack:read}
                               [--minutes MINUTES]

Make a bearer token for an MCP client and print it once. Only its SHA-256 is stored.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Issue it for this workspace (default: demo).
  --scope {crm:read,calls:read,slack:read}
                        Grant this scope. Repeat for each explicit grant.
  --minutes MINUTES     Let it last this many minutes, from 1 to 60 (default: 5).

The token reads the accounts the workspace's MCP clients principal is granted.
Example: gtm-connector token new --scope crm:read --scope calls:read
`

const tokenListHelp = `usage: gtm-connector token list [-h] [--json] [--state-dir STATE_DIR]

List the machine tokens of a workspace, with their scopes, state and expiry. A token itself is never shown.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).

CALLER is the token ID its receipts name: gtm-connector audit --caller CALLER lists its calls.
Example: gtm-connector token list
`

const tokenRevokeHelp = `usage: gtm-connector token revoke [-h] [--json] [--state-dir STATE_DIR] ID

Revoke a token, so a client presenting it is refused from its next call.

positional arguments:
  ID                    A token ID that token list prints.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).

Example: gtm-connector token revoke TOKEN_ID
`
