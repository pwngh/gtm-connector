package evidence

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The port of slack_reader.validate_slack_snapshot, the check the messages and the snapshot of one live
// Slack thread pass before a card is built from them, with slack_mapping.validate_slack_mapping, which it
// applies to the snapshot's mapping. It reads no file, credential or network.

// The bounds of one thread read, as slack_reader.py names them.
const (
	maxSlackRequests  = 10
	maxSlackResponse  = 1_048_576
	maxSlackRows      = 100
	maxSlackText      = 16_384
	maxSlackTotalText = 65_536
	maxSlackOutput    = 262_144
	maxSlackSeconds   = 253402300799 // the last second of the year 9999
)

var (
	snapshotKeys = []string{"schema_version", "source", "mapping", "cutoff_ts", "started_at", "fetched_at",
		"pagination_complete", "counts", "manifest_sha256", "snapshot_sha256"}
	mappingKeys = []string{"schema_version", "mapping_id", "account_id", "crm_binding", "slack_binding",
		"channel_id", "thread_ts", "selected_at", "resolution_receipt_id"}
	messageKeys = []string{"id", "account_id", "channel", "author", "sent_at", "body", "slack_message"}
	// What a message's record keeps of its manifest: every field but the text, which is the record's body.
	contentKeys = []string{"ts", "thread_ts", "subtype", "user_id", "bot_id", "edited", "omissions"}
	countKeys   = []string{"http_attempts", "auth_attempts", "message_attempts", "response_bytes", "pages",
		"raw_rows", "messages", "duplicates", "refresh_attempts", "token_retries", "rate_retries", "omitted_files",
		"omitted_attachments", "omitted_blocks"}
	omitted      = []string{"files", "attachments", "blocks"}
	metadataKeys = append([]string{"schema_version", "snapshot_sha256"}, contentKeys...)
)

// canonicalASCII is json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).
var canonicalASCII = strictjson.DumpOptions{EnsureASCII: true, SortKeys: true, Compact: true}

// asciiDigest is slack_reader._digest: the SHA-256 of a value's canonical ASCII encoding.
func asciiDigest(value any) string {
	encoded, err := strictjson.Dumps(value, canonicalASCII)
	if err != nil {
		panic(invalid)
	}
	return digest(encoded)
}

// threadMicros is slack_mapping.timestamp_microseconds: a Slack timestamp, whole seconds up to the year
// 9999 with no leading zero and six digits of a fraction, as microseconds.
func threadMicros(v any) (int64, bool) {
	s, ok := v.(string)
	dot := len(s) - 7
	if !ok || dot < 1 || dot > 12 || s[dot] != '.' || s[0] == '0' && dot != 1 {
		return 0, false
	}
	seconds, okSeconds := digits(s[:dot])
	fraction, okFraction := digits(s[dot+1:])
	if !okSeconds || !okFraction || seconds > maxSlackSeconds {
		return 0, false
	}
	return int64(seconds)*1_000_000 + int64(fraction), true
}

// slackID is whether a value is a Slack identifier: one of the prefix letters, then 1 to most capitals or
// digits.
func slackID(v any, prefixes string, most int) bool {
	s, ok := v.(string)
	if !ok || len(s) < 2 || len(s) > most+1 || !in(rune(s[0]), prefixes) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isDigit(s[i]) && !asciiUpper(rune(s[i])) {
			return false
		}
	}
	return true
}

// utcTime is slack_reader._date: a time written as its own isoformat in UTC, with Z for the offset.
func utcTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || !utcStamp(s) {
		return time.Time{}, false
	}
	moment, err := time.Parse(time.RFC3339Nano, s)
	return moment, err == nil
}

// lowerUUID is whether a text is a UUID as str(uuid.UUID) writes one: 8-4-4-4-12 hexadecimal digits in
// lower case.
func lowerUUID(v any) bool {
	s, ok := v.(string)
	if !ok || len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if s[i] != '-' {
				return false
			}
		case !isDigit(s[i]) && (s[i] < 'a' || s[i] > 'f'):
			return false
		}
	}
	return true
}

// without is an object's members but one.
func without(o *strictjson.Object, key string) *strictjson.Object {
	rest := o.Copy()
	rest.Delete(key)
	return rest
}

