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
	// Usable is false for a token that was revoked, that has expired by the database's clock, whose principal
	// is disabled, or that is limited to a selection of accounts, which no server applies yet. The database's
	// clock both sets an expiry and decides it has come, so a process whose clock is behind cannot admit a
	// token past it. A caller may also refuse it earlier by its own clock.
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
		       t.revoked_at IS NULL AND t.expires_at > now() AND p.disabled_at IS NULL AND t.selection IS NULL
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

// ClientAccount is an account of a workspace's connected CRM, by its ref and its name.
type ClientAccount struct{ Ref, Name string }

// IssueClientToken stores a new MCP client token of a workspace by its SHA-256, in one transaction with
// everything it opens. The token belongs to the workspace's MCP clients principal, made the first time a
// workspace issues one; a disabled one gives ErrDisabled and is never replaced. That principal is granted
// each of accounts it was never granted, so every live client token of the workspace gains an account the
// CRM newly holds, and a grant revoked for it stays revoked. When the token cannot be stored, no account or
// grant is added either. The token carries these scopes and expires after lifetime by the database's clock.
func (s *Store) IssueClientToken(ctx context.Context, w Workspace, accounts []ClientAccount, tokenSHA256 []byte,
	scopes []string, lifetime time.Duration) (ClientToken, error) {
	if len(tokenSHA256) != 32 || lifetime <= 0 {
		return ClientToken{}, errors.New("a token needs its SHA-256 and a lifetime")
	}
	var issued ClientToken
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// One transaction at a time looks for the principal and makes it, so a workspace gets only one.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gtm mcp clients ' || $1, 0))`,
			w.ID); err != nil {
			return err
		}
		var principal string
		var disabled bool
		err := tx.QueryRow(ctx, `
			SELECT id, disabled_at IS NOT NULL FROM principals
			 WHERE workspace_id = $1 AND kind = 'machine' AND display_name = $2
			 ORDER BY created_at, id LIMIT 1`, w.ID, ClientsName).Scan(&principal, &disabled)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `
				INSERT INTO principals (workspace_id, workspace_kind, kind, display_name)
				VALUES ($1, $2, 'machine', $3) RETURNING id`, w.ID, w.Kind, ClientsName).Scan(&principal)
		case err == nil && disabled:
			return ErrDisabled
		}
		if err != nil {
			return err
		}
		for _, account := range accounts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO accounts (workspace_id, ref, display_name) VALUES ($1, $2, $3)
				ON CONFLICT (workspace_id, ref) DO NOTHING`, w.ID, account.Ref, account.Name); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO account_grants (workspace_id, principal_id, account_ref) VALUES ($1, $2, $3)
				ON CONFLICT (workspace_id, principal_id, account_ref) DO NOTHING`, w.ID, principal, account.Ref); err != nil {
				return err
			}
		}
		issued, err = scanClientToken(tx.QueryRow(ctx, `
			INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, expires_at)
			VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
			RETURNING `+clientTokenColumns, w.ID, principal, tokenSHA256, scopes, lifetime.Seconds()))
		return err
	})
	return issued, err
}

// TokenWorkspace is the workspace a token with this SHA-256 was issued in, usable or not, or ErrNotFound. It
// tells a server which workspace's gate to ask; that gate verifies the token in full.
func (s *Store) TokenWorkspace(ctx context.Context, tokenSHA256 []byte) (Workspace, error) {
	if len(tokenSHA256) != 32 {
		return Workspace{}, ErrNotFound
	}
	var w Workspace
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, w.slug, w.kind, w.display_name FROM machine_tokens t JOIN workspaces w ON w.id = t.workspace_id
		 WHERE t.token_sha256 = $1`, tokenSHA256).Scan(&w.ID, &w.Slug, &w.Kind, &w.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return w, err
}

// HeldAccounts are the accounts a workspace holds, by ref: those a command has listed in it.
func (s *Store) HeldAccounts(ctx context.Context, workspaceID string) ([]ClientAccount, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT ref, display_name FROM accounts WHERE workspace_id = $1 ORDER BY ref`,
		workspaceID)
	if err != nil {
		return nil, err
	}
	var held []ClientAccount
	var account ClientAccount
	_, err = pgx.ForEachRow(rows, []any{&account.Ref, &account.Name}, func() error {
		held = append(held, account)
		return nil
	})
	return held, err
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
