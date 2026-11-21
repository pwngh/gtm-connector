package postgres

import (
	"context"
	"io"
	"time"
)

// SetCrash makes the store fail at a named step inside a transaction, as if
// the process stopped there: the transaction is rolled back.
func SetCrash(s *Store, crash func(point string) error) { s.crash = crash }

// SetPublishTimeouts shortens how long a publication's transaction waits on
// a lock and runs one statement.
func SetPublishTimeouts(s *Store, lock, statement time.Duration) {
	s.publishLock, s.publishStatement = lock, statement
}

// MigrateTo migrates to exactly this schema version, as an older binary did.
func MigrateTo(s *Store, ctx context.Context, version int) error { return s.migrateTo(ctx, version) }

// MigrationVersion is the number of the embedded migration with this name, or 0.
func MigrationVersion(name string) int { return migrationVersion(name) }

// MigrationSQL gives the text of each embedded migration, in order.
func MigrationSQL() []string {
	list, _ := migrations()
	var texts []string
	for _, m := range list {
		texts = append(texts, m.sql)
	}
	return texts
}

// MigrationLock is the advisory lock a migration and a restore hold.
const MigrationLock int64 = migrationLock

// DumpAt dumps a database at an older schema version, as an older binary did.
func DumpAt(s *Store, ctx context.Context, version int, open func(table string) (io.WriteCloser, error)) ([]TableCopy, error) {
	return s.dump(ctx, version, open)
}

// NotBackedUp gives the tables a backup leaves out, with the reasons.
func NotBackedUp() map[string]string { return notBackedUp }
