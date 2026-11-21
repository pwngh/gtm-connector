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
	if tooDeep(text) {
		return nil, ErrTooDeep
	}
	p := &parser{text: text, loose: true}
	value, err := p.document()
	if err != nil {
		return nil, err
	}
	return finish(value, true), nil
}

// lastWins keeps each key once, at its first position, with the value written last, as a Python dict does.
func lastWins(o *rawObject) *rawObject {
	index := make(map[rawString]int, len(o.keys))
	out := &rawObject{}
	for i, key := range o.keys {
		if at, ok := index[key]; ok {
			out.values[at] = o.values[i]
			continue
		}
		index[key] = len(out.keys)
		out.keys = append(out.keys, key)
		out.values = append(out.values, o.values[i])
	}
	return out
}

// decodeDetected is b.decode(json.detect_encoding(b), "surrogatepass"), as WTF-8. UTF-8 is checked and
// read where it is; UTF-16 and UTF-32 are written out again, at most half as long again.
func decodeDetected(b []byte) ([]byte, error) {
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
		return b[3:], checkUTF8(b[3:], true)
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
	return b, checkUTF8(b, true)
}

// decodeUTF16 pairs surrogates where they pair and passes lone ones through, as surrogatepass does.
func decodeUTF16(b []byte, order binary.ByteOrder) ([]byte, error) {
	if len(b)%2 != 0 {
		return nil, &Error{"UnicodeDecodeError", "'utf-16' codec can't decode byte: truncated data"}
	}
	out := make([]byte, 0, len(b)+len(b)/2)
	for i := 0; i < len(b); i += 2 {
		u := rune(order.Uint16(b[i:]))
		if utf16.IsSurrogate(u) && u < 0xDC00 && i+2 < len(b) {
			if pair := utf16.DecodeRune(u, rune(order.Uint16(b[i+2:]))); pair != 0xFFFD {
				out = appendWTF8(out, pair)
				i += 2
				continue
			}
		}
		out = appendWTF8(out, u)
	}
	return out, nil
}

func decodeUTF32(b []byte, order binary.ByteOrder) ([]byte, error) {
	if len(b)%4 != 0 {
		return nil, &Error{"UnicodeDecodeError", "'utf-32' codec can't decode bytes: truncated data"}
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i += 4 {
		c := order.Uint32(b[i:])
		if c > 0x10FFFF {
			return nil, &Error{"UnicodeDecodeError", fmt.Sprintf("'utf-32' codec can't decode bytes in position %d-%d: code point not in range(0x110000)", i, i+3)}
		}
		out = appendWTF8(out, rune(c))
	}
	return out, nil
}
