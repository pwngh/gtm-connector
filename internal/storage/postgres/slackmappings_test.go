package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// slackMapping is an invented selection of a thread for an account, under a Slack binding whose digest is
// made of identity.
func slackMapping(t *testing.T, account, channel, stamp, identity string) postgres.SlackMapping {
	t.Helper()
	text, err := strictjson.Dumps(strictjson.NewObject("account_id", account, "channel_id", channel, "thread_ts", stamp,
		"crm_binding", crmBinding("sqlite", "a"), "slack_binding", strictjson.NewObject("schema_version", 1,
			"provider", "slack", "identity_sha256", identity)), strictjson.Prepared)
	ok(t, err)
	return postgres.SlackMapping{AccountRef: account, ChannelID: channel, ThreadTS: stamp, Mapping: text}
}

// A workspace maps one thread to each account and each thread to one account. Mapping the same thread again
// changes nothing; another thread needs replace; another account's thread needs reassign, which leaves that
// account with none; and every mapping was made under the same bindings.
func TestSlackMappingsKeepOneThreadPerAccount(t *testing.T) {
	f := postgrestest.New(t)
	identity := fmt.Sprintf("%064x", 7)
	for _, account := range []string{"acc-004", "acc-005"} {
		ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, account, "Invented account"))
	}
	save := func(m postgres.SlackMapping, replace, reassign bool) (bool, error) {
		saved, changed, err := f.Store.SaveSlackMapping(ctx, f.Workspace.ID, m, replace, reassign)
		if err == nil {
			if got, _ := f.Store.SlackMappingOf(ctx, f.Workspace.ID, m.AccountRef); got != saved {
				t.Errorf("%s holds %s, and the save gave %s", m.AccountRef, got, saved)
			}
		}
		return changed, err
	}
	first := slackMapping(t, "acc-003", "C0123", "1790000000.000001", identity)
	if changed, err := save(first, false, false); !changed || err != nil {
		t.Fatalf("the first mapping: %v %v", changed, err)
	}
	if changed, err := save(first, false, false); changed || err != nil {
		t.Errorf("the same mapping again: %v %v", changed, err)
	}
	other := slackMapping(t, "acc-003", "C0123", "1790000000.000002", identity)
	if _, err := save(other, false, true); !errors.Is(err, postgres.ErrSlackMappingConflict) {
		t.Errorf("another thread without replace: %v", err)
	}
	if changed, err := save(other, true, false); !changed || err != nil {
		t.Errorf("another thread with replace: %v %v", changed, err)
	}
	moved := slackMapping(t, "acc-004", "C0123", "1790000000.000002", identity)
	if _, err := save(moved, true, false); !errors.Is(err, postgres.ErrSlackMappingConflict) {
		t.Errorf("another account's thread without reassign: %v", err)
	}
	if changed, err := save(moved, false, true); !changed || err != nil {
		t.Errorf("another account's thread with reassign: %v %v", changed, err)
	}
	if got, err := f.Store.SlackMappingOf(ctx, f.Workspace.ID, "acc-003"); got != "" || err != nil {
		t.Errorf("the account that lost its thread holds %q, %v", got, err)
	}
	elsewhere := slackMapping(t, "acc-005", "C0456", "1790000000.000003", fmt.Sprintf("%064x", 8))
	if _, err := save(elsewhere, false, false); !errors.Is(err, postgres.ErrSlackMappingSource) {
		t.Errorf("a mapping made under another Slack identity: %v", err)
	}
	unlisted := slackMapping(t, "acc-099", "C0456", "1790000000.000003", identity)
	if _, err := save(unlisted, false, false); err == nil {
		t.Error("an account the workspace does not list was mapped")
	}
	// The row agrees with its mapping for any writer.
	if err := sql(t, f, `UPDATE slack_mappings SET thread_ts = '1790000000.000009' WHERE account_ref = 'acc-004'`); err == nil {
		t.Error("a row was changed apart from its mapping")
	}
}

// At most a hundred accounts of a workspace have a thread.
func TestSlackMappingsStopAtAHundred(t *testing.T) {
	f := postgrestest.New(t)
	identity := fmt.Sprintf("%064x", 7)
	for i := range postgres.MaxSlackMappings + 1 {
		account := fmt.Sprintf("acc-%03d", 100+i)
		ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, account, "Invented account"))
		_, _, err := f.Store.SaveSlackMapping(ctx, f.Workspace.ID,
			slackMapping(t, account, "C0123", fmt.Sprintf("1790000000.%06d", i), identity), false, false)
		if want := i == postgres.MaxSlackMappings; errors.Is(err, postgres.ErrSlackMappingConflict) != want ||
			(!want && err != nil) {
			t.Fatalf("mapping %d: %v", i+1, err)
		}
	}
}
