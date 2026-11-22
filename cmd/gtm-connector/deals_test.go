package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// dealResult is a deal as deal new, list, show and close give it, and as the routes do.
type dealResult struct {
	ID           string  `json:"id"`
	Account      string  `json:"account"`
	AccountName  string  `json:"account_name"`
	State        string  `json:"state"`
	Opportunity  *string `json:"opportunity"`
	Contract     *string `json:"contract"`
	RenewsOn     *string `json:"renews_on"`
	RegisteredBy *string `json:"registered_by"`
	ClosedAt     *string `json:"closed_at"`
	Reads        struct {
		GetAccount        string `json:"get_account"`
		ListOpportunities string `json:"list_opportunities"`
	} `json:"reads"`
	LatestCard *struct {
		ID      string `json:"id"`
		BriefID string `json:"brief_id"`
	} `json:"latest_card"`
}

// shown is deal show's result.
type shown struct {
	Deal dealResult
	Card *struct {
		BriefID string `json:"brief_id"`
		Text    string
	}
}

func text(p *string) string {
	if p == nil {
		return "null"
	}
	return *p
}

// The owner registers a deal by hand with deal new: the account and its opportunities are read through the
// CRM, each under a receipt of its own, and the deal binds what they returned under the ID
// thornfield-2027-2e3hke. brief then makes the deal's card, which deal show gives, and a closed deal's account
// gets its own card again. A second open deal of one account is refused, and the account's next deal of the
// year takes the next number. A person granted the account afterwards reads both receipts the deal names.
func TestDealCommand(t *testing.T) {
	d := setUpDemo(t)
	if code, stdout, stderr := d.read(t, "deal", "list"); code != 0 || stderr != "" ||
		stdout != "No deals. Register one: gtm-connector deal new ACCOUNT_ID\n" {
		t.Fatalf("deal list in an empty workspace: exit %d\n%s%s", code, stdout, stderr)
	}
	d.connectDemo(t, defaultWorkspace)

	var deal dealResult
	d.result(t, &deal, "deal", "new", "acc-003")
	if deal.ID != "thornfield-2027-2e3hke" || deal.Account != "acc-003" || deal.AccountName != "Thornfield Wren LLP" ||
		deal.State != "open" || text(deal.Opportunity) != "opp-003" || deal.Contract != nil ||
		text(deal.RenewsOn) != "2027-02-08" || deal.RegisteredBy != nil || deal.ClosedAt != nil || deal.LatestCard != nil {
		t.Fatalf("deal new acc-003: %+v", deal)
	}
	// Each read is a tool call under its own receipt, which binds the records the deal names, and names no
	// principal: the owner made it.
	for tool, receipt := range map[string]string{"get_account": deal.Reads.GetAccount,
		"list_opportunities": deal.Reads.ListOpportunities} {
		if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND tool = $2 AND status = 'ok'
			AND principal_id IS NULL AND brief_id LIKE 'deal-%' AND requested_account = 'acc-003'`, receipt, tool); n != 1 {
			t.Errorf("the %s read of the deal: %s", tool, receipt)
		}
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND rows ? 'crm/opportunities/opp-003'`,
		deal.Reads.ListOpportunities); n != 1 {
		t.Errorf("the opportunities read did not return opp-003")
	}
	principal, err := d.owner.DemoPrincipal(ctx, demoLogin)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, principal.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	app, err := postgres.Open(ctx, d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	browser := signInToDemo(t, app)
	for _, receipt := range []string{deal.Reads.GetAccount, deal.Reads.ListOpportunities} {
		var read struct{ Account string }
		if err := json.Unmarshal(browser.get("/api/v1/receipts/"+receipt), &read); err != nil || read.Account != "acc-003" {
			t.Errorf("GET /api/v1/receipts/%s: %+v %v", receipt, read, err)
		}
	}

	// One open deal per account.
	if _, message, next := d.refusal(t, 3, "deal", "new", "acc-003"); !strings.Contains(message, "open deal") ||
		next != "gtm-connector deal list" {
		t.Errorf("a second deal of acc-003: %s (%s)", message, next)
	}
	// An account the CRM does not hold, and one of the wrong form, register nothing.
	if _, message, _ := d.refusal(t, 2, "deal", "new", "acc-999"); message != "account not found: acc-999" {
		t.Errorf("deal new acc-999: %s", message)
	}
	if code, _, _ := d.read(t, "deal", "new", "acc-sf-001000000000001AAA", "--json"); code != 2 {
		t.Errorf("deal new with a Salesforce ID on the demo book: exit %d", code)
	}

	// brief makes the open deal's card, and says so.
	code, stdout, stderr := d.read(t, "brief", "acc-003")
	if code != 0 || stderr != "Sources: CRM, calls, and Slack are demo data.\nDeal: thornfield-2027-2e3hke.\n" {
		t.Fatalf("brief acc-003: exit %d\n%s", code, stderr)
	}
	var made shown
	d.result(t, &made, "deal", "show", "thornfield-2027-2e3hke")
	if made.Card == nil || made.Deal.LatestCard == nil || made.Card.BriefID != made.Deal.LatestCard.BriefID ||
		!strings.Contains(stdout, "Brief: "+made.Card.BriefID+"\n") ||
		!strings.HasPrefix(made.Card.Text, `DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15`) {
		t.Fatalf("deal show after brief: %+v\n%s", made, stdout)
	}
	code, stdout, _ = d.read(t, "deal", "show", "thornfield-2027-2e3hke")
	if code != 0 || !strings.HasPrefix(stdout, "ID\tthornfield-2027-2e3hke\nAccount\tacc-003\nName\t\"Thornfield Wren LLP\"\n"+
		"State\topen\nOpportunity\topp-003\nContract\tnone\nRenews\t2027-02-08\n\n"+made.Card.Text) {
		t.Errorf("deal show as text:\n%s", stdout)
	}

	// Closing keeps the deal and its card; the account's next card is its own, with the same text.
	var closed dealResult
	d.result(t, &closed, "deal", "close", "thornfield-2027-2e3hke")
	if closed.State != "closed" || closed.ClosedAt == nil || closed.LatestCard == nil {
		t.Fatalf("deal close: %+v", closed)
	}
	var again dealResult
	d.result(t, &again, "deal", "close", "thornfield-2027-2e3hke")
	if again.State != "closed" || text(again.ClosedAt) != text(closed.ClosedAt) {
		t.Errorf("closing twice: %+v", again)
	}
	code, _, stderr = d.read(t, "brief", "acc-003")
	if code != 0 || stderr != "Sources: CRM, calls, and Slack are demo data.\n" {
		t.Errorf("brief after the deal closed: exit %d\n%s", code, stderr)
	}
	var account struct{ Text string }
	d.result(t, &account, "brief", "acc-003")
	if n := d.count(t, "SELECT count(*) FROM deal_cards"); n != 1 || d.count(t, "SELECT count(*) FROM cards") != 3 {
		t.Errorf("%d of the account's cards belong to a deal", n)
	}
	// Apart from when the sources were read, the two cards are the same text.
	unread := func(text string) string {
		lines := strings.Split(text, "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[1], "Sources read ") {
			t.Fatalf("a card without its read times:\n%s", text)
		}
		return strings.Join(append(lines[:1:1], lines[2:]...), "\n")
	}
	if unread(account.Text) != unread(made.Card.Text) {
		t.Errorf("the deal's card and the account's card differ:\n%s\n%s", made.Card.Text, account.Text)
	}

	// The account's next deal of the same year takes the next number.
	var next dealResult
	d.result(t, &next, "deal", "new", "acc-003")
	if next.ID != "thornfield-2027-2e3hke-2" || next.State != "open" {
		t.Errorf("the next deal of acc-003: %+v", next)
	}
	var listed []dealResult
	d.result(t, &listed, "deal", "list")
	if len(listed) != 2 || listed[0].ID != "thornfield-2027-2e3hke-2" || listed[1].ID != "thornfield-2027-2e3hke" {
		t.Errorf("deal list: %+v", listed)
	}
	code, stdout, _ = d.read(t, "deal", "list")
	if code != 0 || stdout != "thornfield-2027-2e3hke-2\tacc-003\tThornfield Wren LLP\topen\t2027-02-08\n"+
		"thornfield-2027-2e3hke\tacc-003\tThornfield Wren LLP\tclosed\t2027-02-08\n" {
		t.Errorf("deal list as text:\n%s", stdout)
	}
	if _, message, _ := d.refusal(t, 2, "deal", "show", "thornfield"); !strings.Contains(message, "thornfield-2027-2e3hke") {
		t.Errorf("deal show of a malformed ID: %s", message)
	}
	if _, message, _ := d.refusal(t, 2, "deal", "close", "alder-2027-he2vbf"); message != "No deal of this workspace has that ID." {
		t.Errorf("deal close of an unknown ID: %s", message)
	}
}

// The ID is the first word of the account's name that has an ASCII letter or digit once its accents are
// off, and the year.
func TestDealWord(t *testing.T) {
	for name, want := range map[string]string{
		"Thornfield Wren LLP":   "thornfield",
		"Égal Systèmes":         "egal",
		"  O'Brien & Sons ":     "obrien",
		"& 3M":                  "3m",
		"株式会社":                  "deal",
		strings.Repeat("a", 60): strings.Repeat("a", 40),
	} {
		if got := dealWord(name); got != want {
			t.Errorf("dealWord(%q) = %q, not %q", name, got, want)
		}
	}
}

// A deal binds the contract a card counts to and the open opportunity that closes first, each by ID on a
// tie, and renews on the account's renewal date, the contract's end or the opportunity's close date.
func TestRenewalOf(t *testing.T) {
	record := func(ref string, fields ...any) *strictjson.Object {
		return strictjson.NewObject(append(fields, "record_ref", ref)...)
	}
	account := []*strictjson.Object{record("crm/accounts/acc-sf-1", "name", "Invented"),
		record("crm/contracts/ctr-b", "end_date", "2028-03-01"), record("crm/contracts/ctr-a", "end_date", "2028-03-01"),
		record("crm/contracts/ctr-c", "end_date", "2027-01-14"), record("crm/contracts/ctr-d", "end_date", nil)}
	opportunities := []*strictjson.Object{record("crm/opportunities/opp-0", "close_date", nil, "is_closed", false),
		record("crm/opportunities/opp-3", "close_date", "2027-02-10", "is_closed", false),
		record("crm/opportunities/opp-1", "close_date", "2027-02-10", "is_closed", false),
		record("crm/opportunities/opp-2", "close_date", "2027-01-20", "is_closed", true)}
	got := renewalOf(account, opportunities, "2027-01-15")
	if got.ContractRef != "ctr-a" || got.OpportunityRef != "opp-1" || got.RenewsOn != "2028-03-01" || got.Year != 2028 {
		t.Errorf("on Salesforce: %+v", got)
	}
	demo := []*strictjson.Object{record("crm/accounts/acc-001", "renewal_date", "2027-02-01")}
	staged := []*strictjson.Object{record("crm/opportunities/opp-9", "close_date", "2026-12-01", "stage", "closed_won"),
		record("crm/opportunities/opp-8", "close_date", "2027-01-25", "stage", "discovery")}
	if got := renewalOf(demo, staged, "2027-01-15"); got.OpportunityRef != "opp-8" || got.RenewsOn != "2027-02-01" ||
		got.ContractRef != "" || got.Year != 2027 {
		t.Errorf("on the demo book: %+v", got)
	}
	if got := renewalOf(account[:1], nil, "2026-11-21"); got.RenewsOn != "" || got.Year != 2026 ||
		got.OpportunityRef != "" || got.ContractRef != "" {
		t.Errorf("with no date: %+v", got)
	}
}

// The routes serve deals to a person signed in, by the grants they hold: the list holds the deals of the
// granted accounts only, a deal of another account answers 403 as an unknown one does, and registering for
// an account without a grant is refused under the read's receipt, with nothing read. A deal registered
// through the API names its principal on the deal and on both receipts.
func TestDealRoutesKeepTheGrants(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
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
	digest := sha256.Sum256([]byte("outsider-password-0123456789-abcdef"))
	outsider, err := d.owner.EnsureDemoPrincipal(ctx, d.workspace, "demo-outsider", "Demo outsider", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, outsider.ID, "acc-001"); err != nil {
		t.Fatal(err)
	}
	var owned dealResult
	d.result(t, &owned, "deal", "new", "acc-003")
	if code, _, stderr := d.read(t, "brief", "acc-003"); code != 0 {
		t.Fatalf("brief: %s", stderr)
	}

	b := signInToDemo(t, app)
	write := func(account string, header map[string]string) *http.Response {
		body, _ := json.Marshal(map[string]string{"account": account})
		sent := map[string]string{"Origin": b.origin, "X-CSRF-Token": b.csrf, "Content-Type": "application/json"}
		for name, value := range header {
			sent[name] = value
		}
		return b.send(http.MethodPost, "/v1/deals", body, sent)
	}
	reply := write("acc-002", nil)
	var registered struct{ Deal dealResult }
	b.json(reply, &registered)
	if reply.StatusCode != http.StatusCreated || registered.Deal.ID != "copper-2027-b6ce6n" ||
		text(registered.Deal.RegisteredBy) != principal.ID || text(registered.Deal.Opportunity) != "opp-002" {
		t.Fatalf("POST /v1/deals: %d %+v", reply.StatusCode, registered)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id IN ($1, $2) AND status = 'ok' AND principal_id = $3`,
		registered.Deal.Reads.GetAccount, registered.Deal.Reads.ListOpportunities, principal.ID); n != 2 {
		t.Errorf("%d of the deal's reads name its principal", n)
	}
	if reply := write("acc-002", nil); reply.StatusCode != http.StatusConflict {
		t.Errorf("a second deal of acc-002: %d", reply.StatusCode)
	}
	if reply := write("acc-004", map[string]string{"X-CSRF-Token": "stale"}); reply.StatusCode != http.StatusForbidden {
		t.Errorf("a write without the CSRF token: %d", reply.StatusCode)
	}
	if reply := write("acc-004", map[string]string{"Origin": "https://elsewhere.example"}); reply.StatusCode !=
		http.StatusForbidden {
		t.Errorf("a write from another origin: %d", reply.StatusCode)
	}
	if reply := write("", nil); reply.StatusCode != http.StatusBadRequest {
		t.Errorf("a registration without an account: %d", reply.StatusCode)
	}
	// An account granted in the workspace that the CRM does not hold: the read fails under its receipt.
	if err := d.owner.EnsureAccount(ctx, d.workspace.ID, "acc-099", "Invented account"); err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, principal.ID, "acc-099"); err != nil {
		t.Fatal(err)
	}
	if reply := write("acc-099", nil); reply.StatusCode != http.StatusBadGateway {
		t.Errorf("a registration of an account the CRM does not hold: %d", reply.StatusCode)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE requested_account = 'acc-099' AND tool = 'get_account'
		AND status = 'error' AND principal_id = $1`, principal.ID); n != 1 {
		t.Errorf("the failed read of acc-099 has %d receipts", n)
	}
	if reply := b.send(http.MethodGet, "/v1/deals", nil, map[string]string{"Authorization": "Bearer x"}); reply.StatusCode !=
		http.StatusUnauthorized || reply.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("a bearer token on /v1/deals: %d", reply.StatusCode)
	}

	var listed struct{ Deals []dealResult }
	if err := json.Unmarshal(b.get("/v1/deals"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Deals) != 2 || listed.Deals[0].ID != "copper-2027-b6ce6n" || listed.Deals[1].ID != "thornfield-2027-2e3hke" {
		t.Fatalf("GET /v1/deals: %+v", listed)
	}
	var one struct {
		Deal dealResult
		Card *struct {
			ID    string
			Lines []json.RawMessage
			Units []json.RawMessage
		}
	}
	if err := json.Unmarshal(b.get("/v1/deals/thornfield-2027-2e3hke"), &one); err != nil {
		t.Fatal(err)
	}
	if one.Deal.ID != "thornfield-2027-2e3hke" || one.Card == nil || one.Deal.LatestCard == nil ||
		one.Card.ID != one.Deal.LatestCard.ID || len(one.Card.Lines) == 0 || len(one.Card.Units) == 0 {
		t.Fatalf("GET /v1/deals/thornfield-2027-2e3hke: %+v", one)
	}
	// The card is the one GET /api/v1/cards/{card} serves, field for field.
	var direct, nested map[string]json.RawMessage
	json.Unmarshal(b.get("/api/v1/cards/"+one.Card.ID), &direct)
	var whole map[string]json.RawMessage
	json.Unmarshal(b.get("/v1/deals/thornfield-2027-2e3hke"), &whole)
	json.Unmarshal(whole["card"], &nested)
	if len(direct) == 0 || len(direct) != len(nested) {
		t.Errorf("the deal's card and the card route differ: %d and %d fields", len(direct), len(nested))
	}
	for key, value := range direct {
		if string(nested[key]) != string(value) {
			t.Errorf("card field %s differs", key)
		}
	}

	// The outsider holds a grant on acc-001 alone.
	o := signInAs(t, app, "demo-outsider", "outsider-password-0123456789-abcdef")
	if err := json.Unmarshal(o.get("/v1/deals"), &listed); err != nil || len(listed.Deals) != 0 {
		t.Errorf("the outsider's deals: %+v %v", listed, err)
	}
	for _, path := range []string{"/v1/deals/thornfield-2027-2e3hke", "/v1/deals/copper-2027-b6ce6n", "/v1/deals/nothing-2027",
		"/v1/deals/not%20an%20id"} {
		reply := o.send(http.MethodGet, path, nil, nil)
		body, _ := io.ReadAll(reply.Body)
		reply.Body.Close()
		if reply.StatusCode != http.StatusForbidden || !strings.Contains(string(body), `"not_permitted"`) {
			t.Errorf("the outsider's GET %s: %d %s", path, reply.StatusCode, body)
		}
	}
	before := d.count(t, "SELECT count(*) FROM receipts WHERE principal_id = $1", outsider.ID)
	body, _ := json.Marshal(map[string]string{"account": "acc-003"})
	reply = o.send(http.MethodPost, "/v1/deals", body, map[string]string{"Origin": o.origin, "X-CSRF-Token": o.csrf,
		"Content-Type": "application/json"})
	reply.Body.Close()
	if reply.StatusCode != http.StatusForbidden {
		t.Errorf("the outsider's POST for acc-003: %d", reply.StatusCode)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'get_account'
		AND status = 'error' AND http_status = 403 AND row_count = 0`, outsider.ID); n != before+1 {
		t.Errorf("the refused registration left %d refused receipts", n-before)
	}
	if n := d.count(t, "SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'list_opportunities'",
		outsider.ID); n != 0 {
		t.Errorf("a refused registration read on")
	}
	// The outsider registers for the account it holds, and sees that deal alone.
	body, _ = json.Marshal(map[string]string{"account": "acc-001"})
	reply = o.send(http.MethodPost, "/v1/deals", body, map[string]string{"Origin": o.origin, "X-CSRF-Token": o.csrf,
		"Content-Type": "application/json"})
	reply.Body.Close()
	if err := json.Unmarshal(o.get("/v1/deals"), &listed); err != nil || reply.StatusCode != http.StatusCreated ||
		len(listed.Deals) != 1 || listed.Deals[0].ID != "alder-2027-he2vbf" {
		t.Errorf("the outsider's own deal: %d %+v %v", reply.StatusCode, listed, err)
	}

	// A deal is read by its account's grants, whoever registered it. A revoked grant takes the account's
	// deals out of the list and refuses each one.
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.get("/v1/deals"), &listed); err != nil || len(listed.Deals) != 2 ||
		listed.Deals[0].ID != "alder-2027-he2vbf" || listed.Deals[1].ID != "copper-2027-b6ce6n" {
		t.Errorf("GET /v1/deals after a revocation: %+v %v", listed, err)
	}
	if reply := b.send(http.MethodGet, "/v1/deals/thornfield-2027-2e3hke", nil, nil); reply.StatusCode != http.StatusForbidden {
		t.Errorf("GET of a revoked account's deal: %d", reply.StatusCode)
	}
	d.everyToolIsNamed(t)
}

// On Salesforce the deal binds the account's opportunity and the contract a card counts the renewal to, and
// takes its year from that contract's end. Each read goes to the org as a tool call under its receipt.
func TestDealOnSalesforce(t *testing.T) {
	d := setUpDemo(t)
	org, _, books := inventedSalesforce(t)
	where := []string{"--state-dir", "invented-books"}
	if code, _, stderr := d.read(t, append([]string{"connect", "salesforce", "--org", salesforcetest.Alias,
		"--environment", "production"}, where...)...); code != 0 {
		t.Fatalf("connect: %s", stderr)
	}
	book := books[0]
	sent := len(org.Requests)
	var deal dealResult
	d.result(t, &deal, append([]string{"deal", "new", book.AccountID, "--as-of", "2027-01-15"}, where...)...)
	if deal.ID != "gemini-2027-rwqjal" || text(deal.RenewsOn) != "2027-02-20" || deal.Contract == nil ||
		!strings.HasPrefix(*deal.Contract, "ctr-sf-") || deal.Opportunity == nil ||
		!strings.HasPrefix(*deal.Opportunity, "opp-sf-") || deal.AccountName != "Gemini export 0: Zoom cloud recording VTT" {
		t.Fatalf("deal new on Salesforce: %+v", deal)
	}
	if len(org.Requests) == sent {
		t.Errorf("the deal read nothing from the org")
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND rows ? ('crm/contracts/' || $2)`,
		deal.Reads.GetAccount, *deal.Contract); n != 1 {
		t.Errorf("the account read did not return the contract")
	}
	code, _, stderr := d.read(t, append([]string{"brief", book.AccountID, "--as-of", "2027-01-15"}, where...)...)
	if code != 0 || !strings.HasSuffix(stderr, "Deal: gemini-2027-rwqjal.\n") {
		t.Fatalf("brief: exit %d\n%s", code, stderr)
	}
	var made shown
	d.result(t, &made, append([]string{"deal", "show", "gemini-2027-rwqjal"}, where...)...)
	if made.Card == nil || made.Deal.LatestCard == nil || made.Card.BriefID != made.Deal.LatestCard.BriefID ||
		!strings.HasPrefix(made.Card.Text, "DEAL CARD: ") {
		t.Errorf("the deal's card: %+v", made)
	}
	// With no --as-of, a deal on Salesforce plans from today's date in UTC: the contract it binds is the
	// earliest that ends on or after that day. The clock stands at an instant whose date in its own zone
	// is the day before its date in UTC. One contract ends on each of those two days and one a year on,
	// so today's UTC date binds the second, and a planning day after it binds the third.
	previous := wallClock
	wallClock = func() time.Time { return time.Date(2031, 5, 5, 23, 30, 0, 0, time.FixedZone("west", -7*3600)) }
	t.Cleanup(func() { wallClock = previous })
	dated, err := strictjson.LoadsText(`{"Account": [{"Id": "001000000000777AAA", "Name": "Dated contracts",
		"Industry": "Testing", "AccountNumber": null}], "Contact": [], "Opportunity": [], "Contract": [
		{"Id": "800000000000001AAA", "AccountId": "001000000000777AAA", "Status": "Activated", "StatusCode": "Activated",
		 "StartDate": "2030-01-01", "ContractTerm": 12, "EndDate": "2031-05-05"},
		{"Id": "800000000000002AAA", "AccountId": "001000000000777AAA", "Status": "Activated", "StatusCode": "Activated",
		 "StartDate": "2030-01-01", "ContractTerm": 12, "EndDate": "2031-05-06"},
		{"Id": "800000000000003AAA", "AccountId": "001000000000777AAA", "Status": "Activated", "StatusCode": "Activated",
		 "StartDate": "2030-01-01", "ContractTerm": 24, "EndDate": "2032-05-06"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	account := org.Add(dated.(*strictjson.Object))
	d.result(t, &deal, append([]string{"deal", "new", account}, where...)...)
	if text(deal.Contract) != "ctr-sf-800000000000002AAA" || text(deal.RenewsOn) != "2031-05-06" {
		t.Fatalf("deal new with no --as-of: %+v", deal)
	}
	d.result(t, &deal, append([]string{"deal", "close", deal.ID}, where...)...)
	d.result(t, &deal, append([]string{"deal", "new", account, "--as-of", "2031-05-07"}, where...)...)
	if text(deal.Contract) != "ctr-sf-800000000000003AAA" {
		t.Fatalf("deal new planned from the day after: %+v", deal)
	}
	d.everyToolIsNamed(t)
}

// hookedCRM serves the demo book through a CRM that calls hook before each read, until the test ends.
func hookedCRM(t *testing.T, hook func(tool, account string)) {
	t.Helper()
	kind := kindsNamed("demo")[0]
	saved := *kind
	t.Cleanup(func() { *kind = saved })
	kind.crm = func(_ context.Context, o opening) (crmSource, error) {
		book, err := openedDemo(o)
		return hooked{book, hook}, err
	}
}

type hooked struct {
	demoBook
	hook func(tool, account string)
}

func (h hooked) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	h.hook(tool, accountID)
	return h.demoBook.Read(ctx, tool, accountID)
}

// register asks to register a deal for an account, as the signed-in person.
func (b *signedIn) register(account string) (status int, body string) {
	b.t.Helper()
	sent, _ := json.Marshal(map[string]string{"account": account})
	reply := b.send(http.MethodPost, "/v1/deals", sent, map[string]string{"Origin": b.origin, "X-CSRF-Token": b.csrf,
		"Content-Type": "application/json"})
	defer reply.Body.Close()
	read, _ := io.ReadAll(reply.Body)
	return reply.StatusCode, string(read)
}

// A grant revoked while the account is read stops the registration at the next read: that read is refused
// under its receipt as error/403, the CRM is not asked again, and no deal is saved.
func TestRegistrationReadsOnlyUnderALiveGrant(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
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
	var asked []string
	hookedCRM(t, func(tool, account string) {
		asked = append(asked, tool+" "+account)
		if tool == "get_account" {
			if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal.ID, account); err != nil {
				t.Error(err)
			}
		}
	})
	b := signInToDemo(t, app)
	if status, body := b.register("acc-005"); status != http.StatusForbidden || !strings.Contains(body, "not_permitted") {
		t.Fatalf("a registration whose grant was revoked during its first read: %d %s", status, body)
	}
	if strings.Join(asked, ", ") != "get_account acc-005" {
		t.Errorf("the CRM was asked for %q", asked)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'list_opportunities'
		AND requested_account = 'acc-005' AND status = 'error' AND http_status = 403 AND row_count = 0`,
		principal.ID); n != 1 {
		t.Errorf("%d refused receipts of the second read", n)
	}
	if n := d.count(t, "SELECT count(*) FROM deals"); n != 0 {
		t.Errorf("%d deals saved", n)
	}
}

// With the CRM detached, a person granted the account is told so, with no receipt to point at, and one
// without a grant is refused under a receipt, as with a CRM.
func TestRegistrationWithoutACRM(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := d.read(t, "connect", "demo", "--remove"); code != 0 {
		t.Fatalf("connect demo --remove: %s", stderr)
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
	if err := d.owner.RevokeAccountGrant(ctx, d.workspace.ID, principal.ID, "acc-004"); err != nil {
		t.Fatal(err)
	}
	b := signInToDemo(t, app)
	before := d.count(t, "SELECT count(*) FROM receipts")
	if status, body := b.register("acc-003"); status != http.StatusConflict || !strings.Contains(body, "crm_not_connected") ||
		strings.Contains(body, "receipt") {
		t.Errorf("a registration with no CRM: %d %s", status, body)
	}
	if n := d.count(t, "SELECT count(*) FROM receipts"); n != before {
		t.Errorf("a registration with no CRM wrote %d receipts", n-before)
	}
	if status, _ := b.register("acc-004"); status != http.StatusForbidden {
		t.Errorf("an ungranted registration with no CRM: %d", status)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'get_account'
		AND requested_account = 'acc-004' AND connection_id IS NULL AND status = 'error' AND http_status = 403`,
		principal.ID); n != 1 {
		t.Errorf("the ungranted registration left %d refused receipts", n)
	}
}

// A deal registered while brief reads its account still gets the card: the open deal is found when the card
// is published.
func TestADealOpenedDuringBriefGetsItsCard(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	opened := false
	hookedCRM(t, func(tool, account string) {
		if tool == "list_opportunities" && !opened {
			opened = true
			if code, _, stderr := d.read(t, "deal", "new", account); code != 0 {
				t.Errorf("deal new during brief: %s", stderr)
			}
		}
	})
	code, stdout, stderr := d.read(t, "brief", "acc-003")
	if code != 0 || !opened || stderr != "Sources: CRM, calls, and Slack are demo data.\nDeal: thornfield-2027-2e3hke.\n" {
		t.Fatalf("brief: exit %d\n%s", code, stderr)
	}
	var made shown
	d.result(t, &made, "deal", "show", "thornfield-2027-2e3hke")
	if made.Card == nil || !strings.Contains(stdout, "Brief: "+made.Card.BriefID+"\n") {
		t.Errorf("the deal's card: %+v", made.Card)
	}
}

// POST /v1/deals/{id}/refresh reads every source of an open deal again as the signed-in person and publishes
// a new card for the deal: each read and the seal name the person, the answer is the deal as GET gives it
// with the new card, and the card before it stays. A person with no live grant on the deal's account, an
// unknown deal and a write without the CSRF token are refused and publish nothing, and a closed deal is not
// refreshed.
func TestRefreshPublishesANewCardForTheDeal(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
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
	digest := sha256.Sum256([]byte("outsider-password-0123456789-abcdef"))
	outsider, err := d.owner.EnsureDemoPrincipal(ctx, d.workspace, "demo-outsider", "Demo outsider", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := d.owner.GrantAccount(ctx, d.workspace.ID, outsider.ID, "acc-001"); err != nil {
		t.Fatal(err)
	}
	var deal dealResult
	d.result(t, &deal, "deal", "new", "acc-003")
	b := signInToDemo(t, app)
	refresh := func(who *signedIn, id string, header map[string]string) (*http.Response, []byte) {
		sent := map[string]string{"Origin": who.origin, "X-CSRF-Token": who.csrf}
		for name, value := range header {
			sent[name] = value
		}
		reply := who.send(http.MethodPost, "/v1/deals/"+id+"/refresh", nil, sent)
		body, _ := io.ReadAll(reply.Body)
		reply.Body.Close()
		return reply, body
	}
	cards := func() int { return d.count(t, `SELECT count(*) FROM deal_cards WHERE deal_ref = $1`, deal.ID) }
	type refreshed struct {
		Deal dealResult
		Card *struct {
			ID    string
			Units []json.RawMessage
		}
	}
	// The deal has no card yet: the refresh makes its first.
	reply, body := refresh(b, deal.ID, nil)
	var first refreshed
	if err := json.Unmarshal(body, &first); reply.StatusCode != http.StatusOK || err != nil || first.Card == nil ||
		first.Deal.LatestCard == nil || first.Card.ID != first.Deal.LatestCard.ID || len(first.Card.Units) == 0 ||
		reply.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the first refresh: %d %s", reply.StatusCode, body)
	}
	// Every read the card rests on names the person who asked, and so does its seal.
	if n := d.count(t, `SELECT count(*) FILTER (WHERE r.principal_id = $2 AND r.status = 'ok') * 1000 + count(*)
		FROM card_reads cr JOIN cards c ON c.id = cr.card_id JOIN receipts r ON r.id = cr.receipt_id
		WHERE c.id = $1`, first.Card.ID, principal.ID); n < 3003 || n/1000 != n%1000 {
		t.Errorf("the reads of the refreshed card that name the person, and all of them: %d", n)
	}
	if n := d.count(t, `SELECT count(*) FROM cards c JOIN receipts r ON r.id = c.seal_receipt_id
		WHERE c.id = $1 AND r.tool = 'save_brief' AND r.status = 'ok' AND r.principal_id = $2`, first.Card.ID,
		principal.ID); n != 1 {
		t.Errorf("the seal of the refreshed card names the person: %d", n)
	}
	// A second refresh publishes another card, and the first stays the deal's.
	reply, body = refresh(b, deal.ID, nil)
	var second refreshed
	if err := json.Unmarshal(body, &second); reply.StatusCode != http.StatusOK || err != nil || second.Card == nil ||
		second.Card.ID == first.Card.ID || cards() != 2 {
		t.Fatalf("the second refresh: %d, %d cards\n%s", reply.StatusCode, cards(), body)
	}
	// Refusals publish nothing, for this deal or any other.
	every := func() int { return d.count(t, `SELECT count(*) FROM cards`) }
	before := every()
	o := signInAs(t, app, "demo-outsider", "outsider-password-0123456789-abcdef")
	for name, c := range map[string]struct {
		who    *signedIn
		id     string
		header map[string]string
		status int
		code   string
	}{
		"no grant on the deal's account": {o, deal.ID, nil, http.StatusForbidden, "not_permitted"},
		"an unknown deal":                {o, "nothing-2027-aaaaaa", nil, http.StatusForbidden, "not_permitted"},
		"what is no deal ID":             {o, "not%20an%20id", nil, http.StatusForbidden, "not_permitted"},
		"an ID that holds a NUL":         {o, "nothing%00", nil, http.StatusForbidden, "not_permitted"},
		"no CSRF token":                  {b, deal.ID, map[string]string{"X-CSRF-Token": "stale"}, http.StatusForbidden, "csrf_invalid"},
		"another origin":                 {b, deal.ID, map[string]string{"Origin": "https://elsewhere.example"}, http.StatusForbidden, ""},
	} {
		reply, body := refresh(c.who, c.id, c.header)
		var refused struct{ Error struct{ Code string } }
		json.Unmarshal(body, &refused)
		if reply.StatusCode != c.status || c.code != "" && refused.Error.Code != c.code || every() != before {
			t.Errorf("%s: %d %s, %d cards, %d before", name, reply.StatusCode, body, every(), before)
		}
	}
	// A deal the person holds no grant on and one that does not exist are refused alike, each under one
	// get_account receipt that names the person, finished as error/403 with no rows. Only the first names an
	// account, and what is no deal ID is not kept as it was sent.
	for arguments, account := range map[string]string{
		`{"deal": "` + deal.ID + `"}`:     "acc-003",
		`{"deal": "nothing-2027-aaaaaa"}`: "",
		`{"deal": "<invalid>"}`:           "",
	} {
		want := 1
		if arguments == `{"deal": "<invalid>"}` {
			want = 2
		}
		if n := d.count(t, `SELECT count(*) FROM receipts WHERE principal_id = $1 AND tool = 'get_account'
			AND status = 'error' AND http_status = 403 AND row_count = 0 AND connection_id IS NULL
			AND arguments::jsonb = $2::jsonb AND coalesce(requested_account, '') = $3`, outsider.ID, arguments, account); n != want {
			t.Errorf("%d receipts of the outsider's refused refresh of %s", n, arguments)
		}
	}
	// A closed deal is not refreshed.
	d.result(t, &deal, "deal", "close", deal.ID)
	reply, body = refresh(b, deal.ID, nil)
	var closed struct{ Error struct{ Code string } }
	json.Unmarshal(body, &closed)
	if reply.StatusCode != http.StatusConflict || closed.Error.Code != "deal_closed" || cards() != 2 {
		t.Errorf("a refresh of a closed deal: %d %s", reply.StatusCode, body)
	}
	d.everyToolIsNamed(t)
}

// A refreshed card is the deal's it was asked for, or is not published. A deal that closed before the card
// was read, one that closes while it is read, and one that closes while another opens, each publish nothing.
func TestARefreshedCardIsOnlyTheDealsAskedFor(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	principal, err := d.owner.DemoPrincipal(ctx, demoLogin)
	if err != nil {
		t.Fatal(err)
	}
	every := func() int { return d.count(t, `SELECT count(*) FROM cards`) }
	// publish makes the card as a refresh of the deal does, running during once, before its first read.
	publish := func(only string, during func()) error {
		t.Helper()
		from, err := workspaceReads(ctx, d.owner, d.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := from.open(ctx, "crm", "calls", "slack"); err != nil {
			t.Fatal(err)
		}
		scopes := []string{"crm:read", "calls:read", "slack:read"}
		_, err = from.publish(ctx, cards.Principal{WorkspaceID: d.workspace.ID, PrincipalID: principal.ID,
			TokenID: receipts.Fingerprint(randomToken()), Scopes: scopes, Verify: func() ([]string, bool) {
				if during != nil {
					during()
					during = nil
				}
				return scopes, true
			}}, "acc-003", from.day(), only)
		return err
	}
	var first, second, third dealResult
	d.result(t, &first, "deal", "new", "acc-003")
	if err := publish(first.ID, nil); err != nil {
		t.Fatalf("a refresh of the open deal: %v", err)
	}
	before := every()
	// The deal closed, and the account's next one is open.
	d.result(t, &first, "deal", "close", first.ID)
	d.result(t, &second, "deal", "new", "acc-003")
	if err := publish(first.ID, nil); !errors.Is(err, postgres.ErrDealChanged) || every() != before {
		t.Errorf("a card for a deal that closed before it was read: %v, %d cards", err, every()-before)
	}
	// The deal closes while its card is read, and the account has none open as the card is published.
	closing := func() {
		if _, _, err := d.owner.CloseDeal(ctx, d.workspace.ID, second.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := publish(second.ID, closing); !errors.Is(err, postgres.ErrDealChanged) || every() != before {
		t.Errorf("a card for a deal that closed while it was read: %v, %d cards", err, every()-before)
	}
	// The deal closes while its card is read, and the account's next one is open as the card is published.
	d.result(t, &third, "deal", "new", "acc-003")
	var fourth dealResult
	swapping := func() {
		d.result(t, &third, "deal", "close", third.ID)
		d.result(t, &fourth, "deal", "new", "acc-003")
	}
	if err := publish(third.ID, swapping); !errors.Is(err, postgres.ErrDealChanged) || every() != before {
		t.Errorf("a card for a deal that gave way to the next while it was read: %v, %d cards", err, every()-before)
	}
	if n := d.count(t, `SELECT count(*) FROM deal_cards WHERE deal_ref = $1`, fourth.ID); n != 0 {
		t.Errorf("the next deal got %d cards of a refresh of the one before it", n)
	}
}
