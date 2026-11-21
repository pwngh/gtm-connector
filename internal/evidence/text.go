// Package evidence is the port of the Python package's evidence rules: neutralized
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
	"html"
	"sort"
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
		// A browser reads https:files.example and https:/files.example as https://files.example, a file link as
		// a path on the reader's computer, and the host of a scheme it does not know as an opaque name. A scheme
		// longer than _RAW_LINK reads names no host either.
		if !literal(target, scheme, len(target), "//") || scheme > 33 || !special(target[:scheme]) {
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

// special is whether a scheme, with its colon, is one whose host a browser reads (units._SPECIAL).
func special(scheme []rune) bool {
	switch strings.ToLower(string(scheme)) { // ASCII only
	case "http:", "https:", "ftp:", "ws:", "wss:":
		return true
	}
	return false
}

// linkHost is units._host: the host a browser opens for a link target, read as the URL Standard reads it, or
// "unknown host" when a browser would open another host, or none.
func linkHost(target []rune, bare bool) string {
	if hasPrefix(u.Lower(target), "mailto:") {
		return "mail"
	}
	// A target with no scheme and no // names no host: a browser resolves it against the page it is on.
	authority, _, named, ok := linkAuthority(target, bare)
	if !ok || !named {
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
	// A browser refuses an xn-- label that does not decode to a valid name. A valid one is shown in the ASCII
	// form the browser looks up, which cannot hold two characters that look alike.
	for _, label := range labels {
		if strings.HasPrefix(label, "xn--") {
			if !validIDNA(host) {
				return unknownHost
			}
			break
		}
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

// token is safe_text's replacement for a Slack token, and false when the token is kept. labeled is whether
// the token has a | part.
func token(kind rune, target, label []rune, labeled bool, counts *Counts) (string, bool) {
	target = strip(target)
	switch kind {
	case '!':
		counts.Mentions++
		word, _, _ := partition(target, '^', false)
		switch string(word) {
		case "channel", "here", "everyone":
			return "[mention " + string(word) + "]", true
		}
		return "[mention group]", true
	case '@':
		counts.Mentions++
		if id(target) {
			return "[user " + string(target) + "]", true
		}
		return "[user]", true
	case '#':
		counts.Mentions++
		if id(target) {
			return "[channel " + string(target) + "]", true
		}
		return "[channel]", true
	}
	if _, ok := Target(target); !ok && !labeled {
		return "", false
	}
	// Slack renders any target|label pair as a link, whatever the target is. A Markdown client reads the
	// character references in it, and if that changes the host, neither host is shown.
	counts.Links++
	host := linkHost(target, false)
	if linkHost(unescape(target, false), false) != host {
		host = unknownHost
	}
	if text := strip(label); len(text) > 0 {
		return string(text) + " [link " + host + "]", true
	}
	return "[link " + host + "]", true
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

// SafeText neutralizes one source text (units.safe_text) and says what it replaced. A step can leave a form
// that an earlier one reads, so the steps run again until a round changes nothing.
func SafeText(raw string) (string, Counts, error) {
	var counts Counts
	if !utf8.ValidString(raw) {
		return "", counts, ErrInvalidText
	}
	text := []rune(raw)
	for {
		neutral := neutralize(text, &counts)
		if string(neutral) == string(text) {
			return string(neutral), counts, nil
		}
		text = neutral
	}
}

// neutralize is one round of SafeText (units._neutralize).
func neutralize(raw []rune, counts *Counts) []rune {
	plain := u.NFC(raw)
	text := make([]rune, 0, len(plain))
	for i := 0; i < len(plain); i++ {
		if plain[i] == '\r' && i+1 < len(plain) && plain[i+1] == '\n' {
			continue
		}
		text = append(text, plain[i])
	}
	text = slackTokens(text, counts, true)
	// _OPEN_MENTION: <([!@])([A-Za-z0-9^_-]*)()
	text = substitute(text, func(i int) (int, string, bool) {
		if text[i] != '<' || i+1 >= len(text) || !in(text[i+1], "!@") {
			return 0, "", false
		}
		j := i + 2
		for j < len(text) && (asciiLetter(text[j]) || asciiDigit(text[j]) || in(text[j], "^_-")) {
			j++
		}
		replacement, _ := token(text[i+1], text[i+2:j], []rune{}, true, counts)
		return j, replacement, true
	})
	// A Markdown link goes before a link in running text, so one inside its target is read with it, and a
	// Slack token left in a target that ended no link is read after it.
	text = slackTokens(links(text, counts), counts, false)
	// _RAW_LINK, with what a client leaves out of a link left outside it.
	found := rawLinks(text)
	text = substitute(text, func(i int) (int, string, bool) {
		if len(found) == 0 || found[0].Start != i {
			return 0, "", false
		}
		whole, end, bare := text[found[0].Start:found[0].End], found[0].End, found[0].www
		found = found[1:]
		link := trimmed(whole)
		// A client takes the scheme of a link in running text to be the letters right before its ://, so in
		// "see...https://files.example" it opens files.example.
		start := 0
		if head, ok := schemeHead(whole, 0); ok {
			for start = head - 3; start > 0 && asciiLetter(whole[start-1]); start-- {
			}
			if !special(whole[start : head-2]) {
				start = 0
			}
		}
		target := link[start:]
		host := linkHost(target, bare)
		// A client that ends the link at a quote or a >, or reads its escapes and character references, opens
		// another host, or none, if the host changes there. A dot right after the host or its port may end the
		// link or the sentence, and a browser opens a different host for each, or none. A link cut by a tab or
		// a line break before its host ends goes on in a browser, to another host.
		short := target
		for k, c := range target {
			if in(c, `>"'`) {
				short = target[:k]
				break
			}
		}
		_, open, _, _ := linkAuthority(whole[start:], bare)
		if linkHost(short, bare) != host || linkHost(unescape(target, true), bare) != host ||
			len(link) < len(whole) && whole[len(link)] == '.' && linkHost(whole[start:len(link)+1], bare) != host ||
			open && linkContinues(text, end) {
			host = unknownHost
		}
		counts.Links++
		return end, string(whole[:start]) + "[link " + host + "]" + string(whole[len(link):]), true
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
	// The finished text is read once more as a Markdown client reads it.
	return definitions(autolinks(links(finished, counts), counts), counts)
}

// trimmed is units._trimmed: a link in running text less what a GitHub Markdown client leaves out of it:
// closing punctuation at its end, a ) that closes no ( in the link, and a character reference at its end.
func trimmed(link []rune) []rune {
	opened, closed := 0, 0
	for _, c := range link {
		switch c {
		case '(':
			opened++
		case ')':
			closed++
		}
	}
	end := len(link)
	for end > 0 {
		last := link[end-1]
		switch {
		case last == ')' && closed > opened:
			closed, end = closed-1, end-1
		case in(last, `?!.,:*"'_~`):
			end--
		case last == ';':
			start := end - 2
			for start > 0 && asciiLetter(link[start]) {
				start--
			}
			if start < end-2 && link[start] == '&' {
				end = start
			} else {
				end--
			}
		default:
			return link[:end]
		}
	}
	return link[:end]
}

// punctuation is units._PUNCTUATION: what a backslash escapes in Markdown.
func punctuation(c rune) bool { return in(c, "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~") }

// namedReference is the text an HTML5 named character reference &name; stands for. html.UnescapeString
// reads a name that is not in its table as a shorter one that is, and its result then ends with the ;, which
// no entry's text does but that of &semi;. Its table leaves out two names. TestReferences checks every name of
// Python's html5 table.
func namedReference(name string) (string, bool) {
	switch name {
	case "nGt":
		return "\u226b\u20d2", true
	case "nLt":
		return "\u226a\u20d2", true
	}
	written := "&" + name + ";"
	text := html.UnescapeString(written)
	if text == written || strings.HasSuffix(text, ";") && text != ";" {
		return "", false
	}
	return text, true
}

// reference is units._REFERENCE.match(s, i) with what it stands for: a hexadecimal or a decimal character
// reference, or a named one.
func reference(s []rune, i int) (end int, text string, ok bool) {
	n := len(s)
	j := i + 1
	if j < n && s[j] == '#' {
		j++
		hex := j < n && (s[j] == 'x' || s[j] == 'X')
		if hex {
			j++
		}
		start, code := j, 0
		for j < n && (hex && j-start < 6 && asciiHex(s[j]) || !hex && j-start < 7 && asciiDigit(s[j])) {
			digit := int(s[j] - '0')
			if hex && !asciiDigit(s[j]) {
				digit = int(s[j]|0x20-'a') + 10
			}
			if hex {
				code = code*16 + digit
			} else {
				code = code*10 + digit
			}
			j++
		}
		if j == start || j >= n || s[j] != ';' {
			return 0, "", false
		}
		if code <= 0 || code > 0x10FFFF || code >= 0xD800 && code <= 0xDFFF {
			code = 0xFFFD
		}
		return j + 1, string(rune(code)), true
	}
	start := j
	if j >= n || !asciiLetter(s[j]) {
		return 0, "", false
	}
	for j++; j < n && j-start < 32 && (asciiLetter(s[j]) || asciiDigit(s[j])); j++ {
	}
	if j >= n || s[j] != ';' {
		return 0, "", false
	}
	if text, ok := namedReference(string(s[start:j])); ok {
		return j + 1, text, true
	}
	return 0, "", false
}

// unescape is units._unescape: a Markdown destination as CommonMark reads it. A character reference stands
// for the character it names, and, with escapes, a backslash before ASCII punctuation for that character.
func unescape(s []rune, escapes bool) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		if escapes && s[i] == '\\' && i+1 < len(s) && punctuation(s[i+1]) {
			out = append(out, s[i+1])
			i += 2
			continue
		}
		if s[i] == '&' {
			if end, text, ok := reference(s, i); ok {
				out = append(out, []rune(text)...)
				i = end
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return out
}

// isLink is units._is_link: whether a client opens a Markdown destination, read as CommonMark reads it, as
// a link of its own rather than as a path on the page that shows it: one with a scheme, // or www. at its
// start.
func isLink(target []rune) bool {
	return schemeEnd(target) > 0 || hasPrefix(target, "//") ||
		len(target) >= 4 && target[0]|0x20 == 'w' && target[1]|0x20 == 'w' && target[2]|0x20 == 'w' && target[3] == '.'
}

// markdownTarget is units._target: what a browser opens for a link destination as written: inside its angle
// brackets, with its escapes and character references read and the spaces a browser drops gone.
func markdownTarget(destination []rune) []rune {
	if len(destination) > 0 && destination[0] == '<' {
		destination = destination[1 : len(destination)-1]
	}
	target := unescape(destination, true)
	i, j := 0, len(target)
	for i < j && target[i] == ' ' {
		i++
	}
	for j > i && target[j-1] == ' ' {
		j--
	}
	return target[i:j]
}

// spaces is units._spaces: where the spaces and at most one line break from s[i] end.
func spaces(s []rune, i int) int {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	if i < len(s) && s[i] == '\n' {
		for i++; i < len(s) && s[i] == ' '; i++ {
		}
	}
	return i
}

// blank is units._blank: whether the line from s[i] holds nothing but spaces.
func blank(s []rune, i int) bool {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i == len(s) || s[i] == '\n'
}

// titleEnd is units._title_end: where a link title that opens at s[i] ends, past its closing mark, and false
// when it does not close before a blank line.
func titleEnd(s []rune, i int) (int, bool) {
	closer := s[i]
	if closer == '(' {
		closer = ')'
	}
	for i++; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && punctuation(s[i+1]) {
			i++
			continue
		}
		if s[i] == closer {
			return i + 1, true
		}
		if closer == ')' && s[i] == '(' || s[i] == '\n' && blank(s, i+1) {
			return 0, false
		}
	}
	return 0, false
}

// destination is units._destination: where a link destination that starts at s[i] ends, as CommonMark reads
// one. An inline link's destination ends at a ) that closes no ( of its own, and may be empty.
func destination(s []rune, i int, inline bool) (int, bool) {
	if i < len(s) && s[i] == '<' {
		for i++; i < len(s) && !in(s[i], "<>\n"); {
			if s[i] == '\\' {
				i += 2
			} else {
				i++
			}
		}
		if i < len(s) && s[i] == '>' {
			return i + 1, true
		}
		return 0, false
	}
	start, depth := i, 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) && punctuation(s[i+1]) {
			i += 2
			continue
		}
		if s[i] == '(' {
			if depth++; depth > 32 {
				return 0, false
			}
		} else if s[i] == ')' {
			if depth == 0 {
				break
			}
			depth--
		} else if s[i] <= ' ' || s[i] == 0x7F {
			break
		}
		i++
	}
	if depth > 0 || inline && i == len(s) || !inline && i == start {
		return 0, false
	}
	return i, true
}

// links is units._links: CommonMark's inline links. Each one whose destination opens a link of its own
// becomes its label and a marker. Brackets pair innermost first, as a client pairs them, so a link that a
// replaced one leaves around it is read too, and so is a marker followed by a destination.
func links(text []rune, counts *Counts) []rune {
	out := make([]rune, 0, len(text))
	var openers []int
	// marker holds what a replacement left to read, its first character last.
	var marker []rune
	for i := 0; len(marker) > 0 || i < len(text); {
		var c rune
		if len(marker) > 0 {
			c, marker = marker[len(marker)-1], marker[:len(marker)-1]
		} else {
			c = text[i]
			i++
			if c == '\\' && i < len(text) && punctuation(text[i]) {
				out = append(out, c, text[i])
				i++
				continue
			}
			if c == '\n' && blank(text, i) {
				// A blank line ends a paragraph, and no link spans one.
				openers = openers[:0]
			}
		}
		if c == '[' {
			openers = append(openers, len(out))
		} else if c == ']' && len(openers) > 0 {
			start := openers[len(openers)-1]
			openers = openers[:len(openers)-1]
			if len(marker) == 0 && i < len(text) && text[i] == '(' {
				begin := spaces(text, i+1)
				end, ok := destination(text, begin, true)
				after := -1
				if ok {
					after = spaces(text, end)
					if after > end && after < len(text) && in(text[after], `"'(`) {
						if title, closed := titleEnd(text, after); closed {
							after = spaces(text, title)
						} else {
							after = -1
						}
					}
				}
				if after >= 0 && after < len(text) && text[after] == ')' {
					if target := markdownTarget(text[begin:end]); isLink(target) {
						counts.Links++
						out = append(append(out[:start], out[start+1:]...), ' ')
						word := []rune("[link " + linkHost(target, false) + "]")
						for k := len(word) - 1; k >= 0; k-- {
							marker = append(marker, word[k])
						}
						i = after + 1
						continue
					}
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// autolinks is units._autolinks: CommonMark's angle-bracket links, each replaced by a marker. One is a
// scheme of 2 to 32 characters, a colon, and no space up to the closing bracket, or a mail address.
func autolinks(s []rune, counts *Counts) []rune {
	return substitute(s, func(i int) (int, string, bool) {
		n := len(s)
		if s[i] != '<' || i+1 >= n {
			return 0, "", false
		}
		if asciiLetter(s[i+1]) {
			j := i + 2
			for j < n && j-i-1 < 32 && (asciiLetter(s[j]) || asciiDigit(s[j]) || in(s[j], "+.-")) {
				j++
			}
			if j-i-1 >= 2 && j < n && s[j] == ':' {
				for j++; j < n && !in(s[j], " \n<>"); j++ {
				}
				if j < n && s[j] == '>' {
					counts.Links++
					return j + 1, "[link " + linkHost(unescape(s[i+1:j], false), false) + "]", true
				}
			}
		}
		if end, ok := mailAddress(s, i+1); ok {
			counts.Links++
			return end, "[link mail]", true
		}
		return 0, "", false
	})
}

// mailAddress is where the mail-address alternative of units._AUTOLINK ends when its address starts at s[i]:
// a local part, an @, and labels of 1 to 63 letters, digits or hyphens that neither start nor end with a
// hyphen, joined by dots, then the closing bracket.
func mailAddress(s []rune, i int) (int, bool) {
	n := len(s)
	j := i
	for j < n && (asciiLetter(s[j]) || asciiDigit(s[j]) || in(s[j], ".!#$%&'*+/=?^_`{|}~-")) {
		j++
	}
	if j == i || j >= n || s[j] != '@' {
		return 0, false
	}
	start := j + 1
	for j = start; j < n && (asciiLetter(s[j]) || asciiDigit(s[j]) || s[j] == '.' || s[j] == '-'); j++ {
	}
	if j >= n || s[j] != '>' {
		return 0, false
	}
	for _, label := range strings.Split(string(s[start:j]), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return 0, false
		}
	}
	return j + 1, true
}

// definition is units._definition: a reference definition that starts at s[i], as CommonMark reads one:
// where its destination starts and ends, and where its last line ends. A title must close, and nothing but
// spaces may follow the destination or the title on its line.
func definition(s []rune, i int) (start, end, last int, ok bool) {
	n := len(s)
	j := i
	for j < n && j-i < 3 && s[j] == ' ' {
		j++
	}
	if j >= n || s[j] != '[' {
		return 0, 0, 0, false
	}
	j++
	label := j
	for ; j < n && !in(s[j], "[]"); j++ {
		if s[j] == '\\' && j+1 < n {
			j++
		} else if s[j] == '\n' && blank(s, j+1) {
			return 0, 0, 0, false
		}
	}
	if !literal(s, j, n, "]:") || j-label > 999 || !hasWord(s[label:j]) {
		return 0, 0, 0, false
	}
	start = spaces(s, j+2)
	if end, ok = destination(s, start, false); !ok {
		return 0, 0, 0, false
	}
	if after := spaces(s, end); after > end && after < n && in(s[after], `"'(`) {
		if title, closed := titleEnd(s, after); closed {
			for title < n && s[title] == ' ' {
				title++
			}
			if title == n || s[title] == '\n' {
				return start, end, title, true
			}
		}
	}
	last = end
	for last < n && s[last] == ' ' {
		last++
	}
	if last == n || s[last] == '\n' {
		return start, end, last, true
	}
	return 0, 0, 0, false
}

// hasWord is whether s holds a character that is not a space or a line break.
func hasWord(s []rune) bool {
	for _, c := range s {
		if c != ' ' && c != '\n' {
			return true
		}
	}
	return false
}

// blockLine is units._BLOCK.fullmatch(line): a line that a Markdown client reads as a heading, a rule, a
// setext underline or a code fence.
func blockLine(line []rune) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) == 0 {
		return false
	}
	run := 0
	for run < len(rest) && rest[run] == rest[0] {
		run++
	}
	switch rest[0] {
	case '#':
		return run <= 6 && (run == len(rest) || rest[run] == ' ')
	case '*', '-', '_':
		marks := 0
		for _, c := range rest {
			if c == rest[0] {
				marks++
			} else if c != ' ' {
				return false
			}
		}
		return marks >= 3
	case '=':
		for _, c := range rest[run:] {
			if c != ' ' {
				return false
			}
		}
		return true
	case '`', '~':
		return run >= 3
	}
	return false
}

// itemEnd is where units._ITEM.match(line, i) ends: a list item's marker, after at most three spaces, and
// the spaces after it. It is -1 when there is none.
func itemEnd(line []rune, i int) int {
	j := i
	for j < len(line) && j-i < 3 && line[j] == ' ' {
		j++
	}
	if j < len(line) && in(line[j], "-+*") {
		j++
	} else {
		k := j
		for k < len(line) && k-j < 9 && asciiDigit(line[k]) {
			k++
		}
		if k == j || k >= len(line) || !in(line[k], ".)") {
			return -1
		}
		j = k + 1
	}
	if j < len(line) && line[j] != ' ' {
		return -1
	}
	for j < len(line) && line[j] == ' ' {
		j++
	}
	return j
}

// definitions is units._definitions: the destination of each reference definition becomes a marker when it
// opens a link of its own or is written in angle brackets. A definition starts a paragraph: at the start of
// the text, after a blank line, a heading, a rule or a code fence, at the start of a block quote or a list
// item, or on the line after another definition. It is read inside a block quote with the quote's markers
// left out.
func definitions(text []rune, counts *Counts) []rune {
	// The text of every line after its block-quote markers, joined, and where each character of it is in text.
	var joined []rune
	var origin, starts, depths, items []int
	for position := 0; position <= len(text); {
		end := index(text, '\n', position)
		if end < 0 {
			end = len(text)
		}
		line := text[position:end]
		depth, i := 0, 0
		for {
			j := i
			for j < len(line) && j-i < 3 && line[j] == ' ' {
				j++
			}
			if j == len(line) || line[j] != '>' {
				break
			}
			depth++
			if i = j + 1; i < len(line) && line[i] == ' ' {
				i++
			}
		}
		if position > 0 {
			joined = append(joined, '\n')
			origin = append(origin, position-1)
		}
		item := itemEnd(line, i)
		if item >= 0 {
			item -= i
		}
		starts = append(starts, len(joined))
		depths = append(depths, depth)
		items = append(items, item)
		joined = append(joined, line[i:]...)
		for k := i; k < len(line); k++ {
			origin = append(origin, position+k)
		}
		position = end + 1
	}
	out := make([]rune, 0, len(text))
	done, follows := 0, false
	for number := 0; number < len(starts); {
		opens := follows || number == 0 || items[number] >= 0 || depths[number] > depths[number-1]
		if !opens {
			content := joined[starts[number-1] : starts[number]-1]
			opens = !nonBlank(content) || blockLine(content)
		}
		if opens {
			if start, end, last, ok := definition(joined, starts[number]+max(items[number], 0)); ok {
				if target := markdownTarget(joined[start:end]); joined[start] == '<' || isLink(target) {
					counts.Links++
					out = append(out, text[done:origin[start]]...)
					out = append(out, []rune("[link "+linkHost(target, false)+"]")...)
					done = origin[end-1] + 1
				}
				number = sort.Search(len(starts), func(k int) bool { return starts[k] > last })
				follows = true
				continue
			}
		}
		number++
		follows = false
	}
	return append(out, text[done:]...)
}

// nonBlank is whether s holds a character that is not a space.
func nonBlank(s []rune) bool {
	for _, c := range s {
		if c != ' ' {
			return true
		}
	}
	return false
}

// slackTokens is units._slack_tokens: each Slack <...> token replaced by what token gives, or kept. A token
// holds no < or >, and a line break only when it has a | part. Tokens are read innermost first, so a token
// that forms around a replaced one is read too. With markdown, a link target in angle brackets right after
// ]( is left for the Markdown link it may end.
func slackTokens(text []rune, counts *Counts, markdown bool) []rune {
	out := make([]rune, 0, len(text))
	// Where each open < is in out, and whether a < or > it does not close is left after it.
	type opener struct {
		at   int
		held bool
	}
	var openers []opener
	for _, c := range text {
		if c == '<' {
			openers = append(openers, opener{len(out), false})
		} else if c == '>' && len(openers) > 0 {
			open := openers[len(openers)-1]
			openers = openers[:len(openers)-1]
			inner := out[open.at+1:]
			var kind rune
			if len(inner) > 0 && in(inner[0], "!@#") {
				kind, inner = inner[0], inner[1:]
			}
			target, label, labeled := partition(inner, '|', false)
			if !open.held && (labeled || !contains(target, '\n')) && !(markdown && kind == 0 && opensTarget(out, open.at)) {
				if replacement, ok := token(kind, target, label, labeled, counts); ok {
					out = append(out[:open.at], []rune(replacement)...)
					continue
				}
			}
			if len(openers) > 0 {
				openers[len(openers)-1].held = true
			}
		}
		out = append(out, c)
	}
	return out
}

// opensTarget is units._opens_target: whether out[start] follows a ]( and the spaces and at most one line
// break a Markdown link allows there.
func opensTarget(out []rune, start int) bool {
	i := start - 1
	for i >= 0 && out[i] == ' ' {
		i--
	}
	if i >= 0 && out[i] == '\n' {
		for i--; i >= 0 && out[i] == ' '; i-- {
		}
	}
	return i >= 1 && out[i] == '(' && out[i-1] == ']'
}
