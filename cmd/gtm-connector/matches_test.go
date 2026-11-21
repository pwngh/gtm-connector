package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// matchResult is a match as deal matches, deal decide and the routes give it.
type matchResult struct {
	ID         string
	Kind       string
	Item       string
	Account    string
	At         *string
	Candidates []string
	State      string
	Deal       *string
	Basis      string
	DecidedBy  *string `json:"decided_by"`
	Supersedes *string
	Receipt    string
	Superseded bool
	Stale      bool
}

// shownWithMatches is deal show's result with the matches its card read.
type shownWithMatches struct {
	Deal    dealResult
	Card    *struct{ Text string }
	Matches []matchResult
}

// calls writes an invented transcript of acc-007 and gives its path. Each text is its own import.
func callFile(t *testing.T, name, said string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), name)
	body := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:04.000\n<v Dana Crest>" + said + "\n\n" +
		"2\n00:00:05.000 --> 00:00:08.000\n<v Morgan Reed>Great, I will send the order form today.\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// An imported call joins the deal whose time holds it, and a deal's card reads it only then. The connector
// decides a call when the account's deal is registered, and when a call is imported while it has one, each
// decision under its own receipt; a call the account's second deal does not hold stays off its card, and the
// card says so. A call with no time fits both deals, so it waits for a person, who decides it through the API
// under a receipt naming them. The owner decides from the CLI. Unknown and ungranted matches are refused
// alike, and every write needs the session's CSRF token and the public origin.
func TestCallsAreMatchedToTheirDeal(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "matches"}
	with := func(argv ...string) []string { return append(argv, where...) }
	d.connectDemo(t, "matches")
	if code, _, stderr := d.read(t, with("connect", "calls", "--seller", "Morgan Reed")...); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	workspace, err := d.owner.Workspace(ctx, "matches")
	if err != nil {
		t.Fatal(err)
	}
	importCall := func(file string, argv ...string) string {
		t.Helper()
		code, stdout, stderr := d.line(t, append(with("--json", "debug", "calls", "import", file, "--account", "acc-007"),
			argv...)...)
		var imported struct {
			ImportID string `json:"import_id"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &imported) != nil {
			t.Fatalf("import %s: exit %d\n%s", file, code, stderr)
		}
		return imported.ImportID
	}
	every := func() []matchResult {
		t.Helper()
		var listed []matchResult
		d.result(t, &listed, with("deal", "matches", "--all")...)
		return listed
	}
	briefGaps := func() []string {
		t.Helper()
		var card savedCard
		d.result(t, &card, with("brief", "acc-007")...)
		var codes []string
		for _, g := range card.Gaps {
			codes = append(codes, g.Code)
		}
		return codes
	}

	// A call imported before the account has a deal is not decided.
	first := importCall(callFile(t, "first.vtt", "We will sign the renewal this quarter."), "--called-at",
		"2020-01-10T15:00:00Z")
	if listed := every(); len(listed) != 0 {
		t.Fatalf("matches before a deal: %+v", listed)
	}

	// Registering the deal decides it: the account's one deal holds every time.
	var deal dealResult
	d.result(t, &deal, with("deal", "new", "acc-007")...)
	listed := every()
	if len(listed) != 1 || listed[0].Item != first || listed[0].Kind != "call" || listed[0].State != "matched" ||
		text(listed[0].Deal) != deal.ID || listed[0].Basis != "only_deal" || listed[0].DecidedBy != nil ||
		text(listed[0].At) != "2020-01-10T15:00:00Z" || len(listed[0].Candidates) != 1 {
		t.Fatalf("the match made at registration: %+v", listed)
	}
	firstMatch := listed[0]
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND tool = 'decide_match' AND status = 'ok'
		AND principal_id IS NULL AND requested_account = 'acc-007' AND brief_id LIKE 'match-%'
		AND arguments::jsonb = jsonb_build_object('kind', 'call', 'item', $2::text, 'decision', 'matched', 'deal', $3::text)`,
		firstMatch.Receipt, first, deal.ID); n != 1 {
		t.Errorf("the receipt of the match: %s", firstMatch.Receipt)
	}

	// The deal's card reads the call and names its match.
	if gaps := briefGaps(); !strings.Contains(strings.Join(gaps, " "), "calls_excerpt") {
		t.Errorf("the deal's card did not read its call: %v", gaps)
	}
	var shown shownWithMatches
	d.result(t, &shown, with("deal", "show", deal.ID)...)
	if len(shown.Matches) != 1 || shown.Matches[0].ID != firstMatch.ID || shown.Card == nil {
		t.Fatalf("deal show: %+v", shown)
	}
	code, stdout, _ := d.read(t, with("deal", "show", deal.ID)...)
	if code != 0 || !strings.Contains(stdout, "Read call\t"+first+" (match "+firstMatch.ID+")\n") {
		t.Errorf("deal show as text:\n%s", stdout)
	}

	// The account's next deal: the call belongs to the first, so the second's card does not read it.
	d.result(t, &deal, with("deal", "close", deal.ID)...)
	var second dealResult
	d.result(t, &second, with("deal", "new", "acc-007")...)
	if second.ID != deal.ID+"-2" {
		t.Fatalf("the second deal: %+v", second)
	}
	// The first deal holds every time before the second was registered, the close included, and the second
	// every time from its registration on.
	for at, want := range map[string]string{"closed_at": deal.ID, "registered_at": second.ID} {
		if n := d.count(t, `SELECT count(*) FROM deals d WHERE d.ref = $1 AND gtm_match_candidates(d.workspace_id,
			'acc-007', (SELECT `+at+` FROM deals WHERE ref = $1), clock_timestamp()) = ARRAY[$1::text]`, want); n != 1 {
			t.Errorf("%s of %s is not its own", at, want)
		}
	}
	if gaps := strings.Join(briefGaps(), " "); !strings.Contains(gaps, "calls_not_matched") ||
		strings.Contains(gaps, "calls_excerpt") {
		t.Errorf("the second deal's card: %s", gaps)
	}
	code, stdout, _ = d.read(t, with("brief", "acc-007")...)
	if code != 0 || !strings.Contains(stdout, "Calls: not read. The account's transcript is not matched to this card.\n") {
		t.Errorf("the second deal's card as text:\n%s", stdout)
	}
	d.result(t, &shown, with("deal", "show", second.ID)...)
	if len(shown.Matches) != 0 {
		t.Errorf("the second deal's card names %+v", shown.Matches)
	}

	// A call after the first deal closed joins the second as it is imported.
	later := importCall(callFile(t, "later.vtt", "We will sign the renewal next quarter."), "--called-at",
		"2030-01-10T15:00:00Z")
	current, decided, err := d.owner.CurrentMatch(ctx, workspace.ID, "call", later)
	if err != nil || !decided || current.State != "matched" || current.DealRef != second.ID ||
		current.Basis != "within_deal" {
		t.Fatalf("the later call: %+v %v %v", current, decided, err)
	}

	// A call with no time fits both deals and waits; the card reads no call meanwhile.
	untimed := importCall(callFile(t, "untimed.vtt", "We will sign the renewal soon."))
	var waiting []matchResult
	d.result(t, &waiting, with("deal", "matches")...)
	if len(waiting) != 1 || waiting[0].Item != untimed || waiting[0].State != "waiting" || waiting[0].Deal != nil ||
		waiting[0].Basis != "time_unknown" || strings.Join(waiting[0].Candidates, " ") != deal.ID+" "+second.ID {
		t.Fatalf("the waiting match: %+v", waiting)
	}
	if gaps := strings.Join(briefGaps(), " "); !strings.Contains(gaps, "calls_not_matched") {
		t.Errorf("a card read a waiting call: %s", gaps)
	}

}

