// Package mcp serves the connector's MCP tools on streamable HTTP and stdio. A request is admitted here,
// in gtm_connector.request_checks' order, under a receipt committed before anything runs; only then does
// the Go MCP SDK dispatch it. A reply leaves only after its receipt is finished.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Verifier returns the token a raw bearer names, or nil.
type Verifier func(raw string) *Token

// GatewayConfig is what one HTTP gateway serves and how it checks callers.
type GatewayConfig struct {
	Resource       string // the exact https://host:port/mcp URL tokens are issued for
	Issuer         string
	RateLimit      int
	WindowSeconds  float64
	WritesDisabled bool
	Selected       bool // a selection offers the brief tools and refuses writes
	Verify         Verifier
	Receipts       Recorder
	Run            ToolFunc
	Clock          func() float64 // the rate clock, in seconds
	Wall           func() float64 // the wall clock, in seconds since the epoch
}

// Gateway is the /mcp endpoint and its protected-resource metadata.
type Gateway struct {
	config      GatewayConfig
	policy      Policy
	definitions map[string]ToolDefinition
	sdk         http.Handler

	mu      sync.Mutex // guards windows, which composite reads also charge
	windows Windows
}

// NewGateway builds a gateway over a stateless, JSON-response SDK handler restricted to ProtocolVersion.
func NewGateway(config GatewayConfig) *Gateway {
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
	origin := config.Resource[:strings.LastIndex(config.Resource, "/")]
	g := &Gateway{
		config: config, definitions: definitions, windows: Windows{},
		policy: Policy{Resource: config.Resource, MetadataURL: origin + "/.well-known/oauth-protected-resource/mcp",
			RateLimit: config.RateLimit, WindowSeconds: config.WindowSeconds,
			WritesDisabled: config.WritesDisabled || config.Selected},
	}
	server := NewServer(ServerConfig{Tools: tools, Run: config.Run, Versions: []string{ProtocolVersion}})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true,
			MaxRequestBodyBytes: MaxBody})
	// The gateway has already refused an invalid or expired bearer; this only hands the scopes to the tools.
	g.sdk = auth.RequireBearerToken(func(_ context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		token := config.Verify(raw)
		if token == nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Scopes: token.Scopes}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(handler)
	return g
}

// Windows returns a copy of the rate windows, for tests.
func (g *Gateway) Windows() Windows {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := Windows{}
	for k, v := range g.windows {
		out[k] = v
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, headers map[string]string, value any) {
	body, _ := json.Marshal(value)
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// latin1 reads a header value byte by byte, as Starlette decodes it.
func latin1(value string) string {
	runes := make([]rune, len(value))
	for i := 0; i < len(value); i++ {
		runes[i] = rune(value[i])
	}
	return string(runes)
}

// response is a reply held until its receipt finishes.
type response struct {
	status  int
	headers http.Header
	body    []byte
}

// holder captures what the SDK writes so nothing reaches the caller before the receipt finishes.
type holder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *holder) Header() http.Header { return h.header }
func (h *holder) WriteHeader(status int) {
	if h.status == 0 {
		h.status = status
	}
}
func (h *holder) Write(p []byte) (int, error) {
	h.WriteHeader(http.StatusOK)
	return h.body.Write(p)
}
func (h *holder) Flush() {}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource/mcp":
		scopes := []string{"crm:read"}
		writeJSON(w, http.StatusOK, nil, map[string]any{"resource": g.config.Resource,
			"authorization_servers": []string{g.config.Issuer}, "scopes_supported": scopes,
			"bearer_methods_supported": []string{"header"}})
		return
	case r.URL.Path == "/mcp" && r.Method == http.MethodGet:
		// A GET would open a listening stream that never ends and passes none of the checks below.
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"Allow": "POST"},
			rpcError(nil, -32600, "Method Not Allowed", nil))
		return
	case r.URL.Path == "/mcp" && r.Method == http.MethodDelete:
		writeJSON(w, http.StatusMethodNotAllowed, nil,
			rpcError(nil, -32600, "Method Not Allowed: Session termination not supported", nil))
		return
	case r.URL.Path != "/mcp" || r.Method != http.MethodPost:
		http.NotFound(w, r)
		return
	}
	g.post(w, r)
}

