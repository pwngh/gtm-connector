package calls

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The parser and the excerpt, as tests/test_calls_import.py's ParsingTests hold calls_import.py to them, and
// the record of an import. Every file here is invented.

// Characters written by their UTF-8 bytes, so each is visible in this file.
const (
	bom           = "\xef\xbb\xbf"
	noBreakSpace  = "\xc2\xa0"
	emSpace       = "\xe2\x80\x83"
	lineSeparator = "\xe2\x80\xa8"
	nextLine      = "\xc2\x85"
	arabicThree   = "\xd9\xa3"
)

func parsed(t *testing.T, raw, format string) *Parsed {
	t.Helper()
	p, err := Parse([]byte(raw), format, false)
	if err != nil {
		t.Fatalf("%q does not parse as %s: %v", raw, format, err)
	}
	return p
}

func refused(t *testing.T, raw, format string, want *Error) {
	t.Helper()
	if _, err := Parse([]byte(raw), format, false); err != want {
		t.Errorf("%q as %s ended with %v, want %v", raw, format, err, want)
	}
}

func texts(units []Unit) []string {
	found := make([]string, len(units))
	for i, unit := range units {
		found[i] = unit.Text
	}
	return found
}

// cue is a unit as (identifier, text, start, end), with "" for no identifier.
func cue(unit Unit) string {
	id := ""
	if unit.CueID != nil {
		id = *unit.CueID
	}
	return fmt.Sprintf("%s|%s|%d|%d", id, unit.Text, *unit.StartMS, *unit.EndMS)
}

func same(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLinesAndCuesAreReadAsTheFileHasThem(t *testing.T) {
	raw := bom + "Caf\xc3\xa9\r\n\r\nA separator stays here: " + lineSeparator + ".\rLast\r"
	p := parsed(t, raw, "txt")
	same(t, texts(p.Units), []string{"Caf\xc3\xa9", "", "A separator stays here: " + lineSeparator + ".", "Last"})
	if string(p.Raw) != raw || p.Units[3].Index != 4 || p.Units[0].CueID != nil || p.Units[0].StartMS != nil {
		t.Errorf("the file reads as %+v", p)
	}
	if sum := sha256.Sum256([]byte(raw)); p.ContentSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("the digest is %s", p.ContentSHA256)
	}
	same(t, len(parsed(t, "a\n\n", "txt").Units), 2)

	vtt := parsed(t, bom+"WEBVTT\r\n\r\nopening\r\n00:00.000 --> 00:04.000\r\n<v Morgan>We need a revised seat count.\r\n"+
		"The renewal is approaching.</v>\r\n\r\n00:00:03.500 --> 01:00:06.250\r\nSecond.\r\n", "vtt")
	same(t, cue(vtt.Units[0]), "opening|<v Morgan>We need a revised seat count.\nThe renewal is approaching.</v>|0|4000")
	same(t, cue(vtt.Units[1]), "|Second.|3500|3606250")
}

func TestTheExcerptIsTheRangeWithOneUnitOnEachSide(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("Unit %d", i))
	}
	p := parsed(t, strings.Join(lines, "\n"), "txt")
	chosen, err := SelectExcerpt(p, nil)
	if err != nil || chosen.Requested != (Range{1, 20}) || chosen.Cited != (Range{1, 21}) || len(chosen.Units) != 21 ||
		chosen.SelectionMode != "default" || chosen.UnitKind != "lines" || chosen.TotalUnits != 30 {
		t.Fatalf("the default excerpt is %+v (%v)", chosen, err)
	}
	chosen, err = SelectExcerpt(p, &Range{10, 12})
	if err != nil || chosen.Cited != (Range{9, 13}) || chosen.Body != "Unit 9\nUnit 10\nUnit 11\nUnit 12\nUnit 13" ||
		chosen.SelectionMode != "explicit" {
		t.Fatalf("lines 10 to 12 give %+v (%v)", chosen, err)
	}
	if chosen, err = SelectExcerpt(p, &Range{30, 30}); err != nil || chosen.Cited != (Range{29, 30}) {
		t.Fatalf("the last line gives %+v (%v)", chosen, err)
	}
	for _, span := range []Range{{0, 1}, {2, 1}, {1, 31}, {31, 31}, {-1, 2}} {
		if _, err := SelectExcerpt(p, &span); err != ErrInvalid {
			t.Errorf("the range %v ended with %v", span, err)
		}
	}
}