// The same story as the API reads it, in the workspace the demo sign-in belongs to.
func TestMatchRoutesKeepTheGrants(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	importCall := func(file string, argv ...string) {
		t.Helper()
		if code, _, stderr := d.line(t, append([]string{"debug", "calls", "import", file, "--account", "acc-007"},
			argv...)...); code != 0 {
			t.Fatalf("import: %s", stderr)
		}
	}
	importCall(callFile(t, "first.vtt", "We will sign the renewal this quarter."), "--called-at", "2020-01-10T15:00:00Z")
	var first, second dealResult
	d.result(t, &first, "deal", "new", "acc-007")
	d.result(t, &first, "deal", "close", first.ID)
	d.result(t, &second, "deal", "new", "acc-007")
	importCall(callFile(t, "untimed.vtt", "We will sign the renewal soon."))

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
	digest := sha256.Sum256([]byte("outsider-password-0123456789-abcdef"))
	outsider, err := d.owner.EnsureDemoPrincipal(ctx, d.workspace, "demo-outsider", "Demo outsider", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.EnsureAccount(ctx, d.workspace.ID, "acc-001", "Alder Vale Packaging"); err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, outsider.ID, "acc-001"); err != nil {
		t.Fatal(err)
	}

	b := signInToDemo(t, app)
	var queue struct{ Matches []matchResult }
	if err := json.Unmarshal(b.get("/v1/matches"), &queue); err != nil || len(queue.Matches) != 1 ||
		queue.Matches[0].State != "waiting" || len(queue.Matches[0].Candidates) != 2 {
		t.Fatalf("GET /v1/matches: %+v %v", queue, err)
	}
	waiting := queue.Matches[0]
	decide := func(c *signedIn, id string, body map[string]any, header map[string]string) (int, string) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		sent := map[string]string{"Origin": c.origin, "X-CSRF-Token": c.csrf, "Content-Type": "application/json"}
		for name, value := range header {
			sent[name] = value
		}
		reply := c.send(http.MethodPost, "/v1/matches/"+id, encoded, sent)
		defer reply.Body.Close()
		answer, _ := io.ReadAll(reply.Body)
		if reply.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("POST /v1/matches/%s is cacheable", id)
		}
		return reply.StatusCode, string(answer)
	}
	onto := map[string]any{"decision": "match", "deal": second.ID}

	// Writes without the CSRF token, from another origin, with a bad body or onto another account's deal.
	if status, _ := decide(b, waiting.ID, onto, map[string]string{"X-CSRF-Token": "stale"}); status != http.StatusForbidden {
		t.Errorf("a decision without the CSRF token: %d", status)
	}
	if status, _ := decide(b, waiting.ID, onto, map[string]string{"Origin": "https://elsewhere.example"}); status !=
		http.StatusForbidden {
		t.Errorf("a decision from another origin: %d", status)
	}
	for _, body := range []map[string]any{{"decision": "match"}, {"decision": "reject", "deal": second.ID},
		{"decision": "maybe"}, {"deal": second.ID}} {
		if status, _ := decide(b, waiting.ID, body, nil); status != http.StatusBadRequest {
			t.Errorf("the decision %v: %d", body, status)
		}
	}
	var copper dealResult
	d.result(t, &copper, "deal", "new", "acc-002")
	if status, answer := decide(b, waiting.ID, map[string]any{"decision": "match", "deal": copper.ID}, nil); status !=
		http.StatusBadRequest || !strings.Contains(answer, `"invalid_deal"`) {
		t.Errorf("a decision onto another account's deal: %d %s", status, answer)
	}

	// The outsider holds no grant on acc-007: the queue is empty, and its match and an unknown one are
	// refused alike, each under a refused receipt that names no account.
	o := signInAs(t, app, "demo-outsider", "outsider-password-0123456789-abcdef")
	if err := json.Unmarshal(o.get("/v1/matches"), &queue); err != nil || len(queue.Matches) != 0 {
		t.Errorf("the outsider's queue: %+v %v", queue, err)
	}
	var refusals []string
	for _, id := range []string{waiting.ID, "00000000-0000-4000-8000-000000000000", "not-an-id"} {
		status, answer := decide(o, id, onto, nil)
		if status != http.StatusForbidden {
			t.Errorf("the outsider's decision of %s: %d", id, status)
		}
		refusals = append(refusals, answer)
	}
	if refusals[0] != refusals[1] || refusals[1] != refusals[2] {
		t.Errorf("the refusals differ: %q", refusals)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'decide_match'
		AND status = 'error' AND http_status = 403 AND account_ref IS NULL`, outsider.ID); n != 3 {
		t.Errorf("the outsider's refused decisions left %d receipts", n)
	}

	// The person decides: the call joins the open deal, under a receipt naming them, and the card reads it.
	status, answer := decide(b, waiting.ID, onto, nil)
	var decided struct{ Match matchResult }
	if json.Unmarshal([]byte(answer), &decided); status != http.StatusOK || decided.Match.State != "matched" ||
		text(decided.Match.Deal) != second.ID || decided.Match.Basis != "person" ||
		text(decided.Match.DecidedBy) != principal.ID || text(decided.Match.Supersedes) != waiting.ID {
		t.Fatalf("the decision: %d %s", status, answer)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND tool = 'decide_match' AND status = 'ok'
		AND principal_id = $2 AND account_ref = 'acc-007' AND arguments ->> 'match' = $3`,
		decided.Match.Receipt, principal.ID, waiting.ID); n != 1 {
		t.Errorf("the decision's receipt: %s", decided.Match.Receipt)
	}
	if status, answer := decide(b, waiting.ID, onto, nil); status != http.StatusConflict ||
		!strings.Contains(answer, `"match_decided"`) {
		t.Errorf("deciding a superseded match: %d %s", status, answer)
	}
	if err := json.Unmarshal(b.get("/v1/matches"), &queue); err != nil || len(queue.Matches) != 0 {
		t.Errorf("the queue after the decision: %+v %v", queue, err)
	}
	if code, _, stderr := d.read(t, "brief", "acc-007"); code != 0 {
		t.Fatalf("brief: %s", stderr)
	}
	var one struct {
		Deal    dealResult
		Matches []matchResult
	}
	if err := json.Unmarshal(b.get("/v1/deals/"+second.ID), &one); err != nil || len(one.Matches) != 1 ||
		one.Matches[0].ID != decided.Match.ID {
		t.Errorf("GET /v1/deals/%s: %+v %v", second.ID, one, err)
	}

	// The owner takes it off every card from the CLI; the next card reads no call.
	var rejected matchResult
	d.result(t, &rejected, "deal", "decide", decided.Match.ID, "--reject")
	if rejected.State != "rejected" || rejected.Deal != nil || rejected.DecidedBy != nil || rejected.Basis != "person" {
		t.Errorf("the owner's decision: %+v", rejected)
	}
	if _, message, _ := d.refusal(t, 3, "deal", "decide", decided.Match.ID, "--deal", second.ID); !strings.Contains(
		message, "decided again") {
		t.Errorf("the owner's decision of a superseded match: %s", message)
	}
	if code, _, _ := d.read(t, "deal", "decide", rejected.ID); code != 2 {
		t.Errorf("deal decide without --deal or --reject: exit %d", code)
	}
	if _, message, _ := d.refusal(t, 2, "deal", "decide", "00000000-0000-4000-8000-000000000000", "--reject"); !strings.Contains(
		message, "No match") {
		t.Errorf("deal decide of an unknown match: %s", message)
	}
	code, stdout, _ := d.read(t, "brief", "acc-007")
	if code != 0 || !strings.Contains(stdout, "Calls: not read. The account's transcript is not matched to this card.\n") {
		t.Errorf("the card after the rejection:\n%s", stdout)
	}

	// A revoked grant takes the account's matches away, and refuses their decision.
	d.result(t, &rejected, "deal", "decide", rejected.ID, "--deal", second.ID)
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal.ID, "acc-007"); err != nil {
		t.Fatal(err)
	}
	if status, _ := decide(b, rejected.ID, map[string]any{"decision": "reject"}, nil); status != http.StatusForbidden {
		t.Errorf("a decision after the grant was revoked: %d", status)
	}
	if reply := b.send(http.MethodGet, "/v1/deals/"+second.ID, nil, nil); reply.StatusCode != http.StatusForbidden {
		t.Errorf("the deal after the grant was revoked: %d", reply.StatusCode)
	}
}

