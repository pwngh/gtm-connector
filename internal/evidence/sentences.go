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

// block is a run of text that no sentence crosses: a paragraph, and in a message also a block quote, a code
// block, a list item or a speaker's turn. quoted is whether it is a block quote or a speaker's turn.
type block struct {
	text   []rune
	quoted bool
}

// paragraphs splits text at a blank line: \n[ \t]*\n.
func paragraphs(s []rune) []block {
	var found []block
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j >= len(s) || s[j] != '\n' {
			continue
		}
		found = append(found, block{text: s[start:i]})
		start = j + 1
		i = j
	}
	return append(found, block{text: s[start:]})
}

// messageBlocks splits a chat message into blocks. A line break is a wrap, except that a blank line ends a
// block and each of these starts one: a line that starts a list item, a known person's turn (see turn), a
// run of lines that start a block quote (> or &gt;), and the line that opens a code fence. The line after a
// block quote, and the line after the one that closes a code fence, start one too. Inside a code block
// nothing else starts a block. A block quote and a turn are marked quoted: the author relays someone else's
// words, and a turn keeps its label in the text to say whose.
func messageBlocks(s []rune, known func(name string) bool) []block {
	var found []block
	ended, fenced, quoting := true, false, false
	for start := 0; start <= len(s); {
		end := start
		for end < len(s) && s[end] != '\n' {
			end++
		}
		line := s[start:end]
		start = end + 1
		if spacesOnly(line) {
			ended, quoting = true, false
			continue
		}
		trimmed := strip(line)
		begins, closes, quote, label := false, false, false, false
		switch {
		case fenced:
			closes = hasSuffix(trimmed, "```")
			fenced = !closes
		case hasPrefix(trimmed, "```"):
			begins = true
			closes = len(trimmed) > 3 && hasSuffix(trimmed, "```")
			fenced = !closes
		case hasPrefix(trimmed, ">") || hasPrefix(trimmed, "&gt;"):
			begins, quote = !quoting, true
		default:
			label = turn(trimmed, known)
			begins = quoting || label || listItem(line)
		}
		if ended || begins {
			found = append(found, block{quoted: quote || label})
		} else {
			found[len(found)-1].text = append(found[len(found)-1].text, '\n')
		}
		found[len(found)-1].text = append(found[len(found)-1].text, line...)
		ended, quoting = closes, quote
	}
	return found
}

// spacesOnly is whether a line holds only spaces and tabs, as a blank line between paragraphs does.
func spacesOnly(line []rune) bool {
	for _, c := range line {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

func hasSuffix(s []rune, word string) bool {
	n := len([]rune(word))
	return len(s) >= n && literal(s, len(s)-n, len(s), word)
}

// listItem is whether a line starts a list item: after any spaces, a Markdown marker (-, +, *, or one to
// nine digits then . or )) or a bullet Slack writes for a list (•, ◦ or ▪), then a space or the end of the
// line.
func listItem(line []rune) bool {
	j := 0
	for j < len(line) && u.IsSpace(line[j]) {
		j++
	}
	switch {
	case j < len(line) && in(line[j], "-+*•◦▪"):
		j++
	case j < len(line) && asciiDigit(line[j]):
		k := j
		for k < len(line) && k-j < 9 && asciiDigit(line[k]) {
			k++
		}
		if k == len(line) || !in(line[k], ".)") {
			return false
		}
		j = k + 1
	default:
		return false
	}
	return j == len(line) || u.IsSpace(line[j])
}

// turn is whether a line is a known person's turn, as a pasted exchange writes one: up to its first colon,
// a label that known says names a person of the card, then a space and more, as in "Dana Ito: we can
// sign". A label that names nobody known, such as "Update:", leaves the line the author's own.
func turn(line []rune, known func(name string) bool) bool {
	colon := index(line, ':', 0)
	if known == nil || colon < 1 || colon+2 >= len(line) || !u.IsSpace(line[colon+1]) || u.IsSpace(line[colon+2]) {
		return false
	}
	return known(string(line[:colon]))
}

// Sentences splits neutralized text into whole sentences. A line break inside a paragraph is a wrap;
// only a blank line or closing punctuation ends a sentence. With lines, as in a Slack message, the blocks
// messageBlocks finds end sentences too; with no person known, no line is a turn.
func Sentences(safe string, lines bool) ([]string, error) {
	found, _, err := sentences(safe, lines, nil)
	return found, err
}

// sentences is Sentences, with the people a turn may name, and whether each sentence lies in a block quote
// or a turn.
func sentences(safe string, lines bool, known func(name string) bool) ([]string, []bool, error) {
	if !utf8.ValidString(safe) {
		return nil, nil, ErrInvalidText
	}
	text := []rune(safe)
	parts := paragraphs(text)
	if lines {
		parts = messageBlocks(text, known)
	}
	found, quoted := []string{}, []bool{}
	for _, paragraph := range parts {
		var line []rune
		for i, field := range fields(paragraph.text) {
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
				found, quoted = append(found, string(line[start:end.End1])), append(quoted, paragraph.quoted)
				start = end.End2
			}
		}
		if start < len(line) {
			found, quoted = append(found, string(line[start:])), append(quoted, paragraph.quoted)
		}
	}
	return found, quoted, nil
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
