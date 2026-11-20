package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/realdata"
)

// No test here reaches the network: every https request goes to a proxy that refuses it.
func TestMain(m *testing.M) {
	os.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	os.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	os.Unsetenv("NO_PROXY")
	os.Exit(m.Run())
}

// capture runs the command with its output, and the oracle's, collected.
func capture(t *testing.T, args ...string) (int, string) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = write, write
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(read)
		done <- string(data)
	}()
	code := run(args)
	os.Stdout, os.Stderr = stdout, stderr
	write.Close()
	return code, <-done
}

// oraclePython is the pinned interpreter: $GTM_ORACLE_PYTHON, or .venv/bin/python in the repository.
func oraclePython(t *testing.T) string {
	t.Helper()
	root, err := repository()
	if err != nil {
		t.Fatal(err)
	}
	python := os.Getenv("GTM_ORACLE_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".venv", "bin", "python")
	}
	out, err := exec.Command(python, "-I", "-c", "import sys, unicodedata; print(sys.version.split()[0], unicodedata.unidata_version)").Output()
	if err != nil || strings.TrimSpace(string(out)) != "3.14.6 16.0.0" {
		t.Skip("needs CPython 3.14.6 with Unicode 16.0.0: set GTM_ORACLE_PYTHON")
	}
	return python
}

const secret = "Quarterly renewal blocked by Zanzibar clause"

// A local run end to end: whatever links or files are left in FILE.r0, nothing is written outside it, every
// file in it is private, and no output names the text.
func TestLocalEndToEnd(t *testing.T) {
	python := oraclePython(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	lines := `{"id": "m1", "text": "We agree. ` + secret + ` until Friday."}` + "\n" +
		`{"id": "m2", "text": "<@U0ABC123> see https://deals.example/r?id=7 ` + secret + `"}` + "\n"
	if err := os.WriteFile(input, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	out := input + ".r0"
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oracle.jsonl.gz", "oracle-digests.jsonl.gz", "oracle.json", "report.json"} {
		if err := os.Symlink(sentinel, filepath.Join(out, name+".partial")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(sentinel, filepath.Join(out, "oracle.jsonl.gz")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "report.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before := listing(t, dir, out)
	code, printed := capture(t, "-python", python, "-local", input, "verify")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, printed)
	}
	if data, _ := os.ReadFile(sentinel); string(data) != "untouched" {
		t.Errorf("the file outside was written: %q", data)
	}
	if after := listing(t, dir, out); after != before {
		t.Errorf("files beside the results folder changed:\n%s\n%s", before, after)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(e.Name(), ".partial") {
			continue // the links left before the run
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", e.Name(), info.Mode())
		}
	}
	report, _ := os.ReadFile(filepath.Join(out, "report.json"))
	for _, word := range []string{"Zanzibar", "Quarterly", "deals.example", "m1", "m2"} {
		if strings.Contains(printed, word) || strings.Contains(string(report), word) {
			t.Errorf("%q is in the output or the report", word)
		}
	}
}

// listing names the files of a folder, other than one folder in it, with their sizes.
func listing(t *testing.T, dir, skip string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if path == skip {
			return filepath.SkipDir
		}
		if err == nil {
			b.WriteString(path + " " + info.Mode().String() + "\n")
		}
		return nil
	})
	return b.String()
}

// A dataset named for verify that cannot be compared is a failure of its own, before anything is fetched.
func TestVerifyNamedDatasetThatCannotRun(t *testing.T) {
	t.Setenv("GTM_REAL_CACHE", t.TempDir())
	for name, want := range map[string]string{
		"fomc-press-conferences": "no converter",
		"icsi-meetings":          "needs the owner",
	} {
		code, printed := capture(t, "-python", "", "verify", name)
		if code == 0 || !strings.Contains(printed, want) {
			t.Errorf("verify %s: exit %d:\n%s", name, code, printed)
		}
	}
}

// The cache is refused inside the repository, however the path reaches it.
func TestCacheOutsideRepository(t *testing.T) {
	root, err := repository()
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "build", "realdata-test-cache")
	t.Cleanup(func() { os.RemoveAll(inside) })
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	spellings := []string{inside, filepath.Join(link, "build", "realdata-test-cache")}
	if upper := strings.ToUpper(root); upper != root {
		if _, err := os.Stat(upper); err == nil {
			spellings = append(spellings, filepath.Join(upper, "build", "realdata-test-cache"))
		}
	}
	for _, cache := range spellings {
		t.Setenv("GTM_REAL_CACHE", cache)
		code, printed := capture(t, "-python", "", "fetch", "lwc-recipes")
		if code == 0 || !strings.Contains(printed, "inside the repository") {
			t.Errorf("GTM_REAL_CACHE=%s: exit %d:\n%s", cache, code, printed)
		}
		if _, err := os.Stat(inside); err == nil {
			t.Errorf("GTM_REAL_CACHE=%s: the cache was made inside the repository", cache)
			os.RemoveAll(inside)
		}
	}
}

// A panic is reported by its type alone, with exit 2: its value could hold an item's text.
func TestPanicShowsTypeOnly(t *testing.T) {
	saved := dispatchCommand
	dispatchCommand = func(context.Context, string, []string, string, string) (int, error) {
		panic(fmt.Errorf("while reading %q", secret))
	}
	defer func() { dispatchCommand = saved }()
	code, printed := capture(t, "verify")
	if code != 2 || !strings.Contains(printed, "internal error") || strings.Contains(printed, "Zanzibar") {
		t.Errorf("exit %d:\n%s", code, printed)
	}
}

// -local runs one private file through verify or compare, and nothing else.
func TestLocalRefusesOtherCommands(t *testing.T) {
	input := filepath.Join(t.TempDir(), "messages.jsonl")
	os.WriteFile(input, []byte(`{"id": "a", "text": "x"}`+"\n"), 0o600)
	for _, args := range [][]string{{"-local", input, "verify", "ami-manual-1.6.2"}, {"-local", input, "fetch"},
		{"-local", input, "pin"}} {
		code, printed := capture(t, append([]string{"-python", ""}, args...)...)
		if code != 2 || !strings.Contains(printed, "-local") {
			t.Errorf("%v: exit %d:\n%s", args, code, printed)
		}
	}
}

// A run that fails does not stop the others: each is compared and reported, and the command fails.
func TestCompareGoesOnAfterAFailure(t *testing.T) {
	python := oraclePython(t)
	dir := t.TempDir()
	broken, fine := filepath.Join(dir, "broken.jsonl"), filepath.Join(dir, "fine.jsonl")
	os.WriteFile(broken, []byte(`{"id": "a", "text": "one"}`+"\n"), 0o600)
	os.WriteFile(fine, []byte(`{"id": "a", "text": "We agree. They sign."}`+"\n"), 0o600)
	root, err := repository()
	if err != nil {
		t.Fatal(err)
	}
	if code, printed := capture(t, "-python", python, "-local", fine, "verify"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, printed)
	}
	runs := []realdata.Run{}
	for _, input := range []string{broken, fine} {
		r, err := realdata.LocalRun(input, root)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	read, write, _ := os.Pipe()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = write, write
	_, err = compare(runs)
	os.Stdout, os.Stderr = stdout, stderr
	write.Close()
	printed, _ := io.ReadAll(read)
	if err == nil || !strings.Contains(string(printed), "no differences") || !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("%v:\n%s", err, printed)
	}
}
