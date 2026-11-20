package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// StdioConfig is one stdio server's machine principal and limits.
type StdioConfig struct {
	Token          string // the machine principal's bearer, verified again on every request
	Resource       string
	RateLimit      int
	WindowSeconds  float64
	WritesDisabled bool
	Selected       bool
	Verify         Verifier
	Receipts       Recorder
	Run            ToolFunc
	Clock          func() float64
	Wall           func() float64
}

// ServeStdio serves MCP on the given transport until it closes. Each tools/list, tools/call, prompts/get
// and resources/read passes the same admission as an HTTP request the stdio bridge forwards today: the
// bridge's own HTTPS headers and 2026-07-28 metadata always pass, so only the principal, rate, scope,
// write, argument and method checks can refuse. Other methods, initialize and server/discover among them,
// are answered by the SDK, as the bridge answers them itself.
func ServeStdio(ctx context.Context, config StdioConfig, transport sdk.Transport) error {
	if config.Clock == nil {
		start := time.Now()
		config.Clock = func() float64 { return time.Since(start).Seconds() }
	}
	if config.Wall == nil {
		config.Wall = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	tools := DefaultTools
	definitions := map[string]ToolDefinition{}
	for name, definition := range Definitions {
		if config.Selected || (name != "prepare_renewal_brief" && name != "read_renewal_brief") {
			definitions[name] = definition
		}
	}
	if config.Selected {
		tools = SelectedTools
	}
	gate := &stdioGate{config: config, definitions: definitions, windows: Windows{}, pending: map[jsonrpc.ID]pendingCall{},
		policy: Policy{Resource: config.Resource, RateLimit: config.RateLimit, WindowSeconds: config.WindowSeconds,
			WritesDisabled: config.WritesDisabled || config.Selected}}
	server := NewServer(ServerConfig{Tools: tools, Run: config.Run, Scopes: func() []string {
		if token := config.Verify(config.Token); token != nil {
			return token.Scopes
		}
		return nil
	}})
	return server.Run(ctx, &gatedTransport{inner: transport, gate: gate})
}

type pendingCall struct {
	auditID string
	started float64
}

type stdioGate struct {
	config      StdioConfig
	policy      Policy
	definitions map[string]ToolDefinition

	mu      sync.Mutex
	windows Windows
	pending map[jsonrpc.ID]pendingCall
}

type gatedTransport struct {
	inner sdk.Transport
	gate  *stdioGate
}

func (t *gatedTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	connection, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &gatedConnection{Connection: connection, gate: t.gate}, nil
}

type gatedConnection struct {
	sdk.Connection
	gate *stdioGate
}

var gatedMethods = map[string]bool{"tools/list": true, "tools/call": true, "prompts/get": true, "resources/read": true}

// Read passes on each admitted message. A refused request is answered here and never reaches the SDK.
func (c *gatedConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		message, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		request, ok := message.(*jsonrpc.Request)
		if !ok || !gatedMethods[request.Method] {
			return message, nil
		}
		reply, err := c.gate.admit(request)
		if err != nil {
			return nil, err
		}
		if reply == nil {
			return message, nil
		}
		if request.IsCall() {
			if err := c.Connection.Write(ctx, reply); err != nil {
				return nil, err
			}
		}
	}
}

// Write holds a call's reply until its receipt is finished, and withholds it if that fails.
func (c *gatedConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if response, ok := message.(*jsonrpc.Response); ok {
		c.gate.mu.Lock()
		call, held := c.gate.pending[response.ID]
		delete(c.gate.pending, response.ID)
		c.gate.mu.Unlock()
		if held {
			message = c.gate.finish(response, call)
		}
	}
	return c.Connection.Write(ctx, message)
}

func errorResponse(id jsonrpc.ID, code int, message string, data any) *jsonrpc.Response {
	wire := &jsonrpc.Error{Code: int64(code), Message: message}
	if data != nil {
		wire.Data, _ = json.Marshal(data)
	}
	return &jsonrpc.Response{ID: id, Error: wire}
}

