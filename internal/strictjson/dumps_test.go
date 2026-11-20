package strictjson

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

func TestDumpsOracle(t *testing.T) {
	var fixture struct {
		Cases []map[string]json.RawMessage `json:"cases"`
	}
	oracletest.JSON(t, "library/json_dumps.json", &fixture)
	variants := map[string]DumpOptions{
		"default":     {EnsureASCII: true, AllowNaN: true},
		"ascii_false": {AllowNaN: true},
		"audit":       Quoted,
		"artifact":    Artifact,
		"cli":         {Indent: 2, AllowNaN: true},
	}
	nonFinite := map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "-inf": math.Inf(-1)}
	mismatches := oracletest.NewMismatches(t, "library/json_dumps.json")
	for _, c := range fixture.Cases {
		value, err := Loads(c["input"])
		if err != nil {
			t.Fatalf("%s: %v", c["input"], err)
		}
		// A non-finite float is recorded as its repr, and refused where NaN is not allowed.
		var audit outcome
		if err := json.Unmarshal(c["audit"], &audit); err != nil {
			t.Fatal(err)
		}
		if text, ok := value.(string); ok && audit.OK == nil {
			value = nonFinite[text]
		}
		for name, options := range variants {
			mismatches.Row()
			var want outcome
			if err := json.Unmarshal(c[name], &want); err != nil {
				t.Fatal(err)
			}
			got, err := Dumps(value, options)
			switch {
			case want.OK != nil && (err != nil || got != *want.OK):
				mismatches.Add("%s %s: got %q, %v; want %q", c["input"], name, got, err, *want.OK)
			case want.OK == nil && (err == nil || err.Error() != want.Error+": "+want.Message):
				mismatches.Add("%s %s: got %q, %v; want %s: %s", c["input"], name, got, err, want.Error, want.Message)
			}
		}
	}
}

func TestDumpsPreparedVariant(t *testing.T) {
	value := NewObject("b", []any{1, "é"}, "a", NewObject("z", nil, "y", true), "c", 0.0)
	got, err := Dumps(value, Prepared)
	if want := `{"a":{"y":true,"z":null},"b":[1,"é"],"c":0.0}`; err != nil || got != want {
		t.Fatalf("got %s, %v; want %s", got, err, want)
	}
}
