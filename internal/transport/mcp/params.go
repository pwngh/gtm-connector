package mcp

import (
	"math"
	"math/big"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The Python SDK validates a 2026-07-28 request against that revision's schema (mcp_types.methods,
// validate_client_request) before it looks for a handler, and answers a mis-shaped one with -32602
// "Invalid request parameters". These are the schema's rules for the methods that reach that step, with
// pydantic's lax coercions where the schema's types allow them, and members it does not name ignored.

// schemaMethods have a 2026-07-28 request schema. Any other method, a removed one like ping included, goes
// straight to the handler lookup.
var schemaMethods = map[string]bool{"completion/complete": true, "prompts/get": true, "prompts/list": true,
	"resources/list": true, "resources/read": true, "resources/templates/list": true, "server/discover": true,
	"subscriptions/listen": true, "tools/call": true, "tools/list": true}

// servedMethods are the requests the Python gateway has handlers for; every other one is its 404.
var servedMethods = map[string]bool{"server/discover": true, "tools/list": true, "tools/call": true,
	"resources/list": true, "prompts/list": true, "resources/templates/list": true}

const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaLogLevel           = "io.modelcontextprotocol/logLevel"
)

// validParams reports whether a request's params satisfy its method's 2026-07-28 schema.
func validParams(method string, params *strictjson.Object) bool {
	if params == nil {
		return false
	}
	meta, present := member(params, "_meta")
	if !present || !validMeta(meta) {
		return false
	}
	field := func(name string) (any, bool) { return member(params, name) }
	switch method {
	case "tools/list", "prompts/list", "resources/list", "resources/templates/list":
		return optional(field("cursor"))(isString)
	case "tools/call":
		name, ok := field("name")
		// inputResponses answer an input request; no tool here makes one, so any value is refused.
		responses, given := field("inputResponses")
		return ok && isString(name) && optional(field("arguments"))(isObject) && optional(field("requestState"))(isString) &&
			(!given || responses == nil)
	case "completion/complete":
		argument, ok := field("argument")
		ref, refOK := field("ref")
		return ok && refOK && validObject(argument, map[string]bool{"name": true, "value": true}, map[string]func(any) bool{
			"name": isString, "value": isString}) && validReference(ref) &&
			optional(field("context"))(func(v any) bool {
				return validObject(v, nil, map[string]func(any) bool{"arguments": func(a any) bool { return isMapOf(a, isString) }})
			})
	case "subscriptions/listen":
		filter, ok := field("notifications")
		return ok && validObject(filter, nil, map[string]func(any) bool{
			"promptsListChanged": isLaxBool, "resourcesListChanged": isLaxBool, "toolsListChanged": isLaxBool,
			"resourceSubscriptions": func(v any) bool { return isListOf(v, isString) }})
	case "prompts/get", "resources/read", "server/discover":
		// prompts/get and resources/read are refused before this step; discovery takes only _meta.
		return true
	}
	return true
}

// validMeta is the RequestMetaObject schema: the version is a string, the capabilities and the optional
// client identity are well formed, and the log level and progress token have their types. Other keys are free.
func validMeta(meta any) bool {
	object, ok := meta.(*strictjson.Object)
	if !ok || object == nil {
		return false
	}
	version, hasVersion := member(object, metaProtocolVersion)
	capabilities, hasCapabilities := member(object, metaClientCapabilities)
	return hasVersion && isString(version) && hasCapabilities && validCapabilities(capabilities) &&
		optional(member(object, metaClientInfo))(validImplementation) &&
		optional(member(object, metaLogLevel))(oneOf("alert", "critical", "debug", "emergency", "error", "info", "notice", "warning")) &&
		optional(member(object, "progressToken"))(func(v any) bool { return isString(v) || isLaxInt(v) })
}

