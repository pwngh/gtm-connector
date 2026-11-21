package slack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// omitted are the parts of a message a read counts and leaves out.
var omitted = []string{"files", "attachments", "blocks"}

// canonical is a value as sorted, compact, ASCII JSON: the bytes every digest is taken over.
func canonical(value any) (string, error) {
	return strictjson.Dumps(value, strictjson.DumpOptions{EnsureASCII: true, SortKeys: true, Compact: true})
}

// digest is the SHA-256 of a value's canonical JSON, in hex. The values digested are the reader's own.
func digest(value any) string {
	text, _ := canonical(value)
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// sameJSON reports whether two values are the same JSON.
func sameJSON(a, b any) bool {
	left, errA := canonical(a)
	right, errB := canonical(b)
	return errA == nil && errB == nil && left == right
}

// text is a member that is a string.
func text(o *strictjson.Object, key string) (string, bool) {
	value, _ := o.Get(key)
	s, isText := value.(string)
	return s, isText
}

// object is a value that is a JSON object.
func object(value any) (*strictjson.Object, bool) {
	o, isObject := value.(*strictjson.Object)
	return o, isObject && o != nil
}

// timestamp reads a Slack timestamp, whole seconds, a dot and exactly six digits, as microseconds. The
// seconds have no leading zero and end by the year 9999.
func timestamp(value any) (int64, bool) {
	s, isText := value.(string)
	if !isText || len(s) < 8 || s[len(s)-7] != '.' {
		return 0, false
	}
	seconds, fraction := s[:len(s)-7], s[len(s)-6:]
	if !digits(seconds, 12) || !digits(fraction, 6) || (len(seconds) > 1 && seconds[0] == '0') {
		return 0, false
	}
	whole, _ := strconv.ParseInt(seconds, 10, 64)
	if whole > 253402300799 {
		return 0, false
	}
	part, _ := strconv.ParseInt(fraction, 10, 64)
	return whole*1_000_000 + part, true
}

// stamp writes a time as the saved documents hold it: UTC, with microseconds only when there are some.
func stamp(t time.Time) string {
	t = t.UTC()
	text := fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(),
		t.Second())
	if micro := t.Nanosecond() / 1000; micro != 0 {
		text += fmt.Sprintf(".%06d", micro)
	}
	return text + "Z"
}

// wallTimestamp is a wall time as a Slack timestamp. A clock outside 1970 to 9999 gives none.
func wallTimestamp(t time.Time) (string, int64, error) {
	micros := t.UnixMicro()
	if micros < 0 {
		return "", 0, refuse("slack_response_invalid")
	}
	text := fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
	if _, valid := timestamp(text); !valid {
		return "", 0, refuse("slack_response_invalid")
	}
	return text, micros, nil
}

// identifier reports whether a value is a Slack ID: one of the prefixes, then 1 to 63 capitals or digits.
func identifier(value any, prefixes string) bool {
	return identifierOf(value, prefixes, 1)
}

