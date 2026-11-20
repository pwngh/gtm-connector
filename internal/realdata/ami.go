package realdata

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The AMI manual annotations are NITE XML. words/MEETING.SPEAKER.words.xml holds one speaker's words in
// order, each a <w> element (punc="true" on punctuation), among other elements such as <vocalsound> and
// <gap>. segments/MEETING.SPEAKER.segments.xml groups them: each <segment> points at a range of word IDs,
// "MEETING.SPEAKER.words.xml#id(FIRST)..id(LAST)" or "#id(ONE)".
//
// One utterance is one segment: the text of the <w> elements in its range, in order, one space apart, with
// punctuation joined to the word before it. Other elements carry no words and are left out. A segment with
// no words gives no item.

const niteNamespace = "http://nite.sourceforge.net/"

var amiRange = regexp.MustCompile(`^([A-Za-z0-9._-]+\.words\.xml)#id\(([^()]+)\)(?:\.\.id\(([^()]+)\))?$`)

type amiElement struct {
	word bool // a <w> element
	punc bool
	text string
}

type amiWords struct {
	elements []amiElement
	index    map[string]int // nite:id -> position in elements
	used     []bool
}

// amiKind is a meeting's kind by its ID: the EN and IN meetings are natural, the scenario meetings (ES, IS,
// TS) and IB role-played.
func amiKind(meeting string) string {
	if strings.HasPrefix(meeting, "EN") || strings.HasPrefix(meeting, "IN") {
		return "natural"
	}
	return "role-played"
}

func convertAMI(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"segment files": 0, "segments": 0, "segments without words": 0, "words": 0,
		"words outside segments": 0}
	err := files.Walk("segments/*.segments.xml", func(m Member) error {
		counts["segment files"]++
		segments, err := readAMISegments(m)
		if err != nil {
			return err
		}
		archive := strings.TrimSuffix(m.Name, "/"+m.Inner)
		meeting, _, _ := strings.Cut(path.Base(m.Inner), ".")
		loaded := map[string]*amiWords{}
		for _, s := range segments {
			counts["segments"]++
			var parts []string
			for _, href := range s.hrefs {
				found := amiRange.FindStringSubmatch(href)
				if found == nil {
					return fmt.Errorf("%s: segment %s points at %q, which is not a word range", m.Inner, s.id, href)
				}
				wordsName := archive + "/words/" + found[1]
				words := loaded[wordsName]
				if words == nil {
					if words, err = readAMIWords(files, wordsName); err != nil {
						return err
					}
					loaded[wordsName] = words
				}
				text, err := words.span(found[2], found[3])
				if err != nil {
					return fmt.Errorf("%s: segment %s: %w", m.Inner, s.id, err)
				}
				if text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) == 0 {
				counts["segments without words"]++
				continue
			}
			if err := add(Item{ID: s.id, File: m.Inner, Kind: amiKind(meeting), Text: strings.Join(parts, " ")}); err != nil {
				return err
			}
		}
		for _, words := range loaded {
			for i, e := range words.elements {
				if e.word {
					counts["words"]++
					if !words.used[i] {
						counts["words outside segments"]++
					}
				}
			}
		}
		return nil
	})
	if err == nil && counts["segment files"] == 0 {
		err = errors.New("no segments/*.segments.xml")
	}
	return counts, err
}

type amiSegment struct {
	id    string
	hrefs []string
}

func readAMISegments(m Member) ([]amiSegment, error) {
	data, err := m.Read()
	if err != nil {
		return nil, err
	}
	name := m.Inner
	decoder := amiDecoder(data, name)
	var segments []amiSegment
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return segments, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 2 && t.Name.Local == "segment":
				id := attr(t, niteNamespace, "id")
				if id == "" {
					return nil, fmt.Errorf("%s: a segment has no nite:id", name)
				}
				segments = append(segments, amiSegment{id: id})
			case depth == 3 && t.Name.Space == niteNamespace && t.Name.Local == "child" && len(segments) > 0:
				segments[len(segments)-1].hrefs = append(segments[len(segments)-1].hrefs, attr(t, "", "href"))
			}
		case xml.EndElement:
			depth--
		}
	}
}

func readAMIWords(files *Files, name string) (*amiWords, error) {
	data, err := files.Read(name)
	if err != nil {
		return nil, err
	}
	decoder := amiDecoder(data, name)
	words := &amiWords{index: map[string]int{}}
	depth := 0
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			words.used = make([]bool, len(words.elements))
			return words, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth != 2 {
				continue
			}
			id := attr(t, niteNamespace, "id")
			if _, seen := words.index[id]; seen || id == "" {
				return nil, fmt.Errorf("%s: an element with no nite:id, or one given twice", name)
			}
			words.index[id] = len(words.elements)
			words.elements = append(words.elements, amiElement{word: t.Name.Local == "w",
				punc: attr(t, "", "punc") == "true"})
			text.Reset()
		case xml.CharData:
			if depth == 2 {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == 2 {
				last := &words.elements[len(words.elements)-1]
				if last.word {
					last.text = strings.TrimSpace(text.String())
				}
			}
			depth--
		}
	}
}

// span joins the words from one ID to another, both included.
func (w *amiWords) span(first, last string) (string, error) {
	if last == "" {
		last = first
	}
	from, ok := w.index[first]
	to, ok2 := w.index[last]
	if !ok || !ok2 || to < from {
		return "", errors.New("its word range is not in its words file")
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		w.used[i] = true
		e := w.elements[i]
		if !e.word || e.text == "" {
			continue
		}
		if b.Len() > 0 && !e.punc {
			b.WriteByte(' ')
		}
		b.WriteString(e.text)
	}
	return b.String(), nil
}

// amiDecoder reads XML that declares UTF-8 or ISO-8859-1. encoding/xml resolves no external entity.
func amiDecoder(data []byte, name string) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-8859-1", "latin1", "latin-1":
			// Each byte is the code point of the same number.
			raw, err := io.ReadAll(input)
			utf := make([]byte, 0, len(raw))
			for _, c := range raw {
				utf = utf8.AppendRune(utf, rune(c))
			}
			return bytes.NewReader(utf), err
		}
		return nil, fmt.Errorf("%s declares %s, which this converter does not read", path.Base(name), charset)
	}
	return decoder
}

func attr(t xml.StartElement, space, local string) string {
	for _, a := range t.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
