package realdata

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The converters of datasets that ship text files. A line that is not valid UTF-8 is counted and gives no
// item: Go refuses such text and Python never sees it as text.

// lines splits a file into lines without their line ends.
func lines(data []byte) [][]byte {
	split := bytes.Split(data, []byte("\n"))
	if len(split) > 0 && len(split[len(split)-1]) == 0 {
		split = split[:len(split)-1]
	}
	for i, line := range split {
		split[i] = bytes.TrimSuffix(line, []byte("\r"))
	}
	return split
}

// Rev's .nlp files (Earnings-21 and -22): a header, then one token a line, "|"-separated, with columns named
// by the header. A turn is a run of tokens with one speaker ID. Its text is the words one space apart: each
// token after its prepunctuation (when the file has that column) and before its punctuation, cased as the
// README defines the case column: UC the first letter upper case and the rest lower, LC all lower, CA all
// upper, and MC (or a code the README does not define, such as N/A) as written. An empty token gives only
// its punctuation, and nothing when it has none. A line that is not UTF-8 ends the turn, since its speaker
// is unknown.
func convertRevNLP(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "tokens": 0, "empty tokens": 0, "turns": 0, "JSON files": 0,
		"lines that are not UTF-8": 0, "files strict_json refuses": 0}
	err := files.Walk("*/transcripts/nlp_references/*.nlp", func(m Member) error {
		counts["files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		all := lines(data)
		if len(all) == 0 {
			return fmt.Errorf("%s is empty", m.Inner)
		}
		column := map[string]int{}
		for i, name := range strings.Split(string(all[0]), "|") {
			column[name] = i
		}
		for _, name := range []string{"token", "speaker", "punctuation", "case"} {
			if _, ok := column[name]; !ok {
				return fmt.Errorf("%s has no %s column", m.Inner, name)
			}
		}
		pre, hasPre := column["prepunctuation"]
		var turn []string
		speaker, turns := "", 0
		flush := func() error {
			if len(turn) == 0 {
				return nil
			}
			turns++
			counts["turns"]++
			text := strings.Join(turn, " ")
			turn = nil
			return add(Item{ID: fmt.Sprintf("%s#%d", m.Inner, turns), File: m.Inner, Text: text})
		}
		started := false
		for _, line := range all[1:] {
			if !utf8.Valid(line) {
				counts["lines that are not UTF-8"]++
				if err := flush(); err != nil {
					return err
				}
				started = false
				continue
			}
			cells := strings.Split(string(line), "|")
			if len(cells) != len(column) {
				return fmt.Errorf("%s has a line with %d columns, not %d", m.Inner, len(cells), len(column))
			}
			counts["tokens"]++
			if !started || cells[column["speaker"]] != speaker {
				if err := flush(); err != nil {
					return err
				}
				speaker, started = cells[column["speaker"]], true
			}
			word := revCase(cells[column["token"]], cells[column["case"]]) + cells[column["punctuation"]]
			if hasPre {
				word = cells[pre] + word
			}
			if word == "" {
				counts["empty tokens"]++
				continue
			}
			turn = append(turn, word)
		}
		return flush()
	})
	if err != nil {
		return nil, err
	}
	err = files.Walk("*/transcripts/wer_tags/*.json", func(m Member) error {
		counts["JSON files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		if _, ok := strictValue(data); !ok {
			counts["files strict_json refuses"]++
		}
		return addJSON(add, m.Inner, m.Inner, "", data)
	})
	return counts, err
}

// revCase applies a case code as Rev's README defines it.
func revCase(token, code string) string {
	switch code {
	case "UC":
		first, size := utf8.DecodeRuneInString(token)
		if size == 0 {
			return ""
		}
		return string(unicode.ToUpper(first)) + strings.ToLower(token[size:])
	case "LC":
		return strings.ToLower(token)
	case "CA":
		return strings.ToUpper(token)
	}
	return token
}

var (
	ircMessage = regexp.MustCompile(`^\[\d\d:\d\d\] <[^>]*>(?: (.*))?$`)
	ircAction  = regexp.MustCompile(`^(?:\[\d\d:\d\d\]  \* |=== )\S+ (.*)$`)
	ircNotice  = regexp.MustCompile(`^\[\d\d:\d\d\] -\S+- (.*)$`)
	// A "=== " line with a bracket after the nick, or a [user@host] mask anywhere, is a join, part or quit;
	// a nick change or a topic is a system line too.
	ircSystem = regexp.MustCompile(`^=== (?:\S+ \[|.*\[[^\]]*@[^\]]*\]|\S+ (?:.* )?is now known as |.*[Tt]opic)`)
)

