package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// matchedCall sets up the default workspace with imported calls and one call of acc-007 at 2020-01-10,
// matched to the account's first deal, and gives that deal.
func matchedCall(t *testing.T, d *demoDatabase) dealResult {
	t.Helper()
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "call.vtt", "We will sign soon."),
		"--account", "acc-007", "--called-at", "2020-01-10T15:00:00Z"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	var deal dealResult
	d.result(t, &deal, "deal", "new", "acc-007")
	return deal
}

// A rejected call stays off every card: the account's own card once its deal is closed, and an MCP read of
// the account's calls, which says why.
func TestARejectedCallReachesNoCard(t *testing.T) {
	d := setUpDemo(t)
	deal := matchedCall(t, d)
	var listed []matchResult
	d.result(t, &listed, "deal", "matches", "--all")
	var rejected matchResult
	d.result(t, &rejected, "deal", "decide", listed[0].ID, "--reject")
	d.result(t, &deal, "deal", "close", deal.ID)
	code, stdout, stderr := d.read(t, "brief", "acc-007")
	if code != 0 || strings.Contains(stdout, "We will sign soon.") ||
		!strings.Contains(stdout, "Calls: not read. The account's transcript is not matched to this card.\n") {
		t.Fatalf("the account's card after the rejection: exit %d\n%s%s", code, stdout, stderr)
	}
	p := startStdio(t, d, "--scope", "calls:read")
	p.send(stdioStart, stdioReady, stdioCall(2, "read_call_notes", `{"account_id":"acc-007"}`))
	p.answer(1)
	refused := p.answer(2)
	if dig(refused, "result", "isError") != true || strings.Contains(fmt.Sprint(refused), "We will sign soon.") ||
		!strings.Contains(fmt.Sprint(refused), "calls_not_matched") {
		t.Errorf("the MCP read of a rejected call: %v", refused)
	}
	p.stop()
}

