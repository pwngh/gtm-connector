package calls

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The attendee file of debug calls import, and the attendee list an import's manifest holds. Every name and
// address here is invented.

const attendeeFile = `{"schema_version": 1, "attendees": [` +
	`{"label": "Dana Crest", "email": "dana@acme.example"}, ` +
	`{"label": "Morgan", "email": "morgan.reed@northwind.example"}]}`

func TestAnAttendeeFileIsReadStrictly(t *testing.T) {
	got, err := ParseAttendees([]byte(attendeeFile))
	if err != nil || len(got) != 2 || got[0] != (Attendee{"Dana Crest", "dana@acme.example"}) ||
		got[1] != (Attendee{"Morgan", "morgan.reed@northwind.example"}) {
		t.Fatalf("the file read as %+v, %v", got, err)
	}
	row := func(label, email string) string {
		text, _ := strictjson.Dumps(strictjson.NewObject("label", label, "email", email), strictjson.Quoted)
		return text
	}
	file := func(rows ...string) string {
		return `{"schema_version": 1, "attendees": [` + strings.Join(rows, ", ") + `]}`
	}
	many := make([]string, evidence.MaxAttendees+1)
	for i := range many {
		many[i] = row("Guest", fmt.Sprintf("guest%d@acme.example", i))
	}
	if _, err := ParseAttendees([]byte(file(many[:evidence.MaxAttendees]...))); err != nil {
		t.Errorf("200 attendees ended with %v", err)
	}
	if _, err := ParseAttendees([]byte(file(row(strings.Repeat("é", 200), "x@acme.example")))); err != nil {
		t.Errorf("a label of 200 characters ended with %v", err)
	}
	for name, raw := range map[string]string{
		"not JSON":              `{"schema_version": 1,`,
		"a list":                `[]`,
		"another version":       `{"schema_version": 2, "attendees": [` + row("Dana", "dana@acme.example") + `]}`,
		"a version as text":     `{"schema_version": "1", "attendees": [` + row("Dana", "dana@acme.example") + `]}`,
		"a version as true":     `{"schema_version": true, "attendees": [` + row("Dana", "dana@acme.example") + `]}`,
		"another key":           `{"schema_version": 1, "attendees": [], "room": "B"}`,
		"no rows":               file(),
		"too many rows":         file(many...),
		"a row with more":       file(`{"label": "Dana", "email": "dana@acme.example", "title": "CFO"}`),
		"a row without email":   file(`{"label": "Dana"}`),
		"a label not text":      file(`{"label": 7, "email": "dana@acme.example"}`),
		"an email not text":     file(`{"label": "Dana", "email": null}`),
		"a blank label":         file(row(" \t ", "dana@acme.example")),
		"a long label":          file(row(strings.Repeat("a", 201), "dana@acme.example")),
		"a control in a label":  file(row("Dana\x01", "dana@acme.example")),
		"the same row twice":    file(row("Dana", "dana@acme.example"), row("Dana", "dana@acme.example")),
		"no at":                 file(row("Dana", "dana.acme.example")),
		"two ats":               file(row("Dana", "dana@x@acme.example")),
		"an empty local part":   file(row("Dana", "@acme.example")),
		"a long local part":     file(row("Dana", strings.Repeat("d", 65)+"@acme.example")),
		"a space":               file(row("Dana", "dana crest@acme.example")),
		"a quote":               file(row("Dana", `"dana"@acme.example`)),
		"an angle bracket":      file(row("Dana", "<dana@acme.example>")),
		"a comma":               file(row("Dana", "dana,crest@acme.example")),
		"a non-ASCII local":     file(row("Dana", "dané@acme.example")),
		"an upper-case domain":  file(row("Dana", "dana@Acme.example")),
		"a domain with no dot":  file(row("Dana", "dana@localhost")),
		"an empty domain label": file(row("Dana", "dana@acme..example")),
		"a hyphen at the edge":  file(row("Dana", "dana@-acme.example")),
		"a long address":        file(row("Dana", "d@"+strings.Repeat("a", 63)+"."+strings.Repeat("b", 63)+"."+strings.Repeat("c", 63)+"."+strings.Repeat("d", 63)+".example")),
	} {
		if _, err := ParseAttendees([]byte(raw)); err != ErrAttendeesInvalid {
			t.Errorf("%s ended with %v", name, err)
		}
	}
	// Two people behind one label, as in a meeting room, are two rows.
	if _, err := ParseAttendees([]byte(file(row("Room B", "dana@acme.example"), row("Room B", "lee@acme.example")))); err != nil {
		t.Errorf("a room ended with %v", err)
	}
}

