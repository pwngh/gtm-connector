package cards

import (
	"context"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// -update writes a card only when it passes its own seal and receipt checks: a changed byte, or a log that
// lacks its seal, refuses it.
func TestUpdateWritesOnlyACardThatPassesItsChecks(t *testing.T) {
	sc := newScope("update/acc-003")
	m, store := sc.maker(demoBook(t), demoSources(nil))
	sealed, err := m.Brief(context.Background(), Brief{Principal: owner(sc.token(accessToken), "crm:read",
		"calls:read", "slack:read"), OperationID: "update", AccountID: "acc-003", AsOf: DemoAsOf, IncludeSlack: true})
	if err != nil {
		t.Fatal(err)
	}
	log := StoreLog{Store: store, WorkspaceID: testWorkspace}
	if err := renewable(sealed, log, demoSources(nil)); err != nil {
		t.Fatalf("a card made now is refused: %v", err)
	}
	changed := *sealed
	changed.Payload = []byte(strings.Replace(string(sealed.Payload), "Morgan Reed", "Morgan Reid", 1))
	if renewable(&changed, log, demoSources(nil)) == nil {
		t.Error("a card with a changed byte is written")
	}
	empty := StoreLog{Store: receipts.NewMemory(nil, nil), WorkspaceID: testWorkspace}
	if renewable(sealed, empty, demoSources(nil)) == nil {
		t.Error("a card whose seal is not in the log is written")
	}
}

// -update says, for each family, how many cards and units it changed and which members.
func TestUpdateSaysWhatChanged(t *testing.T) {
	card := func(version int, text string, units ...any) *strictjson.Object {
		return strictjson.NewObject("rules_version", version, "text", text, "units", units)
	}
	unit := func(n int, text string) *strictjson.Object {
		return strictjson.NewObject("n", n, "kind", "sentence", "text", text, "sanitizer_version", 4)
	}
	r := renewal{members: map[string]int{}}
	r.add(card(4, "same", unit(1, "a")), card(4, "same", unit(1, "a")))
	r.add(card(3, "old", unit(1, "a"), unit(2, "b")), card(4, "new", unit(1, "a b")))
	if got := r.String(); got != "1 of 2 cards and 2 units changed; changed members: card rules_version 1, card text 1, "+
		"unit added or removed 1, unit text 1" {
		t.Errorf("the summary is %q", got)
	}
}
