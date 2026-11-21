package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The mcp command serves the connector's MCP tools on stdin and stdout, for an assistant app that starts it
// as a process. It reads the workspace --state-dir names from PostgreSQL
// itself, as the app role, so no server needs to run. Each request passes the gate the HTTPS endpoint uses,
// as a machine principal: the workspace's MCP clients principal, holding a token made for this process with
// exactly the scopes given. The token is never shown. It lasts two minutes, is renewed every 30 seconds while
// the process runs, and is revoked when it ends; one that expired or was revoked is never renewed. A process
// that is killed leaves its token usable for at most two minutes.
//
// A client that closes its end of stdout, or stops reading it, ends the session through the same path as the
// end of its input: SIGPIPE is ignored, so a failed write is an error the transport sees, and each write is
// bounded by mcp.DefaultWriteTimeout.

var mcpArguments = arguments{options: []option{
	{name: "--scope", value: true, repeat: true, required: true, choices: mcp.ReadScopes},
	{name: "--selection", value: true}},
	required: "action", choices: []string{"stdio"}, help: mcpHelp}

// clientCommands are the commands that take an action first: mcp and token for MCP clients, and deal. main
// runs them as it runs the read commands.
var clientCommands = map[string]func(context.Context, []string, screen) int{"mcp": runMCP, "token": runToken,
	"deal": runDeal}

// stdioLifetime is how long the process's token lasts unless it is renewed, and stdioRenewal how often it is.
const (
	stdioLifetime = 2 * time.Minute
	stdioRenewal  = 30 * time.Second
)

// revokeWait bounds the revocation of the process's token as it ends.
const revokeWait = 5 * time.Second

// notRevoked is what the process says on stderr when its token could not be revoked.
const notRevoked = "gtm-connector mcp: the session token could not be revoked; it stops working within two minutes."

// runMCP runs the mcp command and gives its exit code. stdout carries only MCP messages once it serves.
func runMCP(ctx context.Context, tokens []string, s screen) int {
	signal.Ignore(syscall.SIGPIPE)
	return mcpCommand(ctx, tokens, s, os.Stdin)
}

func mcpCommand(ctx context.Context, tokens []string, s screen, in io.Reader) int {
	args, err := mcpArguments.parse(tokens)
	if err != nil {
		return s.fail("mcp", refused("%s", err))
	}
	if args.help {
		fmt.Fprint(s.out, mcpHelp)
		return 0
	}
	r := &run{command: "mcp", args: args, screen: s, name: defaultWorkspace}
	if name, given := args.text("--state-dir"); given {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	if err := r.serveStdio(ctx, in, s.out); err != nil && ctx.Err() == nil {
		return s.fail("mcp", r.failed(ctx, err))
	}
	return 0
}

// serveStdio serves the workspace's tools on in and out until in ends or the process is stopped. A selection
// file is read first, before the database, and never again.
func (r *run) serveStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	var selection cards.Selection
	if file, given := r.args.text("--selection"); given {
		if file == "" {
			return refused("--selection needs the name of a selection file; an empty name is not \"no selection\"")
		}
		var err error
		if selection, err = loadSelection(file); err != nil {
			return refused("%s", err)
		}
	}
	c, err := loadConfig()
	if err != nil {
		return local("%s", err)
	}
	if err := r.open(ctx); err != nil {
		return err
	}
	// Receipts that an earlier process left pending when it was killed or lost the database are finished
	// here, as serve finishes them, so an install that only runs stdio keeps none.
	if err := finishExpired(ctx, r.store); err != nil {
		return err
	}
	if _, err := r.reads(ctx); err != nil {
		return err
	}
	raw, issued, err := issueClientToken(ctx, r.store, r.workspace, scopeSet(r.args.lists["--scope"]), stdioLifetime)
	if err != nil {
		return err
	}
	defer revokeSession(context.WithoutCancel(ctx), r.store, r.workspace.ID, issued.ID, r.screen.err)
	serving, stop := context.WithCancel(ctx)
	defer stop()
	go renew(serving, r.store, r.workspace.ID, issued.ID)
	// Each call reads the workspace's sources as they are connected when it starts, through the seam serve's
	// tools use, so a source removed or replaced while the process runs is refused here too.
	config, err := mcptools.Stdio(mcptools.Config{Resource: c.publicOrigin + "/mcp", Issuer: c.publicOrigin,
		WorkspaceID: r.workspace.ID, Store: r.store, Receipts: &postgres.ReceiptStore{Store: r.store},
		Open: toolSources(r.store, r.workspace), Bound: toolBindings(r.store, r.workspace), Selection: selection}, raw)
	if err != nil {
		return err
	}
	return mcp.ServeStdio(serving, config, in, out)
}

