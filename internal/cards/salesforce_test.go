package cards

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/demo"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce"
	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// besideDemo reads the CRM from one source and call notes and Slack messages from the demo stores, as the
// oracle's folder does: its CRM is Salesforce, and its calls and Slack are the demo's, loaded by account ID.
type besideDemo struct {
	crm  Source
	demo *demo.Store
}

func (s besideDemo) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "get_account" || tool == "list_opportunities" || tool == "list_users" {
		return s.crm.Read(ctx, tool, accountID)
	}
	return s.demo.Read(ctx, tool, accountID)
}

// salesforceSources is a folder connected to this org, with the demo's calls and Slack.
func salesforceSources(identity salesforce.Identity) evidence.Sources {
	return evidence.Sources{CRMBackend: "salesforce", CRMBinding: identity.Binding(), CallsBackend: "fixture",
		SlackBackend: "fixture"}
}

// The two commands one login read runs, and nothing else.
var loginCommands = [][]string{
	{"org", "display", "--target-org", salesforcetest.Alias, "--json"},
	{"org", "auth", "show-access-token", "--target-org", salesforcetest.Alias, "--json"},
}

// salesforceBooks builds the books_salesforce cards as the oracle did: one invented org and one reader for
// the family, signed in through an invented sf, each account's recorded rows put in the org before its card,
// and its call notes and Slack messages in the demo stores.
func salesforceBooks(t *testing.T, newStore func(*testing.T) cardStore, book []byte) {
	ctx := context.Background()
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	reader, err := salesforce.FromSF(ctx, salesforcetest.Alias, salesforce.Options{Environment: "production",
		Runner: sf, Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := reader.PrepareConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runFamily(t, "books_salesforce", newStore, func(row fixtureRow, input *strictjson.Object) setup {
		rows, _ := input.Get("org_rows")
		if accountID := org.Add(rows.(*strictjson.Object)); accountID != row.AccountID {
			t.Fatalf("%s: the recorded rows are of %s", row.Key, accountID)
		}
		store, err := demo.Load(book)
		if err != nil {
			t.Fatal(err)
		}
		for member, table := range map[string][2]string{"call_notes": {"calls", "call_notes"}, "messages": {"slack", "messages"}} {
			list, _ := input.Get(member)
			for _, values := range list.([]any) {
				if err := store.Insert(table[0], table[1], values.([]any)); err != nil {
					t.Fatal(err)
				}
			}
		}
		name, _ := input.Get("book")
		index, _ := input.Get("index")
		return setup{scope: fmt.Sprintf("books/salesforce/%s/%d", name, intOf(index)),
			source: besideDemo{reader, store}, sources: salesforceSources(identity)}
	})
	if len(org.Unexpected) > 0 {
		t.Errorf("the invented org was asked what it does not serve: %v", org.Unexpected[:1])
	}
	if !reflect.DeepEqual(sf.Commands, loginCommands) {
		t.Errorf("sf ran %v", sf.Commands)
	}
}

// waitingSF writes an invented sf program that answers the two login commands until the file "expired"
// stands beside it. From then on it never answers: it starts a child, writes both process IDs to "waiting",
// and waits. salesforce.Exec runs only in a live process, so the test lets it run this program until it ends.
func waitingSF(t *testing.T) (program, folder string) {
	t.Helper()
	sourcehttp.Live.Store(true)
	t.Cleanup(func() { sourcehttp.Live.Store(false) })
	folder = t.TempDir()
	program = filepath.Join(folder, "sf")
	script := `#!/bin/sh
folder=$(dirname "$0")
if [ -e "$folder/expired" ]; then
  sleep 600 &
  echo "$$ $!" > "$folder/waiting.part" && mv "$folder/waiting.part" "$folder/waiting"
  wait
fi
case "$2" in
  display) echo '{"status": 0, "result": {"id": "` + salesforcetest.OrganizationID + `", "instanceUrl": "` + salesforcetest.Instance + `", "username": "` + salesforcetest.Username + `", "alias": "` + salesforcetest.Alias + `"}}' ;;
  auth) echo '{"status": 0, "result": {"accessToken": "` + salesforcetest.Token + `"}}' ;;
esac
`
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, folder
}

// running is whether a process with this ID exists.
func running(pid int) bool { return syscall.Kill(pid, 0) != syscall.ESRCH }

// A read whose token expired waits on sf. A cancel then ends sf and the child sf started, the read's receipt
// finishes as an error, no later read runs, and no card is published or sealed.
func TestACancelEndsSFAndPublishesNoCard(t *testing.T) {
	program, folder := waitingSF(t)
	org := salesforcetest.NewOrg()
	rows := load(t, []byte(`{"Account": [{"Id": "001000000000000AAA", "Name": "Invented Account", "Industry": null,
		"AccountNumber": null}], "Contact": [], "Opportunity": [], "Contract": []}`)).(*strictjson.Object)
	accountID := org.Add(rows)
	starting, cancelStarting := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStarting()
	reader, err := salesforce.FromSF(starting, salesforcetest.Alias, salesforce.Options{Environment: "production",
		Runner: salesforce.Exec{Path: program}, Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := reader.PrepareConnection(starting)
	if err != nil {
		t.Fatal(err)
	}
	// The org stops accepting the token, and sf stops answering.
	org.Token = "invented-rotated-token"
	if err := os.WriteFile(filepath.Join(folder, "expired"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	memory := receipts.NewMemory(nil, nil)
	connections := map[string]string{"crm": "crm-1", "calls": "calls-1", "slack": "slack-1"}
	for _, id := range connections {
		memory.SetGeneration(testWorkspace, id, 1)
	}
	store := &watched{Store: memory}
	read := map[string]int{}
	source := during{besideDemo{reader, demoBook(t)}, func(tool string) { read[tool]++ }}
	m := &Maker{Receipts: store, Source: source, Sources: salesforceSources(identity), Connections: connections}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pids []int
	found := make(chan struct{})
	go func() {
		defer close(found)
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			written, err := os.ReadFile(filepath.Join(folder, "waiting"))
			if err != nil {
				continue
			}
			for _, field := range strings.Fields(string(written)) {
				pid, _ := strconv.Atoi(field)
				pids = append(pids, pid)
			}
			break
		}
		cancel()
	}()
	sealed, err := m.Brief(ctx, Brief{Principal: owner("a bearer", "crm:read", "calls:read", "slack:read"),
		OperationID: "brief-cancelled", AccountID: accountID, AsOf: DemoAsOf, IncludeSlack: true})
	<-found
	if sealed != nil || !errors.Is(err, ErrBriefing) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a card: %t; %v", sealed != nil, err)
	}
	if len(pids) != 2 || pids[0] <= 1 || pids[1] <= 1 {
		t.Fatalf("sf did not start waiting: %v", pids)
	}
	// sf itself is reaped before the read returns. Its child was killed with it, and is gone once the
	// system has reaped it.
	for deadline := time.Now().Add(10 * time.Second); running(pids[1]) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if running(pids[0]) || running(pids[1]) {
		t.Fatalf("sf %d runs: %t; its child %d runs: %t", pids[0], running(pids[0]), pids[1], running(pids[1]))
	}
	first, err := memory.ByOperation(context.Background(), testWorkspace, "brief-cancelled/1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Tool != "get_account" || first.Status != "error" || *first.HTTPStatus != 200 ||
		*first.Error != "request_failed" || len(first.Rows) != 0 {
		t.Fatalf("the stopped read: %+v", first)
	}
	if len(read) != 1 || read["get_account"] != 1 {
		t.Fatalf("the reads: %v", read)
	}
	if store.published != 0 {
		t.Fatalf("%d cards published", store.published)
	}
	if _, err := memory.ByOperation(context.Background(), testWorkspace, "brief-cancelled/seal"); !errors.Is(err, receipts.ErrNotFound) {
		t.Fatalf("a seal was started: %v", err)
	}
	// The org was asked for the account once, answered 401, and was not asked again.
	if last := org.Requests[len(org.Requests)-1]; !strings.Contains(last, "FROM Account WHERE Id = '001000000000000AAA'") {
		t.Fatalf("the last request: %s", last)
	}
}
