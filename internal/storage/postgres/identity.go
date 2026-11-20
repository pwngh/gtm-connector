package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound means no row matched.
var ErrNotFound = errors.New("not found")

// ErrCredentialMismatch means a demo principal already exists with a
// different credential: the credentials directory does not belong to this
// database.
var ErrCredentialMismatch = errors.New("the demo credential in the credentials directory does not match this database")

// Workspace is one tenant. Kind is "demo" or "live".
type Workspace struct {
	ID, Slug, Kind, DisplayName string
}

// Principal is a person or a machine in one workspace.
type Principal struct {
	ID, WorkspaceID, WorkspaceKind, Kind, DisplayName string
	Login                                             string
}

// EnsureWorkspace returns the workspace with this slug, creating it if absent.
// An existing workspace of another kind is refused.
func (s *Store) EnsureWorkspace(ctx context.Context, slug, kind, displayName string) (Workspace, error) {
	var w Workspace
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workspaces (slug, kind, display_name) VALUES ($1, $2, $3)
		ON CONFLICT (slug) DO UPDATE SET slug = excluded.slug
		RETURNING id, slug, kind, display_name`, slug, kind, displayName).
		Scan(&w.ID, &w.Slug, &w.Kind, &w.DisplayName)
	if err != nil {
		return Workspace{}, err
	}
	if w.Kind != kind {
		return Workspace{}, fmt.Errorf("workspace %s is %s, not %s", slug, w.Kind, kind)
	}
	return w, nil
}

// CreatePrincipal adds a principal without a sign-in credential.
func (s *Store) CreatePrincipal(ctx context.Context, w Workspace, kind, displayName string) (Principal, error) {
	p := Principal{WorkspaceID: w.ID, WorkspaceKind: w.Kind, Kind: kind, DisplayName: displayName}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO principals (workspace_id, workspace_kind, kind, display_name)
		VALUES ($1, $2, $3, $4) RETURNING id`, w.ID, w.Kind, kind, displayName).Scan(&p.ID)
	return p, err
}

// EnsureDemoPrincipal returns the demo person signing in as login, creating
// it with this credential digest if absent. An existing one with another
// digest gives ErrCredentialMismatch.
func (s *Store) EnsureDemoPrincipal(ctx context.Context, w Workspace, login, displayName string, credentialSHA256 []byte) (Principal, error) {
	p := Principal{WorkspaceKind: w.Kind, Kind: "person", Login: login}
	var stored []byte
	err := s.pool.QueryRow(ctx, `
		INSERT INTO principals (workspace_id, workspace_kind, kind, display_name, login, credential_sha256)
		VALUES ($1, $2, 'person', $3, $4, $5)
		ON CONFLICT (login) DO UPDATE SET login = excluded.login
		RETURNING id, workspace_id, display_name, credential_sha256`,
		w.ID, w.Kind, displayName, login, credentialSHA256).
		Scan(&p.ID, &p.WorkspaceID, &p.DisplayName, &stored)
	if err != nil {
		return Principal{}, err
	}
	if p.WorkspaceID != w.ID || subtle.ConstantTimeCompare(stored, credentialSHA256) != 1 {
		return Principal{}, ErrCredentialMismatch
	}
	return p, nil
}

// ErrDisabled means the principal exists and is disabled.
var ErrDisabled = errors.New("the principal is disabled")

// DemoPrincipal gives the demo principal for login, enabled or not: a
// disabled one with ErrDisabled, a missing one with ErrNotFound.
func (s *Store) DemoPrincipal(ctx context.Context, login string) (Principal, error) {
	p := Principal{Login: login}
	var disabled bool
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, workspace_kind, kind, display_name, disabled_at IS NOT NULL
		  FROM principals WHERE login = $1 AND workspace_kind = 'demo'`, login).
		Scan(&p.ID, &p.WorkspaceID, &p.WorkspaceKind, &p.Kind, &p.DisplayName, &disabled)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Principal{}, ErrNotFound
	case err == nil && disabled:
		return p, ErrDisabled
	}
	return p, err
}

// DemoPrincipalByLogin gives the enabled demo principal for login and its
// credential digest, or ErrNotFound.
func (s *Store) DemoPrincipalByLogin(ctx context.Context, login string) (Principal, []byte, error) {
	p := Principal{Login: login}
	var digest []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, workspace_kind, kind, display_name, credential_sha256
		  FROM principals
		 WHERE login = $1 AND disabled_at IS NULL AND workspace_kind = 'demo'`, login).
		Scan(&p.ID, &p.WorkspaceID, &p.WorkspaceKind, &p.Kind, &p.DisplayName, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, nil, ErrNotFound
	}
	return p, digest, err
}

