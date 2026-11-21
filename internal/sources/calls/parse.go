package calls

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Unit is one transcript unit: a line of a TXT file, or a timed cue of a WebVTT or SRT file. Index counts
// from 1 in file order. A line has no identifier and no times.
type Unit struct {
	Index   int
	Text    string
	CueID   *string
	StartMS *int64
	EndMS   *int64
}

// Parsed is one export as read: its exact bytes, how they were read, and every unit.
type Parsed struct {
	Raw           []byte
	Format        string // txt, vtt or srt
	SpeakerNames  bool
	ContentSHA256 string
	Units         []Unit
}

// Parse is calls_import.parse_transcript for a file being imported: a strict subset of each format, read so
// that no line of speech is dropped. speakerNames is the operator's statement that every line or cue is one
// line that opens with its speaker's name and a colon. It is checked against the whole file.
func Parse(raw []byte, format string, speakerNames bool) (*Parsed, error) {
	return parse(raw, format, speakerNames, false)
}

// parse reads an export. A stored import is not held again to the rule that a name holds no sentence end,
// so later sentence rules never unread what was accepted.
func parse(raw []byte, format string, speakerNames, stored bool) (*Parsed, error) {
	if format != "txt" && format != "vtt" && format != "srt" {
		return nil, ErrInvalid
	}
	if len(raw) > evidence.MaxRawBytes {
		return nil, ErrTooLarge
	}
	if !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	text, ok := evidence.SourceText(string(raw), evidence.MaxRawBytes, true)
	if !ok {
		return nil, ErrInvalid
	}
	text = strings.TrimPrefix(text, "\ufeff")
	var units []Unit
	if format == "txt" {
		// A unit is counted as its line is read, so a file of line breaks is refused at the limit and not
		// after every one of its lines is held.
		for lines := (lineCursor{text: text}); ; {
			line, more := lines.next()
			if !more {
				break
			}
			if len(units) == evidence.MaxTotalUnits {
				return nil, ErrTooLarge
			}
			units = append(units, Unit{Index: len(units) + 1, Text: line})
		}
	} else {
		var err error
		if units, err = cues(text, format == "vtt"); err != nil {
			return nil, err
		}
	}
	spoken := false
	for _, unit := range units {
		spoken = spoken || !blank(unit.Text)
	}
	if !spoken {
		return nil, ErrInvalid
	}
	if speakerNames {
		for _, unit := range units {
			if blank(unit.Text) {
				continue
			}
			// A name holds no sentence end ("We have signed. Dana Crest: ..."), so words never leave a
			// quotation to become a label.
			// The name is the one label of the cue: a WebVTT voice tag after it would name a second speaker.
			name, _, named := evidence.NamedCue(unit.Text, format)
			if !named {
				return nil, ErrSpeakerNamesMissing
			}
			if sentences, err := evidence.Sentences(name, false); !stored && (err != nil || len(sentences) != 1) {
				return nil, ErrSpeakerNamesMissing
			}
		}
	}
	digest := sha256.Sum256(raw)
	return &Parsed{Raw: raw, Format: format, SpeakerNames: speakerNames, ContentSHA256: hex.EncodeToString(digest[:]),
		Units: units}, nil
}

// lineCursor reads the lines of a text one at a time, as calls_import._lines splits them: a line ends at
// CR LF, CR or LF, and a line break that ends the text starts no line after it. A copy of a cursor reads on
// from the same place, so a reader looks ahead by reading a copy.
type lineCursor struct {
	text    string
	at      int
	started bool
}

// next is the next line, and false once there is none.
func (c *lineCursor) next() (string, bool) {
	n := len(c.text)
	if c.at > n || c.at == n && c.started {
		return "", false
	}
	c.started = true
	i := c.at
	for i < n && c.text[i] != '\r' && c.text[i] != '\n' {
		i++
	}
	line := c.text[c.at:i]
	switch {
	case i == n:
		c.at = n + 1
	case c.text[i] == '\r' && i+1 < n && c.text[i+1] == '\n':
		c.at = i + 2
	default:
		c.at = i + 1
	}
	return line, true
}

// blank is whether text.strip() is empty.
func blank(text string) bool {
	for _, c := range text {
		if !u.IsSpace(c) {
			return false
		}
	}
	return true
}

