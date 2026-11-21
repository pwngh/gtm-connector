// Package strictjson parses JSON as gtm_connector/strict_json.py does: it refuses duplicate keys,
// non-finite numbers and lone surrogates, and gives each refusal the type and message Python gives.
// Nesting past MaxDepth is refused with ErrTooDeep, whose message is its own.
//
// Values are nil, bool, string, *big.Int, []any, *Object, and for a number with a fraction or an exponent
// either float64 (Loads and LoadsText, like Python's default) or Decimal (LoadsDecimal, like
// parse_float=Decimal).
package strictjson

import (
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
	text, err := decodeUTF8(data)
	if err != nil {
		return nil, err
	}
	return parse(text, decimal)
}

// LoadsText parses text that is already decoded, as strict_json.loads(str) does: a byte-order mark is
// refused, and a lone surrogate written in the text (as WTF-8) cannot be encoded. Bytes that are neither
// UTF-8 nor a WTF-8 surrogate are refused as Loads refuses them.
func LoadsText(text string) (any, error) { return loadsText(text, false) }

func loadsText(text string, decimal bool) (any, error) {
	runes, err := decodeWTF8(text)
	if err != nil {
		return nil, err
	}
	if err := encodable(runes); err != nil {
		return nil, err
	}
	return parse(runes, decimal)
}

func parse(text []rune, decimal bool) (any, error) {
	if len(text) > 0 && text[0] == 0xFEFF {
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
	return finish(value), nil
}

// decodeUTF8 is bytes.decode("utf-8") with CPython's error positions and reasons.
func decodeUTF8(data []byte) ([]rune, error) {
	out := make([]rune, 0, len(data))
	for i := 0; i < len(data); {
		c := data[i]
		if c < 0x80 {
			out = append(out, rune(c))
			i++
			continue
		}
		width, valid, reason := utf8Sequence(data[i:])
		if reason != "" {
			return nil, utf8Error(data, i, i+valid, reason)
		}
		r, _ := utf8.DecodeRune(data[i : i+width])
		out = append(out, r)
		i += width
	}
	return out, nil
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

// decodeWTF8 reads text whose lone surrogates are written as three-byte sequences, as Python's
// surrogatepass writes them.
func decodeWTF8(text string) ([]rune, error) {
	data := []byte(text)
	out := make([]rune, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] == 0xED && i+2 < len(data) && data[i+1] >= 0xA0 && data[i+1] < 0xC0 &&
			data[i+2] >= 0x80 && data[i+2] < 0xC0 {
			out = append(out, 0xD000|rune(data[i+1]&0x3F)<<6|rune(data[i+2]&0x3F))
			i += 3
			continue
		}
		if data[i] < 0x80 {
			out = append(out, rune(data[i]))
			i++
			continue
		}
		width, valid, reason := utf8Sequence(data[i:])
		if reason != "" {
			return nil, utf8Error(data, i, i+valid, reason)
		}
		r, _ := utf8.DecodeRune(data[i : i+width])
		out = append(out, r)
		i += width
	}
	return out, nil
}

func surrogate(c rune) bool { return c >= 0xD800 && c <= 0xDFFF }

// encodable is str.encode("utf-8"): the first run of surrogates is refused.
func encodable(s []rune) error {
	for i, c := range s {
		if !surrogate(c) {
			continue
		}
		end := i + 1
		for end < len(s) && surrogate(s[end]) {
			end++
		}
		if end-i == 1 {
			return &Error{"UnicodeEncodeError", fmt.Sprintf(
				"'utf-8' codec can't encode character '\\u%04x' in position %d: surrogates not allowed", c, i)}
		}
		return &Error{"UnicodeEncodeError", fmt.Sprintf(
			"'utf-8' codec can't encode characters in position %d-%d: surrogates not allowed", i, end-1)}
	}
	return nil
}

func decodeError(text []rune, message string, pos int) error {
	line, column := 1, pos+1
	for i := 0; i < pos && i < len(text); i++ {
		if text[i] == '\n' {
			line++
			column = pos - i
		}
	}
	return &Error{"JSONDecodeError", fmt.Sprintf("%s: line %d column %d (char %d)", message, line, column, pos)}
}

// Inside the parser, a string is kept as runes so that a lone surrogate survives until the final check,
// and an object keeps its keys in that form until its duplicates are checked.
type rawString []rune

type rawObject struct {
	keys   []rawString
	values []any
}

type parser struct {
	text    []rune
	lone    bool // a lone surrogate was decoded from an escape
	decimal bool // parse_float=Decimal
	loose   bool // json.loads itself: non-finite numbers, and the last of duplicate keys wins
}

// expectingValue is Python's StopIteration from scan_once, which raw_decode reports as "Expecting value".
type expectingValue struct{ pos int }

func (e expectingValue) Error() string { return "expecting value" }

func whitespace(c rune) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

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
		// An array or object that opens here is one level inside every open frame.
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
			closer := rune('}')
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
		return nil, 0, decodeError(p.text, "Expecting property name enclosed in double quotes", i)
	}
	key, i, err := p.str(i + 1)
	if err != nil {
		return nil, 0, err
	}
	i = p.skip(i)
	if i >= len(p.text) || p.text[i] != ':' {
		return nil, 0, decodeError(p.text, "Expecting ':' delimiter", i)
	}
	return key, p.skip(i + 1), nil
}

