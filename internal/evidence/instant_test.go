package evidence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// isoformat is datetime.isoformat() of an aware UTC time.
func isoformat(moment string) string {
	return strings.Replace(strings.Replace(moment, "Z", "+00:00", 1), ".000000+", "+", 1)
}

func TestInstantAndDayOracle(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Input   string             `json:"input"`
			Instant map[string]*string `json:"units._instant"`
			Day     map[string]*string `json:"units._day"`
		} `json:"cases"`
	}
	oracletest.JSON(t, "library/datetime.json", &fixture)
	mismatches := oracletest.NewMismatches(t, "library/datetime.json")
	for _, c := range fixture.Cases {
		mismatches.Row()
		want := c.Instant["ok"]
		moment, ok := Instant(c.Input)
		got := (*string)(nil)
		if ok {
			text := isoformat(moment.Format("2006-01-02T15:04:05.000000Z07:00"))
			got = &text
		}
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			mismatches.Add("_instant(%q): got %v, want %v", c.Input, shown(got), shown(want))
		}
		day, err := Day(c.Input)
		wantDay, dayOK := c.Day["ok"]
		switch {
		case dayOK && (err != nil || day.Format("2006-01-02") != *wantDay):
			mismatches.Add("_day(%q): got %v %v, want %s", c.Input, day, err, *wantDay)
		case !dayOK && err == nil:
			mismatches.Add("_day(%q): got %v, want a refusal", c.Input, day)
		}
	}
}

func shown(s *string) string {
	if s == nil {
		return "None"
	}
	data, _ := json.Marshal(*s)
	return string(data)
}

func TestPlainInstant(t *testing.T) {
	for text, want := range map[string]bool{
		"2027-01-15T15:00:00Z": true, "2027-01-15T15:00:00.123456Z": true, "2027-01-15T15:00:00.1234567Z": false,
		"2027-01-15T15:00:00.Z": false, "2027-01-15T15:00:00+23:59": true, "2027-01-15T15:00:00+24:00": false,
		"2027-01-15T15:00:00-05:60": false, "2027-01-15T15:00:00": false, "2027-01-15 15:00:00Z": false,
		"２027-01-15T15:00:00Z": false, "2027-01-15T15:00:00Zx": false, "2027-01-15T15:00:00+0500": false,
	} {
		if PlainInstant(text) != want {
			t.Errorf("PlainInstant(%q) = %v", text, !want)
		}
	}
}
