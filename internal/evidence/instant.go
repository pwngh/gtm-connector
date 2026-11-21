package evidence

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrDay refuses a day that date.fromisoformat reads but that is not written YYYY-MM-DD, such as 20270115
// or 2027-W03-5 (units._day's BriefRejected).
var ErrDay = errors.New("Evidence dates must use YYYY-MM-DD.")

// ErrNotADay is a text date.fromisoformat refuses, or a day outside the calendar (its ValueError).
var ErrNotADay = errors.New("evidence: not an ISO date")

// DateError is date.fromisoformat's ValueError, with its message. It is ErrNotADay.
type DateError struct{ Message string }

func (e *DateError) Error() string        { return e.Message }
func (e *DateError) Is(target error) bool { return target == ErrNotADay }

// DateFromISO is date.fromisoformat: YYYY-MM-DD, YYYYMMDD, or an ISO week date, at midnight UTC.
func DateFromISO(value string) (time.Time, error) {
	n := len(value)
	if n == 7 || n == 8 || n == 10 {
		if year, month, day, ok := isoText(value).date(n); ok {
			switch {
			case year < 1 || year > 9999:
				return time.Time{}, &DateError{fmt.Sprintf("year must be in 1..9999, not %d", year)}
			case month < 1 || month > 12:
				return time.Time{}, &DateError{fmt.Sprintf("month must be in 1..12, not %d", month)}
			case day < 1 || day > daysIn(year, month):
				return time.Time{}, &DateError{fmt.Sprintf("day %d must be in range 1..%d for month %d in year %d",
					day, daysIn(year, month), month, year)}
			}
			return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, &DateError{"Invalid isoformat string: " + pyRepr([]rune(value))}
}

// Day is units._day: a real day written exactly YYYY-MM-DD, in years 1 to 9999, at midnight UTC. A text
// date.fromisoformat reads in another form gives ErrDay; one it refuses gives its DateError.
func Day(value string) (time.Time, error) {
	d, err := DateFromISO(value)
	if err != nil {
		return time.Time{}, err
	}
	if d.Format("2006-01-02") != value {
		return time.Time{}, ErrDay
	}
	return d, nil
}

func digits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func leap(year int) bool { return year%4 == 0 && (year%100 != 0 || year%400 == 0) }

func daysIn(year, month int) int {
	switch month {
	case 2:
		if leap(year) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

func validDate(year, month, day int) bool {
	return year >= 1 && year <= 9999 && month >= 1 && month <= 12 && day >= 1 && day <= daysIn(year, month)
}

// PlainInstant is _PLAIN_INSTANT.fullmatch: a time a card prints as given,
// YYYY-MM-DDTHH:MM:SS[.f{1,6}](Z|+HH:MM|-HH:MM) with an offset hour of at most 23.
func PlainInstant(s string) bool {
	if len(s) < 20 {
		return false
	}
	for i, c := range []byte(s[:19]) {
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		case 10:
			if c != 'T' {
				return false
			}
		case 13, 16:
			if c != ':' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	rest := s[19:]
	if rest[0] == '.' {
		n := 1
		for n < len(rest) && n <= 7 && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 1 || n > 7 {
			return false
		}
		rest = rest[n:]
	}
	if rest == "Z" {
		return true
	}
	if len(rest) != 6 || rest[0] != '+' && rest[0] != '-' || rest[3] != ':' {
		return false
	}
	h, okH := digits(rest[1:3])
	m, okM := digits(rest[4:6])
	return okH && okM && h <= 23 && m <= 59
}

// Instant is units._instant for a string: datetime.fromisoformat after every Z is read as +00:00, in UTC.
// It is false when the text is not an ISO time, has no offset, or leaves years 1 to 9999 in UTC.
func Instant(value string) (time.Time, bool) {
	return FromISOFormat(strings.ReplaceAll(value, "Z", "+00:00"))
}

// FromISOFormat is CPython 3.14's datetime.fromisoformat (Modules/_datetimemodule.c), which reads the text
// as UTF-8 bytes, for aware results only.
func FromISOFormat(text string) (time.Time, bool) {
	if utf8.RuneCountInString(text) < 7 {
		return time.Time{}, false
	}
	p := isoText(text)
	separator := p.separator()
	year, month, day, ok := p.date(separator)
	if !ok || len(p) <= separator {
		// A date alone has no offset.
		return time.Time{}, false
	}
	// The separator is any one character, so the time starts after its last byte.
	_, size := utf8.DecodeRuneInString(string(p[separator:]))
	t := p[separator+size:]
	hour, minute, second, micro, offset, offsetMicro, aware, ok := t.time()
	if !ok || !aware {
		return time.Time{}, false
	}
	if hour == 24 && month <= 12 && day <= daysIn(year, month) {
		if minute != 0 || second != 0 || micro != 0 {
			return time.Time{}, false
		}
		hour, day = 0, day+1
		if day > daysIn(year, month) {
			day, month = 1, month+1
			if month > 12 {
				month, year = 1, year+1
			}
		}
	}
	if !validDate(year, month, day) || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	total := time.Duration(offset)*time.Second + time.Duration(offsetMicro)*time.Microsecond
	if offset == 0 {
		total = 0 // An offset of zero seconds is UTC, whatever its fraction.
	}
	if total <= -24*time.Hour || total >= 24*time.Hour {
		return time.Time{}, false
	}
	moment := time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, time.UTC).Add(-total)
	if moment.Year() < 1 || moment.Year() > 9999 {
		return time.Time{}, false // astimezone overflows
	}
	return moment, true
}

// isoText reads like the C parser's NUL-terminated buffer: past the end is a zero byte.
type isoText string

func (s isoText) at(i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// number is parse_digits: exactly n ASCII digits from i.
func (s isoText) number(i, n int) (int, int, bool) {
	value := 0
	for k := 0; k < n; k++ {
		c := s.at(i + k)
		if !isDigit(c) {
			return 0, 0, false
		}
		value = value*10 + int(c-'0')
	}
	return value, i + n, true
}

// separator is _find_isoformat_datetime_separator; -1 when there is none.
func (s isoText) separator() int {
	n := len(s)
	if n == 7 {
		return 7
	}
	if s.at(4) == '-' {
		if s.at(5) != 'W' {
			return 10
		}
		if n < 8 {
			return -1
		}
		if n > 8 && s.at(8) == '-' {
			if n == 9 {
				return -1
			}
			if n > 10 && isDigit(s.at(10)) {
				return 8
			}
			return 10
		}
		return 8
	}
	if s.at(4) == 'W' {
		i := 7
		for i < n && isDigit(s.at(i)) {
			i++
		}
		if i < 9 {
			return i
		}
		if i%2 == 0 {
			return 7
		}
		return 8
	}
	return 8
}

// date is parse_isoformat_date, with the ISO week form converted by iso_to_ymd. The year, month and day
// are checked later, as new_datetime checks them.
func (s isoText) date(length int) (year, month, day int, ok bool) {
	if length < 0 {
		return 0, 0, 0, false
	}
	year, p, ok := s.number(0, 4)
	if !ok {
		return 0, 0, 0, false
	}
	dashed := s.at(p) == '-'
	if dashed {
		p++
	}
	if s.at(p) == 'W' {
		week, q, ok := s.number(p+1, 2)
		if !ok {
			return 0, 0, 0, false
		}
		weekday := 1
		if q < length {
			if dashed {
				if s.at(q) != '-' {
					return 0, 0, 0, false
				}
				q++
			}
			if weekday, _, ok = s.number(q, 1); !ok {
				return 0, 0, 0, false
			}
		}
		return isoWeekDate(year, week, weekday)
	}
	if month, p, ok = s.number(p, 2); !ok {
		return 0, 0, 0, false
	}
	if dashed {
		if s.at(p) != '-' {
			return 0, 0, 0, false
		}
		p++
	}
	if day, _, ok = s.number(p, 2); !ok {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

// isoWeekDate is iso_to_ymd.
func isoWeekDate(year, week, weekday int) (int, int, int, bool) {
	if year < 1 || year > 9999 {
		return 0, 0, 0, false
	}
	jan1 := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	first := (int(jan1.Weekday()) + 6) % 7 // Monday is 0
	if week <= 0 || week >= 53 {
		if week != 53 || !(first == 3 || first == 2 && leap(year)) {
			return 0, 0, 0, false
		}
	}
	if weekday <= 0 || weekday >= 8 {
		return 0, 0, 0, false
	}
	// The Monday of week 1 is the Monday on or before January 4.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	d := monday.AddDate(0, 0, (week-1)*7+weekday-1)
	return d.Year(), int(d.Month()), d.Day(), true
}

// time is parse_isoformat_time: the clock and, when there is one, the offset in seconds and microseconds.
func (s isoText) time() (hour, minute, second, micro, offset, offsetMicro int, aware, ok bool) {
	if len(s) == 0 {
		return
	}
	zone := 0
	for zone < len(s) && s[zone] != 'Z' && s[zone] != '+' && s[zone] != '-' {
		zone++
	}
	var more int
	if hour, minute, second, micro, more, ok = s.clock(0, zone); !ok {
		return
	}
	if zone == len(s) {
		ok = more == 0
		return
	}
	if s[zone] == 'Z' {
		ok, aware = s.at(zone+1) == 0, true
		return
	}
	sign := 1
	if s[zone] == '-' {
		sign = -1
	}
	h, m, sec, us, rest, valid := s.clock(zone+1, len(s))
	if !valid || rest != 0 {
		ok = false
		return
	}
	return hour, minute, second, micro, sign * (h*3600 + m*60 + sec), sign * us, true, true
}

// clock is parse_hh_mm_ss_ff over s[start:end]. more is 1 when the field stopped short of a NUL, as the C
// function returns it.
func (s isoText) clock(start, end int) (hour, minute, second, micro, more int, ok bool) {
	values := [3]*int{&hour, &minute, &second}
	p, separated := start, true
	for i := 0; i < 3; i++ {
		var v int
		if v, p, ok = s.number(p, 2); !ok {
			return
		}
		*values[i] = v
		c := s.at(p)
		p++
		if i == 0 {
			separated = c == ':'
		}
		switch {
		case p >= end:
			if c != 0 {
				more = 1
			}
			return hour, minute, second, micro, more, true
		case separated && c == ':':
			if i == 2 {
				return 0, 0, 0, 0, 0, false
			}
			continue
		case c == '.' || c == ',':
			if i < 2 {
				return 0, 0, 0, 0, 0, false
			}
		case !separated:
			p--
			continue
		default:
			return 0, 0, 0, 0, 0, false
		}
		break
	}
	n := min(end-p, 6)
	if micro, p, ok = s.number(p, n); !ok {
		return
	}
	if n < 6 {
		for k := n; k < 6; k++ {
			micro *= 10
		}
	}
	for isDigit(s.at(p)) {
		p++
	}
	if s.at(p) != 0 {
		more = 1
	}
	return hour, minute, second, micro, more, true
}
