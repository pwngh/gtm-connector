// Package speakerrules gives the cards and briefs the oracle's folders recorded as Go makes them under the
// speaker rules. The folders hold cards sealed before them, which stay as sealed so that an old card is
// still admitted. Go makes the same reads, receipts and evidence, and seals the workspace's speaker settings
// in the evidence: none, in a demo workspace. Its text, units and gaps are what evidence.Build makes of
// that evidence, so a speaker's role and basis are the speaker rules', and everything else is the oracle's.
// It is for tests only.
package speakerrules

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Card is a recorded card or prepared brief, as its JSON object, made again under the speaker rules: its
// evidence with the empty people object, and its text, units and gaps built from that evidence. The object
// is changed in place.
func Card(card *strictjson.Object) error {
	value, _ := card.Get("evidence")
	snapshot, ok := value.(*strictjson.Object)
	if !ok {
		return fmt.Errorf("speakerrules: the card has no evidence")
	}
	snapshot.Set("people", evidence.Sources{}.People())
	units, text, gaps, _, err := evidence.Build(snapshot)
	if err != nil {
		return err
	}
	card.Set("text", text)
	card.Set("units", evidence.UnitValues(units))
	card.Set("gaps", evidence.GapValues(gaps))
	return nil
}

// Payload is Card on a card's bytes, encoded again as a card (strictjson.Artifact) or a prepared brief
// (strictjson.Prepared) is.
func Payload(payload []byte) ([]byte, error) {
	value, err := strictjson.Loads(payload)
	card, ok := value.(*strictjson.Object)
	if err != nil || !ok {
		return nil, fmt.Errorf("speakerrules: the card is not a JSON object")
	}
	if err := Card(card); err != nil {
		return nil, err
	}
	options := strictjson.Artifact
	if kind, _ := card.Get("kind"); kind == "selected_brief" {
		options = strictjson.Prepared
	}
	encoded, err := strictjson.Dumps(card, options)
	return []byte(encoded), err
}

// Reply is a recorded tool reply that holds a brief, with the brief's text, units and gaps replaced by the
// rebuilt card's: in the brief object of the result, and in the text content, which is the result indented
// by two as the MCP server writes it.
func Reply(recorded []byte, rebuilt *strictjson.Object) ([]byte, error) {
	value, err := strictjson.Loads(recorded)
	if err != nil {
		return nil, err
	}
	brief, _ := rebuilt.Get("brief_id")
	// walk replaces the brief wherever it is, and says whether it found one.
	var walk func(any) (any, bool, error)
	walk = func(v any) (any, bool, error) {
		found := false
		switch v := v.(type) {
		case *strictjson.Object:
			if id, _ := v.Get("brief_id"); id == brief && has(v, "text", "units", "gaps") {
				for _, key := range []string{"text", "units", "gaps"} {
					member, _ := rebuilt.Get(key)
					v.Set(key, member)
				}
				found = true
			}
			for i, member := range v.Values {
				changed, inside, err := walk(member)
				if err != nil {
					return nil, false, err
				}
				v.Values[i], found = changed, found || inside
			}
		case []any:
			for i, member := range v {
				changed, inside, err := walk(member)
				if err != nil {
					return nil, false, err
				}
				v[i], found = changed, found || inside
			}
		case string:
			inner, err := strictjson.LoadsText(v)
			object, isObject := inner.(*strictjson.Object)
			if err != nil || !isObject {
				return v, false, nil
			}
			if _, inside, err := walk(object); err != nil || !inside {
				return v, false, err
			}
			compact, err := strictjson.Dumps(object, strictjson.Quoted)
			if err != nil {
				return nil, false, err
			}
			var text bytes.Buffer
			if err := json.Indent(&text, []byte(compact), "", "  "); err != nil {
				return nil, false, err
			}
			return text.String(), true, nil
		}
		return v, found, nil
	}
	if value, _, err = walk(value); err != nil {
		return nil, err
	}
	encoded, err := strictjson.Dumps(value, strictjson.Quoted)
	return []byte(encoded), err
}

func has(o *strictjson.Object, keys ...string) bool {
	for _, key := range keys {
		if _, ok := o.Get(key); !ok {
			return false
		}
	}
	return true
}
