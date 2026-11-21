// Package mcp serves the connector's MCP tools on streamable HTTP and stdio. A request is admitted here, in
// gtm_connector.request_checks' order and then the Python SDK's, under a receipt committed before anything
// runs. Every refusal a client can see is decided here. Only an admitted request reaches the Go MCP SDK,
// rebuilt from its admitted fields under a dispatch id of the connector's own, and its reply leaves only after
// the receipt is finished, carrying the client's own id.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Verifier returns the token a raw bearer names, or nil.
type Verifier func(raw string) *Token

// GatewayConfig is what one HTTP gateway serves and how it checks callers.
type GatewayConfig struct {
	Resource      string // the exact https://host:port/path URL tokens are issued for
	Issuer        string
	RateLimit     int
	WindowSeconds float64
	Selected      bool // a selection offers the brief tools
	Verify        Verifier
	Receipts      Recorder
	Run           ToolFunc
	Clock         func() float64 // the rate clock, in seconds
	Wall          func() float64 // the wall clock, in seconds since the epoch
}

// Gateway is the /mcp endpoint and its protected-resource metadata.
type Gateway struct {
	*core
	issuer string
	path   string
	sdk    http.Handler
}

// NewGateway checks the configuration and builds a gateway over a stateless, JSON-response SDK handler
// restricted to ProtocolVersion.
func NewGateway(config GatewayConfig) (*Gateway, error) {
	c, err := newCore(settings{resource: config.Resource, rateLimit: config.RateLimit, windowSeconds: config.WindowSeconds,
		selected: config.Selected, verify: config.Verify, receipts: config.Receipts,
		run: config.Run, clock: config.Clock, wall: config.Wall}, []string{ProtocolVersion})
	if err != nil {
		return nil, err
	}
	resource, _ := url.Parse(config.Resource) // newCore has checked it
	g := &Gateway{core: c, issuer: config.Issuer, path: resource.Path}
	g.sdk = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return c.server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true, MaxRequestBodyBytes: -1})
	return g, nil
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

// answer is a reply held until its receipt finishes.
type answer struct {
	status  int
	headers map[string]string
	body    []byte // nil sends no body and no Content-Type
}

func jsonAnswer(status int, body []byte, headers map[string]string) answer {
	return answer{status: status, body: body, headers: headers}
}

func (a answer) write(w http.ResponseWriter, head bool) {
	for k, v := range a.headers {
		w.Header().Set(k, v)
	}
	if a.body != nil {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(a.body)))
	w.WriteHeader(a.status)
	if !head {
		w.Write(a.body)
	}
}

var withheld = jsonAnswer(http.StatusServiceUnavailable, []byte(`{"error":"execution_or_audit_unavailable; no result released"}`), nil)

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource"+g.path:
		body, _ := json.Marshal(map[string]any{"resource": g.policy.Resource, "authorization_servers": []string{g.issuer},
			"scopes_supported": []string{"crm:read"}, "bearer_methods_supported": []string{"header"}})
		jsonAnswer(http.StatusOK, body, nil).write(w, false)
	case r.URL.Path != g.path:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "Not Found")
	case r.Method == http.MethodPost:
		g.post(w, r)
	case !g.trusted(r):
		// Every method answers only the resource's own Host over TLS, with no Origin, before anything else
		// is said. The Python gateway answers its 405 first.
		jsonAnswer(http.StatusForbidden, rpcError(nil, Refusals["untrusted_origin_or_host"].Code, "untrusted origin or host",
			reason("untrusted_origin_or_host")), nil).write(w, r.Method == http.MethodHead)
	case r.Method == http.MethodGet:
		// A GET would open a listening stream that never ends and passes none of the checks below.
		jsonAnswer(http.StatusMethodNotAllowed, rpcError(nil, -32600, "Method Not Allowed", nil), map[string]string{"Allow": "POST"}).write(w, false)
	case r.Method == http.MethodDelete:
		jsonAnswer(http.StatusMethodNotAllowed, rpcError(nil, -32600, "Method Not Allowed: Session termination not supported", nil), nil).write(w, false)
	default:
		jsonAnswer(http.StatusMethodNotAllowed, rpcError(nil, -32600, "Method Not Allowed", nil),
			map[string]string{"Allow": "GET, POST, DELETE"}).write(w, r.Method == http.MethodHead)
	}
}

