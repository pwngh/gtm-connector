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
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// mcpGateway is the MCP endpoint serve mounts at /mcp: the read tools of the demo workspace over the demo
// book, for the machine tokens of that workspace. It makes sure the workspace holds a connection per source
// and every account of the book, as demo does. Tokens are issued for the public origin's /mcp, and that
// origin's Host is the only one the endpoint admits.
func mcpGateway(ctx context.Context, store *postgres.Store, publicOrigin string, selection cards.Selection) (*mcp.Gateway, error) {
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err != nil {
		return nil, mountError(err)
	}
	for _, source := range sourceNames {
		if _, err := store.EnsureConnection(ctx, workspace, source, "demo"); err != nil {
			return nil, mountError(err)
		}
	}
	from, err := workspaceReads(ctx, store, workspace)
	if err != nil {
		return nil, mountError(err)
	}
	// The tools read the book itself: they search within a selection and list every account's
	// opportunities, which no source of a workspace does yet.
	book, err := seededBook()
	if err != nil {
		return nil, mountError(err)
	}
	for _, account := range book.Accounts() {
		if err := store.EnsureAccount(ctx, workspace.ID, account.ID, account.Name); err != nil {
			return nil, mountError(err)
		}
	}
	return mcptools.New(mcptools.Config{Resource: publicOrigin + "/mcp", Issuer: publicOrigin, WorkspaceID: workspace.ID,
		Store: store, Receipts: &postgres.ReceiptStore{Store: store}, Source: book, Sources: from.sources(),
		Connections: from.connections, Selection: selection})
}

// mountError is what serve says when the MCP endpoint cannot be set up. An error from PostgreSQL is told by
// its SQLSTATE alone: its own text can name a role, a table or a host.
func mountError(err error) error {
	var refused *pgconn.PgError
	var unported *unportedSource
	switch {
	case errors.As(err, &refused):
		return fmt.Errorf("the MCP endpoint could not be set up: PostgreSQL error %s", refused.Code)
	case errors.As(err, &unported), errors.Is(err, errNotConnected):
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
