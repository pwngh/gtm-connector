package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
	d.result(t, &deal, "deal", "close", deal.ID, "--reason", "Renewed", "--no-card")
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
	d.result(t, &first, "deal", "close", first.ID, "--reason", "Renewed", "--no-card")
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

// A restore checks a decision that waits between two deals too: its first candidate is the closed deal the
// item was on, its second the deal open when it was made, and it was made after the first closed. The table
// refuses a later decision that is neither a person's nor that kind.
func TestRestoreChecksAWaitBetweenTwoDeals(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "untimed.vtt", "We will sign soon."),
		"--account", "acc-007"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	var deal, second dealResult
	d.result(t, &deal, "deal", "new", "acc-007")
	d.result(t, &deal, "deal", "close", deal.ID, "--reason", "Renewed", "--no-card")
	d.result(t, &second, "deal", "new", "acc-007")
	owner := conn(t, d.ownerURL)
	violations := func() []string {
		t.Helper()
		rows, err := owner.Query(ctx, `SELECT * FROM gtm_integrity_violations()`)
		if err != nil {
			t.Fatal(err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if found := violations(); len(found) != 0 {
		t.Fatalf("violations in a database the commands wrote: %v", found)
	}
	var waits int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM deal_matches WHERE basis = 'deal_closed' AND state = 'waiting'
		AND supersedes IS NOT NULL AND candidates = ARRAY[$1::text, $2::text]`, deal.ID, second.ID).Scan(&waits); err != nil ||
		waits != 1 {
		t.Fatalf("%d decisions wait between the two deals: %v", waits, err)
	}
	if _, err := owner.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	// A later decision by the connector is only of that kind: the table refuses one by the time rule, also
	// with its triggers off, as a restore loads it.
	_, err := owner.Exec(ctx, `INSERT INTO deal_matches (workspace_id, kind, item, account_ref, candidates, state,
		deal_ref, basis, supersedes, receipt_id) SELECT workspace_id, kind, item, account_ref, candidates, 'matched',
		candidates[2], 'within_deal', id, gen_random_uuid() FROM deal_matches WHERE basis = 'deal_closed'`)
	if err == nil || !strings.Contains(err.Error(), "deal_matches_superseding_basis") {
		t.Errorf("a later decision by the time rule: %v", err)
	}
	if _, err := owner.Exec(ctx, `CREATE TEMP TABLE kept AS SELECT * FROM deal_matches`); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]string{
		"its candidates the other way round": `UPDATE deal_matches SET candidates = ARRAY[candidates[2], candidates[1]]
			WHERE basis = 'deal_closed'`,
		"the closed deal twice": `UPDATE deal_matches SET candidates = ARRAY[candidates[1], candidates[1]]
			WHERE basis = 'deal_closed'`,
		"one candidate": `UPDATE deal_matches SET candidates = ARRAY[candidates[1]] WHERE basis = 'deal_closed'`,
		"a time before its deal closed": `UPDATE deal_matches SET decided_at = (SELECT closed_at - interval '1 microsecond'
			FROM deals WHERE closed_at IS NOT NULL) WHERE basis = 'deal_closed'`,
	} {
		if _, err := owner.Exec(ctx, change); err != nil {
			t.Fatal(err)
		}
		if found := strings.Join(violations(), "\n"); !strings.Contains(found, "does not wait between a closed deal") {
			t.Errorf("a decision with %s passes the restore checks: %s", name, found)
		}
		if _, err := owner.Exec(ctx, `DELETE FROM deal_matches`); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO deal_matches SELECT * FROM kept`); err != nil {
			t.Fatal(err)
		}
	}
	if found := violations(); len(found) != 0 {
		t.Errorf("violations after the rows were put back: %v", found)
	}
}

// decisionReceipt commits a decide_match receipt of the connector's, finished ok, with these arguments, as
// another writer to the database could, and gives its ID.
func decisionReceipt(t *testing.T, d *demoDatabase, workspaceID, account string, arguments map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("match-%032x", time.Now().UnixNano())
	receipt, err := d.owner.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: workspaceID, OperationID: name + "/1",
		TokenID: strings.Repeat("0", 64), Tool: "decide_match", AccountRef: account, Arguments: encoded,
		BriefID: name, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.owner.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken,
		Status: "ok", HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	return receipt.ID
}