func rpcError(id any, code int, message string, data any) map[string]any {
	errorBody := map[string]any{"code": code, "message": message}
	if data != nil {
		errorBody["data"] = data
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": errorBody}
}

func (g *Gateway) post(w http.ResponseWriter, r *http.Request) {
	headers := map[string]string{"host": latin1(r.Host)}
	counts := map[string]int{"host": 1}
	for name, values := range r.Header {
		key := strings.ToLower(name)
		counts[key] += len(values)
		headers[key] = latin1(values[len(values)-1])
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return // the caller went away before sending the whole body
	}
	marked := headers["mcp-method"] == "tools/call"
	request, err := strictjson.Loads(body)
	if err != nil {
		request = nil
		// A refused body that still names a tool call keeps its receipt.
		var refused map[string]json.RawMessage
		if json.Unmarshal(body, &refused) == nil {
			var method string
			marked = marked || (json.Unmarshal(refused["method"], &method) == nil && method == "tools/call")
		}
	}
	object, _ := request.(*strictjson.Object)
	params, _ := memberOr(object, "params", &strictjson.Object{}).(*strictjson.Object)
	if params == nil {
		params = &strictjson.Object{}
	}
	method, _ := member(object, "method")
	isCall := method == "tools/call" || marked
	rawAuth := headers["authorization"]
	parts := strings.Split(rawAuth, " ")
	rawToken := ""
	if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" && parts[1] != "" {
		rawToken = parts[1]
	}
	header, hasName := headers["mcp-name"]
	if !hasName {
		header = "<unparsed>"
	}
	tool, ok := memberOr(params, "name", header).(string)
	if !ok {
		tool = "<invalid>"
	}
	arguments := memberOr(params, "arguments", &strictjson.Object{})
	meta := memberOr(params, "_meta", &strictjson.Object{})
	var briefID *string
	if metaObject, ok := meta.(*strictjson.Object); ok {
		if value, ok := memberOr(metaObject, "gtm.demo/briefId", nil).(string); ok && utf8.RuneCountInString(value) <= 128 {
			briefID = &value
		}
	}
	requestID, _ := member(object, "id")
	started := g.config.Clock()
	auditID := ""
	var callerToken *string
	if rawToken != "" {
		callerToken = &rawToken
	} else if rawAuth != "" {
		callerToken = &rawAuth
	}
	if isCall {
		// JSON-RPC ids are strings or integers. Any other value is recorded as a fixed marker, so a caller
		// cannot imitate the server's own correlation for brief reads.
		var receiptID any = "<invalid>"
		if requestID == nil || isRequestID(requestID) {
			receiptID = plain(requestID)
		}
		recorded := plain(arguments)
		if request == nil {
			sum := sha256.Sum256(body)
			recorded = map[string]any{"unparsed_sha256": hex.EncodeToString(sum[:])}
		}
		auditID, err = g.config.Receipts.Start(Start{Token: callerToken, Tool: tool, Arguments: recorded,
			RequestID: receiptID, BriefID: briefID})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, nil, map[string]any{"error": "audit_unavailable; call was not executed"})
			return
		}
	}

	facts := RequestFacts{BodySize: len(body), Parsed: request, Scheme: scheme(r), Headers: headers,
		HeaderCounts: counts, RawToken: rawToken, IsCall: isCall, Tool: tool, Arguments: arguments, Meta: meta}
	var token *Token
	if rawToken != "" {
		token = g.config.Verify(rawToken)
	}
	g.mu.Lock()
	decision := CheckRequest(facts, g.policy, g.windows, g.definitions, token, g.config.Wall(), g.config.Clock())
	if decision.Windows != nil {
		g.windows = decision.Windows
	}
	g.mu.Unlock()

	var reply response
	if decision.Error != nil {
		// An id that is not a string or integer is not echoed back.
		var echoed any
		if isRequestID(requestID) {
			echoed = plain(requestID)
		}
		e := decision.Error
		reply = jsonReply(e.Status(), e.Headers, rpcError(echoed, e.Code(), e.Message, map[string]any{"reason": e.Reason}))
	} else if refused, ok := ladder(object, headers, counts); ok {
		reply = refused
	} else {
		reply = g.dispatch(r, body, headers)
	}

	released, result, document := releasedResult(reply)
	if auditID != "" {
		rows := []string{}
		if released {
			rows = recordRefs(result)
		}
		if result != nil {
			// The reply is complete before the receipt is finished, so a receipt never says more than it.
			annotate(result, auditID)
			reply.body, _ = json.Marshal(document)
			reply.headers.Set("Content-Length", strconv.Itoa(len(reply.body)))
		}
		status, failure := "ok", (*string)(nil)
		if !released {
			reason := "request_failed"
			status, failure = "error", &reason
		}
		if err := g.config.Receipts.Finish(auditID, Finish{Status: status, HTTPStatus: reply.status, Rows: rows,
			ElapsedMs: (g.config.Clock() - started) * 1000, Error: failure}); err != nil {
			// The already-committed pending receipt remains visible; nothing is released.
			writeJSON(w, http.StatusServiceUnavailable, nil,
				map[string]any{"error": "execution_or_audit_unavailable; no result released"})
			return
		}
	}
	for k, v := range reply.headers {
		w.Header()[k] = v
	}
	w.WriteHeader(reply.status)
	w.Write(reply.body)
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func jsonReply(status int, headers map[string]string, value any) response {
	body, _ := json.Marshal(value)
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	return response{status: status, headers: h, body: body}
}

