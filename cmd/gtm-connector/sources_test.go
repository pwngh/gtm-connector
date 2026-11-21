package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Every kind names one source, and opens only that source.
func TestEachSourceKindOpensItsOwnSource(t *testing.T) {
	for _, kind := range sourceKinds {
		openers := map[string]bool{"crm": kind.crm != nil, "calls": kind.calls != nil, "slack": kind.slack != nil}
		for source, has := range openers {
			if has && source != kind.source {
				t.Errorf("%s %s has the opener of %s", kind.provider, kind.source, source)
			}
		}
		if kindOf(kind.source, kind.mode) != kind || len(kindsNamed(kind.provider)) == 0 || kind.title == "" || kind.detail == "" {
			t.Errorf("%s %s is not found by its row or its name", kind.provider, kind.source)
		}
		if kind.live == (kind.mode == "demo") {
			t.Errorf("%s %s is both demo data and a real source, or neither", kind.provider, kind.source)
		}
	}
	if got := strings.Join(providerNames(), " "); got != "demo salesforce calls slack" {
		t.Errorf("connect takes %s", got)
	}
}

// inventedCRM is a CRM registered by a test: the Salesforce reader against salesforcetest's invented org.
// It signs in through the invented sf on its first read, and checks then that the org is the one its
// connection is pinned to. The invented org answers no search, so the test answers that one itself.
type inventedCRM struct {
	t      *testing.T
	d      *demoDatabase
	org    *salesforcetest.Org
	sf     *salesforcetest.SF
	pinned string // the identity digest of the connection row

	once    sync.Once
	reader  *salesforce.Reader
	failed  error
	pending []string // for each read, the tools whose receipt was pending when the read ran
}

func (c *inventedCRM) signedIn(ctx context.Context) (*salesforce.Reader, error) {
	c.once.Do(func() {
		var identity salesforce.Identity
		if c.reader, c.failed = salesforce.FromSF(ctx, salesforcetest.Alias, salesforce.Options{
			Environment: "production", Runner: c.sf, Transport: c.org}); c.failed != nil {
			return
		}
		if identity, c.failed = c.reader.PrepareConnection(ctx); c.failed == nil && get(identity.Binding(), "identity_sha256") != c.pinned {
			c.failed = &failure{code: 3, message: "The org is not the one this workspace is pinned to."}
		}
	})
	return c.reader, c.failed
}

// started notes which receipts are pending now, as the read begins.
func (c *inventedCRM) started() {
	conn := conn(c.t, c.d.ownerURL)
	var tools string
	if err := conn.QueryRow(ctx, "SELECT coalesce(string_agg(tool, ' '), '') FROM receipts WHERE status = 'pending'").
		Scan(&tools); err != nil {
		c.t.Fatal(err)
	}
	c.pending = append(c.pending, tools)
}

func (c *inventedCRM) Read(ctx context.Context, tool, accountID string) ([]*strictjson.Object, error) {
	c.started()
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, err
	}
	_, records, err := reader.Read(ctx, tool, accountID)
	if errors.Is(err, salesforce.ErrAccountNotFound) {
		err = errAccountNotFound
	}
	return records, err
}

func (c *inventedCRM) SearchAccounts(ctx context.Context, query string) ([]*strictjson.Object, bool, error) {
	c.started()
	reader, err := c.signedIn(ctx)
	if err != nil {
		return nil, false, err
	}
	account, err := reader.GetAccount(ctx, inventedAccount)
	if err != nil || !strings.Contains(cellText(get(account.Record, "name")), query) {
		return nil, false, err
	}
	return []*strictjson.Object{account.Record}, false, nil
}

// inventedAccount is the one account of the invented org: the first of the recorded Salesforce book.
const inventedAccount = "acc-sf-0012AF6DF49DA83QNG"

