package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The people who sign in through their company's identity provider, and the grants the operator gives them.

// PersonIdentity is who a sign-in through an identity provider says signed in: the issuer and the subject
// that find the person, and the email, whether the provider verified it, and the name it gave this time.
type PersonIdentity struct {
	Issuer, Subject string
	Email           string // "" when the ID token holds none
	EmailVerified   bool
	DisplayName     string
}

// SignInPerson signs a person in, in one transaction: it consumes the sign-in's admission once, finds the
// person of the workspace with this issuer and subject or makes one with no grant, records the email and
// the name given now, ends the session the browser held before, when there is one, and stores a new
// session that lasts ttl by the database's clock. An admission that is no pre-session, or was used before
// or has expired, gives ErrSessionUsed, and a disabled person ErrDisabled; either stores nothing.
func (s *Store) SignInPerson(ctx context.Context, admitted Admission, idSHA256 []byte, w Workspace,
	who PersonIdentity, ttl time.Duration, previous []byte) (Principal, time.Time, error) {
	if !admitted.PreSession {
		return Principal{}, time.Time{}, ErrSessionUsed
	}
	p := Principal{WorkspaceID: w.ID, WorkspaceKind: w.Kind, Kind: "person", DisplayName: who.DisplayName,
		Issuer: who.Issuer, Subject: who.Subject, Email: who.Email, EmailVerified: who.EmailVerified}
	var expires time.Time
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO consumed_presessions (id_sha256, expires_at)
			SELECT $1, $2 WHERE $2 > now()
			ON CONFLICT (id_sha256) DO NOTHING`, admitted.IDSHA256, admitted.ExpiresAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSessionUsed
		}
		var disabled bool
		if err := tx.QueryRow(ctx, `
			INSERT INTO principals (workspace_id, workspace_kind, kind, display_name, oidc_issuer, oidc_subject,
				email, email_verified)
			VALUES ($1, $2, 'person', $3, $4, $5, $6, $7)
			ON CONFLICT (workspace_id, oidc_issuer, oidc_subject) DO UPDATE
			   SET display_name = excluded.display_name, email = excluded.email,
			       email_verified = excluded.email_verified
			RETURNING id, disabled_at IS NOT NULL`,
			w.ID, w.Kind, who.DisplayName, who.Issuer, who.Subject, nullable(who.Email), who.EmailVerified).
			Scan(&p.ID, &disabled); err != nil {
			return err
		}
		if disabled {
			return ErrDisabled
		}
		if previous != nil {
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id_sha256 = $1 AND revoked_at IS NULL`,
				previous); err != nil {
				return err
			}
		}
		expires, err = createSession(ctx, tx, idSHA256, p, ttl)
		return err
	})
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	return p, expires, nil
}

