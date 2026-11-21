package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// sealedEvidence is a new copy of the evidence one oracle card seals.
func sealedEvidence(t *testing.T, family, key string) *strictjson.Object {
	t.Helper()
	for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
		var row struct {
			Key  string `json:"key"`
			Card string `json:"card"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Key != key {
			continue
		}
		card, err := strictjson.LoadsText(row.Card)
		if err != nil {
			t.Fatal(err)
		}
		evidence, _ := card.(*strictjson.Object).Get("evidence")
		return evidence.(*strictjson.Object)
	}
	t.Fatalf("no card %s in %s", key, family)
	return nil
}

// member walks a JSON value by object keys and list positions.
func member(value any, path ...any) any {
	for _, step := range path {
		switch step := step.(type) {
		case string:
			value, _ = value.(*strictjson.Object).Get(step)
		case int:
			value = value.([]any)[step]
		}
	}
	return value
}

func memberObject(value any, path ...any) *strictjson.Object {
	return member(value, path...).(*strictjson.Object)
}

// recordOf is the evidence's first record of one source table, such as "calls/call_notes".
func recordOf(evidence *strictjson.Object, table string) *strictjson.Object {
	for _, value := range member(evidence, "records").([]any) {
		if ref, _ := member(value, "record_ref").(string); strings.HasPrefix(ref, table+"/") {
			return value.(*strictjson.Object)
		}
	}
	return nil
}

// receiptOf is the evidence's receipt of one tool.
func receiptOf(evidence *strictjson.Object, tool string) *strictjson.Object {
	for _, value := range member(evidence, "receipts").([]any) {
		if member(value, "tool") == tool {
			return value.(*strictjson.Object)
		}
	}
	return nil
}

// remake gives a transcript record the ID its changed manifest makes, as calls_import._make_record does,
// and puts its new reference in the read's receipt. Only the checks of the manifest itself can then refuse it.
func remake(t *testing.T, evidence *strictjson.Object) {
	t.Helper()
	record := recordOf(evidence, "calls/call_notes")
	canonical, err := strictjson.Dumps(member(record, "manifest"), strictjson.Prepared)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(canonical))
	id := "note-" + hex.EncodeToString(sum[:])
	record.Set("id", id)
	record.Set("record_ref", "calls/call_notes/"+id)
	receiptOf(evidence, "read_call_notes").Set("rows", []any{"calls/call_notes/" + id})
}

const (
	otherRevision = "Imported transcript evidence does not match its immutable selected revision."
	otherBinding  = "Imported evidence requires one supported CRM binding for both sources."
	plainExport   = "exports-gemini/0"               // WebVTT, 14 numbered cues, read whole
	namedExport   = "exports-gemini/0 speaker names" // the same file, imported as naming every speaker
	voicedExport  = "exports-gemini/8"               // WebVTT whose cues open with a voice tag, not a name
)

// importedRefusals are the changes to a saved transcript record, or to its bindings, that
// the removed Python suite refused, each with
// the refusal units.build gives for it. A change marked remade has the ID of its changed manifest, so only
// the check of the changed field can refuse it.
var importedRefusals = []struct {
	name   string
	card   string
	change func(evidence, record, manifest *strictjson.Object)
	remade bool
	want   string
}{
	{"forged body", plainExport, func(_, r, _ *strictjson.Object) { r.Set("body", "Forged text") }, false, otherRevision},
	{"forged unit text", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "units", 0).Set("text", "Forged selected unit")
	}, false, otherRevision},
	{"changed cue time", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "units", 0).Set("start_ms", 1)
	}, false, otherRevision},
	{"shorter cited range", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "cited_range").Set("last", 3)
	}, false, otherRevision},
	{"later cited range", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "cited_range").Set("first", 2)
	}, false, otherRevision},
	{"other content digest", plainExport, func(_, _, m *strictjson.Object) {
		m.Set("content_sha256", strings.Repeat("f", 64))
	}, false, otherRevision},
	{"forged resolution receipt", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "association").Set("resolution_receipt_id", "forged-receipt")
	}, false, otherRevision},
	{"another account in the association", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "association").Set("account_id", "acc-008")
	}, false, otherRevision},
	{"wrong reference", plainExport, func(_, r, _ *strictjson.Object) {
		r.Set("record_ref", "calls/call_notes/note-wrong")
	}, false, "Record reference does not identify its source."},
	{"original text on the record", plainExport, func(_, r, _ *strictjson.Object) {
		r.Set("original_text", "forbidden original")
	}, false, otherRevision},
	{"source bundle on the record", plainExport, func(_, r, _ *strictjson.Object) {
		r.Set("source_bundle", strictjson.NewObject("raw", "Zm9yYmlkZGVu"))
	}, false, otherRevision},
	{"parser version true", plainExport, func(_, _, m *strictjson.Object) { m.Set("parser_version", true) }, false,
		otherRevision},
	{"parser version true, remade", plainExport, func(_, _, m *strictjson.Object) { m.Set("parser_version", true) },
		true, otherRevision},
	{"unit index true", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "units", 0).Set("index", true)
	}, false, otherRevision},
	{"unit index true, remade", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "units", 0).Set("index", true)
	}, true, otherRevision},
	{"another CRM identity for both sources", plainExport, func(e, _, _ *strictjson.Object) {
		memberObject(e, "crm_binding").Set("identity_sha256", strings.Repeat("c", 64))
		memberObject(e, "calls_binding").Set("identity_sha256", strings.Repeat("c", 64))
	}, false, otherRevision},
	{"speaker names stated over unnamed cues", voicedExport, func(_, _, m *strictjson.Object) {
		m.Set("speaker_names", true)
	}, true, otherRevision},
	{"speaker names stated false", plainExport, func(_, _, m *strictjson.Object) { m.Set("speaker_names", false) },
		true, otherRevision},
	{"speaker names false over named cues", namedExport, func(_, _, m *strictjson.Object) {
		m.Set("speaker_names", false)
	}, true, otherRevision},
	{"speaker names 1", namedExport, func(_, _, m *strictjson.Object) { m.Set("speaker_names", 1) }, true,
		otherRevision},
	{"speaker names as text", namedExport, func(_, _, m *strictjson.Object) { m.Set("speaker_names", "true") }, true,
		otherRevision},
	{"speaker names null", namedExport, func(_, _, m *strictjson.Object) { m.Set("speaker_names", nil) }, true,
		otherRevision},
	{"original text in the manifest", plainExport, func(_, _, m *strictjson.Object) {
		m.Set("original_text", "Unselected private text.")
	}, false, otherRevision},
	{"original text in the manifest, remade", plainExport, func(_, _, m *strictjson.Object) {
		m.Set("original_text", "Unselected private text.")
	}, true, otherRevision},
	{"guessed speaker on a unit, remade", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "units", 0).Set("speaker", "Guessed speaker")
	}, true, otherRevision},
	{"private account number in the association, remade", plainExport, func(_, _, m *strictjson.Object) {
		memberObject(m, "association").Set("account_number", "PRIVATE-INVENTED-1")
	}, true, otherRevision},
	{"another identity in the calls binding", plainExport, func(e, _, _ *strictjson.Object) {
		memberObject(e, "calls_binding").Set("identity_sha256", strings.Repeat("0", 64))
	}, false, otherBinding},
	{"another identity in the CRM binding", plainExport, func(e, _, _ *strictjson.Object) {
		memberObject(e, "crm_binding").Set("identity_sha256", strings.Repeat("0", 64))
	}, false, otherBinding},
	{"no CRM binding", plainExport, func(e, _, _ *strictjson.Object) { e.Set("crm_binding", nil) }, false,
		otherBinding},
	{"original text in the evidence", plainExport, func(e, _, _ *strictjson.Object) {
		e.Set("original_text", "Unselected private text.")
	}, false, "Evidence has an unsupported shape."},
	{"no manifest", plainExport, func(_, r, _ *strictjson.Object) { r.Delete("manifest") }, false,
		"An imported transcript requires its declared source binding."},
}

// changedEvidence is the evidence of one refusal: its card's evidence, changed, and remade when it says so.
func changedEvidence(t *testing.T, i int) *strictjson.Object {
	t.Helper()
	c := importedRefusals[i]
	evidence := sealedEvidence(t, "books_calls", c.card)
	record := recordOf(evidence, "calls/call_notes")
	c.change(evidence, record, memberObject(record, "manifest"))
	if c.remade {
		remake(t, evidence)
	}
	return evidence
}

func TestImportedTranscriptRefusals(t *testing.T) {
	for _, key := range []string{plainExport, namedExport} {
		if _, _, _, _, err := Build(sealedEvidence(t, "books_calls", key)); err != nil {
			t.Fatalf("%s as sealed: %v", key, err)
		}
	}
	for i, c := range importedRefusals {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, _, err := Build(changedEvidence(t, i)); err == nil || err.Error() != c.want {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

// secondTranscript is evidence with two valid transcript records of one account: the same file as imported
// plainly, and as imported again as naming its speakers.
func secondTranscript(t *testing.T) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "books_calls", plainExport)
	second := recordOf(sealedEvidence(t, "books_calls", namedExport), "calls/call_notes")
	evidence.Set("records", append(member(evidence, "records").([]any), second))
	receipt := receiptOf(evidence, "read_call_notes")
	receipt.Set("rows", append(member(receipt, "rows").([]any), member(second, "record_ref")))
	receipt.Set("row_count", 2)
	return evidence
}

func TestOneTranscriptForAnAccount(t *testing.T) {
	const want = "Imported calls hold at most one selected transcript excerpt for an account."
	if _, _, _, _, err := Build(secondTranscript(t)); err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// TestRemadeTranscriptIsAccepted: a manifest changed to another valid one and remade is a valid record, so
// the remade refusals above are refused for their changed field and not for a stale ID.
func TestRemadeTranscriptIsAccepted(t *testing.T) {
	evidence := sealedEvidence(t, "books_calls", plainExport)
	record := recordOf(evidence, "calls/call_notes")
	memberObject(record, "manifest").Set("called_at", "2027-01-10T10:00:00Z")
	record.Set("called_at", "2027-01-10T10:00:00Z")
	if _, _, _, _, err := Build(evidence); err == nil || err.Error() != otherRevision {
		t.Fatalf("a changed manifest under its old ID: %v", err)
	}
	remake(t, evidence)
	_, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "] call 2027-01-10T10:00:00Z; ") {
		t.Errorf("the card does not print the call's time:\n%s", text)
	}
}

// TestVoices is the removed Python suite's test_each_voice_tag_of_a_cue_labels_what_follows_it.
func TestVoices(t *testing.T) {
	type span = [2]any
	for _, c := range []struct {
		text  string
		label any
		want  []span
	}{
		{"<v Dana Ito>Fine.</v>\n<v.loud Morgan Reed>They agreed.</v>", nil,
			[]span{{"Dana Ito", "Fine."}, {"Morgan Reed", "They agreed."}}},
		// A closing tag ends the voice. What follows belongs to the cue's own label again.
		{"<v Dana Ito>Fine.</v> and so on <v Sam Lee>Yes.", "cue",
			[]span{{"Dana Ito", "Fine."}, {"cue", " and so on "}, {"Sam Lee", "Yes."}}},
		// Text before the first tag keeps the cue's own label. A tag with no text after it says nothing.
		{"Hello <v Dana Ito>there<v Sam Lee> </v>", "intro", []span{{"intro", "Hello "}, {"Dana Ito", "there"}}},
		// Styling spans and cue timestamps are markup. Any other tag is text and stays.
		{"<v Dana Ito>We <b>will</b> <00:01:05.000>sign <script>.</v>", nil,
			[]span{{"Dana Ito", "We will sign <script>."}}},
		// Removing a tag never makes a voice tag: this text has no speaker but the cue's own.
		{"<v<i> Dana Ito>We approve.</v>", "cue", []span{{"cue", "<v Dana Ito>We approve."}}},
		{"<<b>v Sam Lee>We refund.", "cue", []span{{"cue", "<v Sam Lee>We refund."}}},
		// Only a language span carries an annotation, so a word inside a tag is never dropped. A ruby reading is.
		{"<v Dana Ito>We will <b not> sign <c 2 million> <u>now</u> <lang en-GB>ok</lang>: " +
			"<ruby>WWW<rt>World Wide Web</rt></ruby> is down.</v>", nil,
			[]span{{"Dana Ito", "We will <b not> sign <c 2 million> now ok: WWW is down."}}},
		{"no tags here", nil, []span{{nil, "no tags here"}}},
		{"<v Dana Ito></v>", nil, nil},
	} {
		var got []span
		for _, v := range voices(c.text, c.label) {
			got = append(got, span{v.label, v.text})
		}
		if len(got) != len(c.want) {
			t.Errorf("voices(%q): %v, want %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("voices(%q): %v, want %v", c.text, got, c.want)
			}
		}
	}
}

// transcriptEvidence is the evidence of a card that read one imported text transcript: an oracle card with no
// transcript, with a record made for these lines of the file added to it. first and last are the lines asked
// for; the record cites one line of context on each side.
func transcriptEvidence(t *testing.T, lines []string, first, last int) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "books_calls", "sol/1")
	account := member(evidence, "account_id").(string)
	from, to := max(1, first-1), min(len(lines), last+1)
	var units []any
	for index := from; index <= to; index++ {
		units = append(units, strictjson.NewObject("index", index, "text", lines[index-1], "cue_id", nil,
			"start_ms", nil, "end_ms", nil))
	}
	raw := strings.Join(lines, "\n") + "\n"
	sum := sha256.Sum256([]byte(raw))
	manifest := strictjson.NewObject("schema_version", 1, "source", "user_supplied_export", "format", "txt",
		"parser_version", 2, "content_sha256", hex.EncodeToString(sum[:]), "raw_byte_count", len(raw),
		"association", strictjson.NewObject("crm_binding", strictjson.Clone(member(evidence, "calls_binding")),
			"account_id", account, "method", "account_id",
			"resolution_receipt_id", "00000000-0000-4000-8000-000000000001"),
		"association_generation", 1, "called_at", nil, "imported_at", "2027-01-15T09:00:00Z",
		"selection_mode", "explicit", "unit_kind", "lines",
		"requested_range", strictjson.NewObject("first", first, "last", last),
		"cited_range", strictjson.NewObject("first", from, "last", to), "total_units", len(lines), "units", units)
	// remake gives the record its ID and its reference.
	record := strictjson.NewObject("id", "", "account_id", account, "record_kind", "imported_transcript",
		"record_version", 2, "called_at", nil, "body", strings.Join(lines[from-1:to], "\n"), "manifest", manifest,
		"record_ref", "calls/call_notes/")
	evidence.Set("records", append(member(evidence, "records").([]any), record))
	receiptOf(evidence, "read_call_notes").Set("row_count", 1)
	remake(t, evidence)
	return evidence
}

// TestExcerptEdgeCutsASentence is the removed Python suite's
// test_a_sentence_cut_by_the_edge_of_an_excerpt_is_counted_and_not_shown: lines 3 and 4 are asked for, so
// lines 2 to 5 are read, and the sentences the excerpt's edges cut are counted and not shown.
func TestExcerptEdgeCutsASentence(t *testing.T) {
	evidence := transcriptEvidence(t, []string{"We", "will not sign until the rider is approved.", "Good.", "Fine.",
		"The price is acceptable if", "legal approves."}, 3, 4)
	units, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	type spoken struct {
		text    string
		partial bool
	}
	var got []spoken
	for _, u := range units {
		if u.Kind == "sentence" {
			got = append(got, spoken{u.Text, u.Partial})
		}
	}
	want := []spoken{{"will not sign until the rider is approved.", true}, {"Good.", false}, {"Fine.", false},
		{"The price is acceptable if", true}}
	if len(got) != len(want) {
		t.Fatalf("sentences %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d: %v, want %v", i+1, got[i], want[i])
		}
	}
	said := strings.SplitN(strings.SplitN(text, "\nSAID", 2)[1], "\n\n", 2)[0]
	var shown []string
	for _, line := range strings.Split(said, "\n") {
		if strings.HasPrefix(line, "        ") {
			shown = append(shown, strings.TrimSpace(line))
		}
	}
	if strings.Join(shown, " ") != `"Good." "Fine."` {
		t.Errorf("SAID shows %v", shown)
	}
	for _, line := range []string{
		"Calls: one transcript excerpt was read, lines 2 to 5 of 6. The rest of that call and every other call " +
			"were not read.",
		"Sentences: 2 cut off at a pause, a change of speaker, a break before their end, or the edge of the " +
			"excerpt were read and are not shown (2 unknown).",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the card lacks %q:\n%s", line, text)
		}
	}
	for _, hidden := range []string{"will not sign", "The price is acceptable"} {
		if strings.Contains(text, hidden) {
			t.Errorf("the card shows a cut sentence: %q", hidden)
		}
	}
}

// In an import that states speaker names, the name is the one label of a cue. A WebVTT cue whose words
// after the name hold a voice tag would give the card a second speaker to take the role from, so a saved
// record of that shape is refused, remade whole so that nothing else about it is wrong.
func TestAStatedNameIsTheOnlyLabelOfACue(t *testing.T) {
	for words, want := range map[string]string{
		"These words are changed.":                           "",
		"<v Dorian Pike>We approved the renewal.</v>":        otherRevision,
		"We approved the renewal.</v>":                       otherRevision,
		"We approved. <v.loud Dorian Pike>And we signed it.": otherRevision,
	} {
		evidence := sealedEvidence(t, "books_calls", namedExport)
		record := recordOf(evidence, "calls/call_notes")
		unit := memberObject(record, "manifest", "units", 0)
		spoken := member(unit, "text").(string)
		name, _, named := NamedLine(spoken)
		if !named || member(record, "manifest", "format") != "vtt" {
			t.Fatalf("%s does not open with a name in a WebVTT cue", namedExport)
		}
		changed := name + ": " + words
		unit.Set("text", changed)
		record.Set("body", strings.Replace(member(record, "body").(string), spoken, changed, 1))
		remake(t, evidence)
		_, _, _, _, err := Build(evidence)
		if want == "" && err != nil || want != "" && (err == nil || err.Error() != want) {
			t.Errorf("%q after the name: %v, want %q", words, err, want)
		}
	}
}

// The scan for voice tags reads each run of class characters once. A cue of tags that open and never close
// has one such run through all of them, and none of them is a tag: a scan from every one of them would read
// the run to its end each time, 600 million characters for the 60,000 here.
func TestVoiceTagsAreFoundInLinearTime(t *testing.T) {
	const starts = 20_000
	for _, c := range []struct{ start, end string }{{"<v.", ""}, {"<v.", ">said"}, {"<v.loud.fast", "\nsaid"},
		{"<v.<c.", ""}} {
		text := []rune(strings.Repeat(c.start, starts) + c.end)
		scanned := 0
		if found := voiceTags(text, &scanned); len(found) != 0 {
			t.Errorf("%d tags found in %d starts of %q", len(found), starts, c.start)
		}
		if scanned > 2*len(text) {
			t.Errorf("%d characters read by the class scans of %d, want at most twice the text", scanned, len(text))
		}
	}
	// What the scan finds is unchanged: every tag of a cue with classes, names and a closing tag.
	text := []rune("<v.a.b Dana>one</v> two <v Morgan Reed>three <v.x>four<v  >five")
	var names []string
	for _, tag := range VoiceTags(text) {
		names = append(names, string(text[tag.NameStart:tag.NameEnd]))
	}
	if got := strings.Join(names, "|"); got != "Dana||Morgan Reed| " {
		t.Errorf("the tags name %q", got)
	}
}