// A source is wired by filling its entry of sourceKinds: attach, and the opener of its source. This test
// registers a CRM that way and changes nothing else. connect then attaches it, status shows it, accounts,
// inspect and brief read it under receipts committed before each read, the seller list reaches the card,
// and removing it leaves a workspace that reads nothing, never the demo data.
func TestASourceRegisteredInItsKindIsReadByEveryCommand(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "invented-books"}
	connect := append([]string{"connect", "salesforce", "--org", salesforcetest.Alias, "--seller", "Morgan Reed",
		"--seller", "U0731P9Q2R"}, where...)
	if _, message, _ := d.refusal(t, 3, connect...); !strings.Contains(message, "not available in this build") {
		t.Fatalf("before the source is registered: %s", message)
	}

	crm := &inventedCRM{t: t, d: d, org: salesforcetest.NewOrg(), sf: salesforcetest.NewSF()}
	var input struct {
		Input struct {
			Rows json.RawMessage `json:"org_rows"`
		}
	}
	if err := json.Unmarshal(oracletest.Lines(t, "cards/books_salesforce.jsonl.gz")[0], &input); err != nil {
		t.Fatal(err)
	}
	rows, err := strictjson.Loads(input.Input.Rows)
	if err != nil || crm.org.Add(rows.(*strictjson.Object)) != inventedAccount {
		t.Fatalf("the recorded org rows are not of %s: %v", inventedAccount, err)
	}
	opened := 0
	kind := kindsNamed("salesforce")[0]
	saved := *kind
	t.Cleanup(func() { *kind = saved })
	kind.attach = func(ctx context.Context, a attaching) (attached, error) {
		alias, given := a.args.text("--org")
		if !given {
			return attached{}, refused("connect salesforce needs --org ALIAS.")
		}
		reader, err := salesforce.FromSF(ctx, alias, salesforce.Options{Environment: "production", Runner: crm.sf,
			Transport: crm.org})
		if err != nil {
			return attached{}, err
		}
		identity, err := reader.PrepareConnection(ctx)
		digest, _ := get(identity.Binding(), "identity_sha256").(string)
		return attached{binding: strictjson.NewObject("org", alias, "environment", "production"), identity: digest}, err
	}
	kind.crm = func(_ context.Context, o opening) (connected[crmSource], error) {
		// The opener reads the row and nothing else: the reader signs in on its first read.
		opened++
		crm.pinned = o.connection.IdentitySHA256
		return connected[crmSource]{reads: crm, backend: "salesforce", described: "Salesforce (" +
			cellText(get(o.binding, "environment")) + "), identity verified",
			binding: strictjson.NewObject("schema_version", 1, "crm_backend", "salesforce",
				"identity_sha256", o.connection.IdentitySHA256)}, nil
	}

	var attached, status connectResult
	d.result(t, &attached, connect...)
	d.result(t, &status, append([]string{"connect"}, where...)...)
	source := status.Sources["crm"]
	if attached.Status != "connected" || len(status.Sources) != 1 || source["provider"] != "salesforce" ||
		source["org"] != salesforcetest.Alias || source["environment"] != "production" ||
		strings.Join(status.Sellers, ", ") != "Morgan Reed, U0731P9Q2R" {
		t.Fatalf("connected %+v, then status %+v", attached, status)
	}
	// The rows hold the alias and a digest. They hold no credential, org ID or address.
	var stored string
	if err := conn(t, d.ownerURL).QueryRow(ctx, `SELECT string_agg(c::text, ' ') || ' ' || string_agg(w::text, ' ')
		FROM connections c JOIN workspaces w ON w.id = c.workspace_id WHERE w.slug = 'invented-books' AND w.kind = 'live'
		 AND c.mode = 'sf'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{salesforcetest.Token, salesforcetest.OrganizationID, "salesforce.com"} {
		if strings.Contains(stored, secret) {
			t.Errorf("the saved rows hold %q", secret)
		}
	}
	_, stdout, _ := d.read(t, append([]string{"connect"}, where...)...)
	if stdout != "crm\tconnected\tSalesforce\ncalls\tnot connected\t\nslack\tnot connected\t\n"+
		"Sellers: \"Morgan Reed\", \"U0731P9Q2R\"\n" {
		t.Errorf("connect status:\n%s", stdout)
	}
	// Status opened nothing and no login ran for it: connect ran the two login commands once.
	if opened != 0 || len(crm.sf.Commands) != 2 {
		t.Errorf("status opened the source %d times, and sf ran %d commands", opened, len(crm.sf.Commands))
	}
	// The demo data never joins a real source, and a source with no entry filled stays refused.
	if _, message, _ := d.refusal(t, 3, append([]string{"connect", "demo"}, where...)...); !strings.Contains(message, "real sources") {
		t.Errorf("connect demo beside the registered CRM: %s", message)
	}
	if _, message, _ := d.refusal(t, 3, append([]string{"connect", "calls"}, where...)...); !strings.Contains(message, "not available") {
		t.Errorf("connect calls: %s", message)
	}

	code, stdout, stderr := d.read(t, append([]string{"accounts", "--query", "Gemini"}, where...)...)
	if code != 0 || stderr != "Sources: CRM is Salesforce (production), identity verified.\n" ||
		!strings.HasPrefix(stdout, inventedAccount+"\tGemini export 0: Zoom cloud recording VTT\t") {
		t.Fatalf("accounts: exit %d\n%s%s", code, stdout, stderr)
	}
	var account struct {
		ID        string
		Contacts  []map[string]any
		Contracts []map[string]any
		Newest    *string `json:"newest_brief_id"`
	}
	d.result(t, &account, append([]string{"inspect", inventedAccount}, where...)...)
	if account.ID != inventedAccount || len(account.Contacts) != 1 || len(account.Contracts) != 1 || account.Newest != nil {
		t.Fatalf("inspect %s: %+v", inventedAccount, account)
	}
	if _, message, _ := d.refusal(t, 2, append([]string{"inspect", "acc-sf-001000000000000AAA"}, where...)...); message !=
		"account not found: acc-sf-001000000000000AAA" {
		t.Errorf("inspect of an account the org does not hold: %s", message)
	}

	code, stdout, stderr = d.read(t, append([]string{"brief", inventedAccount}, where...)...)
	lines := strings.Split(stdout, "\n")
	today := time.Now().UTC().Format("2006-01-02")
	if code != 0 || stderr != "Sources: CRM is Salesforce (production), identity verified; calls are not connected; "+
		"Slack is not connected.\n" || lines[0] != `DEAL CARD: "Gemini export 0: Zoom cloud recording VTT" (`+
		inventedAccount+`) as of `+today || !strings.Contains(stdout, "\n  Calls: not connected.\n") {
		t.Fatalf("brief: exit %d\n%s%s", code, stdout, stderr)
	}
	brief := strings.TrimPrefix(lines[len(lines)-4], "Brief: ")
	var card struct {
		AccountID string `json:"account_id"`
		Units     []any
	}
	if d.result(t, &card, append([]string{"inspect", brief}, where...)...); card.AccountID != inventedAccount || len(card.Units) == 0 {
		t.Errorf("inspect %s: %+v", brief, card)
	}
	workspace, err := d.owner.Workspace(ctx, "invented-books")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := d.owner.CardByBrief(ctx, workspace.ID, brief)
	if err != nil || !strings.Contains(string(payload.Bytes), `"sellers": [`) ||
		!strings.Contains(string(payload.Bytes), `"Morgan Reed"`) || !strings.Contains(string(payload.Bytes), `"crm_backend": "salesforce"`) {
		t.Errorf("the saved card does not hold the workspace's sellers and its CRM: %v", err)
	}

	// Every read of the source ran under a receipt of its own tool that was committed, and still pending,
	// when the read began. accounts and each inspect made one read, and brief listed the account and then
	// read it twice more for its card.
	want := []string{"search_accounts", "get_account", "get_account", "get_account", "get_account", "list_opportunities"}
	if strings.Join(crm.pending, ",") != strings.Join(want, ",") {
		t.Errorf("the receipts pending at each read: %q", crm.pending)
	}
	var receipts []receiptRow
	d.result(t, &receipts, append([]string{"audit"}, where...)...)
	tools := ""
	for _, receipt := range receipts {
		tools += receipt.Tool + ":" + receipt.Status + " "
	}
	if tools != "search_accounts:ok get_account:ok get_account:error get_account:ok get_account:ok list_opportunities:ok save_brief:ok " ||
		receipts[0].Rows[0] != "crm/accounts/"+inventedAccount || !strings.HasPrefix(*receipts[0].BriefID, "inspect-") {
		t.Errorf("the workspace's receipts: %s", tools)
	}
	// One login served every read: the reader signed in once, on the first read.
	if len(crm.sf.Commands) != 4 {
		t.Errorf("sf ran %d commands", len(crm.sf.Commands))
	}

	var removed connectResult
	d.result(t, &removed, append([]string{"connect", "salesforce", "--remove"}, where...)...)
	if _, message, _ := d.refusal(t, 2, append([]string{"accounts"}, where...)...); removed.Status != "removed" ||
		!strings.HasPrefix(message, "No CRM is connected.") {
		t.Errorf("after the removal: %+v, then accounts: %s", removed, message)
	}
}