// A deal registered while brief reads the account's imported call: the card read the call for no deal, so it
// is not published, and the next brief reads the call the registration matched.
func TestADealOpenedDuringBriefOfAMatchedCall(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "call.vtt", "We will sign soon."),
		"--account", "acc-007"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	opened := false
	hookedCRM(t, func(tool, account string) {
		if tool == "list_opportunities" && !opened {
			opened = true
			if code, _, stderr := d.read(t, "deal", "new", account); code != 0 {
				t.Errorf("deal new during brief: %s", stderr)
			}
		}
	})
	if _, message, _ := d.refusal(t, 3, "brief", "acc-007"); !opened || !strings.Contains(message, "changed while") {
		t.Fatalf("brief while a deal was registered: %s", message)
	}
	if n := d.count(t, `SELECT count(*) FROM deal_cards`); n != 0 {
		t.Errorf("%d deal cards", n)
	}
	code, stdout, stderr := d.read(t, "brief", "acc-007")
	if code != 0 || !strings.Contains(stdout, "Calls: one transcript excerpt was read") ||
		!strings.Contains(stderr, "Deal: ") {
		t.Fatalf("brief again: exit %d\n%s%s", code, stdout, stderr)
	}
	if n := d.count(t, `SELECT count(*) FROM deal_card_matches`); n != 1 {
		t.Errorf("the card names %d matches", n)
	}
}

