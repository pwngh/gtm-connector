package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is the advisory lock key that serializes migrations, so two
// processes starting at once do not apply the same file twice.
const migrationLock = 0x67746d31 // "gtm1"

// A migration file is named PREFIX_words.sql, where PREFIX is the UTC minute
// it was written, as 20261121T1830 (`make migration NAME=words` writes one).
// The first thirteen files keep the numbers 0001 to 0013, which sort before
// every stamp. Migrations apply in name order, and every one that is not
// applied yet is applied, so two files written apart merge in either order.
var migrationFileName = regexp.MustCompile(`^([0-9]{4}|[0-9]{8}T[0-9]{4})_[a-z0-9_]+\.sql$`)

// lastNumbered is the last migration named by a number. No file is numbered
// after it.
const lastNumbered = 13

// schema_migrations has a row for each applied migration: its file name and
// the SHA-256 of the file's contents when it was applied.
const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	name text PRIMARY KEY,
	sha256 text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now())`

// keyByName converts the schema_migrations an older binary made, which keyed
// each row by its file's number beside the file's name: the number goes, and
// the name each row holds is the key. The digests stay as they were recorded.
const keyByName = `DO $convert$ BEGIN
	IF EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('schema_migrations')
	           AND attname = 'version' AND NOT attisdropped) THEN
		ALTER TABLE schema_migrations DROP COLUMN version, ADD PRIMARY KEY (name);
	END IF;