// ladder applies the checks the Python SDK makes on an admitted 2026-07-28 request before dispatch, in its
// order and with its messages. A notification is acknowledged and dropped, as the Python SDK does.
func ladder(object *strictjson.Object, headers map[string]string, counts map[string]int) (response, bool) {
	invalidBody := jsonReply(400, nil, rpcError(nil, -32600, "Body must be a single JSON-RPC request or notification object", nil))
	if !accepts(headers["accept"], "application/json", "application/*") {
		return response{status: 406, headers: http.Header{"Content-Length": {"0"}}}, true
	}
	version, _ := member(object, "jsonrpc")
	method, _ := member(object, "method")
	_, isText := method.(string)
	params, hasParams := member(object, "params")
	_, paramsObject := params.(*strictjson.Object)
	envelope := version == "2.0" && isText && (!hasParams || params == nil || paramsObject)
	id, hasID := member(object, "id")
	if !hasID {
		if !envelope {
			return invalidBody, true
		}
		return response{status: http.StatusAccepted, headers: http.Header{"Content-Length": {"0"}}}, true
	}
	if !envelope || !isRequestID(id) {
		return invalidBody, true
	}
	echoed := plain(id)
	mismatch := func(message string) (response, bool) {
		return jsonReply(400, nil, rpcError(echoed, -32020, message, nil)), true
	}
	// First-copy and last-copy readers of a routing header would disagree.
	for _, name := range []string{"mcp-protocol-version", "mcp-method", "mcp-name"} {
		if counts[name] > 1 {
			return mismatch(name + " header appears more than once")
		}
	}
	meta, _ := memberOr(params, "_meta", nil).(*strictjson.Object)
	if meta == nil {
		return jsonReply(400, nil, rpcError(echoed, -32602, "params._meta must be an object carrying the required "+
			"'io.modelcontextprotocol/protocolVersion' and 'io.modelcontextprotocol/clientCapabilities' envelope keys", nil)), true
	}
	var missing []string
	for _, key := range []string{"io.modelcontextprotocol/protocolVersion", "io.modelcontextprotocol/clientCapabilities"} {
		if _, ok := member(meta, key); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return jsonReply(400, nil, rpcError(echoed, -32602,
			"params._meta is missing the required envelope key(s): "+strings.Join(missing, ", "), nil)), true
	}
	if headers["mcp-method"] != method {
		return mismatch("mcp-method header does not match the request body's method")
	}
	key := map[any]string{"tools/call": "name", "prompts/get": "name", "resources/read": "uri"}[method]
	if key != "" {
		if value := memberOr(params, key, nil); value != nil {
			name, present := headers["mcp-name"]
			decoded, ok := decodeHeaderValue(name)
			if !present || !ok || decoded != value {
				return mismatch("mcp-name header does not match the request body's '" + key + "' parameter")
			}
		}
	}
	return response{}, false
}