// validSlackMapping is slack_mapping.validate_slack_mapping: the selection of one thread for one account,
// under the CRM and Slack bindings it was made with, named by the digest of its own fields.
func validSlackMapping(v any, accountID string, crmBinding, slackBinding any) (*strictjson.Object, bool) {
	backend, bound := crmBackend(crmBinding)
	mapping, isObject := v.(*strictjson.Object)
	if !bound || !ValidBinding(slackBinding, "provider", "slack") || !isObject || !hasKeys(mapping, mappingKeys...) {
		return nil, false
	}
	_, versioned := intIn(field(mapping, "schema_version"), 1, 1)
	_, threaded := threadMicros(field(mapping, "thread_ts"))
	selected, isText := field(mapping, "selected_at").(string)
	if !versioned || !equal(field(mapping, "crm_binding"), crmBinding) ||
		!equal(field(mapping, "slack_binding"), slackBinding) || !ValidAccountID(accountID, backend == "salesforce") ||
		field(mapping, "account_id") != accountID || !slackID(field(mapping, "channel_id"), "CG", 31) || !threaded ||
		!isText || !PlainInstant(selected) || selected[len(selected)-1] != 'Z' ||
		!lowerUUID(field(mapping, "resolution_receipt_id")) {
		return nil, false
	}
	// A selection time is a real time: datetime.fromisoformat reads it.
	if _, real := Instant(selected); !real {
		return nil, false
	}
	canonical, err := strictjson.Dumps(without(mapping, "mapping_id"), strictjson.Prepared)
	return mapping, err == nil && field(mapping, "mapping_id") == "map-"+digest(canonical)
}

// slackMessage is slack_reader._manifest_rules: one message of the thread, as its record keeps it with its
// text. It gives the message's time in microseconds.
func slackMessage(message *strictjson.Object, parent string, cutoff int64) (int64, bool) {
	stamp, timed := threadMicros(field(message, "ts"))
	from, _ := threadMicros(parent)
	if !timed || field(message, "thread_ts") != parent || stamp < from || stamp > cutoff {
		return 0, false
	}
	// Only Slack's own system user writes the placeholder for a deleted parent.
	subtype := field(message, "subtype")
	if subtype != nil && !oneOf(subtype, "bot_message", "me_message", "file_share", "thread_broadcast", "tombstone") ||
		subtype == "tombstone" && (field(message, "ts") != parent || field(message, "user_id") != "USLACKBOT" ||
			field(message, "bot_id") != nil) {
		return 0, false
	}
	text, isText := field(message, "text").(string)
	user, bot := field(message, "user_id"), field(message, "bot_id")
	if !isText || !utf8.ValidString(text) || len(text) > maxSlackText || user != nil && !slackID(user, "UW", 63) ||
		bot != nil && !slackID(bot, "B", 63) {
		return 0, false
	}
	if edited := field(message, "edited"); edited != nil {
		edit, isObject := edited.(*strictjson.Object)
		if !isObject || !hasKeys(edit, "ts", "user_id") || !slackID(field(edit, "user_id"), "UW", 63) {
			return 0, false
		}
		if at, ok := threadMicros(field(edit, "ts")); !ok || at < stamp {
			return 0, false
		}
	}
	omissions, isObject := field(message, "omissions").(*strictjson.Object)
	if !isObject || !hasKeys(omissions, omitted...) {
		return 0, false
	}
	for _, key := range omitted {
		if _, ok := intIn(field(omissions, key), 0, maxSlackResponse); !ok {
			return 0, false
		}
	}
	return stamp, true
}

// slackAuthor is slack_reader._author: what a message's record says of its author, from its kind alone.
func slackAuthor(message *strictjson.Object) string {
	switch subtype := field(message, "subtype"); {
	case subtype == "tombstone":
		return "Deleted Slack message"
	case subtype == "bot_message" || field(message, "bot_id") != nil:
		return "Slack app"
	case field(message, "user_id") != nil:
		return "Slack participant"
	}
	return "Unknown Slack author"
}

// stampOf is slack_reader._stamp of a time given in microseconds since 1970: its isoformat in UTC, with Z.
func stampOf(micros int64) string {
	moment := time.UnixMicro(micros).UTC()
	if micros%1_000_000 == 0 {
		return moment.Format("2006-01-02T15:04:05Z")
	}
	return moment.Format("2006-01-02T15:04:05.000000Z")
}

