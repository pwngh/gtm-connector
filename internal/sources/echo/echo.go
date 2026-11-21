// Package echo finds a credential of a read in what the source sent back. A reader refuses such an answer
// whole, so a token that a source returns as a field never becomes a record, part of a request or part of a
// saved sign-in.
//
// Text is compared as a card will later show it. internal/evidence composes source text (NFC) and lowers
// the host of a link, and the Slack reader lowers a workspace address, so text that is not a token can
// become one afterwards: the Kelvin sign composes to the letter K, and a host in capitals is lowered. InText
// therefore looks for a token without regard to case, in the text as given and in its composed form.
package echo

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unique"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// MinLength is the fewest characters a credential may have. A shorter one would be found in ordinary
// answers, a one-letter token in the name of a member, and every answer would be refused.
const MinLength = 20

// Usable reports whether a credential is MinLength to most characters, each a printable ASCII character
// other than a space. A reader refuses any other credential when it is given one. The rule names no
// narrower set, because a service may change what its tokens are made of, and a real token refused here
// would be a sign-in that cannot be used.
func Usable(token string, most int) bool {
	if len(token) < MinLength || len(token) > most {
		return false
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

// Tokens are the credentials one read has held, from its first to its last. One that was replaced during
// the read stays among them until the read ends. Each is kept behind a handle and Tokens prints as
// "[redacted]", so that printing whatever holds them writes out no token.
type Tokens struct {
	held []credential
}

// credential is one token, as it is and lowered.
type credential struct {
	exact, lowered unique.Handle[string]
}

// Format writes the redaction for every verb.
func (Tokens) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[redacted]") }

// Add puts a credential among the tokens. An empty one, or one already there, changes nothing.
func (t *Tokens) Add(token string) {
	if token == "" {
		return
	}
	exact := unique.Make(token)
	for _, already := range t.held {
		if already.exact == exact {
			return
		}
	}
	t.held = append(t.held, credential{exact, unique.Make(unicodetab.LowerString(token))})
}

// InBytes reports whether an answer's bytes hold a token, before anything parses them.
func (t *Tokens) InBytes(body []byte) bool {
	for _, token := range t.held {
		if bytes.Contains(body, []byte(token.exact.Value())) {
			return true
		}
	}
	return false
}

// InText reports whether text holds a token, or would once it is composed or its case changed.
func (t *Tokens) InText(text string) bool {
	if len(t.held) == 0 {
		return false
	}
	forms := []string{text, unicodetab.LowerString(text)}
	if composed := unicodetab.NFCString(text); composed != text {
		forms = append(forms, composed, unicodetab.LowerString(composed))
	}
	for _, token := range t.held {
		for _, form := range forms {
			if strings.Contains(form, token.exact.Value()) || strings.Contains(form, token.lowered.Value()) {
				return true
			}
		}
	}
	return false
}

// InValue reports whether any text of a parsed answer, the name of a member or a value, holds a token as
// InText finds one. It finds a token the JSON wrote with an escape, which the answer's bytes do not show.
func (t *Tokens) InValue(value any) bool {
	switch v := value.(type) {
	case string:
		return t.InText(v)
	case []any:
		for _, item := range v {
			if t.InValue(item) {
				return true
			}
		}
	case *strictjson.Object:
		if v == nil {
			return false
		}
		for i, key := range v.Keys {
			if t.InText(key) || t.InValue(v.Values[i]) {
				return true
			}
		}
	}
	return false
}