// accepts reports whether an Accept header admits JSON, with Starlette's wildcard reading.
func accepts(header string, kinds ...string) bool {
	if header == "" {
		header = "*/*" // an HTTP client that names nothing accepts anything
	}
	for _, part := range strings.Split(header, ",") {
		kind := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(part), ";", 2)[0]))
		if kind == "*/*" {
			return true
		}
		for _, k := range kinds {
			if kind == k {
				return true
			}
		}
	}
	return false
}

// decodeHeaderValue undoes the =?base64?...?= sentinel, refusing non-canonical base64 and bad UTF-8.
func decodeHeaderValue(value string) (string, bool) {
	payload, ok := strings.CutPrefix(value, "=?base64?")
	if !ok {
		return value, true
	}
	if payload, ok = strings.CutSuffix(payload, "?="); !ok {
		return value, true
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != payload || !utf8.Valid(decoded) {
		return "", false
	}
	return string(decoded), true
}

// dispatch hands an admitted request to the SDK and holds its reply.
func (g *Gateway) dispatch(r *http.Request, body []byte, headers map[string]string) response {
	inner := r.Clone(r.Context())
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	// The checks above are the contract; the SDK's own, stricter Accept rule and its undecoded name
	// comparison are satisfied here rather than allowed to answer differently.
	inner.Header.Set("Accept", "application/json, text/event-stream")
	if name, ok := headers["mcp-name"]; ok {
		if decoded, ok := decodeHeaderValue(name); ok {
			inner.Header.Set("Mcp-Name", decoded)
		}
	}
	held := &holder{header: http.Header{}}
	g.sdk.ServeHTTP(held, inner)
	if held.status == 0 {
		held.status = http.StatusOK
	}
	body = held.body.Bytes()
	// A method the SDK does not serve is answered in the Python SDK's words: "Method not found", the method as data.
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &reply) == nil && reply.Error != nil && reply.Error.Code == -32601 {
		method := r.Header.Get("Mcp-Method")
		body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": reply.ID,
			"error": map[string]any{"code": -32601, "message": "Method not found", "data": method}})
	}
	held.header.Set("Content-Length", strconv.Itoa(len(body)))
	return response{status: held.status, headers: held.header, body: body}
}

// annotate names a tool result's receipt in its _meta and writes the two members the SDK leaves out when
// they hold their defaults and the Python SDK always sends: isError false and resultType "complete".
func annotate(result map[string]any, auditID string) {
	meta, _ := result["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		result["_meta"] = meta
	}
	meta["gtm.demo/auditId"] = auditID
	if _, ok := result["isError"]; !ok {
		result["isError"] = false
	}
	if _, ok := result["resultType"]; !ok {
		result["resultType"] = "complete"
	}
}

// releasedResult reports whether a reply is a tool result that may be released, and returns the decoded
// result and document for annotation. Only a result without isError, below HTTP 400, counts.
func releasedResult(reply response) (bool, map[string]any, map[string]any) {
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(reply.body))
	decoder.UseNumber()
	if decoder.Decode(&document) != nil {
		return false, nil, nil
	}
	result, _ := document["result"].(map[string]any)
	_, hasError := document["error"]
	isError, _ := result["isError"].(bool)
	return reply.status < 400 && result != nil && !isError && !hasError, result, document
}

func recordRefs(result map[string]any) []string {
	rows := []string{}
	structured, _ := result["structuredContent"].(map[string]any)
	records, _ := structured["records"].([]any)
	for _, record := range records {
		row, _ := record.(map[string]any)
		ref, _ := row["record_ref"].(string)
		rows = append(rows, ref)
	}
	return rows
}

// memberOr is Python's dict.get(key, default) on a strictjson object; a non-object gives the default.
func memberOr(object any, key string, fallback any) any {
	o, ok := object.(*strictjson.Object)
	if !ok || o == nil {
		return fallback
	}
	if value, ok := member(o, key); ok {
		return value
	}
	return fallback
}

// plain turns a strictjson value into the encoding/json shape, keeping integers exact.
func plain(value any) any {
	switch v := value.(type) {
	case *strictjson.Object:
		out := make(map[string]any, len(v.Keys))
		for i, k := range v.Keys {
			out[k] = plain(v.Values[i])
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = plain(item)
		}
		return out
	case *big.Int:
		return json.Number(v.String())
	}
	return value
}
