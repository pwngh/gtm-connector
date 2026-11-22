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

// block is a run of text that no sentence crosses: a paragraph, or in a message a run of lines.
type block struct {
	text   []rune
	breaks []int // in a message, where each line break inside the run stands in text, as one space
	quoted bool  // lines of a block quote, or a speaker's turn and the lines under it
	item   bool  // it starts with a list item, whose end is an end
	code   bool  // a line of a code block, one sentence
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

// messageBlocks splits a chat message into blocks: runs of lines that follow each other with no blank line
// between, all in a block quote (after >) or none. A list item and a known person's turn (see turn) start a
// run, and each line of a code block, between its ``` fences, is a block of its own. A block quote and a
// turn with the lines under it are marked quoted: the author relays someone else's words, and a turn keeps
// its label in the text to say whose. A quote marker and a fence are markup, and no part of a block's text;
// a quoted line with only its marker is a blank line, and a line of code with no letter or digit, such as a
// brace, is no block. A code block opened in a block quote ends with the quote.
func messageBlocks(s []rune, known func(name string) bool) []block {
	var found []block
	// fenced: in a code block, whose fences stand in a block quote when fenceQuoted. open: the next line may
	// join the last block, whose lines carry a quote marker when marked.
	fenced, fenceQuoted, open, marked := false, false, false, false
	for start := 0; start <= len(s); {
		end := start
		for end < len(s) && s[end] != '\n' {
			end++
		}
		line := strip(s[start:end])
		start = end + 1
		quote := false
		if !fenced || fenceQuoted {
			for hasPrefix(line, ">") {
				line, quote = strip(line[1:]), true
			}
		}
		fenced = fenced && (quote || !fenceQuoted)
		if fenced || hasPrefix(line, "```") {
			if !fenced {
				line, fenceQuoted = line[3:], quote
			}
			if fenced = !hasSuffix(line, "```"); !fenced {
				line = line[:len(line)-3]
			}
			if lettered(line) {
				found = append(found, block{text: spaced(line), quoted: quote, code: true})
			}
			open = false
			continue
		}
		if line = spaced(line); len(line) == 0 {
			open = false
			continue
		}
		item := listItem(line)
		label := !quote && !item && turn(line, known)
		if last := len(found) - 1; open && !item && !label && marked == quote {
			found[last].breaks = append(found[last].breaks, len(found[last].text))
			found[last].text = append(append(found[last].text, ' '), line...)
			continue
		}
		found = append(found, block{text: line, quoted: quote || label, item: item})
		open, marked = true, quote
	}
	return found
}

// lettered is whether text holds a letter or a digit of any script.
func lettered(s []rune) bool {
	for _, c := range s {
		if u.IsAlpha(c) || u.IsDigit(c) {
			return true
		}
	}
	return false
}

// spaced is text with each run of spaces as one space, and none at its ends.
func spaced(s []rune) []rune {
	var line []rune
	for i, field := range fields(s) {
		if i > 0 {
			line = append(line, ' ')
		}
		line = append(line, field...)
	}
	return line
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
// only a blank line or closing punctuation ends a sentence. With lines, as in a Slack message, a line break
// ends one too, unless the text settles that the next line goes on; with no person known, no line is a turn.
func Sentences(safe string, lines bool) ([]string, error) {
	found, _, _, err := sentences(safe, lines, nil)
	return found, err
}

// sentences is Sentences, with the people a turn may name. For each sentence it says whether it lies in a
// block quote or a turn, and whether it stands at an end that needs no closing punctuation: the end of a
// list item, or a line of a code block.
//
// In a message, Slack wraps no text, so a line break is one its author wrote, and it ends a sentence where
// a space would not let it go on: after closing punctuation, as continues decides for a space there, so
// "Dr." at the end of a line goes on; and with no closing punctuation, unless the next line starts in lower
// case. A list item ends at the first line break that ends a sentence, or with its run.
func sentences(safe string, lines bool, known func(name string) bool) (found []string, quoted, closed []bool, err error) {
	if !utf8.ValidString(safe) {
		return nil, nil, nil, ErrInvalidText
	}
	text := []rune(safe)
	parts := paragraphs(text)
	if lines {
		parts = messageBlocks(text, known)
	}
	found, quoted, closed = []string{}, []bool{}, []bool{}
	for _, part := range parts {
		line := part.text
		if !lines {
			line = spaced(line)
		}
		if len(line) == 0 {
			continue
		}
		start, item, ends, breaks := 0, part.item, SentenceEnds(line), part.breaks
		add := func(to int, atEnd bool) {
			found, quoted = append(found, string(line[start:to])), append(quoted, part.quoted)
			closed = append(closed, part.code || item && atEnd)
			item = item && !atEnd
		}
		for !part.code && len(ends)+len(breaks) > 0 {
			if len(breaks) == 0 || len(ends) > 0 && ends[0].End1 <= breaks[0] {
				// Closing punctuation, at a line break or inside a line.
				end, atBreak := ends[0], len(breaks) > 0 && ends[0].End1 == breaks[0]
				if ends = ends[1:]; atBreak {
					breaks = breaks[1:]
				}
				if !continues(line, start, end.End1, end.End2) {
					add(end.End1, atBreak)
					start = end.End2
				}
				continue
			}
			// A line break with no sentence end before it for the next line to follow.
			at := breaks[0]
			if breaks = breaks[1:]; !u.IsLower(line[at+1]) {
				add(at, true)
				start = at + 1
			}
		}
		if start < len(line) {
			add(len(line), true)
		}
	}
	return found, quoted, closed, nil
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
	// A bracket or a quotation mark opened in this sentence and closed soon after holds its own punctuation.
	near := max(start, end-200)
	for _, pair := range [][2]rune{{'(', ')'}, {'[', ']'}, {'“', '”'}} {
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
	digit := after < n && u.IsDigit(line[after])
	// "no." in lower case is a number sign only before a number or #, so "that is a no." ends its sentence.
	if at, ok := NumberSign(line, window, end); ok && shortForm(line, at, end)[0] == 'n' {
		return digit || after < n && line[after] == '#'
	}
	dotted, isDotted := Dotted(line, window, end)
	if _, ok := MayEnd(line, window, end); ok || isDotted && u.IsUpperString(line[dotted:end]) {
		_, opens := Opener(line, after, n)
		return !opens
	}
	if at, ok := Abbreviation(line, window, end); isDotted || ok {
		// A month or a day written short ends a sentence before a word that opens one: "in Feb. I'll".
		if _, opens := Opener(line, after, n); ok && !isDotted && opens && calendar[shortForm(line, at, end)] {
			return false
		}
		return true
	}
	if _, ok := SectionSign(line, after, n); ok {
		return true
	}
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

// calendar is the months and days of forms: short forms that often end a sentence.
var calendar = words("Mon", "Tue", "Tues", "Wed", "Thu", "Thur", "Thurs", "Fri", "Sat", "Sun",
	"Jan", "Feb", "Mar", "Apr", "Jun", "Jul", "Aug", "Sep", "Sept", "Oct", "Nov", "Dec")

// shortForm is the letters of a short form that Abbreviation or NumberSign matched at at: the match starts
// at the text's start or at the character before the form.
func shortForm(line []rune, at, end int) string {
	if at > 0 || !asciiLetter(line[at]) {
		at++
	}
	return string(line[at:letters(line, at, end)])
}
