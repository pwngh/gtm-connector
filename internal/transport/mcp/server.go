package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call is one admitted tool call as a tool body sees it.
type Call struct {
	Tool       string
	Arguments  json.RawMessage
	Invocation *Invocation // the receipt and principal it was admitted under
}

// ToolError is a refusal a tool body returns; it becomes a tool result with isError, as Python's ToolError
// does. Any other error becomes the fixed "execution_failed", so a backend's own words never reach a client.
type ToolError string

func (e ToolError) Error() string { return string(e) }

// ToolFunc runs one admitted call and returns its structured content as a JSON object, or an error.
type ToolFunc func(ctx context.Context, call Call) (json.RawMessage, error)

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

// newServer builds the SDK server that dispatches admitted requests. It never decides admission: the HTTP
// gateway and the stdio connection refuse before a request reaches it, and rebuild what does reach it.
func newServer(tools []string, run ToolFunc, lookup func(string) *Invocation, versions []string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "gtm-connector", Version: "0.2.0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}, Prompts: &sdk.PromptCapabilities{},
			Resources: &sdk.ResourceCapabilities{}},
		SupportedProtocolVersions: versions,
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) {
			c.CacheScope, c.TTLMs = "private", 0
		},
	})
	for _, name := range tools {
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
		}, toolHandler(name, run, lookup))
	}
	// An unknown tool is a tool error with HTTP 200, as the Python SDK answers it, not a protocol error.
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
			if call, ok := request.(*sdk.CallToolRequest); ok && call.Params != nil && !slices.Contains(tools, call.Params.Name) {
				return &sdk.CallToolResult{IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "Unknown tool: " + call.Params.Name}}}, nil
			}
			return next(ctx, method, request)
		}
	})
	return server
}

// toolHandler runs one tool for the invocation its dispatch id names. The scope check repeats the gateway's,
// from the same admitted scopes, so a tool never runs outside its grant even if a gateway path missed it.
func toolHandler(name string, run ToolFunc, lookup func(string) *Invocation) sdk.ToolHandler {
	return func(ctx context.Context, request *sdk.CallToolRequest) (result *sdk.CallToolResult, err error) {
		id, _ := request.Params.Meta[dispatchKey].(string)
		inv := lookup(id)
		if inv == nil || inv.Tool != name {
			return toolFailure(name, "execution_failed"), nil
		}
		defer func() {
			// A panicking tool releases nothing: the gateway sees the mark and withholds the reply.
			if recover() != nil {
				inv.failed.Store(true)
				result, err = toolFailure(name, "execution_failed"), nil
			}
		}()
		if scope := Definitions[name].Scope; !slices.Contains(inv.Scopes, scope) {
			return toolFailure(name, "Required scope: "+scope), nil
		}
		structured, err := run(ctx, Call{Tool: name, Arguments: request.Params.Arguments, Invocation: inv})
		var refusal ToolError
		switch {
		case errors.As(err, &refusal):
			return toolFailure(name, string(refusal)), nil
		case err != nil:
			return toolFailure(name, "execution_failed"), nil
		}
		var text bytes.Buffer
		if !json.Valid(structured) || bytes.TrimSpace(structured)[0] != '{' || json.Indent(&text, structured, "", "  ") != nil {
			inv.failed.Store(true)
			return toolFailure(name, "execution_failed"), nil
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text.String()}},
			StructuredContent: structured}, nil
	}
}

func toolFailure(name, message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("Error executing tool %s: %s", name, message)}}}
}
