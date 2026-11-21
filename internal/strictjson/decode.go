package strictjson

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
)

// The refusals of a document that Loads reads but that is not of its format. Neither quotes the document.
var (
	ErrUnknownMember = errors.New("strictjson: the document has a member its format does not name")
	ErrWrongType     = errors.New("strictjson: a value of the document is not of its field's type")
)

// ErrByteOrderMark refuses a document that starts with a UTF-8 byte-order mark, which Loads would skip. The
// program never writes one, and encoding/json refuses one.
var ErrByteOrderMark = errors.New("strictjson: the document starts with a byte-order mark")

// ErrUnsupportedType refuses a type Decode cannot fill, before any document is read.
var ErrUnsupportedType = errors.New("strictjson: Decode cannot fill this type")

// Decode reads one document with Loads and fills into from it. into points to a string, a bool, an int, a
// slice or a struct of those, the struct's fields named by their json tags. It is for the formats the
// program writes itself, and reads them exactly:
//
//   - a member is matched to a field by its whole name, and a member no field names is refused;
//   - a value must be of its field's type: a number with a fraction or an exponent is not an int, and null
//     is only an empty slice, as encoding/json writes one;
//   - a field the document leaves out keeps its zero value, for the caller to check;
//   - a leading byte-order mark is refused.
//
// The type is checked once, before any document is read, and the check is kept for the type: a field of
// another kind, a tagged field that is not exported, or two fields of one name is refused with
// ErrUnsupportedType whatever the document holds.
func Decode(document []byte, into any) error {
	target := reflect.ValueOf(into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("%w: %T is not a pointer to a value", ErrUnsupportedType, into)
	}
	s, err := shapeOf(target.Type().Elem())
	if err != nil {
		return err
	}
	if bytes.HasPrefix(document, []byte("\xef\xbb\xbf")) {
		return ErrByteOrderMark
	}
	value, err := Loads(document)
	if err != nil {
		return err
	}
	return fill(target.Elem(), s, value)
}

// shape is what Decode reads into a type: its kind, the shape of a slice's items, and a struct's fields by
// their names.
type shape struct {
	kind   reflect.Kind
	item   *shape
	fields map[string]field
}

type field struct {
	index int
	shape *shape
}

type checked struct {
	shape *shape
	err   error
}

// shapes holds the check of each type Decode has been given.
var shapes sync.Map

func shapeOf(t reflect.Type) (*shape, error) {
	if c, ok := shapes.Load(t); ok {
		return c.(checked).shape, c.(checked).err
	}
	s, err := build(t, map[reflect.Type]*shape{})
	shapes.Store(t, checked{s, err})
	return s, err
}

// build checks a type and gives its shape. A type met again inside itself is given the shape being built.
func build(t reflect.Type, building map[reflect.Type]*shape) (*shape, error) {
	if s, ok := building[t]; ok {
		return s, nil
	}
	s := &shape{kind: t.Kind()}
	building[t] = s
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64:
	case reflect.Slice:
		item, err := build(t.Elem(), building)
		if err != nil {
			return nil, err
		}
		s.item = item
	case reflect.Struct:
		s.fields = map[string]field{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			if !f.IsExported() {
				return nil, fmt.Errorf("%w: %s.%s is not exported", ErrUnsupportedType, t, f.Name)
			}
			if _, named := s.fields[name]; named {
				return nil, fmt.Errorf("%w: two fields of %s are named %q", ErrUnsupportedType, t, name)
			}
			inner, err := build(f.Type, building)
			if err != nil {
				return nil, err
			}
			s.fields[name] = field{i, inner}
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, t)
	}
	return s, nil
}

func fill(target reflect.Value, s *shape, value any) error {
	switch s.kind {
	case reflect.String:
		text, ok := value.(string)
		if !ok {
			return ErrWrongType
		}
		target.SetString(text)
	case reflect.Bool:
		flag, ok := value.(bool)
		if !ok {
			return ErrWrongType
		}
		target.SetBool(flag)
	case reflect.Int, reflect.Int64:
		n, ok := value.(*big.Int)
		if !ok || !n.IsInt64() || target.OverflowInt(n.Int64()) {
			return ErrWrongType
		}
		target.SetInt(n.Int64())
	case reflect.Slice:
		if value == nil {
			target.SetZero()
			return nil
		}
		list, ok := value.([]any)
		if !ok {
			return ErrWrongType
		}
		target.Set(reflect.MakeSlice(target.Type(), len(list), len(list)))
		for i, item := range list {
			if err := fill(target.Index(i), s.item, item); err != nil {
				return err
			}
		}
	case reflect.Struct:
		object, ok := value.(*Object)
		if !ok {
			return ErrWrongType
		}
		for i, key := range object.Keys {
			f, known := s.fields[key]
			if !known {
				return ErrUnknownMember
			}
			if err := fill(target.Field(f.index), f.shape, object.Values[i]); err != nil {
				return err
			}
		}
	}
	return nil
}