func TestALimitShortensTheDefaultExcerptAndRefusesARange(t *testing.T) {
	p := parsed(t, strings.Repeat("a", 8000)+"\n"+strings.Repeat("b", 8000)+"\n"+strings.Repeat("c", 8000), "txt")
	chosen, err := SelectExcerpt(p, nil)
	if err != nil || chosen.Requested != (Range{1, 1}) || chosen.Cited != (Range{1, 2}) {
		t.Fatalf("the default excerpt of three long lines is %+v (%v)", chosen, err)
	}
	if _, err := SelectExcerpt(p, &Range{2, 2}); err != ErrExcerptTooLarge {
		t.Errorf("a range over the quoted text limit ended with %v", err)
	}
	if _, err := SelectExcerpt(parsed(t, strings.Repeat("a", evidence.MaxQuoteBytes+1), "txt"), nil); err != ErrExcerptTooLarge {
		t.Errorf("one line over the quoted text limit ended with %v", err)
	}
	blank := parsed(t, "a\n"+strings.Repeat("\n", 300), "txt")
	if _, err := SelectExcerpt(blank, &Range{1, 256}); err != ErrExcerptTooLarge {
		t.Errorf("257 units with context ended with %v", err)
	}
	if _, err := SelectExcerpt(blank, &Range{1, 300}); err != ErrExcerptTooLarge {
		t.Errorf("300 units ended with %v", err)
	}
	if chosen, err := SelectExcerpt(blank, &Range{1, 255}); err != nil || len(chosen.Units) != 256 {
		t.Errorf("256 units with context ended with %v", err)
	}
	// An identifier as long as a record may be is refused before anything is saved.
	long := parsed(t, "WEBVTT\n\n"+strings.Repeat("a", evidence.MaxRecordBytes)+"\n00:00.000 --> 00:01.000\nbrief\n", "vtt")
	if _, err := SelectExcerpt(long, nil); err != ErrExcerptTooLarge {
		t.Errorf("a cue with a huge identifier ended with %v", err)
	}
}

func TestAFileThatIsNotATranscriptIsRefusedByAFixedCode(t *testing.T) {
	for _, raw := range []string{"", " \n\t", bom, "\xff", "a\x00", "a\x1b", "a" + nextLine + "b", "\xed\xa0\x80"} {
		refused(t, raw, "txt", ErrInvalid)
	}
	refused(t, "one", "docx", ErrInvalid)
	refused(t, strings.Repeat("x", evidence.MaxRawBytes+1), "txt", ErrTooLarge)
	refused(t, strings.Repeat("x\n", evidence.MaxTotalUnits+1), "txt", ErrTooLarge)
	same(t, len(parsed(t, strings.Repeat("x\n", evidence.MaxTotalUnits), "txt").Units), evidence.MaxTotalUnits)
	manyCues := "WEBVTT\n\n" + strings.Repeat("00:00.000 --> 00:01.000\nx\n\n", evidence.MaxTotalUnits+1)
	refused(t, manyCues, "vtt", ErrTooLarge)
	if ErrInvalid.Error() != "calls_import_invalid" || ErrInvalid.ExitCode() != 2 || ErrInvalid.Category() != "usage" {
		t.Errorf("an invalid file is %v, exit %d", ErrInvalid, ErrInvalid.ExitCode())
	}
	exits := map[*Error]int{ErrTooLarge: 2, ErrExcerptTooLarge: 2, ErrSpeakerNamesMissing: 2, ErrNotFound: 2,
		ErrAccountNotFound: 2, ErrReassignRequired: 3, ErrAssociationChanged: 3, ErrSourceMismatch: 3,
		ErrAccountAmbiguous: 3, ErrCorrupt: 4, ErrAssociationInvalid: 4, ErrAssociationFailed: 4,
		ErrSourceUnavailable: 5, ErrIO: 1}
	for refusal, exit := range exits {
		if refusal.ExitCode() != exit {
			t.Errorf("%v exits %d, want %d", refusal, refusal.ExitCode(), exit)
		}
	}
}

func TestOnlyMetadataLinesMayFollowTheWebVTTSignature(t *testing.T) {
	said := "\n\n00:00.000 --> 00:01.000\nDana: hello\n"
	for _, header := range []string{"WEBVTT", "WEBVTT\nKind: captions\nLanguage: en", "WEBVTT - meeting\nLanguage: pt-BR",
		"WEBVTT\tmeeting", "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000",
		"WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0", "WEBVTT\nLanguage: zh-Hant-TW"} {
		same(t, texts(parsed(t, header+said, "vtt").Units), []string{"Dana: hello"})
	}
	// A line that could be speech is never dropped as a header.
	for _, header := range []string{"WEBVTT\nDana: this was said", "WEBVTT\nKind: captions\nspoken text",
		"WEBVTT\nKind: We must wait for procurement.", "WEBVTT\nLanguage: we never agreed",
		"WEBVTT\nX-TIMESTAMP-MAP=the budget is frozen", "WEBVTT\nLanguage: e", "WEBVTT\nLanguage: en-",
		"WEBVTT\nLanguage: en-abcdefghi", "WEBVTT\nKind: captions ", "WEBVTTX", "WEBVTT --> x", "BAD",
		"WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:1,LOCAL:1,MPEGTS:2"} {
		refused(t, header+said, "vtt", ErrInvalid)
	}
}

