package realdata

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secret stands for private text: no message may quote it.
const secret = "Quarterly renewal blocked by Zanzibar clause"

func TestItemsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items.jsonl.gz")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	w, err := CreateItems(root, "items.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	written := []Item{
		{ID: "a", File: "f.xml", Text: "We sign Friday.  <@U1> \U0001F642 \"quoted\" \\ \x00"},
		{ID: "b", File: "f.json", JSON: []byte("{\"a\": 1}\xff"), IsJSON: true},
		{ID: "c", Text: ""},
	}
	for _, it := range written {
		if err := w.Add(it); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Add(Item{ID: "a", Text: "again"}); err == nil {
		t.Error("an ID given twice was written")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	items, digest := readItems(t, path, false)
	if len(items) != len(written) || digest != w.SHA256() {
		t.Fatalf("%d items, digest %s, want %s", len(items), digest, w.SHA256())
	}
	for i, it := range items {
		want := written[i]
		want.Line = i + 1
		if it.ID != want.ID || it.File != want.File || it.Text != want.Text || string(it.JSON) != string(want.JSON) ||
			it.IsJSON != want.IsJSON || it.Line != want.Line {
			t.Errorf("item %d: got %+v, want %+v", i, it, want)
		}
	}
}

func TestLocalItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	lines := "\n" +
		`{"id": "m1", "text": "First.", "author": "ignored", "file": "ignored"}` + "\r\n" +
		"   \t\n" +
		"\xef\xbb\xbf" + `{"text": "Second.", "id": "m2"}` + "\n" +
		`{"id": "m3", "text": "no newline at the end"}`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _ := readItems(t, path, true)
	if len(items) != 3 || items[0].Line != 2 || items[1].Line != 4 || items[2].Line != 5 ||
		items[1].Text != "Second." || items[0].File != "" {
		t.Fatalf("%+v", items)
	}
}

// Every refusal names the line and a fixed reason, never what the line holds.
func TestItemRefusalsQuoteNothing(t *testing.T) {
	for name, c := range map[string]struct {
		line  string
		local bool
	}{
		"not JSON":                {secret, true},
		"a JSON syntax error":     {`{"id": "1", "text": "` + secret + `" x}`, true},
		"an array":                {`["` + secret + `"]`, true},
		"no id":                   {`{"text": "` + secret + `"}`, true},
		"a number id":             {`{"id": 7, "text": "` + secret + `"}`, true},
		"a number text":           {`{"id": "1", "text": 7, "note": "` + secret + `"}`, true},
		"no text":                 {`{"id": "1", "body": "` + secret + `"}`, true},
		"a duplicate key":         {`{"id": "1", "text": "a", "text": "` + secret + `"}`, true},
		"a lone surrogate":        {`{"id": "1", "text": "` + secret + `\ud800"}`, true},
		"bad UTF-8":               {`{"id": "1", "text": "` + secret + "\xff" + `"}`, true},
		"a non-finite number":     {`{"id": "1", "text": "` + secret + `", "n": NaN}`, true},
		"json in a local file":    {`{"id": "1", "json": "` + secret + `"}`, true},
		"an unknown dataset key":  {`{"id": "1", "text": "a", "` + secret + `": 1}`, false},
		"text and json":           {`{"id": "1", "text": "` + secret + `", "json": "e30="}`, false},
		"json that is not base64": {`{"id": "1", "json": "` + secret + `"}`, false},
	} {
		_, err := parseItem([]byte(c.line), 12, c.local)
		var refusal *ErrNotItem
		if !errors.As(err, &refusal) || refusal.Line != 12 {
			t.Errorf("%s: got %v", name, err)
			continue
		}
		for _, word := range longWords(secret) {
			if strings.Contains(err.Error(), word) {
				t.Errorf("%s: the refusal quotes the line: %s", name, err)
			}
		}
	}
}

func TestOpenItemsRefusesBrokenGzip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl.gz")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenItems(path, false); err == nil || strings.Contains(err.Error(), "Zanzibar") {
		t.Errorf("got %v", err)
	}
	r, err := OpenItems(filepath.Join(t.TempDir(), "missing.jsonl"), true)
	if err == nil {
		r.Close()
		t.Error("a missing file was opened")
	}
}

// longWords are the words of text long enough not to occur in a report by chance.
func longWords(text string) []string {
	var found []string
	for _, word := range strings.Fields(text) {
		if len(word) >= 5 {
			found = append(found, word)
		}
	}
	return found
}

// Both readers accept and refuse the same lines (tools/oracle/real.py reads the same vectors).
func TestItemLineVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "item-lines.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Lines []struct {
			Why   string `json:"why"`
			Hex   string `json:"hex"`
			Local bool   `json:"local"`
			OK    bool   `json:"ok"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors.Lines {
		line, err := hex.DecodeString(v.Hex)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseItem(line, 1, v.Local)
		if (err == nil) != v.OK {
			t.Errorf("%s: got %v, want accepted %v", v.Why, err, v.OK)
		}
	}
}

// A read error that comes with the last bytes of a file is reported, not dropped with them.
func TestItemReaderKeepsReadErrors(t *testing.T) {
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	io.WriteString(w, `{"id": "a", "text": "x"}`+"\n"+`{"id": "b", "text": "y"}`)
	w.Close()
	data := zipped.Bytes()
	data[len(data)-8] ^= 0xFF // the CRC-32 in the trailer
	path := filepath.Join(t.TempDir(), "items.jsonl.gz")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenItems(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if it, err := r.Next(); err == nil {
		t.Errorf("the item read with a checksum error was given: %+v", it)
	}
}