// The database keeps a decision as its receipt names it and as the rule makes it, never changes or deletes
// one, and refuses a card that names a match it did not read for its deal.
func TestTheDatabaseKeepsTheMatchRules(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if code, _, stderr := d.read(t, "connect", "calls", "--seller", "Morgan Reed"); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	if code, _, stderr := d.line(t, "debug", "calls", "import", callFile(t, "call.vtt", "We will sign soon."),
		"--account", "acc-007"); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	var deal dealResult
	d.result(t, &deal, "deal", "new", "acc-007")
	match, err := d.owner.Matches(ctx, d.workspace.ID, true)
	if err != nil || len(match) != 1 {
		t.Fatalf("matches: %+v %v", match, err)
	}
	owner := conn(t, d.ownerURL)
	for name, statement := range map[string]string{
		"a change":   `UPDATE deal_matches SET state = 'rejected', deal_ref = NULL`,
		"a deletion": `DELETE FROM deal_matches`,
		"a second automatic decision": fmt.Sprintf(`INSERT INTO deal_matches (workspace_id, kind, item, account_ref,
			candidates, state, deal_ref, basis, receipt_id) SELECT workspace_id, kind, item, account_ref, candidates,
			state, deal_ref, basis, gen_random_uuid() FROM deal_matches WHERE id = '%s'`, match[0].ID),
		"a decision its receipt does not name": fmt.Sprintf(`INSERT INTO deal_matches (workspace_id, kind, item,
			account_ref, candidates, state, deal_ref, basis, supersedes, receipt_id) SELECT workspace_id, kind, item,
			account_ref, candidates, 'rejected', NULL, 'person', id, (SELECT account_read_id FROM deals LIMIT 1)
			FROM deal_matches WHERE id = '%s'`,
			match[0].ID),
	} {
		if _, err := owner.Exec(ctx, statement); err == nil {
			t.Errorf("the database accepted %s", name)
		}
	}
	// Another deal's card did not read the call, so it may not name its match.
	var other dealResult
	d.result(t, &other, "deal", "new", "acc-001")
	if code, _, stderr := d.read(t, "brief", "acc-001"); code != 0 {
		t.Fatalf("brief acc-001: %s", stderr)
	}
	if _, err := owner.Exec(ctx, fmt.Sprintf(`INSERT INTO deal_card_matches (workspace_id, card_id, match_id)
		SELECT workspace_id, card_id, '%s' FROM deal_cards WHERE deal_ref = $1`, match[0].ID), other.ID); err == nil {
		t.Errorf("the database accepted a match on another deal's card")
	}
	var violations int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM gtm_integrity_violations()`).Scan(&violations); err != nil ||
		violations != 0 {
		t.Errorf("%d integrity violations: %v", violations, err)
	}
}

// A mapped Slack thread joins the account's deal when the deal is registered, by its parent message's time,
// and the deal's card reads it from the invented Slack and names its match.
func TestAMappedThreadIsMatchedToItsDeal(t *testing.T) {
	d, folder, wall := slackSetup(t)
	slackAPI := newInventedSlack(t)
	slackAPI.valid[inventedAccess] = true
	books := registerRecordedFolders(t)
	var c recordedCard
	for _, card := range recordedCards(t) {
		if card.thread != nil {
			c = card
			break
		}
	}
	seedSignIn(t, folder, "threads", *wall, "ready")
	connectRecorded(t, d, books, c, "threads")
	slackAPI.serve(c.mapped("channel_id"), c.thread, c.page)
	if code, _, stderr := d.line(t, "debug", "slack", "map", "--account", c.account, "--channel",
		c.mapped("channel_id"), "--thread-ts", c.mapped("thread_ts"), "--state-dir", "threads"); code != 0 {
		t.Fatalf("map: exit %d\n%s", code, stderr)
	}
	var deal dealResult
	d.result(t, &deal, "deal", "new", c.account, "--state-dir", "threads")
	var listed []matchResult
	d.result(t, &listed, "deal", "matches", "--all", "--state-dir", "threads")
	if len(listed) != 1 || listed[0].Kind != "thread" || listed[0].State != "matched" || text(listed[0].Deal) != deal.ID ||
		listed[0].At == nil || !strings.HasPrefix(listed[0].Item, "map-") {
		t.Fatalf("the thread's match: %+v", listed)
	}
	code, stdout, stderr := d.line(t, "brief", c.account, "--as-of", "2027-01-15", "--state-dir", "threads")
	if code != 0 || !strings.Contains(stdout, "from one selected thread") {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	var shown shownWithMatches
	d.result(t, &shown, "deal", "show", deal.ID, "--state-dir", "threads")
	if len(shown.Matches) != 1 || shown.Matches[0].ID != listed[0].ID {
		t.Errorf("the deal's card names %+v", shown.Matches)
	}
}
