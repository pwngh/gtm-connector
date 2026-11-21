package realdata

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Item is one input: a text (one utterance or one message) or one raw JSON document.
//
// An items file holds one JSON object a line: "id", then "text" (a string) or "json" (the document's bytes
// in standard base64), and for a dataset "file", where the item came from, and "kind" (natural,
// role-played, elicited or invented). A local file needs only "id" and "text", and its other keys are
// ignored. A line is parsed as strict_json parses, so a duplicate key or a
// lone surrogate refuses it. Blank lines are skipped. The oracle read the same rules.
type Item struct {
	Line   int // the line of the items file it is on, from 1
	ID     string
	File   string
	Kind   string
	Text   string
	JSON   []byte
	IsJSON bool
}

// InputSHA256 is the digest of the input: the text's UTF-8, or the document's bytes.
func (it Item) InputSHA256() string {
	data := []byte(it.Text)
	if it.IsJSON {
		data = it.JSON
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// line is the item as a line of an items file.
func (it Item) line() ([]byte, error) {
	o := strictjson.NewObject("id", it.ID)
	if it.File != "" {
		o.Set("file", it.File)
	}
	if it.Kind != "" {
		o.Set("kind", it.Kind)
	}
	if it.IsJSON {
		o.Set("json", base64.StdEncoding.EncodeToString(it.JSON))
	} else {
		o.Set("text", it.Text)
	}
	text, err := canonical(o)
	return append([]byte(text), '\n'), err
}

// ItemReader reads an items file in order and digests every byte it reads.
type ItemReader struct {
	in     *bufio.Reader
	closer []io.Closer
	hash   hash.Hash
	line   int
	local  bool
}

// OpenItems opens an items file, gzipped when its name ends in .gz. A local file holds only text items.
func OpenItems(path string, local bool) (*ItemReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return newItemReader(file, strings.HasSuffix(path, ".gz"), local)
}

func newItemReader(file *os.File, gzipped, local bool) (*ItemReader, error) {
	r := &ItemReader{closer: []io.Closer{file}, hash: sha256.New(), local: local}
	var source io.Reader = file
	if gzipped {
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, errors.New("the items file is not gzip")
		}
		r.closer = append(r.closer, unzipped)
		source = unzipped
	}
	r.in = bufio.NewReaderSize(io.TeeReader(source, r.hash), 1<<20)
	return r, nil
}

// Close closes the file.
func (r *ItemReader) Close() error {
	var err error
	for i := len(r.closer) - 1; i >= 0; i-- {
		err = errors.Join(err, r.closer[i].Close())
	}
	return err
}

// SHA256 is the digest of what was read, which is the whole file once Next has given io.EOF.
func (r *ItemReader) SHA256() string { return hex.EncodeToString(r.hash.Sum(nil)) }

// ErrNotItem is a line that is not an item. Its message names the line and never quotes it.
type ErrNotItem struct {
	Line   int
	Reason string
}

func (e *ErrNotItem) Error() string {
	return fmt.Sprintf("line %d is not an item: %s", e.Line, e.Reason)
}

// Next gives the next item, or io.EOF.
func (r *ItemReader) Next() (Item, error) {
	for {
		raw, err := r.in.ReadBytes('\n')
		switch {
		case err == io.EOF && len(raw) == 0:
			return Item{}, io.EOF
		case err != nil && err != io.EOF:
			// A read error names the file's trouble, never its content.
			return Item{}, fmt.Errorf("reading line %d failed", r.line+1)
		}
		r.line++
		if len(bytes.Trim(raw, " \t\r\n")) == 0 {
			continue
		}
		return parseItem(raw, r.line, r.local)
	}
}

// parseItem reads one line. Each refusal has a fixed reason, so no message carries the line's text.
func parseItem(raw []byte, line int, local bool) (Item, error) {
	refuse := func(reason string) (Item, error) { return Item{}, &ErrNotItem{line, reason} }
	value, err := strictjson.Loads(raw)
	if err != nil {
		return refuse("not JSON that strict_json reads")
	}
	o, ok := value.(*strictjson.Object)
	if !ok {
		return refuse("not a JSON object")
	}
	it := Item{Line: line}
	fields := map[string]*string{"id": &it.ID, "text": &it.Text}
	if !local {
		fields["file"] = &it.File
		fields["kind"] = &it.Kind
	}
	var encoded *string
	for i, key := range o.Keys {
		target, known := fields[key]
		if key == "json" {
			if local {
				return refuse(`a local file holds text items only, not "json"`)
			}
			text, isText := o.Values[i].(string)
			if !isText {
				return refuse(`"json" is not a string`)
			}
			encoded = &text
			continue
		}
		if !known {
			if local {
				continue
			}
			return refuse("a key other than id, file, kind, text and json")
		}
		text, isText := o.Values[i].(string)
		if !isText {
			return refuse(fmt.Sprintf("%q is not a string", key))
		}
		*target = text
	}
	_, hasID := o.Get("id")
	_, hasText := o.Get("text")
	switch {
	case !hasID:
		return refuse(`no "id"`)
	case hasText == (encoded != nil):
		return refuse(`not exactly one of "text" and "json"`)
	case encoded != nil:
		// Canonical base64 only: no line breaks, padding, and no bits set in the padding.
		data, err := base64.StdEncoding.Strict().DecodeString(*encoded)
		if err != nil || base64.StdEncoding.EncodeToString(data) != *encoded {
			return refuse(`"json" is not canonical base64`)
		}
		it.JSON, it.IsJSON = data, true
	}
	return it, nil
}

// ItemWriter writes a gzipped items file into a folder, under a fresh temporary name until Close moves it in.
type ItemWriter struct {
	root            *os.Root
	name, temporary string
	file            *os.File
	zip             *gzip.Writer
	buffer          *bufio.Writer
	hash            hash.Hash
	ids             map[string]bool
	Text, JSON      int
}

// CreateItems starts an items file in a folder.
func CreateItems(root *os.Root, name string) (*ItemWriter, error) {
	file, temporary, err := createExclusive(root, name, 0o644)
	if err != nil {
		return nil, err
	}
	w := &ItemWriter{root: root, name: name, temporary: temporary, file: file, hash: sha256.New(), ids: map[string]bool{}}
	// No name and no time in the header, so the same items give the same file.
	w.zip, _ = gzip.NewWriterLevel(file, gzip.BestCompression)
	w.buffer = bufio.NewWriterSize(io.MultiWriter(w.zip, w.hash), 1<<20)
	return w, nil
}

// Add writes one item. IDs are unique within a file.
func (w *ItemWriter) Add(it Item) error {
	if w.ids[it.ID] {
		return fmt.Errorf("item %s is given twice", it.ID)
	}
	w.ids[it.ID] = true
	line, err := it.line()
	if err != nil {
		return err
	}
	if it.IsJSON {
		w.JSON++
	} else {
		w.Text++
	}
	_, err = w.buffer.Write(line)
	return err
}

// SHA256 is the digest of the items written, uncompressed.
func (w *ItemWriter) SHA256() string { return hex.EncodeToString(w.hash.Sum(nil)) }

// Close finishes the file and moves it into place.
func (w *ItemWriter) Close() error {
	err := errors.Join(w.buffer.Flush(), w.zip.Close(), w.file.Close())
	if err == nil {
		err = w.root.Rename(w.temporary, w.name)
	}
	if err != nil {
		w.root.Remove(w.temporary)
	}
	return err
}

// Abort removes the unfinished file.
func (w *ItemWriter) Abort() {
	w.zip.Close()
	w.file.Close()
	w.root.Remove(w.temporary)
}
