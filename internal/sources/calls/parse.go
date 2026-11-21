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
	lines := splitLines(strings.TrimPrefix(text, "\ufeff"))
	var units []Unit
	if format == "txt" {
		if len(lines) > evidence.MaxTotalUnits {
			return nil, ErrTooLarge
		}
		units = make([]Unit, len(lines))
		for i, line := range lines {
			units[i] = Unit{Index: i + 1, Text: line}
		}
	} else {
		var err error
		if units, err = cues(lines, format == "vtt"); err != nil {
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
			name, _, named := evidence.NamedLine(unit.Text)
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

// splitLines is calls_import._lines: the lines of a text, ended by CR LF, CR or LF. A line break that ends
// the text starts no line after it.
func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\r' && text[i] != '\n' {
			continue
		}
		lines = append(lines, text[start:i])
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	if start < len(text) || len(lines) == 0 {
		lines = append(lines, text[start:])
	}
	return lines
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

// cues reads the cues of a WebVTT or SRT file, in file order.
func cues(lines []string, vtt bool) ([]Unit, error) {
	format, body := "srt", -1
	if vtt {
		// Exporters write these metadata lines under the signature. Any other text there could be speech.
		format, body = "vtt", 1
		for body < len(lines) && vttHeader(lines[body]) {
			body++
		}
		signed := lines[0] == "WEBVTT" || strings.HasPrefix(lines[0], "WEBVTT ") || strings.HasPrefix(lines[0], "WEBVTT\t")
		if len(lines) <= body || !signed || strings.Contains(lines[0], "-->") || lines[body] != "" {
			return nil, ErrInvalid
		}
	}
	rest := lines[body+1:]
	timing := func(at int) bool {
		if at >= len(rest) {
			return false
		}
		_, _, _, ok := evidence.CueTiming(rest[at], vtt)
		return ok
	}
	// An empty line ends a block. A run of lines of spaces does too, but only where a cue, an empty line, or
	// the end of the file follows it. Anywhere else the run is part of a cue's text. A cue starts with its
	// timing line, or with one identifier line above it.
	separates := make([]bool, len(rest)+1)
	separates[len(rest)] = true
	for at := len(rest) - 1; at >= 0; at-- {
		if spaces(rest[at]) {
			separates[at] = rest[at] == "" || separates[at+1] || timing(at+1) || timing(at+2)
		}
	}
	var blocks [][]string
	var block []string
	for at, line := range rest {
		if !separates[at] {
			block = append(block, line)
		} else if len(block) > 0 {
			blocks, block = append(blocks, block), nil
		}
	}
	if len(block) > 0 {
		blocks = append(blocks, block)
	}
	var units []Unit
	for _, block := range blocks {
		first := block[0]
		comment := vtt && (first == "NOTE" || strings.HasPrefix(first, "NOTE ") || strings.HasPrefix(first, "NOTE\t"))
		display := vtt && (strings.TrimRight(first, " \t") == "STYLE" || strings.TrimRight(first, " \t") == "REGION")
		if comment || display {
			if display && len(units) > 0 {
				return nil, ErrInvalid
			}
			for _, line := range block {
				if strings.Contains(line, "-->") {
					return nil, ErrInvalid
				}
			}
			continue
		}
		var cueID *string
		start, end, settings, timed := evidence.CueTiming(first, vtt)
		payload := block[1:]
		if !timed {
			if strings.Contains(first, "-->") || len(block) < 2 {
				return nil, ErrInvalid
			}
			// Some exports put the speaker's name here, so an identifier may repeat. SRT only numbers its cues.
			if _, ok := evidence.SourceText(first, evidence.MaxRawBytes, true); !ok || !vtt && !all(first, digit) {
				return nil, ErrInvalid
			}
			cueID, payload = &first, block[2:]
			start, end, settings, timed = evidence.CueTiming(block[1], vtt)
		}
		// Auto-captions repeat and reorder start times and end a cue where it starts. None of that loses text.
		if !timed || !evidence.CueText(payload, format) || !placing(settings, vtt) || end < start {
			return nil, ErrInvalid
		}
		units = append(units, Unit{Index: len(units) + 1, Text: strings.Join(payload, "\n"), CueID: cueID,
			StartMS: &start, EndMS: &end})
		if len(units) > evidence.MaxTotalUnits {
			return nil, ErrTooLarge
		}
	}
	return units, nil
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
