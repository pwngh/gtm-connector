package realdata

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// A Converter turns a dataset's downloads into items. It reads them with code in this repository and never
// runs anything they hold. It returns counts of what it read and what it skipped; a count never holds text.
type Converter func(files *Files, add func(Item) error) (map[string]int, error)

// converters are the converters by the name a manifest entry gives. A new dataset is a manifest entry and,
// when no converter here reads its format, one more converter.
var converters = map[string]Converter{
	"ami-words":    convertAMI,
	"notsofar":     convertNOTSOFAR,
	"textgrid":     convertTextGrids,
	"rev-nlp":      convertRevNLP,
	"apptek-jsonl": convertAppTek,
	"slack-export": convertSlackExport,
	"csv-columns":  convertCSVColumns,
	"irc-raw":      convertIRC,
	"sobject-tree": convertSObjectTree,
	"csv-cells":    convertCSVCells,
}

// maxMember is the most any one file may hold once decompressed.
const maxMember = 256 << 20

// Files are a dataset's downloads, each opened once and checked against its pin. Converters read only
// those open files, so a file replaced after the check is never read, and Recheck finds one changed in
// place. A .zip is read as its members, a .tar.gz as its members in archive order, and any other file as
// itself.
type Files struct {
	root    *os.Root
	files   []File
	open    map[string]*os.File
	zips    map[string]*zip.Reader
	dataset Dataset
}

// Member is one file a converter reads: a download, or a member of an archive download.
type Member struct {
	// Name is the download's path, then "/" and the member's path for an archive member.
	Name string
	// Inner is the path a pattern matches: the member's path in its archive, or the download's path.
	Inner string
	size  int64
	open  func() (io.ReadCloser, error)
}

// Read gives the member's bytes, up to maxMember.
func (m Member) Read() ([]byte, error) {
	if m.size > maxMember {
		return nil, fmt.Errorf("%s holds more than %d bytes", m.Name, maxMember)
	}
	in, err := m.open()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, maxMember+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	if len(data) > maxMember {
		return nil, fmt.Errorf("%s holds more than %d bytes", m.Name, maxMember)
	}
	return data, nil
}

// Kind is the dataset's kind, for items a converter does not tag itself.
func (f *Files) Kind() string { return f.dataset.Kind }

// openFiles opens each of a dataset's downloads in its folder in the cache and checks it against its pin.
func openFiles(cache string, d Dataset) (*Files, error) {
	root, err := datasetDir(cache, d.ID)
	if err != nil {
		return nil, err
	}
	f := &Files{root: root, files: append([]File(nil), d.Files...), open: map[string]*os.File{},
		zips: map[string]*zip.Reader{}, dataset: d}
	for _, file := range d.Files {
		in, err := openVerified(root, "files/"+file.Path, file)
		if err != nil {
			f.Close()
			return nil, err
		}
		f.open[file.Path] = in
		if strings.HasSuffix(file.Path, ".zip") {
			archive, err := zip.NewReader(io.NewSectionReader(in, 0, file.Bytes), file.Bytes)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("%s: %w", file.Path, err)
			}
			f.zips[file.Path] = archive
		}
	}
	sort.Slice(f.files, func(i, j int) bool { return f.files[i].Path < f.files[j].Path })
	return f, nil
}

// openVerified opens a regular file in the folder and checks its bytes against its pin.
func openVerified(root *os.Root, name string, file File) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", file.Path)
	}
	in, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	if opened, err := in.Stat(); err != nil || !os.SameFile(info, opened) {
		in.Close()
		return nil, fmt.Errorf("%s changed while it was opened", file.Path)
	}
	if err := verifyOpen(in, file); err != nil {
		in.Close()
		return nil, err
	}
	return in, nil
}

// Recheck checks every open download against its pin again, so one changed in place while it was read is
// found before its items are kept.
func (f *Files) Recheck() error {
	for _, file := range f.files {
		if err := verifyOpen(f.open[file.Path], file); err != nil {
			return fmt.Errorf("%w, after it was checked", err)
		}
	}
	return nil
}

// Close closes the downloads and the folder.
func (f *Files) Close() {
	for _, in := range f.open {
		in.Close()
	}
	f.root.Close()
}

// section reads a download's checked bytes from its open file.
func (f *Files) section(file File) *io.SectionReader {
	return io.NewSectionReader(f.open[file.Path], 0, file.Bytes)
}

