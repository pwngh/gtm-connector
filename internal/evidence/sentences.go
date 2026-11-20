package evidence

import (
	"unicode/utf8"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// fields is str.split() with no argument.
func fields(s []rune) [][]rune {
	var found [][]rune
	for i := 0; i < len(s); {
		for i < len(s) && u.IsSpace(s[i]) {
			i++
		}
		j := i
		for j < len(s) && !u.IsSpace(s[j]) {
			j++
		}
		if j > i {
			found = append(found, s[i:j])
		}
		i = j
	}
	return found
}

// paragraphs is re.split on "\n", or on a blank line: \n[ \t]*\n.
func paragraphs(s []rune, lines bool) [][]rune {
	var found [][]rune
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		j := i + 1
		if !lines {
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j >= len(s) || s[j] != '\n' {
				continue
			}
			j++
		}
		found = append(found, s[start:i])
		start = j
		i = j - 1
	}
	return append(found, s[start:])
}

// Sentences splits neutralized text into whole sentences (units.sentences). A line break inside a
// paragraph is a wrap; only a blank line or closing punctuation ends a sentence. With lines, every line
// break ends one.
func Sentences(safe string, lines bool) ([]string, error) {
	if !utf8.ValidString(safe) {
		return nil, ErrInvalidText
	}
	found := []string{}
	for _, paragraph := range paragraphs([]rune(safe), lines) {
		var line []rune
		for i, field := range fields(paragraph) {
			if i > 0 {
				line = append(line, ' ')
			}
			line = append(line, field...)
		}
		if len(line) == 0 {
			continue
		}
		start := 0
		for _, end := range SentenceEnds(line) {
			if !continues(line, start, end.End1, end.End2) {
				found = append(found, string(line[start:end.End1]))
				start = end.End2
			}
		}
		if start < len(line) {
			found = append(found, string(line[start:]))
		}
	}
	return found, nil
}

func count(s []rune, c rune, from, to int) int {
	n := 0
	for i := from; i < to && i < len(s); i++ {
		if s[i] == c {
			n++
		}
	}
	return n
}

// continues is units._continues: whether the closing punctuation that ends at end leaves its sentence,
// begun at start, unfinished. after is where the next token starts.
func continues(line []rune, start, end, after int) bool {
	n := len(line)
	if after < n && u.IsLower(line[after]) {
		return true
	}
	// A bracket opened in this sentence and closed soon after holds its own punctuation.
	near := max(start, end-200)
	for _, pair := range [][2]rune{{'(', ')'}, {'[', ']'}} {
		if count(line, pair[0], near, end) > count(line, pair[1], near, end) &&
			count(line, pair[1], end, min(end+200, n)) > 0 {
			return true
		}
	}
	tail := end
	for tail > max(start, end-8) && in(line[tail-1], closersLong) {
		tail--
	}
	if tail == max(start, end-8) || line[tail-1] != '.' {
		return false
	}
	if ListMarkerFull(line, start, end) {
		return true
	}
	window := max(start-1, end-24, 0)
	dotted, isDotted := Dotted(line, window, end)
	if _, ok := MayEnd(line, window, end); ok || isDotted && u.IsUpperString(line[dotted:end]) {
		_, opens := Opener(line, after, n)
		return !opens
	}
	if _, ok := Abbreviation(line, window, end); isDotted || ok {
		return true
	}
	if _, ok := SectionSign(line, after, n); ok {
		return true
	}
	digit := after < n && u.IsDigit(line[after])
	if _, wordStart, wordEnd, ok := Short(line, window, end); ok {
		if _, next := ShortNext(line, after, n); next || wordEnd-wordStart < 5 && digit {
			return true
		}
	}
	// "Docket No. A-17" and "No. 14" go on. "No." as the whole sentence is an answer, unless a number
	// follows. The match ends at end.
	_, number := NumberSign(line, window, end)
	return number && (end-start > 5 || digit)
}
