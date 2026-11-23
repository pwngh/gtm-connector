package httptransport_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// schema is noSessions with a set answer to the health check. It notes how
// long the check was given.
type schema struct {
	noSessions
	state postgres.MigrationState
	err   error
	given *time.Duration
	// failed and uncounted answer the count of failed card checks.
	failed    int
	uncounted error
}

func (s schema) FailedCardChecks(context.Context) (int, error) { return s.failed, s.uncounted }

func (s schema) MigrationState(ctx context.Context) (postgres.MigrationState, error) {
	if deadline, ok := ctx.Deadline(); ok {
		*s.given = time.Until(deadline)
	}
	return s.state, s.err
}

func healthz(t *testing.T, s *httptransport.Server, code int, status, state string) {
	t.Helper()
	w := send(s, http.MethodGet, "/healthz", "127.0.0.1:8443", nil)
	want := `{"status":"` + status + `"}` + "\n"
	if status == "ok" {
		// A healthy answer also counts the stored cards that failed their check in the last day.
		want = `{"failed_card_checks_24h":0,"status":"ok"}` + "\n"
	}
	if w.Code != code || w.Body.String() != want {
		t.Errorf("%s: %d %q", state, w.Code, w.Body.String())
	}
}

// The body is one of five fixed statuses, whatever the store's error says,
// and the check is given three seconds at most.
func TestHealthzStatesTheDatabaseAndItsMigrations(t *testing.T) {
	driver := errors.New(`failed to connect to "host=db.internal.example user=gtm_app password=app-password"`)
	for _, c := range []struct {
		state      string
		migrations postgres.MigrationState
		err        error
		code       int
		status     string
	}{
		{"current migrations", postgres.MigrationsCurrent, nil, http.StatusOK, "ok"},
		{"a migration not applied", postgres.MigrationsBehind, nil, http.StatusServiceUnavailable, "migrations_behind"},
		{"a later migration applied", postgres.MigrationsAhead, nil, http.StatusServiceUnavailable, "migrations_ahead"},
		{"a migration that changed", postgres.MigrationsChanged, nil, http.StatusServiceUnavailable, "migrations_changed"},
		{"no answer", "", driver, http.StatusServiceUnavailable, "database_unavailable"},
	} {
		var given time.Duration
		healthz(t, surfaceOn(t, schema{state: c.migrations, err: c.err, given: &given}), c.code, c.status, c.state)
		if given <= 0 || given > 3*time.Second {
			t.Errorf("%s: the check was given %s", c.state, given)
		}
	}
	// A healthy answer gives the count as the store has it. A count that cannot be read is no healthy answer.
	var given time.Duration
	w := send(surfaceOn(t, schema{state: postgres.MigrationsCurrent, given: &given, failed: 3}), http.MethodGet, "/healthz",
		"127.0.0.1:8443", nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"failed_card_checks_24h":3,"status":"ok"}`+"\n" {
		t.Errorf("three failed checks: %d %q", w.Code, w.Body.String())
	}
	healthz(t, surfaceOn(t, schema{state: postgres.MigrationsCurrent, given: &given, uncounted: driver}),
		http.StatusServiceUnavailable, "database_unavailable", "a count that cannot be read")
}

// relay carries TCP connections to target until stop, which also ends the
// ones it carries: whoever connected through it can no longer reach target.
func relay(t *testing.T, target string) (address string, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var carried []net.Conn
	stopped := false
	go func() {
		for {
			in, err := listener.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			mu.Lock()
			if stopped {
				in.Close()
				out.Close()
			} else {
				carried = append(carried, in, out)
				go func() { io.Copy(out, in); out.Close() }()
				go func() { io.Copy(in, out); in.Close() }()
			}
			mu.Unlock()
		}
	}()
	stop = func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		for _, conn := range carried {
			conn.Close()
		}
	}
	t.Cleanup(stop)
	return listener.Addr().String(), stop
}

func TestHealthzOnPostgreSQL(t *testing.T) {
	ctx := t.Context()
	direct := pgtest.URL(t)
	through, err := url.Parse(direct)
	if err != nil {
		t.Fatal(err)
	}
	target := through.Host
	if through.Port() == "" {
		target = net.JoinHostPort(through.Hostname(), "5432")
	}
	address, stop := relay(t, target)
	through.Host = address
	store, err := postgres.Open(ctx, through.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s := surfaceOn(t, store)
	healthz(t, s, http.StatusOK, "ok", "every migration applied")

	// The newest migration's row is gone, as in a database restored to the
	// schema before it.
	conn, err := pgx.Connect(ctx, direct)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	names := postgres.MigrationNames()
	if _, err := conn.Exec(ctx, "DELETE FROM schema_migrations WHERE name = $1", names[len(names)-1]); err != nil {
		t.Fatal(err)
	}
	healthz(t, s, http.StatusServiceUnavailable, "migrations_behind", "one migration behind")

	// A migration this binary does not hold is applied, as a newer binary's
	// setup leaves it.
	if _, err := conn.Exec(ctx,
		"INSERT INTO schema_migrations (name, sha256) VALUES ('29991231T2359_later.sql', 'x')"); err != nil {
		t.Fatal(err)
	}
	healthz(t, s, http.StatusServiceUnavailable, "migrations_ahead", "one migration ahead")

	// The first migration's recorded digest is not this binary's file's.
	if _, err := conn.Exec(ctx, "UPDATE schema_migrations SET sha256 = 'changed' WHERE name = $1", names[0]); err != nil {
		t.Fatal(err)
	}
	healthz(t, s, http.StatusServiceUnavailable, "migrations_changed", "a migration that changed")

	stop()
	healthz(t, s, http.StatusServiceUnavailable, "database_unavailable", "an unreachable database")
}
