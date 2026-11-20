package strictjson

import "math/big"

// NewObject builds an Object from alternating keys and values, in that order.
func NewObject(pairs ...any) *Object {
	o := &Object{}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// Get is a member's value and whether the object has it.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	for i, k := range o.Keys {
		if k == key {
			return o.Values[i], true
		}
	}
	return nil, false
}

// Set replaces a member's value in place, or adds the member at the end, as a Python dict does.
func (o *Object) Set(key string, value any) {
	for i, k := range o.Keys {
		if k == key {
			o.Values[i] = value
			return
		}
	}
	o.Keys = append(o.Keys, key)
	o.Values = append(o.Values, value)
}

// Delete removes a member and keeps the order of the rest.
func (o *Object) Delete(key string) {
	for i, k := range o.Keys {
		if k == key {
			o.Keys = append(o.Keys[:i:i], o.Keys[i+1:]...)
			o.Values = append(o.Values[:i:i], o.Values[i+1:]...)
			return
		}
	}
}

// Copy is a shallow copy: the members are new, their values shared.
func (o *Object) Copy() *Object {
	return &Object{Keys: append([]string(nil), o.Keys...), Values: append([]any(nil), o.Values...)}
}

// Clone is a deep copy of a JSON value: objects, lists and integers are new, so nothing the copy holds is
// shared with the original.
func Clone(value any) any {
	switch v := value.(type) {
	case *Object:
		if v == nil {
			return v
		}
		o := &Object{Keys: append([]string(nil), v.Keys...), Values: make([]any, len(v.Values))}
		for i, item := range v.Values {
			o.Values[i] = Clone(item)
		}
		return o
	case []any:
		l := make([]any, len(v))
		for i, item := range v {
			l[i] = Clone(item)
		}
		return l
	case []string:
		return append([]string(nil), v...)
	case *big.Int:
		if v == nil {
			return v
		}
		return new(big.Int).Set(v)
	}
	return value
}