// spaces is whether a line holds nothing but spaces and tabs.
func spaces(line string) bool { return strings.Trim(line, " \t") == "" }

// block is one block of a WebVTT or SRT file, read a line at a time: a cue, or a comment, STYLE or REGION
// block of WebVTT, which holds no speech.
type block struct {
	vtt        bool
	lines      int
	skipped    bool
	id         *string
	timed      bool
	start, end int64
	settings   string // what follows the end time
	payload    int
	one        string          // the cue's text while it is one line
	more       strings.Builder // the cue's text once it has more
}

// add takes the next line of the block. spoken is how many cues the file has given so far.
func (b *block) add(line string, spoken int) error {
	b.lines++
	arrow := strings.Contains(line, "-->")
	switch {
	case b.skipped:
		if arrow {
			return ErrInvalid
		}
	case b.lines == 1:
		head := strings.TrimRight(line, " \t")
		comment := line == "NOTE" || strings.HasPrefix(line, "NOTE ") || strings.HasPrefix(line, "NOTE\t")
		if display := head == "STYLE" || head == "REGION"; b.vtt && (comment || display) {
			if arrow || display && spoken > 0 {
				return ErrInvalid
			}
			b.skipped = true
			return nil
		}
		if b.time(line) {
			return nil
		}
		if b.timed {
			return ErrInvalid
		}
		// Some exports put the speaker's name here, so an identifier may repeat. SRT only numbers its cues.
		if _, ok := evidence.SourceText(line, evidence.MaxRawBytes, true); arrow || !ok || !b.vtt && !all(line, digit) {
			return ErrInvalid
		}
		// The line is copied so that only an identifier is kept, and no other line of the file.
		id := line
		b.id = &id
	case !b.timed:
		if !b.time(line) {
			return ErrInvalid
		}
	default:
		if !evidence.CueLine(line, b.vtt) {
			return ErrInvalid
		}
		switch b.payload++; b.payload {
		case 1:
			b.one = line
		case 2:
			b.more.WriteString(b.one)
			fallthrough
		default:
			b.more.WriteByte('\n')
			b.more.WriteString(line)
		}
	}
	return nil
}

// time reads a timing line into the block and reports whether the line is one the block may have.
// Auto-captions repeat and reorder start times and end a cue where it starts, and none of that loses text.
// A cue that ends before it starts, or anything after the end time but cue settings, is refused.
func (b *block) time(line string) bool {
	b.start, b.end, b.settings, b.timed = evidence.CueTiming(line, b.vtt)
	return b.timed && placing(b.settings, b.vtt) && b.end >= b.start
}

// close ends the block and adds its cue, if it is one, to the units. The cue that passes the limit is
// refused there, before any later block is read.
func (b *block) close(units []Unit) ([]Unit, error) {
	lines, skipped, timed, id, start, end, text := b.lines, b.skipped, b.timed, b.id, b.start, b.end, b.one
	if b.payload > 1 {
		text = b.more.String()
	}
	*b = block{vtt: b.vtt}
	if lines == 0 || skipped {
		return units, nil
	}
	if !timed {
		return nil, ErrInvalid
	}
	units = append(units, Unit{Index: len(units) + 1, Text: text, CueID: id, StartMS: &start, EndMS: &end})
	if len(units) > evidence.MaxTotalUnits {
		return nil, ErrTooLarge
	}
	return units, nil
}