// Walk calls fn on each member whose inner path matches the pattern: downloads in path order, a zip's
// members in name order, a tar's in archive order, in one pass. A pattern is path.Match's, where * stays
// within one folder; one that starts with **/ matches the rest against the last segment, at any depth.
func (f *Files) Walk(pattern string, fn func(Member) error) error {
	for _, file := range f.files {
		var err error
		switch {
		case f.zips[file.Path] != nil:
			err = walkZip(file.Path, f.zips[file.Path], pattern, fn)
		case strings.HasSuffix(file.Path, ".tar.gz") || strings.HasSuffix(file.Path, ".tgz"):
			err = walkTar(file.Path, f.section(file), pattern, fn)
		default:
			var ok bool
			if ok, err = matchPath(pattern, file.Path); ok {
				err = fn(Member{Name: file.Path, Inner: file.Path, size: file.Bytes,
					open: func() (io.ReadCloser, error) { return io.NopCloser(f.section(file)), nil }})
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func walkZip(name string, archive *zip.Reader, pattern string, fn func(Member) error) error {
	entries := append([]*zip.File(nil), archive.File...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, entry := range entries {
		if entry.FileInfo().IsDir() {
			continue
		}
		ok, err := matchPath(pattern, entry.Name)
		if err != nil {
			return err
		}
		if ok {
			if err := fn(Member{Name: name + "/" + entry.Name, Inner: entry.Name, size: int64(entry.UncompressedSize64),
				open: entry.Open}); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkTar(name string, in io.Reader, pattern string, fn func(Member) error) error {
	unzipped, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("%s is not gzip", name)
	}
	defer unzipped.Close()
	archive := tar.NewReader(unzipped)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		ok, err := matchPath(pattern, header.Name)
		if err != nil {
			return err
		}
		if ok {
			if err := fn(Member{Name: name + "/" + header.Name, Inner: header.Name, size: header.Size,
				open: func() (io.ReadCloser, error) { return io.NopCloser(archive), nil }}); err != nil {
				return err
			}
		}
	}
}

func matchPath(pattern, name string) (bool, error) {
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		return path.Match(rest, path.Base(name))
	}
	return path.Match(pattern, name)
}

// Read gives one member of a zip, or one download, by its full name.
func (f *Files) Read(name string) ([]byte, error) {
	for _, file := range f.files {
		if archive := f.zips[file.Path]; archive != nil && strings.HasPrefix(name, file.Path+"/") {
			inner := strings.TrimPrefix(name, file.Path+"/")
			for _, entry := range archive.File {
				if entry.Name == inner {
					return Member{Name: name, size: int64(entry.UncompressedSize64), open: entry.Open}.Read()
				}
			}
		}
		if file.Path == name {
			return Member{Name: name, size: file.Bytes,
				open: func() (io.ReadCloser, error) { return io.NopCloser(f.section(file)), nil }}.Read()
		}
	}
	return nil, fmt.Errorf("%s is not among the files", name)
}

// addJSON adds a raw JSON document for strict_json.
func addJSON(add func(Item) error, id, file, kind string, data []byte) error {
	return add(Item{ID: id, File: file, Kind: kind, JSON: data, IsJSON: true})
}

// ConvertSummary is items.json: what a converter made of a dataset.
type ConvertSummary struct {
	Dataset     string         `json:"dataset"`
	Version     string         `json:"version"`
	Converter   string         `json:"converter"`
	TextItems   int            `json:"text_items"`
	JSONItems   int            `json:"json_items"`
	Kinds       map[string]int `json:"kinds"`
	ItemsSHA256 string         `json:"items_sha256"`
	Counts      map[string]int `json:"counts"`
}

// ErrNoConverter is a dataset whose text no converter reads yet.
var ErrNoConverter = errors.New("no converter yet")

// Convert writes a fetched dataset's items into its folder.
func Convert(cache string, d Dataset) (*ConvertSummary, error) {
	if d.Converter == "" {
		return nil, fmt.Errorf("%s: %w", d.ID, ErrNoConverter)
	}
	files, err := openFiles(cache, d)
	if err != nil {
		return nil, err
	}
	defer files.Close()
	w, err := CreateItems(files.root, itemsFile)
	if err != nil {
		return nil, err
	}
	kinds := map[string]int{}
	counts, err := converters[d.Converter](files, func(it Item) error {
		if it.Kind == "" {
			it.Kind = d.Kind
		}
		if !knownKinds[it.Kind] {
			return fmt.Errorf("item %s has kind %q", it.ID, it.Kind)
		}
		kinds[it.Kind]++
		return w.Add(it)
	})
	if err == nil && w.Text+w.JSON == 0 {
		err = errors.New("the converter found nothing it reads")
	}
	if err == nil {
		err = files.Recheck()
	}
	if err != nil {
		w.Abort()
		return nil, fmt.Errorf("%s: %w", d.ID, err)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	summary := &ConvertSummary{Dataset: d.ID, Version: d.Version, Converter: d.Converter, TextItems: w.Text,
		JSONItems: w.JSON, Kinds: kinds, ItemsSHA256: w.SHA256(), Counts: counts}
	return summary, writeJSON(files.root, itemsMeta, summary, 0o644)
}

// writeJSON writes a value as indented JSON, a fresh file in the folder.
func writeJSON(root *os.Root, name string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		return err
	}
	return writeExclusive(root, name, append(data, '\n'), mode)
}
