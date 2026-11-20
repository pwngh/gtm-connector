package postgres

import (
	"context"
	"io"
)

// SetCrash makes the store fail at a named step inside a transaction, as if
// the process stopped there: the transaction is rolled back.
func SetCrash(s *Store, crash func(point string) error) { s.crash = crash }

// MigrateTo migrates to exactly this schema version, as an older binary did.
func MigrateTo(s *Store, ctx context.Context, version int) error { return s.migrateTo(ctx, version) }

// DumpAt dumps a database at an older schema version, as an older binary did.
func DumpAt(s *Store, ctx context.Context, version int, open func(table string) (io.WriteCloser, error)) ([]TableCopy, error) {
	return s.dump(ctx, version, open)
}

// NotBackedUp gives the tables a backup leaves out, with the reasons.
func NotBackedUp() map[string]string { return notBackedUp }
