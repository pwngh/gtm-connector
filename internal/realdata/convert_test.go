package realdata

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// An invented meeting in the AMI layout: two speakers, punctuation, a vocal sound, a gap, a segment with no
// words, a single-word segment, a word outside every segment, and one byte that is é in ISO-8859-1.
var amiFiles = map[string]string{
	"words/XX0001a.A.words.xml": `<?xml version="1.0" encoding="ISO-8859-1" standalone="yes"?>
<nite:root nite:id="XX0001a.A.words" xmlns:nite="http://nite.sourceforge.net/">
   <w nite:id="XX0001a.A.words0" starttime="1.0" endtime="1.2">Okay</w>
   <w nite:id="XX0001a.A.words1" starttime="1.2" endtime="1.2" punc="true">.</w>
   <w nite:id="XX0001a.A.words2" starttime="1.3" endtime="1.5">So</w>
   <vocalsound nite:id="XX0001a.A.words3" starttime="1.5" endtime="1.6" type="laugh"/>
   <w nite:id="XX0001a.A.words4" starttime="1.6" endtime="1.9">we&#39;ll</w>
   <w nite:id="XX0001a.A.words5" starttime="1.9" endtime="2.1" trunc="true">st</w>
   <w nite:id="XX0001a.A.words6" starttime="2.1" endtime="2.4">caf` + "\xe9" + `</w>
   <w nite:id="XX0001a.A.words7" starttime="2.4" endtime="2.4" punc="true">?</w>
   <gap nite:id="XX0001a.A.words8" starttime="3.0" endtime="4.0"/>
   <vocalsound nite:id="XX0001a.A.words9" starttime="5.0" endtime="5.1" type="cough"/>
   <w nite:id="XX0001a.A.words10" starttime="6.0" endtime="6.2">Right</w>
   <w nite:id="XX0001a.A.words11" starttime="9.0" endtime="9.2">unheard</w>
</nite:root>
`,
	"segments/XX0001a.A.segments.xml": `<?xml version="1.0" encoding="ISO-8859-1" standalone="yes"?>
<nite:root nite:id="XX0001a.A.segs" xmlns:nite="http://nite.sourceforge.net/">
   <segment nite:id="XX0001a.sync.1" channel="0">
      <nite:child href="XX0001a.A.words.xml#id(XX0001a.A.words0)..id(XX0001a.A.words7)"/>
   </segment>
   <segment nite:id="XX0001a.sync.3" channel="0">
      <nite:child href="XX0001a.A.words.xml#id(XX0001a.A.words8)..id(XX0001a.A.words9)"/>
   </segment>
   <segment nite:id="XX0001a.sync.4" channel="0">
      <nite:child href="XX0001a.A.words.xml#id(XX0001a.A.words10)"/>
   </segment>
</nite:root>
`,
	"words/XX0001a.B.words.xml": `<?xml version="1.0" encoding="ISO-8859-1"?>
<nite:root nite:id="XX0001a.B.words" xmlns:nite="http://nite.sourceforge.net/">
   <w nite:id="XX0001a.B.words0">Yes</w><w nite:id="XX0001a.B.words1" punc="true">,</w><w nite:id="XX0001a.B.words2">fine</w>
</nite:root>
`,
	"segments/XX0001a.B.segments.xml": `<?xml version="1.0" encoding="ISO-8859-1"?>
<nite:root nite:id="XX0001a.B.segs" xmlns:nite="http://nite.sourceforge.net/">
   <segment nite:id="XX0001a.sync.2"><nite:child href="XX0001a.B.words.xml#id(XX0001a.B.words0)..id(XX0001a.B.words2)"/></segment>
</nite:root>
`,
	"LICENCE.txt": "invented",
}

