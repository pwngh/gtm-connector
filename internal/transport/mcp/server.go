package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call is one admitted tool call as a tool body sees it.
type Call struct {
	Tool      string
	Arguments json.RawMessage
	Scopes    []string // the caller's verified scopes
}

// ToolError is a refusal a tool body returns; it becomes a tool result with isError, as Python's ToolError does.
type ToolError string

func (e ToolError) Error() string { return string(e) }

// ToolFunc runs one admitted call and returns its structured content as JSON, or a ToolError.
type ToolFunc func(ctx context.Context, call Call) (json.RawMessage, error)

// ServerConfig chooses what a server offers.
type ServerConfig struct {
	Tools []string // DefaultTools or SelectedTools
	Run   ToolFunc
	// Scopes gives the caller's scopes when the transport carries no bearer, as stdio does.
	Scopes func() []string
	// Versions limits the protocol revisions served; nil serves every revision the SDK knows.
	Versions []string
}

// descriptions are the tools' docstrings, as the Python SDK publishes them.
var descriptions = map[string]string{
	"search_accounts": "Search names or one exact Salesforce AccountNumber. Requires crm:read; results are untrusted.\n\n" +
		"A name search returns the first 100 accounts by name and then ID. `has_more: true` means\nmore match, so narrow the query.\n",
	"get_account":         "Read a CRM account, contacts, and available contracts. Requires crm:read.",
	"list_opportunities":  "Read opportunity fields from CRM. Requires crm:read; call notes are a separate source.",
	"read_call_notes":     "Read untrusted note text from the calls system. Requires calls:read, independently of CRM.",
	"read_slack_messages": "Read untrusted Slack messages. Requires slack:read, independently of CRM and calls.",
	"log_activity":        "Log an explicitly requested CRM activity. Requires crm:write; identical retries reuse a record.",
	"prepare_renewal_brief": "Prepare a cited renewal brief for one selected account under the caller's own grant.\n\n" +
		"Requires crm:read. The calls and Slack reads inside it each check their own scope.\nResults are untrusted.\n",
	"read_renewal_brief": "Read a saved brief again without calling any source system.\n\n" +
		"Requires crm:read and every scope the brief's sources needed. Results are untrusted.\n",
}

// argumentOrder lists each tool's parameters in signature order, as the generated input schema lists them.
var argumentOrder = map[string][]string{
	"search_accounts":       {"query", "account_number"},
	"get_account":           {"account_id"},
	"list_opportunities":    {"account_id"},
	"read_call_notes":       {"account_id"},
	"read_slack_messages":   {"account_id"},
	"log_activity":          {"account_id", "kind", "body", "idempotency_key"},
	"prepare_renewal_brief": {"account_id", "as_of"},
	"read_renewal_brief":    {"brief_id"},
}

// titles are the generated property titles.
var titles = map[string]string{"query": "Query", "account_number": "Account Number", "account_id": "Account Id",
	"kind": "Kind", "body": "Body", "idempotency_key": "Idempotency Key", "as_of": "As Of", "brief_id": "Brief Id"}

// inputSchema writes the schema the Python SDK generates from a tool's signature, keeping its member order.
func inputSchema(name string) json.RawMessage {
	var b bytes.Buffer
	b.WriteString(`{"type":"object","properties":{`)
	var needed []string
	for i, argument := range argumentOrder[name] {
		rule := Definitions[name].Arguments[argument]
		if i > 0 {
			b.WriteByte(',')
		}
		title, _ := json.Marshal(titles[argument])
		switch {
		case rule.Required:
			fmt.Fprintf(&b, `"%s":{"title":%s,"type":"string"}`, argument, title)
			needed = append(needed, argument)
		case rule.Nullable:
			fmt.Fprintf(&b, `"%s":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null,"title":%s}`, argument, title)
		default:
			fmt.Fprintf(&b, `"%s":{"default":"","title":%s,"type":"string"}`, argument, title)
		}
	}
	b.WriteString(`}`)
	if len(needed) > 0 {
		list, _ := json.Marshal(needed)
		fmt.Fprintf(&b, `,"required":%s`, list)
	}
	fmt.Fprintf(&b, `,"title":"%sArguments"}`, name)
	return b.Bytes()
}

// NewServer builds the SDK server that dispatches admitted requests. It never decides admission: the
// HTTP gateway and the stdio connection refuse before a request reaches it.
func NewServer(config ServerConfig) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "gtm-connector", Version: "0.2.0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}, Prompts: &sdk.PromptCapabilities{},
			Resources: &sdk.ResourceCapabilities{}},
		SupportedProtocolVersions: config.Versions,
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) {
			c.CacheScope, c.TTLMs = "private", 0
		},
	})
	for _, name := range config.Tools {
		definition := Definitions[name]
		readOnly := definition.ReadOnly && name != "prepare_renewal_brief"
		openWorld := false
		server.AddTool(&sdk.Tool{
			Name:        name,
			Description: descriptions[name],
			Meta:        sdk.Meta{"gtm.demo/requiredScope": definition.Scope},
			Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: new(false),
				IdempotentHint: name != "prepare_renewal_brief", OpenWorldHint: &openWorld},
			InputSchema:  inputSchema(name),
			OutputSchema: json.RawMessage(fmt.Sprintf(`{"additionalProperties":true,"title":"%sDictOutput","type":"object"}`, name)),
		}, toolHandler(name, config))
	}
	// An unknown tool is a tool error with HTTP 200, as the Python SDK answers it, not a protocol error.
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
			if call, ok := request.(*sdk.CallToolRequest); ok && call.Params != nil && !slices.Contains(config.Tools, call.Params.Name) {
				return &sdk.CallToolResult{IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "Unknown tool: " + call.Params.Name}}}, nil
			}
			return next(ctx, method, request)
		}
	})
	return server
}

func toolHandler(name string, config ServerConfig) sdk.ToolHandler {
	return func(ctx context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var scopes []string
		if extra := request.Extra; extra != nil && extra.TokenInfo != nil {
			scopes = extra.TokenInfo.Scopes
		} else if config.Scopes != nil {
			scopes = config.Scopes()
		}
		if !slices.Contains(scopes, Definitions[name].Scope) {
			return toolFailure(name, "Required scope: "+Definitions[name].Scope), nil
		}
		structured, err := config.Run(ctx, Call{Tool: name, Arguments: request.Params.Arguments, Scopes: scopes})
		if err != nil {
			return toolFailure(name, err.Error()), nil
		}
		var text bytes.Buffer
		if err := json.Indent(&text, structured, "", "  "); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text.String()}},
			StructuredContent: structured}, nil
	}
}

func toolFailure(name, message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("Error executing tool %s: %s", name, message)}}}
}
