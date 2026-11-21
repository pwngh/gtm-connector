package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MachineToken is what a stored bearer token opens. The store holds the token's SHA-256, never the token.
type MachineToken struct {
	WorkspaceID, PrincipalID string
	Scopes                   []string
	ExpiresAt                time.Time
	// Usable is false for a token that was revoked, whose principal is disabled, or that is limited to a
	// selection of accounts, which no server applies yet. Expiry is the caller's check, by its own clock.
	Usable bool
}

// MachineToken finds the token with this SHA-256 in a workspace, usable or not, or ErrNotFound. A token of
// another workspace is not found.
func (s *Store) MachineToken(ctx context.Context, workspaceID string, tokenSHA256 []byte) (MachineToken, error) {
	if !validUUID(workspaceID) || len(tokenSHA256) != 32 {
		return MachineToken{}, ErrNotFound
	}
	token := MachineToken{WorkspaceID: workspaceID}
	err := s.pool.QueryRow(ctx, `
		SELECT t.principal_id::text, t.scopes, t.expires_at,
		       t.revoked_at IS NULL AND p.disabled_at IS NULL AND t.selection IS NULL
		  FROM machine_tokens t
		  JOIN principals p ON p.workspace_id = t.workspace_id AND p.id = t.principal_id
		 WHERE t.workspace_id = $1 AND t.token_sha256 = $2`, workspaceID, tokenSHA256).
		Scan(&token.PrincipalID, &token.Scopes, &token.ExpiresAt, &token.Usable)
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineToken{}, ErrNotFound
	}
	return token, err
}

// CardByBrief reads the card of a workspace with this brief ID, with its bytes, or ErrNotFound.
func (s *Store) CardByBrief(ctx context.Context, workspaceID, briefID string) (Card, error) {
	if !validUUID(workspaceID) {
		return Card{}, ErrNotFound
	}
	return scanCard(s.pool.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.brief_id = $2`, workspaceID, briefID))
}
