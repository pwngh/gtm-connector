// Package strictjson parses JSON as gtm_connector/strict_json.py does: it refuses duplicate keys,
// non-finite numbers and lone surrogates, and gives each refusal the type and message Python gives.
// Nesting past MaxDepth is refused with ErrTooDeep, whose message is its own.
//
// A document is read from its bytes, with no decoded copy of it. Before anything is built, one pass over
// the bytes checks the encoding and a second counts the nesting, so a document that is not UTF-8 or is too
// deep is refused for the cost of reading it. A reader sets no limit on a document's size: the caller
// checks that before it loads the bytes.
//
// Refusals come in one order. A document that is not UTF-8 (or, as text, holds a lone surrogate) is refused
// for that, whatever else it holds. A well-encoded document nested past MaxDepth is refused as too deep,
// whatever other fault it has, before or after that point: nothing else about it is looked at. Every other
// fault is the parser's, in the order of the document, and an escaped lone surrogate is refused last.
//
// Values are nil, bool, string, *big.Int, []any, *Object, and for a number with a fraction or an exponent
// either float64 (Loads and LoadsText, like Python's default) or Decimal (LoadsDecimal, like
// parse_float=Decimal).
package strictjson

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

// Object is a JSON object with its members in document order.
type Object struct {
	Keys   []string
	Values []any
}

// Error is a refusal, named by the Python exception type that strict_json raises for it.
type Error struct {
	// ValueError, JSONDecodeError, UnicodeDecodeError, UnicodeEncodeError, InvalidOperation or RecursionError
	Type    string
	Message string
}

func (e *Error) Error() string { return e.Type + ": " + e.Message }

// maxIntDigits is CPython's default limit for converting a decimal string to an int.
const maxIntDigits = 4300

// MaxDepth is how many arrays and objects, counted together, a document may have open at once.
// Python's own limit is the room left on its thread's stack, so it is not one number: CPython 3.14.6
// first refuses at 116,203 levels on the main thread and at 1,646 in a thread with a 256 KiB stack.
// MaxDepth is the deepest document the oracle records Python reading (library/strict_json.json), which
// is below both.
const MaxDepth = 1000

// ErrTooDeep is the refusal of a document nested past MaxDepth, where Python raises RecursionError.
// Every reader returns this value itself, so errors.Is finds it.
var ErrTooDeep = &Error{"RecursionError", fmt.Sprintf("JSON is nested deeper than %d levels", MaxDepth)}

// Loads parses one UTF-8 document, after one optional byte-order mark, as strict_json.loads(bytes) does.
func Loads(document []byte) (any, error) { return loads(document, false) }

// LoadsDecimal is Loads with parse_float=Decimal, as the Salesforce adapter reads amounts: a number with a
// fraction or an exponent keeps its exact digits and scale, and is never refused as non-finite.
func LoadsDecimal(document []byte) (any, error) { return loads(document, true) }

func loads(document []byte, decimal bool) (any, error) {
	data := document
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	if err := checkUTF8(data, false); err != nil {
		return nil, err
	}
	if tooDeep(data) {
		return nil, ErrTooDeep
	}
	return parse(data, decimal)
}

// LoadsText parses text that is already decoded, as strict_json.loads(str) does: a byte-order mark is
// refused, and a lone surrogate written in the text (as WTF-8) cannot be encoded. Bytes that are neither
// UTF-8 nor a WTF-8 surrogate are refused as Loads refuses them.
func LoadsText(text string) (any, error) { return loadsText(text, false) }

func loadsText(text string, decimal bool) (any, error) {
	data := []byte(text)
	if err := checkUTF8(data, true); err != nil {
		return nil, err
	}
	if err := encodable(data); err != nil {
		return nil, err
	}
	if tooDeep(data) {
		return nil, ErrTooDeep
	}
	return parse(data, decimal)
}

// tooDeep reports whether a document opens more than MaxDepth arrays and objects at once, counting the
// brackets outside strings. It reads bytes that are already known to be well encoded and allocates nothing,
// so a document of any size that is too deep is refused before it is built. Up to the parser's first
// refusal it counts exactly as the parser does, and a document that is too deep past another fault of its
// syntax is refused as too deep. A closing bracket with nothing open is a fault the parser reports, so the
// count stops there.
func tooDeep(text []byte) bool {
	depth, inString := 0, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			if depth++; depth > MaxDepth {
				return true
			}
		case ']', '}':
			if depth == 0 {
				return false
			}
			depth--
		}
	}
	return false
}

