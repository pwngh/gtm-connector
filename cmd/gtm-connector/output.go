package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// What the read commands print. Data goes to stdout; errors and notices go to stderr. With --json a result
// is json.dumps(result, indent=2, ensure_ascii=False) and an error is one object on stderr. Without it the
// output is text: a pipe gets tab-separated rows without headings or color.

// failure ends a command: its exit code, what went wrong, and the command to run next.
type failure struct {
	code    int
	message string
	next    string // empty for the command's own help
}

func (f *failure) Error() string { return f.message }

// refused is a refusal of what was asked for: exit 2.
func refused(format string, values ...any) *failure {
	return &failure{code: 2, message: fmt.Sprintf(format, values...)}
}

// The JSON error code of each exit code.
var exitNames = map[int]string{1: "local_failure", 2: "usage_error", 3: "refused", 4: "check_failed",
	5: "upstream_failure", 130: "interrupted"}

// screen is where one command writes.
type screen struct {
	out, err io.Writer
	json     bool
	terminal bool // out is a terminal, so tables get headings
	bold     bool // and the headings are bold
}

// newScreen is the process's own output.
func newScreen(asJSON bool) screen {
	s := screen{out: os.Stdout, err: os.Stderr, json: asJSON}
	if info, err := os.Stdout.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		_, plain := os.LookupEnv("NO_COLOR")
		s.terminal, s.bold = true, !plain && os.Getenv("TERM") != "dumb"
	}
	return s
}

// fail writes a failure and gives its exit code.
func (s screen) fail(command string, f *failure) int {
	next := f.next
	if next == "" {
		next = "gtm-connector " + command + " --help"
	}
	if s.json {
		text, _ := strictjson.Dumps(strictjson.NewObject("error", strictjson.NewObject(
			"code", exitNames[f.code], "message", f.message, "next", next)), strictjson.DumpOptions{EnsureASCII: true})
		fmt.Fprintln(s.err, text)
	} else {
		fmt.Fprintf(s.err, "Error: %s\nNext: %s\n", oneLine(f.message), oneLine(next))
	}
	return f.code
}

// oneLine keeps a source value or an error on one visible terminal line: a character that does not print
// is written as its JSON escape.
func oneLine(text string) string {
	var b strings.Builder
	for _, c := range text {
		if unicodetab.IsPrintable(c) {
			b.WriteRune(c)
			continue
		}
		escaped, _ := strictjson.Dumps(string(c), strictjson.DumpOptions{EnsureASCII: true})
		b.WriteString(escaped[1 : len(escaped)-1])
	}
	return b.String()
}

// cellText is Python's str() of a table cell.
func cellText(cell any) string {
	if number, ok := cell.(float64); ok {
		return strictjson.FloatRepr(number)
	}
	if text, ok := evidence.PyStr(cell); ok {
		return text
	}
	return fmt.Sprint(cell)
}

// table is the rows under their headings on a terminal, and whole tab-separated rows in a pipe. A missing
// cell reads "unknown".
func (s screen) table(headers []string, rows [][]any) string {
	cells := make([][]string, len(rows))
	for i, row := range rows {
		cells[i] = make([]string, len(row))
		for j, cell := range row {
			if cell == nil {
				cell = "unknown"
			}
			cells[i][j] = oneLine(cellText(cell))
		}
	}
	lines := make([]string, 0, len(rows)+1)
	if !s.terminal {
		for _, row := range cells {
			lines = append(lines, strings.Join(row, "\t"))
		}
		return strings.Join(lines, "\n")
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = utf8.RuneCountInString(header)
		for _, row := range cells {
			widths[i] = max(widths[i], utf8.RuneCountInString(row[i]))
		}
	}
	padded := func(row []string) string {
		parts := make([]string, len(row))
		for i, cell := range row {
			parts[i] = cell + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	heading := padded(headers)
	if s.bold {
		heading = "\033[1m" + heading + "\033[0m"
	}
	lines = append(lines, heading)
	for _, row := range cells {
		lines = append(lines, padded(row))
	}
	return strings.Join(lines, "\n")
}

// literal is a source value as the card prints one: a string as a quoted literal, an empty value as Missing.
func literal(value any) any {
	if text, ok := value.(string); ok && text != "" {
		return strictjson.Quote(text)
	}
	if value == nil || value == "" {
		return "Missing"
	}
	return value
}

// shellQuote is shlex.quote: a word the shell reads as this text.
func shellQuote(text string) string {
	if text == "" {
		return "''"
	}
	for _, c := range []byte(text) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_@%+=:,./-", c) >= 0) {
			return "'" + strings.ReplaceAll(text, "'", `'"'"'`) + "'"
		}
	}
	return text
}

// rounded is round(value, 2), as the audit table shows an elapsed time.
func rounded(value float64) float64 {
	twoPlaces, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	return twoPlaces
}

// get is a member of a JSON object, or nil.
func get(o *strictjson.Object, key string) any {
	value, _ := o.Get(key)
	return value
}

// neutral makes the source text in every string of a result inert for display, as a card shows it.
func neutral(value any) (any, error) {
	switch v := value.(type) {
	case string:
		safe, _, err := evidence.SafeText(v)
		return safe, err
	case []any:
		list := make([]any, len(v))
		for i, item := range v {
			var err error
			if list[i], err = neutral(item); err != nil {
				return nil, err
			}
		}
		return list, nil
	case *strictjson.Object:
		if v == nil {
			return v, nil
		}
		o := &strictjson.Object{Keys: append([]string(nil), v.Keys...), Values: make([]any, len(v.Values))}
		for i, item := range v.Values {
			var err error
			if o.Values[i], err = neutral(item); err != nil {
				return nil, err
			}
		}
		return o, nil
	}
	return value, nil
}
