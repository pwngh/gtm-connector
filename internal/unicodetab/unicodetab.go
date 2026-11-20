// Package unicodetab answers the Unicode questions the evidence rules ask, exactly as the Python oracle
// (CPython 3.14.6, Unicode 16.0.0) answers them. Every table comes from testdata/oracle/unicode through
// gen/main.go. Nothing here reads Go's unicode package.
package unicodetab

import "sort"

//go:generate go run ./gen

type rng struct{ lo, hi rune }

type categoryRun struct {
	lo, hi rune
	name   uint8
}

type combiningRun struct {
	lo, hi rune
	class  uint8
}

type mapping struct {
	code  rune
	parts []rune
}

// MaxRune is the last code point.
const MaxRune = 0x10FFFF

// set is a bitset over every code point.
type set []uint64

func newSet(ranges []rng) set {
	s := make(set, (MaxRune+1)/64)
	for _, r := range ranges {
		for c := r.lo; c <= r.hi; c++ {
			s[c>>6] |= 1 << (uint(c) & 63)
		}
	}
	return s
}

func (s set) has(c rune) bool {
	return c >= 0 && c <= MaxRune && s[c>>6]&(1<<(uint(c)&63)) != 0
}

var (
	isSpace       = newSet(pyIsspaceRanges)
	isDigit       = newSet(pyIsdigitRanges)
	isDecimal     = newSet(pyIsdecimalRanges)
	isPrintable   = newSet(pyIsprintableRanges)
	isLower       = newSet(pyIslowerRanges)
	isUpper       = newSet(pyIsupperRanges)
	isTitleOnly   = newSet(pyIstitleOnlyRanges)
	isAlpha       = newSet(pyIsalphaRanges)
	reSpace       = newSet(reSpaceRanges)
	reDigit       = newSet(reDigitRanges)
	reWord        = newSet(reWordRanges)
	icAZ          = newSet(icAzRanges)
	caseIgnorable = newSet(caseIgnorableRanges)
	cased         = newSet(casedRanges)
	nfkcNetloc    = newSet(nfkcNetlocRanges)
	lowerMap      = mappings(lowerEntries)
)

func mappings(entries []mapping) map[rune][]rune {
	m := make(map[rune][]rune, len(entries))
	for _, e := range entries {
		m[e.code] = e.parts
	}
	return m
}

// IsSpace is str.isspace on one character. str.split() and str.strip() use the same set.
func IsSpace(c rune) bool { return isSpace.has(c) }

// IsDigit is str.isdigit on one character.
func IsDigit(c rune) bool { return isDigit.has(c) }

// IsDecimal is str.isdecimal on one character.
func IsDecimal(c rune) bool { return isDecimal.has(c) }

// IsPrintable is str.isprintable on one character.
func IsPrintable(c rune) bool { return isPrintable.has(c) }

// IsLower is str.islower on one character.
func IsLower(c rune) bool { return isLower.has(c) }

// IsUpper is str.isupper on one character.
func IsUpper(c rune) bool { return isUpper.has(c) }

// IsTitleOnly holds for a titlecase character that is not also uppercase.
func IsTitleOnly(c rune) bool { return isTitleOnly.has(c) }

// IsAlpha is str.isalpha on one character.
func IsAlpha(c rune) bool { return isAlpha.has(c) }

// IsASCII is str.isascii on one character.
func IsASCII(c rune) bool { return c >= 0 && c < 128 }

// ReSpace is what re's \s matches in a str pattern.
func ReSpace(c rune) bool { return reSpace.has(c) }

// ReDigit is what re's \d matches in a str pattern.
func ReDigit(c rune) bool { return reDigit.has(c) }

// ReWord is what re's \w matches in a str pattern.
func ReWord(c rune) bool { return reWord.has(c) }

// IgnoreCaseAZ is what [a-z] matches under re.IGNORECASE.
func IgnoreCaseAZ(c rune) bool { return icAZ.has(c) }

// IgnoreCaseLetter is what one ASCII letter matches under re.IGNORECASE. The letter is given in either case.
func IgnoreCaseLetter(letter byte, c rune) bool {
	if letter >= 'A' && letter <= 'Z' {
		letter += 'a' - 'A'
	}
	if letter < 'a' || letter > 'z' {
		panic("unicodetab: not an ASCII letter")
	}
	for _, r := range icLetterRanges[letter-'a'] {
		if c >= r.lo && c <= r.hi {
			return true
		}
	}
	return false
}

// NFKCNetloc holds for a code point whose NFKC form holds one of / ? # @ :, which urlsplit refuses in a
// netloc that is not all ASCII.
func NFKCNetloc(c rune) bool { return nfkcNetloc.has(c) }

// Category is unicodedata.category.
func Category(c rune) string {
	i := sort.Search(len(categoryRuns), func(i int) bool { return categoryRuns[i].hi >= c })
	if i == len(categoryRuns) || c < categoryRuns[i].lo {
		return "Cn"
	}
	return categoryNames[categoryRuns[i].name]
}

// CombiningClass is unicodedata.combining.
func CombiningClass(c rune) uint8 {
	if c < 0x300 {
		return 0
	}
	i := sort.Search(len(combiningRuns), func(i int) bool { return combiningRuns[i].hi >= c })
	if i == len(combiningRuns) || c < combiningRuns[i].lo {
		return 0
	}
	return combiningRuns[i].class
}

// Lower is str.lower on a whole string, with the final sigma decided by its context.
func Lower(s []rune) []rune {
	out := make([]rune, 0, len(s))
	for i, c := range s {
		if c == 0x3A3 {
			out = append(out, sigma(s, i))
			continue
		}
		if parts, ok := lowerMap[c]; ok {
			out = append(out, parts...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

// LowerString is Lower on a string.
func LowerString(s string) string { return string(Lower([]rune(s))) }

// sigma is CPython's handle_capital_sigma: final when a cased letter comes before, past case-ignorable
// ones, and none comes after.
func sigma(s []rune, i int) rune {
	j := i - 1
	for j >= 0 && caseIgnorable.has(s[j]) {
		j--
	}
	final := j >= 0 && cased.has(s[j])
	if final && i+1 < len(s) {
		j = i + 1
		for j < len(s) && caseIgnorable.has(s[j]) {
			j++
		}
		final = j == len(s) || !cased.has(s[j])
	}
	if final {
		return 0x3C2
	}
	return 0x3C3
}

// IsLowerString is str.islower on a whole string.
func IsLowerString(s []rune) bool {
	found := false
	for _, c := range s {
		if isUpper.has(c) || isTitleOnly.has(c) {
			return false
		}
		if isLower.has(c) {
			found = true
		}
	}
	return found
}

// IsUpperString is str.isupper on a whole string.
func IsUpperString(s []rune) bool {
	found := false
	for _, c := range s {
		if isLower.has(c) || isTitleOnly.has(c) {
			return false
		}
		if isUpper.has(c) {
			found = true
		}
	}
	return found
}
