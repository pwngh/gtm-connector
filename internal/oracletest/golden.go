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

// Write replaces a fixture's content, compressed when the name ends in .gz, and the content's size and
// SHA-256 in manifest.json. A compressed file is written with no name and no time, so the same content gives the same
// bytes.
func Write(t testing.TB, name string, content []byte) {
	t.Helper()
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
	entry := regexp.MustCompile(`(?s)("` + regexp.QuoteMeta(name) + `": \{\s*"sha256": ")[0-9a-f]{64}(",\s*"bytes": )\d+`)
	if len(entry.FindAllIndex(manifest, -1)) != 1 {
		t.Fatalf("manifest.json has no one entry for %s", name)
	}
	sum := sha256.Sum256(content)
	manifest = entry.ReplaceAll(manifest, []byte(fmt.Sprintf("${1}%s${2}%d", hex.EncodeToString(sum[:]), len(content))))
	if err := os.WriteFile(Path(t, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
}
