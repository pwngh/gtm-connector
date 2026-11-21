package strictjson

import (
	"errors"
	"strings"
	"testing"
)

// The limit is 1,000 levels, stated here on its own so that a change to MaxDepth fails a test: a document
// 1,000 deep is read and one 1,001 deep is refused, whatever the constant says.
func TestTheNestingLimitIsOneThousand(t *testing.T) {
	if MaxDepth != 1000 {
		t.Fatalf("MaxDepth is %d", MaxDepth)
	}
	if _, err := Loads([]byte(strings.Repeat("[", 1000) + strings.Repeat("]", 1000))); err != nil {
		t.Fatalf("1,000 levels: %v", err)
	}
	if _, err := Loads([]byte(strings.Repeat("[", 1001) + strings.Repeat("]", 1001))); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("1,001 levels: %v", err)
	}
}
