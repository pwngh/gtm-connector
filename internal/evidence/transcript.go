package evidence

import (
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// What units.py reads from an imported transcript, and the port of calls_import.validate_imported_record,
// the check a saved transcript record passes before a card is built from it.

// The bounds of an imported transcript, as calls_import.py names them.
const (
	MaxRawBytes      = 2 * 1024 * 1024
	MaxTotalUnits    = 10_000
	MaxQuoteBytes    = 16 * 1024
	MaxReturnedUnits = 256
	MaxRecordBytes   = 128 * 1024
	DefaultUnits     = 20
	ParserVersion    = 2
	RecordVersion    = 2
	maxCueMS         = 3_600_000_000_000_000
	maxCueLabel      = 200
)

// NamedCue is one line or cue of a transcript that declares speaker names, as NamedLine reads it, held to
// one label: the name is the speaker of every word after it. In a WebVTT cue a voice tag names a speaker
// too, so a cue whose words hold one is not a named cue, and no card can take a role from the tag where the
// file stated the name.
func NamedCue(text, format string) (name, words string, ok bool) {
	name, words, ok = NamedLine(text)
	if ok && format == "vtt" && len(VoiceTags([]rune(words))) > 0 {
		return "", "", false
	}
	return name, words, ok
}

// voice is one span of a WebVTT cue: its speaker label, nil for none, and its text.
type voice struct {
	label any
	text  string
}

// voices is units.voices: one WebVTT cue split at its voice tags. A tag is a label, not speech, and text
// outside a voice span belongs to the cue's own label. Styling is removed after the voices are found, and a
// span with nothing left is dropped.
func voices(text string, label any) []voice {
	s := []rune(text)
	var found []voice
	keep := func(speaker any, raw []rune) {
		if part := StripCueMarkup(raw); len(strip(part)) > 0 {
			found = append(found, voice{speaker, string(part)})
		}
	}
	position, speaker := 0, label
	for _, tag := range VoiceTags(s) {
		keep(speaker, s[position:tag.Start])
		speaker, position = label, tag.End
		if tag.Named {
			speaker = string(s[tag.NameStart:tag.NameEnd])
		}
	}
	keep(speaker, s[position:])
	return found
}

// NamedLine is units.named: one line or cue of a transcript that declares speaker names, as the name and the
// words after it. A cue of two lines is not one, and neither is a line whose name starts in lower case.
func NamedLine(text string) (name, words string, ok bool) {
	var lines [][]rune
	for _, line := range strings.Split(text, "\n") {
		if runes := []rune(line); len(strip(runes)) > 0 {
			lines = append(lines, runes)
		}
	}
	if len(lines) != 1 {
		return "", "", false
	}
	opened, ok := NamedMatch(lines[0])
	if !ok || u.IsLower(lines[0][opened.Start]) {
		return "", "", false
	}
	return string(lines[0][opened.Start:opened.NameEnd]), string(lines[0][opened.End:]), true
}

// unfinished is units._unfinished: whether transcript text stops mid-sentence. It has no closing
// punctuation, ends in "..", is a bare list marker, or ends on a word that cannot end a sentence.
func unfinished(text string) bool {
	s := strip([]rune(text))
	n := len(s)
	tail := n
	for tail > 0 && in(s[tail-1], closersLong) {
		tail--
	}
	if _, ended := Ended(s, 0, n); !ended || tail >= 2 && s[tail-1] == '.' && s[tail-2] == '.' ||
		ListMarkerFull(s, 0, n) {
		return true
	}
	_, last := NeverLast(s, max(n-24, 0), n)
	return last && !NumberSignFull(s, 0, n)
}

// goesOn is units._goes_on: whether text opens as the rest of a sentence, with a letter in lower case.
func goesOn(text string) bool {
	for _, c := range text {
		if !u.IsSpace(c) {
			return u.IsLower(c)
		}
	}
	return false
}

// allDigits is str.isdigit.
func allDigits(s string) bool {
	for _, c := range s {
		if !u.IsDigit(c) {
			return false
		}
	}
	return s != ""
}

// nonSpace is sum(map(len, text.split())): the characters of a text that are not whitespace.
func nonSpace(text string) int {
	n := 0
	for _, c := range text {
		if !u.IsSpace(c) {
			n++
		}
	}
	return n
}

// cues is the speech of an imported transcript's excerpt, part by part. A WebVTT voice tag names the
// speaker. A cue identifier does only when it is not a number. In an import that declares speaker names, the
// name that opens the cue does instead, and is not part of what was said.
func cues(manifest *strictjson.Object) []part {
	_, declared := manifest.Get("speaker_names")
	vtt := field(manifest, "format") == "vtt"
	var parts []part
	for _, value := range list(field(manifest, "units")) {
		cue := object(value)
		text := str(field(cue, "text"))
		var speaker any
		if declared {
			if name, words, ok := NamedLine(text); ok {
				speaker, text = name, words
			}
		} else if id, ok := field(cue, "cue_id").(string); ok && !allDigits(id) {
			speaker = id
		}
		p := part{label: speaker, raw: text, start: optionalInt(field(cue, "start_ms")),
			end: optionalInt(field(cue, "end_ms")), cue: optionalInt(field(cue, "index"))}
		if !vtt {
			parts = append(parts, p)
			continue
		}
		for _, v := range voices(text, speaker) {
			p.label, p.raw = v.label, v.text
			parts = append(parts, p)
		}
	}
	return parts
}

// optionalInt is an integer field of a checked record, or nil for null.
func optionalInt(v any) *int {
	if v == nil {
		return nil
	}
	n, ok := isInt(v)
	if !ok || !n.IsInt64() {
		panic(invalid)
	}
	return intp(int(n.Int64()))
}

// ---- calls_import.validate_imported_record ----

var (
	recordKeys   = []string{"id", "account_id", "record_kind", "record_version", "called_at", "body", "manifest"}
	manifestKeys = []string{"schema_version", "source", "format", "parser_version", "content_sha256",
		"raw_byte_count", "association", "association_generation", "called_at", "imported_at", "selection_mode",
		"unit_kind", "requested_range", "cited_range", "total_units", "units"}
	cueKeys         = []string{"index", "text", "cue_id", "start_ms", "end_ms"}
	associationKeys = []string{"crm_binding", "account_id", "method", "resolution_receipt_id"}
)

// hasKeys is set(o) == set(keys) for an object, whose keys are never repeated.
func hasKeys(o *strictjson.Object, keys ...string) bool {
	if o == nil || len(o.Keys) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := o.Get(key); !ok {
			return false
		}
	}
	return true
}

