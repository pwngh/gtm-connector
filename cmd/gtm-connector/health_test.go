package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// schema is a session store that answers only the health check, as set.
type schema struct {
	httptransport.Sessions
	current bool
	err     error
}

func (s schema) MigrationsCurrent(context.Context) (bool, error) { return s.current, s.err }

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
	serveFor(t, schema{current: true})
	if err := health(ctx, nil); err != nil {
		t.Fatalf("current migrations: %v", err)
	}
	for status, answer := range map[string]schema{
		"migrations_not_current": {},
		"database_unavailable":   {err: errors.New("no route to db.internal.example")},
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
	if err := health(ctx, nil); err == nil || err.Error() != `healthz answered 503: {"status":"migrations_not_current"}` {
		t.Fatalf("one migration behind: %v", err)
	}
}