// EnsureAccount records an account of a workspace.
func (s *Store) EnsureAccount(ctx context.Context, workspaceID, ref, displayName string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO accounts (workspace_id, ref, display_name) VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, ref) DO NOTHING`, workspaceID, ref, displayName)
	return err
}

// GrantAccount gives a principal read access to an account, or renews a
// revoked grant. A live grant keeps the time it was granted.
func (s *Store) GrantAccount(ctx context.Context, workspaceID, principalID, accountRef string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO account_grants (workspace_id, principal_id, account_ref) VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, principal_id, account_ref)
		DO UPDATE SET revoked_at = NULL, granted_at = now() WHERE account_grants.revoked_at IS NOT NULL`,
		workspaceID, principalID, accountRef)
	return err
}

// EnsureAccountGrant gives a principal read access to an account it was
// never granted, and leaves a grant that exists as it is: a revoked grant
// stays revoked. It reports whether the grant is live.
func (s *Store) EnsureAccountGrant(ctx context.Context, workspaceID, principalID, accountRef string) (bool, error) {
	var live bool
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO account_grants (workspace_id, principal_id, account_ref) VALUES ($1, $2, $3)
			ON CONFLICT (workspace_id, principal_id, account_ref) DO NOTHING`,
			workspaceID, principalID, accountRef); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT revoked_at IS NULL FROM account_grants
			WHERE workspace_id = $1 AND principal_id = $2 AND account_ref = $3`,
			workspaceID, principalID, accountRef).Scan(&live)
	})
	return live, err
}

// AccountsWithCards lists the accounts holding a card made here, sealed by
// this principal, as of this day.
func (s *Store) AccountsWithCards(ctx context.Context, workspaceID, principalID, asOf string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT c.account_ref FROM cards c JOIN receipts r ON r.id = c.seal_receipt_id
		 WHERE c.workspace_id = $1 AND c.kind = 'go' AND r.principal_id = $2
		   AND convert_from(c.card_bytes, 'UTF8')::jsonb ->> 'as_of' = $3`, workspaceID, principalID, asOf)
	if err != nil {
		return nil, err
	}
	accounts := map[string]bool{}
	var account string
	_, err = pgx.ForEachRow(rows, []any{&account}, func() error { accounts[account] = true; return nil })
	return accounts, err
}

// RevokeAccountGrant ends a principal's grant on an account.
func (s *Store) RevokeAccountGrant(ctx context.Context, workspaceID, principalID, accountRef string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE account_grants SET revoked_at = now()
		 WHERE workspace_id = $1 AND principal_id = $2 AND account_ref = $3 AND revoked_at IS NULL`,
		workspaceID, principalID, accountRef)
	return err
}

