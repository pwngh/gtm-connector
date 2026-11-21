package strictjson

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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
			b.WriteString(FloatRepr(v))
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

// asciiString is json.dumps(text) with ensure_ascii=True.
func asciiString(s string) string {
	var b strings.Builder
	writeString(&b, s, true)
	return b.String()
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

// nested is a document with this many arrays or objects open around a 1. The kind is "array", "object"
// or "mixed", which opens an array and an object in turn.
func nested(kind string, levels int) string {
	var open, shut strings.Builder
	for i := 0; i < levels; i++ {
		if kind == "array" || kind == "mixed" && i%2 == 0 {
			open.WriteString("[")
			shut.WriteString("]")
		} else {
			open.WriteString(`{"a":`)
			shut.WriteString("}")
		}
	}
	closers := []byte(shut.String())
	for i, j := 0, len(closers)-1; i < j; i, j = i+1, j-1 {
		closers[i], closers[j] = closers[j], closers[i]
	}
	return open.String() + "1" + string(closers)
}

// levelsOf counts the arrays and objects around the innermost value.
func levelsOf(value any) int {
	for levels := 0; ; levels++ {
		switch v := value.(type) {
		case []any:
			value = v[0]
		case *Object:
			value = v.Values[0]
		default:
			return levels
		}
	}
}

// Every reader reads a document nested MaxDepth levels and refuses one nested a level deeper, whether the
// levels are arrays, objects or both.
func TestNestingLimit(t *testing.T) {
	readers := map[string]func(string) (any, error){
		"Loads":        func(s string) (any, error) { return Loads([]byte(s)) },
		"LoadsDecimal": func(s string) (any, error) { return LoadsDecimal([]byte(s)) },
		"LoadsText":    LoadsText,
		"LoadsLoose":   func(s string) (any, error) { return LoadsLoose([]byte(s)) },
	}
	for name, read := range readers {
		for _, kind := range []string{"array", "object", "mixed"} {
			value, err := read(nested(kind, MaxDepth))
			if err != nil {
				t.Fatalf("%s, %s at the limit: %v", name, kind, err)
			}
			if got := levelsOf(value); got != MaxDepth {
				t.Fatalf("%s, %s at the limit: read %d levels", name, kind, got)
			}
			value, err = read(nested(kind, MaxDepth+1))
			var refusal *Error
			if value != nil || !errors.Is(err, ErrTooDeep) || !errors.As(err, &refusal) || refusal.Type != "RecursionError" {
				t.Fatalf("%s, %s one past the limit: %v, %v", name, kind, value, err)
			}
		}
	}
}

// An empty array or object counts as a level, a level that has closed does not, and a document that is
// too deep is refused as too deep whatever else is wrong with it, before or after that point. Brackets
// inside strings are not levels, and a closing bracket with nothing open is the parser's to refuse.
func TestNestingLimitCounts(t *testing.T) {
	open, shut := strings.Repeat("[", MaxDepth), strings.Repeat("]", MaxDepth)
	for document, tooDeep := range map[string]bool{
		open + shut:                           false,
		open[1:] + "{}" + shut[1:]:            false,
		"[" + open + shut + "]":               true,
		open + "{}" + shut:                    true,
		"[" + open:                            true,
		open + "{":                            true,
		"[" + open + "1" + shut:               true,
		"[" + open + "x":                      true,
		"x[" + open:                           true,
		"\xff[" + open:                        true,
		`["` + open + `[",` + open[1:] + shut: false,
		`["\"` + open + `",` + open[1:] + `[]` + shut: true,
		`["\\",` + open[1:] + `[]` + shut:             true,
		// Many levels, one after the other, at the limit.
		open[1:] + strings.Repeat("[1],{},", 2000) + "[]" + shut[1:]: false,
		"[" + strings.Repeat(open[1:]+shut[1:]+",", 10) + "1]":       false,
	} {
		_, err := Loads([]byte(document))
		if errors.Is(err, ErrTooDeep) != tooDeep || !tooDeep && err != nil {
			t.Errorf("%.20q (%d bytes): too deep is %t, got %v", document, len(document), tooDeep, err)
		}
	}
	_, err := Loads([]byte("][" + open))
	if want := "JSONDecodeError: Expecting value: line 1 column 1 (char 0)"; err == nil || err.Error() != want {
		t.Errorf("a closing bracket first: got %v, want %s", err, want)
	}
}

// measured runs read and gives the bytes it allocated.
func measured(read func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	read()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A document is refused for depth, or for bytes that are not UTF-8, before any of it is decoded or built:
// 16 MiB that end in either fault cost under 64 KiB, in every reader. A document that is read costs its
// values and no decoded copy of its text: one 16 MiB string costs under twice its bytes.
func TestALargeDocumentIsRefusedBeforeItIsDecoded(t *testing.T) {
	const size = 16 << 20
	numbers := bytes.Repeat([]byte("1,"), size/2)
	deep := append(append([]byte("["), numbers...), bytes.Repeat([]byte("["), MaxDepth)...)
	notUTF8 := append(append([]byte("["), numbers...), 0xFF)
	readers := map[string]func([]byte) (any, error){
		"Loads": Loads, "LoadsDecimal": LoadsDecimal, "LoadsLoose": LoadsLoose,
	}
	for name, read := range readers {
		for label, c := range map[string]struct {
			document []byte
			refusal  string
		}{"too deep": {deep, "RecursionError"}, "not UTF-8": {notUTF8, "UnicodeDecodeError"}} {
			var err error
			used := measured(func() { _, err = read(c.document) })
			var refusal *Error
			if !errors.As(err, &refusal) || refusal.Type != c.refusal {
				t.Fatalf("%s, %s: %v", name, label, err)
			}
			if used > 64<<10 {
				t.Errorf("%s, %s: allocated %d bytes before refusing %d", name, label, used, len(c.document))
			}
		}
	}
	// LoadsText counts the levels of its string where it is, and copies it only to read it.
	text := string(deep)
	var err error
	if used := measured(func() { _, err = LoadsText(text) }); !errors.Is(err, ErrTooDeep) || used > 64<<10 {
		t.Errorf("LoadsText, too deep: %v after %d bytes", err, used)
	}
	long := append(append([]byte(`"`), bytes.Repeat([]byte("a"), size)...), '"')
	for name, read := range readers {
		var value any
		used := measured(func() { value, err = read(long) })
		if text, _ := value.(string); err != nil || len(text) != size {
			t.Fatalf("%s: %v", name, err)
		}
		if used > 2*size {
			t.Errorf("%s: allocated %d bytes to read a string of %d", name, used, size)
		}
	}
}

// Ten million open arrays are refused at level MaxDepth+1, by the pass that counts levels: nothing is
// allocated for the document, whatever its size, and the stack does not grow.
func TestTenMillionLevelsAreRefused(t *testing.T) {
	document := bytes.Repeat([]byte("["), 10_000_000)
	for name, read := range map[string]func([]byte) (any, error){"Loads": Loads, "LoadsLoose": LoadsLoose} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		started := time.Now()
		_, err := read(document)
		elapsed := time.Since(started)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrTooDeep) {
			t.Fatalf("%s: %v", name, err)
		}
		objects := after.Mallocs - before.Mallocs
		bytesUsed := after.TotalAlloc - before.TotalAlloc
		stack := int64(after.StackInuse) - int64(before.StackInuse)
		t.Logf("%s: %v, %d allocations, %d bytes, stack %+d bytes", name, elapsed, objects, bytesUsed, stack)
		if objects > 8*MaxDepth {
			t.Errorf("%s: %d allocations for %d levels read", name, objects, MaxDepth)
		}
		if bytesUsed > 64<<10 {
			t.Errorf("%s: allocated %d bytes for a document refused by its depth", name, bytesUsed)
		}
		if stack > 1<<20 {
			t.Errorf("%s: the stack grew by %d bytes", name, stack)
		}
		if elapsed > 5*time.Second {
			t.Errorf("%s: took %v", name, elapsed)
		}
	}
}
