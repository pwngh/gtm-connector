// Package pgtest gives each test its own empty PostgreSQL database.
//
// Tests that need one call URL. It reads GTM_TEST_DATABASE_URL, a server URL
// whose role may create databases, and skips the test when that is unset,
// unless GTM_REQUIRE_DATABASE=1 (as in CI), where it fails instead.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// URL creates a fresh database for this test, drops it when the test ends,
// and returns its URL.
func URL(t testing.TB) string {
	t.Helper()
	admin := os.Getenv("GTM_TEST_DATABASE_URL")
	if admin == "" {
		if os.Getenv("GTM_REQUIRE_DATABASE") == "1" {
			t.Fatal("GTM_REQUIRE_DATABASE=1 but GTM_TEST_DATABASE_URL is not set")
		}
		t.Skip("GTM_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(admin)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("GTM_TEST_DATABASE_URL must be a postgres:// URL")
	}
	suffix := make([]byte, 8)
	rand.Read(suffix)
	name := "gtm_test_" + hex.EncodeToString(suffix)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("test database server: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8' LOCALE 'C'"); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Errorf("drop test database: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	parsed.Path = "/" + name
	return parsed.String()
}
