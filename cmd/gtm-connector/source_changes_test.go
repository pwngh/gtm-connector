package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// connectInvented connects a workspace to the registered invented CRM.
func (d *demoDatabase) connectInvented(t *testing.T, name, alias string) {
	t.Helper()
	var attached connectResult
	if d.result(t, &attached, "connect", "salesforce", "--org", alias, "--state-dir", name); attached.Status != "connected" {
		t.Fatalf("connect salesforce --org %s: %+v", alias, attached)
	}
}

// audited is a workspace's receipts as "tool:status:http_status:error", oldest first.
func (d *demoDatabase) audited(t *testing.T, name string) string {
	t.Helper()
	var rows []receiptRow
	d.result(t, &rows, "audit", "--state-dir", name)
	var shown []string
	for _, row := range rows {
		status, code := "", ""
		if row.HTTPStatus != nil {
			status = strconv.Itoa(*row.HTTPStatus)
		}
		if row.Error != nil {
			code = *row.Error
		}
		shown = append(shown, row.Tool+":"+row.Status+":"+status+":"+code)
	}
	return strings.Join(shown, " ")
}

// A command reads a source only through the row it opened it from. When the row moves between the open
// and the read's receipt, by a reconnect or a removal, the receipt finishes as error/409 source_changed and
// the source is not read: accounts, inspect and brief each refuse.
func TestACommandReadsNothingFromASourceChangedAfterItWasOpened(t *testing.T) {
	d := setUpDemo(t)
	crm := registerInventedCRM(t, d)
	const name = "invented-books"
	d.connectInvented(t, name, salesforcetest.Alias)
	workspace, err := d.owner.Workspace(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	const changed = "A source of this workspace was connected again while it was read; nothing was published. " +
		"Run the command again."
	changes := map[string]func(o opening){
		"reconnected": func(o opening) {
			if _, err := d.owner.BumpConnectionGeneration(ctx, o.connection.ID); err != nil {
				t.Error(err)
			}
		},
		"detached": func(o opening) {
			if _, err := d.owner.UpdateSources(ctx, workspace, func([]postgres.Connection) (postgres.SourceUpdate, error) {
				return postgres.SourceUpdate{Changes: []postgres.SourceChange{{Source: "crm", Mode: "none"}}}, nil
			}); err != nil {
				t.Error(err)
			}
		},
	}
	for _, change := range []string{"reconnected", "detached"} {
		for _, tokens := range [][]string{{"accounts"}, {"inspect", inventedAccount}, {"brief", inventedAccount}} {
			crm.onOpen = changes[change]
			if _, message, _ := d.refusal(t, 3, append(tokens, "--state-dir", name)...); message != changed {
				t.Errorf("%s while %v ran: %s", change, tokens, message)
			}
			crm.onOpen = nil
			if change == "detached" {
				d.connectInvented(t, name, salesforcetest.Alias)
			}
		}
	}
	if len(crm.pending) != 0 {
		t.Errorf("the CRM was read: %q", crm.pending)
	}
	const fenced = "search_accounts:error:409:source_changed get_account:error:409:source_changed " +
		"get_account:error:409:source_changed "
	if got := d.audited(t, name); got != strings.TrimSpace(strings.Repeat(fenced, 2)) {
		t.Errorf("the receipts: %s", got)
	}
}

// A workspace stays with the CRM it was first connected to, also after the CRM is removed: another org is
// refused, attach is handed the pin, and the same org connects again. A CRM with an identity is refused in a
// workspace whose CRM row has none, as the Python package refuses a folder with no pinned connection.
func TestAWorkspaceKeepsTheCRMItWasPinnedTo(t *testing.T) {
	d := setUpDemo(t)
	crm := registerInventedCRM(t, d)
	const name = "invented-books"
	d.connectInvented(t, name, salesforcetest.Alias)
	pinned := crm.offered
	var removed connectResult
	if d.result(t, &removed, "connect", "salesforce", "--remove", "--state-dir", name); removed.Status != "removed" {
		t.Fatalf("remove: %+v", removed)
	}
	const elsewhere = "This workspace belongs to a different connection of this source, and keeps the accounts, cards " +
		"and receipts read through it. Connect the one it was first connected to, or connect this one in a " +
		"workspace of its own with --state-dir NAME. Nothing was changed."
	if _, message, _ := d.refusal(t, 3, "connect", "salesforce", "--org", otherOrg, "--state-dir", name); message != elsewhere {
		t.Errorf("another org after the removal: %s", message)
	}
	workspace, err := d.owner.Workspace(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.owner.Connections(ctx, workspace.ID)
	if err != nil || len(rows) != 1 || rows[0].Mode != "none" || rows[0].IdentitySHA256 == "" {
		t.Fatalf("the removed row: %+v %v", rows, err)
	}
	d.connectInvented(t, name, salesforcetest.Alias)
	if pinned != "" || crm.offered != rows[0].IdentitySHA256 {
		t.Errorf("attach was handed %q, then %q", pinned, crm.offered)
	}

	// A workspace whose CRM was connected with no identity, as a legacy token row is.
	legacy, err := d.owner.EnsureWorkspace(ctx, "legacy-books", "live", "Workspace legacy-books")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.owner.EnsureConnection(ctx, legacy, "crm", "env_token"); err != nil {
		t.Fatal(err)
	}
	if _, message, _ := d.refusal(t, 3, "connect", "salesforce", "--org", salesforcetest.Alias, "--state-dir",
		"legacy-books"); !strings.HasPrefix(message, "This workspace has no pinned connection of its CRM") {
		t.Errorf("a CRM over one with no pin: %s", message)
	}
	var detached connectResult
	if d.result(t, &detached, "connect", "salesforce", "--remove", "--state-dir", "legacy-books"); detached.Status != "removed" {
		t.Errorf("remove of a legacy token row: %+v", detached)
	}
}

// A command opens only the sources it reads. A stored row this build has no opener for, here imported
// calls, does not stop accounts, inspect or a card without calls, and a card that reads calls refuses it by
// name before anything is read or written.
func TestACommandOpensOnlyTheSourcesItReads(t *testing.T) {
	d := setUpDemo(t)
	crm := registerInventedCRM(t, d)
	const name = "invented-books"
	d.connectInvented(t, name, salesforcetest.Alias)
	workspace, err := d.owner.Workspace(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.owner.UpdateSources(ctx, workspace, func([]postgres.Connection) (postgres.SourceUpdate, error) {
		return postgres.SourceUpdate{Changes: []postgres.SourceChange{{Source: "calls", Mode: "imported"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	where := []string{"--state-dir", name}
	for _, tokens := range [][]string{{"accounts"}, {"inspect", inventedAccount}, {"brief", inventedAccount, "--without-calls"}} {
		if code, stdout, stderr := d.read(t, append(tokens, where...)...); code != 0 {
			t.Errorf("%v beside imported calls: exit %d\n%s%s", tokens, code, stdout, stderr)
		}
	}
	before := d.audited(t, name)
	if _, message, _ := d.refusal(t, 3, append([]string{"brief", inventedAccount}, where...)...); message !=
		"This workspace is connected to imported transcripts, which this command does not read yet." {
		t.Errorf("a card that reads calls: %s", message)
	}
	if after := d.audited(t, name); after != before {
		t.Errorf("the refused card wrote receipts:\n%s\n%s", before, after)
	}
	if crm.opened != 4 {
		t.Errorf("the CRM was opened %d times", crm.opened)
	}
}

// brief makes the maker's own checks of its request before any lookup or write: a day that is not one,
// and an account ID of the other CRM's form, are refused with nothing read and nothing written.
func TestBriefChecksItsRequestBeforeAnyRead(t *testing.T) {
	d := setUpDemo(t)
	demoWorkspace := d.connectDemo(t, "dated-books")
	crm := registerInventedCRM(t, d)
	d.connectInvented(t, "invented-books", salesforcetest.Alias)
	live, err := d.owner.Workspace(ctx, "invented-books")
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		return d.count(t, `SELECT (SELECT count(*) FROM accounts WHERE workspace_id IN ($1, $2)) +
			(SELECT count(*) FROM receipts WHERE workspace_id IN ($1, $2))`, demoWorkspace.ID, live.ID)
	}
	held := count()
	for _, c := range []struct {
		tokens []string
		want   string
	}{
		{[]string{"brief", "acc-003", "--as-of", "0000-01-01", "--state-dir", "dated-books"}, "as-of date must use YYYY-MM-DD"},
		{[]string{"brief", "acc-003", "--state-dir", "invented-books"},
			"account ID must use acc-sf- followed by the 18-character Salesforce ID"},
		{[]string{"brief", inventedAccount, "--as-of", "0000-01-01", "--state-dir", "invented-books"},
			"as-of date must use YYYY-MM-DD"},
	} {
		if _, message, _ := d.refusal(t, 2, c.tokens...); message != c.want {
			t.Errorf("%v: %s", c.tokens, message)
		}
	}
	if count() != held || len(crm.pending) != 0 {
		t.Errorf("a refused request wrote %d rows, and the CRM was read %d times", count()-held, len(crm.pending))
	}
}

// An owner read is never left pending. A caller that goes after the CRM has answered gets nothing, and the
// receipt is finished as an error on a context of its own. A get_account answer that is no account fails
// its receipt before the command says so.
func TestAnOwnerReadIsNeverLeftPending(t *testing.T) {
	d := setUpDemo(t)
	crm := registerInventedCRM(t, d)
	const name = "invented-books"
	d.connectInvented(t, name, salesforcetest.Alias)

	interrupted, cancel := context.WithCancel(ctx)
	defer cancel()
	crm.answer = func(_ context.Context, tool string) ([]*strictjson.Object, bool, error) {
		// The CRM has answered, and the caller goes before the receipt binds it.
		cancel()
		return []*strictjson.Object{strictjson.NewObject("id", inventedAccount, "name", "An invented account",
			"record_ref", "crm/accounts/"+inventedAccount)}, true, nil
	}
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	var out, errs bytes.Buffer
	if code := runCommand(interrupted, "accounts", []string{"--state-dir", name, "--json"},
		screen{out: &out, err: &errs, json: true}); code != 130 || out.Len() != 0 {
		t.Errorf("accounts, interrupted: exit %d\n%s%s", code, out.String(), errs.String())
	}

	crm.answer = func(context.Context, string) ([]*strictjson.Object, bool, error) { return nil, true, nil }
	if _, message, _ := d.refusal(t, 5, "inspect", inventedAccount, "--state-dir", name); message !=
		"get_account: invalid or failed tool result; no account data returned" {
		t.Errorf("inspect of an empty answer: %s", message)
	}
	if got := d.audited(t, name); got != "search_accounts:error:200:request_failed get_account:error:200:request_failed" {
		t.Errorf("the receipts: %s", got)
	}
}
