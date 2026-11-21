package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

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

// inventedCRM is a CRM a test registers in the salesforce entry in place of the real one, to reach inside
// the seam: it notes when it is opened and which receipts are pending at each read, and can answer a read
// itself. Otherwise it is the Salesforce reader against salesforcetest's invented org. It signs in through
// the invented sf on its first read, and checks then that the org is the one its connection is pinned to.
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

	opened  int    // how many times the opener ran
	offered string // the pin attach was last handed
	// onOpen, when set, runs as the opener opens the CRM.
	onOpen func(o opening)
	// answer, when set, answers a read in place of the org when it gives true.
	answer func(ctx context.Context, tool string) ([]*strictjson.Object, bool, error)
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
	if c.answer != nil {
		if records, answered, err := c.answer(ctx, tool); answered {
			return records, err
		}
	}
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
	if c.answer != nil {
		if records, answered, err := c.answer(ctx, "search_accounts"); answered {
			return records, false, err
		}
	}
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

// otherOrg is an alias the registered CRM attaches as a second org, with an identity of its own, without
// signing in.
const otherOrg = "invented-other-org"

// registerInventedCRM puts the invented CRM in the salesforce entry of sourceKinds until the test ends.
func registerInventedCRM(t *testing.T, d *demoDatabase) *inventedCRM {
	t.Helper()
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
	kind := kindsNamed("salesforce")[0]
	saved := *kind
	t.Cleanup(func() { *kind = saved })
	kind.attach = func(ctx context.Context, a attaching) (attached, error) {
		crm.offered = a.pinned
		alias, given := a.args.text("--org")
		if !given {
			return attached{}, refused("connect salesforce needs --org ALIAS.")
		}
		binding := strictjson.NewObject("org", alias, "environment", "production")
		if alias == otherOrg {
			return attached{binding: binding, identity: strings.Repeat("cd", 32)}, nil
		}
		reader, err := salesforce.FromSF(ctx, alias, salesforce.Options{Environment: "production", Runner: crm.sf,
			Transport: crm.org})
		if err != nil {
			return attached{}, err
		}
		identity, err := reader.PrepareConnection(ctx)
		digest, _ := get(identity.Binding(), "identity_sha256").(string)
		return attached{binding: binding, identity: digest}, err
	}
	kind.crm = func(_ context.Context, o opening) (crmSource, error) {
		// The opener reads the row and nothing else: the reader signs in on its first read.
		crm.opened++
		crm.pinned = o.connection.IdentitySHA256
		if crm.onOpen != nil {
			crm.onOpen(o)
		}
		return crm, nil
	}
	return crm
}
