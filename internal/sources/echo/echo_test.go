package echo

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

const token = "invented-Kelvin-echo-token"

// kelvin is the token with the Kelvin sign where it has the letter K. Composing it gives the token.
var kelvin = strings.Replace(token, "K", string(rune(0x212A)), 1)

func held(tokens ...string) *Tokens {
	t := &Tokens{}
	for _, token := range tokens {
		t.Add(token)
	}
	return t
}

// Text is searched as a card will show it: whatever evidence.SafeText turns into the token is found before
// it gets there.
func TestTextACardWouldTurnIntoATokenIsFound(t *testing.T) {
	tokens := held(token)
	for name, text := range map[string]string{
		"the token":                     token,
		"the token inside a sentence":   "It said " + token + " aloud.",
		"a Kelvin sign for the K":       kelvin,
		"capitals":                      "INVENTED-KELVIN-ECHO-TOKEN",
		"small letters":                 "invented-kelvin-echo-token",
		"a Kelvin sign and capitals":    strings.Replace("INVENTED-KELVIN-ECHO-TOKEN", "K", string(rune(0x212A)), 1),
		"the host of a link":            "See https://INVENTED-KELVIN-ECHO-TOKEN/page for more.",
		"a combining mark after it":     token + string(rune(0x301)),
		"a combining mark after a copy": "INVENTED-KELVIN-ECHO-TOKEN" + string(rune(0x301)),
	} {
		if !tokens.InText(text) {
			t.Errorf("%s: not found", name)
		}
	}
	// Why these are looked for: neither text below holds its token, and what a card shows of it does. A
	// card composes the Kelvin sign into the letter K, and lowers the host of a link.
	const lower = "invented-lower-echo-token"
	for text, token := range map[string]string{
		kelvin: token,
		"See https://INVENTED-LOWER-ECHO-TOKEN/page for more.": lower,
	} {
		shown, _, err := evidence.SafeText(text)
		if err != nil || strings.Contains(text, token) || !strings.Contains(shown, token) {
			t.Errorf("a card does not turn the text into the token: %v", err)
		}
		if !held(token).InText(text) {
			t.Error("text a card turns into the token was not found")
		}
	}
	for name, text := range map[string]string{
		"other text":             "A quoted source statement.",
		"the token cut short":    token[:len(token)-1],
		"the token with a space": strings.Replace(token, "-", " ", 1),
		"nothing":                "",
	} {
		if tokens.InText(text) {
			t.Errorf("%s: found", name)
		}
	}
	if (&Tokens{}).InText(token) || (&Tokens{}).InBytes([]byte(token)) || held("").InText("") {
		t.Error("no token was held, and one was found")
	}
}

// Every token added stays, and each is looked for in bytes, in the names of members and in values.
func TestEveryTokenHeldIsLookedFor(t *testing.T) {
	const first, second = "invented-first-echo-token", "invented-second-echo-token"
	tokens := held(first, second, first)
	if len(tokens.held) != 2 {
		t.Fatalf("%d tokens are held", len(tokens.held))
	}
	// Printing the tokens, or a struct that keeps them in an unexported field, writes out none of them.
	printed := fmt.Sprintf("%v %+v %#v %s %q %+v %#v", tokens, tokens, *tokens, tokens, *tokens,
		struct{ kept Tokens }{*tokens}, struct{ kept *Tokens }{tokens})
	if strings.Contains(printed, first) || strings.Contains(printed, second) || !strings.Contains(printed, "[redacted]") {
		t.Fatal("the tokens were printed")
	}
	for _, body := range []string{`{"text":"` + first + `"}`, `{"` + second + `":1}`, `not JSON ` + second} {
		if !tokens.InBytes([]byte(body)) {
			t.Errorf("not found in %d bytes", len(body))
		}
	}
	if tokens.InBytes([]byte(`{"ok":true,"messages":[]}`)) {
		t.Error("found in an answer that holds none")
	}
	upper := strings.Replace("INVENTED-SECOND-ECHO-TOKEN", "K", string(rune(0x212A)), 1)
	for name, value := range map[string]any{
		"a value":          strictjson.NewObject("text", first),
		"a name":           strictjson.NewObject(second, 1),
		"a nested value":   strictjson.NewObject("rows", []any{strictjson.NewObject("deep", []any{nil, 1, upper})}),
		"a list":           []any{"other", second},
		"text of its own":  first,
		"a copy in a name": strictjson.NewObject(upper, nil),
	} {
		if !tokens.InValue(value) {
			t.Errorf("%s: not found", name)
		}
	}
	var none *strictjson.Object
	for name, value := range map[string]any{
		"an answer that holds none": strictjson.NewObject("ok", true, "messages", []any{"other"}, "count", 3),
		"a nil object":              none,
		"nothing":                   nil,
	} {
		if tokens.InValue(value) {
			t.Errorf("%s: found", name)
		}
	}
}

// A text the token rule refuses is never held: a fragment such as "e", read from a token file while it was
// written, would otherwise be found in nearly every answer for as long as the tokens live.
func TestAFragmentIsNotHeld(t *testing.T) {
	tokens := held("e", "", strings.Repeat("x", MinLength-1), "invented token with spaces 0123", token)
	for _, text := range []string{"every answer has an e", strings.Repeat("x", 40), "invented token with spaces 0123"} {
		if tokens.InText(text) || tokens.InBytes([]byte(text)) {
			t.Errorf("a refused text was held, and %q is refused", text)
		}
	}
	if !tokens.InText(token) {
		t.Error("the usable token beside them was not held")
	}
}

// A credential is refused when an answer could hold it by chance, or when it holds a space, a control
// character or anything outside ASCII. Any printable ASCII mark is allowed, because a service may change
// what its tokens are made of.
func TestACredentialMustBeLongAndPlain(t *testing.T) {
	twenty := strings.Repeat("x", MinLength)
	for token, want := range map[string]bool{
		twenty: true, "Abc123._-" + twenty: true, strings.Repeat("x", 64): true,
		twenty + "!": true, twenty + `"`: true, twenty + "{": true, twenty + ":": true, twenty + "~": true,
		"": false, "o": false, "ok": false, "records": false, twenty[1:]: false, strings.Repeat("x", 65): false,
		twenty + " ": false, twenty + "\t": false, twenty + "\x7f": false, twenty + "é": false,
		twenty + "\x00": false,
	} {
		if got := Usable(token, 64); got != want {
			t.Errorf("a credential of %d bytes: usable %t, want %t", len(token), got, want)
		}
	}
}