// IRC logs as downloaded (.raw.txt). The words of a message ("[HH:MM] <nick> words"), an action ("/me", logged
// as "[HH:MM]  * nick words" or "=== nick words") and a notice ("[HH:MM] -nick- words") are text items.
// Joins, parts, quits, nick changes and topics are system lines, counted with the rest.
func convertIRC(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "lines": 0, "messages": 0, "messages without words": 0, "actions": 0,
		"notices": 0, "system lines": 0, "other lines": 0, "lines that are not UTF-8": 0}
	err := files.Walk("data/*/*.raw.txt", func(m Member) error {
		counts["files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		for n, line := range lines(data) {
			counts["lines"]++
			if !utf8.Valid(line) {
				counts["lines that are not UTF-8"]++
				continue
			}
			var words []byte
			switch {
			case ircMessage.Match(line):
				found := ircMessage.FindSubmatchIndex(line)
				if found[2] < 0 {
					counts["messages without words"]++
					continue
				}
				counts["messages"]++
				words = line[found[2]:found[3]]
			case ircSystem.Match(line):
				counts["system lines"]++
				continue
			case ircAction.Match(line):
				counts["actions"]++
				words = ircAction.FindSubmatch(line)[1]
			case ircNotice.Match(line):
				counts["notices"]++
				words = ircNotice.FindSubmatch(line)[1]
			default:
				counts["other lines"]++
				continue
			}
			if err := add(Item{ID: fmt.Sprintf("%s:%d", m.Inner, n+1), File: m.Inner, Text: string(words)}); err != nil {
				return err
			}
		}
		return nil
	})
	return counts, err
}

// slackEntities counts a Slack message that holds &amp;, &lt; or &gt;, which safe_text does not decode.
func slackEntities(counts map[string]int, words string) {
	if strings.Contains(words, "&amp;") || strings.Contains(words, "&lt;") || strings.Contains(words, "&gt;") {
		counts["messages with &amp; &lt; or &gt;"]++
	}
}

// readCSV reads a CSV file as it is: a byte-order mark before the header is dropped, and every row must have
// the header's columns.
func readCSV(data []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	reader.LazyQuotes = false
	var rows [][]string
	for {
		row, err := reader.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				return nil, fmt.Errorf("not CSV at line %d", parse.Line)
			}
			return nil, err
		}
		rows = append(rows, row)
	}
}

// CSV files with a "message" column (the archival Slack conversations): each row's message is a text item.
// A row with every cell empty is padding, counted and skipped. Files without that column (the annotations)
// are counted.
func convertCSVColumns(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "files without a message column": 0, "rows": 0, "empty rows": 0,
		"cells that are not UTF-8": 0, "messages with &amp; &lt; or &gt;": 0}
	err := files.Walk("*.csv", func(m Member) error {
		counts["files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		rows, err := readCSV(data)
		if err != nil || len(rows) == 0 {
			return fmt.Errorf("%s: %v", m.Inner, err)
		}
		column := -1
		for i, name := range rows[0] {
			if name == "message" {
				column = i
			}
		}
		if column < 0 {
			counts["files without a message column"]++
			return nil
		}
		for n, row := range rows[1:] {
			if strings.Join(row, "") == "" {
				counts["empty rows"]++
				continue
			}
			counts["rows"]++
			if !utf8.ValidString(row[column]) {
				counts["cells that are not UTF-8"]++
				continue
			}
			slackEntities(counts, row[column])
			if err := add(Item{ID: fmt.Sprintf("%s:%d", m.Inner, n+2), File: m.Inner, Text: row[column]}); err != nil {
				return err
			}
		}
		return nil
	})
	return counts, err
}

