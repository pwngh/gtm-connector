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

// ClientsName is the display name of the machine principal every MCP client token of a workspace belongs
// to.
const ClientsName = "MCP clients"

// ClientsPrincipal is the machine principal the MCP client tokens of a workspace belong to, made the first
// time a workspace issues one. Every client token of the workspace holds that principal's grants, so a grant
// revoked for it stays revoked for every token. A disabled one gives ErrDisabled and is never replaced.
func (s *Store) ClientsPrincipal(ctx context.Context, w Workspace) (Principal, error) {
	p := Principal{WorkspaceID: w.ID, WorkspaceKind: w.Kind, Kind: "machine", DisplayName: ClientsName}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// One transaction at a time looks for the principal and makes it, so a workspace gets only one.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gtm mcp clients ' || $1, 0))`,
			w.ID); err != nil {
			return err
		}
		var disabled bool
		err := tx.QueryRow(ctx, `
			SELECT id, disabled_at IS NOT NULL FROM principals
			 WHERE workspace_id = $1 AND kind = 'machine' AND display_name = $2
			 ORDER BY created_at, id LIMIT 1`, w.ID, ClientsName).Scan(&p.ID, &disabled)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return tx.QueryRow(ctx, `
				INSERT INTO principals (workspace_id, workspace_kind, kind, display_name)
				VALUES ($1, $2, 'machine', $3) RETURNING id`, w.ID, w.Kind, ClientsName).Scan(&p.ID)
		case err == nil && disabled:
			return ErrDisabled
		}
		return err
	})
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

// ClientToken is one machine token as the token commands show it. Fingerprint is the hex SHA-256 of the
// token, which is the token ID a receipt names; the token itself is never stored.
type ClientToken struct {
	ID, PrincipalID, Fingerprint string
	Scopes                       []string
	CreatedAt, ExpiresAt         time.Time
	RevokedAt                    *time.Time
}

const clientTokenColumns = `id::text, principal_id::text, encode(token_sha256, 'hex'), scopes, created_at, expires_at, revoked_at`

func scanClientToken(row pgx.Row) (ClientToken, error) {
	var t ClientToken
	err := row.Scan(&t.ID, &t.PrincipalID, &t.Fingerprint, &t.Scopes, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt)
	return t, err
}

// CreateMachineToken stores a new token of a machine principal by its SHA-256, with these scopes, expiring
// after lifetime by the database's clock.
func (s *Store) CreateMachineToken(ctx context.Context, p Principal, tokenSHA256 []byte, scopes []string, lifetime time.Duration) (ClientToken, error) {
	if len(tokenSHA256) != 32 || lifetime <= 0 {
		return ClientToken{}, errors.New("a token needs its SHA-256 and a lifetime")
	}
	return scanClientToken(s.pool.QueryRow(ctx, `
		INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
		RETURNING `+clientTokenColumns, p.WorkspaceID, p.ID, tokenSHA256, scopes, lifetime.Seconds()))
}

// MachineTokens lists the machine tokens of a workspace, oldest first, revoked and expired ones too.
func (s *Store) MachineTokens(ctx context.Context, workspaceID string) ([]ClientToken, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+clientTokenColumns+` FROM machine_tokens
		WHERE workspace_id = $1 ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []ClientToken{}
	for rows.Next() {
		t, err := scanClientToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeMachineToken revokes a token of a workspace by its ID and returns it. A token revoked before keeps
// its first revocation time. One the workspace does not hold gives ErrNotFound.
func (s *Store) RevokeMachineToken(ctx context.Context, workspaceID, id string) (ClientToken, error) {
	if !validUUID(workspaceID) || !validUUID(id) {
		return ClientToken{}, ErrNotFound
	}
	t, err := scanClientToken(s.pool.QueryRow(ctx, `
		UPDATE machine_tokens SET revoked_at = coalesce(revoked_at, now())
		 WHERE workspace_id = $1 AND id = $2 RETURNING `+clientTokenColumns, workspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientToken{}, ErrNotFound
	}
	return t, err
}

// RenewMachineToken moves a live token's expiry to lifetime from now. A token that is revoked or has
// expired is never renewed, and gives false.
func (s *Store) RenewMachineToken(ctx context.Context, workspaceID, id string, lifetime time.Duration) (bool, error) {
	if !validUUID(workspaceID) || !validUUID(id) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE machine_tokens SET expires_at = now() + make_interval(secs => $3)
		 WHERE workspace_id = $1 AND id = $2 AND revoked_at IS NULL AND expires_at > now()`,
		workspaceID, id, lifetime.Seconds())
	return err == nil && tag.RowsAffected() == 1, err
}
