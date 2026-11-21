package evidence

import (
	"bufio"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A host with an xn-- label is shown in its ASCII form when a browser opens it as written, and as unknown host
// when a label fails UTS #46 with the URL Standard's flags.
func TestPunycodeLabel(t *testing.T) {
	checkLinks(t, [][2]string{
		// Valid labels in several scripts, in each link form.
		{"https://xn--bcher-kva.example/ now", "[link xn--bcher-kva.example] now"},
		{"<https://files.XN--p1ai/|deck>", "deck [link files.xn--p1ai]"},
		{"[deck](https://xn--fiqs8s.example/x)", "deck [link xn--fiqs8s.example]"},
		{"<https://xn--r8jz45g.example>", "[link xn--r8jz45g.example]"},
		{"[deck]: https://xn--hxargifdar.example/", "[deck]: [link xn--hxargifdar.example]"},
		{"see www.xn--3e0b707e.example now", "see [link xn--3e0b707e.example] now"},
		{"<https://www.xn--h2brj9c.example/|deck>", "deck [link xn--h2brj9c.example]"},
		{"[deck](https://xn--bcher-kva&#46;example/)", "deck [link xn--bcher-kva.example]"},
		{"https://xn--9dbne9b.example/", "[link xn--9dbne9b.example]"},
		{"https://xn--mgbh0fb.example/", "[link xn--mgbh0fb.example]"},
		{"https://xn--1-ymcl5hc.example/", "[link xn--1-ymcl5hc.example]"},
		{"https://xn--11b2ezcw70k.example/", "[link xn--11b2ezcw70k.example]"},
		{"https://xn--zca.example/", "[link xn--zca.example]"},
		{"https://xn--tda.example/", "[link xn--tda.example]"},
		{"https://xn--ls8h.example/", "[link xn--ls8h.example]"},
		{"https://xn----eha.example/", "[link xn----eha.example]"},
		{"https://xn--bcher-kva.example.:8443/", "[link xn--bcher-kva.example.]"},
		// Bad Punycode, and Punycode that starts with its delimiter, which no encoder writes.
		{"https://xn--a.example/", "[link unknown host]"},
		{"<https://files.example.xn--/|deck>", "deck [link unknown host]"},
		{"https://xn---sgb.example/", "[link unknown host]"},
		// A label that decodes to an ASCII-only string, or to one that starts with xn--.
		{"https://xn--abc-.example/", "[link unknown host]"},
		{"https://xn--xn---epa.example/", "[link unknown host]"},
		// The bidi rule: a label with left-to-right and right-to-left letters, and a label that starts with a
		// digit in a name with a right-to-left label.
		{"https://xn--a-0hc.example/", "[link unknown host]"},
		{"https://1.xn--9dbne9b.example/", "[link unknown host]"},
		// The joiner rules: a zero width non-joiner after no virama, and a lone zero width joiner.
		{"https://xn--x-dha079u.example/", "[link unknown host]"},
		{"https://xn--1ug.example/", "[link unknown host]"},
		// A label that starts with a combining mark, holds a character the mapping changes or drops, is not
		// NFC, or holds a character Unicode 17.0.0 added, which Unicode 16.0.0 leaves unassigned.
		{"https://xn--a-wbb.example/", "[link unknown host]"},
		{"https://xn--wca.example/", "[link unknown host]"},
		{"https://xn--a-uca.example/", "[link unknown host]"},
		{"https://xn--u-ccb.example/", "[link unknown host]"},
		{"https://xn--793o.example/", "[link unknown host]"},
		// Each other rule still holds: xn-- inside a label is plain text, a port stops at 65535, and a host that
		// still holds www. is not shown.
		{"<https://axn--b.example/|deck>", "deck [link axn--b.example]"},
		{"https://xn--bcher-kva.example:65536/", "[link unknown host]"},
		{"https://www.xn--bcher-kva.www.example/", "[link unknown host]"},
	})
}

// urlParserDifferences are the generated hosts a link shows that the recorded parser refuses. Each has a
// right-to-left label with a letter or mark Unicode 14.0 added, from U+061D and U+0870 to U+08B5. Unicode
// 16.0.0 gives each of them the bidi class AL or NSM, the label passes every rule of UTS #46, and Node's parser
// (ada 3.4.4) refuses it, as it does any such label beside an older right-to-left letter.
var urlParserDifferences = map[string]bool{
	"xn--7dbby13z":                           true,
	"b2b.example.xn--4dbf2a94v":              true,
	"xn--cgbaflb":                            true,
	"xn--9ybbe":                              true,
	"xn--5xbb7aa1d.example":                  true,
	"xn--dmb07bsoianak.files.xn--fsqu85c14z": true,
}

// TestHostsMatchTheURLParser checks, on 20,000 generated hosts with xn-- labels, that a link shows a host only
// where the URL Standard's parser opens that same host, as Node's parser recorded it in testdata/url_hosts.
// That parser leaves out the bidi rule for a left-to-right label, part of the joiner rules and the check of
// Punycode that starts with its delimiter, so it opens some hosts a link shows as unknown host.
func TestHostsMatchTheURLParser(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "url_hosts.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(unzipped)
	lines.Scan()
	t.Logf("recorded with %s", lines.Text())
	inputs, opened, shown, stricter := 0, 0, 0, 0
	for lines.Scan() {
		var row [2]*string
		decode(t, lines.Bytes(), &row)
		inputs++
		host := *row[0]
		got := linkHost([]rune("https://"+host+"/"), false)
		if got != unknownHost {
			shown++
		}
		if row[1] == nil {
			if got != unknownHost && !urlParserDifferences[host] {
				t.Errorf("%s: a link shows %q, and the URL parser refuses it", host, got)
			}
			continue
		}
		opened++
		want := *row[1]
		for strings.HasPrefix(want, "www.") {
			want = want[4:]
		}
		switch got {
		case want:
		case unknownHost:
			stricter++
		default:
			t.Errorf("%s: a link shows %q, and the URL parser opens %q", host, got, want)
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d hosts: the parser opens %d, a link shows %d, and shows %d of the rest as unknown host", inputs,
		opened, shown, stricter)
	// Showing no host would meet the property, so most valid hosts must be shown.
	if inputs != 20000 || shown < 8000 {
		t.Errorf("%d hosts and %d shown", inputs, shown)
	}
}
