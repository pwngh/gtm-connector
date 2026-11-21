package slack

import (
	"errors"
	"strings"
	"testing"
)

// A message over any one of Slack's limits is refused, never cut; one at each limit passes.
func TestAMessageOverALimitIsRefused(t *testing.T) {
	text := func(n int) Text { return mrkdwn(strings.Repeat("x", n)) }
	header := func(n int) Block { h := plain(strings.Repeat("x", n)); return Block{Type: "header", Text: &h} }
	sectionOf := func(n int) Block { s := text(n); return Block{Type: "section", Text: &s} }
	repeat := func(b Block, n int) []Block {
		out := make([]Block, n)
		for i := range out {
			out[i] = b
		}
		return out
	}
	fields := func(count, size int) Block {
		b := Block{Type: "section"}
		for range count {
			b.Fields = append(b.Fields, text(size))
		}
		return b
	}
	elements := func(count int) Block {
		b := Block{Type: "context"}
		for range count {
			b.Elements = append(b.Elements, text(1))
		}
		return b
	}
	for name, c := range map[string]struct {
		notification string
		blocks       []Block
		ok           bool
	}{
		"50 blocks":             {"", repeat(Block{Type: "divider"}, MaxBlocks), true},
		"51 blocks":             {"", repeat(Block{Type: "divider"}, MaxBlocks+1), false},
		"a header of 150":       {"", []Block{header(MaxHeader)}, true},
		"a header of 151":       {"", []Block{header(MaxHeader + 1)}, false},
		"a section of 3,000":    {"", []Block{sectionOf(MaxText)}, true},
		"a section of 3,001":    {"", []Block{sectionOf(MaxText + 1)}, false},
		"an empty section":      {"", []Block{sectionOf(0)}, false},
		"10 fields of 2,000":    {"", []Block{fields(MaxFields, MaxField)}, true},
		"11 fields":             {"", []Block{fields(MaxFields+1, 1)}, false},
		"a field of 2,001":      {"", []Block{fields(1, MaxField+1)}, false},
		"10 context elements":   {"", []Block{elements(MaxElements)}, true},
		"11 context elements":   {"", []Block{elements(MaxElements + 1)}, false},
		"& counted as 5":        {"", []Block{{Type: "section", Text: &Text{Type: "mrkdwn", Text: strings.Repeat("&", MaxText/5+1)}}}, false},
		"a notification of 40k": {strings.Repeat("x", MaxMessageText+1), nil, false},
	} {
		err := check(c.notification, c.blocks)
		if c.ok && err != nil || !c.ok && !errors.Is(err, ErrLimit) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