func buildZip(t *testing.T, files map[string]string) []byte {
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for _, name := range names {
		out, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(out, files[name])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// placeDataset puts a dataset's one download in a fresh cache, as fetch leaves it.
func placeDataset(t *testing.T, converter string, archive []byte) (string, Dataset) {
	return placeFiles(t, converter, map[string][]byte{"set.zip": archive})
}

// placeFiles puts a dataset's downloads, by path, in a fresh cache, pinned as they are.
func placeFiles(t *testing.T, converter string, downloads map[string][]byte) (string, Dataset) {
	cache := t.TempDir()
	d := Dataset{ID: "set-1", Version: "1", Access: Public, Kind: "role-played", Converter: converter,
		BaseURL: "https://data.example/"}
	for name, data := range downloads {
		d.Files = append(d.Files, pin(name, d.BaseURL+name, data))
		local := filepath.Join(filesDir(cache, d), filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cache, d
}

func readItems(t *testing.T, path string, local bool) ([]Item, string) {
	t.Helper()
	r, err := OpenItems(path, local)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var items []Item
	for {
		it, err := r.Next()
		if err == io.EOF {
			return items, r.SHA256()
		}
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, it)
	}
}

func TestConvertAMI(t *testing.T) {
	cache, d := placeDataset(t, "ami-words", buildZip(t, amiFiles))
	summary, err := Convert(cache, d)
	if err != nil {
		t.Fatal(err)
	}
	items, digest := readItems(t, DatasetRun(cache, d).Items, false)
	scenario := "role-played"
	want := []Item{
		{Line: 1, ID: "XX0001a.sync.1", File: "segments/XX0001a.A.segments.xml", Kind: scenario, Text: "Okay. So we'll st café?"},
		{Line: 2, ID: "XX0001a.sync.4", File: "segments/XX0001a.A.segments.xml", Kind: scenario, Text: "Right"},
		{Line: 3, ID: "XX0001a.sync.2", File: "segments/XX0001a.B.segments.xml", Kind: scenario, Text: "Yes, fine"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items:\n got %+v\nwant %+v", items, want)
	}
	wantCounts := map[string]int{"segment files": 2, "segments": 4, "segments without words": 1, "words": 12,
		"words outside segments": 1}
	if !reflect.DeepEqual(summary.Counts, wantCounts) || summary.TextItems != 3 || summary.JSONItems != 0 ||
		summary.Kinds[scenario] != 3 || summary.ItemsSHA256 != digest {
		t.Errorf("summary: %+v; items digest %s", summary, digest)
	}
	// The same download converts to the same bytes.
	again, err := Convert(cache, d)
	if err != nil || again.ItemsSHA256 != summary.ItemsSHA256 {
		t.Errorf("a second conversion: %v, %s", err, again.ItemsSHA256)
	}
}

func TestAMIKinds(t *testing.T) {
	for meeting, kind := range map[string]string{"EN2001a": "natural", "IN1001": "natural", "ES2002a": "role-played",
		"IS1000a": "role-played", "TS3003b": "role-played", "IB4001": "role-played"} {
		if got := amiKind(meeting); got != kind {
			t.Errorf("%s: %s, want %s", meeting, got, kind)
		}
	}
}

func TestConvertAMIRefuses(t *testing.T) {
	for name, change := range map[string][3]string{
		"a range into a missing file": {"segments/XX0001a.B.segments.xml", "XX0001a.B.words.xml#", "XX0001a.Z.words.xml#"},
		"a range to a missing word":   {"segments/XX0001a.B.segments.xml", "id(XX0001a.B.words2)", "id(XX0001a.B.words9)"},
		"a range that runs backwards": {"segments/XX0001a.B.segments.xml", "id(XX0001a.B.words0)..id(XX0001a.B.words2)", "id(XX0001a.B.words2)..id(XX0001a.B.words0)"},
		"an href that is no range":    {"segments/XX0001a.B.segments.xml", "#id(", "#xpointer("},
		"another encoding":            {"words/XX0001a.B.words.xml", "ISO-8859-1", "Shift_JIS"},
		"a word ID given twice":       {"words/XX0001a.B.words.xml", `"XX0001a.B.words1"`, `"XX0001a.B.words0"`},
		"broken XML":                  {"words/XX0001a.B.words.xml", "</nite:root>", "</nite:rot>"},
		"an external entity":          {"words/XX0001a.B.words.xml", "<nite:root", `<!DOCTYPE r [<!ENTITY x SYSTEM "file:///etc/hosts">]><nite:root`},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range amiFiles {
				files[k] = v
			}
			changed := strings.Replace(files[change[0]], change[1], change[2], 1)
			if changed == files[change[0]] {
				t.Fatal("the change did not apply")
			}
			files[change[0]] = changed
			if name == "an external entity" {
				files[change[0]] = strings.Replace(changed, ">Yes<", ">&x;<", 1)
			}
			cache, d := placeDataset(t, "ami-words", buildZip(t, files))
			if _, err := Convert(cache, d); err == nil {
				t.Fatal("converted")
			}
			if _, err := os.Stat(DatasetRun(cache, d).Items); err == nil {
				t.Error("an items file was left")
			}
		})
	}
}

// A download that no longer matches the manifest is not converted.
func TestConvertChecksDigests(t *testing.T) {
	cache, d := placeDataset(t, "ami-words", buildZip(t, amiFiles))
	d.Files[0].SHA256 = strings.Repeat("0", 64)
	if _, err := Convert(cache, d); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("converted: %v", err)
	}
}
