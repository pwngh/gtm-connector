package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The reads below are the owner's: the commands of the person who runs the
// install read a whole workspace, with no grant.

// Workspace finds a workspace by its slug.
func (s *Store) Workspace(ctx context.Context, slug string) (Workspace, error) {
	var w Workspace
	err := s.pool.QueryRow(ctx, `SELECT id, slug, kind, display_name FROM workspaces WHERE slug = $1`, slug).
		Scan(&w.ID, &w.Slug, &w.Kind, &w.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return w, err
}

// Connections lists what a workspace's sources are connected to.
func (s *Store) Connections(ctx context.Context, workspaceID string) ([]Connection, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT id, workspace_id, source, mode, generation FROM connections
		WHERE workspace_id = $1 ORDER BY source`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Connection, error) {
		var c Connection
		err := row.Scan(&c.ID, &c.WorkspaceID, &c.Source, &c.Mode, &c.Generation)
		return c, err
	})
}

// CardByBrief reads the card with this brief ID, with its bytes.
func (s *Store) CardByBrief(ctx context.Context, workspaceID, briefID string) (Card, error) {
	if !validUUID(workspaceID) {
		return Card{}, ErrNotFound
	}
	return scanCard(s.pool.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.brief_id = $2`, workspaceID, briefID))
}

// AccountBriefs lists the brief IDs of an account's cards, newest first.
func (s *Store) AccountBriefs(ctx context.Context, workspaceID, accountRef string) ([]string, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT brief_id FROM cards WHERE workspace_id = $1 AND account_ref = $2
		ORDER BY published_at DESC, id`, workspaceID, accountRef)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ReceiptFilter selects receipts of a workspace. A nil or zero field selects
// every receipt.
type ReceiptFilter struct {
	BriefID, Tool, TokenID, Status *string
	Since, Until                   time.Time // started at or after Since and before Until
}

// Receipts lists a workspace's receipts in the order they started, without
// their leases.
func (s *Store) Receipts(ctx context.Context, workspaceID string, f ReceiptFilter) ([]Receipt, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	where, args := []string{"r.workspace_id = $1"}, []any{workspaceID}
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	for column, value := range map[string]*string{"r.brief_id": f.BriefID, "r.tool": f.Tool,
		"r.token_id": f.TokenID, "r.status": f.Status} {
		if value != nil {
			add(column+" = $%d", *value)
		}
	}
	if !f.Since.IsZero() {
		add("r.started_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("r.started_at < $%d", f.Until)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+receiptColumns+` FROM receipts r WHERE `+
		strings.Join(where, " AND ")+` ORDER BY r.started_at, r.id`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Receipt, error) {
		r, err := scanReceipt(row)
		r.LeaseToken = ""
		return r, err
	})
}
