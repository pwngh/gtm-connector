package cards

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// allocated runs read and gives the bytes it allocated.
func allocated(read func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	read()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// sized makes a file of exactly this size without writing it: the file is all holes.
func sized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

// A card file over its size is refused by its size and never read: opening the folder costs no memory for
// it, its card is refused as invalid when it is asked for, and the folder's other card still verifies. A
// file of exactly its size is read.
func TestACardFileOverItsSizeIsNotRead(t *testing.T) {
	ctx := context.Background()
	other := "brief-" + strings.Repeat("0", 31) + "9"
	for _, c := range []struct {
		folder  folderCase
		sub     string
		limit   int64
		refusal error
	}{
		{folders[0], "briefs", MaxCardBytes, ErrInvalidCard},
		{folders[1], "selected-briefs", MaxArtifactBytes, ErrBriefInvalid},
	} {
		t.Run(c.sub, func(t *testing.T) {
			dir := t.TempDir()
			copyTree(t, oracletest.Path(t, "folders/"+c.folder.name), dir)
			path := filepath.Join(dir, c.sub, other+".json")
			sized(t, path, c.limit+1)
			var f *Folder
			var err error
			used := allocated(func() { f, err = OpenTrustedExport(dir) })
			if err != nil {
				t.Fatal(err)
			}
			if used > 1<<20 {
				t.Errorf("opening the folder allocated %d bytes beside a file of %d", used, c.limit+1)
			}
			if _, held := f.payloads(c.sub)[other]; held {
				t.Error("the file over its size was read")
			}
			if _, err := f.Verify(ctx, other, demoSources(nil)); !errors.Is(err, c.refusal) {
				t.Errorf("the card over its size: got %v, want %v", err, c.refusal)
			}
			if _, err := f.Verify(ctx, c.folder.brief, demoSources(nil)); err != nil {
				t.Errorf("the folder's own card: %v", err)
			}
			sized(t, path, c.limit)
			if f, err = OpenTrustedExport(dir); err != nil {
				t.Fatal(err)
			}
			if got := int64(len(f.payloads(c.sub)[other])); got != c.limit {
				t.Errorf("a file of exactly its size: %d bytes read", got)
			}
			if _, err := f.Verify(ctx, other, demoSources(nil)); !errors.Is(err, c.refusal) {
				t.Errorf("a file of holes: got %v, want %v", err, c.refusal)
			}
		})
	}
}

func (f *Folder) payloads(sub string) map[string][]byte {
	if sub == "briefs" {
		return f.Cards
	}
	return f.Prepared
}

// The audit export is read a row at a time. One over its whole size is refused by its size; one whose first
// row is not a receipt is refused there, for the cost of that row and not of the file; and a row over the
// size of a receipt is refused, where a row of 64 MiB was once read.
func TestAnAuditExportIsReadARowAtATime(t *testing.T) {
	folder := func(t *testing.T) (dir, export string) {
		dir = t.TempDir()
		copyTree(t, oracletest.Path(t, "folders/cli_card"), dir)
		return dir, filepath.Join(dir, "audit.jsonl")
	}
	t.Run("over its size", func(t *testing.T) {
		dir, export := folder(t)
		sized(t, export, maxAuditBytes+1)
		var err error
		used := allocated(func() { _, err = OpenTrustedExport(dir) })
		if !errors.Is(err, ErrFileTooLarge) || used > 1<<20 {
			t.Fatalf("got %v after %d bytes", err, used)
		}
	})
	t.Run("a first row that is no receipt", func(t *testing.T) {
		dir, export := folder(t)
		rows, err := os.ReadFile(export)
		if err != nil {
			t.Fatal(err)
		}
		const size = 64 << 20
		padded := append([]byte("{}\n"), bytes.Repeat(append(bytes.Repeat([]byte(" "), 1023), '\n'), size>>10)...)
		if err := os.WriteFile(export, append(padded, rows...), 0o600); err != nil {
			t.Fatal(err)
		}
		used := allocated(func() { _, err = OpenTrustedExport(dir) })
		if err == nil || used > 2*maxAuditRowBytes {
			t.Fatalf("got %v after %d bytes, beside an export of %d", err, used, size)
		}
	})
	t.Run("a row over the size of a receipt", func(t *testing.T) {
		dir, export := folder(t)
		rows, err := os.ReadFile(export)
		if err != nil {
			t.Fatal(err)
		}
		first, rest, _ := bytes.Cut(rows, []byte("\n"))
		for padding, read := range map[int]bool{maxAuditRowBytes - len(first) - 1: true, maxAuditRowBytes: false} {
			long := append(append(append(bytes.Repeat([]byte(" "), padding), first...), '\n'), rest...)
			if err := os.WriteFile(export, long, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := OpenTrustedExport(dir)
			if read && (err != nil || len(f.Log) != bytes.Count(rows, []byte("\n"))) {
				t.Errorf("a row just inside the size: %v", err)
			}
			if !read && err == nil {
				t.Errorf("a row of %d bytes was read", padding+len(first))
			}
		}
	})
}