func TestTheCueShapesExportsUseAreRead(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:04,000\nDana: hello\n\n" +
		"2\n00:00:04,500 --> 00:00:06,000\nline two\nsecond line\n \n3\n01:00:00,000 --> 01:00:00,000\nlast\n"
	p := parsed(t, srt, "srt")
	same(t, []string{cue(p.Units[0]), cue(p.Units[1]), cue(p.Units[2])},
		[]string{"1|Dana: hello|1000|4000", "2|line two\nsecond line|4500|6000", "3|last|3600000|3600000"})
	if chosen, err := SelectExcerpt(p, nil); err != nil || chosen.UnitKind != "cues" || chosen.TotalUnits != 3 {
		t.Errorf("the SRT excerpt is %+v (%v)", chosen, err)
	}
	// Auto-captions repeat a start time, end a cue where it starts, and are not always in order.
	same(t, texts(parsed(t, "WEBVTT\n\n00:00:02.000 --> 00:00:02.000\nzero length\n\t\n"+
		"00:00:01.000 --> 00:00:03.000\nearlier start\n \n00:00:01.000 --> 00:00:02.500\nsame start\n", "vtt").Units),
		[]string{"zero length", "earlier start", "same start"})
	// Some exports write the speaker as the cue identifier, so identifiers repeat. The speaker stays in the
	// quotation. A number or a single token is only an identifier and does not.
	named := parsed(t, "WEBVTT\n\nDana Crest\n00:01.000 --> 00:02.000\none\n\nDana Crest\n00:02.000 --> 00:03.000\ntwo\n", "vtt")
	same(t, []string{cue(named.Units[0]), cue(named.Units[1])}, []string{"Dana Crest|one|1000|2000", "Dana Crest|two|2000|3000"})
	chosen, _ := SelectExcerpt(named, nil)
	same(t, chosen.Body, "Dana Crest\none\nDana Crest\ntwo")
	chosen, _ = SelectExcerpt(parsed(t, "WEBVTT\n\n1\n00:01.000 --> 00:02.000\none\n\nabc-def\n00:02.000 --> 00:03.000\ntwo\n", "vtt"), nil)
	same(t, chosen.Body, "one\ntwo")
	chosen, _ = SelectExcerpt(parsed(t, "WEBVTT\n\n"+strings.Repeat("word ", 60)+"\n00:01.000 --> 00:02.000\none\n", "vtt"), nil)
	same(t, chosen.Body, "one")
	// An SRT file needs its timing lines, and no cue may end before it starts. Only a number may sit above a
	// timing line, nothing may follow one, and a comment block is not skipped as WebVTT's is.
	for _, bad := range []string{"1\nDana: hello\n", "1\n00:00:04,000 --> 00:00:01,000\ntext\n",
		"WEBVTT\n\n1\n00:00:01,000 --> 00:00:02,000\nx\n", "Dana\n00:00:01,000 --> 00:00:02,000\nx\n",
		arabicThree + "\n00:00:01,000 --> 00:00:02,000\nx\n", "1\n00:01,000 --> 00:02,000\nx\n",
		"1\n00:00:01,000 --> 00:00:02,000 we agreed on 40 seats\nx\n",
		"NOTE this matters\nand so does this\n\n1\n00:00:01,000 --> 00:00:02,000\nx\n",
		"1\n00:00:01,000 --> 00:00:02,000\nx\n2\n00:00:02,000 --> 00:00:03,000\nmerged\n"} {
		refused(t, bad, "srt", ErrInvalid)
	}
	same(t, texts(parsed(t, "1\n00:00:01,000 --> 00:00:02,000\nplan A --> plan B\n", "srt").Units), []string{"plan A --> plan B"})
	same(t, texts(parsed(t, "00:00:01.000 --> 00:00:02.000  \nno number, dots\n", "srt").Units), []string{"no number, dots"})
}

func TestLinesOfSpacesNeverCostAWord(t *testing.T) {
	timing := "00:01.000 --> 00:02.000\n"
	// Text after a run of spaces stays in its cue, whatever it starts with, and a comment is only a block of
	// its own.
	for text, units := range map[string][]string{
		"one\n \nNOTE this matters\nmore":                       {"one\n \nNOTE this matters\nmore"},
		"one\n \n\t\nstill one":                                 {"one\n \n\t\nstill one"},
		"one\n \nNOTEWORTHY: forty seats":                       {"one\n \nNOTEWORTHY: forty seats"},
		"one\n \n \n00:02.000 --> 00:03.000\ntwo":               {"one", "two"},
		"one\n \nid\n00:02.000 --> 00:03.000\ntwo":              {"one", "two"},
		"one\n\nNOTE a comment\n\n00:02.000 --> 00:03.000\ntwo": {"one", "two"},
		"one\n \nstill one\n \n00:02.000 --> 00:03.000\ntwo":    {"one\n \nstill one", "two"},
	} {
		same(t, texts(parsed(t, "WEBVTT\n\n"+timing+text+"\n", "vtt").Units), units)
	}
	// A line of spaces above a timing line is no identifier. Any other blank identifier is refused.
	for _, identifier := range []string{" ", "\t", " \t "} {
		p := parsed(t, "WEBVTT\n\n"+identifier+"\n00:00.000 --> 00:01.000\nInvented call text.\n", "vtt")
		if len(p.Units) != 1 || p.Units[0].CueID != nil {
			t.Errorf("a cue under %q reads as %+v", identifier, p.Units)
		}
	}
	for _, identifier := range []string{noBreakSpace, emSpace} {
		refused(t, "WEBVTT\n\n"+identifier+"\n00:00.000 --> 00:01.000\nInvented call text.\n", "vtt", ErrInvalid)
	}
	// What follows an end time is cue settings. Anything else there could be speech and refuses.
	same(t, len(parsed(t, "WEBVTT\n\n00:01.000 --> 00:02.000 align:start position:10%\none\n", "vtt").Units), 1)
	same(t, len(parsed(t, "WEBVTT\n\n00:01.000 --> 00:02.000 line:a:b"+noBreakSpace+"size:1 \t\none\n", "vtt").Units), 1)
	for _, after := range []string{"we agreed on 40 seats", "align:", "align", "colour:red"} {
		refused(t, "WEBVTT\n\n00:01.000 --> 00:02.000 "+after+"\none\n", "vtt", ErrInvalid)
	}
}