// parse reads text that is UTF-8.
func parse(text []byte, decimal bool) (any, error) {
	if bytes.HasPrefix(text, []byte("\xEF\xBB\xBF")) {
		return nil, decodeError(text, "Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	p := &parser{text: text, decimal: decimal}
	value, err := p.document()
	if err != nil {
		return nil, err
	}
	if p.lone {
		if err := checkStrings(value); err != nil {
			return nil, err
		}
	}
	return finish(value, false), nil
}

// checkUTF8 is bytes.decode("utf-8") with CPython's error positions and reasons, keeping nothing. With
// surrogates it is the surrogatepass decoder, which also reads a lone surrogate written as three bytes.
func checkUTF8(data []byte, surrogates bool) error {
	for i := 0; i < len(data); {
		if data[i] < 0x80 {
			i++
			continue
		}
		if surrogates && surrogateAt(data, i) {
			i += 3
			continue
		}
		width, valid, reason := utf8Sequence(data[i:])
		if reason != "" {
			return utf8Error(data, i, i+valid, reason)
		}
		i += width
	}
	return nil
}

// surrogateAt is whether the three bytes of a lone surrogate start at i.
func surrogateAt(data []byte, i int) bool {
	return i+2 < len(data) && data[i] == 0xED && data[i+1] >= 0xA0 && data[i+1] < 0xC0 &&
		data[i+2] >= 0x80 && data[i+2] < 0xC0
}

// characters counts the characters of UTF-8 or WTF-8 text: every byte that does not continue a sequence.
func characters(text []byte) int {
	n := 0
	for _, c := range text {
		if c&0xC0 != 0x80 {
			n++
		}
	}
	return n
}

// utf8Sequence reads one multi-byte sequence as CPython's decoder does. It returns the sequence's width,
// or the number of bytes in error and why.
func utf8Sequence(s []byte) (width, bad int, reason string) {
	c := s[0]
	continuation := func(b byte) bool { return b >= 0x80 && b < 0xC0 }
	switch {
	case c < 0xC2 || c > 0xF4:
		return 0, 1, "invalid start byte"
	case c < 0xE0:
		if len(s) < 2 {
			return 0, 1, "unexpected end of data"
		}
		if !continuation(s[1]) {
			return 0, 1, "invalid continuation byte"
		}
		return 2, 0, ""
	case c < 0xF0:
		if len(s) < 2 {
			return 0, len(s), "unexpected end of data"
		}
		second := s[1]
		if !continuation(second) || c == 0xE0 && second < 0xA0 || c == 0xED && second >= 0xA0 {
			return 0, 1, "invalid continuation byte"
		}
		if len(s) < 3 {
			return 0, len(s), "unexpected end of data"
		}
		if !continuation(s[2]) {
			return 0, 2, "invalid continuation byte"
		}
		return 3, 0, ""
	default:
		if len(s) < 2 {
			return 0, len(s), "unexpected end of data"
		}
		second := s[1]
		if !continuation(second) || c == 0xF0 && second < 0x90 || c == 0xF4 && second >= 0x90 {
			return 0, 1, "invalid continuation byte"
		}
		if len(s) < 3 {
			return 0, len(s), "unexpected end of data"
		}
		if !continuation(s[2]) {
			return 0, 2, "invalid continuation byte"
		}
		if len(s) < 4 {
			return 0, len(s), "unexpected end of data"
		}
		if !continuation(s[3]) {
			return 0, 3, "invalid continuation byte"
		}
		return 4, 0, ""
	}
}

func utf8Error(data []byte, start, end int, reason string) error {
	if end-start == 1 {
		return &Error{"UnicodeDecodeError", fmt.Sprintf(
			"'utf-8' codec can't decode byte 0x%02x in position %d: %s", data[start], start, reason)}
	}
	return &Error{"UnicodeDecodeError", fmt.Sprintf(
		"'utf-8' codec can't decode bytes in position %d-%d: %s", start, end-1, reason)}
}

func surrogate(c rune) bool { return c >= 0xD800 && c <= 0xDFFF }

// encodable is str.encode("utf-8") of text held as WTF-8: the first run of surrogates is refused, at its
// position in characters.
func encodable(s []byte) error {
	for i := 0; i < len(s); i++ {
		if !surrogateAt(s, i) {
			continue
		}
		start, run := characters(s[:i]), 1
		for surrogateAt(s, i+3*run) {
			run++
		}
		if run == 1 {
			c := 0xD000 | rune(s[i+1]&0x3F)<<6 | rune(s[i+2]&0x3F)
			return &Error{"UnicodeEncodeError", fmt.Sprintf(
				"'utf-8' codec can't encode character '\\u%04x' in position %d: surrogates not allowed", c, start)}
		}
		return &Error{"UnicodeEncodeError", fmt.Sprintf(
			"'utf-8' codec can't encode characters in position %d-%d: surrogates not allowed", start, start+run-1)}
	}
	return nil
}

// decodeError is a JSONDecodeError at a byte of the text, reported as Python reports it: by line, column
// and character.
func decodeError(text []byte, message string, pos int) error {
	before := text[:min(pos, len(text))]
	char := characters(before)
	line, column := 1+bytes.Count(before, []byte("\n")), char+1
	if newline := bytes.LastIndexByte(before, '\n'); newline >= 0 {
		column = characters(before[newline+1:]) + 1
	}
	return &Error{"JSONDecodeError", fmt.Sprintf("%s: line %d column %d (char %d)", message, line, column, char)}
}

// Inside the parser, a string is kept as WTF-8 so that a lone surrogate survives until the final check,
// and an object keeps its keys in that form until its duplicates are checked.
type rawString string

type rawObject struct {
	keys   []rawString
	values []any
}

type parser struct {
	text    []byte
	lone    bool // a lone surrogate was decoded from an escape
	decimal bool // parse_float=Decimal
	loose   bool // json.loads itself: non-finite numbers, and the last of duplicate keys wins
}

// expectingValue is Python's StopIteration from scan_once, which raw_decode reports as "Expecting value".
type expectingValue struct{ pos int }

func (e expectingValue) Error() string { return "expecting value" }

func whitespace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (p *parser) skip(i int) int {
	for i < len(p.text) && whitespace(p.text[i]) {
		i++
	}
	return i
}

func (p *parser) document() (any, error) {
	value, end, err := p.value(p.skip(0))
	if err != nil {
		if stop, ok := err.(expectingValue); ok {
			return nil, decodeError(p.text, "Expecting value", stop.pos)
		}
		return nil, err
	}
	if end = p.skip(end); end != len(p.text) {
		return nil, decodeError(p.text, "Extra data", end)
	}
	return value, nil
}

// frame is one open array or object. The parser keeps them on its own stack, which holds at most
// MaxDepth frames, so deep nesting cannot overflow the Go stack or grow the heap.
type frame struct {
	array   []any
	object  *rawObject
	key     rawString
	isArray bool
}

// value is scan_once from Modules/_json.c, with arrays and objects parsed without recursion.
func (p *parser) value(idx int) (any, int, error) {
	var stack []*frame
	for {
		var result any
		var end int
		var err error
		// Open containers until a scalar or an empty container is read.
		if idx >= len(p.text) {
			return nil, 0, expectingValue{idx}
		}
		// An array or object that opens here is one level inside every open frame. tooDeep has refused a
		// document this deep already, and the parser holds the limit itself all the same.
		if (p.text[idx] == '[' || p.text[idx] == '{') && len(stack) >= MaxDepth {
			return nil, 0, ErrTooDeep
		}
		switch p.text[idx] {
		case '[':
			i := p.skip(idx + 1)
			if i < len(p.text) && p.text[i] == ']' {
				result, end = []any{}, i+1
				break
			}
			stack = append(stack, &frame{isArray: true, array: []any{}})
			idx = i
			continue
		case '{':
			i := p.skip(idx + 1)
			if i < len(p.text) && p.text[i] == '}' {
				result, end = &rawObject{}, i+1
				break
			}
			key, next, err := p.key(i)
			if err != nil {
				return nil, 0, err
			}
			stack = append(stack, &frame{object: &rawObject{}, key: key})
			idx = next
			continue
		default:
			result, end, err = p.scalar(idx)
			if err != nil {
				return nil, 0, err
			}
		}
		// Close containers while the value just read completes them.
		for {
			if len(stack) == 0 {
				return result, end, nil
			}
			top := stack[len(stack)-1]
			if top.isArray {
				top.array = append(top.array, result)
			} else {
				top.object.keys = append(top.object.keys, top.key)
				top.object.values = append(top.object.values, result)
			}
			i := p.skip(end)
			closer := byte('}')
			if top.isArray {
				closer = ']'
			}
			if i < len(p.text) && p.text[i] == closer {
				stack = stack[:len(stack)-1]
				end = i + 1
				if top.isArray {
					result = top.array
				} else if result, err = p.closeObject(top.object); err != nil {
					return nil, 0, err
				}
				continue
			}
			if i >= len(p.text) || p.text[i] != ',' {
				return nil, 0, decodeError(p.text, "Expecting ',' delimiter", i)
			}
			comma := i
			i = p.skip(i + 1)
			if i < len(p.text) && p.text[i] == closer {
				what := "object"
				if top.isArray {
					what = "array"
				}
				return nil, 0, decodeError(p.text, "Illegal trailing comma before end of "+what, comma)
			}
			if top.isArray {
				idx = i
			} else {
				key, next, err := p.key(i)
				if err != nil {
					return nil, 0, err
				}
				top.key = key
				idx = next
			}
			break
		}
	}
}

// key reads a member's name and its ':' delimiter, and returns where its value starts.
func (p *parser) key(i int) (rawString, int, error) {
	if i >= len(p.text) || p.text[i] != '"' {
		return "", 0, decodeError(p.text, "Expecting property name enclosed in double quotes", i)
	}
	key, i, err := p.str(i + 1)
	if err != nil {
		return "", 0, err
	}
	i = p.skip(i)
	if i >= len(p.text) || p.text[i] != ':' {
		return "", 0, decodeError(p.text, "Expecting ':' delimiter", i)
	}
	return key, p.skip(i + 1), nil
}

// closeObject is strict_json._unique, which json calls when an object ends.
func (p *parser) closeObject(o *rawObject) (any, error) {
	if p.loose {
		return lastWins(o), nil
	}
	seen := make(map[rawString]bool, len(o.keys))
	for _, key := range o.keys {
		if seen[key] {
			return nil, &Error{"ValueError", "duplicate JSON key"}
		}
		seen[key] = true
	}
	return o, nil
}

func (p *parser) scalar(idx int) (any, int, error) {
	text := p.text
	has := func(word string) bool { return bytes.HasPrefix(text[idx:], []byte(word)) }
	switch text[idx] {
	case '"':
		value, end, err := p.str(idx + 1)
		if err != nil {
			return nil, 0, err
		}
		return value, end, nil
	case 'n':
		if has("null") {
			return nil, idx + 4, nil
		}
	case 't':
		if has("true") {
			return true, idx + 4, nil
		}
	case 'f':
		if has("false") {
			return false, idx + 5, nil
		}
	case 'N':
		if has("NaN") {
			if p.loose {
				return math.NaN(), idx + 3, nil
			}
			return nil, 0, &Error{"ValueError", "non-finite JSON number"}
		}
	case 'I':
		if has("Infinity") {
			if p.loose {
				return math.Inf(1), idx + 8, nil
			}
			return nil, 0, &Error{"ValueError", "non-finite JSON number"}
		}
	case '-':
		if has("-Infinity") {
			if p.loose {
				return math.Inf(-1), idx + 9, nil
			}
			return nil, 0, &Error{"ValueError", "non-finite JSON number"}
		}
	}
	return p.number(idx)
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

// number is _match_number_unicode, with strict_json's float rule and CPython's int digit limit.
func (p *parser) number(start int) (any, int, error) {
	text, last := p.text, len(p.text)-1
	idx := start
	if text[idx] == '-' {
		idx++
		if idx > last {
			return nil, 0, expectingValue{start}
		}
	}
	switch {
	case text[idx] >= '1' && text[idx] <= '9':
		idx++
		for idx <= last && digit(text[idx]) {
			idx++
		}
	case text[idx] == '0':
		idx++
	default:
		return nil, 0, expectingValue{start}
	}
	float := false
	if idx < last && text[idx] == '.' && digit(text[idx+1]) {
		float = true
		idx += 2
		for idx <= last && digit(text[idx]) {
			idx++
		}
	}
	if idx < last && (text[idx] == 'e' || text[idx] == 'E') {
		eStart := idx
		idx++
		if idx < last && (text[idx] == '-' || text[idx] == '+') {
			idx++
		}
		for idx <= last && digit(text[idx]) {
			idx++
		}
		if digit(text[idx-1]) {
			float = true
		} else {
			idx = eStart
		}
	}
	literal := string(text[start:idx])
	if float && p.decimal {
		value, err := ParseDecimal(literal)
		if err != nil {
			return nil, 0, err
		}
		return value, idx, nil
	}
	if float {
		value := parseFloat(literal)
		if math.IsInf(value, 0) && !p.loose {
			return nil, 0, &Error{"ValueError", "non-finite JSON number"}
		}
		return value, idx, nil
	}
	if digits := len(strings.TrimPrefix(literal, "-")); digits > maxIntDigits {
		return nil, 0, &Error{"ValueError", fmt.Sprintf("Exceeds the limit (%d digits) for integer string "+
			"conversion: value has %d digits; use sys.set_int_max_str_digits() to increase the limit",
			maxIntDigits, digits)}
	}
	value, _ := new(big.Int).SetString(literal, 10)
	return value, idx, nil
}

func hexDigit(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

// str is scanstring_unicode from Modules/_json.c in strict mode. begin is just past the opening quote.
// Every byte it looks for is ASCII, which no byte of a longer sequence equals, so it reads bytes where
// Python reads characters. Where fewer than four characters follow a \u, fewer than four bytes do, or a
// byte among the first four is no hex digit, so both refuse the escape, and at the same place.
func (p *parser) str(begin int) (rawString, int, error) {
	text := p.text
	var out []byte
	end := begin
	for {
		next := end
		var c byte
		for ; next < len(text); next++ {
			c = text[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return "", 0, decodeError(text, "Invalid control character at", next)
			}
		}
		if next >= len(text) {
			return "", 0, decodeError(text, "Unterminated string starting at", begin-1)
		}
		if c == '"' && out == nil {
			// A string with no escape is its own bytes.
			return rawString(text[end:next]), next + 1, nil
		}
		out = append(out, text[end:next]...)
		if c == '"' {
			return rawString(out), next + 1, nil
		}
		next++
		if next == len(text) {
			return "", 0, decodeError(text, "Unterminated string starting at", begin-1)
		}
		c = text[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			default:
				return "", 0, decodeError(text, "Invalid \\escape", end-2)
			}
			out = append(out, c)
			continue
		}
		next++
		end = next + 4
		if end >= len(text) {
			return "", 0, decodeError(text, "Invalid \\uXXXX escape", next-1)
		}
		var r rune
		for ; next < end; next++ {
			d, ok := hexDigit(text[next])
			if !ok {
				return "", 0, decodeError(text, "Invalid \\uXXXX escape", end-5)
			}
			r = r<<4 | d
		}
		if r >= 0xD800 && r <= 0xDBFF && end+6 < len(text) && text[next] == '\\' && text[next+1] == 'u' {
			next += 2
			end += 6
			var r2 rune
			for ; next < end; next++ {
				d, ok := hexDigit(text[next])
				if !ok {
					return "", 0, decodeError(text, "Invalid \\uXXXX escape", end-5)
				}
				r2 = r2<<4 | d
			}
			if r2 >= 0xDC00 && r2 <= 0xDFFF {
				r = 0x10000 + (r-0xD800)<<10 + (r2 - 0xDC00)
			} else {
				end -= 6
			}
		}
		if surrogate(r) {
			p.lone = true
		}
		out = appendWTF8(out, r)
	}
}

// appendWTF8 appends a character as UTF-8, and a surrogate as the three bytes surrogatepass writes.
func appendWTF8(out []byte, c rune) []byte {
	if surrogate(c) {
		return append(out, 0xED, byte(0x80|(c>>6)&0x3F), byte(0x80|c&0x3F))
	}
	return utf8.AppendRune(out, c)
}

// checkStrings is strict_json's walk over every string after a document with an escape: the first
// string, in the walk's order, that holds a lone surrogate is refused.
func checkStrings(value any) error {
	pending := []any{value}
	for len(pending) > 0 {
		v := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch v := v.(type) {
		case rawString:
			if err := encodable([]byte(v)); err != nil {
				return err
			}
		case *rawObject:
			for _, key := range v.keys {
				pending = append(pending, key)
			}
			pending = append(pending, v.values...)
		case []any:
			pending = append(pending, v...)
		}
	}
	return nil
}

// finish turns the parser's strings and objects into the package's values. It has no recursion either.
// Only LoadsLoose keeps lone surrogates, and there each becomes U+FFFD, which is as long in bytes.
func finish(value any, surrogates bool) any {
	text := func(s rawString) string {
		if !surrogates || !strings.Contains(string(s), "\xED") {
			return string(s)
		}
		b := []byte(s)
		for i := range b {
			if surrogateAt(b, i) {
				copy(b[i:], "�")
			}
		}
		return string(b)
	}
	type slot struct {
		value any
		set   func(any)
	}
	var root any
	pending := []slot{{value, func(v any) { root = v }}}
	for len(pending) > 0 {
		s := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch v := s.value.(type) {
		case rawString:
			s.set(text(v))
		case *rawObject:
			o := &Object{Keys: make([]string, len(v.keys)), Values: make([]any, len(v.values))}
			for i, key := range v.keys {
				o.Keys[i] = text(key)
				i := i
				pending = append(pending, slot{v.values[i], func(x any) { o.Values[i] = x }})
			}
			s.set(o)
		case []any:
			a := make([]any, len(v))
			for i := range v {
				i := i
				pending = append(pending, slot{v[i], func(x any) { a[i] = x }})
			}
			s.set(a)
		default:
			s.set(v)
		}
	}
	return root
}