// The database holds every writer to the rule, not only the connector's own code. No writer asks again about
// an item a person kept on the closed deal once the open deal was registered; the account's next deal is
// another question. A wait names the deal open now and is dated by the database's clock, so it stays true
// when that deal closes.
func TestTheDatabaseHoldsEveryWriterToTheWait(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "untimed.vtt", "We will sign soon."),
		"--account", "acc-007"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	var first, second, third, fourth dealResult
	d.result(t, &first, "deal", "new", "acc-007")
	d.result(t, &first, "deal", "close", first.ID, "--reason", "Renewed", "--no-card")
	d.result(t, &second, "deal", "new", "acc-007")
	var waiting []matchResult
	d.result(t, &waiting, "deal", "matches")
	if len(waiting) != 1 || waiting[0].Basis != "deal_closed" {
		t.Fatalf("the wait: %+v", waiting)
	}
	item := waiting[0].Item
	owner := conn(t, d.ownerURL)
	wait := func(supersedes, open, decidedAt string) error {
		receipt := decisionReceipt(t, d, d.workspace.ID, "acc-007", map[string]any{"kind": "call", "item": item,
			"decision": "waiting", "deal": nil, "match": supersedes})
		_, err := owner.Exec(ctx, `INSERT INTO deal_matches (workspace_id, kind, item, account_ref, at, candidates, state,
			basis, supersedes, receipt_id, decided_at) SELECT workspace_id, kind, item, account_ref, at,
			ARRAY[$2::text, $3::text], 'waiting', 'deal_closed', id, $4, `+decidedAt+` FROM deal_matches WHERE id = $1`,
			supersedes, first.ID, open, receipt)
		return err
	}
	// A person keeps the call on the closed deal. No writer asks about it again for this open deal.
	var kept matchResult
	d.result(t, &kept, "deal", "decide", waiting[0].ID, "--deal", first.ID)
	if err := wait(kept.ID, second.ID, "clock_timestamp()"); err == nil ||
		!strings.Contains(err.Error(), "does not wait between a closed deal") {
		t.Errorf("a wait over a person's choice of the closed deal: %v", err)
	}
	// The account's next deal is another question: the connector asks, and names the choice it supersedes.
	d.result(t, &second, "deal", "close", second.ID, "--reason", "Renewed", "--no-card")
	d.result(t, &third, "deal", "new", "acc-007")
	d.result(t, &waiting, "deal", "matches")
	if len(waiting) != 1 || strings.Join(waiting[0].Candidates, " ") != first.ID+" "+third.ID ||
		text(waiting[0].Supersedes) != kept.ID {
		t.Fatalf("the wait for the third deal: %+v", waiting)
	}
	d.result(t, &kept, "deal", "decide", waiting[0].ID, "--deal", first.ID)
	d.result(t, &third, "deal", "close", third.ID, "--reason", "Renewed", "--no-card")
	d.result(t, &fourth, "deal", "new", "acc-007")
	d.result(t, &waiting, "deal", "matches")
	if len(waiting) != 1 || text(waiting[0].Supersedes) != kept.ID {
		t.Fatalf("the wait for the fourth deal: %+v", waiting)
	}
	// With the connector's own wait taken away, another writer makes one. A wait that names the deal before,
	// dated while that deal was open, is refused: the fourth deal is the open one.
	for _, statement := range []string{`SET session_replication_role = replica`,
		`DELETE FROM deal_matches WHERE id = '` + waiting[0].ID + `'`, `SET session_replication_role = origin`} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := wait(kept.ID, third.ID, `(SELECT closed_at - interval '1 microsecond' FROM deals WHERE ref = '`+third.ID+`')`); err == nil {
		t.Error("a wait that names a deal that is not the open one was accepted")
	}
	// A wait dated tomorrow is dated now.
	if err := wait(kept.ID, fourth.ID, `clock_timestamp() + interval '1 day'`); err != nil {
		t.Fatalf("a wait between the closed deal and the open one: %v", err)
	}
	if n := d.count(t, `SELECT count(*) FROM deal_matches WHERE supersedes = $1 AND decided_at <= clock_timestamp()`,
		kept.ID); n != 1 {
		t.Error("a wait kept the time its writer gave it")
	}
	d.result(t, &fourth, "deal", "close", fourth.ID, "--reason", "Renewed", "--no-card")
	if n := d.count(t, `SELECT count(*) FROM gtm_integrity_violations()`); n != 0 {
		t.Errorf("%d integrity violations after the open deal closed", n)
	}
}
