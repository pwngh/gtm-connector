package people

import (
	"strings"

	u "github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Label is a label on one line: its words, split on the characters unicodetab counts as space, joined by
// one space. It is the card's oneLine in internal/evidence, so two labels are equal for rung 4 exactly
// when the card today counts them equal.
func Label(text string) string {
	return strings.Join(words(text), " ")
}

// words splits text on unicodetab spaces, as Python's str.split() does.
func words(text string) []string {
	s := []rune(text)
	var found []string
	for i := 0; i < len(s); {
		for i < len(s) && u.IsSpace(s[i]) {
			i++
		}
		j := i
		for j < len(s) && !u.IsSpace(s[j]) {
			j++
		}
		if j > i {
			found = append(found, string(s[i:j]))
		}
		i = j
	}
	return found
}

// honorifics are the personal titles the sentence rules in internal/evidence already know as
// abbreviations. A name variant drops them from its start.
var honorifics = set("mr", "mrs", "ms", "mx", "mme", "mlle", "messrs", "sra", "dr", "prof", "hon", "rev", "sen",
	"rep", "gov", "pres", "gen", "col", "capt", "lt", "sgt")

// suffixes are the name suffixes the sentence rules know. A name variant drops one from its end.
var suffixes = set("jr", "sr", "esq")

// devices are the words a device name ends in, lower case. "Dana Crest's iPhone" is Dana Crest's device.
var devices = set("iphone", "ipad", "ipod", "android", "phone", "mobile", "cell", "galaxy", "pixel", "macbook",
	"macbook pro", "macbook air", "mac", "imac", "laptop", "pc", "computer", "desktop", "surface", "tablet",
	"chromebook", "thinkpad")

// anonymous are the words of a label that names no one: "Speaker 2", "Unknown speaker", "Guest".
var anonymous = set("speaker", "unknown speaker", "unidentified speaker", "participant", "guest", "attendee",
	"caller", "unknown", "user")

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}

// Variant is a name in the form rung 5 compares. It is "" when the text names nobody. In order, it:
//
//  1. puts the text on one line and in lower case (unicodetab's str.lower);
//  2. drops one trailing group in brackets, the organisation in "Dana Crest (Acme)" or "Dana Crest [Acme]";
//  3. reads a device name: "Dana Crest's iPhone" is "dana crest", while "Dana's iPhone" and a bare
//     "iPhone" name nobody, since one word does not name a person;
//  4. drops one suffix from the end (Jr, Sr, Esq, with or without a dot or a comma before it);
//  5. turns "Last, First" into "first last" when the text holds exactly one comma;
//  6. drops honorifics from the start (Mr, Dr, Prof and the other titles the sentence rules know), before
//     and after the turn in step 5.
//
// The same function reads labels and people's names, so a label and a name match when their variants
// are equal and not empty.
func Variant(text string) string {
	s := u.LowerString(Label(text))
	s = dropBrackets(s)
	tokens := words(s)
	tokens, named := dropDevice(tokens)
	if !named {
		return ""
	}
	if n := len(tokens); n > 1 && suffixes[strings.TrimSuffix(tokens[n-1], ".")] {
		tokens = tokens[:n-1]
		tokens[n-2] = strings.TrimSuffix(tokens[n-2], ",")
	}
	s = strings.Join(dropHonorifics(tokens), " ")
	if strings.Count(s, ",") == 1 {
		last, first, _ := strings.Cut(s, ",")
		if last, first = Label(last), Label(first); last != "" && first != "" {
			s = first + " " + last
		}
	}
	return strings.Join(dropHonorifics(words(s)), " ")
}

// dropHonorifics drops honorifics from the start of a name, keeping at least one word.
func dropHonorifics(tokens []string) []string {
	for len(tokens) > 1 && honorifics[strings.TrimSuffix(tokens[0], ".")] {
		tokens = tokens[1:]
	}
	return tokens
}

// dropBrackets drops one trailing group in round or square brackets, when text is left before it.
func dropBrackets(s string) string {
	pairs := [][2]string{{"(", ")"}, {"[", "]"}}
	for _, p := range pairs {
		if strings.HasSuffix(s, p[1]) {
			if i := strings.LastIndex(s, p[0]); i > 0 {
				if rest := Label(s[:i]); rest != "" {
					return rest
				}
			}
		}
	}
	return s
}

// possessive is the ending that makes a word an owner: 's, the same with a right single quote, or a bare
// apostrophe after s.
func possessive(word string) (string, bool) {
	for _, end := range []string{"'s", "\u2019s", "'", "\u2019"} {
		if strings.HasSuffix(word, end) && len(word) > len(end) {
			return strings.TrimSuffix(word, end), true
		}
	}
	return word, false
}

// dropDevice reads a trailing device name. It returns the owner's words and true when the owner has at
// least two words; nil and false when the text is a device that names nobody; and the words unchanged and
// true when the text does not end in a device name after a possessive owner.
func dropDevice(tokens []string) ([]string, bool) {
	for size := 2; size >= 1; size-- {
		if len(tokens) < size || !devices[strings.Join(tokens[len(tokens)-size:], " ")] {
			continue
		}
		owner := tokens[:len(tokens)-size]
		if len(owner) == 0 {
			return nil, false
		}
		last, ok := possessive(owner[len(owner)-1])
		if !ok {
			return tokens, true
		}
		if len(owner) < 2 {
			return nil, false
		}
		named := append(append([]string(nil), owner[:len(owner)-1]...), last)
		return named, true
	}
	return tokens, true
}

// Anonymous is whether a label names no one: one of the anonymous words ("Speaker", "Participant",
// "Guest", "Unknown speaker" and the like), in any case, alone or followed by a number ("Speaker 2",
// "Speaker #2", "Speaker_2").
func Anonymous(label string) bool {
	s := u.LowerString(Label(label))
	if anonymous[s] {
		return true
	}
	end := len([]rune(s))
	r := []rune(s)
	for end > 0 && u.IsDecimal(r[end-1]) {
		end--
	}
	if end == len(r) || end == 0 {
		return false
	}
	head := string(r[:end])
	for _, sep := range []string{" #", " ", "#", "_", "-"} {
		if base, ok := strings.CutSuffix(head, sep); ok && anonymous[base] {
			return true
		}
	}
	return anonymous[head]
}

// email is an address in lower case and its domain, or false when text is not an address.
func email(text string) (address, domain string, ok bool) {
	address = u.LowerString(Label(text))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 || strings.Contains(address, " ") {
		return "", "", false
	}
	return address, address[at+1:], true
}

// domain is a domain as the lists hold it: on one line and in lower case.
func domain(text string) string { return u.LowerString(Label(text)) }
