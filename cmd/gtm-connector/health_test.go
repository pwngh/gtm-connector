package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// schema is a session store that answers only the health check, as set.
type schema struct {
	httptransport.Sessions
	state postgres.MigrationState
	err   error
}

func (s schema) MigrationState(context.Context) (postgres.MigrationState, error) {
	return s.state, s.err
}

// serveFor serves sessions as serve does, on a free loopback port with new
// credentials, and sets this process's environment to that install.
func serveFor(t *testing.T, sessions httptransport.Sessions) {
	t.Helper()
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts(nil)); err != nil {
		t.Fatal(err)
	}
	pair, key, err := serving(creds)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	surface, err := httptransport.New(httptransport.Config{PublicOrigin: "https://" + address,
		AllowedHosts: []string{address}, SessionKey: key}, sessions, authz.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(surface, pair, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go server.ServeTLS(listener, "", "")
	t.Cleanup(func() { server.Close() })
	for name, value := range map[string]string{"GTM_CREDENTIALS_DIR": dir, "GTM_LISTEN": address,
		"GTM_CONTAINER_LISTEN": "", "GTM_PUBLIC_ORIGIN": "", "GTM_ALLOWED_HOSTS": ""} {
		t.Setenv(name, value)
	}
}

// health fails with the server's status unless that status is ok, and main
// exits 1 on any command's error.
func TestHealthFailsWithTheServersStatus(t *testing.T) {
	serveFor(t, schema{state: postgres.MigrationsCurrent})
	if err := health(ctx, nil); err != nil {
		t.Fatalf("current migrations: %v", err)
	}
	for status, answer := range map[string]schema{
		"migrations_behind":    {state: postgres.MigrationsBehind},
		"migrations_ahead":     {state: postgres.MigrationsAhead},
		"migrations_changed":   {state: postgres.MigrationsChanged},
		"database_unavailable": {err: errors.New("no route to db.internal.example")},
	} {
		serveFor(t, answer)
		if err := health(ctx, nil); err == nil || err.Error() != `healthz answered 503: {"status":"`+status+`"}` {
			t.Errorf("%s: %v", status, err)
		}
	}
}

// The server's own role reads schema_migrations, so health tells a database
// that lost its newest migration from a current one.
func TestHealthReadsTheMigrationsAsTheAppRole(t *testing.T) {
	d := setUpDemo(t)
	store, err := openAppStore(ctx, config{databaseURL: d.appURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	serveFor(t, store)
	if err := health(ctx, nil); err != nil {
		t.Fatalf("current migrations: %v", err)
	}
	conn, err := pgx.Connect(ctx, d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1",
		postgres.LatestSchemaVersion()); err != nil {
		t.Fatal(err)
	}
	if err := health(ctx, nil); err == nil || err.Error() != `healthz answered 503: {"status":"migrations_behind"}` {
		t.Fatalf("one migration behind: %v", err)
	}
}

// serve does not start on a database whose migrations are not exactly this
// binary's. Each of the three states refuses with its own line, before the
// credentials are opened or a port is taken, and a current database gets past
// the check. No migration is named by its number: the newest is whatever
// LatestSchemaVersion says, and a later one is one past it.
func TestServeRefusesADatabaseWhoseMigrationsAreNotItsOwn(t *testing.T) {
	d := setUpDemo(t)
	conn, err := pgx.Connect(ctx, d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	run := func(statement string, arguments ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	// There is no credentials folder, so a serve that gets past the database
	// stops there, with another error, before it listens.
	for name, value := range map[string]string{"GTM_DATABASE_URL": d.appURL,
		"GTM_CREDENTIALS_DIR": filepath.Join(t.TempDir(), "missing"),
		"GTM_LISTEN":          "127.0.0.1:0", "GTM_CONTAINER_LISTEN": "", "GTM_PUBLIC_ORIGIN": "", "GTM_ALLOWED_HOSTS": ""} {
		t.Setenv(name, value)
	}
	refusal := func(state string, want error) {
		t.Helper()
		if err := serve(ctx, nil); err == nil || err.Error() != want.Error() || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: serve answered %v, not %v", state, err, want)
		}
	}
	latest := postgres.LatestSchemaVersion()
	var newest struct{ name, digest string }
	if err := conn.QueryRow(ctx, "SELECT name, sha256 FROM schema_migrations WHERE version = $1", latest).
		Scan(&newest.name, &newest.digest); err != nil {
		t.Fatal(err)
	}

	if err := serve(ctx, nil); err == nil || !strings.Contains(err.Error(), "run setup first") ||
		strings.Contains(err.Error(), "the database") {
		t.Fatalf("current migrations: serve stopped at %v, not at the missing credentials", err)
	}

	run("DELETE FROM schema_migrations WHERE version = $1", latest)
	refusal("behind", postgres.MigrationsBehind.Err())
	run("INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)", latest, newest.name, newest.digest)

	run("INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, 'later.sql', 'x')", latest+1)
	refusal("ahead", postgres.MigrationsAhead.Err())
	run("DELETE FROM schema_migrations WHERE version = $1", latest+1)

	// The schema is at this binary's version, as the check before this one
	// asked, and one applied migration's contents are not this binary's.
	run("UPDATE schema_migrations SET sha256 = $2 WHERE version = $1", latest, strings.Repeat("0", 64))
	refusal("changed", postgres.MigrationsChanged.Err())
	run("UPDATE schema_migrations SET sha256 = $2 WHERE version = $1", latest, newest.digest)

	if err := serve(ctx, nil); err == nil || strings.Contains(err.Error(), "the database") {
		t.Fatalf("current migrations again: %v", err)
	}
}