// latin1 reads a header value byte by byte, as Starlette decodes it.
func latin1(value string) string {
	runes := make([]rune, len(value))
	for i := 0; i < len(value); i++ {
		runes[i] = rune(value[i])
	}
	return string(runes)
}

// carrier is what the gateway reads from one request's HTTP envelope. Header names are lowercase. The
// gateway's own checks read the last value of a header, as Connector builds its dict; the SDK's checks read
// the first, as Starlette's Headers do.
type carrier struct {
	last, first map[string]string
	counts      map[string]int
}

// readCarrier takes the wire authority as Python sees it: an HTTP/1 Host line, only for an origin-form
// target, or the HTTP/2 :authority with any Host field counted as a second one.
func readCarrier(r *http.Request) carrier {
	c := carrier{last: map[string]string{}, first: map[string]string{}, counts: map[string]int{}}
	for name, values := range r.Header {
		key := strings.ToLower(name)
		c.counts[key] += len(values)
		c.first[key] = latin1(values[0])
		c.last[key] = latin1(values[len(values)-1])
	}
	switch {
	case r.ProtoMajor >= 2:
		c.counts["host"]++
		c.first["host"], c.last["host"] = latin1(r.Host), latin1(r.Host)
	case strings.HasPrefix(r.RequestURI, "/") && r.Host != "":
		c.counts["host"] = 1
		c.first["host"], c.last["host"] = latin1(r.Host), latin1(r.Host)
	default:
		// An absolute-form target hides the Host line behind the target's authority; HTTP/1.0 may send none.
		delete(c.first, "host")
		delete(c.last, "host")
		c.counts["host"] = 0
	}
	return c
}

// trusted is the Host and Origin check of a request that is no POST, as admission makes it of one that is.
func (g *Gateway) trusted(r *http.Request) bool {
	headers := readCarrier(r)
	return TrustedHost(scheme(r), headers.last, headers.counts, g.policy)
}