// CSV seed files: every non-empty cell below the header is a text item.
func convertCSVCells(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "rows": 0, "cells": 0, "empty cells": 0, "cells that are not UTF-8": 0}
	err := files.Walk("*/seeds/*.csv", func(m Member) error {
		counts["files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		rows, err := readCSV(data)
		if err != nil || len(rows) == 0 {
			return fmt.Errorf("%s: %v", m.Inner, err)
		}
		for n, row := range rows[1:] {
			counts["rows"]++
			for c, cell := range row {
				switch {
				case cell == "":
					counts["empty cells"]++
				case !utf8.ValidString(cell):
					counts["cells that are not UTF-8"]++
				default:
					counts["cells"]++
					if err := add(Item{ID: fmt.Sprintf("%s:%d:%d", m.Inner, n+2, c+1), File: m.Inner, Text: cell}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return counts, err
}

// Praat TextGrids (AISHELL-4), in the long or the short text format, as UTF-8 or as UTF-16 with a byte-order
// mark. They are read as Praat reads them: only free-standing numbers, "strings" ("" is one quote, and a
// string may run over lines) and <flags> count; labels such as "xmin =" and "intervals [1]:" are comments, as
// is the rest of a line after "!". Each interval's text and each point's mark that is not blank is a text
// item, non-speech marks as written. The counts the file declares must account for every value: anything
// missing or left over refuses the file.
func convertTextGrids(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "intervals": 0, "points": 0, "blank": 0, "files that are not UTF-8": 0}
	err := files.Walk("**/*.TextGrid", func(m Member) error { return textGrid(m, add, counts) })
	return counts, err
}

func textGrid(m Member, add func(Item) error, counts map[string]int) error {
	counts["files"]++
	data, err := m.Read()
	if err != nil {
		return err
	}
	text, ok := decodeTextGrid(data)
	if !ok {
		counts["files that are not UTF-8"]++
		return nil
	}
	tokens, err := textGridTokens(text)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Inner, err)
	}
	g := &grid{tokens: tokens}
	if g.text() != "ooTextFile" || g.text() != "TextGrid" {
		return fmt.Errorf("%s is not a TextGrid text file", m.Inner)
	}
	g.number()
	g.number()
	tiers := 0
	if g.flag() == "<exists>" {
		tiers = g.count()
	}
	for tier := 1; tier <= tiers && g.err == nil; tier++ {
		class := g.text()
		g.text() // the tier's name
		g.number()
		g.number()
		size := g.count()
		for k := 1; k <= size && g.err == nil; k++ {
			var words string
			switch class {
			case "IntervalTier":
				g.number()
				g.number()
				words = g.text()
				counts["intervals"]++
			case "TextTier":
				g.number()
				words = g.text()
				counts["points"]++
			default:
				return fmt.Errorf("%s: tier %d has class %q, not IntervalTier or TextTier", m.Inner, tier, class)
			}
			if g.err != nil {
				break
			}
			if strings.TrimSpace(words) == "" {
				counts["blank"]++
				continue
			}
			if err := add(Item{ID: fmt.Sprintf("%s#%d.%d", m.Inner, tier, k), File: m.Inner, Text: words}); err != nil {
				return err
			}
		}
	}
	if g.err == nil && g.next < len(g.tokens) {
		g.err = errors.New("it holds more values than its counts declare")
	}
	if g.err != nil {
		return fmt.Errorf("%s: %w", m.Inner, g.err)
	}
	return nil
}

// A TextGrid token: a string, a number or a flag.
type gridToken struct {
	kind  byte // 's', 'n' or 'f'
	value string
}

// textGridTokens splits a TextGrid into the tokens Praat reads.
func textGridTokens(text string) ([]gridToken, error) {
	var tokens []gridToken
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
	for i := 0; i < len(text); {
		if space(text[i]) {
			i++
			continue
		}
		switch text[i] {
		case '!':
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		case '"':
			var b strings.Builder
			for i++; ; i++ {
				if i >= len(text) {
					return nil, errors.New("a string is not closed")
				}
				if text[i] == '"' {
					if i+1 < len(text) && text[i+1] == '"' {
						b.WriteByte('"')
						i++
						continue
					}
					i++
					break
				}
				b.WriteByte(text[i])
			}
			if i < len(text) && !space(text[i]) {
				return nil, errors.New("a string runs into the next token")
			}
			tokens = append(tokens, gridToken{'s', b.String()})
			continue
		}
		start := i
		for i < len(text) && !space(text[i]) {
			i++
		}
		word := text[start:i]
		switch _, err := strconv.ParseFloat(word, 64); {
		case err == nil:
			tokens = append(tokens, gridToken{'n', word})
		case word == "<exists>" || word == "<absent>":
			tokens = append(tokens, gridToken{'f', word})
		}
		// Any other word is a label, which Praat ignores.
	}
	return tokens, nil
}

// grid reads a TextGrid's tokens in order; the first token of the wrong kind, or a missing one, is its error.
type grid struct {
	tokens []gridToken
	next   int
	err    error
}

func (g *grid) take(kind byte, what string) string {
	if g.err != nil {
		return ""
	}
	if g.next >= len(g.tokens) {
		g.err = fmt.Errorf("it ends before its counts are met, where %s was due", what)
		return ""
	}
	t := g.tokens[g.next]
	g.next++
	if t.kind != kind {
		g.err = fmt.Errorf("value %d is not %s", g.next, what)
		return ""
	}
	return t.value
}

func (g *grid) text() string   { return g.take('s', "a string") }
func (g *grid) number() string { return g.take('n', "a number") }
func (g *grid) flag() string   { return g.take('f', "a flag") }

// count is a number of tiers, intervals or points: a whole number, not negative.
func (g *grid) count() int {
	word := g.number()
	n, err := strconv.Atoi(word)
	if g.err == nil && (err != nil || n < 0) {
		g.err = fmt.Errorf("value %d is not a count", g.next)
	}
	return n
}

// decodeTextGrid reads UTF-8, or UTF-16 after its byte-order mark. A lone surrogate refuses the file.
func decodeTextGrid(data []byte) (string, bool) {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		order = binary.BigEndian
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		order = binary.LittleEndian
	default:
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		return string(data), utf8.Valid(data)
	}
	data = data[2:]
	if len(data)%2 != 0 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(data); i += 2 {
		unit := rune(order.Uint16(data[i:]))
		if utf16.IsSurrogate(unit) {
			if i+3 >= len(data) {
				return "", false
			}
			unit = utf16.DecodeRune(unit, rune(order.Uint16(data[i+2:])))
			if unit == utf8.RuneError {
				return "", false
			}
			i += 2
		}
		b.WriteRune(unit)
	}
	return b.String(), true
}
