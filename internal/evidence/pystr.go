package evidence

// The Python str methods the evidence rules use on code points: find, partition, rpartition and repr.

import (
	"fmt"
	"strings"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

func index(s []rune, c rune, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func contains(s []rune, c rune) bool { return index(s, c, 0) >= 0 }

// partition is str.partition and rpartition.
func partition(s []rune, c rune, last bool) (head, tail []rune, found bool) {
	i := index(s, c, 0)
	if last {
		i = -1
		for j := len(s) - 1; j >= 0; j-- {
			if s[j] == c {
				i = j
				break
			}
		}
	}
	if i < 0 {
		if last {
			return nil, s, false
		}
		return s, nil, false
	}
	return s[:i], s[i+1:], true
}

// pyRepr is repr(str).
func pyRepr(s []rune) string {
	quote := '\''
	if contains(s, '\'') && !contains(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, c := range s {
		switch {
		case c == quote || c == '\\':
			b.WriteRune('\\')
			b.WriteRune(c)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < ' ' || c == 0x7F:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c < 0x7F || u.IsPrintable(c):
			b.WriteRune(c)
		case c <= 0xFF:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			fmt.Fprintf(&b, `\U%08x`, c)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