// admit records a call's receipt and checks the request. It returns the refusal to send, or nil to admit.
func (g *stdioGate) admit(request *jsonrpc.Request) (*jsonrpc.Response, error) {
	isCall := request.Method == "tools/call"
	params := any(&strictjson.Object{})
	parsed := true
	if len(request.Params) > 0 {
		value, err := strictjson.Loads(request.Params)
		if err != nil {
			parsed = false
		} else if _, ok := value.(*strictjson.Object); ok {
			params = value
		}
	}
	tool, ok := memberOr(params, "name", "<unparsed>").(string)
	if !ok {
		tool = "<invalid>"
	}
	arguments := memberOr(params, "arguments", &strictjson.Object{})
	started := g.config.Clock()
	auditID := ""
	if isCall {
		recorded := plain(arguments)
		if !parsed {
			sum := sha256.Sum256(request.Params)
			recorded = map[string]any{"unparsed_sha256": hex.EncodeToString(sum[:])}
		}
		var requestID any
		if request.ID.IsValid() {
			requestID = request.ID.Raw()
		}
		token := g.config.Token
		id, err := g.config.Receipts.Start(Start{Token: &token, Tool: tool, Arguments: recorded, RequestID: requestID})
		if err != nil {
			return errorResponse(request.ID, -32603, "audit_unavailable; call was not executed", nil), nil
		}
		auditID = id
	}
	// The facts the bridge's forwarded request carries: its own host, content type, bearer and metadata.
	resource, _ := url.Parse(g.config.Resource)
	envelope := &strictjson.Object{Keys: []string{"jsonrpc", "method"}, Values: []any{"2.0", request.Method}}
	if request.ID.IsValid() {
		envelope.Keys, envelope.Values = append(envelope.Keys, "id"), append(envelope.Values, idValue(request.ID))
	}
	if !parsed {
		envelope = nil
	}
	encoded, _ := jsonrpc.EncodeMessage(request)
	facts := RequestFacts{BodySize: len(encoded), Parsed: envelope, Scheme: "https",
		Headers: map[string]string{"host": resource.Host, "content-type": "application/json",
			"mcp-protocol-version": ProtocolVersion},
		HeaderCounts: map[string]int{"host": 1, "content-type": 1, "authorization": 1},
		RawToken:     g.config.Token, IsCall: isCall, Tool: tool, Arguments: arguments,
		Meta: &strictjson.Object{Keys: []string{"io.modelcontextprotocol/protocolVersion"}, Values: []any{ProtocolVersion}}}
	g.mu.Lock()
	decision := CheckRequest(facts, g.policy, g.windows, g.definitions, g.config.Verify(g.config.Token),
		g.config.Wall(), g.config.Clock())
	if decision.Windows != nil {
		g.windows = decision.Windows
	}
	if decision.Error == nil && auditID != "" {
		g.pending[request.ID] = pendingCall{auditID: auditID, started: started}
	}
	g.mu.Unlock()
	if decision.Error == nil {
		return nil, nil
	}
	e := decision.Error
	if auditID != "" {
		reason := "request_failed"
		if err := g.config.Receipts.Finish(auditID, Finish{Status: "error", HTTPStatus: e.Status(), Rows: []string{},
			ElapsedMs: (g.config.Clock() - started) * 1000, Error: &reason}); err != nil {
			return errorResponse(request.ID, -32603, "execution_or_audit_unavailable; no result released", nil), nil
		}
	}
	return errorResponse(request.ID, e.Code(), e.Message, map[string]any{"reason": e.Reason}), nil
}

// idValue gives a request id as strictjson holds it, so CheckRequest sees an integer or a string.
func idValue(id jsonrpc.ID) any {
	value, _ := strictjson.Loads(mustJSON(id.Raw()))
	return value
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

// statusOf is the HTTP status a stdio reply stands for in its receipt, by the SEP-2575 table.
func statusOf(response *jsonrpc.Response) int {
	wire, ok := response.Error.(*jsonrpc.Error)
	if response.Error == nil || !ok {
		if response.Error != nil {
			return 503
		}
		return 200
	}
	switch wire.Code {
	case -32601:
		return 404
	case -32700, -32600, -32602, -32020, -32021, -32022:
		return 400
	}
	return 200
}

// finish records how an admitted call ended and returns the reply to release.
func (g *stdioGate) finish(response *jsonrpc.Response, call pendingCall) jsonrpc.Message {
	status := statusOf(response)
	released := false
	rows := []string{}
	if response.Error == nil {
		var result map[string]any
		decoder := json.NewDecoder(bytes.NewReader(response.Result))
		decoder.UseNumber()
		if decoder.Decode(&result) == nil {
			isError, _ := result["isError"].(bool)
			released = !isError
			if released {
				rows = recordRefs(result)
			}
			annotate(result, call.auditID)
			response = &jsonrpc.Response{ID: response.ID, Result: mustJSON(result)}
		}
	}
	outcome, failure := "ok", (*string)(nil)
	if !released {
		reason := "request_failed"
		outcome, failure = "error", &reason
	}
	if err := g.config.Receipts.Finish(call.auditID, Finish{Status: outcome, HTTPStatus: status, Rows: rows,
		ElapsedMs: (g.config.Clock() - call.started) * 1000, Error: failure}); err != nil {
		return errorResponse(response.ID, -32603, "execution_or_audit_unavailable; no result released", nil)
	}
	return response
}