// PreSessionUsed reports whether the pre-session with this digest has signed in.
func (s *Store) PreSessionUsed(ctx context.Context, idSHA256 []byte) (bool, error) {
	var used bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM consumed_presessions WHERE id_sha256 = $1)`,
		idSHA256).Scan(&used)
	return used, err
}

// Person is a person of a workspace who signs in through an identity provider, with every grant they hold
// or held.
type Person struct {
	Principal
	Disabled  bool
	CreatedAt time.Time
	Grants    []Grant
}

// Grant is one account grant: live while RevokedAt is nil.
type Grant struct {
	AccountRef, AccountName string
	GrantedAt               time.Time
	RevokedAt               *time.Time
}

// People lists the people of a workspace who sign in through an identity provider, in the order they
// first signed in, each with their grants by account.
func (s *Store) People(ctx context.Context, workspaceID string) ([]Person, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, workspace_kind, display_name, oidc_issuer, oidc_subject, coalesce(email, ''),
		       email_verified, disabled_at IS NOT NULL, created_at
		  FROM principals WHERE workspace_id = $1 AND oidc_subject IS NOT NULL
		 ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	people := []Person{}
	at := map[string]int{}
	var p Person
	_, err = pgx.ForEachRow(rows, []any{&p.ID, &p.WorkspaceID, &p.WorkspaceKind, &p.DisplayName, &p.Issuer,
		&p.Subject, &p.Email, &p.EmailVerified, &p.Disabled, &p.CreatedAt}, func() error {
		p.Kind = "person"
		at[p.ID] = len(people)
		people = append(people, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	grants, err := s.pool.Query(ctx, `
		SELECT g.principal_id, g.account_ref, a.display_name, g.granted_at, g.revoked_at
		  FROM account_grants g
		  JOIN accounts a ON a.workspace_id = g.workspace_id AND a.ref = g.account_ref
		  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
		 WHERE g.workspace_id = $1 AND p.oidc_subject IS NOT NULL
		 ORDER BY g.account_ref`, workspaceID)
	if err != nil {
		return nil, err
	}
	var principal string
	var g Grant
	_, err = pgx.ForEachRow(grants, []any{&principal, &g.AccountRef, &g.AccountName, &g.GrantedAt, &g.RevokedAt},
		func() error {
			if i, ok := at[principal]; ok {
				people[i].Grants = append(people[i].Grants, g)
			}
			return nil
		})
	return people, err
}

// AccountName is the display name of an account the workspace holds, or ErrNotFound.
func (s *Store) AccountName(ctx context.Context, workspaceID, accountRef string) (string, error) {
	if !validUUID(workspaceID) {
		return "", ErrNotFound
	}
	var name string
	err := s.pool.QueryRow(ctx, `SELECT display_name FROM accounts WHERE workspace_id = $1 AND ref = $2`,
		workspaceID, accountRef).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// GrantRow is the reference a grant change's receipt names: grants/PRINCIPAL/ACCOUNT.
func GrantRow(principalID, accountRef string) string {
	return "grants/" + principalID + "/" + accountRef
}

// ChangeGrant gives a principal a live grant on an account, or with revoke ends its live grant, under a
// receipt the caller started at began and holds the lease of, and finishes that receipt ok, naming
// GrantRow, in the same transaction. So a grant changes only with its receipt, and a receipt says ok only
// of a change that committed. It reports whether the grant changed: granting a live grant, or revoking one
// that is not live, changes nothing and is ok all the same.
func (s *Store) ChangeGrant(ctx context.Context, started Receipt, began time.Time, workspaceID, principalID,
	accountRef string, revoke bool) (bool, Receipt, error) {
	if !validUUID(workspaceID) || !validUUID(principalID) || !validUUID(started.ID) {
		return false, Receipt{}, ErrNotFound
	}
	var changed bool
	var finished Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var tag pgconn.CommandTag
		var err error
		if revoke {
			tag, err = tx.Exec(ctx, `
				UPDATE account_grants SET revoked_at = now()
				 WHERE workspace_id = $1 AND principal_id = $2 AND account_ref = $3 AND revoked_at IS NULL`,
				workspaceID, principalID, accountRef)
		} else {
			tag, err = tx.Exec(ctx, `
				INSERT INTO account_grants (workspace_id, principal_id, account_ref) VALUES ($1, $2, $3)
				ON CONFLICT (workspace_id, principal_id, account_ref)
				DO UPDATE SET revoked_at = NULL, granted_at = now() WHERE account_grants.revoked_at IS NOT NULL`,
				workspaceID, principalID, accountRef)
		}
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		finished, err = finishLocked(ctx, tx, ReceiptFinish{ReceiptID: started.ID, LeaseToken: started.LeaseToken,
			Status: "ok", HTTPStatus: 200, Rows: []string{GrantRow(principalID, accountRef)},
			ElapsedMS: float64(time.Since(began).Microseconds()) / 1000}, false)
		return err
	})
	if errors.Is(err, errNotPending) {
		return false, Receipt{}, ErrLeaseLost
	}
	return changed, finished, err
}
