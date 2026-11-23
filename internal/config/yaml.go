// Package config reads gtm.yaml: the one file RevOps keeps in Git for what they set. It is read by a strict
// reader of its own, which takes the YAML docs/config.md shows and nothing else, so that a settings file
// holds no surprise: no anchor is followed, no tag is obeyed, "no" is a word and not false, and a key given
// twice is an error. Every refusal names the line and what was expected there, and repeats nothing of the
// file: a value pasted into it by mistake stays out of the terminal.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// MaxBytes is the most a file may hold.
const MaxBytes = 64 << 10

// Error is a file that was refused, with the line it was refused at (0 for the file as a whole).
type Error struct {
	Line    int
	Message string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Message
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

func refuse(line int, format string, args ...any) *Error {
	return &Error{Line: line, Message: fmt.Sprintf(format, args...)}
}

// node is a value of the file: a mapping with its keys in order, a list, or a scalar that is text, a whole
// number or a boolean.
type node struct {
	line  int
	keys  []string
	items map[string]*node
	list  []*node
	text  *string
	whole *int
	truth *bool
}

func (n *node) mapping() bool { return n.items != nil }

func (n *node) kind() string {
	switch {
	case n.mapping():
		return "a mapping"
	case n.list != nil:
		return "a list"
	case n.text != nil:
		return "text"
	case n.whole != nil:
		return "a number"
	}
	return "true or false"
}

type line struct {
	n      int
	indent int
	text   string // without its indent, its comment and trailing space
}

// lines splits the file into its lines that hold something, each without its comment.
func lines(data []byte) ([]line, error) {
	if len(data) > MaxBytes {
		return nil, refuse(0, "the file is larger than %d bytes", MaxBytes)
	}
	if !utf8.Valid(data) {
		return nil, refuse(0, "the file is not UTF-8")
	}
	var out []line
	split := strings.Split(string(data), "\n")
	for i, raw := range split {
		n := i + 1
		// A carriage return is part of a line's end, and of nothing else.
		if i < len(split)-1 {
			raw = strings.TrimSuffix(raw, "\r")
		}
		for _, c := range raw {
			if c == '\t' {
				return nil, refuse(n, "a tab; indent with spaces")
			}
			// Every character prints: no control character, no invisible or direction-changing one, and no
			// space but the plain one.
			if c != ' ' && !unicodetab.IsPrintable(c) {
				return nil, refuse(n, "a control character, or one that does not print")
			}
		}
		text := strings.TrimRight(uncommented(raw), " ")
		trimmed := strings.TrimLeft(text, " ")
		if trimmed == "" {
			continue
		}
		if trimmed == "---" || trimmed == "..." || strings.HasPrefix(trimmed, "--- ") {
			return nil, refuse(n, "a document marker; the file is one document")
		}
		if strings.HasPrefix(trimmed, "%") {
			return nil, refuse(n, "a directive")
		}
		out = append(out, line{n: n, indent: len(text) - len(trimmed), text: trimmed})
	}
	return out, nil
}

// uncommented is a line without its comment: a # at the line's start or after a space, outside quotes.
func uncommented(raw string) string {
	quote := byte(0)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || raw[i-1] == ' '):
			return raw[:i]
		}
	}
	return raw
}

// parse reads the file into its top mapping.
func parse(data []byte) (*node, error) {
	ls, err := lines(data)
	if err != nil {
		return nil, err
	}
	if len(ls) == 0 {
		return &node{items: map[string]*node{}}, nil
	}
	if ls[0].indent != 0 {
		return nil, refuse(ls[0].n, "the first key is indented")
	}
	root, rest, err := block(ls, 0)
	if err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		return nil, refuse(rest[0].n, "this line is indented less than the mapping it follows began")
	}
	return root, nil
}

// block reads the mapping whose keys stand at indent, and gives the lines after it.
func block(ls []line, indent int) (*node, []line, error) {
	out := &node{line: ls[0].n, items: map[string]*node{}}
	for len(ls) > 0 && ls[0].indent >= indent {
		l := ls[0]
		if l.indent > indent {
			return nil, nil, refuse(l.n, "indented %d spaces where the mapping's keys stand at %d", l.indent, indent)
		}
		if strings.HasPrefix(l.text, "- ") || l.text == "-" {
			return nil, nil, refuse(l.n, "a list written over several lines; write it on one line, as [a, b]")
		}
		key, rest, found := strings.Cut(l.text, ":")
		if !found || (rest != "" && rest[0] != ' ') {
			return nil, nil, refuse(l.n, "expected a key and a colon, as in key: value")
		}
		if !plainKey(key) {
			return nil, nil, refuse(l.n, "what stands before the colon is no key; a key is letters, digits, _ and -")
		}
		if _, twice := out.items[key]; twice {
			return nil, nil, refuse(l.n, "a key is given twice")
		}
		ls = ls[1:]
		var value *node
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			if len(ls) == 0 || ls[0].indent <= indent {
				return nil, nil, refuse(l.n, "the key has no value")
			}
			if ls[0].indent != indent+2 {
				return nil, nil, refuse(ls[0].n, "indent by two spaces")
			}
			var err error
			if value, ls, err = block(ls, indent+2); err != nil {
				return nil, nil, err
			}
		} else {
			p := &flow{text: rest, line: l.n}
			var err error
			if value, err = p.value(false); err != nil {
				return nil, nil, err
			}
			if p.skip(); p.at < len(p.text) {
				return nil, nil, refuse(l.n, "there is text after the value")
			}
		}
		out.keys = append(out.keys, key)
		out.items[key] = value
	}
	return out, ls, nil
}

func plainKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '-')) {
			return false
		}
	}
	return true
}

// flow reads a value written on one line: a scalar, {key: value, ...} or [value, ...].
type flow struct {
	text string
	at   int
	line int
}

func (p *flow) skip() {
	for p.at < len(p.text) && p.text[p.at] == ' ' {
		p.at++
	}
}

// value reads one value. inside is whether it stands in a flow mapping or list, where , } and ] end it.
func (p *flow) value(inside bool) (*node, error) {
	p.skip()
	if p.at == len(p.text) {
		return nil, refuse(p.line, "a value is missing")
	}
	switch c := p.text[p.at]; c {
	case '{':
		return p.mapping()
	case '[':
		return p.sequence()
	case '"', '\'':
		return p.quoted(c)
	case '&', '*', '!', '|', '>', '@', '`', '%':
		return nil, refuse(p.line, "a value may not start with %c; put text in quotes", c)
	case ']', '}', ',':
		return nil, refuse(p.line, "a value is missing before %c", c)
	}
	start := p.at
	for p.at < len(p.text) {
		c := p.text[p.at]
		if inside && (c == ',' || c == '}' || c == ']') {
			break
		}
		if inside && (c == '{' || c == '[') || c == '"' || c == '\'' {
			return nil, refuse(p.line, "a %c inside a value; put the text in quotes", c)
		}
		if c == ':' && (p.at+1 == len(p.text) || p.text[p.at+1] == ' ') {
			return nil, refuse(p.line, "a colon inside a value; put the text in quotes")
		}
		p.at++
	}
	text := strings.TrimRight(p.text[start:p.at], " ")
	out := &node{line: p.line}
	switch {
	case text == "":
		return nil, refuse(p.line, "a value is missing")
	case text == "true" || text == "false":
		truth := text == "true"
		out.truth = &truth
	case text == "null" || text == "~":
		return nil, refuse(p.line, "%s is no value here; leave the key out", text)
	case wholeNumber(text):
		whole, err := strconv.Atoi(text)
		if err != nil {
			return nil, refuse(p.line, "the number is too large")
		}
		out.whole = &whole
	default:
		out.text = &text
	}
	return out, nil
}

func wholeNumber(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	if digits == "" {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *flow) quoted(quote byte) (*node, error) {
	p.at++
	var b strings.Builder
	for p.at < len(p.text) {
		c := p.text[p.at]
		p.at++
		switch {
		case quote == '"' && c == '\\':
			if p.at == len(p.text) || (p.text[p.at] != '"' && p.text[p.at] != '\\') {
				return nil, refuse(p.line, `in double quotes a backslash goes before " or \ only`)
			}
			b.WriteByte(p.text[p.at])
			p.at++
		case quote == '\'' && c == '\'' && p.at < len(p.text) && p.text[p.at] == '\'':
			b.WriteByte('\'')
			p.at++
		case c == quote:
			text := b.String()
			return &node{line: p.line, text: &text}, nil
		default:
			b.WriteByte(c)
		}
	}
	return nil, refuse(p.line, "a quote is not closed")
}

func (p *flow) mapping() (*node, error) {
	p.at++
	out := &node{line: p.line, items: map[string]*node{}}
	for {
		p.skip()
		if p.at < len(p.text) && p.text[p.at] == '}' && len(out.keys) == 0 {
			p.at++
			return out, nil
		}
		start := p.at
		for p.at < len(p.text) && p.text[p.at] != ':' && p.text[p.at] != ',' && p.text[p.at] != '}' {
			p.at++
		}
		key := strings.TrimRight(p.text[start:p.at], " ")
		// A colon ends a key only with a space after it: "kind:demo" is one word, and no key and value.
		if p.at >= len(p.text)-1 || p.text[p.at] != ':' || p.text[p.at+1] != ' ' || !plainKey(key) {
			return nil, refuse(p.line, "inside { } expected key: value, with a space after the colon")
		}
		if _, twice := out.items[key]; twice {
			return nil, refuse(p.line, "a key is given twice")
		}
		p.at++
		value, err := p.value(true)
		if err != nil {
			return nil, err
		}
		out.keys = append(out.keys, key)
		out.items[key] = value
		p.skip()
		switch {
		case p.at < len(p.text) && p.text[p.at] == ',':
			p.at++
		case p.at < len(p.text) && p.text[p.at] == '}':
			p.at++
			return out, nil
		default:
			return nil, refuse(p.line, "a { is not closed")
		}
	}
}

func (p *flow) sequence() (*node, error) {
	p.at++
	out := &node{line: p.line, list: []*node{}}
	for {
		p.skip()
		if p.at < len(p.text) && p.text[p.at] == ']' && len(out.list) == 0 {
			p.at++
			return out, nil
		}
		value, err := p.value(true)
		if err != nil {
			return nil, err
		}
		out.list = append(out.list, value)
		p.skip()
		switch {
		case p.at < len(p.text) && p.text[p.at] == ',':
			p.at++
		case p.at < len(p.text) && p.text[p.at] == ']':
			p.at++
			return out, nil
		default:
			return nil, refuse(p.line, "a [ is not closed")
		}
	}
}