func validCapabilities(value any) bool {
	jsonObject := func(v any) bool { return isMapOf(v, isAny) }
	return validObject(value, nil, map[string]func(any) bool{
		"elicitation": func(v any) bool {
			return validObject(v, nil, map[string]func(any) bool{"form": jsonObject, "url": jsonObject})
		},
		"experimental": func(v any) bool { return isMapOf(v, jsonObject) },
		"extensions":   func(v any) bool { return isMapOf(v, jsonObject) },
		"roots":        jsonObject,
		"sampling": func(v any) bool {
			return validObject(v, nil, map[string]func(any) bool{"context": jsonObject, "tools": jsonObject})
		},
	})
}

func validImplementation(value any) bool {
	icon := func(v any) bool {
		return validObject(v, map[string]bool{"src": true}, map[string]func(any) bool{"src": isString, "mimeType": isString,
			"sizes": func(s any) bool { return isListOf(s, isString) }, "theme": oneOf("dark", "light")})
	}
	return validObject(value, map[string]bool{"name": true, "version": true}, map[string]func(any) bool{
		"name": isString, "version": isString, "title": isString, "description": isString, "websiteUrl": isString,
		"icons": func(v any) bool { return isListOf(v, icon) }})
}

func validReference(value any) bool {
	prompt := validObject(value, map[string]bool{"name": true, "type": true}, map[string]func(any) bool{
		"name": isString, "title": isString, "type": oneOf("ref/prompt")})
	resource := validObject(value, map[string]bool{"type": true, "uri": true}, map[string]func(any) bool{
		"type": oneOf("ref/resource"), "uri": isString})
	return prompt || resource
}

// validObject checks a model: an object with its required members, each named member valid or null. A
// required member may not be null unless its rule allows it.
func validObject(value any, required map[string]bool, rules map[string]func(any) bool) bool {
	object, ok := value.(*strictjson.Object)
	if !ok || object == nil {
		return false
	}
	for name := range required {
		if v, present := member(object, name); !present || !rules[name](v) {
			return false
		}
	}
	for name, rule := range rules {
		if required[name] {
			continue
		}
		if !optional(member(object, name))(rule) {
			return false
		}
	}
	return true
}

// optional accepts an absent or null member, and otherwise applies the rule.
func optional(value any, present bool) func(func(any) bool) bool {
	return func(rule func(any) bool) bool { return !present || value == nil || rule(value) }
}

func isAny(any) bool { return true }

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func isObject(v any) bool {
	o, ok := v.(*strictjson.Object)
	return ok && o != nil
}

func isMapOf(v any, rule func(any) bool) bool {
	o, ok := v.(*strictjson.Object)
	if !ok || o == nil {
		return false
	}
	for _, value := range o.Values {
		if !rule(value) {
			return false
		}
	}
	return true
}

func isListOf(v any, rule func(any) bool) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if !rule(item) {
			return false
		}
	}
	return true
}

func oneOf(choices ...string) func(any) bool {
	return func(v any) bool {
		s, ok := v.(string)
		for _, c := range choices {
			if ok && s == c {
				return true
			}
		}
		return false
	}
}

// isLaxInt is pydantic's lax int: an integer, a boolean, or a float with no fraction.
func isLaxInt(v any) bool {
	switch v := v.(type) {
	case *big.Int, bool:
		return true
	case float64:
		return !math.IsInf(v, 0) && !math.IsNaN(v) && v == math.Trunc(v)
	}
	return false
}

// isLaxBool is pydantic's lax bool: a boolean, 0 or 1 as an integer or float, or one of its words.
func isLaxBool(v any) bool {
	switch v := v.(type) {
	case bool:
		return true
	case *big.Int:
		return v.IsInt64() && (v.Int64() == 0 || v.Int64() == 1)
	case float64:
		return v == 0 || v == 1
	case string:
		switch strings.ToLower(v) {
		case "0", "off", "f", "false", "n", "no", "1", "on", "t", "true", "y", "yes":
			return true
		}
	}
	return false
}
