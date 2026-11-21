package strictjson

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type entry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type document struct {
	Format  string   `json:"format"`
	Version int      `json:"version"`
	Live    bool     `json:"live"`
	Names   []string `json:"names"`
	Entries []entry  `json:"entries,omitempty"`
	First   entry    `json:"first"`
	Skipped string   `json:"-"`
	Untaken string
}

// Decode fills a struct from the members its tags name, and refuses what encoding/json lets through: a
// member named twice, a member no field names, a name in another case, a value of another type, and null
// where there is no empty slice to mean.
func TestDecode(t *testing.T) {
	var got document
	err := Decode([]byte(`{"version": 1, "format": "f", "live": true, "names": ["a", "b"],
		"entries": [{"name": "x", "size": 9007199254740993}, {}], "first": {"size": 2}}`), &got)
	want := document{Format: "f", Version: 1, Live: true, Names: []string{"a", "b"},
		Entries: []entry{{"x", 9007199254740993}, {}}, First: entry{Size: 2}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	got = document{Names: []string{"kept"}}
	if err := Decode([]byte(`{"names": null, "entries": []}`), &got); err != nil || got.Names != nil ||
		got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("null and empty slices: %+v, %v", got, err)
	}
	var list []string
	if err := Decode([]byte(`["a", "b"]`), &list); err != nil || !reflect.DeepEqual(list, []string{"a", "b"}) {
		t.Fatalf("a list: %q, %v", list, err)
	}
	for body, refusal := range map[string]error{
		`{"format": "a", "format": "b"}`:          nil, // Loads's own refusal
		`{"format": "a", "other": 1}`:             ErrUnknownMember,
		`{"Format": "a"}`:                         ErrUnknownMember,
		`{"FORMAT": "a"}`:                         ErrUnknownMember,
		`{"Skipped": "a"}`:                        ErrUnknownMember,
		`{"-": "a"}`:                              ErrUnknownMember,
		`{"Untaken": "a"}`:                        ErrUnknownMember,
		`{"first": {"name": "a", "extra": true}}`: ErrUnknownMember,
		`{"format": null}`:                        ErrWrongType,
		`{"format": 1}`:                           ErrWrongType,
		`{"version": "1"}`:                        ErrWrongType,
		`{"version": 1.0}`:                        ErrWrongType,
		`{"version": 1e2}`:                        ErrWrongType,
		`{"version": null}`:                       ErrWrongType,
		`{"version": 9223372036854775808}`:        ErrWrongType,
		`{"live": 1}`:                             ErrWrongType,
		`{"names": "a"}`:                          ErrWrongType,
		`{"names": ["a", null]}`:                  ErrWrongType,
		`{"names": ["a", 1]}`:                     ErrWrongType,
		`{"first": []}`:                           ErrWrongType,
		`{"first": null}`:                         ErrWrongType,
		`{"entries": [{"size": "1"}]}`:            ErrWrongType,
		`[]`:                                      ErrWrongType,
		`null`:                                    ErrWrongType,
		`{} {}`:                                   nil,
		`{"format": "\ud800"}`:                    nil,
		"{\"format\": \"\xff\"}":                  nil,
		`{"names": ` + strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1) + `}`: ErrTooDeep,
	} {
		var into document
		err := Decode([]byte(body), &into)
		var parser *Error
		if err == nil || refusal != nil && !errors.Is(err, refusal) || refusal == nil && !errors.As(err, &parser) {
			t.Errorf("%.60s: got %v, want %v", body, err, refusal)
		}
	}
	for _, body := range []string{`["a", null]`, `[1]`, `{}`, `"a"`} {
		if err := Decode([]byte(body), &list); !errors.Is(err, ErrWrongType) {
			t.Errorf("%s into a list of strings: %v", body, err)
		}
	}
}
