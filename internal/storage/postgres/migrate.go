package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is the advisory lock key that serializes migrations, so two
// processes starting at once do not apply the same file twice.
const migrationLock = 0x67746d31 // "gtm1"

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version integer PRIMARY KEY,
	name text NOT NULL,
	sha256 text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now())`

type migration struct {
	version int
	name    string
	sql     string
	sha256  string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var list []migration
	for _, entry := range entries {
		name := entry.Name()
		number, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(number)
		if !ok || err != nil || version < 1 || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("migration file %q is not NNNN_name.sql", name)
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(body)
		list = append(list, migration{version, name, string(body), hex.EncodeToString(digest[:])})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })
	for i, m := range list {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %d is missing", i+1)
		}
	}
	return list, nil
}

// migrationVersion is the number of the embedded migration with this name
// (NNNN_name.sql), or 0, so code that names a migration does not name its number.
func migrationVersion(name string) int {
	list, _ := migrations()
	for _, m := range list {
		if strings.HasSuffix(m.name, "_"+name+".sql") {
			return m.version
		}
	}
	return 0
}

// LatestSchemaVersion is the version the embedded migrations reach.
func LatestSchemaVersion() int {
	list, err := migrations()
	if err != nil || len(list) == 0 {
		return 0
	}
	return list[len(list)-1].version
}

// Migrate applies every pending migration, each in its own transaction. It
// refuses a database whose applied migrations differ from the embedded files
// (changed), and one that is newer than this binary (ahead), in the lines
// MigrationState.Err gives.
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrateTo(ctx, LatestSchemaVersion())
}

// SchemaVersion is the highest applied migration, or 0 for an empty database.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		"SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var version int
	err := s.pool.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

// MigrationState is how a database's applied migrations stand against the
// ones this binary embeds.
type MigrationState string

const (
	// MigrationsCurrent: the same versions, each with the same digest.
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
// version and by the SHA-256 of each file's contents, which schema_migrations
// records as a migration is applied. Changed is reported before ahead, and
// ahead before behind. It is one read of that table outside any transaction,
// so a migration or a restore never waits for it, and it waits for neither:
// they hold no lock that stops a reader.
func (s *Store) MigrationState(ctx context.Context) (MigrationState, error) {
	list, err := migrations()
	if err != nil {
		return "", err
	}
	rows, _ := s.pool.Query(ctx, "SELECT version, sha256 FROM schema_migrations")
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var version int
		var digest string
		if err := rows.Scan(&version, &digest); err != nil {
			return "", err
		}
		applied[version] = digest
	}
	if err := rows.Err(); sqlState(err) == undefinedTable {
		return MigrationsBehind, nil
	} else if err != nil {
		return "", err
	}
	return standing(list, applied), nil
}

// standing is the state of these applied migrations, each version with its
// recorded digest, against the embedded list.
func standing(list []migration, applied map[int]string) MigrationState {
	same, ahead, changed := 0, false, false
	for version, digest := range applied {
		switch {
		case version < 1 || version > len(list):
			ahead = true
		case list[version-1].sha256 != digest:
			changed = true
		default:
			same++
		}
	}
	switch {
	case changed:
		return MigrationsChanged
	case ahead:
		return MigrationsAhead
	case same < len(list):
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

func (s *Store) migrateTo(ctx context.Context, target int) error {
	list, err := migrations()
	if err != nil {
		return err
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
	applied := map[int]string{}
	rows, err := conn.Query(ctx, "SELECT version, sha256 FROM schema_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		var digest string
		if err := rows.Scan(&version, &digest); err != nil {
			return err
		}
		applied[version] = digest
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if state := standing(list, applied); state == MigrationsAhead || state == MigrationsChanged {
		return state.Err()
	}
	for _, m := range list {
		if m.version > target {
			break
		}
		if _, done := applied[m.version]; done {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
			_, err := tx.Exec(ctx,
				"INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)",
				m.version, m.name, m.sha256)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