// renew moves the token's expiry forward every stdioRenewal until ctx ends. A renewal that fails leaves the
// token to expire, after which every call is refused; nothing is retried with another token.
func renew(ctx context.Context, store *postgres.Store, workspaceID, tokenID string) {
	ticker := time.NewTicker(stdioRenewal)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if live, err := store.RenewMachineToken(ctx, workspaceID, tokenID, stdioLifetime); err == nil && !live {
			return
		}
	}
}

// revokeSession revokes the process's token within revokeWait, and says so on stderr in one fixed line when
// it cannot.
func revokeSession(ctx context.Context, store *postgres.Store, workspaceID, tokenID string, stderr io.Writer) {
	revoking, cancel := context.WithTimeout(ctx, revokeWait)
	defer cancel()
	if _, err := store.RevokeMachineToken(revoking, workspaceID, tokenID); err != nil {
		fmt.Fprintln(stderr, notRevoked)
	}
}

// finishExpired finishes, as interrupted, every pending receipt whose worker's lease has expired.
func finishExpired(ctx context.Context, store *postgres.Store) error {
	for {
		n, err := store.AbandonExpired(ctx, 100)
		if err != nil || n < 100 {
			return err
		}
	}
}

// issueClientToken makes a token for the workspace's MCP clients principal and gives it, never stored, with
// its row. The principal's grants on the connected CRM's accounts and the token are written in one
// transaction, so a token that cannot be stored widens no other token's access. The accounts are those of
// the demo book beside the demo CRM, and beside a real CRM those the workspace holds: each account a command
// has listed there, as brief does. A workspace with no CRM, or with one the MCP tools do not serve, gets no
// token. Opening the CRM reads its row only: nothing is read from the source here.
func issueClientToken(ctx context.Context, store *postgres.Store, workspace postgres.Workspace,
	scopes []string, lifetime time.Duration) (string, postgres.ClientToken, error) {
	from, err := workspaceReads(ctx, store, workspace)
	if err != nil {
		return "", postgres.ClientToken{}, err
	}
	opened, err := from.crm(ctx)
	if err != nil {
		return "", postgres.ClientToken{}, err
	}
	if _, serves := opened.(servedCRM); !serves {
		return "", postgres.ClientToken{}, &failure{code: 3, message: "This workspace's CRM is " +
			from.described("crm") + ", which the MCP tools do not serve in this build."}
	}
	var accounts []postgres.ClientAccount
	if from.backend("crm") == "sqlite" {
		book, err := seededBook()
		if err != nil {
			return "", postgres.ClientToken{}, err
		}
		for _, account := range book.Accounts() {
			accounts = append(accounts, postgres.ClientAccount{Ref: account.ID, Name: account.Name})
		}
	} else if accounts, err = store.HeldAccounts(ctx, workspace.ID); err != nil {
		return "", postgres.ClientToken{}, err
	}
	raw := randomToken()
	digest := sha256.Sum256([]byte(raw))
	issued, err := store.IssueClientToken(ctx, workspace, accounts, digest[:], scopes, lifetime)
	switch {
	case errors.Is(err, postgres.ErrDisabled):
		return "", issued, &failure{code: 3, message: "The " + postgres.ClientsName + " principal of this workspace is " +
			"disabled, so no client token is issued for it."}
	case err != nil:
		return "", issued, err
	}
	return raw, issued, nil
}

// scopeSet is the scopes given, each once, in the order mcp.ReadScopes lists them.
func scopeSet(given []string) []string {
	var scopes []string
	for _, scope := range mcp.ReadScopes {
		if slices.Contains(given, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

const mcpHelp = `usage: gtm-connector mcp [-h] [--json] [--state-dir STATE_DIR]
                         --scope {crm:read,calls:read,slack:read}
                         [--selection SELECTION]
                         {stdio}

Run the local stdio MCP server that an assistant app starts.

positional arguments:
  {stdio}               Serve MCP on stdin and stdout. An assistant app starts this command.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --scope {crm:read,calls:read,slack:read}
                        Grant this scope. Repeat for each explicit grant.
  --selection SELECTION
                        Serve only the accounts and Slack threads in this private selection file,
                        and add the two brief tools.

It reads the workspace from PostgreSQL at GTM_DATABASE_URL itself, so no server needs to run.
Example: gtm-connector mcp stdio --scope crm:read
`