END $convert$`

type migration struct {
	name   string
	sql    string
	sha256 string
}

func newMigration(name, body string) migration {
	digest := sha256.Sum256([]byte(body))
	return migration{name, body, hex.EncodeToString(digest[:])}
}

// embeddedMigrations are the migration files built into this binary, in
// name order.
func embeddedMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var list []migration
	for _, entry := range entries {
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		list = append(list, newMigration(entry.Name(), string(body)))
	}
	return checkMigrations(list)
}

// checkMigrations sorts the list by name. It refuses a name that is not
// PREFIX_words.sql, a stamp that is not a time, a name given twice, and
// numbered files that are not exactly 0001 to 0013.
func checkMigrations(list []migration) ([]migration, error) {
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	numbered := 0
	for i, m := range list {
		match := migrationFileName.FindStringSubmatch(m.name)
		if match == nil {
			return nil, fmt.Errorf("migration file %q is not named YYYYMMDDTHHMM_words.sql", m.name)
		}
		if i > 0 && list[i-1].name == m.name {
			return nil, fmt.Errorf("migration file %q is given twice", m.name)
		}
		if len(match[1]) == 4 {
			numbered++
			if n, _ := strconv.Atoi(match[1]); n != numbered || n > lastNumbered {
				return nil, fmt.Errorf("migration file %q is numbered; name a new migration by its UTC time, "+
					"as YYYYMMDDTHHMM_words.sql", m.name)
			}
		} else if _, err := time.Parse("20060102T1504", match[1]); err != nil {
			return nil, fmt.Errorf("migration file %q is not stamped with a UTC time", m.name)
		}
	}
	if numbered != lastNumbered {
		return nil, fmt.Errorf("migration %04d is missing", numbered+1)
	}
	return list, nil
}

// migrations are the migrations this store applies: the embedded ones, and
// any a test added.
func (s *Store) migrations() ([]migration, error) {
	list, err := embeddedMigrations()
	if err != nil || len(s.extraMigrations) == 0 {
		return list, err
	}
	return checkMigrations(append(list, s.extraMigrations...))
}

// MigrationNames are the file names of the embedded migrations, in the order
// they apply.
func MigrationNames() []string {
	list, _ := embeddedMigrations()
	names := make([]string, len(list))
	for i, m := range list {
		names[i] = m.name
	}
	return names
}

// NumberedMigrations are the names of migrations 0001 to n: what a database
// an older binary migrated to schema n holds.
func NumberedMigrations(n int) ([]string, error) {
	if n < 1 || n > lastNumbered {
		return nil, fmt.Errorf("the backup is at schema %d; this binary knows 1 to %d", n, lastNumbered)
	}
	return MigrationNames()[:n], nil
}

// migrationFile is the name of the embedded migration PREFIX_words.sql, or "",
// so code that names a migration does not name its prefix.
func migrationFile(words string) string {
	for _, name := range MigrationNames() {
		if _, rest, _ := strings.Cut(name, "_"); rest == words+".sql" {
			return name
		}
	}
	return ""
}

// Migrate applies every embedded migration that is not applied yet, in name
// order, each in its own transaction. It refuses a database whose applied
// migrations differ from the embedded files (changed), and one that holds a
// migration this binary does not (ahead), in the lines MigrationState.Err
// gives.
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrateFirst(ctx, -1)
}

// AppliedMigrations are the names of the applied migrations, in name order,
// and none for an empty database.
func (s *Store) AppliedMigrations(ctx context.Context) ([]string, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		"SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return nil, err
	}
	rows, _ := s.pool.Query(ctx, "SELECT name FROM schema_migrations")
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	slices.Sort(names)
	return names, err
}

// MigrationState is how a database's applied migrations stand against the
// ones this binary embeds.
type MigrationState string

const (
	// MigrationsCurrent: the same migrations, each with the same digest.
	MigrationsCurrent MigrationState = "current"
	// MigrationsBehind: an embedded migration is not applied. A database
	// without schema_migrations is behind.
	MigrationsBehind MigrationState = "behind"
	// MigrationsAhead: a migration is applied that this binary does not hold.
	MigrationsAhead MigrationState = "ahead"
	// MigrationsChanged: an applied migration's recorded digest is not the
	// digest of this binary's file.
	MigrationsChanged MigrationState = "changed"
)

// Err is why a server does not run on a database in this state, in one line,
// and nil for a current one. Setup refuses an ahead or a changed database
// with the same lines.
func (m MigrationState) Err() error {
	switch m {
	case MigrationsCurrent:
		return nil
	case MigrationsBehind:
		return errors.New("the database is behind this binary: a migration is not applied; run setup")
	case MigrationsAhead:
		return errors.New("the database is ahead of this binary: this binary is older than the database")
	}
	return errors.New("the database's migrations changed: an applied migration's contents differ from this binary's")
}

// MigrationState compares the applied migrations with the embedded ones by
// name and by the SHA-256 of each file's contents, which schema_migrations
// records as a migration is applied. Changed is reported before ahead, and
// ahead before behind. It is one read of that table outside any transaction,
// so a migration or a restore never waits for it, and it waits for neither:
// they hold no lock that stops a reader. It reads the table an older binary
// made as well, before setup converts it.
func (s *Store) MigrationState(ctx context.Context) (MigrationState, error) {
	list, err := s.migrations()
	if err != nil {
		return "", err
	}
	applied, err := appliedDigests(ctx, s.pool)
	if sqlState(err) == undefinedTable {
		return MigrationsBehind, nil
	} else if err != nil {
		return "", err
	}
	return standing(list, applied), nil
}

// appliedDigests reads each applied migration's name with its recorded digest.
func appliedDigests(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[string]string, error) {
	rows, _ := db.Query(ctx, "SELECT name, sha256 FROM schema_migrations")
	defer rows.Close()
	applied := map[string]string{}
	for rows.Next() {
		var name, digest string
		if err := rows.Scan(&name, &digest); err != nil {
			return nil, err
		}
		applied[name] = digest
	}
	return applied, rows.Err()
}

// standing is the state of these applied migrations, each name with its
// recorded digest, against the known list.
func standing(list []migration, applied map[string]string) MigrationState {
	known := map[string]string{}
	for _, m := range list {
		known[m.name] = m.sha256
	}
	ahead, changed := false, false
	for name, digest := range applied {
		want, ok := known[name]
		switch {
		case !ok:
			ahead = true
		case want != digest:
			changed = true
		}
	}
	switch {
	case changed:
		return MigrationsChanged
	case ahead:
		return MigrationsAhead
	case len(applied) < len(list):
		return MigrationsBehind
	}
	return MigrationsCurrent
}

// undefinedTable is the SQLSTATE of a read from a table that does not exist.
const undefinedTable = "42P01"

// requireUTF8 refuses a database that is not encoded as UTF-8, before any migration or restore. The checks
// name characters outside ASCII, and the records they compare may hold any character.
func requireUTF8(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var encoding string
	if err := db.QueryRow(ctx, "SELECT current_setting('server_encoding')").Scan(&encoding); err != nil {
		return err
	}
	if encoding != "UTF8" {
		return fmt.Errorf("the database is encoded as %s; create it with ENCODING 'UTF8' TEMPLATE template0", encoding)
	}
	return nil
}

// migrateFirst applies the first count migrations in name order that are
// not applied yet, or all of them for a count below zero.
func (s *Store) migrateFirst(ctx context.Context, count int) error {
	all, err := s.migrations()
	if err != nil {
		return err
	}
	list := all
	if count >= 0 {
		list = all[:count]
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if err := requireUTF8(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock)

	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		return err
	}
	applied, err := appliedDigests(ctx, conn)
	if err != nil {
		return err
	}
	if state := standing(all, applied); state == MigrationsAhead || state == MigrationsChanged {
		return state.Err()
	}
	if _, err := conn.Exec(ctx, keyByName); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	for _, m := range list {
		if _, done := applied[m.name]; done {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			return apply(ctx, tx, m)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and records it.
func apply(ctx context.Context, tx pgx.Tx, m migration) error {
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name, sha256) VALUES ($1, $2)", m.name, m.sha256)
	return err
}
