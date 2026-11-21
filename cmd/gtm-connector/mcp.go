package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// mcpGateway is the MCP endpoint serve mounts at /mcp: the read tools of every workspace, each for the
// machine tokens of its own workspace. A request goes to the gate of the workspace its bearer was issued in,
// which verifies it in full; one whose bearer no workspace issued goes to the demo workspace's gate, which
// refuses it as any gate refuses an unknown bearer. In the demo workspace a source that was never connected
// is connected to the demo data, and the accounts of the demo book are listed, as demo does. Tokens are
// issued for the public origin's /mcp, and that origin's Host is the only one the endpoint admits. A
// selection file names demo accounts, so with one only the demo workspace is served.
//
// The endpoint holds no source. Every call asks the workspace what it is connected to at that moment
// (toolSources), so a source detached or connected again while the server runs is not read through what
// was opened before. A workspace with no CRM is served too: the browser surface works, and every tool is
// refused by name, under its receipt.
//
// A request whose bearer's workspace cannot be read, because the database fails or takes longer than
// tokenLookup, is the one refusal of a call that keeps no receipt: no gate can take it, since a gate keeps
// the receipts of its own workspace. It is answered as a gate answers a call whose receipt could not be
// committed, and logged in one fixed line. Making the gate of a workspace that was read reads nothing, and
// fails only on settings the demo gate was made with at mount, so it is answered the same way.
func mcpGateway(ctx context.Context, store *postgres.Store, publicOrigin string, selection cards.Selection,
	logger *slog.Logger) (http.Handler, error) {
	demoGate, workspace, err := demoGateway(ctx, store, publicOrigin, selection)
	if err != nil {
		return nil, err
	}
	return &mcpEndpoints{store: store, publicOrigin: publicOrigin, demo: demoGate, demoID: workspace.ID,
		selected: selection != nil, gates: map[string]*mcp.Gateway{}, logger: logger}, nil
}

// mcpEndpoints is /mcp across workspaces: one gate for each workspace a bearer names, made on its first
// request.
type mcpEndpoints struct {
	store        *postgres.Store
	publicOrigin string
	demo         *mcp.Gateway
	demoID       string
	selected     bool
	logger       *slog.Logger

	mu    sync.Mutex
	gates map[string]*mcp.Gateway
}

// tokenLookup bounds the read of which workspace a bearer belongs to.
const tokenLookup = 5 * time.Second

func (e *mcpEndpoints) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	gate := e.demo
	// The bearer as the gate reads it: the last Authorization header, "Bearer TOKEN".
	authorization := r.Header.Values("Authorization")
	if parts := strings.Split(lastOf(authorization), " "); !e.selected && len(parts) == 2 &&
		strings.ToLower(parts[0]) == "bearer" && parts[1] != "" {
		digest := sha256.Sum256([]byte(parts[1]))
		ctx, cancel := context.WithTimeout(r.Context(), tokenLookup)
		workspace, err := e.store.TokenWorkspace(ctx, digest[:])
		if err == nil && workspace.ID != e.demoID {
			gate, err = e.gate(ctx, workspace)
		}
		cancel()
		if err != nil && !errors.Is(err, postgres.ErrNotFound) {
			e.logger.Warn("an MCP request was refused with no receipt: the workspace of its bearer could not be read",
				"error", plain(err))
			mcp.AuditUnavailable(w)
			return
		}
	}
	gate.ServeHTTP(w, r)
}

func lastOf(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// gate is a workspace's own gate, made once.
func (e *mcpEndpoints) gate(ctx context.Context, workspace postgres.Workspace) (*mcp.Gateway, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gate, made := e.gates[workspace.ID]; made {
		return gate, nil
	}
	gate, err := mcptools.New(mcptools.Config{Resource: e.publicOrigin + "/mcp", Issuer: e.publicOrigin,
		WorkspaceID: workspace.ID, Store: e.store, Receipts: &postgres.ReceiptStore{Store: e.store},
		Open: toolSources(e.store, workspace)})
	if err != nil {
		return nil, err
	}
	e.gates[workspace.ID] = gate
	return gate, nil
}

// demoGateway is the demo workspace's gate.
func demoGateway(ctx context.Context, store *postgres.Store, publicOrigin string,
	selection cards.Selection) (*mcp.Gateway, postgres.Workspace, error) {
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		return nil, workspace, mountError(err)
	}
	for _, source := range sourceNames {
		// A row that exists is left as it is: a source the operator removed stays removed.
		if _, err := store.EnsureConnection(ctx, workspace, source, "demo"); err != nil {
			return nil, workspace, mountError(err)
		}
	}
	from, err := workspaceReads(ctx, store, workspace)
	switch {
	case errors.Is(err, errNotConnected):
	case err != nil:
		return nil, workspace, mountError(err)
	case from.backend("crm") == "sqlite":
		book, err := seededBook()
		if err != nil {
			return nil, workspace, mountError(err)
		}
		for _, account := range book.Accounts() {
			if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
				return nil, workspace, mountError(err)
			}
		}
	}
	gate, err := mcptools.New(mcptools.Config{Resource: publicOrigin + "/mcp", Issuer: publicOrigin,
		WorkspaceID: workspace.ID, Store: store, Receipts: &postgres.ReceiptStore{Store: store},
		Open: toolSources(store, workspace), Selection: selection})
	return gate, workspace, err
}

// servedCRM is a CRM the MCP tools serve: besides a card's two reads, a search and a list of opportunities,
// each within the accounts the caller may read.
type servedCRM interface {
	Search(ctx context.Context, query string, number *string, accounts []string) ([]*strictjson.Object, bool, error)
	Opportunities(ctx context.Context, accounts []string) ([]*strictjson.Object, error)
}

