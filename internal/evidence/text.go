// Package evidence is the port of the evidence rules in gtm_connector/units.py: neutralized source text
// and whole sentences. It matches the Python oracle in testdata/oracle exactly.
package evidence

import (
	"fmt"
	"strings"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Counts is what SafeText replaced.
type Counts struct {
	Mentions, Links, Controls, Brackets int
}

// strip is str.strip() with no argument.
func strip(s []rune) []rune {
	i, j := 0, len(s)
	for i < j && u.IsSpace(s[i]) {
		i++
	}
	for j > i && u.IsSpace(s[j-1]) {
		j--
	}
	return s[i:j]
}

func hasPrefix(s []rune, prefix string) bool { return literal(s, 0, len(s), prefix) }

// Host is the host a card shows for a link target (units._host).
func Host(target []rune) string {
	if hasPrefix(u.Lower(target), "mailto:") {
		return "mail"
	}
	head := target
	if len(head) > 40 {
		head = head[:40]
	}
	url := target
	if !strings.Contains(string(head), "//") {
		url = append([]rune("//"), target...)
	}
	host := []rune{}
	if parts, err := URLSplit(url); err == nil {
		host = u.Lower([]rune(Hostname(parts.Netloc)))
	}
	for hasPrefix(host, "www.") {
		host = host[4:]
	}
	if len(host) < 1 || len(host) > 80 || strings.Contains(string(host), "www.") {
		return "unknown host"
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || asciiDigit(c) || c == '.' || c == '-') {
			return "unknown host"
		}
	}
	return string(host)
}

