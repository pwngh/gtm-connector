package calls

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pwngh/gtm-connector/internal/evidence"
)

// What debug calls import reads from its command line before it opens anything else: the parser a file name
// selects, the range of --lines or --cues, the time of --called-at, and the file itself. A choice that is
// refused gives the message the command prints.

// FormatOf is the parser a file's name selects, by its extension in either case: txt, vtt or srt.
func FormatOf(path string) (string, error) {
	name := filepath.Base(path)
	dot := strings.LastIndex(name, ".")
	// A name with nothing but dots before its last dot has no extension.
	if dot > 0 && strings.Trim(name[:dot], ".") != "" {
		switch format := asciiLower(name[dot+1:]); format {
		case "txt", "vtt", "srt":
			return format, nil
		}
	}
	return "", errors.New("Import an existing UTF-8 .txt, .vtt, or .srt transcript export.")
}

func asciiLower(text string) string {
	lowered := []byte(text)
	for i, c := range lowered {
		if c >= 'A' && c <= 'Z' {
			lowered[i] = c + 'a' - 'A'
		}
	}
	return string(lowered)
}

// UnitRange is the range --lines or --cues names: FIRST:LAST, counted from 1, with the end included. lines
// is whether it came from --lines, which only a TXT file takes; --cues is for VTT and SRT.
func UnitRange(text, format string, lines bool) (Range, error) {
	if lines != (format == "txt") {
		return Range{}, errors.New("Use --cues with VTT or SRT, or --lines with TXT.")
	}
	first, last, found := strings.Cut(text, ":")
	if !found || !count(first) || !count(last) {
		return Range{}, errors.New("Select an inclusive one-based range such as 3:8.")
	}
	selected := Range{number(first), number(last)}
	if selected.First > selected.Last {
		return Range{}, errors.New("The first selected unit must not follow the last selected unit.")
	}
	return selected, nil
}

// count is [1-9][0-9]{0,4}.
func count(text string) bool {
	return len(text) >= 1 && len(text) <= 5 && text[0] != '0' && all(text, digit)
}

func number(text string) int {
	n := 0
	for i := 0; i < len(text); i++ {
		n = n*10 + int(text[i]-'0')
	}
	return n
}

// CalledAt is the time --called-at names, in UTC as a manifest holds it. The time is ISO 8601 with a T and
// a UTC offset.
func CalledAt(text string) (string, error) {
	moment, ok := evidence.FromISOFormat(text)
	if !ok || !strings.Contains(text, "T") {
		return "", errors.New("--called-at requires an ISO timestamp with a UTC offset, such as 2027-01-15T10:00:00Z.")
	}
	if moment.Nanosecond() == 0 {
		return moment.Format("2006-01-02T15:04:05Z"), nil
	}
	return moment.Format("2006-01-02T15:04:05.000000Z"), nil
}

// ReadExport reads the file to import: a regular file, never through a link, of at most the 2 MiB an
// export may hold.
func ReadExport(path string) ([]byte, error) {
	checked, err := os.Lstat(path)
	if err != nil {
		return nil, ErrIO
	}
	if !checked.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrIO
	}
	defer file.Close()
	opened, err := file.Stat()
	switch {
	case err != nil:
		return nil, ErrIO
	case !opened.Mode().IsRegular():
		return nil, ErrInvalid
	case opened.Size() > evidence.MaxRawBytes:
		return nil, ErrTooLarge
	}
	// One byte past the limit, so a file that grew while it was read is still refused as too large.
	raw, err := io.ReadAll(io.LimitReader(file, evidence.MaxRawBytes+1))
	if err != nil {
		return nil, ErrIO
	}
	return raw, nil
}
