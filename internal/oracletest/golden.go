package oracletest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write the golden files in testdata/oracle again from what Go makes now")

// Update is whether the test run was given -update: a test that checks a golden file then writes it again
// from what Go makes, instead of failing where they differ.
func Update() bool { return *update }

// RefuseNarrowRun fails a test that would write a golden file under -update in a run narrowed by -run or
// -skip: a golden file is written only by a run of every test that checks it.
func RefuseNarrowRun(t testing.TB) {
	t.Helper()
	for _, name := range []string{"test.run", "test.skip"} {
		if f := flag.Lookup(name); Update() && f != nil && f.Value.String() != "" {
			t.Fatalf("-update writes golden files only in a run of the whole package; drop -%s", name[len("test."):])
		}
	}
}

var (
	recordedRules = regexp.MustCompile(`(\\?"(?:rules|sanitizer)_version\\?":\s*)3\b`)
	recordedUnit  = regexp.MustCompile(`(\\?")partial(\\?":\s*)(true|false),((?:\\n)?\s*)`)
)

// TodaysRules is a card, a brief or a reply that holds one, recorded under rules version 3, as Go writes it
// under version: the version it states, and each sentence unit's quoted member, false, after its partial
// member. It reads the JSON as written, and as a JSON string holds it. The recorded folders keep the cards
// as sealed; this is what Go makes of the same inputs, where the rules since change nothing else.
func TodaysRules(recorded []byte, version int) []byte {
	out := recordedRules.ReplaceAll(recorded, []byte(fmt.Sprintf("${1}%d", version)))
	return recordedUnit.ReplaceAll(out, []byte("${1}partial${2}${3},${4}${1}quoted${2}false,${4}"))
}

// Encoder writes one fixture value in the file's own encoding.
type Encoder func(value any) (string, error)

// Golden is one JSON Lines fixture that a test may write again under -update.
type Golden struct {
	t       testing.TB
	name    string
	encode  Encoder
	lines   [][]byte
	changed int
}

// NewGolden reads a JSON Lines fixture. Under -update, the lines a test changed with Set are written back
// when the test ends, and the file's entry in manifest.json with them.
func NewGolden(t testing.TB, name string, encode Encoder) *Golden {
	t.Helper()
	RefuseNarrowRun(t)
	g := &Golden{t: t, name: name, encode: encode, lines: Lines(t, name)}
	t.Cleanup(func() {
		if g.changed == 0 || t.Failed() {
			return
		}
		var b bytes.Buffer
		for _, line := range g.lines {
			b.Write(line)
			b.WriteByte('\n')
		}
		Write(t, name, b.Bytes())
		t.Logf("%s: %d of %d lines written again", name, g.changed, len(g.lines))
	})
	return g
}

// Lines are the fixture's lines as read.
func (g *Golden) Lines() [][]byte { return g.lines }

// Set makes line i the encoding of value. The line's old value must encode to the bytes read, so a line
// changes only where the value does.
func (g *Golden) Set(i int, old, value any) {
	g.t.Helper()
	if !Update() {
		g.t.Fatal("Golden.Set without -update")
	}
	was, err := g.encode(old)
	if err != nil || was != string(g.lines[i]) {
		g.t.Fatalf("%s line %d: the fixture's encoding is not reproduced (%v)", g.name, i+1, err)
	}
	now, err := g.encode(value)
	if err != nil {
		g.t.Fatalf("%s line %d: %v", g.name, i+1, err)
	}
	if now != was {
		g.lines[i] = []byte(now)
		g.changed++
	}
}

// manifestEntry is manifest.json with the entry of one fixture stating its content: its SHA-256, its size,
// and for a JSON Lines file its number of lines.
func manifestEntry(manifest []byte, name string, content []byte) ([]byte, error) {
	entry := regexp.MustCompile(`(?s)("` + regexp.QuoteMeta(name) + `": \{\s*"sha256": ")[0-9a-f]{64}(",\s*"bytes": )\d+(,\s*"records": )(\d+)`)
	if len(entry.FindAllIndex(manifest, -1)) != 1 {
		return nil, fmt.Errorf("manifest.json has no one entry for %s", name)
	}
	sum := sha256.Sum256(content)
	records := "${4}"
	if strings.Contains(name, ".jsonl") {
		records = fmt.Sprint(bytes.Count(content, []byte("\n")))
	}
	return entry.ReplaceAll(manifest, []byte(fmt.Sprintf("${1}%s${2}%d${3}%s", hex.EncodeToString(sum[:]), len(content),
		records))), nil
}

// Write replaces a fixture's content, compressed when the name ends in .gz, and its entry in manifest.json.
// A compressed file is written with no name and no time, so the same content gives the same bytes.
func Write(t testing.TB, name string, content []byte) {
	t.Helper()
	RefuseNarrowRun(t)
	data := content
	if strings.HasSuffix(name, ".gz") {
		var b bytes.Buffer
		w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
		if err == nil {
			_, err = w.Write(content)
		}
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		data = b.Bytes()
	}
	if err := os.WriteFile(Path(t, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(Path(t, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = manifestEntry(manifest, name, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(t, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
}
