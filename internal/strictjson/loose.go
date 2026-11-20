package strictjson

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// LoadsLoose parses one document as json.loads(bytes) does, which is what strict_json falls back to when it
// only needs to know what a refused body says. The encoding is detected as json.detect_encoding detects it
// (UTF-8 with or without a mark, UTF-16 and UTF-32 in either order) and decoded with surrogatepass; NaN,
// Infinity and -Infinity are numbers, a float too large is infinite, the last of duplicate keys wins, and a
// lone surrogate is kept. Values have Loads's types; a lone surrogate reads as U+FFFD in a Go string.
func LoadsLoose(document []byte) (any, error) {
	text, err := decodeDetected(document)
	if err != nil {
		return nil, err
	}
	p := &parser{text: text, loose: true}
	value, err := p.document()
	if err != nil {
		return nil, err
	}
	return finish(value), nil
}

// lastWins keeps each key once, at its first position, with the value written last, as a Python dict does.
func lastWins(o *rawObject) *rawObject {
	index := make(map[string]int, len(o.keys))
	out := &rawObject{}
	for i, key := range o.keys {
		k := string(encodeWTF8(key))
		if at, ok := index[k]; ok {
			out.values[at] = o.values[i]
			continue
		}
		index[k] = len(out.keys)
		out.keys = append(out.keys, key)
		out.values = append(out.values, o.values[i])
	}
	return out
}

// decodeDetected is b.decode(json.detect_encoding(b), "surrogatepass").
func decodeDetected(b []byte) ([]rune, error) {
	has := func(prefix ...byte) bool { return len(b) >= len(prefix) && string(b[:len(prefix)]) == string(prefix) }
	switch {
	case has(0, 0, 0xFE, 0xFF):
		return decodeUTF32(b[4:], binary.BigEndian)
	case has(0xFF, 0xFE, 0, 0):
		return decodeUTF32(b[4:], binary.LittleEndian)
	case has(0xFE, 0xFF):
		return decodeUTF16(b[2:], binary.BigEndian)
	case has(0xFF, 0xFE):
		return decodeUTF16(b[2:], binary.LittleEndian)
	case has(0xEF, 0xBB, 0xBF):
		return decodeWTF8(string(b[3:]))
	}
	if len(b) >= 4 {
		switch {
		case b[0] == 0 && b[1] != 0:
			return decodeUTF16(b, binary.BigEndian)
		case b[0] == 0:
			return decodeUTF32(b, binary.BigEndian)
		case b[1] == 0 && (b[2] != 0 || b[3] != 0):
			return decodeUTF16(b, binary.LittleEndian)
		case b[1] == 0:
			return decodeUTF32(b, binary.LittleEndian)
		}
	} else if len(b) == 2 {
		switch {
		case b[0] == 0:
			return decodeUTF16(b, binary.BigEndian)
		case b[1] == 0:
			return decodeUTF16(b, binary.LittleEndian)
		}
	}
	return decodeWTF8(string(b))
}

// decodeUTF16 pairs surrogates where they pair and passes lone ones through, as surrogatepass does.
func decodeUTF16(b []byte, order binary.ByteOrder) ([]rune, error) {
	if len(b)%2 != 0 {
		return nil, &Error{"UnicodeDecodeError", "'utf-16' codec can't decode byte: truncated data"}
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = order.Uint16(b[2*i:])
	}
	out := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := rune(units[i])
		if utf16.IsSurrogate(u) && u < 0xDC00 && i+1 < len(units) {
			if pair := utf16.DecodeRune(u, rune(units[i+1])); pair != 0xFFFD {
				out = append(out, pair)
				i++
				continue
			}
		}
		out = append(out, u)
	}
	return out, nil
}

func decodeUTF32(b []byte, order binary.ByteOrder) ([]rune, error) {
	if len(b)%4 != 0 {
		return nil, &Error{"UnicodeDecodeError", "'utf-32' codec can't decode bytes: truncated data"}
	}
	out := make([]rune, 0, len(b)/4)
	for i := 0; i < len(b); i += 4 {
		c := order.Uint32(b[i:])
		if c > 0x10FFFF {
			return nil, &Error{"UnicodeDecodeError", fmt.Sprintf("'utf-32' codec can't decode bytes in position %d-%d: code point not in range(0x110000)", i, i+3)}
		}
		out = append(out, rune(c))
	}
	return out, nil
}
