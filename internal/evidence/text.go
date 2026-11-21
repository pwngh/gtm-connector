// Package evidence is the port of the evidence rules in gtm_connector/units.py and briefing.py: neutralized
// source text, whole sentences, and the Deal card built from one evidence snapshot. It matches the Python
// oracle in testdata/oracle exactly.
//
// SafeText and Sentences take valid UTF-8 and refuse anything else with ErrInvalidText. A Python str can
// hold a lone surrogate, which safe_text would show as [U+D800], but no source can hand the connector one:
// strict_json refuses them and every decode is strict. So Go refuses such text instead of carrying it or
// replacing it. The scanners take code points and read any value as Python's re reads that code point.
package evidence

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// ErrInvalidText refuses text that is not valid UTF-8, a lone surrogate included.
var ErrInvalidText = errors.New("evidence: text is not valid UTF-8")

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

const unknownHost = "unknown host"

// schemeEnd is where _SCHEME.match(target) ends: a name and a colon at the start of a link target. It is 0
// when the target starts with no scheme.
func schemeEnd(target []rune) int {
	if len(target) == 0 || !asciiLetter(target[0]) {
		return 0
	}
	i := 1
	for i < len(target) && (asciiLetter(target[i]) || asciiDigit(target[i]) || in(target[i], "+.-")) {
		i++
	}
	if i < len(target) && target[i] == ':' {
		return i + 1
	}
	return 0
}

// linkAuthority is units._authority: the part of a link target that names its host, whether the target ends
// inside it, and whether the part is an authority: what follows //, or a www. address in running text. ok is
// false when a browser reads no host there. A bare target is a www. address in running text, which a client
// opens as a web address, so a colon in it starts a port. Any other target that starts with a name and a
// colon has a scheme.
func linkAuthority(target []rune, bare bool) (authority []rune, open, named, ok bool) {
	start, scheme := 0, 0
	if !bare {
		scheme = schemeEnd(target)
	}
	switch {
	case scheme > 0:
		// A browser reads https:files.example and https:/files.example as https://files.example, and a file
		// link as a path on the reader's computer. A scheme longer than _RAW_LINK reads names no host either.
		if !literal(target, scheme, len(target), "//") || scheme > 33 ||
			strings.ToLower(string(target[:scheme])) == "file:" { // ASCII only
			return nil, false, false, false
		}
		start = scheme + 2
	case !bare && hasPrefix(target, "//"):
		start = 2
	}
	end := start
	for end < len(target) && !in(target[end], "/?#") {
		end++
	}
	return target[start:end], end == len(target), bare || start > 0, true
}

// linkHost is units._host: the host a browser opens for a link target, read as the URL Standard reads it, or
// "unknown host" when a browser would open another host, or none.
func linkHost(target []rune, bare bool) string {
	if hasPrefix(u.Lower(target), "mailto:") {
		return "mail"
	}
	authority, _, named, ok := linkAuthority(target, bare)
	if !ok {
		return unknownHost
	}
	// A backslash, a control character or a bracket gives no host, and so does a character whose compatibility
	// form is one of / ? # @ :.
	for _, c := range authority {
		if c < 0x20 || c >= 0x7F && c <= 0x9F || in(c, `\[]`) || c > 0x7F && u.NFKCNetloc(c) {
			return unknownHost
		}
	}
	// The host follows the last @ and ends at the first colon. A browser refuses a port that is not a number
	// up to 65535.
	_, hostPort, _ := partition(authority, '@', true)
	name, port, _ := partition(hostPort, ':', false)
	number, digits := 0, 0
	for _, c := range port {
		if !asciiDigit(c) {
			return unknownHost
		}
		if digits > 0 || c != '0' {
			digits++
			if number = number*10 + int(c-'0'); digits > 5 || number > 65535 {
				return unknownHost
			}
		}
	}
	if len(name) == 0 {
		return unknownHost
	}
	for _, c := range name {
		if !(asciiLetter(c) || asciiDigit(c) || c == '.' || c == '-') {
			return unknownHost
		}
	}
	host := strings.ToLower(string(name)) // ASCII only
	// A browser reads a host whose last label is a number as an address and rewrites or refuses it: 0xc0000201
	// and 3221225985 both open 192.0.2.1. Only four plain decimal parts stay as written.
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if len(labels) > 1 && last == "" {
		last = labels[len(labels)-2]
	}
	if numberLabel(last) && !plainAddress(labels) {
		return unknownHost
	}
	// The host of an authority is shown without a leading www., which a client would turn back into a link:
	// files.example names the site of www.files.example. A host that still holds www. is not shown.
	for named && strings.HasPrefix(host, "www.") {
		host = host[4:]
	}
	if len(host) < 1 || len(host) > 80 || strings.Contains(host, "www.") {
		return unknownHost
	}
	return host
}

