package postgres

import (
	"context"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenIdle opens a store whose sessions the server ends after idle inside a
// transaction, in place of idleInTransaction.
func OpenIdle(ctx context.Context, url string, idle time.Duration) (*Store, error) {
	return open(ctx, url, idle)
}

// Pool is the store's connection pool.
func Pool(s *Store) *pgxpool.Pool { return s.pool }

// SetCrash makes the store fail at a named step inside a transaction, as if
// the process stopped there: the transaction is rolled back.
func SetCrash(s *Store, crash func(point string) error) { s.crash = crash }

// SetPublishTimeouts shortens how long a publication's transaction, and each
// that starts or finishes a receipt, waits on a lock and runs one statement.
func SetPublishTimeouts(s *Store, lock, statement time.Duration) {
	s.publishLock, s.publishStatement = lock, statement
}

// MigrateTo applies the first n migrations in name order, as an older binary
// that held only those did.
func MigrateTo(s *Store, ctx context.Context, n int) error { return s.migrateFirst(ctx, n) }

// MigrationVersion is the place, from 1, of the embedded migration
// PREFIX_name.sql in name order, or 0.
func MigrationVersion(name string) int {
	return slices.Index(MigrationNames(), migrationFile(name)) + 1
}

// MigrationSQL gives the text of each embedded migration, in order.
func MigrationSQL() []string {
	list, _ := embeddedMigrations()
	var texts []string
	for _, m := range list {
		texts = append(texts, m.sql)
	}
	return texts
}

// MigrationDigest is the SHA-256 of a migration's text, as schema_migrations
// records it.
func MigrationDigest(sql string) string { return newMigration("x", sql).sha256 }

// CheckMigrationNames checks the embedded files with files of these names
// beside them, as the binary checks its files.
func CheckMigrationNames(names ...string) error {
	list, err := embeddedMigrations()
	if err != nil {
		return err
	}
	for _, name := range names {
		list = append(list, newMigration(name, ""))
	}
	_, err = checkMigrations(list)
	return err
}

// AddMigrations makes the store apply these files, name to text, beside the
// embedded ones, as a binary that held them would; nil takes them away.
func AddMigrations(s *Store, files map[string]string) {
	s.extraMigrations = nil
	for name, sql := range files {
		s.extraMigrations = append(s.extraMigrations, newMigration(name, sql))
	}
}

// MigrationLock is the advisory lock a migration and a restore hold.
const MigrationLock int64 = migrationLock

// DumpAt dumps a database with these migrations applied, as an older binary did.
func DumpAt(s *Store, ctx context.Context, applied []string, open func(table string) (io.WriteCloser, error)) ([]TableCopy, error) {
	return s.dump(ctx, applied, open)
}

// NotBackedUp gives the tables a backup leaves out, with the reasons.
func NotBackedUp() map[string]string { return notBackedUp }
