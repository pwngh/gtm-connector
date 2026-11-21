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

// WorkspaceByID finds a workspace by its ID.
func (s *Store) WorkspaceByID(ctx context.Context, id string) (Workspace, error) {
	if !validUUID(id) {
		return Workspace{}, ErrNotFound
	}
	var w Workspace
	err := s.pool.QueryRow(ctx, `SELECT id, slug, kind, display_name FROM workspaces WHERE id = $1`, id).
		Scan(&w.ID, &w.Slug, &w.Kind, &w.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return w, err
}

// Connections lists what a workspace's sources are connected to, by source.
func (s *Store) Connections(ctx context.Context, workspaceID string) ([]Connection, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	return connections(ctx, s.pool, workspaceID)
}

func connections(ctx context.Context, q querier, workspaceID string) ([]Connection, error) {
	rows, err := q.Query(ctx, `SELECT `+connectionColumns+` FROM connections
		WHERE workspace_id = $1 ORDER BY source`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Connection, error) { return scanConnection(row) })
}

// SourceChange is what one source of a workspace is to be connected to. Mode
// "none" detaches it.
type SourceChange struct {
	Source, Mode string
	Binding      string // the text of one JSON object; "" is the empty object
	// IdentitySHA256 pins the source to one identity. "" keeps the pin the
	// connection has: a pin is never cleared, not even by detaching.
	IdentitySHA256 string
}

// ErrPinned refuses a change that would connect a source to another identity
// than the one it is pinned to.
var ErrPinned = errors.New("the source is pinned to another identity")

// ErrDemoCallNotes refuses imported calls in a workspace that has read demo call notes.
var ErrDemoCallNotes = errors.New("the workspace has read demo call notes")

// SourceUpdate is one change to what a workspace reads.
type SourceUpdate struct {
	Changes []SourceChange
	// Sellers replaces the workspace's seller list. Nil keeps the saved one.
	Sellers []string
	// Speakers replaces the workspace's speaker settings, a JSON object. Empty keeps the saved one.
	Speakers string
}

// UpdateSources changes what a workspace's sources are connected to, one
// command at a time. It locks the workspace, gives decide the connections as
// they are then, and saves what decide returns in the same transaction: each
// source's connection, made if absent, and the seller list. A connection
// whose mode, binding or identity changes moves to its next generation, so a
// read that started before the change cannot finish ok. A connection pinned
// to one identity is never given another (ErrPinned). It returns the
// connections as they are afterwards. When decide fails, or any change is
// refused, nothing is saved.
func (s *Store) UpdateSources(ctx context.Context, w Workspace,
	decide func(current []Connection) (SourceUpdate, error)) ([]Connection, error) {
	if !validUUID(w.ID) {
		return nil, ErrNotFound
	}
	var after []Connection
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var locked bool
		err := tx.QueryRow(ctx, `SELECT true FROM workspaces WHERE id = $1 FOR UPDATE`, w.ID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		current, err := connections(ctx, tx, w.ID)
		if err != nil {
			return err
		}
		update, err := decide(current)
		if err != nil {
			return err
		}
		for _, c := range update.Changes {
			binding, identity := c.Binding, &c.IdentitySHA256
			if binding == "" {
				binding = "{}"
			}
			if c.IdentitySHA256 == "" {
				identity = nil
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO connections (workspace_id, workspace_kind, source, mode, binding, identity_sha256)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (workspace_id, source) DO UPDATE
				   SET mode = excluded.mode, binding = excluded.binding,
				       identity_sha256 = coalesce(excluded.identity_sha256, connections.identity_sha256),
				       generation = connections.generation + 1, updated_at = now()
				 WHERE (connections.mode, connections.binding, connections.identity_sha256) IS DISTINCT FROM
				       (excluded.mode, excluded.binding, coalesce(excluded.identity_sha256, connections.identity_sha256))`,
				w.ID, w.Kind, c.Source, c.Mode, binding, identity); sqlState(err) == "GT019" {
				return ErrPinned
			} else if sqlState(err) == "GT020" {
				return ErrDemoCallNotes
			} else if err != nil {
				return err
			}
		}
		if update.Sellers != nil {
			if _, err := tx.Exec(ctx, `UPDATE workspaces SET sellers = $2 WHERE id = $1`, w.ID, update.Sellers); err != nil {
				return err
			}
		}
		if update.Speakers != "" {
			if _, err := tx.Exec(ctx, `UPDATE workspaces SET speakers = $2 WHERE id = $1`, w.ID, update.Speakers); err != nil {
				return err
			}
		}
		after, err = connections(ctx, tx, w.ID)
		return err
	})
	return after, err
}

// Sellers is the identities the operator listed as the workspace's sellers,
// in the order given.
func (s *Store) Sellers(ctx context.Context, workspaceID string) ([]string, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	sellers := []string{}
	err := s.pool.QueryRow(ctx, `SELECT sellers FROM workspaces WHERE id = $1`, workspaceID).Scan(&sellers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sellers, err
}

// Speakers is the workspace's speaker settings as saved: a JSON object, "{}" when none are set.
func (s *Store) Speakers(ctx context.Context, workspaceID string) (string, error) {
	if !validUUID(workspaceID) {
		return "", ErrNotFound
	}
	var speakers string
	err := s.pool.QueryRow(ctx, `SELECT speakers FROM workspaces WHERE id = $1`, workspaceID).Scan(&speakers)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return speakers, err
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