func (g *Gateway) post(w http.ResponseWriter, r *http.Request) {
	headers := readCarrier(r)
	// A receipt is written under a context that outlives the caller: one who leaves after its call was read
	// still has the call's receipt committed and finished.
	ctx := context.WithoutCancel(r.Context())
	started := g.clock()
	rawAuth := headers.last["authorization"]
	parts := strings.Split(rawAuth, " ")
	rawToken := ""
	if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" && parts[1] != "" {
		rawToken = parts[1]
	}
	var callerToken *string
	if rawToken != "" {
		callerToken = &rawToken
	} else if rawAuth != "" {
		callerToken = &rawAuth
	}
	header, hasName := headers.last["mcp-name"]
	if !hasName {
		header = "<unparsed>"
	}
	marked := headers.last["mcp-method"] == "tools/call"
	unparsed := func(body []byte) *strictjson.Object {
		sum := sha256.Sum256(body)
		return strictjson.NewObject("unparsed_sha256", hex.EncodeToString(sum[:]))
	}
	invalidJSON := jsonAnswer(Refusals["invalid_json"].Status, rpcError(nil, Refusals["invalid_json"].Code,
		"expected one valid JSON-RPC object", reason("invalid_json")), nil)

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		// The body did not arrive whole, so nothing in it is read. A request its header marks as a tool call
		// still gets its receipt, under the header's tool name, with what did arrive named by its digest.
		if marked {
			tool := header
			if callerToken != nil && strings.Contains(tool, *callerToken) {
				tool = "<invalid>"
			}
			auditID, err := g.start(ctx, Start{Token: callerToken, Tool: tool, Arguments: unparsed(body)})
			if err != nil {
				jsonAnswer(http.StatusServiceUnavailable, []byte(`{"error":"audit_unavailable; call was not executed"}`), nil).write(w, false)
				return
			}
			if !g.refuseCall(ctx, auditID, invalidJSON.status, started) {
				withheld.write(w, false)
				return
			}
		}
		invalidJSON.write(w, false)
		return
	}
	request, err := strictjson.Loads(body)
	tooDeepTool, tooDeep := "", false
	if err != nil {
		request = nil
		if errors.Is(err, strictjson.ErrTooDeep) {
			// A body refused for its depth is well encoded and is not parsed at all, so what else may be wrong
			// with it is not known. Its method and its tool name are read without descending, so a tool call
			// it makes keeps its receipt, under its own tool's name.
			method, name, named := shallowCall(body)
			if method == "tools/call" {
				marked, tooDeep = true, named
				tooDeepTool = name
			}
		} else if refused, err := strictjson.LoadsLoose(body); err == nil {
			// A refused body that still names a tool call, as json.loads reads it, keeps its receipt.
			method, _ := memberOr(refused, "method", nil).(string)
			marked = marked || method == "tools/call"
		}
	}
	object, _ := request.(*strictjson.Object)
	params, _ := memberOr(object, "params", nil).(*strictjson.Object)
	if params == nil {
		params = &strictjson.Object{}
	}
	method, _ := member(object, "method")
	isCall := method == "tools/call" || marked
	tool, ok := memberOr(params, "name", header).(string)
	switch {
	case tooDeep:
		tool = tooDeepTool
	case !ok:
		tool = "<invalid>"
	}
	arguments := memberOr(params, "arguments", &strictjson.Object{})
	meta := memberOr(params, "_meta", &strictjson.Object{})
	var briefID *string
	if value, ok := memberOr(meta, "gtm.demo/briefId", nil).(string); ok && utf8.RuneCountInString(value) <= 128 {
		briefID = &value
	}
	requestID, _ := member(object, "id")
	// A request that repeats the credential it presents, in its id, its tool name, its arguments or its
	// brief id, is refused in fixed words, and its receipt holds none of them: a receipt and a reply never
	// carry a credential, whoever put it there.
	repeated := callerToken != nil && (holds(requestID, *callerToken) || holds(tool, *callerToken) ||
		holds(arguments, *callerToken) || briefID != nil && strings.Contains(*briefID, *callerToken))
	// PostgreSQL holds no NUL character in JSON text, so arguments with one, in a name or in a value, are
	// refused and never recorded as given.
	unrecordable := holds(arguments, "\x00")
	auditID := ""
	if isCall {
		// JSON-RPC ids are strings or integers. Any other value is recorded as a fixed marker, so a caller
		// cannot imitate the server's own correlation for brief reads.
		var receiptID any = "<invalid>"
		if requestID == nil || isRequestID(requestID) {
			receiptID = plain(requestID)
		}
		recorded, recordedTool, recordedBrief := arguments, tool, briefID
		switch {
		case request == nil:
			recorded = unparsed(body)
			if callerToken != nil && strings.Contains(tool, *callerToken) {
				recordedTool = "<invalid>"
			}
		case repeated:
			recorded, receiptID, recordedBrief = strictjson.NewObject("unrecorded", "the request repeated its credential"), "<invalid>", nil
			if holds(tool, *callerToken) {
				recordedTool = "<invalid>"
			}
		case unrecordable:
			recorded = strictjson.NewObject("unrecorded", "the arguments hold a NUL character")
		}
		if auditID, err = g.start(ctx, Start{Token: callerToken, Tool: recordedTool, Arguments: recorded, RequestID: receiptID,
			BriefID: recordedBrief}); err != nil {
			jsonAnswer(http.StatusServiceUnavailable, []byte(`{"error":"audit_unavailable; call was not executed"}`), nil).write(w, false)
			return
		}
	}

	var refused *answer
	var token *Token
	if repeated && request != nil {
		// Refused before anything else is checked, so no later refusal echoes the id.
		e := &RequestError{Reason: "credential_in_request", Message: "the request repeats the credential it presents"}
		a := jsonAnswer(e.Status(), rpcError(nil, e.Code(), e.Message, reason(e.Reason)), nil)
		refused = &a
	} else {
		if rawToken != "" {
			token = g.verify(rawToken) // the only verification of this bearer for this request
		}
		decision := g.admit(RequestFacts{BodySize: len(body), Parsed: request, Scheme: scheme(r), Headers: headers.last,
			HeaderCounts: headers.counts, RawToken: rawToken, IsCall: isCall, Tool: tool, Arguments: arguments, Meta: meta}, token)
		if e := decision.Error; e != nil {
			a := jsonAnswer(e.Status(), rpcError(echoID(requestID), e.Code(), e.Message, reason(e.Reason)), e.Headers)
			refused = &a
		} else if isCall && unrecordable {
			e := &RequestError{Reason: "invalid_arguments", Message: "invalid tool arguments"}
			a := jsonAnswer(e.Status(), rpcError(echoID(requestID), e.Code(), e.Message, reason(e.Reason)), nil)
			refused = &a
		} else if a, ok := ladder(object, headers); ok {
			refused = &a
		}
	}
	switch {
	case refused != nil:
		if auditID != "" && !g.refuseCall(ctx, auditID, refused.status, started) {
			withheld.write(w, false)
			return
		}
		refused.write(w, false)
	case !isCall:
		g.dispatch(r, object, tool, arguments, nil).write(w, false)
	default:
		inv := &Invocation{AuditID: auditID, Tool: tool, TokenID: fingerprint(rawToken), Scopes: token.Scopes,
			Principal: token.Principal, Selected: g.selected, token: rawToken, caller: r.Context()}
		g.dispatch(r, object, tool, arguments, inv).write(w, false)
	}
}

