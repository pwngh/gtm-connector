package realdata

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The functions R0 compares, in the order the oracle recorded them. Each output is a JSON value as the
// oracle fixtures record it:
//   - safe_text: [text, [mentions, links, controls, brackets]]
//   - sentences, sentences_lines: units.sentences(text) without and with lines=True, as corpora/sentences
//     records them; sentences_safe, sentences_safe_lines: the same of safe_text's text, as cards split it
//   - each scanner, as scanners/*.jsonl.gz records it
//   - strict_json, strict_json_decimal: {"ok": json.dumps(value, ensure_ascii=True)} or
//     {"error": type, "message": message}, without and with parse_float=Decimal, a Decimal given as
//     {"decimal": str(value)}, as library/strict_json.json records it
//
// A function that raises in Python gives {"error": type}. One that fails in Go gives {"error": "go error"}
// or {"error": "go panic"}, which never equal a Python output, so they count as differences.
var (
	TextFunctions = []string{"safe_text", "sentences", "sentences_lines", "sentences_safe", "sentences_safe_lines",
		"units_target", "units_raw_link", "units_sentence_end", "units_opener", "units_named",
		"slack_mapping_percent", "slack_callback_percent"}
	JSONFunctions = []string{"strict_json", "strict_json_decimal"}
)

// Functions are the functions that run on an item.
func Functions(it Item) []string {
	if it.IsJSON {
		return JSONFunctions
	}
	return TextFunctions
}

// canonical is the encoding outputs are compared and digested in: json.dumps(value, ensure_ascii=True,
// separators=(",", ":")), as the oracle wrote its outputs.
func canonical(value any) (string, error) {
	return strictjson.Dumps(value, strictjson.DumpOptions{EnsureASCII: true, Compact: true})
}

// Digest is the SHA-256 of an output's canonical encoding.
func Digest(encoded string) string {
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

// Outputs runs the Go port on one item: each function's output in its canonical encoding, in order. A panic
// is caught and recorded without its value, which may hold the item's text.
func Outputs(it Item) []string {
	var values []any
	if it.IsJSON {
		values = []any{
			guard(func() (any, error) { return parsed(strictjson.Loads(it.JSON)) }),
			guard(func() (any, error) { return parsed(strictjson.LoadsDecimal(it.JSON)) }),
		}
	} else {
		values = textOutputs(it.Text)
	}
	encoded := make([]string, len(values))
	for i, value := range values {
		text, err := canonical(value)
		if err != nil {
			text, _ = canonical(failed("go error"))
		}
		encoded[i] = text
	}
	return encoded
}

func textOutputs(text string) []any {
	s := []rune(text)
	var safe *string
	values := []any{
		guard(func() (any, error) {
			out, counts, err := evidence.SafeText(text)
			if err != nil {
				return nil, err
			}
			safe = &out
			return []any{out, []any{counts.Mentions, counts.Links, counts.Controls, counts.Brackets}}, nil
		}),
		guard(func() (any, error) { return evidence.Sentences(text, false) }),
		guard(func() (any, error) { return evidence.Sentences(text, true) }),
	}
	for _, lines := range []bool{false, true} {
		values = append(values, guard(func() (any, error) {
			if safe == nil {
				return nil, errors.New("no safe_text")
			}
			return evidence.Sentences(*safe, lines)
		}))
	}
	return append(values,
		guard(func() (any, error) {
			if end, ok := evidence.Target(s); ok {
				return end, nil
			}
			return nil, nil
		}),
		guard(func() (any, error) { return spans(evidence.RawLinks(s)), nil }),
		guard(func() (any, error) {
			out := []any{}
			for _, m := range evidence.SentenceEnds(s) {
				out = append(out, []any{m.Start, m.End1, m.End2})
			}
			return out, nil
		}),
		guard(func() (any, error) {
			out := []any{}
			for at := 0; at <= len(s); at++ {
				if end, ok := evidence.Opener(s, at, len(s)); ok {
					out = append(out, []any{at, end})
				}
			}
			return out, nil
		}),
		guard(func() (any, error) {
			if m, ok := evidence.NamedMatch(s); ok {
				return []any{m.Start, m.NameEnd, m.End}, nil
			}
			return nil, nil
		}),
		// Both percent patterns are the same pattern; Go has one scanner for them.
		guard(func() (any, error) { return spans(evidence.Percents(s)), nil }),
		guard(func() (any, error) { return spans(evidence.Percents(s)), nil }),
	)
}

func spans(found []evidence.Span) []any {
	out := []any{}
	for _, s := range found {
		out = append(out, []any{s.Start, s.End})
	}
	return out
}

// parsed is a strict_json outcome: the value as json.dumps(value, ensure_ascii=True) writes it, or the refusal.
func parsed(value any, err error) (any, error) {
	if err != nil {
		var refusal *strictjson.Error
		if !errors.As(err, &refusal) {
			return nil, err
		}
		return strictjson.NewObject("error", refusal.Type, "message", refusal.Message), nil
	}
	text, err := strictjson.Dumps(decimals(value), strictjson.DumpOptions{EnsureASCII: true})
	if err != nil {
		return nil, err
	}
	return strictjson.NewObject("ok", text), nil
}

// decimals gives each Decimal as {"decimal": str(value)}, as the oracle's json.dumps default= writes it.
func decimals(value any) any {
	switch v := value.(type) {
	case strictjson.Decimal:
		return strictjson.NewObject("decimal", v.String())
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = decimals(item)
		}
		return out
	case *strictjson.Object:
		out := &strictjson.Object{Keys: v.Keys, Values: make([]any, len(v.Values))}
		for i, item := range v.Values {
			out.Values[i] = decimals(item)
		}
		return out
	}
	return value
}

func failed(kind string) *strictjson.Object { return strictjson.NewObject("error", kind) }

func guard(run func() (any, error)) (value any) {
	defer func() {
		if recover() != nil {
			value = failed("go panic")
		}
	}()
	out, err := run()
	if err != nil {
		return failed("go error")
	}
	return out
}