// validSlackSnapshot is slack_reader.validate_slack_snapshot: whether the messages are, one for one and in
// order, the thread the snapshot describes, read whole within its bounds for this account under these
// bindings. A field that is missing or of another type fails the check.
func validSlackSnapshot(records []*strictjson.Object, v any, accountID string, crmBinding,
	slackBinding any) (valid bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, rejected := r.(*Rejected); !rejected {
				panic(r)
			}
			valid = false
		}
	}()
	snapshot, isObject := v.(*strictjson.Object)
	if !isObject || !hasKeys(snapshot, snapshotKeys...) {
		return false
	}
	_, versioned := intIn(field(snapshot, "schema_version"), 1, 1)
	mapping, mapped := validSlackMapping(field(snapshot, "mapping"), accountID, crmBinding, slackBinding)
	started, okStarted := utcTime(field(snapshot, "started_at"))
	fetched, okFetched := utcTime(field(snapshot, "fetched_at"))
	if !versioned || field(snapshot, "source") != "slack" || field(snapshot, "pagination_complete") != true ||
		!mapped || !okStarted || !okFetched || fetched.Before(started) {
		return false
	}
	// The thread was read up to the moment the read started (slack_reader._wall_timestamp).
	cutoff := started.UnixMicro()
	sealed := field(snapshot, "snapshot_sha256")
	if cutoff < 0 || field(snapshot, "cutoff_ts") != fmt.Sprintf("%d.%06d", cutoff/1_000_000, cutoff%1_000_000) ||
		sealed != asciiDigest(without(snapshot, "snapshot_sha256")) || len(records) < 1 || len(records) > maxSlackRows {
		return false
	}
	parent := str(field(mapping, "thread_ts"))
	previous, text := int64(-1), 0
	digests := make([]any, len(records))
	left := map[string]int64{}
	for i, row := range records {
		_, referenced := row.Get("record_ref")
		metadata, isObject := field(row, "slack_message").(*strictjson.Object)
		if !hasKeys(row, withKey(messageKeys, "record_ref", referenced)...) || !isObject ||
			!hasKeys(metadata, metadataKeys...) {
			return false
		}
		message := strictjson.NewObject("text", field(row, "body"))
		for _, key := range contentKeys {
			message.Set(key, field(metadata, key))
		}
		_, versioned := intIn(field(metadata, "schema_version"), 1, 1)
		stamp, ok := slackMessage(message, parent, cutoff)
		if !versioned || !ok || stamp <= previous || i == 0 && field(message, "ts") != parent {
			return false
		}
		previous = stamp
		// The record the message makes in this snapshot (slack_reader._record).
		digests[i] = asciiDigest(message)
		id := "msg-" + asciiDigest(strictjson.NewObject("snapshot_sha256", sealed, "manifest_sha256", digests[i]))
		reference, _ := row.Get("record_ref")
		if field(row, "id") != id || field(row, "account_id") != accountID ||
			field(row, "channel") != field(mapping, "channel_id") || field(row, "author") != slackAuthor(message) ||
			field(row, "sent_at") != stampOf(stamp) || field(metadata, "snapshot_sha256") != sealed ||
			referenced && reference != "slack/messages/"+id {
			return false
		}
		text += len(str(field(message, "text")))
		for _, key := range omitted {
			n, _ := intIn(field(object(field(message, "omissions")), key), 0, maxSlackResponse)
			left[key] += n
		}
	}
	counts, isObject := field(snapshot, "counts").(*strictjson.Object)
	if text > maxSlackTotalText || !equal(field(snapshot, "manifest_sha256"), digests) || !isObject ||
		!hasKeys(counts, countKeys...) {
		return false
	}
	// Every count is bounded by the read's limits or by the messages, so one that is not a small integer
	// fails a check below.
	count := map[string]int64{}
	for _, key := range countKeys {
		n, ok := intIn(field(counts, key), 0, 1<<40)
		if !ok {
			return false
		}
		count[key] = n
	}
	read := int64(len(records))
	if count["http_attempts"] < 1 || count["http_attempts"] > maxSlackRequests ||
		count["http_attempts"] != count["auth_attempts"]+count["message_attempts"] ||
		count["pages"] < 1 || count["pages"] > count["message_attempts"] ||
		count["message_attempts"] != count["pages"]+count["token_retries"]+count["rate_retries"] ||
		count["response_bytes"] > maxSlackResponse || count["raw_rows"] > maxSlackRows ||
		count["messages"] != read || count["raw_rows"] != read+count["duplicates"] ||
		count["refresh_attempts"] > 1 || count["token_retries"] > 1 || count["rate_retries"] > 1 ||
		count["auth_attempts"] < 2*count["refresh_attempts"] {
		return false
	}
	for _, key := range omitted {
		if count["omitted_"+key] != left[key] {
			return false
		}
	}
	// The whole tool result stays within its bound (slack_reader._output_size).
	rows := make([]any, len(records))
	for i, row := range records {
		referenced := row.Copy()
		referenced.Set("record_ref", "slack/messages/"+str(field(row, "id")))
		rows[i] = referenced
	}
	encoded, err := strictjson.Dumps(strictjson.NewObject("source", "slack", "records", rows, "untrusted_data", true,
		"thread_snapshot", snapshot), canonicalASCII)
	return err == nil && len(encoded) <= maxSlackOutput
}