// cues reads the cues of a WebVTT or SRT file, in file order, in one pass over its lines. It holds one block
// at a time, so what it allocates grows with the cues it keeps and not with the lines of the file.
func cues(text string, vtt bool) ([]Unit, error) {
	lines := lineCursor{text: text}
	if vtt {
		signature, _ := lines.next()
		// Exporters write these metadata lines under the signature. Any other text there could be speech.
		line, more := lines.next()
		for more && vttHeader(line) {
			line, more = lines.next()
		}
		signed := signature == "WEBVTT" || strings.HasPrefix(signature, "WEBVTT ") || strings.HasPrefix(signature, "WEBVTT\t")
		if !more || !signed || strings.Contains(signature, "-->") || line != "" {
			return nil, ErrInvalid
		}
	}
	timing := func(line string, is bool) bool {
		_, _, _, ok := evidence.CueTiming(line, vtt)
		return is && ok
	}
	var units []Unit
	var err error
	current := block{vtt: vtt}
	for {
		here := lines
		line, more := lines.next()
		if !more {
			break
		}
		if !spaces(line) {
			if err = current.add(line, len(units)); err != nil {
				return nil, err
			}
			continue
		}
		// An empty line ends a block. A run of lines of spaces does too, but only where a cue, an empty line,
		// or the end of the file follows it. Anywhere else the run is part of a cue's text. A cue starts
		// with its timing line, or with one identifier line above it. So the run is read ahead to its end:
		// where a cue or the end follows, all of it separates; if not, it separates up to its last empty
		// line, and the lines of spaces after that are text.
		run, end, afterEmpty, empty := here, here, here, false
		var next, second string
		var isNext, isSecond bool
		for {
			end = run
			if next, isNext = run.next(); !isNext || !spaces(next) {
				second, isSecond = run.next()
				break
			}
			if next == "" {
				afterEmpty, empty = run, true
			}
		}
		switch {
		case !isNext || timing(next, true) || timing(second, isSecond):
			lines = end
		case empty:
			lines = afterEmpty
		default:
			for lines = here; lines.at != end.at; {
				spaced, _ := lines.next()
				if err = current.add(spaced, len(units)); err != nil {
					return nil, err
				}
			}
			continue
		}
		if units, err = current.close(units); err != nil {
			return nil, err
		}
	}
	return current.close(units)
}

// all is whether text is not empty and every byte of it passes.
func all(text string, pass func(byte) bool) bool {
	for i := 0; i < len(text); i++ {
		if !pass(text[i]) {
			return false
		}
	}
	return text != ""
}

func letter(c byte) bool   { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func digit(c byte) bool    { return c >= '0' && c <= '9' }
func alnum(c byte) bool    { return letter(c) || digit(c) }
func clockish(c byte) bool { return digit(c) || c == ':' || c == '.' }

// vttHeader is calls_import._VTT_HEADER.fullmatch: a well-formed Kind, Language or X-TIMESTAMP-MAP line.
func vttHeader(line string) bool {
	if kind, ok := strings.CutPrefix(line, "Kind: "); ok {
		switch kind {
		case "captions", "subtitles", "descriptions", "chapters", "metadata":
			return true
		}
		return false
	}
	// [A-Za-z]{2,3}(?:-[A-Za-z0-9]{1,8})*
	if language, ok := strings.CutPrefix(line, "Language: "); ok {
		tags := strings.Split(language, "-")
		if len(tags[0]) < 2 || len(tags[0]) > 3 || !all(tags[0], letter) {
			return false
		}
		for _, tag := range tags[1:] {
			if len(tag) > 8 || !all(tag, alnum) {
				return false
			}
		}
		return true
	}
	// MPEGTS:[0-9]+,LOCAL:[0-9:.]+ or LOCAL:[0-9:.]+,MPEGTS:[0-9]+
	if stamp, ok := strings.CutPrefix(line, "X-TIMESTAMP-MAP="); ok {
		left, right, _ := strings.Cut(stamp, ",")
		for _, pair := range [][2]string{{left, right}, {right, left}} {
			ticks, isTicks := strings.CutPrefix(pair[0], "MPEGTS:")
			local, isLocal := strings.CutPrefix(pair[1], "LOCAL:")
			if isTicks && isLocal && all(ticks, digit) && all(local, clockish) {
				return true
			}
		}
	}
	return false
}

// placing is calls_import._placing: what follows the end time is WebVTT cue settings or nothing. Anything
// else could be speech.
func placing(settings string, vtt bool) bool {
	for _, word := range fields(settings) {
		name, value, found := strings.Cut(word, ":")
		if !vtt || !found || value == "" {
			return false
		}
		switch name {
		case "vertical", "line", "position", "size", "align", "region":
		default:
			return false
		}
		for _, c := range value {
			if u.ReSpace(c) {
				return false
			}
		}
	}
	return true
}

// fields is str.split() with no argument.
func fields(text string) []string {
	var found []string
	start := -1
	for i, c := range text {
		switch {
		case u.IsSpace(c) && start >= 0:
			found, start = append(found, text[start:i]), -1
		case !u.IsSpace(c) && start < 0:
			start = i
		}
	}
	if start >= 0 {
		found = append(found, text[start:])
	}
	return found
}
