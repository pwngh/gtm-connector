package mcp_test

import (
	"crypto/sha256"
	"testing"
	"time"
)

// One clock decides a token's expiry: the database's, which also set it. A process whose clock is ten minutes
// behind the database refuses a token the database says has expired, and the refusal is receipted.
func TestMountedExpiryIsTheDatabasesClock(t *testing.T) {
	m := mount(t, nil, nil)
	m.wall = func() float64 { return float64(time.Now().Add(-10*time.Minute).UnixMicro()) / 1e6 }
	const bearer = "bearer-that-expired-by-the-database-clock-0000"
	digest := sha256.Sum256([]byte(bearer))
	must(t, m.sql(`INSERT INTO machine_tokens (workspace_id, principal_id, token_sha256, scopes, created_at, expires_at)
		VALUES ($1, $2, $3, ARRAY['crm:read'], now() - interval '1 hour', now() - interval '1 second')`,
		m.workspace.ID, m.machine.ID, digest[:]))
	response, outer := m.call(1, "get_account", map[string]any{"account_id": "acc-003"}, bearer)
	if response.Code != 401 || outer.label() != "get_account error 401" || len(m.book.made()) != 0 {
		t.Fatalf("a token past its expiry by the database's clock: %d %s %s", response.Code, outer.label(), response.Body)
	}
}
