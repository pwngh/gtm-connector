package evidence

import (
	"strings"

	"golang.org/x/net/idna"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// urlIDNA is UTS #46 processing with the flags the URL Standard's host parser sets: CheckHyphens false,
// CheckBidi true, CheckJoiners true, UseSTD3ASCIIRules false, Transitional false and VerifyDnsLength false.
// MapForLookup turns on the hyphen and STD3 checks, so the options after it turn them off again.
var urlIDNA = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false), idna.CheckHyphens(false),
	idna.CheckJoiners(true), idna.StrictDomainName(false), idna.VerifyDNSLength(false))

// validIDNA is whether a browser opens host, a lowercase ASCII host with an xn-- label, as it is written. UTS
// #46 finds no error in it: each xn-- label decodes, its decoded name is valid under the mapping, the bidi rule
// and the joiner rules, and it encodes back to the same label, which an empty name does not. x/net/idna leaves
// out one rule with CheckHyphens false, that a decoded name must not start with xn--, and misses some joiner
// contexts, so both are checked here.
// Every decoded character must also be assigned in Unicode 16.0.0, the version of internal/unicodetab, since
// x/net/idna's tables are of a later version than some browsers'.
func validIDNA(host string) bool {
	ascii, err := urlIDNA.ToASCII(host)
	if err != nil || ascii != host {
		return false
	}
	names, err := urlIDNA.ToUnicode(host)
	if err != nil {
		return false
	}
	for _, name := range strings.Split(names, ".") {
		if strings.HasPrefix(name, "xn--") || !joiners([]rune(name)) {
			return false
		}
		for _, c := range name {
			if u.Category(c) == "Cn" {
				return false
			}
		}
	}
	return true
}

// joiners is the ContextJ rule of RFC 5892 appendix A on a decoded name. A zero width joiner must follow a
// virama. A zero width non-joiner must follow a virama, or stand between a letter that joins to its left and
// one that joins to its right, with only transparent characters between them: (L|D) T* ZWNJ T* (R|D).
// x/net/idna checks this with a state machine that a non-joining character after the non-joiner resets, so it
// accepts BEH, ZWNJ, 1.
func joiners(name []rune) bool {
	for i, c := range name {
		if c != 0x200C && c != 0x200D {
			continue
		}
		if i > 0 && u.CombiningClass(name[i-1]) == 9 {
			continue
		}
		if c == 0x200D {
			return false
		}
		before := i - 1
		for before >= 0 && u.JoiningType(name[before]) == 'T' {
			before--
		}
		after := i + 1
		for after < len(name) && u.JoiningType(name[after]) == 'T' {
			after++
		}
		if before < 0 || after == len(name) || !strings.ContainsRune("LD", rune(u.JoiningType(name[before]))) ||
			!strings.ContainsRune("RD", rune(u.JoiningType(name[after]))) {
			return false
		}
	}
	return true
}
