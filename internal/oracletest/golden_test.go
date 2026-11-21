package oracletest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestGoldenChild opens a golden file the way an updating test does. It does something only when
// TestUpdateRefusesANarrowRun runs this test binary as a child.
func TestGoldenChild(t *testing.T) {
	if os.Getenv("GTM_GOLDEN_CHILD") != "1" {
		t.Skip("run by TestUpdateRefusesANarrowRun")
	}
	NewGolden(t, "commands/session.jsonl", func(any) (string, error) { return "", nil })
}

// -update writes golden files only in a run of the whole package: a run narrowed by -run or -skip fails
// before it reads or writes one.
func TestUpdateRefusesANarrowRun(t *testing.T) {
	if os.Getenv("GTM_GOLDEN_CHILD") == "1" {
		t.Skip("the child runs TestGoldenChild only")
	}
	for _, c := range []struct {
		args    []string
		refused string
	}{
		{[]string{"-test.run=TestGoldenChild", "-update"}, "drop -run"},
		{[]string{"-test.skip=TestUpdateRefusesANarrowRun", "-update"}, "drop -skip"},
		{[]string{"-update"}, ""},
	} {
		child := exec.Command(os.Args[0], append(c.args, "-test.count=1")...)
		child.Env = append(os.Environ(), "GTM_GOLDEN_CHILD=1")
		out, err := child.CombinedOutput()
		if c.refused == "" && err != nil {
			t.Errorf("%v: a whole run is refused: %v\n%s", c.args, err, out)
		}
		if c.refused != "" && (err == nil || !strings.Contains(string(out), c.refused)) {
			t.Errorf("%v: a narrow run is not refused: %v\n%s", c.args, err, out)
		}
	}
}
