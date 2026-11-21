package evidence

import "testing"

// A number a Go source hands over as int64 equals the same number read from JSON.
func TestEqualReadsAnInt64AsANumber(t *testing.T) {
	if !equal(int64(1), 1) || !equal(1, int64(1)) || equal(int64(2), 1) {
		t.Fatal("int64 is not compared as a number")
	}
}
