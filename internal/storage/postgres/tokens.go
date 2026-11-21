package postgres

import (
	"context"
	"crypto/subtle"
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
	var stored []byte
	err := s.pool.QueryRow(ctx, `
		SELECT t.token_sha256, t.principal_id::text, t.scopes, t.expires_at,
		       t.revoked_at IS NULL AND p.disabled_at IS NULL AND t.selection IS NULL
		  FROM machine_tokens t
		  JOIN principals p ON p.workspace_id = t.workspace_id AND p.id = t.principal_id
		 WHERE t.workspace_id = $1 AND t.token_sha256 = $2`, workspaceID, tokenSHA256).
		Scan(&stored, &token.PrincipalID, &token.Scopes, &token.ExpiresAt, &token.Usable)
	// The row is fetched by the fingerprint and confirmed here in constant time, as a session's is.
	if errors.Is(err, pgx.ErrNoRows) || err == nil && subtle.ConstantTimeCompare(stored, tokenSHA256) != 1 {
		return MachineToken{}, ErrNotFound
	}
	if err != nil {
		return MachineToken{}, err
	}
	return token, nil
}
