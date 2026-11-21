package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The mcp command serves the connector's MCP tools on stdin and stdout, for an assistant app that starts it
// as a process (gtm_connector/stdio_bridge.py). It reads the workspace --state-dir names from PostgreSQL
// itself, as the app role, so no server needs to run. Each request passes the gate the HTTPS endpoint uses,
// as a machine principal: the workspace's MCP clients principal, holding a token made for this process with
// exactly the scopes given. The token is never shown. It lasts five minutes, is renewed every minute while
// the process runs, and is revoked when it ends; one that expired or was revoked is never renewed.

var mcpArguments = arguments{options: []option{
	{name: "--scope", value: true, repeat: true, required: true, choices: mcp.ReadScopes},
	{name: "--selection", value: true}},
	required: "action", choices: []string{"stdio"}, help: mcpHelp}

// clientCommands are the commands for MCP clients, which main runs as it runs the read commands.
var clientCommands = map[string]func(context.Context, []string, screen) int{"mcp": runMCP, "token": runToken}

// stdioLifetime is how long the process's token lasts unless it is renewed, and stdioRenewal how often it is.
const (
	stdioLifetime = 5 * time.Minute
	stdioRenewal  = time.Minute
)

// runMCP runs the mcp command and gives its exit code. stdout carries only MCP messages once it serves.
func runMCP(ctx context.Context, tokens []string, s screen) int {
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
		return err
	}
	if err := r.open(ctx); err != nil {
		return err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return mountError(err)
	}
	principal, err := grantClients(ctx, r.store, r.workspace, from)
	if err != nil {
		return clientsError(err)
	}
	raw := randomToken()
	digest := sha256.Sum256([]byte(raw))
	issued, err := r.store.CreateMachineToken(ctx, principal, digest[:], scopeSet(r.args.lists["--scope"]), stdioLifetime)
	if err != nil {
		return mountError(err)
	}
	defer r.store.RevokeMachineToken(context.WithoutCancel(ctx), r.workspace.ID, issued.ID)
	serving, stop := context.WithCancel(ctx)
	defer stop()
	go renew(serving, r.store, r.workspace.ID, issued.ID)
	config, err := mcptools.Stdio(mcptools.Config{Resource: c.publicOrigin + "/mcp", Issuer: c.publicOrigin,
		WorkspaceID: r.workspace.ID, Store: r.store, Receipts: &postgres.ReceiptStore{Store: r.store},
		Source: from.book, Sources: from.sources, Connections: from.connections, Selection: selection}, raw)
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

// grantClients is the workspace's MCP clients principal, with a grant on every account of the connected
// book that it was never granted. A grant that was revoked stays revoked, so an account taken from the
// clients is never given back by issuing a token.
func grantClients(ctx context.Context, store *postgres.Store, workspace postgres.Workspace, from reads) (postgres.Principal, error) {
	principal, err := store.ClientsPrincipal(ctx, workspace)
	if err != nil {
		return principal, err
	}
	for _, account := range from.book.Accounts() {
		if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
			return principal, err
		}
		if _, err := store.EnsureAccountGrant(ctx, workspace.ID, principal.ID, account.ID); err != nil {
			return principal, err
		}
	}
	return principal, nil
}

// clientsError is what a command says when the clients principal cannot be had.
func clientsError(err error) error {
	if errors.Is(err, postgres.ErrDisabled) {
		return &failure{code: 3, message: "The " + postgres.ClientsName + " principal of this workspace is disabled, " +
			"so no client token is issued for it."}
	}
	return mountError(err)
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
