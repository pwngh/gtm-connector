package strictjson

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// DumpOptions are the json.dumps options the connector uses. The zero value is json.dumps(value) with
// allow_nan=False.
type DumpOptions struct {
	EnsureASCII bool // escape everything outside printable ASCII, as ensure_ascii=True does
	AllowNaN    bool // write NaN and Infinity instead of refusing them
	Indent      int  // indent=N when above 0: one member a line, and "," between members
	SortKeys    bool
	Compact     bool // separators=(",", ":")
}

// The variants the package writes.
var (
	// Quoted is json.dumps(text, ensure_ascii=False): a card's quotations and labels, and receipt columns.
	Quoted = DumpOptions{}
	// Artifact is a card file saved by brief or demo: json.dumps(artifact, ensure_ascii=False,
	// allow_nan=False, indent=2).
	Artifact = DumpOptions{Indent: 2}
	// Prepared is a brief prepare_renewal_brief saves: json.dumps(artifact, ensure_ascii=False,
	// allow_nan=False, sort_keys=True, separators=(",", ":")).
	Prepared = DumpOptions{SortKeys: true, Compact: true}
)

// Dumps is json.dumps under these options. A value is nil, bool, string, int, int64, *big.Int, float64,
// []any, []string or *Object. A non-finite float is refused with Python's ValueError unless AllowNaN is set.
func Dumps(value any, options DumpOptions) (string, error) {
	var b strings.Builder
	if err := options.write(&b, value, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Quote is json.dumps(text, ensure_ascii=False), which cannot fail.
func Quote(text string) string {
	var b strings.Builder
	writeString(&b, text, false)
	return b.String()
}

func (o DumpOptions) separators() (item, key string) {
	switch {
	case o.Compact:
		return ",", ":"
	case o.Indent > 0:
		return ",", ": "
	default:
		return ", ", ": "
	}
}

func (o DumpOptions) newline(b *strings.Builder, depth int) {
	if o.Indent > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", o.Indent*depth))
	}
}

func (o DumpOptions) write(b *strings.Builder, value any, depth int) error {
	item, key := o.separators()
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		writeString(b, v, o.EnsureASCII)
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case *big.Int:
		b.WriteString(v.String())
	case float64:
		text, err := floatText(v, o.AllowNaN)
		if err != nil {
			return err
		}
		b.WriteString(text)
	case []string:
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		return o.write(b, list, depth)
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, element := range v {
			if i > 0 {
				b.WriteString(item)
			}
			o.newline(b, depth+1)
			if err := o.write(b, element, depth+1); err != nil {
				return err
			}
		}
		o.newline(b, depth)
		b.WriteByte(']')
	case *Object:
		if v == nil || len(v.Keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		order := make([]int, len(v.Keys))
		for i := range order {
			order[i] = i
		}
		if o.SortKeys {
			sort.SliceStable(order, func(i, j int) bool { return v.Keys[order[i]] < v.Keys[order[j]] })
		}
		b.WriteByte('{')
		for n, i := range order {
			if n > 0 {
				b.WriteString(item)
			}
			o.newline(b, depth+1)
			writeString(b, v.Keys[i], o.EnsureASCII)
			b.WriteString(key)
			if err := o.write(b, v.Values[i], depth+1); err != nil {
				return err
			}
		}
		o.newline(b, depth)
		b.WriteByte('}')
	default:
		return fmt.Errorf("strictjson: Object of type %T is not JSON serializable", value)
	}
	return nil
}

// floatText is float.__repr__, or Python's words for a non-finite float when they are allowed.
func floatText(f float64, allowNaN bool) (string, error) {
	var word string
	switch {
	case math.IsNaN(f):
		word = "NaN"
	case math.IsInf(f, 1):
		word = "Infinity"
	case math.IsInf(f, -1):
		word = "-Infinity"
	default:
		return FloatRepr(f), nil
	}
	if !allowNaN {
		repr := map[string]string{"NaN": "nan", "Infinity": "inf", "-Infinity": "-inf"}[word]
		return "", &Error{"ValueError", "Out of range float values are not JSON compliant: " + repr}
	}
	return word, nil
}

// FloatRepr is repr(float) of a finite float: the shortest digits, in exponent form when the point would
// sit more than 16 places right or 4 places left of the first digit.
func FloatRepr(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		panic(errors.New("strictjson: FloatRepr of a non-finite float"))
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

// writeString is the json module's string encoder: with ensureASCII, every character outside printable
// ASCII as \uXXXX (a pair of surrogates above U+FFFF); without it, only quote, backslash and C0 controls.
func writeString(b *strings.Builder, s string, ensureASCII bool) {
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
			case c < ' ':
				fmt.Fprintf(b, `\u%04x`, c)
			case !ensureASCII || c <= '~':
				b.WriteRune(c)
			case c > 0xFFFF:
				c -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xD800+(c>>10), 0xDC00+(c&0x3FF))
			default:
				fmt.Fprintf(b, `\u%04x`, c)
			}
		}
	}
	b.WriteByte('"')
}
