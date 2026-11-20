package strictjson

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// pyDumps is json.dumps(value, ensure_ascii=True) with the default separators, enough to compare a
// parsed value with the oracle's record of it.
func pyDumps(value any) string {
	var b strings.Builder
	var write func(any)
	write = func(v any) {
		switch v := v.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case *big.Int:
			b.WriteString(v.String())
		case float64:
			b.WriteString(pyFloatRepr(v))
		case Decimal:
			b.WriteString(`{"decimal": ` + asciiString(v.String()) + "}")
		case string:
			b.WriteString(asciiString(v))
		case []any:
			b.WriteString("[")
			for i, item := range v {
				if i > 0 {
					b.WriteString(", ")
				}
				write(item)
			}
			b.WriteString("]")
		case *Object:
			b.WriteString("{")
			for i, key := range v.Keys {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(asciiString(key))
				b.WriteString(": ")
				write(v.Values[i])
			}
			b.WriteString("}")
		default:
			panic(fmt.Sprintf("unexpected %T", v))
		}
	}
	write(value)
	return b.String()
}

func asciiString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case c >= ' ' && c <= '~':
				b.WriteRune(c)
			case c > 0xFFFF:
				c -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(c>>10), 0xDC00+(c&0x3FF))
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pyFloatRepr is repr(float): the shortest digits, in exponent form when the point would sit more than 16
// places right or 4 places left of the first digit.
func pyFloatRepr(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		panic("non-finite")
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mantissa, exponent, _ := strings.Cut(e, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	exp, _ := strconv.Atoi(exponent)
	point := exp + 1
	if point <= -4 || point > 16 {
		body := digits[:1]
		if len(digits) > 1 {
			body += "." + digits[1:]
		}
		expSign := "+"
		if exp < 0 {
			expSign, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, body, expSign, exp)
	}
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}

type outcome struct {
	OK      *string `json:"ok"`
	Error   string  `json:"error"`
	Message string  `json:"message"`
}

func TestOracle(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Kind string `json:"kind"`
			Hex  string `json:"hex"`
			outcome
			Decimal outcome `json:"decimal"`
		} `json:"cases"`
	}
	oracletest.JSON(t, "library/strict_json.json", &fixture)
	floats := oracletest.NewMismatches(t, "strict_json")
	decimals := oracletest.NewMismatches(t, "strict_json with parse_float=Decimal")
	for _, c := range fixture.Cases {
		raw, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatal(err)
		}
		label := c.Hex
		if len(label) > 40 {
			label = label[:40] + "..."
		}
		for _, mode := range []struct {
			decimal bool
			want    outcome
			m       *oracletest.Mismatches
		}{{false, c.outcome, floats}, {true, c.Decimal, decimals}} {
			var value any
			if c.Kind == "str" {
				value, err = loadsText(string(raw), mode.decimal)
			} else {
				value, err = loads(raw, mode.decimal)
			}
			mode.m.Row()
			compare(mode.m, c.Kind+" "+label, mode.want, value, err)
		}
	}
}

func compare(m *oracletest.Mismatches, label string, c outcome, value any, err error) {
	switch {
	case c.OK != nil && err != nil:
		m.Add("%s: got %v, want %.200s", label, err, *c.OK)
	case c.OK != nil:
		if got := pyDumps(value); got != *c.OK {
			m.Add("%s: got %.200s, want %.200s", label, got, *c.OK)
		}
	case err == nil:
		m.Add("%s: got %.200s, want %s: %s", label, pyDumps(value), c.Error, c.Message)
	default:
		refusal, ok := err.(*Error)
		if !ok || refusal.Type != c.Error || refusal.Message != c.Message {
			m.Add("%s: got %v, want %s: %s", label, err, c.Error, c.Message)
		}
	}
}

// decimal.json: format(Decimal(text), "f"), and the amount the Salesforce adapter keeps from a JSON number
// read with parse_float=Decimal: at most 28 digits and an exponent within 28 either way.
func TestDecimal(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Input            string              `json:"input"`
			FormatF          struct{ OK string } `json:"format_f"`
			SalesforceAmount struct {
				OK json.RawMessage `json:"ok"`
			} `json:"salesforce_amount"`
		} `json:"cases"`
	}
	oracletest.JSON(t, "library/decimal.json", &fixture)
	m := oracletest.NewMismatches(t, "Decimal format and Salesforce amounts")
	for _, c := range fixture.Cases {
		m.Row()
		d, err := ParseDecimal(c.Input)
		if err != nil || d.Fixed() != c.FormatF.OK {
			m.Add("format(Decimal(%q), 'f'): got %q %v, want %q", c.Input, d.Fixed(), err, c.FormatF.OK)
		}
		row, err := LoadsDecimal([]byte(`{"Amount": ` + c.Input + `}`))
		if err != nil {
			t.Fatalf("%s: %v", c.Input, err)
		}
		var amount Decimal
		switch v := row.(*Object).Values[0].(type) {
		case Decimal:
			amount = v
		case *big.Int:
			amount = Decimal{Negative: v.Sign() < 0, Coefficient: new(big.Int).Abs(v).String()}
		}
		got := `{"refused":"salesforce_invalid_response"}`
		if len(amount.Coefficient) <= 28 && amount.Exponent >= -28 && amount.Exponent <= 28 {
			got = strconv.Quote(amount.Fixed())
		}
		var want bytes.Buffer
		if err := json.Compact(&want, c.SalesforceAmount.OK); err != nil {
			t.Fatal(err)
		}
		if got != want.String() {
			m.Add("Salesforce amount %q: got %s, want %s", c.Input, got, c.SalesforceAmount.OK)
		}
	}
}