func TestAnAttendeeFileIsReadThroughNoLinkAndWithinItsLimit(t *testing.T) {
	folder := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got, err := ReadAttendees(write("attendees.json", attendeeFile)); err != nil || len(got) != 2 {
		t.Errorf("the file read as %+v, %v", got, err)
	}
	padded := attendeeFile + strings.Repeat(" ", MaxAttendeeFileBytes-len(attendeeFile))
	if _, err := ReadAttendees(write("limit.json", padded)); err != nil {
		t.Errorf("a file at the limit ended with %v", err)
	}
	if _, err := ReadAttendees(write("over.json", padded+" ")); err != ErrAttendeesTooLarge {
		t.Errorf("a file over the limit ended with %v", err)
	}
	link := filepath.Join(folder, "link.json")
	if err := os.Symlink(filepath.Join(folder, "attendees.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAttendees(link); err != ErrAttendeesInvalid {
		t.Errorf("a link ended with %v", err)
	}
	if _, err := ReadAttendees(filepath.Join(folder, "missing.json")); err != ErrIO {
		t.Errorf("a missing file ended with %v", err)
	}
	if ErrAttendeesInvalid.ExitCode() != 2 || ErrAttendeesTooLarge.ExitCode() != 2 {
		t.Error("a bad attendee file is not a usage refusal")
	}
}

func TestTheAttendeeListIsPartOfTheImport(t *testing.T) {
	plain := imported(t, webVTT, "vtt", &Range{2, 2})
	listed := plain
	var err error
	if listed.Attendees, err = ParseAttendees([]byte(attendeeFile)); err != nil {
		t.Fatal(err)
	}
	if err := listed.Check(); err != nil {
		t.Fatal(err)
	}
	revision, manifest, err := listed.Revision(1)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := manifest.Get("attendees")
	shown, _ := strictjson.Dumps(rows, strictjson.Quoted)
	if shown != `[{"email": "dana@acme.example", "label": "Dana Crest"}, `+
		`{"email": "morgan.reed@northwind.example", "label": "Morgan"}]` {
		t.Errorf("the manifest holds %s", shown)
	}
	without, bare, _ := plain.Revision(1)
	if _, present := bare.Get("attendees"); present || revision.ImportID == without.ImportID ||
		listed.RequestKey(1) == plain.RequestKey(1) {
		t.Error("the attendee list does not make another import")
	}
	other := listed
	other.Attendees = listed.Attendees[:1]
	if other.RequestKey(1) == listed.RequestKey(1) {
		t.Error("another attendee list makes the same request")
	}
	opened, err := revision.Open(testBinding)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.ValidImportedRecord(opened.Record, "acc-007", testBinding) {
		t.Error("the record with attendees does not pass the check a card makes")
	}
	result := Result(manifest, revision.ImportID, true, false, "invented-receipt-one")
	if count, _ := result.Get("attendees"); !evidence.Equal(count, 2) {
		t.Errorf("the result counts %v attendees", count)
	}
	if _, present := Result(bare, without.ImportID, true, false, "r").Get("attendees"); present {
		t.Error("a result without a list counts attendees")
	}
	text, _ := strictjson.Dumps(result, strictjson.Quoted)
	if strings.Contains(text, "acme.example") || strings.Contains(text, "Dana Crest") {
		t.Errorf("the result shows a row: %s", text)
	}

	// An import with a bad list is refused before it is saved, and a saved manifest whose list a writer
	// changed is corrupt.
	bad := listed
	bad.Attendees = []Attendee{{"Dana", "not an address"}}
	if err := bad.Check(); err != ErrInvalid {
		t.Errorf("a bad list ended with %v", err)
	}
	empty := listed
	empty.Attendees = []Attendee{}
	if err := empty.Check(); err != ErrInvalid {
		t.Errorf("an empty list ended with %v", err)
	}
	forged := revision
	forged.Manifest = strings.Replace(forged.Manifest, "dana@acme.example", "dana@acme", 1)
	forged.ImportID = "note-" + digest(forged.Manifest)
	if _, err := forged.Open(testBinding); err != ErrCorrupt {
		t.Errorf("a forged list ended with %v", err)
	}
	record := strictjson.Clone(opened.Record).(*strictjson.Object)
	inner, _ := record.Get("manifest")
	inner.(*strictjson.Object).Set("attendees", []any{})
	if evidence.ValidImportedRecord(record, "acc-007", testBinding) {
		t.Error("a record with an empty list passes")
	}
}