// ladder applies the checks the Python SDK makes on an admitted 2026-07-28 request, in its order and with
// its words (mcp/server/_streamable_http_modern.py and mcp/shared/inbound.py in mcp 2.3.0), then its schema
// check and handler lookup. A notification is acknowledged and dropped, as the Python SDK does.
func ladder(object *strictjson.Object, headers carrier) (answer, bool) {
	invalidBody := jsonAnswer(400, rpcError(nil, -32600, "Body must be a single JSON-RPC request or notification object", nil), nil)
	hasJSON, hasSSE := accepts(headers.first["accept"])
	if !hasJSON {
		return answer{status: 406}, true
	}
	version, _ := member(object, "jsonrpc")
	method, _ := member(object, "method")
	name, isText := method.(string)
	params, hasParams := member(object, "params")
	paramsObject, _ := params.(*strictjson.Object)
	envelope := version == "2.0" && isText && (!hasParams || params == nil || paramsObject != nil)
	id, hasID := member(object, "id")
	if !hasID {
		if !envelope {
			return invalidBody, true
		}
		return answer{status: http.StatusAccepted}, true
	}
	if !envelope || !isRequestID(id) {
		return invalidBody, true
	}
	if name == "subscriptions/listen" && !hasSSE {
		return answer{status: 406}, true
	}
	mismatch := func(message string) (answer, bool) {
		return jsonAnswer(400, rpcError(id, -32020, message, nil), nil), true
	}
	// First-copy and last-copy readers of a routing header would disagree.
	for _, header := range []string{"mcp-protocol-version", "mcp-method", "mcp-name"} {
		if headers.counts[header] > 1 {
			return mismatch(header + " header appears more than once")
		}
	}
	meta, _ := memberOr(paramsObject, "_meta", nil).(*strictjson.Object)
	if meta == nil {
		return jsonAnswer(400, rpcError(id, -32602, "params._meta must be an object carrying the required "+
			"'io.modelcontextprotocol/protocolVersion' and 'io.modelcontextprotocol/clientCapabilities' envelope keys", nil), nil), true
	}
	var missing []string
	for _, key := range []string{metaProtocolVersion, metaClientCapabilities} {
		if _, ok := member(meta, key); !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return jsonAnswer(400, rpcError(id, -32602, "params._meta is missing the required envelope key(s): "+strings.Join(missing, ", "), nil), nil), true
	}
	if header, ok := headers.first["mcp-protocol-version"]; !ok || memberOr(meta, metaProtocolVersion, nil) != header {
		return mismatch("mcp-protocol-version header does not match the request envelope's protocol version")
	}
	if headers.first["mcp-method"] != name || headers.counts["mcp-method"] == 0 {
		return mismatch("mcp-method header does not match the request body's method")
	}
	if key := map[string]string{"tools/call": "name", "prompts/get": "name", "resources/read": "uri"}[name]; key != "" {
		if value := memberOr(paramsObject, key, nil); value != nil {
			header, present := headers.first["mcp-name"]
			decoded, ok := decodeHeaderValue(header)
			if !present || !ok || decoded != value {
				return mismatch("mcp-name header does not match the request body's '" + key + "' parameter")
			}
		}
	}
	if schemaMethods[name] && !validParams(name, paramsObject) {
		return jsonAnswer(400, rpcError(id, -32602, "Invalid request parameters", ""), nil), true
	}
	if !servedMethods[name] {
		return jsonAnswer(404, rpcError(id, -32601, "Method not found", name), nil), true
	}
	return answer{}, false
}