// withKey is the keys, and one more when present is set.
func withKey(keys []string, key string, present bool) []string {
	if present {
		return append(append([]string(nil), keys...), key)
	}
	return keys
}

// intIn is calls_import._int: an integer, never a bool, from lo to hi.
func intIn(v any, lo, hi int64) (int64, bool) {
	n, ok := isInt(v)
	if !ok || !n.IsInt64() || n.Int64() < lo || n.Int64() > hi {
		return 0, false
	}
	return n.Int64(), true
}

// SourceText is calls_import._text: a string UTF-8 can encode in at most maximum bytes, with no control
// character but tab, line feed and carriage return, and with something other than whitespace when nonempty.
func SourceText(v any, maximum int, nonempty bool) (string, bool) {
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) || len(s) > maximum {
		return "", false
	}
	blank := true
	for _, c := range s {
		if c <= 0x08 || c == 0x0b || c == 0x0c || c >= 0x0e && c <= 0x1f || c >= 0x7f && c <= 0x9f {
			return "", false
		}
		blank = blank && u.IsSpace(c)
	}
	return s, !(nonempty && blank)
}

// UTCStamp is whether a time is written as its own isoformat in UTC, with Z for the offset
// (calls_import._time): YYYY-MM-DDTHH:MM:SSZ, or with six digits of a fraction that is not zero.
func UTCStamp(value string) bool {
	const form = "0000-00-00T00:00:00"
	if len(value) != len(form)+1 && len(value) != len(form)+8 || value[len(value)-1] != 'Z' {
		return false
	}
	for i := 0; i < len(form); i++ {
		if form[i] == '0' && !isDigit(value[i]) || form[i] != '0' && value[i] != form[i] {
			return false
		}
	}
	if len(value) == len(form)+8 {
		fraction, ok := digits(value[len(form)+1 : len(value)-1])
		if value[len(form)] != '.' || !ok || fraction == 0 {
			return false
		}
	}
	number := func(from, to int) int {
		n, _ := digits(value[from:to])
		return n
	}
	return validDate(number(0, 4), number(5, 7), number(8, 10)) && number(11, 13) <= 23 && number(14, 16) <= 59 &&
		number(17, 19) <= 59
}

// CRMBackend is calls_import._valid_binding: the CRM a binding names, when it is a binding of one.
func CRMBackend(binding any) (string, bool) {
	for _, backend := range []string{"sqlite", "salesforce"} {
		if ValidBinding(binding, "crm_backend", backend) {
			return backend, true
		}
	}
	return "", false
}

