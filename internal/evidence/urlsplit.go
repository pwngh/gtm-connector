package evidence

// urllib.parse.urlsplit as CPython 3.14.6 reads a link, with the hostname and port properties and the
// ipaddress checks it makes on a bracketed host.

import (
	"fmt"
	"strings"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// URLParts is what urlsplit returns.
type URLParts struct {
	Scheme, Netloc, Path, Query, Fragment string
}

// ValueError is a ValueError that the Python library raises, with its message.
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

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

const schemeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-."

// URLSplit is urlsplit(url).
func URLSplit(url []rune) (URLParts, error) {
	start := 0
	for start < len(url) && url[start] <= ' ' {
		start++
	}
	s := make([]rune, 0, len(url)-start)
	for _, c := range url[start:] {
		if c != '\t' && c != '\r' && c != '\n' {
			s = append(s, c)
		}
	}
	var parts URLParts
	if i := index(s, ':', 0); i > 0 && asciiLetter(s[0]) {
		scheme := true
		for _, c := range s[:i] {
			if !in(c, schemeChars) {
				scheme = false
				break
			}
		}
		if scheme {
			parts.Scheme = strings.ToLower(string(s[:i])) // ASCII only
			s = s[i+1:]
		}
	}
	var netloc []rune
	if len(s) >= 2 && s[0] == '/' && s[1] == '/' {
		delim := len(s)
		for _, c := range "/?#" {
			if w := index(s, c, 2); w >= 0 && w < delim {
				delim = w
			}
		}
		netloc, s = s[2:delim], s[delim:]
		open, closed := contains(netloc, '['), contains(netloc, ']')
		if open != closed {
			return URLParts{}, &ValueError{"Invalid IPv6 URL"}
		}
		if open {
			if err := checkBracketedNetloc(netloc); err != nil {
				return URLParts{}, err
			}
		}
	}
	if i := index(s, '#', 0); i >= 0 {
		parts.Fragment = string(s[i+1:])
		s = s[:i]
	}
	if i := index(s, '?', 0); i >= 0 {
		parts.Query = string(s[i+1:])
		s = s[:i]
	}
	if err := checkNetloc(netloc); err != nil {
		return URLParts{}, err
	}
	parts.Netloc, parts.Path = string(netloc), string(s)
	return parts, nil
}

func checkBracketedNetloc(netloc []rune) error {
	_, hostPort, _ := partition(netloc, '@', true)
	beforeBracket, bracketed, open := partition(hostPort, '[', false)
	var host []rune
	if open {
		if len(beforeBracket) > 0 {
			return &ValueError{"Invalid IPv6 URL"}
		}
		var port []rune
		host, port, _ = partition(bracketed, ']', false)
		if len(port) > 0 && port[0] != ':' {
			return &ValueError{"Invalid IPv6 URL"}
		}
	} else {
		host, _, _ = partition(hostPort, ':', false)
	}
	return checkBracketedHost(host)
}

func checkBracketedHost(host []rune) error {
	if len(host) > 0 && host[0] == 'v' {
		// \Av[a-fA-F0-9]+\..+\z
		j := 1
		for j < len(host) && asciiHex(host[j]) {
			j++
		}
		if j == 1 || j >= len(host) || host[j] != '.' || j+1 == len(host) || contains(host[j+1:], '\n') {
			return &ValueError{"IPvFuture address is invalid"}
		}
		return nil
	}
	switch {
	case ipv4(host):
		return &ValueError{"An IPv4 address cannot be in brackets"}
	case ipv6(host):
		return nil
	}
	return &ValueError{Repr(host) + " does not appear to be an IPv4 or IPv6 address"}
}

// checkNetloc is _checknetloc. A netloc's NFKC form gains one of / ? # @ : exactly when the netloc holds a
// code point in the oracle's set, since none of those marks composes with a neighbour.
func checkNetloc(netloc []rune) error {
	for _, c := range netloc {
		if u.NFKCNetloc(c) {
			return &ValueError{"netloc '" + string(netloc) + "' contains invalid characters under NFKC normalization"}
		}
	}
	return nil
}

func ipv4(s []rune) bool {
	if contains(s, '/') {
		return false
	}
	octets := strings.Split(string(s), ".")
	if len(octets) != 4 {
		return false
	}
	for _, octet := range octets {
		if octet == "" || len(octet) > 3 || octet != "0" && octet[0] == '0' {
			return false
		}
		value := 0
		for _, c := range octet {
			if !asciiDigit(c) {
				return false
			}
			value = value*10 + int(c-'0')
		}
		if value > 255 {
			return false
		}
	}
	return true
}

func ipv6(s []rune) bool {
	if contains(s, '/') {
		return false
	}
	address, scope, found := partition(s, '%', false)
	if found && (len(scope) == 0 || contains(scope, '%')) {
		return false
	}
	if len(address) == 0 || len(address) > 45 {
		return false
	}
	parts := strings.SplitN(string(address), ":", 10)
	if len(parts) < 3 {
		return false
	}
	if strings.Contains(parts[len(parts)-1], ".") {
		if !ipv4([]rune(parts[len(parts)-1])) {
			return false
		}
		parts = append(parts[:len(parts)-1], "0", "0")
	}
	if len(parts) > 9 {
		return false
	}
	skip := -1
	for i := 1; i < len(parts)-1; i++ {
		if parts[i] == "" {
			if skip >= 0 {
				return false
			}
			skip = i
		}
	}
	hi, lo := len(parts), 0
	if skip >= 0 {
		hi, lo = skip, len(parts)-skip-1
		if parts[0] == "" {
			if hi--; hi > 0 {
				return false
			}
		}
		if parts[len(parts)-1] == "" {
			if lo--; lo > 0 {
				return false
			}
		}
		if 8-(hi+lo) < 1 {
			return false
		}
	} else if len(parts) != 8 || parts[0] == "" || parts[len(parts)-1] == "" {
		return false
	}
	for _, part := range append(parts[:hi:hi], parts[len(parts)-lo:]...) {
		if part == "" || len([]rune(part)) > 4 {
			return false
		}
		for _, c := range part {
			if !asciiHex(c) {
				return false
			}
		}
	}
	return true
}

// hostInfo is the hostname and port text of a netloc, as _hostinfo reads it.
func hostInfo(netloc []rune) (host, port []rune) {
	_, hostPort, _ := partition(netloc, '@', true)
	_, bracketed, open := partition(hostPort, '[', false)
	if open {
		host, port, _ = partition(bracketed, ']', false)
		_, port, _ = partition(port, ':', false)
	} else {
		host, port, _ = partition(hostPort, ':', false)
	}
	return host, port
}

// Hostname is SplitResult.hostname: empty when there is none.
func Hostname(netloc string) string {
	host, _ := hostInfo([]rune(netloc))
	if len(host) == 0 {
		return ""
	}
	name, zone, found := partition(host, '%', false)
	if !found {
		return string(u.Lower(name))
	}
	return string(u.Lower(name)) + "%" + string(zone)
}

// Port is SplitResult.port: -1 when there is none.
func Port(netloc string) (int, error) {
	_, port := hostInfo([]rune(netloc))
	if len(port) == 0 {
		return -1, nil
	}
	for _, c := range port {
		if !asciiDigit(c) {
			return 0, &ValueError{"Port could not be cast to integer value as " + Repr(port)}
		}
	}
	if len(port) > 4300 {
		return 0, &ValueError{fmt.Sprintf("Exceeds the limit (4300 digits) for integer string conversion: "+
			"value has %d digits; use sys.set_int_max_str_digits() to increase the limit", len(port))}
	}
	digits := strings.TrimLeft(string(port), "0")
	value := 0
	for _, c := range digits {
		if value = value*10 + int(c-'0'); value > 65535 {
			return 0, &ValueError{"Port out of range 0-65535"}
		}
	}
	return value, nil
}

// Repr is repr(str).
func Repr(s []rune) string {
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