// accepts is the Python SDK's check_accept_headers: whether the Accept header admits JSON and SSE, with
// wildcards. A missing or empty header admits neither.
func accepts(header string) (hasJSON, hasSSE bool) {
	for _, part := range strings.Split(header, ",") {
		kind := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(part), ";", 2)[0]))
		hasJSON = hasJSON || kind == "*/*" || kind == "application/json" || kind == "application/*"
		hasSSE = hasSSE || kind == "*/*" || kind == "text/event-stream" || kind == "text/*"
	}
	return hasJSON, hasSSE
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

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// dispatch sends an admitted request to the SDK, rebuilt from the admitted fields under a dispatch id, with
// the client's carrier headers left behind. A call's reply is settled against its receipt before it is
// returned; any other reply is returned to the client's id.
//
// A caller who leaves while its call runs is answered nothing, and the rule for the call is one: the tool is
// told (its context ends), it publishes nothing unless a publication had already started, and the receipt
// is finished all the same, as an error unless that publication sealed it.
func (g *Gateway) dispatch(r *http.Request, object *strictjson.Object, tool string, arguments any, inv *Invocation) answer {
	method, _ := member(object, "method")
	name := method.(string)
	clientID, _ := member(object, "id")
	started := g.clock()
	dispatchID := g.register(inv)
	defer g.forget(dispatchID)
	meta := &strictjson.Object{Keys: []string{metaProtocolVersion, metaClientCapabilities},
		Values: []any{ProtocolVersion, &strictjson.Object{}}}
	params := &strictjson.Object{Keys: []string{"_meta"}, Values: []any{meta}}
	if inv != nil {
		set(meta, dispatchKey, dispatchID)
		if args, ok := arguments.(*strictjson.Object); !ok || args == nil {
			arguments = &strictjson.Object{}
		}
		params = &strictjson.Object{Keys: []string{"name", "arguments", "_meta"}, Values: []any{tool, arguments, meta}}
	}
	body := &strictjson.Object{Keys: []string{"jsonrpc", "id", "method", "params"}, Values: []any{"2.0", dispatchID, name, params}}
	var encoded bytes.Buffer
	encodeValue(&encoded, body)
	// The SDK runs the request to its end even if the caller leaves, so the reply is always seen and settled
	// here. The tool body learns that the caller left through its own context (toolHandler).
	inner, _ := http.NewRequestWithContext(context.WithoutCancel(r.Context()), http.MethodPost, "/mcp", &encoded)
	inner.Header.Set("Content-Type", "application/json")
	inner.Header.Set("Accept", "application/json, text/event-stream")
	inner.Header.Set("Mcp-Protocol-Version", ProtocolVersion)
	inner.Header.Set("Mcp-Method", name)
	if inv != nil {
		inner.Header.Set("Mcp-Name", tool)
	}
	held := &holder{header: http.Header{}}
	g.sdk.ServeHTTP(held, inner)
	result, rpcErr, ok := parseReply(held.body.Bytes(), dispatchID)
	if held.status != 0 && held.status != http.StatusOK {
		ok = false
	}
	if inv != nil {
		if !ok {
			result, rpcErr = nil, nil
		}
		settled := g.settle(context.WithoutCancel(r.Context()), inv, started, result, rpcErr, r.Context().Err() != nil)
		switch {
		case settled.withheld:
			return withheld
		case settled.err != nil:
			code, _ := member(settled.err, "code")
			status := 200
			if n, ok := code.(*big.Int); ok {
				status = errorStatus(n.Int64())
			}
			return jsonAnswer(status, reply(clientID, "error", settled.err), nil)
		}
		return jsonAnswer(http.StatusOK, reply(clientID, "result", settled.result), nil)
	}
	if !ok {
		return withheld
	}
	if rpcErr != nil {
		code, _ := member(rpcErr, "code")
		n, _ := code.(*big.Int)
		if n != nil && n.Int64() == -32601 {
			return jsonAnswer(404, rpcError(clientID, -32601, "Method not found", name), nil)
		}
		return withheld
	}
	return jsonAnswer(http.StatusOK, reply(clientID, "result", result), nil)
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

// holds reports whether any string of a strictjson value, a member name included, contains text.
func holds(value any, text string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, text)
	case []any:
		for _, item := range v {
			if holds(item, text) {
				return true
			}
		}
	case *strictjson.Object:
		if v == nil {
			return false
		}
		for i, key := range v.Keys {
			if strings.Contains(key, text) || holds(v.Values[i], text) {
				return true
			}
		}
	}
	return false
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
