package calls

import (
	"errors"
	"math/big"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Attendee is one row of a call's attendee list: the speaker label the transcript uses and that person's
// email. Two rows with one label are people behind one microphone, such as a meeting room.
type Attendee struct {
	Label, Email string
}

// MaxAttendeeFileBytes is the largest attendee file debug calls import reads.
const MaxAttendeeFileBytes = 64 * 1024

// ReadAttendees reads the attendee file given with --attendees, with the same file safety as ReadExport, and
// checks it with ParseAttendees. A file over MaxAttendeeFileBytes is ErrAttendeesTooLarge; a file that is not
// an attendee list, or that is replaced while it is read, is ErrAttendeesInvalid; one that cannot be read is
// ErrIO.
func ReadAttendees(path string) ([]Attendee, error) {
	raw, err := readLimited(path, MaxAttendeeFileBytes, nil)
	switch {
	case errors.Is(err, ErrTooLarge):
		return nil, ErrAttendeesTooLarge
	case errors.Is(err, ErrInvalid):
		return nil, ErrAttendeesInvalid
	case err != nil:
		return nil, err
	}
	return ParseAttendees(raw)
}

// ParseAttendees reads an attendee file: strict JSON, exactly
// {"schema_version": 1, "attendees": [{"label": LABEL, "email": EMAIL}, ...]}, with 1 to 200 rows, each a
// label that is not empty on one line and has at most 200 characters, and an email evidence.ValidEmail
// accepts, and no row twice. Anything else is ErrAttendeesInvalid, which carries no text of the file.
func ParseAttendees(raw []byte) ([]Attendee, error) {
	value, err := strictjson.Loads(raw)
	if err != nil {
		return nil, ErrAttendeesInvalid
	}
	document, ok := value.(*strictjson.Object)
	if !ok || document == nil || len(document.Keys) != 2 {
		return nil, ErrAttendeesInvalid
	}
	version, _ := document.Get("schema_version")
	rows, _ := document.Get("attendees")
	if n, isInt := version.(*big.Int); !isInt || n.Cmp(big.NewInt(1)) != 0 || !evidence.ValidAttendees(rows) {
		return nil, ErrAttendeesInvalid
	}
	list := rows.([]any)
	out := make([]Attendee, len(list))
	for i, row := range list {
		label, _ := row.(*strictjson.Object).Get("label")
		email, _ := row.(*strictjson.Object).Get("email")
		out[i] = Attendee{Label: label.(string), Email: email.(string)}
	}
	return out, nil
}

// attendeeValues is an attendee list as a manifest holds it.
func attendeeValues(attendees []Attendee) []any {
	out := make([]any, len(attendees))
	for i, a := range attendees {
		out[i] = strictjson.NewObject("label", a.Label, "email", a.Email)
	}
	return out
}
