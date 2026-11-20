package strictjson

import (
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"unicode/utf16"
)

func encode16(s string, order binary.ByteOrder, mark bool) []byte {
	units := utf16.Encode([]rune(s))
	if mark {
		units = append([]uint16{0xFEFF}, units...)
	}
	out := make([]byte, 2*len(units))
	for i, u := range units {
		order.PutUint16(out[2*i:], u)
	}
	return out
}

func encode32(s string, order binary.ByteOrder, mark bool) []byte {
	runes := []rune(s)
	if mark {
		runes = append([]rune{0xFEFF}, runes...)
	}
	out := make([]byte, 4*len(runes))
	for i, r := range runes {
		order.PutUint32(out[4*i:], uint32(r))
	}
	return out
}

// Each expectation is what json.loads gives for the same bytes under CPython 3.14.6.
func TestLoadsLoose(t *testing.T) {
	call := `{"method":"tools/call"}`
	method := func(v any) any { o, _ := v.(*Object); return o.Values[0] }
	cases := []struct {
		label string
		input []byte
		check func(any) bool
	}{
		{"NaN", []byte(`{"a":NaN}`), func(v any) bool { return math.IsNaN(method(v).(float64)) }},
		{"infinities", []byte(`[Infinity,-Infinity,1e999]`), func(v any) bool {
			a := v.([]any)
			return math.IsInf(a[0].(float64), 1) && math.IsInf(a[1].(float64), -1) && math.IsInf(a[2].(float64), 1)
		}},
		{"the last duplicate wins, at the first position", []byte(`{"a":1,"b":2,"a":3}`), func(v any) bool {
			o := v.(*Object)
			return fmt.Sprint(o.Keys, o.Values) == "[a b] [3 2]"
		}},
		{"UTF-8 mark", append([]byte{0xEF, 0xBB, 0xBF}, call...), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-16LE", encode16(call, binary.LittleEndian, false), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-16BE", encode16(call, binary.BigEndian, false), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-16 with mark", encode16(call, binary.LittleEndian, true), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-16BE with mark", encode16(call, binary.BigEndian, true), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-32LE", encode32(call, binary.LittleEndian, false), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-32 with mark", encode32(call, binary.LittleEndian, true), func(v any) bool { return method(v) == "tools/call" }},
		{"UTF-32BE", encode32(call, binary.BigEndian, false), func(v any) bool { return method(v) == "tools/call" }},
		{"a lone surrogate escape", []byte(`{"a":"\ud800"}`), func(v any) bool { return v != nil }},
	}
	for _, c := range cases {
		value, err := LoadsLoose(c.input)
		if err != nil || !c.check(value) {
			t.Errorf("%s: %v %v", c.label, value, err)
		}
	}
	for _, refused := range [][]byte{encode16(`{"a":1}`, binary.LittleEndian, false)[:13], []byte("{\"a\":\"\x01\"}"), []byte(`{} x`)} {
		if value, err := LoadsLoose(refused); err == nil {
			t.Errorf("%q was read as %v", refused, value)
		}
	}
	// Loads itself is unchanged.
	if _, err := Loads([]byte(`{"a":NaN}`)); err == nil {
		t.Error("Loads read NaN")
	}
}