// ValidAssociation is calls_import._validate_public_association: how a transcript was bound to its account,
// with no private CRM selector.
func ValidAssociation(v any) bool {
	association, ok := v.(*strictjson.Object)
	if !ok || !hasKeys(association, associationKeys...) {
		return false
	}
	backend, bound := CRMBackend(field(association, "crm_binding"))
	account, isText := field(association, "account_id").(string)
	_, receipt := SourceText(field(association, "resolution_receipt_id"), 128, true)
	method := field(association, "method")
	return bound && isText && ValidAccountID(account, backend == "salesforce") && receipt &&
		(method == "account_id" || method == "account_number" && backend == "salesforce")
}

// unitRange is one of a manifest's ranges: first and last, from 1 to total, in order.
func unitRange(v any, total int64) (first, last int64, ok bool) {
	span, isObject := v.(*strictjson.Object)
	if !isObject || !hasKeys(span, "first", "last") {
		return 0, 0, false
	}
	first, okFirst := intIn(field(span, "first"), 1, total)
	last, okLast := intIn(field(span, "last"), 1, total)
	return first, last, okFirst && okLast && first <= last
}

// timingStamp reads one time of a timing line from i: where it ends and its milliseconds
// (calls_import._milliseconds). A WebVTT time is (?:[0-9]{2,9}:)?[0-5][0-9]:[0-5][0-9]\.[0-9]{3}. An SRT time
// always has its hours and may write a comma before the milliseconds.
func timingStamp(s string, i int, vtt bool) (end int, ms int64, ok bool) {
	// clock is [0-5][0-9]:[0-5][0-9], the separator and [0-9]{3} from j, in milliseconds.
	clock := func(j int) (int, int64, bool) {
		if j+9 > len(s) || s[j] < '0' || s[j] > '5' || !isDigit(s[j+1]) || s[j+2] != ':' || s[j+3] < '0' ||
			s[j+3] > '5' || !isDigit(s[j+4]) || s[j+5] != '.' && (vtt || s[j+5] != ',') || !isDigit(s[j+6]) ||
			!isDigit(s[j+7]) || !isDigit(s[j+8]) {
			return 0, 0, false
		}
		minutes, _ := digits(s[j : j+2])
		seconds, _ := digits(s[j+3 : j+5])
		millis, _ := digits(s[j+6 : j+9])
		return j + 9, int64(minutes*60+seconds)*1000 + int64(millis), true
	}
	hours := i
	for hours < len(s) && hours-i < 10 && isDigit(s[hours]) {
		hours++
	}
	if n := hours - i; n >= 2 && n <= 9 && hours < len(s) && s[hours] == ':' {
		if end, rest, ok := clock(hours + 1); ok {
			count, _ := digits(s[i:hours])
			return end, int64(count)*3_600_000 + rest, true
		}
	}
	if !vtt {
		return 0, 0, false
	}
	return clock(i)
}

// CueTiming is calls_import._TIMING.fullmatch(line) for a WebVTT cue, or _SRT_TIMING for an SRT cue, on a
// line with no line break: two times with an arrow between them, then nothing, or a space and anything. It
// gives the times in milliseconds and what follows the end time.
func CueTiming(line string, vtt bool) (start, end int64, settings string, ok bool) {
	blanks := func(i int) int {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		return i
	}
	i, start, ok := timingStamp(line, 0, vtt)
	if !ok {
		return 0, 0, "", false
	}
	arrow := blanks(i)
	if arrow == i || !strings.HasPrefix(line[arrow:], "-->") {
		return 0, 0, "", false
	}
	second := blanks(arrow + 3)
	if second == arrow+3 {
		return 0, 0, "", false
	}
	i, end, ok = timingStamp(line, second, vtt)
	rest := blanks(i)
	if !ok || rest == i && i < len(line) {
		return 0, 0, "", false
	}
	return start, end, line[rest:], true
}

// CueLine is calls_import._cue_text for one line of a cue's text: the line may not be a timing line, which
// a missing blank line would have merged into the cue.
func CueLine(line string, vtt bool) bool {
	if vtt {
		return !strings.Contains(line, "-->")
	}
	_, _, _, timing := CueTiming(line, false)
	return !timing
}

// cueText is whether every line of a cue's text may be one.
func cueText(text, format string) bool {
	for _, line := range strings.Split(text, "\n") {
		if !CueLine(line, format == "vtt") {
			return false
		}
	}
	return true
}