// A decision and a card's publication that names the match take the match's row in turn: a card waits for
// a decision being made, and a decision waits for a card being published.
func TestADecisionAndAPublicationWaitForEachOther(t *testing.T) {
	d := setUpDemo(t)
	matchedCall(t, d)
	var listed []matchResult
	d.result(t, &listed, "deal", "matches", "--all")
	hold := func(lock string) func() {
		t.Helper()
		owner := conn(t, d.ownerURL)
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM deal_matches WHERE id = $1 FOR `+lock, listed[0].ID); err != nil {
			t.Fatal(err)
		}
		return func() { tx.Rollback(ctx) }
	}
	waits := func(what string, release func(), run func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- run() }()
		select {
		case err := <-done:
			t.Fatalf("%s did not wait: %v", what, err)
		case <-time.After(1500 * time.Millisecond):
		}
		release()
		if err := <-done; err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// A decision in progress holds the row for update: the card's publication waits for it.
	waits("the publication", hold("UPDATE"), func() error {
		if code, _, stderr := d.read(t, "brief", "acc-007"); code != 0 {
			return fmt.Errorf("brief: %s", stderr)
		}
		return nil
	})
	// A publication in progress holds the row for share: the decision waits for it.
	waits("the decision", hold("SHARE"), func() error {
		_, err := d.owner.DecideMatch(ctx, postgres.MatchDecision{WorkspaceID: d.workspace.ID, MatchID: listed[0].ID})
		return err
	})
}

// A restore checks that each decision of the connector follows from the deals registered when it was made
// and from its item's own time.
func TestRestoreChecksTheMatchRule(t *testing.T) {
	d := setUpDemo(t)
	matchedCall(t, d)
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE TEMP TABLE kept AS SELECT * FROM deal_matches`); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]string{
		"another time than its call's": `UPDATE deal_matches SET at = at + interval '1 day'`,
		"no deal registered before it": `UPDATE deal_matches SET decided_at = decided_at - interval '1 hour'`,
	} {
		if _, err := owner.Exec(ctx, change); err != nil {
			t.Fatal(err)
		}
		var violations int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM gtm_integrity_violations()`).Scan(&violations); err != nil ||
			violations == 0 {
			t.Errorf("a decision with %s passes the restore checks: %v", name, err)
		}
		if _, err := owner.Exec(ctx, `DELETE FROM deal_matches`); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO deal_matches SELECT * FROM kept`); err != nil {
			t.Fatal(err)
		}
	}
	var violations int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM gtm_integrity_violations()`).Scan(&violations); err != nil ||
		violations != 0 {
		t.Errorf("%d violations in a database the commands wrote: %v", violations, err)
	}
}

// A waiting match whose call is no longer the account's current import leaves the queue, and the full list
// marks it stale. The API's full list gives every current decision of the person's accounts, and a deal's
// card marks a match a later decision superseded.
func TestStaleAndSupersededMatchesAreMarked(t *testing.T) {
	d := setUpDemo(t)
	first := matchedCall(t, d)
	if code, _, stderr := d.read(t, "brief", "acc-007"); code != 0 {
		t.Fatalf("brief: %s", stderr)
	}
	var listed []matchResult
	d.result(t, &listed, "deal", "matches", "--all")
	read := listed[0]
	var second dealResult
	d.result(t, &first, "deal", "close", first.ID)
	d.result(t, &second, "deal", "new", "acc-007")
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "untimed.vtt", "We will sign one day."),
		"--account", "acc-007"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	var waiting []matchResult
	if d.result(t, &waiting, "deal", "matches"); len(waiting) != 1 || waiting[0].Stale {
		t.Fatalf("the queue: %+v", waiting)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "later.vtt", "We will sign later."),
		"--account", "acc-007", "--called-at", "2030-01-10T15:00:00Z"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	if d.result(t, &listed, "deal", "matches"); len(listed) != 0 {
		t.Errorf("the queue lists a match of a call no longer current: %+v", listed)
	}
	d.result(t, &listed, "deal", "matches", "--all")
	stale := 0
	for _, m := range listed {
		if m.Stale {
			stale++
		}
	}
	if stale != 2 || len(listed) != 3 {
		t.Errorf("deal matches --all: %+v", listed)
	}

	app, err := postgres.Open(ctx, d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	principal, err := d.owner.DemoPrincipal(ctx, demoLogin)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, principal.ID, "acc-007"); err != nil {
		t.Fatal(err)
	}
	b := signInToDemo(t, app)
	var queue struct{ Matches []matchResult }
	if err := json.Unmarshal(b.get("/v1/matches?all=1"), &queue); err != nil || len(queue.Matches) != 3 {
		t.Errorf("GET /v1/matches?all=1: %+v %v", queue, err)
	}
	if err := json.Unmarshal(b.get("/v1/matches"), &queue); err != nil || len(queue.Matches) != 0 {
		t.Errorf("GET /v1/matches: %+v %v", queue, err)
	}

	// The first deal's card read the first call; deciding it again marks the card's match superseded.
	var decided matchResult
	d.result(t, &decided, "deal", "decide", read.ID, "--reject")
	var shown shownWithMatches
	d.result(t, &shown, "deal", "show", first.ID)
	if len(shown.Matches) != 1 || shown.Matches[0].ID != read.ID || !shown.Matches[0].Superseded {
		t.Errorf("deal show of the first deal: %+v", shown.Matches)
	}
	var one struct{ Matches []matchResult }
	if err := json.Unmarshal(b.get("/v1/deals/"+first.ID), &one); err != nil || len(one.Matches) != 1 ||
		!one.Matches[0].Superseded {
		t.Errorf("GET /v1/deals/%s: %+v %v", first.ID, one, err)
	}
	code, stdout, _ := d.read(t, "deal", "show", first.ID)
	if code != 0 || !strings.Contains(stdout, " (match "+read.ID+", decided again)\n") {
		t.Errorf("deal show as text:\n%s", stdout)
	}
}
