package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/mcptools"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// mcpGateway is the MCP endpoint serve mounts at /mcp: the read tools of the demo workspace, for the machine
// tokens of that workspace. A source that was never connected there is connected to the demo data, and the
// accounts of the demo book are listed, as demo does. Tokens are issued for the public origin's /mcp, and
// that origin's Host is the only one the endpoint admits.
//
// The endpoint holds no source. Every call asks the workspace what it is connected to at that moment
// (toolSources), so a source detached or connected again while the server runs is not read through what
// was opened before. A workspace with no CRM is served too: the browser surface works, and every tool is
// refused by name, under its receipt.
func mcpGateway(ctx context.Context, store *postgres.Store, publicOrigin string, selection cards.Selection) (*mcp.Gateway, error) {
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		return nil, mountError(err)
	}
	for _, source := range sourceNames {
		// A row that exists is left as it is: a source the operator removed stays removed.
		if _, err := store.EnsureConnection(ctx, workspace, source, "demo"); err != nil {
			return nil, mountError(err)
		}
	}
	from, err := workspaceReads(ctx, store, workspace)
	switch {
	case errors.Is(err, errNotConnected):
	case err != nil:
		return nil, mountError(err)
	case from.backend("crm") == "sqlite":
		book, err := seededBook()
		if err != nil {
			return nil, mountError(err)
		}
		for _, account := range book.Accounts() {
			if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
				return nil, mountError(err)
			}
		}
	}
	return mcptools.New(mcptools.Config{Resource: publicOrigin + "/mcp", Issuer: publicOrigin, WorkspaceID: workspace.ID,
		Store: store, Receipts: &postgres.ReceiptStore{Store: store}, Open: toolSources(store, workspace),
		Selection: selection})
}

// toolSources gives the MCP tools the workspace's sources as they are connected when a call starts. The
// tools search within a selection and list every account's opportunities, which only the demo book does
// in this build, so a workspace whose CRM is any other is refused by name. So is one with no CRM.
func toolSources(store *postgres.Store, workspace postgres.Workspace) func(context.Context) (mcptools.Opened, error) {
	return func(ctx context.Context) (mcptools.Opened, error) {
		from, err := workspaceReads(ctx, store, workspace)
		switch {
		case errors.Is(err, errNotConnected):
			return mcptools.Opened{}, mcp.ToolError("crm_not_connected")
		case err != nil:
			return mcptools.Opened{}, err
		case from.backend("crm") != "sqlite":
			return mcptools.Opened{}, mcp.ToolError("crm_not_served")
		}
		book, err := seededBook()
		return mcptools.Opened{Source: servedBook{cardSource{from}, book}, Sources: from.sources(),
			Connections: from.connections(), Generations: from.generations()}, err
	}
}

// servedBook is a workspace's sources as the MCP tools read them: each of a card's reads from the source
// the workspace is connected to, and the two CRM reads no card makes from the demo book.
type servedBook struct {
	cardSource
	book *demo.Store
}

func (b servedBook) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	system, records, err := b.cardSource.Read(ctx, tool, accountID)
	var unported *unportedSource
	switch {
	case errors.Is(err, errAccountNotFound):
		err = demo.ErrAccountNotFound
	case errors.As(err, &unported):
		err = mcp.ToolError(system + "_not_served")
	}
	return system, records, err
}

func (b servedBook) Search(query string, accounts []string) ([]*strictjson.Object, bool) {
	return b.book.Search(query, accounts)
}

func (b servedBook) Opportunities(accounts []string) []*strictjson.Object {
	return b.book.Opportunities(accounts)
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