// These follow CPython 3.14.6's json and codecs where the oracle records no case. Each expected text was
// read from that interpreter.
func TestPythonBehaviour(t *testing.T) {
	for document, want := range map[string]string{
		`"abc`:                               "JSONDecodeError: Unterminated string starting at: line 1 column 1 (char 0)",
		`"a\u12`:                             `JSONDecodeError: Invalid \uXXXX escape: line 1 column 4 (char 3)`,
		`"a\u12zz"`:                          `JSONDecodeError: Invalid \uXXXX escape: line 1 column 4 (char 3)`,
		`"a\q"`:                              `JSONDecodeError: Invalid \escape: line 1 column 3 (char 2)`,
		`{"a" 1}`:                            "JSONDecodeError: Expecting ':' delimiter: line 1 column 6 (char 5)",
		`[1 2]`:                              "JSONDecodeError: Expecting ',' delimiter: line 1 column 4 (char 3)",
		`{"a":1,`:                            "JSONDecodeError: Expecting property name enclosed in double quotes: line 1 column 8 (char 7)",
		`1.e5`:                               "JSONDecodeError: Extra data: line 1 column 2 (char 1)",
		`1e+`:                                "JSONDecodeError: Extra data: line 1 column 2 (char 1)",
		`[01]`:                               "JSONDecodeError: Expecting ',' delimiter: line 1 column 3 (char 2)",
		"\n\n  x":                            "JSONDecodeError: Expecting value: line 3 column 3 (char 4)",
		"[1,\n 2,]":                          "JSONDecodeError: Illegal trailing comma before end of array: line 2 column 3 (char 6)",
		"{\"a\":1,\n\"b\"}":                  "JSONDecodeError: Expecting ':' delimiter: line 2 column 4 (char 11)",
		"[\"\xc3\xa9\", x]":                  "JSONDecodeError: Expecting value: line 1 column 7 (char 6)",
		"\xe2\x82":                           "UnicodeDecodeError: 'utf-8' codec can't decode bytes in position 0-1: unexpected end of data",
		"\xf0\x9f(":                          "UnicodeDecodeError: 'utf-8' codec can't decode bytes in position 0-1: invalid continuation byte",
		"\xc3":                               "UnicodeDecodeError: 'utf-8' codec can't decode byte 0xc3 in position 0: unexpected end of data",
		"\xed\xa0":                           "UnicodeDecodeError: 'utf-8' codec can't decode byte 0xed in position 0: invalid continuation byte",
		"\xef\xbb\xbf\xff":                   "UnicodeDecodeError: 'utf-8' codec can't decode byte 0xff in position 0: invalid start byte",
		`["\ud800\ud800"]`:                   "UnicodeEncodeError: 'utf-8' codec can't encode characters in position 0-1: surrogates not allowed",
		`{"\ud800":1,"\ud800":2}`:            "ValueError: duplicate JSON key",
		`["\ud800", NaN]`:                    "ValueError: non-finite JSON number",
		`["\ud800", "\udc01"]`:               "UnicodeEncodeError: 'utf-8' codec can't encode character '\\udc01' in position 0: surrogates not allowed",
		`{"a":"\ud801","\ud802":"b"}`:        "UnicodeEncodeError: 'utf-8' codec can't encode character '\\ud801' in position 0: surrogates not allowed",
		`[["\ud801"],"\ud802"]`:              "UnicodeEncodeError: 'utf-8' codec can't encode character '\\ud802' in position 0: surrogates not allowed",
		`"\ud800\u0041"`:                     "UnicodeEncodeError: 'utf-8' codec can't encode character '\\ud800' in position 0: surrogates not allowed",
		`-Inf`:                               "JSONDecodeError: Expecting value: line 1 column 1 (char 0)",
		`NaNx`:                               "ValueError: non-finite JSON number",
		"\x0c1":                              "JSONDecodeError: Expecting value: line 1 column 1 (char 0)",
		"-" + strings.Repeat("1", 4301):      "ValueError: Exceeds the limit (4300 digits) for integer string conversion: value has 4301 digits; use sys.set_int_max_str_digits() to increase the limit",
		strings.Repeat("1", 4301) + ".0":     "ValueError: non-finite JSON number",
		`[1e-400, -0, -0.0, "\ud800\udc00"]`: `[0.0, 0, -0.0, "\ud800\udc00"]`,
		`" ` + "\x7f" + `"`:                  `" \u007f"`,
		"\t1\r\n":                            "1",
	} {
		value, err := Loads([]byte(document))
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = pyDumps(value)
		}
		if got != want {
			t.Errorf("%q:\n got %s\nwant %s", document, got, want)
		}
	}
}

// Python's main thread parses 116,207 levels. Go parses as deep without growing its stack.
func TestDeepNesting(t *testing.T) {
	depth := 116_207
	value, err := Loads([]byte(strings.Repeat("[", depth) + strings.Repeat("]", depth)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < depth-1; i++ {
		value = value.([]any)[0]
	}
	if len(value.([]any)) != 0 {
		t.Fatal("innermost array is not empty")
	}
}
