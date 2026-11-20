// Package oracletest reads the fixtures in testdata/oracle for parity tests.
package oracletest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path is the path of one fixture file, given relative to testdata/oracle.
func Path(t testing.TB, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "oracle", filepath.FromSlash(name))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's folder")
		}
		dir = parent
	}
}

// Read gives a fixture's bytes, decompressed when the name ends in .gz.
func Read(t testing.TB, name string) []byte {
	t.Helper()
	file, err := os.Open(Path(t, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(name, ".gz") {
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer unzipped.Close()
		reader = unzipped
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return data
}

// JSON decodes a whole fixture file into value.
func JSON(t testing.TB, name string, value any) {
	t.Helper()
	if err := json.Unmarshal(Read(t, name), value); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// Lines gives each line of a JSON Lines fixture.
func Lines(t testing.TB, name string) [][]byte {
	t.Helper()
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(Read(t, name)))
	scanner.Buffer(nil, 1<<26)
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return lines
}

// Mismatches collects differences and reports the first few, then the total, when the test ends.
type Mismatches struct {
	t      testing.TB
	name   string
	shown  int
	total  int // rows with at least one difference
	rows   int
	marked int // the last row counted in total
}

// NewMismatches starts a count for one fixture family.
func NewMismatches(t testing.TB, name string) *Mismatches {
	m := &Mismatches{t: t, name: name}
	t.Cleanup(func() {
		if m.total > 0 {
			t.Errorf("%s: %d of %d rows differ", m.name, m.total, m.rows)
		} else {
			t.Logf("%s: %d rows, no differences", m.name, m.rows)
		}
	})
	return m
}

// Row counts one checked row.
func (m *Mismatches) Row() { m.rows++ }

// Add records one difference in the current row. A row counts once however many differences it has. The
// first ten differences are printed.
func (m *Mismatches) Add(format string, args ...any) {
	m.t.Helper()
	if m.marked != m.rows {
		m.total++
		m.marked = m.rows
	}
	if m.shown < 10 {
		m.shown++
		m.t.Errorf(m.name+": "+format, args...)
	}
}

// Count is the number of rows that differ so far.
func (m *Mismatches) Count() int { return m.total }