// QuotedUnits is calls_import._quoted: the units' text as a record's body. A short identifier with a space
// in it names the speaker, so it stays above its cue.
func QuotedUnits(units []*strictjson.Object) string {
	lines := make([]string, len(units))
	for i, unit := range units {
		lines[i] = str(field(unit, "text"))
		if id, ok := field(unit, "cue_id").(string); ok && strings.Contains(id, " ") && length(id) <= maxCueLabel {
			lines[i] = id + "\n" + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// ImportedRecord is calls_import._make_record: the record a manifest makes, named by the digest of the
// manifest's canonical encoding. It is false for a manifest that lacks a field the record is made from.
func ImportedRecord(manifest *strictjson.Object) (record *strictjson.Object, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, rejected := r.(*Rejected); !rejected {
				panic(r)
			}
			record, ok = nil, false
		}
	}()
	if manifest == nil {
		return nil, false
	}
	canonical, err := strictjson.Dumps(manifest, strictjson.Prepared)
	if err != nil {
		return nil, false
	}
	var units []*strictjson.Object
	for _, unit := range list(field(manifest, "units")) {
		units = append(units, object(unit))
	}
	return strictjson.NewObject("id", "note-"+digest(canonical),
		"account_id", field(object(field(manifest, "association")), "account_id"),
		"record_kind", "imported_transcript", "record_version", RecordVersion,
		"called_at", field(manifest, "called_at"), "body", QuotedUnits(units), "manifest", manifest), true
}

// validCue is the check of one selected line or cue: its place in the transcript, its text and, for a cue,
// its times and identifier.
func validCue(unit *strictjson.Object, index int64, format string, declared bool) bool {
	if !hasKeys(unit, cueKeys...) {
		return false
	}
	text, isText := SourceText(field(unit, "text"), MaxRawBytes, false)
	if _, placed := intIn(field(unit, "index"), index, index); !placed || !isText {
		return false
	}
	if declared && len(strip([]rune(text))) > 0 {
		if _, _, ok := NamedCue(text, format); !ok {
			return false
		}
	}
	id, start, end := field(unit, "cue_id"), field(unit, "start_ms"), field(unit, "end_ms")
	if format == "txt" {
		return id == nil && start == nil && end == nil && !strings.ContainsAny(text, "\r\n")
	}
	from, okFrom := intIn(start, 0, maxCueMS)
	to, okTo := intIn(end, 0, maxCueMS)
	if !okFrom || !okTo || to < from || strings.Contains(text, "\r") || !cueText(text, format) {
		return false
	}
	if id == nil {
		return true
	}
	name, ok := SourceText(id, MaxRawBytes, true)
	return ok && !strings.ContainsAny(name, "\r\n") && !strings.Contains(name, "-->") &&
		(format != "srt" || allDigits(name))
}

// ValidImportedRecord is calls_import.validate_imported_record: whether a saved transcript record is the
// one its own manifest makes, for this account under this CRM binding. It checks the selected snapshot
// against itself and reads no stored bytes. A field that is missing or of another type fails the check.
func ValidImportedRecord(record *strictjson.Object, accountID string, binding any) (valid bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, rejected := r.(*Rejected); !rejected {
				panic(r)
			}
			valid = false
		}
	}()
	// A record may carry its reference, and a manifest says speaker_names only when its import declared them,
	// and holds attendees only when its import was given an attendee list.
	_, referenced := record.Get("record_ref")
	backend, bound := CRMBackend(binding)
	if !hasKeys(record, withKey(recordKeys, "record_ref", referenced)...) || !bound ||
		!ValidAccountID(accountID, backend == "salesforce") {
		return false
	}
	manifest := object(field(record, "manifest"))
	stated, declared := manifest.Get("speaker_names")
	attendees, listed := manifest.Get("attendees")
	if declared && stated != true || listed && !ValidAttendees(attendees) ||
		!hasKeys(manifest, withKey(withKey(manifestKeys, "speaker_names", declared), "attendees", listed)...) {
		return false
	}
	format := field(manifest, "format")
	_, okSchema := intIn(field(manifest, "schema_version"), 1, 1)
	_, okParser := intIn(field(manifest, "parser_version"), ParserVersion, ParserVersion)
	if !okSchema || !okParser || field(manifest, "source") != "user_supplied_export" ||
		!oneOf(format, "txt", "vtt", "srt") {
		return false
	}
	digestText, isDigest := field(manifest, "content_sha256").(string)
	_, okBytes := intIn(field(manifest, "raw_byte_count"), 1, MaxRawBytes)
	_, okGeneration := intIn(field(manifest, "association_generation"), 1, 1<<63-1)
	calledAt := field(manifest, "called_at")
	called, calledIsText := calledAt.(string)
	imported, importedIsText := field(manifest, "imported_at").(string)
	if !isDigest || !lowerHex64(digestText) || !okBytes || !okGeneration ||
		calledAt != nil && !(calledIsText && UTCStamp(called)) || !importedIsText || !UTCStamp(imported) {
		return false
	}
	association := field(manifest, "association")
	if !ValidAssociation(association) || !equal(field(object(association), "crm_binding"), binding) ||
		field(object(association), "account_id") != accountID {
		return false
	}
	total, okTotal := intIn(field(manifest, "total_units"), 1, MaxTotalUnits)
	mode := field(manifest, "selection_mode")
	if !okTotal || !oneOf(mode, "default", "explicit") {
		return false
	}
	requestedFirst, requestedLast, okRequested := unitRange(field(manifest, "requested_range"), total)
	citedFirst, citedLast, okCited := unitRange(field(manifest, "cited_range"), total)
	if !okRequested || !okCited || citedFirst != max(1, requestedFirst-1) || citedLast != min(total, requestedLast+1) ||
		mode == "default" && (requestedFirst != 1 || requestedLast > DefaultUnits) {
		return false
	}
	kind := "cues"
	if format == "txt" {
		kind = "lines"
	}
	units, isList := field(manifest, "units").([]any)
	if field(manifest, "unit_kind") != kind || !isList || len(units) < 1 || len(units) > MaxReturnedUnits ||
		int64(len(units)) != citedLast-citedFirst+1 {
		return false
	}
	cues := make([]*strictjson.Object, len(units))
	for i, value := range units {
		unit, isObject := value.(*strictjson.Object)
		if !isObject || !validCue(unit, citedFirst+int64(i), format.(string), declared) {
			return false
		}
		cues[i] = unit
	}
	// The record the manifest makes (calls_import._make_record), value for value and type for type.
	canonical, err := strictjson.Dumps(manifest, strictjson.Prepared)
	if err != nil {
		return false
	}
	id, body := "note-"+digest(canonical), QuotedUnits(cues)
	_, isVersion := intIn(field(record, "record_version"), RecordVersion, RecordVersion)
	if field(record, "id") != id || field(record, "account_id") != accountID ||
		field(record, "record_kind") != "imported_transcript" || !isVersion ||
		field(record, "called_at") != calledAt || field(record, "body") != body {
		return false
	}
	reference := "calls/call_notes/" + id
	if given, _ := record.Get("record_ref"); referenced && given != reference {
		return false
	}
	saved := strictjson.NewObject("id", id, "account_id", accountID, "record_kind", "imported_transcript",
		"record_version", RecordVersion, "called_at", calledAt, "body", body, "manifest", manifest,
		"record_ref", reference)
	encoded, err := strictjson.Dumps(saved, CanonicalASCII)
	return err == nil && len(body) <= MaxQuoteBytes && len(encoded) <= MaxRecordBytes
}

