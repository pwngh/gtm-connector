package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
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

// writeRecords writes, in FILE.r0, the oracle's records of a local file as an earlier run left them: Go's own
// digests of each item, which the oracle's equal when the two agree, and oracle.json.
func writeRecords(t *testing.T, input string) {
	t.Helper()
	items, err := realdata.OpenItems(input, true)
	if err != nil {
		t.Fatal(err)
	}
	defer items.Close()
	var rows bytes.Buffer
	count := 0
	for {
		it, err := items.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sums := map[string]string{}
		for i, output := range realdata.Outputs(it) {
			sums[realdata.Functions(it)[i]] = realdata.Digest(output)
		}
		row, _ := json.Marshal([]any{it.Line, it.InputSHA256(), sums})
		rows.Write(append(row, '\n'))
		count++
	}
	var frozen struct {
		Package string `json:"package_sha256"`
	}
	oracletest.JSON(t, "manifest.json", &frozen)
	meta, _ := json.Marshal(realdata.OracleMeta{Python: "3.14.6", Unicode: "16.0.0", PackageSHA256: frozen.Package,
		ItemsSHA256: items.SHA256(), Items: count, TextFunctions: realdata.TextFunctions,
		JSONFunctions: realdata.JSONFunctions})
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	w.Write(rows.Bytes())
	w.Close()
	out := input + ".r0"
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"oracle-digests.jsonl.gz": zipped.Bytes(), "oracle.json": meta} {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const secret = "Quarterly renewal blocked by Zanzibar clause"

// A local run end to end: compare reads the records an earlier run left in FILE.r0, writes its report there
// as a private file through no link, writes nothing outside, and no output names the text.
func TestLocalEndToEnd(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	lines := `{"id": "m1", "text": "We agree. ` + secret + ` until Friday."}` + "\n" +
		`{"id": "m2", "text": "<@U0ABC123> see https://deals.example/r?id=7 ` + secret + `"}` + "\n"
	if err := os.WriteFile(input, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, input)
	out := input + ".r0"
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(out, "report.json")); err != nil {
		t.Fatal(err)
	}
	before := listing(t, dir, out)
	code, printed := capture(t, "-local", input, "verify")
	if code != 0 || !strings.Contains(printed, "no differences") {
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
		code, printed := capture(t, "verify", name)
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
		code, printed := capture(t, "fetch", "lwc-recipes")
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
	dispatchCommand = func(context.Context, string, []string, string, []string) (int, error) {
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
		code, printed := capture(t, args...)
		if code != 2 || !strings.Contains(printed, "-local") {
			t.Errorf("%v: exit %d:\n%s", args, code, printed)
		}
	}
}

// A run that fails does not stop the others: each is compared and reported, and the command fails.
func TestCompareGoesOnAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	broken, fine := filepath.Join(dir, "broken.jsonl"), filepath.Join(dir, "fine.jsonl")
	os.WriteFile(broken, []byte(`{"id": "a", "text": "one"}`+"\n"), 0o600)
	os.WriteFile(fine, []byte(`{"id": "a", "text": "We agree. They sign."}`+"\n"), 0o600)
	writeRecords(t, fine)
	root, err := repository()
	if err != nil {
		t.Fatal(err)
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
	_, err = state{}.compare(runs)
	os.Stdout, os.Stderr = stdout, stderr
	write.Close()
	printed, _ := io.ReadAll(read)
	if err == nil || !strings.Contains(string(printed), "no differences") || !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("%v:\n%s", err, printed)
	}
}