// toolSources gives the MCP tools the workspace's sources as they are connected when a call starts, through
// the seam every command reads them by. A CRM is opened from its row; a real one signs in on its first read
// and checks its pin then, as a command's does. The CRM opened for a row is kept while the row stays at the
// same generation, so one sign-in serves the calls that follow it. A workspace with no CRM, or with one the
// tools do not serve, is refused by name.
func toolSources(store *postgres.Store, workspace postgres.Workspace) func(context.Context) (mcptools.Opened, error) {
	var mu sync.Mutex
	var held struct {
		row postgres.Connection
		crm crmSource
	}
	return func(ctx context.Context) (mcptools.Opened, error) {
		from, err := workspaceReads(ctx, store, workspace)
		switch {
		case errors.Is(err, errNotConnected):
			return mcptools.Opened{}, mcp.ToolError("crm_not_connected")
		case err != nil:
			return mcptools.Opened{}, err
		}
		row := from.rows["crm"]
		mu.Lock()
		if held.crm != nil && held.row == row {
			from.crmReads = held.crm
		}
		mu.Unlock()
		opened, err := from.crm(ctx)
		var unported *unportedSource
		switch {
		case errors.As(err, &unported):
			return mcptools.Opened{}, mcp.ToolError("crm_not_served")
		case err != nil:
			return mcptools.Opened{}, err
		}
		served, serves := opened.(servedCRM)
		if !serves {
			return mcptools.Opened{}, mcp.ToolError("crm_not_served")
		}
		mu.Lock()
		held.row, held.crm = row, opened
		mu.Unlock()
		return mcptools.Opened{Source: servedBook{cardSource{from}, served}, Sources: from.sources(),
			Connections: from.connections(), Generations: from.generations()}, nil
	}
}

// servedBook is a workspace's sources as the MCP tools read them: each of a card's reads from the source
// the workspace is connected to, and the two CRM reads no card makes from its CRM. A refusal of the CRM is a
// tool error that names its code.
type servedBook struct {
	cardSource
	crm servedCRM
}

func (b servedBook) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	system, records, err := b.cardSource.Read(ctx, tool, accountID)
	var unported *unportedSource
	switch {
	case errors.Is(err, errAccountNotFound):
		err = demo.ErrAccountNotFound
	case errors.As(err, &unported):
		err = mcp.ToolError(system + "_not_served")
	default:
		err = toolRefusal(err)
	}
	return system, records, err
}

func (b servedBook) Search(ctx context.Context, query string, number *string, accounts []string) ([]*strictjson.Object, bool, error) {
	records, more, err := b.crm.Search(ctx, query, number, accounts)
	return records, more, toolRefusal(err)
}

func (b servedBook) Opportunities(ctx context.Context, accounts []string) ([]*strictjson.Object, error) {
	records, err := b.crm.Opportunities(ctx, accounts)
	return records, toolRefusal(err)
}

// toolRefusal is a source's refusal as a tool error naming its code, such as salesforce_auth_required or
// source_identity_mismatch. Any other error is returned as it is.
func toolRefusal(err error) error {
	if code := failureCode(err); code != "" {
		return mcp.ToolError(code)
	}
	return err
}

// The demo book as a CRM the tools serve. It has no account numbers.
func (d demoBook) Search(_ context.Context, query string, number *string, accounts []string) ([]*strictjson.Object, bool, error) {
	if number != nil {
		return nil, false, mcp.ToolError("account_number_requires_salesforce")
	}
	records, more := d.book.Search(query, accounts)
	return records, more, nil
}

func (d demoBook) Opportunities(_ context.Context, accounts []string) ([]*strictjson.Object, error) {
	return d.book.Opportunities(accounts), nil
}

// mountError is what serve says when the MCP endpoint cannot be set up. An error from PostgreSQL is told by
// its SQLSTATE alone: its own text can name a role, a table or a host.
func mountError(err error) error {
	var refused *pgconn.PgError
	var unported *unportedSource
	switch {
	case errors.As(err, &refused):
		return fmt.Errorf("the MCP endpoint could not be set up: PostgreSQL error %s", refused.Code)
	case errors.As(err, &unported):
		return err
	}
	return errors.New("the MCP endpoint could not be set up")
}

// maxSelectionBytes is the largest selection file read.
const maxSelectionBytes = 65536

// loadSelection reads the selection file serve was given (selection.load_selection): one regular file its
// reader owns and nobody else can write, never a symbolic link, of at most 64 KiB.
func loadSelection(path string) (cards.Selection, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errors.New("The selection file was not found.")
	case err != nil:
		return nil, errors.New("The selection file cannot be opened. It must be a regular file, not a symbolic link.")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("The selection file cannot be opened. It must be a regular file, not a symbolic link.")
	}
	owner, known := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !known || int(owner.Uid) != os.Getuid() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("The selection file must be a regular file you own that others cannot write.")
	}
	return readSelection(file)
}

// readSelection reads an opened selection file to its end and parses it.
func readSelection(file io.Reader) (cards.Selection, error) {
	document, err := io.ReadAll(io.LimitReader(file, maxSelectionBytes+1))
	switch {
	case err != nil:
		return nil, errors.New("The selection file could not be read.")
	case len(document) > maxSelectionBytes:
		return nil, fmt.Errorf("The selection file is larger than %d bytes.", maxSelectionBytes)
	}
	return cards.ParseSelection(document, demoSources.CRMBackend)
}
