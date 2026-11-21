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
	"net"
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

// A connection the network loses while its socket stays open is ended on
// both sides, so the locks of its transaction do not outlive it:
//
//   - idleInTransaction is how long the server lets a session wait inside a
//     transaction for its next statement before it ends the session and its
//     locks. The connector sends a transaction's statements one after the
//     other, so a session of its own that waits this long has lost its client.
//   - keepaliveIdle, keepaliveInterval and keepaliveCount are the TCP
//     keepalives both the connector and the server set on the connection: a
//     probe after keepaliveIdle of silence, then one every keepaliveInterval,
//     and the connection is dropped after keepaliveCount unanswered probes,
//     about a minute after the peer went quiet.
const (
	idleInTransaction = 30 * time.Second
	keepaliveIdle     = 30 * time.Second
	keepaliveInterval = 10 * time.Second
	keepaliveCount    = 3
	connectTimeout    = 5 * time.Second
)

// Open connects to the database at url and checks that it answers. It does
// not migrate; call Migrate.
func Open(ctx context.Context, url string) (*Store, error) {
	return open(ctx, url, idleInTransaction)
}

// open is Open with the server's limit on a session idle in a transaction.
func open(ctx context.Context, url string, idle time.Duration) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database URL: %w", redact(err))
	}
	config.MaxConns = 16
	config.ConnConfig.ConnectTimeout = connectTimeout
	config.ConnConfig.DialFunc = (&net.Dialer{Timeout: connectTimeout, KeepAliveConfig: net.KeepAliveConfig{
		Enable: true, Idle: keepaliveIdle, Interval: keepaliveInterval, Count: keepaliveCount}}).DialContext
	seconds := func(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
	config.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(idle.Milliseconds(), 10)
	config.ConnConfig.RuntimeParams["tcp_keepalives_idle"] = seconds(keepaliveIdle)
	config.ConnConfig.RuntimeParams["tcp_keepalives_interval"] = seconds(keepaliveInterval)
	config.ConnConfig.RuntimeParams["tcp_keepalives_count"] = strconv.Itoa(keepaliveCount)
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

// recoverWait is how long a rollback may take, and the read that settles a
// commit whose answer was lost. Either is one short exchange with a working
// server; past it the connection is taken to be lost.
const recoverWait = 3 * time.Second

// errCommitUnknown is a commit that was sent but got no answer: it may or
// may not have committed. A caller reads back what it wrote to learn which.
var errCommitUnknown = errors.New("the commit got no answer")

// recovering is the context of a rollback or of a read-back after a lost
// commit: the caller's cancel and deadline do not end it, and recoverWait
// does.
func recovering(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recoverWait)
}

// inTx runs fn in one transaction and commits it, or rolls back on error.
// The rollback waits at most recoverWait: one that fails or runs out of time
// closes its connection, which the pool then drops, so a connection lost
// with its socket still open does not hold the caller. A commit that got no
// answer is errCommitUnknown.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		ending, cancel := recovering(ctx)
		defer cancel()
		tx.Rollback(ending) // a no-op after a commit
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		var refused *pgconn.PgError
		if errors.As(err, &refused) || pgconn.SafeToRetry(err) {
			return err // the server refused it, or it was never sent
		}
		return fmt.Errorf("%w: %w", errCommitUnknown, err)
	}
	return nil
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
