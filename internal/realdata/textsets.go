package realdata

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
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
// by the header. A turn is a run of tokens with one speaker ID. Its text is the tokens one space apart, each
// after its prepunctuation (when the file has that column) and before its punctuation, with the case
// column applied: UC upper-cases the first letter, CA the whole token, and LC, MC and N/A leave it as it is.
func convertRevNLP(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "tokens": 0, "turns": 0, "JSON files": 0, "lines that are not UTF-8": 0,
		"files strict_json refuses": 0}
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
		for _, line := range all[1:] {
			if !utf8.Valid(line) {
				counts["lines that are not UTF-8"]++
				continue
			}
			cells := strings.Split(string(line), "|")
			if len(cells) != len(column) {
				return fmt.Errorf("%s has a line with %d columns, not %d", m.Inner, len(cells), len(column))
			}
			counts["tokens"]++
			if cells[column["speaker"]] != speaker {
				if err := flush(); err != nil {
					return err
				}
				speaker = cells[column["speaker"]]
			}
			token := cells[column["token"]]
			switch cells[column["case"]] {
			case "UC":
				first, size := utf8.DecodeRuneInString(token)
				token = string(unicode.ToUpper(first)) + token[size:]
			case "CA":
				token = strings.ToUpper(token)
			}
			if hasPre {
				token = cells[pre] + token
			}
			turn = append(turn, token+cells[column["punctuation"]])
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

var ircMessage = regexp.MustCompile(`^\[\d\d:\d\d\] <[^>]*> (.*)$`)

// IRC logs as downloaded (.raw.txt): "[HH:MM] <nick> words" lines. The words are a text item; system lines
// ("=== ..." joins and quits) and other lines are counted.
func convertIRC(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "lines": 0, "messages": 0, "other lines": 0, "lines that are not UTF-8": 0}
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
			found := ircMessage.FindSubmatch(line)
			if found == nil {
				counts["other lines"]++
				continue
			}
			counts["messages"]++
			if err := add(Item{ID: fmt.Sprintf("%s:%d", m.Inner, n+1), File: m.Inner, Text: string(found[1])}); err != nil {
				return err
			}
		}
		return nil
	})
	return counts, err
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
// Files without that column (the annotations) are counted.
func convertCSVColumns(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "files without a message column": 0, "rows": 0,
		"cells that are not UTF-8": 0}
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
			counts["rows"]++
			if !utf8.ValidString(row[column]) {
				counts["cells that are not UTF-8"]++
				continue
			}
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

var textGridText = regexp.MustCompile(`^\s*text = "((?:[^"]|"")*)"\s*$`)

// Praat TextGrids (AISHELL-4), in the long text format, as UTF-8 or as UTF-16 with a byte-order mark: each
// interval's text = "..." is a text item, with "" read as one quote, when it is not empty. Non-speech marks
// stay as written.
func convertTextGrids(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "intervals": 0, "empty intervals": 0, "files that are not UTF-8": 0}
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
	n := 0
	for _, line := range strings.Split(text, "\n") {
		found := textGridText.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if found == nil {
			continue
		}
		n++
		counts["intervals"]++
		words := strings.ReplaceAll(found[1], `""`, `"`)
		if strings.TrimSpace(words) == "" {
			counts["empty intervals"]++
			continue
		}
		if err := add(Item{ID: fmt.Sprintf("%s#%d", m.Inner, n), File: m.Inner, Text: words}); err != nil {
			return err
		}
	}
	return nil
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
