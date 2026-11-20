package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// The server connects as an app role that reads and writes rows and nothing
// else: it owns no table, so it cannot disable a trigger, truncate, or change
// the schema. The owner role runs setup, migrations, backup and restore.

var roleName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// readOnlyTables are the tables the app role may only read.
var readOnlyTables = []string{"schema_migrations", "restores"}

// deletableTables are the only tables the app role may delete from.
var deletableTables = []string{"sessions", "consumed_presessions"}

// EnsureAppRole creates the app role (or resets its password) and grants it
// exactly row access. Run it as the owner, after Migrate.
func (s *Store) EnsureAppRole(ctx context.Context, name, password string) error {
	if !roleName.MatchString(name) {
		return fmt.Errorf("app role %q is not a plain lower-case name", name)
	}
	if password == "" {
		return errors.New("the app role needs a password")
	}
	var owner string
	if err := s.pool.QueryRow(ctx, "SELECT current_user").Scan(&owner); err != nil {
		return err
	}
	if owner == name {
		return errors.New("the app role must not be the owner role")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", name).
		Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	var statement string
	if err := s.pool.QueryRow(ctx, `SELECT format('%s ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
		NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %L', $1::text, $2::text, $3::text)`, verb, name, password).
		Scan(&statement); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("app role: %w", redactRole(err))
	}
	grants := `DO $grants$ BEGIN
		EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), '` + name + `');
		EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', current_schema(), '` + name + `');
		EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM %I', current_schema(), '` + name + `');
		EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO %I', current_schema(), '` + name + `');
	END $grants$`
	if _, err := s.pool.Exec(ctx, grants); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, table := range tables {
		privileges := "INSERT, UPDATE"
		if contains(readOnlyTables, table) {
			continue
		}
		if contains(deletableTables, table) {
			privileges += ", DELETE"
		}
		if _, err := s.pool.Exec(ctx, fmt.Sprintf("GRANT %s ON %s TO %s", privileges,
			pgx.Identifier{table}.Sanitize(), pgx.Identifier{name}.Sanitize())); err != nil {
			return err
		}
	}
	return nil
}

// CheckAppRole refuses a connection that could change the schema or switch
// a trigger off: a superuser, or a member of the role that owns the tables.
func (s *Store) CheckAppRole(ctx context.Context) error {
	var powerful bool
	err := s.pool.QueryRow(ctx, `
		SELECT r.rolsuper OR EXISTS (
			SELECT 1 FROM pg_tables t WHERE t.schemaname = current_schema()
			   AND pg_has_role(current_user, t.tableowner, 'MEMBER'))
		  FROM pg_roles r WHERE r.rolname = current_user`).Scan(&powerful)
	if err != nil {
		return err
	}
	if powerful {
		return errors.New("the server and demo connect as the app role, which owns no table; " +
			"GTM_DATABASE_URL names the owner or a superuser")
	}
	return nil
}

// installLock serializes setup and restore, so neither sees the other's
// half-written credentials or database.
const installLock = 0x67746d32 // "gtm2"

// LockInstall takes the install lock and returns its release.
func (s *Store) LockInstall(ctx context.Context) (func(), error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", installLock); err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", installLock)
		conn.Release()
	}, nil
}

func contains(list []string, item string) bool {
	for _, entry := range list {
		if entry == item {
			return true
		}
	}
	return false
}

// redactRole keeps the password out of an error about the role statement.
func redactRole(err error) error {
	if code := sqlState(err); code != "" {
		return fmt.Errorf("PostgreSQL error %s", code)
	}
	return errors.New("the role statement failed")
}