func identifierOf(value any, prefixes string, least int) bool {
	s, isText := value.(string)
	if !isText || len(s) < 1+least || len(s) > 64 {
		return false
	}
	first := false
	for i := 0; i < len(prefixes); i++ {
		first = first || s[0] == prefixes[i]
	}
	if !first {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// thread is the selected thread and the window a read accepts messages in.
type thread struct {
	channel, parent            string
	parentMicros, cutoffMicros int64
}

// message is one accepted message of the thread.
type message struct {
	stamp     string
	micros    int64
	text      string
	omissions map[string]int
	// manifest is what the message's digest is taken over: ts, thread_ts, subtype, text, user_id, bot_id,
	// edited and omissions.
	manifest *strictjson.Object
}

// normalize checks one row of a page and reduces it to its manifest. A row that is not a plain message of
// this thread refuses the whole read: nothing is skipped.
func (t thread) normalize(row any) (*message, error) {
	invalid, incomplete := refuse("slack_response_invalid"), refuse("slack_thread_incomplete")
	o, isObject := object(row)
	if !isObject {
		return nil, invalid
	}
	parent := any(t.parent)
	stampValue, _ := o.Get("ts")
	subtype, hasSubtype := o.Get("subtype")
	// Slack leaves a hidden placeholder in place of a deleted parent that still has replies. It is kept, so
	// the replies can be read. No other message may be a placeholder or hidden.
	placeholder := subtype == any("tombstone")
	kind, _ := o.Get("type")
	body, hasText := o.Get("text")
	if kind != any("message") || !hasText || (placeholder && stampValue != parent) {
		return nil, invalid
	}
	for _, wrapper := range []string{"message", "previous_message", "deleted_ts"} {
		if _, wrapped := o.Get(wrapper); wrapped {
			return nil, invalid
		}
	}
	if hidden, stated := o.Get("hidden"); stated && hidden != any(false) && !(placeholder && hidden == any(true)) {
		return nil, invalid
	}
	if hasSubtype {
		if name, isText := subtype.(string); !isText || !slices.Contains(supportedSubtypes, name) {
			return nil, invalid
		}
	}
	if channel, stated := o.Get("channel"); stated && channel != any(t.channel) {
		return nil, incomplete
	}
	if threadOf, stated := o.Get("thread_ts"); stated && threadOf != parent || !stated && stampValue != parent {
		return nil, incomplete
	}
	omissions := map[string]int{}
	for _, part := range omitted {
		items, present := o.Get(part)
		if !present {
			omissions[part] = 0
			continue
		}
		list, isList := items.([]any)
		if !isList {
			return nil, invalid
		}
		for _, item := range list {
			if _, isObject := object(item); !isObject {
				return nil, invalid
			}
		}
		omissions[part] = len(list)
	}
	var edited any
	var editStamp, editor any
	if mark, present := o.Get("edited"); present {
		item, isObject := object(mark)
		var hasStamp, hasEditor bool
		editStamp, hasStamp = item.Get("ts")
		editor, hasEditor = item.Get("user")
		if !isObject || !hasStamp || !hasEditor {
			return nil, invalid
		}
		edited = strictjson.NewObject("ts", editStamp, "user_id", editor)
	}
	if root, present := o.Get("root"); present {
		item, isObject := object(root)
		if subtype != any("thread_broadcast") || !isObject {
			return nil, invalid
		}
		if rootStamp, stated := item.Get("ts"); stated && rootStamp != parent {
			return nil, incomplete
		}
	}
	user, hasUser := o.Get("user")
	bot, hasBot := o.Get("bot_id")
	if (hasUser && user == nil) || (hasBot && bot == nil) {
		return nil, invalid
	}

	micros, valid := timestamp(stampValue)
	if !valid {
		return nil, invalid
	}
	if micros < t.parentMicros || micros > t.cutoffMicros {
		return nil, incomplete
	}
	// Only Slack's own system user writes the placeholder for a deleted parent. A participant cannot.
	if placeholder && (user != any("USLACKBOT") || bot != nil) {
		return nil, invalid
	}
	content, isText := body.(string)
	if !isText {
		return nil, invalid
	}
	if len(content) > maxTextBytes {
		return nil, refuse("slack_output_too_large")
	}
	if (user != nil && !identifier(user, "UW")) || (bot != nil && !identifier(bot, "B")) {
		return nil, invalid
	}
	if edited != nil {
		editMicros, valid := timestamp(editStamp)
		if !identifier(editor, "UW") || !valid || editMicros < micros {
			return nil, invalid
		}
	}
	left := strictjson.NewObject()
	for _, part := range omitted {
		left.Set(part, omissions[part])
	}
	manifest := strictjson.NewObject("ts", stampValue, "thread_ts", t.parent, "subtype", subtype, "text", content,
		"user_id", user, "bot_id", bot, "edited", edited, "omissions", left)
	return &message{stamp: stampValue.(string), micros: micros, text: content, omissions: omissions,
		manifest: manifest}, nil
}

// nextCursor is the cursor of the page after this one, or nil at the end of the thread. A cursor that
// repeats the one just asked for, or that disagrees with has_more, means the thread cannot be read whole.
func nextCursor(document *strictjson.Object, requested *string) (*string, error) {
	incomplete := refuse("slack_thread_incomplete")
	var metadata *strictjson.Object
	if value, present := document.Get("response_metadata"); present {
		var isObject bool
		if metadata, isObject = object(value); !isObject {
			return nil, incomplete
		}
	}
	var next *string
	if value, _ := metadata.Get("next_cursor"); value != nil && value != any("") {
		cursor, isText := value.(string)
		if !isText || len(cursor) > maxCursorBytes || (requested != nil && cursor == *requested) {
			return nil, incomplete
		}
		for _, c := range cursor {
			if c < 32 || (c >= 127 && c <= 159) {
				return nil, incomplete
			}
		}
		next = &cursor
	}
	if more, stated := document.Get("has_more"); stated {
		if flag, isBool := more.(bool); !isBool || flag != (next != nil) {
			return nil, incomplete
		}
	}
	return next, nil
}

// author is the label a record carries in place of a name. The card's speaker is the raw user ID.
func author(manifest *strictjson.Object) string {
	subtype, _ := manifest.Get("subtype")
	bot, _ := manifest.Get("bot_id")
	user, _ := manifest.Get("user_id")
	switch {
	case subtype == any("tombstone"):
		return "Deleted Slack message"
	case subtype == any("bot_message") || bot != nil:
		return "Slack app"
	case user != nil:
		return "Slack participant"
	}
	return "Unknown Slack author"
}

// record is the message as the read returns it. Its ID is a digest of the snapshot and of its own manifest,
// so the same message read again has another ID.
func (m *message) record(snapshot, accountID, channel string) *strictjson.Object {
	id := "msg-" + digest(strictjson.NewObject("snapshot_sha256", snapshot, "manifest_sha256", digest(m.manifest)))
	source := strictjson.NewObject("schema_version", 1, "snapshot_sha256", snapshot)
	for i, key := range m.manifest.Keys {
		if key != "text" {
			source.Set(key, strictjson.Clone(m.manifest.Values[i]))
		}
	}
	sent := time.UnixMicro(m.micros)
	return strictjson.NewObject("id", id, "account_id", accountID, "channel", channel, "author", author(m.manifest),
		"sent_at", stamp(sent), "body", m.text, "slack_message", source)
}
