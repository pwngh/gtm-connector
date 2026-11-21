package people_test

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/people"
)

// TestTitlesAreKnownAbbreviations checks that every honorific and suffix a variant drops is one the
// sentence rules in internal/evidence already treat as an abbreviation.
func TestTitlesAreKnownAbbreviations(t *testing.T) {
	for _, list := range people.Titles {
		for word := range list {
			title := []rune(string(rune(word[0])-'a'+'A') + word[1:] + ".")
			if _, ok := evidence.Abbreviation(title, 0, len(title)); !ok {
				t.Errorf("%q is not an abbreviation the sentence rules know", string(title))
			}
		}
	}
}
