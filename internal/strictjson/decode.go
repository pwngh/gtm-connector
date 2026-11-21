package strictjson

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
)

// The refusals of a document that Loads reads but that is not of its format. Neither quotes the document.
var (
	ErrUnknownMember = errors.New("strictjson: the document has a member its format does not name")
	ErrWrongType     = errors.New("strictjson: a value of the document is not of its field's type")
)

// Decode reads one document with Loads and fills into from it. into points to a string, a bool, an int, a
// slice or a struct of those, the struct's fields named by their json tags. It is for the formats the
// program writes itself, and reads them exactly:
//
//   - a member is matched to a field by its whole name, and a member no field names is refused;
//   - a value must be of its field's type: a number with a fraction or an exponent is not an int, and null
//     is only an empty slice, as encoding/json writes one;
//   - a field the document leaves out keeps its zero value, for the caller to check.
func Decode(document []byte, into any) error {
	value, err := Loads(document)
	if err != nil {
		return err
	}
	return fill(reflect.ValueOf(into).Elem(), value)
}

func fill(target reflect.Value, value any) error {
	switch target.Kind() {
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
			if err := fill(target.Index(i), item); err != nil {
				return err
			}
		}
	case reflect.Struct:
		object, ok := value.(*Object)
		if !ok {
			return ErrWrongType
		}
		fields := map[string]reflect.Value{}
		for i := 0; i < target.NumField(); i++ {
			if name, _, _ := strings.Cut(target.Type().Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
				fields[name] = target.Field(i)
			}
		}
		for i, key := range object.Keys {
			field, known := fields[key]
			if !known {
				return ErrUnknownMember
			}
			if err := fill(field, object.Values[i]); err != nil {
				return err
			}
		}
	default:
		panic(fmt.Sprintf("strictjson: Decode into a %s", target.Kind()))
	}
	return nil
}