func TestMalformedWebVTTNeverDropsWhatWasSaid(t *testing.T) {
	for _, bad := range []string{
		"BAD\n\n00:00.000 --> 00:01.000\ntext",
		"WEBVTT\n00:00.000 --> 00:01.000\ntext",
		"WEBVTT\n\n00:02.000 --> 00:01.000\ntext",
		"WEBVTT\n\n00:60.000 --> 01:01.000\ntext",
		"WEBVTT\n\n00:00.00 --> 00:01.000\ntext",
		"WEBVTT\n\n00:00,000 --> 00:01,000\ntext",
		"WEBVTT\n\n1:00:00.000 --> 2:00:00.000\ntext",
		"WEBVTT\n\n1234567890:00:00.000 --> 1234567890:00:01.000\ntext",
		"WEBVTT\n\n00:00.000-->00:01.000\ntext",
		"WEBVTT\n\n00:00.000 --> 00:01.000x\ntext",
		"WEBVTT\n\n00:00.000 --> 00:01.000\none\n00:01.000 --> 00:02.000\ntwo",
		"WEBVTT\n\nNOTE hidden\n00:00.000 --> 00:01.000\ntext",
		"WEBVTT\n\nunknown block\nspoken text",
		"WEBVTT\n\nlone identifier",
		"WEBVTT\n\n00:00.000 --> 00:01.000\ntext\n\nSTYLE\nbody{}",
		"WEBVTT\n\n00:00.000 --> 00:01.000\n\n00:01.000 --> 00:02.000\n",
		"WEBVTT\n\n",
	} {
		refused(t, bad, "vtt", ErrInvalid)
	}
	p := parsed(t, "WEBVTT\n\nSTYLE \t\nbody{}\n\nREGION\nid:x\n\nNOTE\nabc\n\n123456789:00:00.000 -->\t999999999:59:59.999\n<v Dana>text</v>", "vtt")
	same(t, cue(p.Units[0]), "|<v Dana>text</v>|444444440400000|3599999999999999")
}