// closeObject is strict_json._unique, which json calls when an object ends.
func (p *parser) closeObject(o *rawObject) (any, error) {
	if p.loose {
		return lastWins(o), nil
	}
	seen := make(map[string]bool, len(o.keys))
	for _, key := range o.keys {
		k := string(encodeWTF8(key))
		if seen[k] {
			return nil, &Error{"ValueError", "duplicate JSON key"}
		}
		seen[k] = true
	}
	return o, nil
}

func (p *parser) scalar(idx int) (any, int, error) {
	text := p.text
	has := func(word string) bool {
		if idx+len(word) > len(text) {
			return false
		}
		for i, c := range word {
			if text[idx+i] != c {
				return false
			}
		}
		return true
	}
	switch text[idx] {
	case '"':
		return p.str(idx + 1)
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

func digit(c rune) bool { return c >= '0' && c <= '9' }

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

func hexDigit(c rune) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// str is scanstring_unicode from Modules/_json.c in strict mode. begin is just past the opening quote.
func (p *parser) str(begin int) (rawString, int, error) {
	text := p.text
	var out []rune
	end := begin
	for {
		next := end
		var c rune
		for ; next < len(text); next++ {
			c = text[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return nil, 0, decodeError(text, "Invalid control character at", next)
			}
		}
		if next >= len(text) {
			return nil, 0, decodeError(text, "Unterminated string starting at", begin-1)
		}
		out = append(out, text[end:next]...)
		if c == '"' {
			return out, next + 1, nil
		}
		next++
		if next == len(text) {
			return nil, 0, decodeError(text, "Unterminated string starting at", begin-1)
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
				return nil, 0, decodeError(text, "Invalid \\escape", end-2)
			}
			out = append(out, c)
			continue
		}
		next++
		end = next + 4
		if end >= len(text) {
			return nil, 0, decodeError(text, "Invalid \\uXXXX escape", next-1)
		}
		c = 0
		for ; next < end; next++ {
			d, ok := hexDigit(text[next])
			if !ok {
				return nil, 0, decodeError(text, "Invalid \\uXXXX escape", end-5)
			}
			c = c<<4 | d
		}
		if c >= 0xD800 && c <= 0xDBFF && end+6 < len(text) && text[next] == '\\' && text[next+1] == 'u' {
			next += 2
			end += 6
			var c2 rune
			for ; next < end; next++ {
				d, ok := hexDigit(text[next])
				if !ok {
					return nil, 0, decodeError(text, "Invalid \\uXXXX escape", end-5)
				}
				c2 = c2<<4 | d
			}
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				c = 0x10000 + (c-0xD800)<<10 + (c2 - 0xDC00)
			} else {
				end -= 6
			}
		}
		if surrogate(c) {
			p.lone = true
		}
		out = append(out, c)
	}
}

func encodeWTF8(s []rune) []byte {
	out := make([]byte, 0, len(s))
	for _, c := range s {
		if surrogate(c) {
			out = append(out, 0xED, byte(0x80|(c>>6)&0x3F), byte(0x80|c&0x3F))
		} else {
			out = utf8.AppendRune(out, c)
		}
	}
	return out
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
			if err := encodable(v); err != nil {
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
func finish(value any) any {
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
			s.set(string(v))
		case *rawObject:
			o := &Object{Keys: make([]string, len(v.keys)), Values: make([]any, len(v.values))}
			for i, key := range v.keys {
				o.Keys[i] = string(key)
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
