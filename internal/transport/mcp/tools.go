package mcp

import (
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// StringArgument is one string argument's rule, as tool_definitions.StringArgument states it.
type StringArgument struct {
	MaxLength int
	Required  bool
	Nullable  bool
}

// ToolDefinition is a tool's scope and argument rules, as tool_definitions.ToolDefinition states them.
type ToolDefinition struct {
	Scope     string
	Arguments map[string]StringArgument
}

func required(n int) StringArgument { return StringArgument{MaxLength: n, Required: true} }

// Definitions holds every tool the connector can serve: the read tools of tool_definitions.TOOL_DEFINITIONS.
// Each requires a read scope.
var Definitions = map[string]ToolDefinition{
	"search_accounts": {Scope: "crm:read", Arguments: map[string]StringArgument{
		"query":          {MaxLength: 200},
		"account_number": {MaxLength: 40, Nullable: true},
	}},
	"get_account":         {Scope: "crm:read", Arguments: map[string]StringArgument{"account_id": required(64)}},
	"list_opportunities":  {Scope: "crm:read", Arguments: map[string]StringArgument{"account_id": {MaxLength: 64, Nullable: true}}},
	"read_call_notes":     {Scope: "calls:read", Arguments: map[string]StringArgument{"account_id": required(64)}},
	"read_slack_messages": {Scope: "slack:read", Arguments: map[string]StringArgument{"account_id": required(64)}},
	"prepare_renewal_brief": {Scope: "crm:read", Arguments: map[string]StringArgument{
		"account_id": required(64), "as_of": {MaxLength: 10, Nullable: true},
	}},
	"read_renewal_brief": {Scope: "crm:read", Arguments: map[string]StringArgument{"brief_id": required(64)}},
}

// DefaultTools are the five tools the default server offers; SelectedTools add the brief tools.
var (
	DefaultTools  = []string{"search_accounts", "get_account", "list_opportunities", "read_call_notes", "read_slack_messages"}
	SelectedTools = []string{"search_accounts", "get_account", "list_opportunities", "read_call_notes", "read_slack_messages", "prepare_renewal_brief", "read_renewal_brief"}
)

// writeTool is the Python gateway's CRM write. The connector is read-only: it never lists, defines or runs
// this tool, and CheckRequest refuses a call to it as crm_write_disabled.
const writeTool = "log_activity"

// ValidAccountSearch reports whether an exact account number may be used, as tool_definitions.valid_account_search does.
func ValidAccountSearch(query, accountNumber any) bool {
	if accountNumber == nil {
		return true
	}
	number, ok := accountNumber.(string)
	if query != "" || !ok || strip(number) == "" || utf8.RuneCountInString(number) > 40 {
		return false
	}
	for _, c := range number {
		if c < 32 || (c >= 127 && c <= 159) {
			return false
		}
	}
	return true
}

// ValidArguments checks string lengths in code points without coercing or trimming, as
// tool_definitions.valid_arguments does. Arguments are a value from strictjson.
func ValidArguments(name string, definition ToolDefinition, arguments any) bool {
	object, ok := arguments.(*strictjson.Object)
	if !ok {
		return false
	}
	for _, key := range object.Keys {
		if _, known := definition.Arguments[key]; !known {
			return false
		}
	}
	for key, rule := range definition.Arguments {
		value, present := member(object, key)
		if !present {
			if rule.Required {
				return false
			}
			continue
		}
		if value == nil && rule.Nullable {
			continue
		}
		text, ok := value.(string)
		if !ok || utf8.RuneCountInString(text) > rule.MaxLength || (rule.Required && strip(text) == "") {
			return false
		}
	}
	if name != "search_accounts" {
		return true
	}
	query, present := member(object, "query")
	if !present {
		query = ""
	}
	number, _ := member(object, "account_number")
	return ValidAccountSearch(query, number)
}

// strip removes leading and trailing characters Python's str.isspace accepts.
func strip(s string) string {
	runes := []rune(s)
	start, end := 0, len(runes)
	for start < end && unicodetab.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicodetab.IsSpace(runes[end-1]) {
		end--
	}
	return string(runes[start:end])
}

// member returns an object's value for key. strictjson refuses duplicate keys, so there is at most one.
func member(object *strictjson.Object, key string) (any, bool) {
	if object == nil {
		return nil, false
	}
	for i, k := range object.Keys {
		if k == key {
			return object.Values[i], true
		}
	}
	return nil, false
}