// HasAccountGrant reports whether the principal holds a live grant on the
// account, in this workspace.
func (s *Store) HasAccountGrant(ctx context.Context, workspaceID, principalID, accountRef string) (bool, error) {
	var granted bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM account_grants g
			  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
			 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
			   AND g.revoked_at IS NULL AND p.disabled_at IS NULL)`,
		workspaceID, principalID, accountRef).Scan(&granted)
	return granted, err
}

// Session is a signed-in browser session.
type Session struct {
	IDSHA256  []byte
	Principal *Principal
	ExpiresAt time.Time
}

// ErrSessionUsed means the session or pre-session a sign-in presented was
// already used, revoked or expired when the rotation ran.
var ErrSessionUsed = errors.New("the session was already used, revoked or expired")

// CreateSession stores a signed-in session that lasts ttl from now, by the
// database's clock.
func (s *Store) CreateSession(ctx context.Context, idSHA256 []byte, principal Principal, ttl time.Duration) (time.Time, error) {
	return createSession(ctx, s.pool, idSHA256, principal, ttl)
}

type execQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func createSession(ctx context.Context, q execQuerier, idSHA256 []byte, principal Principal, ttl time.Duration) (time.Time, error) {
	var expires time.Time
	err := q.QueryRow(ctx, `
		INSERT INTO sessions (id_sha256, workspace_id, principal_id, principal_kind, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
		RETURNING expires_at`,
		idSHA256, principal.WorkspaceID, principal.ID, principal.Kind, ttl.Seconds()).Scan(&expires)
	return expires, err
}

// LiveSession gives the session with this ID digest if it has neither
// expired nor been revoked, and its principal is enabled; else ErrNotFound.
func (s *Store) LiveSession(ctx context.Context, idSHA256 []byte) (Session, error) {
	session := Session{IDSHA256: idSHA256, Principal: &Principal{}}
	var login *string
	p := session.Principal
	err := s.pool.QueryRow(ctx, `
		SELECT s.expires_at, p.id, p.workspace_id, p.workspace_kind, p.kind, p.display_name, p.login
		  FROM sessions s
		  JOIN principals p ON p.workspace_id = s.workspace_id AND p.id = s.principal_id
		 WHERE s.id_sha256 = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
		   AND p.disabled_at IS NULL`, idSHA256).
		Scan(&session.ExpiresAt, &p.ID, &p.WorkspaceID, &p.WorkspaceKind, &p.Kind, &p.DisplayName, &login)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	p.Login = deref(login)
	return session, nil
}

// Admission is what a sign-in presented: a signed pre-session (its digest
// and expiry) or a live session.
type Admission struct {
	PreSession bool
	IDSHA256   []byte
	ExpiresAt  time.Time // pre-sessions only
}

// SignIn consumes exactly one live admission and stores a new session for
// the principal, in one transaction, so the session ID (and the CSRF token
// derived from it) rotate. A pre-session signs in once; a session must still
// be live. Otherwise it gives ErrSessionUsed and stores nothing.
func (s *Store) SignIn(ctx context.Context, admitted Admission, idSHA256 []byte, principal Principal, ttl time.Duration) (time.Time, error) {
	var expires time.Time
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		var err error
		if admitted.PreSession {
			tag, err = tx.Exec(ctx, `
				INSERT INTO consumed_presessions (id_sha256, expires_at)
				SELECT $1, $2 WHERE $2 > now()
				ON CONFLICT (id_sha256) DO NOTHING`, admitted.IDSHA256, admitted.ExpiresAt)
		} else {
			tag, err = tx.Exec(ctx, `
				UPDATE sessions SET revoked_at = now()
				 WHERE id_sha256 = $1 AND revoked_at IS NULL AND expires_at > now()`, admitted.IDSHA256)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSessionUsed
		}
		if err := s.at("signin:consumed"); err != nil {
			return err
		}
		expires, err = createSession(ctx, tx, idSHA256, principal, ttl)
		return err
	})
	return expires, err
}

// RevokeSession ends one session. Revoking an ended session is not an error.
func (s *Store) RevokeSession(ctx context.Context, idSHA256 []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now() WHERE id_sha256 = $1 AND revoked_at IS NULL`, idSHA256)
	return err
}

// RevokePrincipalSessions ends every live session of a principal.
func (s *Store) RevokePrincipalSessions(ctx context.Context, workspaceID, principalID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		 WHERE workspace_id = $1 AND principal_id = $2 AND revoked_at IS NULL`, workspaceID, principalID)
	return tag.RowsAffected(), err
}

// DeleteEndedSessions removes sessions that expired or were revoked more
// than a day ago.
func (s *Store) DeleteEndedSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions
		 WHERE expires_at < now() - interval '1 day' OR revoked_at < now() - interval '1 day'`)
	if err != nil {
		return 0, err
	}
	consumed, err := s.pool.Exec(ctx, `DELETE FROM consumed_presessions WHERE expires_at < now() - interval '1 day'`)
	return tag.RowsAffected() + consumed.RowsAffected(), err
}

// Connection is what one source of a workspace is connected to.
type Connection struct {
	ID, WorkspaceID, Source, Mode string
	Generation                    int64
}

// EnsureConnection returns the workspace's connection for source, creating
// it in mode if absent.
func (s *Store) EnsureConnection(ctx context.Context, w Workspace, source, mode string) (Connection, error) {
	c := Connection{WorkspaceID: w.ID, Source: source}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO connections (workspace_id, workspace_kind, source, mode) VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id, source) DO UPDATE SET source = excluded.source
		RETURNING id, mode, generation`, w.ID, w.Kind, source, mode).Scan(&c.ID, &c.Mode, &c.Generation)
	return c, err
}

// BumpConnectionGeneration records that a source changed. Reads started
// under the old generation can no longer finish ok.
func (s *Store) BumpConnectionGeneration(ctx context.Context, connectionID string) (int64, error) {
	var generation int64
	err := s.pool.QueryRow(ctx, `
		UPDATE connections SET generation = generation + 1, updated_at = now()
		 WHERE id = $1 RETURNING generation`, connectionID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return generation, err
}
