package cards

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func (s unsnapped) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	if tool == "read_slack_messages" {
		return "slack", s.messages, nil
	}
	return s.Source.Read(ctx, tool, accountID)
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

// refusingThread is a Slack source whose read fails with one error.
type refusingThread struct {
	Source
	err error
}

func (s refusingThread) ReadThread(context.Context, string) ([]*strictjson.Object, *strictjson.Object, error) {
	return nil, nil, s.err
}

// A failed read keeps the Diagnostic its source wrote for a person, and nothing else of what the source
// returned: an upstream error's text never reaches the error a command prints.
func TestAFailedReadKeepsOnlyTheSourcesDiagnostic(t *testing.T) {
	const key, upstream = "gemini/0", "invented upstream text with a token-like value"
	var row fixtureRow
	for _, line := range oracletest.Lines(t, "cards/slack_threads.jsonl.gz") {
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Key == key {
			break
		}
	}
	input := load(t, row.Input).(*strictjson.Object)
	told := &Diagnostic{Exit: 3, Message: "slack_forbidden: a fixed message.", Next: "a fixed command"}
	for name, c := range map[string]struct {
		err  error
		want *Diagnostic
	}{
		"an upstream error":                     {errors.New(upstream), nil},
		"a diagnostic beside an upstream error": {errors.Join(errors.New(upstream), told), told},
	} {
		t.Run(name, func(t *testing.T) {
			s := slackThread(t, row, input)
			sc := newScope(s.scope)
			m, _ := sc.maker(refusingThread{s.source.(thread).Source, c.err}, s.sources)
			sealed, err := m.Brief(context.Background(), options{IncludeSlack: true}.brief(row.AccountID, sc.token(1)))
			var read *ReadError
			var kept *Diagnostic
			if sealed != nil || !errors.As(err, &read) || strings.Contains(err.Error(), upstream) {
				t.Fatalf("got %v, %v", sealed, err)
			}
			if found := errors.As(err, &kept); found != (c.want != nil) || found && *kept != *c.want {
				t.Errorf("the diagnostic kept: %v", kept)
			}
		})
	}
}