func TestStatedSpeakerNamesAreCheckedAgainstTheWholeFile(t *testing.T) {
	for _, raw := range []string{"Dana Crest: We will sign.\n\nMorgan Reed: Good.\n", "Dr. J. Vance: We will sign.\n"} {
		if p, err := Parse([]byte(raw), "txt", true); err != nil || !p.SpeakerNames {
			t.Errorf("%q with stated names ended with %v", raw, err)
		}
	}
	for _, raw := range []string{"Dana Crest: We will sign.\nNo name here.\n", "dana: lower case\n", "Dana Crest:no space\n",
		"final word on the matter: no\n", "We have signed. Dana Crest: yes\n"} {
		if _, err := Parse([]byte(raw), "txt", true); err != ErrSpeakerNamesMissing {
			t.Errorf("%q with stated names ended with %v", raw, err)
		}
	}
	// A cue is one line: on a second line nothing says whose words they are.
	if _, err := Parse([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nDana Crest: one\ntwo\n"), "vtt", true); err != ErrSpeakerNamesMissing {
		t.Errorf("a cue of two lines with stated names ended with %v", err)
	}
	// A stored import is read by the layout alone, so a later sentence rule never unreads it.
	if _, err := parse([]byte("We have signed. Dana Crest: yes\n"), "txt", true, true); err != nil {
		t.Errorf("a stored import was held to the sentence rule again: %v", err)
	}
	if _, err := parse([]byte("No name here.\n"), "txt", true, true); err != ErrSpeakerNamesMissing {
		t.Errorf("a stored import with no name ended with %v", err)
	}
}

// The stated name is the one label of a cue. A WebVTT voice tag in the words after it would name a second
// speaker, and a card would take the role from the tag, so the cue is refused: when the file is imported,
// when a stored import is read, and when a record of that shape is checked. In a TXT or SRT file a tag is
// text, and without stated names a voice tag is the label it always was.
func TestAVoiceTagAfterAStatedNameIsRefused(t *testing.T) {
	cue := func(words string) []byte { return []byte("WEBVTT\n\n00:00.000 --> 00:01.000\n" + words + "\n") }
	voiced := "Morgan Reed: <v Dana Crest>We approved the renewal.</v>"
	for _, words := range []string{voiced, "Morgan Reed: We approved the renewal.</v>", "Morgan Reed: Yes. <v.loud Dana>No."} {
		for _, stored := range []bool{false, true} {
			if _, err := parse(cue(words), "vtt", true, stored); err != ErrSpeakerNamesMissing {
				t.Errorf("%q with stated names (stored %v) ended with %v", words, stored, err)
			}
		}
	}
	if _, err := Parse(cue(voiced), "vtt", false); err != nil {
		t.Errorf("the cue without stated names ended with %v", err)
	}
	if _, err := Parse(cue("Morgan Reed: We <i>approved</i> the renewal."), "vtt", true); err != nil {
		t.Errorf("a styling tag after a stated name ended with %v", err)
	}
	if _, err := Parse([]byte(voiced+"\n"), "txt", true); err != nil {
		t.Errorf("the line of a TXT file ended with %v", err)
	}
	if _, err := Parse([]byte("1\n00:00:00,000 --> 00:00:01,000\n"+voiced+"\n"), "srt", true); err != nil {
		t.Errorf("the cue of an SRT file ended with %v", err)
	}
	// A record of that shape, made without the parser, does not pass the check a card makes.
	in := imported(t, string(cue(voiced)), "vtt", nil)
	stated := *in.Parsed
	stated.SpeakerNames = true
	in.Parsed = &stated
	if _, _, err := in.Revision(1); err != ErrCorrupt {
		t.Errorf("a record with a voice tag after a stated name was made: %v", err)
	}
	if err := in.Check(); err != ErrSpeakerNamesMissing {
		t.Errorf("the import was checked with %v", err)
	}
}

// allocated is how many bytes a call allocates.
func allocated(call func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	call()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// What parsing allocates grows with the units it keeps, not with the lines of the file: a file is read a
// line at a time, a TXT file is refused at its 10,001st line, and a timed file at its 10,001st cue, before
// the rest is read. Each file here is 2 MiB, and the copy of it as text is 2 MiB of what is allocated. The
// parser that held every line and every block before it counted allocated 40 MiB to 170 MiB for these files.
func TestParsingAllocatesForTheUnitsItKeeps(t *testing.T) {
	const mib = 1 << 20
	size := evidence.MaxRawBytes
	cue := "00:00.000 --> 00:00.000\nx\n\n"
	tail := "\n00:00.000 --> 00:00.000\nsaid\n"
	for name, c := range map[string]struct {
		raw    string
		format string
		want   error
		units  int
		bound  uint64
	}{
		"a TXT file of line breaks": {"x" + strings.Repeat("\n", size-1), "txt", ErrTooLarge, 0, 6 * mib},
		"a WebVTT file of line breaks before its one cue": {"WEBVTT\n" + strings.Repeat("\n", size-len("WEBVTT\n")-len(tail)) + tail,
			"vtt", nil, 1, 3 * mib},
		"a WebVTT file of cues past the limit": {"WEBVTT\n\n" + strings.Repeat(cue, (size-8)/len(cue)), "vtt", ErrTooLarge, 0, 6 * mib},
		"an SRT file of cues past the limit": {strings.Repeat("7\n00:00:00,000 --> 00:00:00,000\nx\n\n", size/35), "srt",
			ErrTooLarge, 0, 6 * mib},
		"a WebVTT cue of a million lines of spaces": {"WEBVTT\n\n00:00.000 --> 00:00.000\nsaid" +
			strings.Repeat("\n ", (size-60)/2) + "\nand more\n", "vtt", nil, 1, 16 * mib},
	} {
		raw := []byte(c.raw)
		var parsed *Parsed
		var err error
		used := allocated(func() { parsed, err = Parse(raw, c.format, false) })
		if err != c.want || err == nil && len(parsed.Units) != c.units {
			t.Errorf("%s: parsed with %v", name, err)
		}
		if used > c.bound {
			t.Errorf("%s: %d bytes allocated for %d bytes of file, want at most %d", name, used, len(raw), c.bound)
		}
	}
}

// countdown is a context that is done after its Err has been read a number of times.
type countdown struct {
	context.Context
	left int
}

func (c *countdown) Err() error {
	if c.left == 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

// oneImport is a store that holds one active import for every account.
type oneImport struct{ active *Active }

func (o oneImport) Workspace() string { return "workspace-1" }

func (o oneImport) ActiveImport(context.Context, *strictjson.Object, string) (*Active, error) {
	return o.active, nil
}

// The read's context bounds the store's query, and parsing the file again cannot be stopped part way. So
// the context is read once the row is there and again before the record is returned: a read whose context
// is done by either time gives the context's error and no record.
func TestAReadWhoseContextIsDoneGivesNoRecord(t *testing.T) {
	in := imported(t, webVTT, "vtt", nil)
	revision, _, err := in.Revision(1)
	if err != nil {
		t.Fatal(err)
	}
	store := oneImport{&Active{Revision: revision, ContentAccount: "acc-007", ContentGeneration: 1}}
	for left, want := range map[int]error{0: context.Canceled, 1: context.Canceled, 2: nil} {
		records, err := CallNotes(&countdown{context.Background(), left}, store, testBinding, "acc-007")
		if err != want || (want == nil) != (len(records) == 1) {
			t.Errorf("a context done after %d readings gave %d records and %v", left, len(records), err)
		}
	}
	source := Source{Imports: store, CRMBinding: testBinding}
	if system, records, err := source.Read(context.Background(), "read_call_notes", "acc-007"); err != nil ||
		system != "calls" || len(records) != 1 || source.Workspace() != "workspace-1" {
		t.Errorf("the source read %d records from %q (%v)", len(records), system, err)
	}
}

func TestTheCommandLineChoicesOfAnImport(t *testing.T) {
	for path, format := range map[string]string{"meeting.txt": "txt", "a/b.c/Meeting.VTT": "vtt", "x.tar.SrT": "srt",
		"a..txt": "txt"} {
		if got, err := FormatOf(path); err != nil || got != format {
			t.Errorf("%s selects %q (%v), want %s", path, got, err, format)
		}
	}
	for _, path := range []string{"meeting", "meeting.docx", ".txt", "..txt", "txt", "meeting.txt.gz", "meeting.t\xc5\xbft", "a.txt/b"} {
		if _, err := FormatOf(path); err == nil {
			t.Errorf("%s selected a parser", path)
		}
	}
	if span, err := UnitRange("8:14", "vtt", false); err != nil || span != (Range{8, 14}) {
		t.Errorf("--cues 8:14 is %v (%v)", span, err)
	}
	if span, err := UnitRange("99999:99999", "txt", true); err != nil || span != (Range{99999, 99999}) {
		t.Errorf("--lines 99999:99999 is %v (%v)", span, err)
	}
	refusals := map[string][]any{
		"Use --cues with VTT or SRT, or --lines with TXT.":                {"1:2", "txt", false},
		"Select an inclusive one-based range such as 3:8.":                {"0:2", "txt", true},
		"The first selected unit must not follow the last selected unit.": {"3:2", "srt", false},
	}
	for message, choice := range refusals {
		if _, err := UnitRange(choice[0].(string), choice[1].(string), choice[2].(bool)); err == nil || err.Error() != message {
			t.Errorf("%v ended with %v", choice, err)
		}
	}
	if _, err := UnitRange("1:2", "srt", true); err == nil {
		t.Error("--lines was taken for an SRT file")
	}
	for _, text := range []string{"3", "3:", ":3", "03:4", "3:4:5", "100000:100001", "3-4", " 3:4", "3:" + arabicThree} {
		if _, err := UnitRange(text, "txt", true); err == nil {
			t.Errorf("--lines %q was taken", text)
		}
	}
	for given, want := range map[string]string{"2027-01-14T15:00:00Z": "2027-01-14T15:00:00Z",
		"2027-01-14T10:00:00-05:00": "2027-01-14T15:00:00Z", "2027-01-14T15:00:00.25+00:00": "2027-01-14T15:00:00.250000Z",
		"20270114T150000+0100": "2027-01-14T14:00:00Z", "2027-01-14T24:00:00Z": "2027-01-15T00:00:00Z",
		"2027-W02-4T15:00:00Z": "2027-01-14T15:00:00Z", "2027-01-14T15:00:00.1234567Z": "2027-01-14T15:00:00.123456Z"} {
		if got, err := CalledAt(given); err != nil || got != want || !evidence.UTCStamp(got) {
			t.Errorf("--called-at %s is %q (%v), want %s", given, got, err, want)
		}
	}
	for _, text := range []string{"2027-01-14", "2027-01-14T15:00:00", "2027-01-14 15:00:00Z", "yesterday", "0001-01-01T00:00:00+01:00",
		"9999-12-31T23:59:59-01:00", ""} {
		if _, err := CalledAt(text); err == nil {
			t.Errorf("--called-at %q was taken", text)
		}
	}
}

func TestOnlyARegularFileWithinTheLimitIsRead(t *testing.T) {
	folder := t.TempDir()
	write := func(name string, size int) string {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if raw, err := ReadExport(write("limit.txt", evidence.MaxRawBytes)); err != nil || len(raw) != evidence.MaxRawBytes {
		t.Errorf("a file at the limit ended with %v", err)
	}
	if _, err := ReadExport(write("over.txt", evidence.MaxRawBytes+1)); err != ErrTooLarge {
		t.Errorf("a file over the limit ended with %v", err)
	}
	link := filepath.Join(folder, "link.txt")
	if err := os.Symlink(write("small.txt", 3), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExport(link); err != ErrInvalid {
		t.Errorf("a link ended with %v", err)
	}
	if _, err := ReadExport(folder); err != ErrInvalid {
		t.Errorf("a folder ended with %v", err)
	}
	if _, err := ReadExport(filepath.Join(folder, "missing.txt")); err != ErrIO {
		t.Errorf("a missing file ended with %v", err)
	}
}

var testBinding = strictjson.NewObject("schema_version", 1, "crm_backend", "sqlite", "identity_sha256", strings.Repeat("a", 64))

// imported is an import of an invented file for acc-007, parsed and selected.
func imported(t *testing.T, raw, format string, span *Range) Import {
	t.Helper()
	p := parsed(t, raw, format)
	chosen, err := SelectExcerpt(p, span)
	if err != nil {
		t.Fatal(err)
	}
	return Import{Parsed: p, Excerpt: chosen, ImportedAt: "2026-11-18T12:00:00Z", Association: Association{
		CRMBinding: testBinding, AccountID: "acc-007", Method: "account_id", ResolutionReceiptID: "invented-receipt-one"}}
}

const webVTT = "WEBVTT\n\nopening\n00:00.000 --> 00:04.000\n<v Morgan>We need a revised seat count.</v>\n\n" +
	"Dana Crest\n00:03.500 --> 00:06.000\nThe renewal is approaching.\n\n00:06.000 --> 00:09.000\nSend the order form.\n"

func TestARevisionIsNamedByItsManifestAndOpensOnlyUnchanged(t *testing.T) {
	in := imported(t, webVTT, "vtt", &Range{2, 2})
	if err := in.Check(); err != nil {
		t.Fatal(err)
	}
	revision, manifest, err := in.Revision(1)
	if err != nil {
		t.Fatal(err)
	}
	if revision.ImportID != "note-"+digest(revision.Manifest) || !ValidImportID(revision.ImportID) ||
		canonical(manifest) != revision.Manifest || revision.RequestKey != in.RequestKey(1) {
		t.Fatalf("the revision is %+v", revision)
	}
	opened, err := revision.Open(testBinding)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := strictjson.Dumps(opened.Record, strictjson.Quoted)
	if !strings.HasPrefix(record, `{"id": "`+revision.ImportID+`", "account_id": "acc-007", "record_kind": `+
		`"imported_transcript", "record_version": 2, "called_at": null, "body": "<v Morgan>We need a revised seat count.</v>\n`+
		`Dana Crest\nThe renewal is approaching.\nSend the order form.", "manifest": {"association": {"account_id": `) {
		t.Errorf("the record is %s", record)
	}
	if !evidence.ValidImportedRecord(opened.Record, "acc-007", testBinding) || len(opened.Parsed.Units) != 3 {
		t.Error("the record does not pass the check a card makes")
	}
	// What makes two requests the same import: not the receipt of the resolution or the time of the import.
	again := in
	again.ImportedAt, again.Association.ResolutionReceiptID = "2026-11-18T13:00:00Z", "invented-receipt-two"
	called := "2026-11-18T12:00:00Z"
	later := in
	later.CalledAt = &called
	other := imported(t, webVTT, "vtt", &Range{3, 3})
	if again.RequestKey(1) != in.RequestKey(1) || later.RequestKey(1) == in.RequestKey(1) ||
		other.RequestKey(1) == in.RequestKey(1) || in.RequestKey(2) == in.RequestKey(1) {
		t.Error("the request key does not follow the request")
	}
	result := Result(manifest, revision.ImportID, true, false, "invented-receipt-one")
	shown, _ := strictjson.Dumps(result, strictjson.Quoted)
	if want := `{"schema_version": 1, "source": "imported_transcript", "status": "imported", "created": true, ` +
		`"selected": true, "reassigned": false, "resolution_receipt_id": "invented-receipt-one", "import_id": "` +
		revision.ImportID + `", "account_id": "acc-007", "format": "vtt", "called_at": null, "imported_at": ` +
		`"2026-11-18T12:00:00Z", "content_sha256": "` + in.Parsed.ContentSHA256 + `", "requested_range": {"first": 2, ` +
		`"last": 2}, "cited_range": {"first": 1, "last": 3}, "total_units": 3, "unit_kind": "cues", ` +
		`"selection_mode": "explicit", "speaker_names": false}`; shown != want {
		t.Errorf("the result is %s", shown)
	}

	// A row that no longer matches its manifest, or bytes that no longer give its excerpt, is corrupt.
	changes := map[string]func(r *Revision){
		"other bytes":         func(r *Revision) { r.Raw = []byte(strings.Replace(webVTT, "approaching", "far away", 1)) },
		"bytes of other size": func(r *Revision) { r.Raw = append([]byte(webVTT), "\n"...) },
		"no bytes":            func(r *Revision) { r.Raw = nil },
		"another request key": func(r *Revision) { r.RequestKey = strings.Repeat("0", 64) },
		"another digest":      func(r *Revision) { r.ContentSHA256 = strings.Repeat("0", 64) },
		"another account":     func(r *Revision) { r.AccountID = "acc-008" },
		"another ID":          func(r *Revision) { r.ImportID = "note-" + strings.Repeat("0", 64) },
		"a forged manifest": func(r *Revision) {
			r.Manifest = strings.Replace(r.Manifest, "Send the order form.", "Send nothing.", 1)
			r.ImportID = "note-" + digest(r.Manifest)
		},
		"an earlier parser": func(r *Revision) {
			r.Manifest = strings.Replace(r.Manifest, `"parser_version":2`, `"parser_version":1`, 1)
			r.ImportID = "note-" + digest(r.Manifest)
		},
		"a private selector": func(r *Revision) {
			r.Manifest = strings.Replace(r.Manifest, `"method":"account_id"`, `"account_number":"INVENTED-1","method":"account_id"`, 1)
			r.ImportID = "note-" + digest(r.Manifest)
		},
		"a manifest that is not an object": func(r *Revision) { r.Manifest, r.ImportID = "[]", "note-"+digest("[]") },
		"a manifest that is not JSON":      func(r *Revision) { r.Manifest = "{" },
		"a manifest over the record limit": func(r *Revision) { r.Manifest = strings.Repeat(" ", evidence.MaxRecordBytes) + r.Manifest },
		// The ID is the digest of the stored text itself, and the stored text is the canonical one.
		"other spacing under the same ID": func(r *Revision) { r.Manifest += " " },
		"other spacing under its own ID": func(r *Revision) {
			r.Manifest = strings.Replace(r.Manifest, `","`, `", "`, 1)
			r.ImportID = "note-" + digest(r.Manifest)
		},
		"other key order under its own ID": func(r *Revision) {
			r.Manifest = `{"units":` + strings.TrimSuffix(strings.SplitN(r.Manifest, `,"units":`, 2)[1], "}") + "," +
				strings.SplitN(r.Manifest, `,"units":`, 2)[0][1:] + "}"
			r.ImportID = "note-" + digest(r.Manifest)
		},
	}
	for name, change := range changes {
		changed := revision
		change(&changed)
		if _, err := changed.Open(testBinding); err != ErrCorrupt {
			t.Errorf("%s: opened with %v", name, err)
		}
	}
	otherBinding := strictjson.NewObject("schema_version", 1, "crm_backend", "sqlite", "identity_sha256", strings.Repeat("c", 64))
	for _, binding := range []*strictjson.Object{otherBinding, nil} {
		if _, err := revision.Open(binding); err != ErrCorrupt {
			t.Errorf("opened under another binding with %v", err)
		}
	}
}

func TestAnImportIsCheckedBeforeItIsSaved(t *testing.T) {
	in := imported(t, "Original.\nSecond line.", "txt", nil)
	if err := in.Check(); err != nil {
		t.Fatal(err)
	}
	forged := *in.Parsed
	forged.Units = []Unit{{Index: 1, Text: "Forged."}, in.Parsed.Units[1]}
	wider := *in.Excerpt
	wider.Requested = Range{1, 1}
	number, blank, long := "INVENTED-1", " ", strings.Repeat("9", 41)
	salesforce := Association{CRMBinding: strictjson.NewObject("schema_version", 1, "crm_backend", "salesforce",
		"identity_sha256", strings.Repeat("b", 64)), AccountID: "acc-sf-001000000000001AAA", Method: "account_number",
		AccountNumber: &number, ResolutionReceiptID: "invented-receipt-one"}
	stamp := "2026-11-18T12:00:00+01:00"
	for name, c := range map[string]struct {
		change func(i *Import)
		want   error
	}{
		"an AccountNumber resolution":      {func(i *Import) { i.Association = salesforce }, nil},
		"a forged parse":                   {func(i *Import) { i.Parsed = &forged }, ErrInvalid},
		"a forged excerpt":                 {func(i *Import) { i.Excerpt = &wider }, ErrInvalid},
		"no excerpt":                       {func(i *Import) { i.Excerpt = nil }, ErrInvalid},
		"a call time with an offset":       {func(i *Import) { i.CalledAt = &stamp }, ErrInvalid},
		"an import time that is a day":     {func(i *Import) { i.ImportedAt = "2026-11-18" }, ErrInvalid},
		"no binding":                       {func(i *Import) { i.Association.CRMBinding = nil }, ErrAssociationInvalid},
		"a Salesforce account on the demo": {func(i *Import) { i.Association.AccountID = salesforce.AccountID }, ErrAssociationInvalid},
		"an AccountNumber on the demo": {func(i *Import) {
			i.Association.Method, i.Association.AccountNumber = "account_number", &number
		}, ErrAssociationInvalid},
		"a number with an account_id resolution": {func(i *Import) { i.Association.AccountNumber = &number }, ErrAssociationInvalid},
		"no receipt":                             {func(i *Import) { i.Association.ResolutionReceiptID = "" }, ErrAssociationInvalid},
		"a blank AccountNumber": {func(i *Import) {
			i.Association = salesforce
			i.Association.AccountNumber = &blank
		}, ErrAssociationInvalid},
		"a long AccountNumber": {func(i *Import) {
			i.Association = salesforce
			i.Association.AccountNumber = &long
		}, ErrAssociationInvalid},
		"no AccountNumber": {func(i *Import) {
			i.Association = salesforce
			i.Association.AccountNumber = nil
		}, ErrAssociationInvalid},
	} {
		changed := in
		c.change(&changed)
		if err := changed.Check(); !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
			t.Errorf("%s: checked with %v, want %v", name, err, c.want)
		}
	}
	// A file parsed without speaker names is not saved as one that states them by setting the field: the
	// check parses the bytes as a new import, and this name holds a sentence end.
	flipped := imported(t, "We have signed. Dana Crest: yes.", "txt", nil)
	stated := *flipped.Parsed
	stated.SpeakerNames = true
	flipped.Parsed = &stated
	if err := flipped.Check(); err != ErrSpeakerNamesMissing {
		t.Errorf("a file with the speaker names field set by hand was checked with %v", err)
	}
}