// The bounds of a call's attendee list.
const (
	MaxAttendees      = 200
	maxAttendeeLabel  = 200
	maxEmailBytes     = 254
	maxEmailLocalPart = 64
)

// ValidAttendees is whether a value is an attendee list as an import's manifest holds it: 1 to MaxAttendees
// objects, each with exactly a label and an email, and no row twice. A label is source text that is not
// empty on one line and has at most 200 characters. An email is one ValidEmail accepts.
func ValidAttendees(value any) bool {
	rows, ok := value.([]any)
	if !ok || len(rows) < 1 || len(rows) > MaxAttendees {
		return false
	}
	seen := map[[2]string]bool{}
	for _, value := range rows {
		row, isObject := value.(*strictjson.Object)
		if !isObject || !hasKeys(row, "label", "email") {
			return false
		}
		label, isLabel := SourceText(field(row, "label"), 4*maxAttendeeLabel, true)
		email, isEmail := field(row, "email").(string)
		if !isLabel || oneLine(label) == "" || length(label) > maxAttendeeLabel || !isEmail || !ValidEmail(email) {
			return false
		}
		pair := [2]string{label, email}
		if seen[pair] {
			return false
		}
		seen[pair] = true
	}
	return true
}

// ValidEmail is whether a text is an email address as an attendee list may hold it: at most 254 bytes, one
// "@", a local part of 1 to 64 printable ASCII characters with no space, quote, angle bracket, comma,
// semicolon or backslash, and a domain of lower-case ASCII labels of letters, digits and hyphens, with at
// least one dot.
func ValidEmail(email string) bool {
	local, domain, found := strings.Cut(email, "@")
	if !found || len(email) > maxEmailBytes || len(local) < 1 || len(local) > maxEmailLocalPart ||
		strings.Contains(domain, "@") {
		return false
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte("\"'<>,;\\", c) >= 0 {
			return false
		}
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			if c := label[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