// numberLabel is _NUMBER.fullmatch(label): decimal digits, or 0x and any hexadecimal digits.
func numberLabel(label string) bool {
	digits := []rune(label)
	hex := strings.HasPrefix(label, "0x")
	if hex {
		digits = digits[2:]
	} else if label == "" {
		return false
	}
	for _, c := range digits {
		if !(asciiDigit(c) || hex && c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// plainAddress is _IPV4.fullmatch(host) on the host's labels: four decimal numbers up to 255, none with a
// leading zero.
func plainAddress(labels []string) bool {
	if len(labels) != 4 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 3 || len(label) > 1 && label[0] == '0' {
			return false
		}
		value := 0
		for _, c := range label {
			if !asciiDigit(c) {
				return false
			}
			value = value*10 + int(c-'0')
		}
		if value > 255 {
			return false
		}
	}
	return true
}

// linkContinues is _CONTINUES.match(s, i): tabs or line breaks, then a character that is not a space and
// does not start a path, a query or a fragment. A browser drops a tab or a line break from a link, so a link
// cut by one goes on in what follows.
func linkContinues(s []rune, i int) bool {
	j := i
	for j < len(s) && in(s[j], "\t\n\r") {
		j++
	}
	return j > i && j < len(s) && !u.ReSpace(s[j]) && !in(s[j], "/?#")
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
			return string(text) + " [link " + linkHost(target, false) + "]"
		}
		return "[link " + linkHost(target, false) + "]"
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

// SafeText neutralizes one source text (units.safe_text) and says what it replaced.
func SafeText(raw string) (string, Counts, error) {
	var counts Counts
	if !utf8.ValidString(raw) {
		return "", counts, ErrInvalidText
	}
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
		// A client renders a target with any scheme as a link, whatever follows the colon.
		if _, ok := Target(target); !ok && schemeEnd(target) == 0 {
			return k + 1, string(text[i : k+1]), true
		}
		counts.Links++
		return k + 1, string(text[i+1:j]) + " [link " + linkHost(target, false) + "]", true
	})
	// _RAW_LINK, with closing punctuation around a link left outside it.
	links := rawLinks(text)
	text = substitute(text, func(i int) (int, string, bool) {
		if len(links) == 0 || links[0].Start != i {
			return 0, "", false
		}
		link, end, bare := text[links[0].Start:links[0].End], links[0].End, links[0].www
		links = links[1:]
		kept := len(link)
		for kept > 0 && in(link[kept-1], ".,;:!?)]}'\"") {
			kept--
		}
		host := linkHost(link[:kept], bare)
		// A dot right after the host or its port may end the link or the sentence, and a browser opens a
		// different host for each, or none. A link cut by a tab or a line break before its host ends goes on in
		// a browser, to another host.
		_, open, _, _ := linkAuthority(link, bare)
		if kept < len(link) && link[kept] == '.' && linkHost(link[:kept+1], bare) != host ||
			open && linkContinues(text, end) {
			host = unknownHost
		}
		counts.Links++
		return end, "[link " + host + "]" + string(link[kept:]), true
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
	finished := substitute(visible, func(i int) (int, string, bool) {
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
	// The finished text is read once more as a Markdown client reads it. Each step can leave a bracket the
	// next one reads, so the order is fixed.
	finished = angleLinks(finished, &counts)
	finished = definitions(finished, &counts)
	finished = openTargets(finished, &counts)
	return string(finished), counts, nil
}

// angleLinks is _AUTOLINK.sub: an angle-bracket link is a scheme of 2 to 32 characters, a colon, and no space
// up to the closing bracket.
func angleLinks(s []rune, counts *Counts) []rune {
	return substitute(s, func(i int) (int, string, bool) {
		n := len(s)
		if s[i] != '<' || i+1 >= n || !asciiLetter(s[i+1]) {
			return 0, "", false
		}
		j := i + 2
		for j < n && j-i-1 < 32 && (asciiLetter(s[j]) || asciiDigit(s[j]) || in(s[j], "+.-")) {
			j++
		}
		if j-i-1 < 2 || j >= n || s[j] != ':' {
			return 0, "", false
		}
		for j++; j < n && !in(s[j], " \n<>"); j++ {
		}
		if j >= n || s[j] != '>' {
			return 0, "", false
		}
		counts.Links++
		return j + 1, "[link " + linkHost(s[i+1:j], false) + "]", true
	})
}

// definition is _DEFINITION.match(s, i): a line that starts, after at most three spaces, with a bracketed
// label and a colon, then a destination on that line or the next. Only a destination that starts with a
// scheme or < is read. It gives where the destination starts and ends.
func definition(s []rune, i int) (start, end int, ok bool) {
	n := len(s)
	if i > 0 && s[i-1] != '\n' {
		return 0, 0, false
	}
	j := i
	for j < n && j-i < 3 && s[j] == ' ' {
		j++
	}
	if j >= n || s[j] != '[' {
		return 0, 0, false
	}
	// The label runs to the first bracket that no backslash comes before.
	label := j + 1
	for j = label; j < n && !in(s[j], "[]"); j++ {
		if s[j] == '\\' {
			if j+1 >= n {
				break
			}
			j++
		}
	}
	if j == label || !literal(s, j, n, "]:") {
		return 0, 0, false
	}
	for j += 2; j < n && s[j] == ' '; j++ {
	}
	if j < n && s[j] == '\n' {
		for j++; j < n && s[j] == ' '; j++ {
		}
	}
	if j >= n {
		return 0, 0, false
	}
	start = j
	if s[j] == '<' {
		for j++; j < n && !in(s[j], "<>\n"); j++ {
		}
		if j >= n || s[j] != '>' {
			return 0, 0, false
		}
		return start, j + 1, true
	}
	if j += schemeEnd(s[j:]); j == start {
		return 0, 0, false
	}
	for j < n && s[j] != ' ' && s[j] != '\n' {
		j++
	}
	return start, j, true
}

// titled is _TITLE.fullmatch(s, i, end): what may follow a destination on its line. That is nothing, or a
// title in quotes or parentheses, which may go on to the next line. Anything else makes the line ordinary
// text, as in "[10:32]: Dana: hello".
func titled(s []rune, i, end int) bool {
	j := i
	for j < end && s[j] == ' ' {
		j++
	}
	if j == end {
		return true
	}
	if j == i || !in(s[j], `"'(`) {
		return false
	}
	closer := s[j]
	if closer == '(' {
		closer = ')'
	}
	for j++; j < end && s[j] != closer; j++ {
		if s[j] == '\\' {
			j++
		}
	}
	// A title that is not closed on this line goes on. One that is closed ends the line.
	for j++; j < end; j++ {
		if s[j] != ' ' {
			return false
		}
	}
	return true
}

// definitions is _DEFINITION.sub: the destination of a reference definition becomes a link marker.
func definitions(s []rune, counts *Counts) []rune {
	return substitute(s, func(i int) (int, string, bool) {
		start, end, ok := definition(s, i)
		if !ok {
			return 0, "", false
		}
		line := end
		for line < len(s) && s[line] != '\n' {
			line++
		}
		if !titled(s, end, line) {
			return end, string(s[i:end]), true
		}
		counts.Links++
		return end, string(s[i:start]) + "[link " + linkHost(s[start:end], false) + "]", true
	})
}

// openTargets is _OPEN_TARGET.sub: a target with a scheme after a ]( that _MARKDOWN_LINK did not read. It
// follows a label that holds a line break or brackets, a target cut by a control character, a <, or a
// bracketed word the connector wrote.
func openTargets(s []rune, counts *Counts) []rune {
	return substitute(s, func(i int) (int, string, bool) {
		n := len(s)
		if s[i] != ']' || i+1 >= n || s[i+1] != '(' {
			return 0, "", false
		}
		j := i + 2
		for j < n && u.ReSpace(s[j]) {
			j++
		}
		if j < n && s[j] == '<' {
			for j++; j < n && u.ReSpace(s[j]); j++ {
			}
		}
		k := j + schemeEnd(s[j:])
		if k == j {
			return 0, "", false
		}
		for k < n && !u.ReSpace(s[k]) && !in(s[k], `<>"'`) {
			k++
		}
		counts.Links++
		return k, string(s[i:j]) + "[link " + linkHost(s[j:k], false) + "]", true
	})
}
