package oracletest

import (
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// manifest.json names every fixture under testdata/oracle and no other file, and states each one's content:
// its SHA-256, its size and, for JSON Lines, its number of lines. An audit database is stated by the digest
// of its rows, which the admit tests check. With -update the entries are written from the files.
func TestManifestStatesEveryFixture(t *testing.T) {
	if os.Getenv("GTM_GOLDEN_CHILD") == "1" {
		t.Skip("the child runs TestGoldenChild only")
	}
	root := Path(t, "")
	var manifest struct {
		Files map[string]map[string]any `json:"files"`
	}
	JSON(t, "manifest.json", &manifest)
	listed := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, _ := filepath.Rel(root, path)
		name = filepath.ToSlash(name)
		if name != "manifest.json" {
			listed[name] = true
			if manifest.Files[name] == nil {
				t.Errorf("manifest.json does not name %s", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	was, err := os.ReadFile(Path(t, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := was
	for _, name := range slices.Sorted(maps.Keys(manifest.Files)) {
		if !listed[name] {
			t.Errorf("manifest.json names %s, which is not there", name)
			continue
		}
		if _, stated := manifest.Files[name]["sha256"]; !stated {
			continue
		}
		if now, err = manifestEntry(now, name, Read(t, name)); err != nil {
			t.Fatal(err)
		}
	}
	if string(now) == string(was) {
		return
	}
	if !Update() {
		t.Fatalf("manifest.json does not state the fixtures' content; run the golden tests with -update (first "+
			"difference: %s)", FirstDifference(string(now), string(was)))
	}
	RefuseNarrowRun(t)
	if err := os.WriteFile(Path(t, "manifest.json"), now, 0o644); err != nil {
		t.Fatal(err)
	}
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
