package mcp

import (
	"bytes"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A body nested past strictjson.MaxDepth is never parsed, so what it asks for is read here without descending:
// the members of one object, each value kept as its bytes, with a nested value skipped by counting brackets.

// shallowCall reads a JSON-RPC request's method and, from its params, the tool name, looking no deeper than
// params' own members. named is false when params names no tool as a string. A body that is not an object
// gives no method.
func shallowCall(body []byte) (method, name string, named bool) {
	members := shallowMembers(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	method, _ = shallowString(members["method"])
	name, named = shallowString(shallowMembers(members["params"])["name"])
	return method, name, named
}

// shallowString is a member's value when it is a JSON string.
func shallowString(value []byte) (string, bool) {
	if len(value) == 0 || value[0] != '"' {
		return "", false
	}
	decoded, err := strictjson.Loads(value)
	text, isText := decoded.(string)
	return text, err == nil && isText
}

// shallowMembers gives the members of one JSON object by name, each value as the bytes it spans. A repeated
// name keeps its last value, as json.loads does. Anything that is not one whole object gives nil.
func shallowMembers(data []byte) map[string][]byte {
	i := skipSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil
	}
	members := map[string][]byte{}
	i = skipSpace(data, i+1)
	if i < len(data) && data[i] == '}' {
		return members
	}
	for {
		end := stringEnd(data, i)
		if end < 0 {
			return nil
		}
		key, isText := shallowString(data[i:end])
		i = skipSpace(data, end)
		if !isText || i >= len(data) || data[i] != ':' {
			return nil
		}
		i = skipSpace(data, i+1)
		end = valueEnd(data, i)
		if end < 0 {
			return nil
		}
		members[key] = data[i:end]
		i = skipSpace(data, end)
		switch {
		case i < len(data) && data[i] == ',':
			i = skipSpace(data, i+1)
		case i < len(data) && data[i] == '}':
			return members
		default:
			return nil
		}
	}
}

func skipSpace(data []byte, i int) int {
	for i < len(data) && strings.IndexByte(" \t\n\r", data[i]) >= 0 {
		i++
	}
	return i
}

// stringEnd is the index after the string that opens at i, or -1.
func stringEnd(data []byte, i int) int {
	if i >= len(data) || data[i] != '"' {
		return -1
	}
	for i++; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// valueEnd is the index after the value that starts at i, or -1. An array or an object ends at its matching
// bracket, found by counting and never by recursion.
func valueEnd(data []byte, i int) int {
	if i >= len(data) {
		return -1
	}
	switch data[i] {
	case '"':
		return stringEnd(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				end := stringEnd(data, i)
				if end < 0 {
					return -1
				}
				i = end
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return -1
	}
	start := i
	for i < len(data) && strings.IndexByte(",}] \t\n\r", data[i]) < 0 {
		i++
	}
	if i == start {
		return -1
	}
	return i
}