// id is _ID.fullmatch: a letter or digit, then up to 127 letters, digits, underscores or hyphens.
func id(s []rune) bool {
	if len(s) == 0 || len(s) > 128 || !(asciiLetter(s[0]) || asciiDigit(s[0])) {
		return false
	}
	for _, c := range s[1:] {
		if !(asciiLetter(c) || asciiDigit(c) || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// token is safe_text's replacement for a Slack token. label is nil when the token has no | part.
func token(kind rune, target, label, whole []rune, counts *Counts) string {
	target = strip(target)
	switch kind {
	case '!':
		counts.Mentions++
		word, _, _ := partition(target, '^', false)
		switch string(word) {
		case "channel", "here", "everyone":
			return "[mention " + string(word) + "]"
		}
		return "[mention group]"
	case '@':
		counts.Mentions++
		if id(target) {
			return "[user " + string(target) + "]"
		}
		return "[user]"
	case '#':
		counts.Mentions++
		if id(target) {
			return "[channel " + string(target) + "]"
		}
		return "[channel]"
	}
	if _, ok := Target(target); ok || label != nil {
		// Slack renders any target|label pair as a link, whatever the target is.
		counts.Links++
		if text := strip(label); len(text) > 0 {
			return string(text) + " [link " + Host(target) + "]"
		}
		return "[link " + Host(target) + "]"
	}
	return string(whole)
}

// substitute is re.sub with a scanner: match gives the end of a match at i, and its replacement.
func substitute(s []rune, match func(i int) (int, string, bool)) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		end, replacement, ok := match(i)
		if !ok {
			out = append(out, s[i])
			i++
			continue
		}
		out = append(out, []rune(replacement)...)
		i = end
	}
	return out
}

// SafeText neutralizes one source text (units.safe_text) and says what it replaced. The text must be
// valid UTF-8.
func SafeText(raw string) (string, Counts) {
	var counts Counts
	plain := u.NFC([]rune(raw))
	text := make([]rune, 0, len(plain))
	for i := 0; i < len(plain); i++ {
		if plain[i] == '\r' && i+1 < len(plain) && plain[i+1] == '\n' {
			continue
		}
		text = append(text, plain[i])
	}

	// _SLACK_TOKEN: <([!@#]?)([^<>|\n]*)(?:\|([^<>\n]*))?>
	text = substitute(text, func(i int) (int, string, bool) {
		n := len(text)
		if text[i] != '<' {
			return 0, "", false
		}
		j := i + 1
		var kind rune
		if j < n && in(text[j], "!@#") {
			kind = text[j]
			j++
		}
		targetStart := j
		for j < n && !in(text[j], "<>|\n") {
			j++
		}
		target := text[targetStart:j]
		switch {
		case j < n && text[j] == '>':
			return j + 1, token(kind, target, nil, text[i:j+1], &counts), true
		case j < n && text[j] == '|':
			k := j + 1
			for k < n && !in(text[k], "<>\n") {
				k++
			}
			if k < n && text[k] == '>' {
				return k + 1, token(kind, target, append([]rune{}, text[j+1:k]...), text[i:k+1], &counts), true
			}
		}
		return 0, "", false
	})
	// _OPEN_MENTION: <([!@])([A-Za-z0-9^_-]*)()
	text = substitute(text, func(i int) (int, string, bool) {
		if text[i] != '<' || i+1 >= len(text) || !in(text[i+1], "!@") {
			return 0, "", false
		}
		j := i + 2
		for j < len(text) && (asciiLetter(text[j]) || asciiDigit(text[j]) || in(text[j], "^_-")) {
			j++
		}
		return j, token(text[i+1], text[i+2:j], []rune{}, text[i:j], &counts), true
	})
	// _MARKDOWN_LINK: \[([^\[\]\n]*)\]\(\s*([^\s()]+)\s*\)
	text = substitute(text, func(i int) (int, string, bool) {
		n := len(text)
		if text[i] != '[' {
			return 0, "", false
		}
		j := i + 1
		for j < n && !in(text[j], "[]\n") {
			j++
		}
		if j+1 >= n || text[j] != ']' || text[j+1] != '(' {
			return 0, "", false
		}
		k := j + 2
		for k < n && u.ReSpace(text[k]) {
			k++
		}
		targetStart := k
		for k < n && !u.ReSpace(text[k]) && text[k] != '(' && text[k] != ')' {
			k++
		}
		target := text[targetStart:k]
		for k < n && u.ReSpace(text[k]) {
			k++
		}
		if len(target) == 0 || k >= n || text[k] != ')' {
			return 0, "", false
		}
		if _, ok := Target(target); !ok {
			return k + 1, string(text[i : k+1]), true
		}
		counts.Links++
		return k + 1, string(text[i+1:j]) + " [link " + Host(target) + "]", true
	})
	// _RAW_LINK, with closing punctuation around a link left outside it.
	links := RawLinks(text)
	text = substitute(text, func(i int) (int, string, bool) {
		if len(links) == 0 || links[0].Start != i {
			return 0, "", false
		}
		link := text[links[0].Start:links[0].End]
		end := links[0].End
		links = links[1:]
		kept := len(link)
		for kept > 0 && in(link[kept-1], ".,;:!?)]}'\"") {
			kept--
		}
		counts.Links++
		return end, "[link " + Host(link[:kept]) + "]" + string(link[kept:]), true
	})

	visible := make([]rune, 0, len(text))
	for _, c := range text {
		switch {
		case c == 0x0A || c == 0x0B || c == 0x0C || c == 0x85 || c == 0x2028 || c == 0x2029:
			visible = append(visible, '\n')
		case c == '\t':
			visible = append(visible, ' ')
		case c < 32 || c >= 127 && c <= 159 || c > 159 && (u.Category(c) == "Cf" || u.Category(c) == "Cs"):
			counts.Controls++
			visible = append(visible, []rune(fmt.Sprintf("[U+%04X]", c))...)
		default:
			visible = append(visible, c)
		}
	}
	// _CITATION: a bracketed integer is how a card cites a unit, so source text never keeps one.
	result := substitute(visible, func(i int) (int, string, bool) {
		if visible[i] != '[' {
			return 0, "", false
		}
		j := i + 1
		for j < len(visible) && u.ReDigit(visible[j]) {
			j++
		}
		if j == i+1 || j >= len(visible) || visible[j] != ']' {
			return 0, "", false
		}
		counts.Brackets++
		return j + 1, "(" + string(visible[i+1:j]) + ")", true
	})
	return string(result), counts
}
