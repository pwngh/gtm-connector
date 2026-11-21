package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// asciiSHA is the SHA-256 of a value's canonical ASCII encoding, as the Slack reader digests one.
func asciiSHA(t *testing.T, value any) string {
	t.Helper()
	encoded, err := strictjson.Dumps(value, strictjson.DumpOptions{EnsureASCII: true, SortKeys: true, Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

// slackMessages are the evidence's live Slack messages, in order.
func slackMessages(evidence *strictjson.Object) []*strictjson.Object {
	var found []*strictjson.Object
	for _, value := range member(evidence, "records").([]any) {
		if ref, _ := member(value, "record_ref").(string); strings.HasPrefix(ref, "slack/messages/") {
			found = append(found, value.(*strictjson.Object))
		}
	}
	return found
}

// reseal gives a changed thread snapshot its own digest, and each message the ID it has under that
// snapshot, as slack_reader._record does, with the new references in the read's receipt. Only a rule about
// the changed field can then refuse the evidence.
func reseal(t *testing.T, evidence *strictjson.Object) {
	t.Helper()
	snapshot := memberObject(evidence, "thread_snapshot")
	rest := snapshot.Copy()
	rest.Delete("snapshot_sha256")
	sealed := asciiSHA(t, rest)
	snapshot.Set("snapshot_sha256", sealed)
	rows := []any{}
	for _, record := range slackMessages(evidence) {
		metadata := memberObject(record, "slack_message")
		metadata.Set("snapshot_sha256", sealed)
		manifest := metadata.Copy()
		manifest.Delete("schema_version")
		manifest.Delete("snapshot_sha256")
		manifest.Set("text", member(record, "body"))
		id := "msg-" + asciiSHA(t, strictjson.NewObject("snapshot_sha256", sealed, "manifest_sha256", asciiSHA(t, manifest)))
		record.Set("id", id)
		record.Set("record_ref", "slack/messages/"+id)
		rows = append(rows, "slack/messages/"+id)
	}
	receiptOf(evidence, "read_slack_messages").Set("rows", rows)
}

const (
	otherSnapshot = "Live Slack evidence does not match its complete bounded source snapshot."
	noSnapshot    = "Only a live Slack read carries a thread snapshot."
	liveThread    = "gemini/0" // three messages of one thread, read in one page, with three blocks left out
)

// slackRefusals are the changes to a live Slack thread's evidence that the removed
// Python suite refused, each with the refusal units.build gives for it. A change marked
// resealed has the digests and IDs of its changed snapshot, so only the check of the changed field can
// refuse it.
var slackRefusals = []struct {
	name     string
	change   func(evidence, snapshot, first *strictjson.Object)
	resealed bool
	want     string
}{
	{"schema version true", func(_, s, _ *strictjson.Object) { s.Set("schema_version", true) }, true, otherSnapshot},
	{"another source", func(_, s, _ *strictjson.Object) { s.Set("source", "fixture") }, true, otherSnapshot},
	{"a field added to the snapshot", func(_, s, _ *strictjson.Object) { s.Set("extra", "private") }, true,
		otherSnapshot},
	{"paging not complete", func(_, s, _ *strictjson.Object) { s.Set("pagination_complete", false) }, false,
		otherSnapshot},
	{"paging not complete, resealed", func(_, s, _ *strictjson.Object) { s.Set("pagination_complete", false) }, true,
		otherSnapshot},
	{"cutoff at a reply", func(e, s, _ *strictjson.Object) {
		s.Set("cutoff_ts", member(slackMessages(e)[1], "slack_message", "ts"))
	}, true, otherSnapshot},
	{"another channel in the mapping", func(_, s, _ *strictjson.Object) {
		memberObject(s, "mapping").Set("channel_id", "C99999999")
	}, false, otherSnapshot},
	{"request count true", func(_, s, _ *strictjson.Object) {
		memberObject(s, "counts").Set("http_attempts", true)
	}, true, otherSnapshot},
	{"more rows than one read holds", func(_, s, _ *strictjson.Object) {
		memberObject(s, "counts").Set("raw_rows", 101)
	}, true, otherSnapshot},
	{"no message counted", func(_, s, _ *strictjson.Object) { memberObject(s, "counts").Set("messages", 0) }, true,
		otherSnapshot},
	{"a duplicate counted", func(_, s, _ *strictjson.Object) { memberObject(s, "counts").Set("duplicates", 1) }, true,
		otherSnapshot},
	{"a file counted as left out", func(_, s, _ *strictjson.Object) {
		memberObject(s, "counts").Set("omitted_files", 1)
	}, true, otherSnapshot},
	{"a block not counted as left out", func(_, s, _ *strictjson.Object) {
		memberObject(s, "counts").Set("omitted_blocks", 0)
	}, false, otherSnapshot},
	{"more sign-in requests than a read makes", func(_, s, _ *strictjson.Object) {
		memberObject(s, "counts").Set("auth_attempts", 11)
	}, true, otherSnapshot},
	{"altered body", func(_, _, m *strictjson.Object) { m.Set("body", "Invented altered statement.") }, false,
		otherSnapshot},
	{"altered author", func(_, _, m *strictjson.Object) { m.Set("author", "Verified account owner") }, false,
		otherSnapshot},
	{"another channel on a message", func(_, _, m *strictjson.Object) { m.Set("channel", "COTHER") }, false,
		otherSnapshot},
	{"wrong reference", func(_, _, m *strictjson.Object) { m.Set("record_ref", "slack/messages/msg-wrong") }, false,
		"Record reference does not identify its source."},
	{"a field added to a message", func(_, _, m *strictjson.Object) { m.Set("extra", 1) }, false, otherSnapshot},
	{"another Slack identity", func(e, _, _ *strictjson.Object) {
		memberObject(e, "slack_binding").Set("identity_sha256", strings.Repeat("0", 64))
	}, false, otherSnapshot},
	{"another CRM identity", func(e, _, _ *strictjson.Object) {
		memberObject(e, "crm_binding").Set("identity_sha256", strings.Repeat("0", 64))
	}, false, otherSnapshot},
	{"no rows in the read's receipt", func(e, _, _ *strictjson.Object) {
		receipt := receiptOf(e, "read_slack_messages")
		receipt.Set("rows", []any{})
		receipt.Set("row_count", 0)
	}, false, "Audit receipt does not match returned records."},
	{"demo Slack", func(e, _, _ *strictjson.Object) { e.Set("slack_backend", "fixture") }, false, noSnapshot},
	{"Slack denied", func(e, _, _ *strictjson.Object) { e.Set("slack_status", "denied") }, false,
		"A Slack read that returned nothing cannot contain a snapshot or messages."},
	{"Slack not requested", func(e, _, _ *strictjson.Object) { e.Set("slack_status", "not_requested") }, false,
		noSnapshot},
	{"no snapshot", func(e, _, _ *strictjson.Object) { e.Set("thread_snapshot", nil) }, false, otherSnapshot},
	{"original text in the evidence", func(e, _, _ *strictjson.Object) {
		e.Set("unrequested_original_text", "Unselected private text.")
	}, false, "Evidence has an unsupported shape."},
}

// changedThread is the evidence of one refusal: the live thread's evidence, changed, and resealed when it
// says so.
func changedThread(t *testing.T, i int) *strictjson.Object {
	t.Helper()
	c := slackRefusals[i]
	evidence := sealedEvidence(t, "slack_threads", liveThread)
	c.change(evidence, memberObject(evidence, "thread_snapshot"), slackMessages(evidence)[0])
	if c.resealed {
		reseal(t, evidence)
	}
	return evidence
}

func TestLiveSlackRefusals(t *testing.T) {
	if _, _, _, _, err := Build(sealedEvidence(t, "slack_threads", liveThread)); err != nil {
		t.Fatalf("as sealed: %v", err)
	}
	for i, c := range slackRefusals {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, _, err := Build(changedThread(t, i)); err == nil || err.Error() != c.want {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

// recountedThread is the live thread's evidence with another valid count of the bytes Slack returned,
// resealed.
func recountedThread(t *testing.T) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "slack_threads", liveThread)
	memberObject(evidence, "thread_snapshot", "counts").Set("response_bytes", 1)
	reseal(t, evidence)
	return evidence
}

// TestResealedThreadIsAccepted: a snapshot changed to another valid one and resealed is valid, so the
// resealed refusals above are refused for their changed field and not for a stale digest.
func TestResealedThreadIsAccepted(t *testing.T) {
	evidence := sealedEvidence(t, "slack_threads", liveThread)
	memberObject(evidence, "thread_snapshot", "counts").Set("response_bytes", 1)
	if _, _, _, _, err := Build(evidence); err == nil || err.Error() != otherSnapshot {
		t.Fatalf("a changed snapshot under its old digest: %v", err)
	}
	_, text, _, _, err := Build(recountedThread(t))
	if err != nil {
		t.Fatal(err)
	}
	const line = "  Slack: 3 messages were read from one selected thread, and paging completed. Not read: files (0), " +
		"attachments (0), blocks (3), earlier versions of edited messages, and every other thread."
	if !strings.Contains(text, line+"\n") {
		t.Errorf("the card lacks %q:\n%s", line, text)
	}
}
