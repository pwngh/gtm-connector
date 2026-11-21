package people

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
)

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Dana Crest":                 "Dana Crest",
		"  Dana \t Crest\n":          "Dana Crest",
		"Dana\u00a0Crest":            "Dana Crest",
		"Dana\u3000Crest\u2028":      "Dana Crest",
		"Dana\u200bCrest":            "Dana\u200bCrest", // a zero-width space is not a space to str.split
		"\x1c\x1d\x1e\x1fDana\u0085": "Dana",
		"":                           "",
	} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVariant(t *testing.T) {
	for in, want := range map[string]string{
		"Dana Crest":               "dana crest",
		"  DANA   CREST ":          "dana crest",
		"Dana Crest (Acme)":        "dana crest",
		"Dana Crest [Acme Corp]":   "dana crest",
		"Crest, Dana":              "dana crest",
		"Crest, Dana (Acme)":       "dana crest",
		"Dr. Dana Crest":           "dana crest",
		"Mrs Dana Crest":           "dana crest",
		"Dana Crest, Jr.":          "dana crest",
		"Dana Crest Esq":           "dana crest",
		"Crest, Dana, Jr.":         "dana crest",
		"Prof. Crest, Dana":        "dana crest",
		"Crest, Prof. Dana":        "dana crest",
		"Dana Crest's iPhone":      "dana crest",
		"Dana Crest\u2019s iPad":   "dana crest",
		"Dana Crest's MacBook Pro": "dana crest",
		"James Ames' Laptop":       "james ames",
		"Dana's iPhone":            "",
		"iPhone":                   "",
		"Dana Crest iPhone":        "dana crest iphone",
		"Dr.":                      "dr.",
		"\u0394\u0391\u039d\u0391 \u039a\u03a1\u0395\u03a3\u03a4": "\u03b4\u03b1\u03bd\u03b1 \u03ba\u03c1\u03b5\u03c3\u03c4",
		"": "",
	} {
		if got := Variant(in); got != want {
			t.Errorf("Variant(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnonymous(t *testing.T) {
	for in, want := range map[string]bool{
		"Speaker 2":       true,
		"speaker 12":      true,
		"Speaker #3":      true,
		"Speaker_4":       true,
		"SPEAKER":         true,
		"Unknown speaker": true,
		"Guest 1":         true,
		"Participant":     true,
		"Speaker Dana":    false,
		"Dana Crest":      false,
		"2":               false,
		"":                false,
	} {
		if got := Anonymous(in); got != want {
			t.Errorf("Anonymous(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestTitlesAreKnownAbbreviations checks that every honorific and suffix a variant drops is one the
// sentence rules in internal/evidence already treat as an abbreviation.
func TestTitlesAreKnownAbbreviations(t *testing.T) {
	for _, list := range []map[string]bool{honorifics, suffixes} {
		for word := range list {
			title := []rune(string(rune(word[0])-'a'+'A') + word[1:] + ".")
			if _, ok := evidence.Abbreviation(title, 0, len(title)); !ok {
				t.Errorf("%q is not an abbreviation the sentence rules know", string(title))
			}
		}
	}
}
