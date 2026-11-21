package evidence

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The headings of a card's last two sections.
const (
	SaidHeading    = "SAID, whole sentences as recorded; not record values"
	NotReadHeading = "NOT READ"
)

// AsShown is a saved unit as anything shows it. A unit sealed under today's rules is shown as sealed. One
// sealed under other rules has its text and speaker neutralized again, so it shows nothing that today's
// rules replace; the unit given, and the bytes it was sealed in, stay as they are. A unit that neutralizing
// again leaves as it is is given back itself.
func AsShown(unit *strictjson.Object) (*strictjson.Object, error) {
	if version, _ := unit.Get("sanitizer_version"); Equal(version, SanitizerVersion) {
		return unit, nil
	}
	shown := unit
	for _, key := range []string{"text", "speaker"} {
		value, _ := unit.Get(key)
		text, ok := value.(string)
		if !ok {
			continue
		}
		safe, _, err := SafeText(text)
		if err != nil {
			return nil, err
		}
		if safe != text {
			if shown == unit {
				shown = unit.Copy()
			}
			shown.Set(key, safe)
		}
	}
	// The identifier that decided a speaker holds a name or an address from a record.
	if matched, ok := unit.Get("matched"); ok {
		if m, isObject := matched.(*strictjson.Object); isObject && m != nil {
			value, _ := m.Get("value")
			if text, isText := value.(string); isText {
				safe, _, err := SafeText(text)
				if err != nil {
					return nil, err
				}
				if safe != text {
					renewed := m.Copy()
					renewed.Set("value", safe)
					if shown == unit {
						shown = unit.Copy()
					}
					shown.Set("matched", renewed)
				}
			}
		}
	}
	return shown, nil
}

var (
	saidUnit = regexp.MustCompile(`^    \[([0-9]+)\] `)
	// ErrCardText is a card whose text does not print its own units.
	ErrCardText = errors.New("the card's text does not print its units")
)

// AsShownText is a saved card's text as a model or a terminal is given it. A card made under today's rules
// is given its sealed text exactly. A card with a sentence sealed under an older neutralizer version is
// rendered again: each quotation under SAID from its unit as AsShown gives it, NOT READ from the card's
// gaps, and every other quoted literal, such as a speaker or a record value, neutralized again as the card's
// renderer neutralizes one, after a first line that says so. Its sealed text stays in its bytes, unchanged.
func AsShownText(card *strictjson.Object) (string, error) {
	sealed, _ := card.Get("text")
	text, ok := sealed.(string)
	if !ok {
		return "", ErrCardText
	}
	older := false
	if version, stated := card.Get("rules_version"); stated && !Equal(version, SanitizerVersion) {
		older = true
	}
	list, _ := card.Get("units")
	units := map[int]*strictjson.Object{}
	for _, value := range asList(list) {
		unit, _ := value.(*strictjson.Object)
		if unit == nil {
			return "", ErrCardText
		}
		n, _ := unit.Get("n")
		number, numbered := smallInt(n)
		if !numbered {
			return "", ErrCardText
		}
		units[number] = unit
		if kind, _ := unit.Get("kind"); kind == "sentence" {
			if version, _ := unit.Get("sanitizer_version"); !Equal(version, SanitizerVersion) {
				older = true
			}
		}
	}
	if !older {
		return text, nil
	}
	gaps, _ := card.Get("gaps")
	out := []string{fmt.Sprintf("This text is rendered again under neutralizer version %d for a card sealed "+
		"under an older version. The card's sealed text is unchanged in its bytes.", SanitizerVersion)}
	lines := strings.Split(text, "\n")
	said, quoting := false, (*strictjson.Object)(nil)
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == SaidHeading:
			said = true
		case line == NotReadHeading:
			// NOT READ is the card's gaps, each on its own line, as the card prints them.
			out = append(out, line)
			for _, value := range asList(gaps) {
				gap, _ := value.(*strictjson.Object)
				gapText, _ := gap.Get("text")
				words, isText := gapText.(string)
				if gap == nil || !isText {
					return "", ErrCardText
				}
				out = append(out, "  "+words)
			}
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  ") {
				i++
			}
			said = false
			continue
		case quoting != nil:
			// The line under a unit's heading is its quotation: the unit's text, as AsShown gives it.
			printed, err := strictjson.LoadsText(strings.TrimPrefix(line, "        "))
			if want, _ := quoting.Get("text"); err != nil || !strings.HasPrefix(line, "        ") || printed != want {
				return "", ErrCardText
			}
			shown, err := AsShown(quoting)
			if err != nil {
				return "", err
			}
			words, _ := shown.Get("text")
			out, quoting = append(out, "        "+strictjson.Quote(words.(string))), nil
			continue
		case said && saidUnit.MatchString(line):
			n, _ := strconv.Atoi(saidUnit.FindStringSubmatch(line)[1])
			if quoting = units[n]; quoting == nil {
				return "", ErrCardText
			}
		}
		renewed, err := neutralizedLiterals(line)
		if err != nil {
			return "", err
		}
		out = append(out, renewed)
	}
	return strings.Join(out, "\n"), nil
}

// asList is a JSON list, or nothing.
func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

// neutralizedLiterals is a line of a card with each quoted literal neutralized again and put on one line, as
// the card's renderer writes a field.
func neutralizedLiterals(line string) (string, error) {
	var b strings.Builder
	for {
		start := strings.IndexByte(line, '"')
		if start < 0 {
			b.WriteString(line)
			return b.String(), nil
		}
		end := start + 1
		for end < len(line) && line[end] != '"' {
			if line[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(line) {
			return "", ErrCardText
		}
		value, err := strictjson.LoadsText(line[start : end+1])
		literal, isText := value.(string)
		if err != nil || !isText {
			return "", ErrCardText
		}
		safe, _, err := SafeText(literal)
		if err != nil {
			return "", err
		}
		b.WriteString(line[:start] + strictjson.Quote(oneLine(safe)))
		line = line[end+1:]
	}
}
