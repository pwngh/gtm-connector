package evidence

// Hand-written scanners for the patterns in units.py, slack_mapping.py and slack_callback.py. Each one
// finds exactly what Python's re finds: the same alternative first, the same greedy or lazy extent, and
// search(line, pos, endpos) where ^ never matches at pos > 0 and \Z matches at endpos. Character classes
// come from unicodetab. Positions count code points, and a window is clamped to the text as re clamps it.

import u "github.com/pwngh/gtm-connector/internal/unicodetab"

func asciiLetter(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func asciiDigit(c rune) bool  { return c >= '0' && c <= '9' }
func asciiUpper(c rune) bool  { return c >= 'A' && c <= 'Z' }
func asciiHex(c rune) bool    { return asciiDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func in(c rune, set string) bool {
	for _, x := range set {
		if c == x {
			return true
		}
	}
	return false
}

// literal is whether s holds word at i, before end.
func literal(s []rune, i, end int, word string) bool {
	for _, c := range word {
		if i >= end || s[i] != c {
			return false
		}
		i++
	}
	return true
}

// literalFold is literal under re.IGNORECASE: each letter matches its IGNORECASE set from the oracle.
func literalFold(s []rune, i, end int, word string) bool {
	for _, c := range word {
		if i >= end {
			return false
		}
		if asciiLetter(c) {
			if !u.IgnoreCaseLetter(byte(c), s[i]) {
				return false
			}
		} else if s[i] != c {
			return false
		}
		i++
	}
	return true
}

// [a-z0-9] under IGNORECASE.
func foldAlnum(c rune) bool { return u.IgnoreCaseAZ(c) || asciiDigit(c) }

// [a-z0-9+.-] under IGNORECASE.
func foldScheme(c rune) bool { return foldAlnum(c) || c == '+' || c == '.' || c == '-' }

// Target is _IS_TARGET.match(text) (units.py line 117, under IGNORECASE): the end of the match.
func Target(s []rune) (int, bool) {
	n := len(s)
	if n > 0 && u.IgnoreCaseAZ(s[0]) {
		j := 1
		for j < n && foldScheme(s[j]) {
			j++
		}
		if literal(s, j, n, "://") {
			return j + 3, true
		}
	}
	if literalFold(s, 0, n, "mailto:") {
		return 7, true
	}
	if literalFold(s, 0, n, "www.") {
		return 4, true
	}
	if literal(s, 0, n, "//") && n > 2 && foldAlnum(s[2]) {
		return 2, true
	}
	return 0, false
}

// clamp limits pos and endpos to the text, as re does for match, search and fullmatch. A window that ends
// before it starts matches nothing.
func clamp(s []rune, pos, end int) (int, int) {
	return min(max(pos, 0), len(s)), min(max(end, 0), len(s))
}

// Span is one match.
type Span struct{ Start, End int }

// rawLinkHead is where the head of _RAW_LINK ends when one starts at i, and whether its group matched: the
// link is a www. address.
func rawLinkHead(s []rune, i int) (end int, www, ok bool) {
	n := len(s)
	if u.IgnoreCaseAZ(s[i]) {
		j := i + 1
		for j < n && j-i-1 < 31 && foldScheme(s[j]) {
			j++
		}
		if literal(s, j, n, "://") {
			return j + 3, false, true
		}
	}
	if literalFold(s, i, n, "mailto:") {
		return i + 7, false, true
	}
	if (i == 0 || !foldAlnum(s[i-1])) && literalFold(s, i, n, "www.") {
		return i + 4, true, true
	}
	if (i == 0 || !(foldAlnum(s[i-1]) || s[i-1] == ':' || s[i-1] == '/')) && literal(s, i, n, "//") &&
		i+2 < n && foldAlnum(s[i+2]) {
		return i + 2, false, true
	}
	return 0, false, false
}

// rawLink is one match of _RAW_LINK.
type rawLink struct {
	Span
	www bool
}

// rawLinks is _RAW_LINK.finditer(text), with each match's group.
func rawLinks(s []rune) []rawLink {
	var found []rawLink
	for i := 0; i < len(s); {
		head, www, ok := rawLinkHead(s, i)
		if !ok {
			i++
			continue
		}
		j := head
		for j < len(s) && !u.ReSpace(s[j]) && !in(s[j], `<>"'`) {
			j++
		}
		found = append(found, rawLink{Span{i, j}, www})
		i = j
	}
	return found
}

// RawLinks is _RAW_LINK.finditer(text) (units.py line 121, under IGNORECASE).
func RawLinks(s []rune) []Span {
	var found []Span
	for _, link := range rawLinks(s) {
		found = append(found, link.Span)
	}
	return found
}

// SentenceEnd is one match of _SENTENCE_END: the closing punctuation ends at End1, and the space after
// it at End2.
type SentenceEnd struct{ Start, End1, End2 int }

// SentenceEnds is _SENTENCE_END.finditer(line) (units.py line 284).
func SentenceEnds(s []rune) []SentenceEnd {
	var found []SentenceEnd
	n := len(s)
	for i := 0; i < n; {
		if !in(s[i], ".!?") {
			i++
			continue
		}
		j := i + 1
		for j < n && in(s[j], "\"')]”’»") {
			j++
		}
		k := j
		for k < n && u.ReSpace(s[k]) {
			k++
		}
		// The lookahead's optional opener adds nothing: an opener is itself neither space nor a-z.
		if k == j || k == n || s[k] >= 'a' && s[k] <= 'z' {
			i++
			continue
		}
		found = append(found, SentenceEnd{i, j, k})
		i = k
	}
	return found
}

var openers = []string{"We", "Our", "They", "Their", "He", "She", "It", "Its", "I", "You", "My", "Your",
	"This", "That", "These", "Those", "The", "A", "An", "In", "On", "At", "For", "If", "When", "As", "There",
	"But", "And", "So", "Then", "However", "Therefore"}

// Opener is _OPENER.match(line, pos, endpos) (units.py line 314): the first alternative, in order, that
// is not followed by a word character, a dot or a hyphen.
func Opener(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	for _, word := range openers {
		if !literal(s, pos, end, word) {
			continue
		}
		after := pos + len(word)
		if after == end || !(u.ReWord(s[after]) || s[after] == '.' || s[after] == '-') {
			return after, true
		}
	}
	return 0, false
}

// Named is _NAMED.match(text) (units.py line 404): the speaker's name runs from Start to NameEnd, and
// the words start at End.
type Named struct{ Start, NameEnd, End int }

func nameChar(c rune) bool { return !in(c, ":!?<>\n。！？।؟۔") }

// NamedMatch is _NAMED.match(text).
func NamedMatch(s []rune) (Named, bool) {
	n := len(s)
	g := 0
	for g < n && (s[g] == ' ' || s[g] == '\t') {
		g++
	}
	// [^\W\d_]: a word character that is neither a digit nor an underscore.
	if g == n || !u.ReWord(s[g]) || u.ReDigit(s[g]) || s[g] == '_' {
		return Named{}, false
	}
	// The name's tail is lazy: the shortest one, up to 99 characters, that a colon and words follow.
	for q := g + 1; q <= g+100 && q <= n; q++ {
		if q > g+1 && !nameChar(s[q-1]) {
			break
		}
		r := q
		for r < n && (s[r] == ' ' || s[r] == '\t') {
			r++
		}
		if r == n || s[r] != ':' {
			continue
		}
		e := r + 1
		for e < n && (s[e] == ' ' || s[e] == '\t') {
			e++
		}
		if e > r+1 && e < n && !u.ReSpace(s[e]) {
			return Named{g, q, e}, true
		}
	}
	return Named{}, false
}

// Percents is the search for a % that two hex digits do not follow (slack_mapping.py line 108 and
// slack_callback.py line 163; the two patterns differ only in the order of their hex ranges).
func Percents(s []rune) []Span {
	var found []Span
	for i, c := range s {
		if c == '%' && !(i+2 < len(s) && asciiHex(s[i+1]) && asciiHex(s[i+2])) {
			found = append(found, Span{i, i + 1})
		}
	}
	return found
}

// ---- the patterns _continues and _unfinished search with a window ----

// before is the class of _BEFORE after its ^ alternative.
func before(c rune) bool { return u.ReSpace(c) || in(c, "([\"'“‘«") }

// searchBefore is search(s, pos, end) for _BEFORE followed by a body that ends at \Z: the leftmost start,
// trying ^ (only at 0) before a class character.
func searchBefore(s []rune, pos, end int, body func(b int) bool) (int, bool) {
	for start := pos; start <= end; start++ {
		if start == 0 && body(0) {
			return 0, true
		}
		if start < end && before(s[start]) && body(start+1) {
			return start, true
		}
	}
	return 0, false
}

// closersTo is whether s from i to end is only characters of set.
func closersTo(s []rune, i, end int, set string) bool {
	for ; i < end; i++ {
		if !in(s[i], set) {
			return false
		}
	}
	return true
}

const (
	closersShort = "\"')]"
	closersLong  = "\"')]”’»"
)

// after is _AFTER from i: a period, closing marks, and the end.
func after(s []rune, i, end int) bool {
	return i < end && s[i] == '.' && closersTo(s, i+1, end, closersLong)
}

func letters(s []rune, i, end int) int {
	j := i
	for j < end && asciiLetter(s[j]) {
		j++
	}
	return j
}

// ListMarkerFull is _LIST_MARKER.fullmatch(s, pos, end).
func ListMarkerFull(s []rune, pos, end int) bool {
	pos, end = clamp(s, pos, end)
	j := pos
	for j < end && u.ReSpace(s[j]) {
		j++
	}
	switch {
	case j < end && u.ReDigit(s[j]):
		for j < end && u.ReDigit(s[j]) {
			j++
		}
	case j < end && asciiLetter(s[j]):
		j++
	default:
		return false
	}
	return j < end && s[j] == '.' && closersTo(s, j+1, end, closersShort)
}

// Dotted is _DOTTED.search(s, pos, end): two or more groups of one to six letters and a period, then
// closing marks. The match ends at end.
func Dotted(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	return searchBefore(s, pos, end, func(b int) bool {
		groups, j := 0, b
		for {
			k := letters(s, j, end)
			if k == j || k-j > 6 || k >= end || s[k] != '.' {
				break
			}
			groups, j = groups+1, k+1
		}
		for j < end && in(s[j], closersShort) {
			j++
		}
		return groups >= 2 && j == end
	})
}

var mayEnd = words("Inc", "Ltd", "Co", "Corp", "LLP", "LLC", "LP", "PLC", "PC", "I")

var forms = words(
	"Mr", "Mrs", "Ms", "Mx", "Mme", "Mlle", "Messrs", "Sra", "Dr", "Prof", "Esq", "Sr", "Jr", "Hon", "Rev", "Sen",
	"Rep", "Gov", "Pres", "Gen", "Col", "Capt", "Lt", "Sgt",
	"Assn", "Bros", "Dept", "Div", "Est", "Mfg", "Natl", "Intl", "Univ", "St", "Mt", "Ft",
	"v", "vs", "al", "cf", "etc", "ibid", "id", "supra", "infra", "approx", "Approx", "misc", "ca", "pp", "vol",
	"fig", "nr", "Nr", "est", "ext",
	"Fed", "Civ", "Crim", "Evid", "App", "Supp", "Dist", "Cir", "Bankr", "Admin", "Stat", "Reg", "Code", "Comp",
	"Ann", "Cons",
	"Tit", "Art", "Sec", "Ch", "Subd", "Subsec", "Cl", "Para", "Pt", "Ex", "Sched", "Amend", "Op", "Proc", "Ord",
	"Const",
	"tit", "art", "sec", "ch", "subd", "subsec", "cl", "para", "pt", "ex", "sched", "amend", "op", "proc",
	"Fam", "Bus", "Com", "Fin", "Ins", "Lab", "Pen", "Prob", "Prop", "Tax", "Veh", "Welf",
	"Ala", "Ariz", "Ark", "Cal", "Colo", "Conn", "Del", "Fla", "Ga", "Ill", "Ind", "Kan", "Ky", "La", "Md", "Mass",
	"Mich", "Minn", "Miss", "Mo", "Mont", "Neb", "Nev", "Okla", "Or", "Pa",
	"Tenn", "Tex", "Va", "Vt", "Wash", "Wis", "Wyo",
	"Mon", "Tue", "Tues", "Wed", "Thu", "Thur", "Thurs", "Fri", "Sat", "Sun",
	"Jan", "Feb", "Mar", "Apr", "Jun", "Jul", "Aug", "Sep", "Sept", "Oct", "Nov", "Dec")

func words(list ...string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, w := range list {
		m[w] = true
	}
	return m
}

// isForm is whether a run of letters is one of _FORMS, whose last alternative is any one letter. Every
// alternative is letters only, so the run before the period must be the whole alternative.
func isForm(run []rune) bool { return len(run) == 1 || forms[string(run)] }

// MayEnd is _MAY_END.search(s, pos, end).
func MayEnd(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	return searchBefore(s, pos, end, func(b int) bool {
		j := letters(s, b, end)
		return mayEnd[string(s[b:j])] && after(s, j, end)
	})
}

// Abbreviation is _ABBREVIATION.search(s, pos, end).
func Abbreviation(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	return searchBefore(s, pos, end, func(b int) bool {
		j := letters(s, b, end)
		return j > b && isForm(s[b:j]) && after(s, j, end)
	})
}

// Short is _SHORT.search(s, pos, end): a capital and one to four letters, then a period. It returns the
// match's start and the span of the short word, its group 1.
func Short(s []rune, pos, end int) (start, wordStart, wordEnd int, ok bool) {
	pos, end = clamp(s, pos, end)
	start, ok = searchBefore(s, pos, end, func(b int) bool {
		if b >= end || !asciiUpper(s[b]) {
			return false
		}
		j := letters(s, b+1, end)
		if j-b-1 < 1 || j-b-1 > 4 || !after(s, j, end) {
			return false
		}
		wordStart, wordEnd = b, j
		return true
	})
	return start, wordStart, wordEnd, ok
}

// ShortNext is _SHORT_NEXT.match(s, pos, end).
func ShortNext(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	j := letters(s, pos, end)
	if j > pos && isForm(s[pos:j]) && j < end && s[j] == '.' {
		return j + 1, true
	}
	return 0, false
}

func numberSignBody(s []rune, b, end int) bool {
	if b >= end || s[b] != 'N' && s[b] != 'n' || !literal(s, b+1, end, "o") {
		return false
	}
	j := b + 2
	if j < end && s[j] == 's' {
		j++
	}
	return j+1 == end && s[j] == '.'
}

// NumberSign is _NUMBER_SIGN.search(s, pos, end).
func NumberSign(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	return searchBefore(s, pos, end, func(b int) bool { return numberSignBody(s, b, end) })
}

// NumberSignFull is _NUMBER_SIGN.fullmatch(s, pos, end).
func NumberSignFull(s []rune, pos, end int) bool {
	pos, end = clamp(s, pos, end)
	if pos == 0 && numberSignBody(s, 0, end) {
		return true
	}
	return pos < end && before(s[pos]) && numberSignBody(s, pos+1, end)
}

var neverLast = []string{"Mr", "Mrs", "Ms", "Mx", "Mme", "Mlle", "Messrs", "Dr", "Prof", "Hon", "v", "vs", "cf",
	"Fed", "No", "Nos", "U.S.C", "C.F.R", "e.g", "i.e"}

// NeverLast is _NEVER_LAST.search(s, pos, end).
func NeverLast(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	return searchBefore(s, pos, end, func(b int) bool {
		for _, word := range neverLast {
			if literal(s, b, end, word) && after(s, b+len(word), end) {
				return true
			}
		}
		return false
	})
}

// SectionSign is _SECTION_SIGN.match(s, pos, end).
func SectionSign(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	j := pos
	for j < end && in(s[j], "\"'([") {
		j++
	}
	if j < end && (s[j] == '§' || s[j] == '¶') {
		return j + 1, true
	}
	return 0, false
}

// Ended is _ENDED.search(s, pos, end): closing punctuation of any script, then closing marks to the end.
func Ended(s []rune, pos, end int) (int, bool) {
	pos, end = clamp(s, pos, end)
	for i := pos; i < end; i++ {
		if in(s[i], ".!?。！？।؟۔") &&
			closersTo(s, i+1, end, "\"')]”’»」）") {
			return i, true
		}
	}
	return 0, false
}

// ---- the patterns voices reads a WebVTT cue with ----

// classes is where (?:\.[^\s>.]*)* ends from i: each class is a dot and what follows it, up to whitespace,
// a dot or a closing bracket. With angle, an opening bracket ends a class too ([^\s<>.]).
func classes(s []rune, i int, angle bool) int {
	for i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && !u.ReSpace(s[i]) && s[i] != '>' && s[i] != '.' && !(angle && s[i] == '<') {
			i++
		}
	}
	return i
}

// VoiceTag is one match of _VOICE: a voice tag with its speaker's name, or the tag that closes a voice,
// which has no name.
type VoiceTag struct {
	Start, End         int
	NameStart, NameEnd int
	Named              bool
}

// voiceTag is _VOICE.match(s, i) (units.py line 145). classesEnd is where the tag's classes end from the
// character after "<v".
func voiceTag(s []rune, i int, classesEnd func(int) int) (VoiceTag, bool) {
	n := len(s)
	if literal(s, i, n, "</v>") {
		return VoiceTag{Start: i, End: i + 4}, true
	}
	if !literal(s, i, n, "<v") {
		return VoiceTag{}, false
	}
	p := classesEnd(i + 2)
	q := p
	for q < n && (s[q] == ' ' || s[q] == '\t') {
		q++
	}
	r := q
	for r < n && !in(s[r], "<>\n") {
		r++
	}
	if q == p || r == n || s[r] != '>' {
		return VoiceTag{}, false
	}
	// The name is what follows the spaces. When nothing does, the last of two or more spaces is the name.
	switch {
	case r > q:
		return VoiceTag{i, r + 1, q, r, true}, true
	case q-p >= 2:
		return VoiceTag{i, r + 1, q - 1, q, true}, true
	}
	return VoiceTag{}, false
}

// VoiceTags is _VOICE.finditer(text).
func VoiceTags(s []rune) []VoiceTag { return voiceTags(s, nil) }

// voiceTags finds the voice tags in time that grows with the text. The classes of a tag run to the next
// whitespace or closing bracket, through any "<v" on the way, so a run of class characters is scanned once:
// a later tag that starts inside the run ends its classes where the run ends. scanned, when given, counts
// the characters the class scans read.
func voiceTags(s []rune, scanned *int) []VoiceTag {
	from, run := 0, 0 // s[from:run] is the run of class characters scanned last
	classesEnd := func(i int) int {
		if i >= len(s) || s[i] != '.' {
			return i
		}
		if i < from || i >= run {
			from, run = i, classes(s, i, false)
			if scanned != nil {
				*scanned += run - from
			}
		}
		return run
	}
	var found []VoiceTag
	for i := 0; i < len(s); {
		tag, ok := voiceTag(s, i, classesEnd)
		if !ok {
			i++
			continue
		}
		found = append(found, tag)
		i = tag.End
	}
	return found
}

// cueStamp is where (?:\d{2,}:)?[0-5]\d:[0-5]\d\.\d{3}> ends from i, the character after the opening bracket.
func cueStamp(s []rune, i int) (int, bool) {
	n := len(s)
	clock := func(j int) (int, bool) {
		if j+10 > n || s[j] < '0' || s[j] > '5' || !u.ReDigit(s[j+1]) || s[j+2] != ':' || s[j+3] < '0' ||
			s[j+3] > '5' || !u.ReDigit(s[j+4]) || s[j+5] != '.' || !u.ReDigit(s[j+6]) || !u.ReDigit(s[j+7]) ||
			!u.ReDigit(s[j+8]) || s[j+9] != '>' {
			return 0, false
		}
		return j + 10, true
	}
	hours := i
	for hours < n && u.ReDigit(s[hours]) {
		hours++
	}
	if hours-i >= 2 && hours < n && s[hours] == ':' {
		if end, ok := clock(hours + 1); ok {
			return end, true
		}
	}
	return clock(i)
}

// cueMarkup is _CUE_MARKUP.match(s, i) (units.py line 155): the end of the match.
func cueMarkup(s []rune, i int) (int, bool) {
	n := len(s)
	if i >= n || s[i] != '<' {
		return 0, false
	}
	// A ruby reading, with the text it carries.
	if literal(s, i, n, "<rt") {
		if j := classes(s, i+3, true); j < n && s[j] == '>' {
			j++
			for j < n && s[j] != '<' && s[j] != '>' {
				j++
			}
			if literal(s, j, n, "</rt>") {
				return j + 5, true
			}
		}
	}
	j := i + 1
	if j < n && s[j] == '/' {
		j++
	}
	// A bold, italic, underline, class or ruby span, opening or closing.
	for _, name := range []string{"b", "i", "u", "c", "ruby", "rt"} {
		if literal(s, j, n, name) {
			if k := classes(s, j+len(name), true); k < n && s[k] == '>' {
				return k + 1, true
			}
		}
	}
	// A language span, which alone takes an annotation.
	if literal(s, j, n, "lang") {
		k := classes(s, j+4, true)
		if k < n && s[k] == '>' {
			return k + 1, true
		}
		if k < n && (s[k] == ' ' || s[k] == '\t') {
			k++
			for k < n && !in(s[k], "<>\n") {
				k++
			}
			if k < n && s[k] == '>' {
				return k + 1, true
			}
		}
	}
	// A cue timestamp. It has no closing form.
	return cueStamp(s, i+1)
}

// StripCueMarkup is _CUE_MARKUP.sub("", text).
func StripCueMarkup(s []rune) []rune {
	return substitute(s, func(i int) (int, string, bool) {
		end, ok := cueMarkup(s, i)
		return end, "", ok
	})
}
