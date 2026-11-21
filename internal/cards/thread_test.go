package cards

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// unsnapped is a Slack source that returns a thread's messages as plain records, with no snapshot of their
// read.
type unsnapped struct {
	Source
	messages []*strictjson.Object
}

func (s unsnapped) Read(tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_slack_messages" {
		return "slack", s.messages, nil
	}
	return s.Source.Read(tool, accountID)
}

// A live thread whose messages are not the ones its snapshot describes gives no card: the reads keep their
// receipts, the card is refused, and no seal is started.
func TestAThreadThatFailsItsSnapshotGivesNoCard(t *testing.T) {
	const key, refusal = "gemini/0", "Live Slack evidence does not match its complete bounded source snapshot."
	var row fixtureRow
	for _, line := range oracletest.Lines(t, "cards/slack_threads.jsonl.gz") {
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Key == key {
			break
		}
	}
	if row.Key != key {
		t.Fatalf("no card %s", key)
	}
	input := load(t, row.Input).(*strictjson.Object)
	for name, change := range map[string]func(thread) Source{
		"an altered message": func(read thread) Source {
			altered := read.messages[0].Copy()
			altered.Set("body", "Invented altered statement.")
			read.messages = append([]*strictjson.Object{altered}, read.messages[1:]...)
			return read
		},
		"a message left out": func(read thread) Source {
			read.messages = read.messages[:len(read.messages)-1]
			return read
		},
		"messages with no snapshot": func(read thread) Source { return unsnapped{read.Source, read.messages} },
	} {
		t.Run(name, func(t *testing.T) {
			s := slackThread(t, row, input)
			sc := newScope(s.scope)
			m, store := sc.maker(change(s.source.(thread)), s.sources)
			sealed, err := m.Brief(context.Background(), options{IncludeSlack: true}.brief(row.AccountID, sc.token(1)))
			var rejected *evidence.Rejected
			if sealed != nil || !errors.As(err, &rejected) || rejected.Message != refusal {
				t.Fatalf("got %v, %v; want the refusal %q", sealed, err, refusal)
			}
			all := store.All()
			if len(all) != 4 {
				t.Fatalf("%d receipts, want the four reads", len(all))
			}
			for _, receipt := range all {
				if receipt.Tool == receipts.SaveBrief || receipt.Status != "ok" {
					t.Errorf("receipt %s %s", receipt.Tool, receipt.Status)
				}
			}
		})
	}
}
