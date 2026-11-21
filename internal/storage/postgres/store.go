// Package postgres is the connector's only durable store: identity, receipts,
// record versions, cards, units and gaps, with the migrations that create them.
//
// Queries are hand-written for pgx and live in this package only. Each one is
// covered by a test against a real PostgreSQL (GTM_TEST_DATABASE_URL).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds a connection pool. Its methods are safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
	// crash, when set by a test, is called at named points inside a
	// transaction; an error it returns aborts that transaction there.
	crash func(point string) error
	// publishLock and publishStatement, when a test sets them, stand in for
	// publishLockTimeout and publishStatementTimeout (bound).
	publishLock, publishStatement time.Duration
}

// bound sets how long tx waits on a lock and runs one statement:
// publishLockTimeout and publishStatementTimeout, or what a test set. The
// settings end with tx. A publication runs under them, and so does each
// transaction that starts or finishes a receipt.
func (s *Store) bound(ctx context.Context, tx pgx.Tx) error {
	lock, statement := publishLockTimeout, publishStatementTimeout
	if s.publishLock > 0 {
		lock = s.publishLock
	}
	if s.publishStatement > 0 {
		statement = s.publishStatement
	}
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
		strconv.FormatInt(lock.Milliseconds(), 10), strconv.FormatInt(statement.Milliseconds(), 10))
	return err
}

// Open connects to the database at url and checks that it answers. It does
// not migrate; call Migrate.
func Open(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database URL: %w", redact(err))
	}
	config.MaxConns = 16
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	// Every session reads and writes times in UTC, so text output (backups,
	// receipts) does not depend on the server's time zone.
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	config.ConnConfig.RuntimeParams["application_name"] = "gtm-connector"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("database: %w", redact(err))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", redact(err))
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection.
func (s *Store) Close() { s.pool.Close() }

func (s *Store) at(point string) error {
	if s.crash == nil {
		return nil
	}
	return s.crash(point)
}

// inTx runs fn in one transaction and commits it, or rolls back on error.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ErrDatabaseURL is a database URL that could not be parsed.
var ErrDatabaseURL = errors.New("the database URL could not be parsed")

// redact drops a connection string's password from a parse error.
func redact(err error) error {
	var parse *pgconn.ParseConfigError
	if errors.As(err, &parse) {
		return ErrDatabaseURL
	}
	return err
}

// sqlState gives the PostgreSQL error code of err, or "".
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
