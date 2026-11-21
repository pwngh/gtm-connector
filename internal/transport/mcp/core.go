package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// settings are what both transports take from their configuration.
type settings struct {
	resource      string
	rateLimit     int
	windowSeconds float64
	selected      bool
	verify        Verifier
	receipts      Recorder
	run           ToolFunc
	clock         func() float64
	wall          func() float64
}

// core is what HTTP and stdio share: the tool selection, the policy, the rate windows, the receipt store,
// and the admitted invocations the SDK's tool handlers look up.
type core struct {
	settings
	policy      Policy
	tools       []string
	definitions map[string]ToolDefinition
	server      *sdk.Server

	mu      sync.Mutex // guards windows; E3 moves the windows to PostgreSQL behind the same two calls
	windows Windows

	calls    sync.Map // dispatch id -> *Invocation
	dispatch atomic.Uint64
}

// selectTools is the one choice of tools and definitions both transports use: only a selection offers and
// defines the brief tools.
func selectTools(selected bool) ([]string, map[string]ToolDefinition) {
	tools := DefaultTools
	if selected {
		tools = SelectedTools
	}
	definitions := map[string]ToolDefinition{}
	for name, definition := range Definitions {
		if selected || (name != "prepare_renewal_brief" && name != "read_renewal_brief") {
			definitions[name] = definition
		}
	}
	return tools, definitions
}

// newCore validates a configuration as Connector.__init__ and LocalAuthority do, and builds the SDK server.
func newCore(s settings, versions []string) (*core, error) {
	resource, err := url.Parse(s.resource)
	switch {
	case err != nil || resource.Scheme != "https" || resource.Host == "" || !strings.HasPrefix(resource.Path, "/") ||
		resource.User != nil || resource.RawQuery != "" || resource.Fragment != "":
		return nil, fmt.Errorf("the resource must be an https URL with a host and a path")
	case s.rateLimit < 1 || !(s.windowSeconds > 0):
		return nil, errors.New("rate limits must be positive")
	case s.verify == nil || s.receipts == nil || s.run == nil:
		return nil, errors.New("a verifier, a receipt store and the tools are required")
	}
	if s.clock == nil {
		start := time.Now()
		s.clock = func() float64 { return time.Since(start).Seconds() }
	}
	if s.wall == nil {
		s.wall = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	c := &core{settings: s, windows: Windows{}}
	c.tools, c.definitions = selectTools(s.selected)
	origin := s.resource[:strings.LastIndex(s.resource, "/")]
	c.policy = Policy{Resource: s.resource, MetadataURL: origin + "/.well-known/oauth-protected-resource/mcp",
		RateLimit: s.rateLimit, WindowSeconds: s.windowSeconds}
	c.server = newServer(c.tools, s.run, c.invocation, versions)
	return c, nil
}

// admit runs CheckRequest under the window lock and keeps the windows it returns.
func (c *core) admit(facts RequestFacts, token *Token) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := CheckRequest(facts, c.policy, c.windows, c.definitions, token, c.wall(), c.clock())
	if decision.Windows != nil {
		c.windows = decision.Windows
	}
	return decision
}

// Invocation is one admitted tool call: the receipt committed for it and the principal it was admitted for.
// The gateway verifies the bearer once; the tool reads the scopes from here and never verifies again.
type Invocation struct {
	AuditID   string
	Tool      string
	TokenID   string // the bearer's SHA-256 fingerprint, as receipts record it
	Scopes    []string
	Principal string // who holds the bearer, as its verifier named them
	Selected  bool   // the connector serves a selection

	token  string
	core   *core
	caller context.Context // ends when the caller leaves; nil when the transport has no such moment
	failed atomic.Bool     // the tool body panicked: nothing is released
}

// Recheck verifies the caller's own bearer again and charges its rate window once more, as a composite
// read's child read does (gateway.child_read).
func (i *Invocation) Recheck() (*Token, *RequestError) {
	token := i.core.verify(i.token)
	if !validToken(token, i.core.policy, i.core.wall()) {
		return nil, &RequestError{Reason: "invalid_token", Message: "invalid or expired access token"}
	}
	if limited := i.charge(); limited != nil {
		return nil, limited
	}
	return token, nil
}

// Current verifies the caller's own bearer again and gives the scopes it carries now, which may be fewer
// than the call was admitted with. live is false when it no longer verifies, or now names another
// principal. It charges nothing: a composite call asks before each child read and once more before it
// releases its result (authority.verify_token_now), and goes by the scopes it is given then.
func (i *Invocation) Current() (scopes []string, live bool) {
	token := i.core.verify(i.token)
	if !validToken(token, i.core.policy, i.core.wall()) || token.Principal != i.Principal {
		return nil, false
	}
	return token.Scopes, true
}

// Charge counts one more attempt against the caller's rate window, as each child read of a composite call
// does, and reports whether the window had room for it.
func (i *Invocation) Charge() bool { return i.charge() == nil }

func (i *Invocation) charge() *RequestError {
	i.core.mu.Lock()
	defer i.core.mu.Unlock()
	updated, limited := ChargeRate(i.core.windows, i.token, i.core.clock(), i.core.policy)
	i.core.windows = updated
	return limited
}

// dispatchKey carries an admitted call's dispatch id in the _meta the gateway writes for the SDK. A
// client's own _meta never reaches the SDK, so a client cannot name another call.
const dispatchKey = "gtm.connector/dispatch"

// register files an admitted call under a fresh dispatch id; the caller forgets it when the reply is handled.
func (c *core) register(inv *Invocation) string {
	id := "gtm-" + strconv.FormatUint(c.dispatch.Add(1), 10)
	if inv != nil {
		inv.core = c
		c.calls.Store(id, inv)
	}
	return id
}

func (c *core) forget(id string) { c.calls.Delete(id) }

func (c *core) invocation(id string) *Invocation {
	if inv, ok := c.calls.Load(id); ok {
		return inv.(*Invocation)
	}
	return nil
}

// start commits a call's pending receipt. An empty id from the store is no receipt.
func (c *core) start(ctx context.Context, s Start) (string, error) {
	id, err := c.receipts.Start(ctx, s)
	if err == nil && id == "" {
		err = errors.New("the receipt store returned no id")
	}
	return id, err
}

// refuseCall finishes a refused call's receipt. It reports false when the store failed, which leaves the
// receipt pending, as Python leaves it.
func (c *core) refuseCall(ctx context.Context, auditID string, status int, started float64) bool {
	reason := "request_failed"
	return c.receipts.Finish(ctx, auditID, Finish{Status: "error", HTTPStatus: status, Rows: []string{},
		ElapsedMs: (c.clock() - started) * 1000, Error: &reason}) == nil
}

// outcome is how an admitted call ended: a result to release, an SDK error to pass on, or nothing at all.
type outcome struct {
	withheld bool               // nothing is released; the transport sends its fixed 503 answer
	result   *strictjson.Object // the annotated tool result
	err      *strictjson.Object // the SDK's own JSON-RPC error
}

// settle decides whether an admitted call's reply may be released, writes its receipt id into the result
// and finishes its receipt; only then may the transport write. Only a complete tool result without isError
// is released. A panic, a caller gone mid-call, a malformed result or record, or a receipt that will not
// finish releases nothing, and the receipt finishes as 503 when the store allows.
func (c *core) settle(ctx context.Context, inv *Invocation, started float64, result, rpcError *strictjson.Object, gone bool) outcome {
	failed := func() outcome {
		reason := "execution_or_audit_failure"
		c.receipts.Finish(ctx, inv.AuditID, Finish{Status: "error", HTTPStatus: 503, Rows: []string{},
			ElapsedMs: (c.clock() - started) * 1000, Error: &reason})
		return outcome{withheld: true}
	}
	if inv.failed.Load() || gone || (result == nil && rpcError == nil) {
		return failed()
	}
	if rpcError != nil {
		code, _ := member(rpcError, "code")
		status := 200
		if n, ok := code.(*big.Int); ok {
			status = errorStatus(n.Int64())
		}
		if !c.refuseCall(ctx, inv.AuditID, status, started) {
			return outcome{withheld: true}
		}
		return outcome{err: rpcError}
	}
	isError := false
	if value, present := member(result, "isError"); present {
		flag, ok := value.(bool)
		if !ok {
			return failed()
		}
		isError = flag
	}
	rows := []string{}
	if !isError {
		var ok bool
		if rows, ok = recordRefs(result); !ok {
			return failed()
		}
	}
	annotate(result, inv.AuditID)
	status, failure := "ok", (*string)(nil)
	if isError {
		reason := "request_failed"
		status, failure = "error", &reason
	}
	if err := c.receipts.Finish(ctx, inv.AuditID, Finish{Status: status, HTTPStatus: 200, Rows: rows,
		ElapsedMs: (c.clock() - started) * 1000, Error: failure}); err != nil {
		return failed()
	}
	return outcome{result: result}
}

// errorStatus is the SEP-2575 table the Python SDK applies to a JSON-RPC error code.
func errorStatus(code int64) int {
	switch code {
	case -32601:
		return 404
	case -32700, -32600, -32602, -32020, -32021, -32022:
		return 400
	}
	return 200
}

// recordRefs reads the released records' refs. Records must be a list of objects that each carry a string
// record_ref; anything else is malformed, as Python's KeyError or TypeError makes it.
func recordRefs(result *strictjson.Object) ([]string, bool) {
	rows := []string{}
	structured, _ := member(result, "structuredContent")
	object, ok := structured.(*strictjson.Object)
	if !ok || object == nil {
		return rows, true
	}
	records, present := member(object, "records")
	if !present {
		return rows, true
	}
	list, ok := records.([]any)
	if !ok {
		return nil, false
	}
	for _, record := range list {
		row, ok := record.(*strictjson.Object)
		if !ok || row == nil {
			return nil, false
		}
		ref, _ := member(row, "record_ref")
		text, ok := ref.(string)
		if !ok {
			return nil, false
		}
		rows = append(rows, text)
	}
	return rows, true
}

// annotate names a tool result's receipt in its _meta and writes the two members the SDK leaves out when
// they hold their defaults and the Python SDK always sends: isError false and resultType "complete".
func annotate(result *strictjson.Object, auditID string) {
	meta, _ := member(result, "_meta")
	object, ok := meta.(*strictjson.Object)
	if !ok || object == nil {
		object = &strictjson.Object{}
		set(result, "_meta", object)
	}
	set(object, "gtm.demo/auditId", auditID)
	if _, ok := member(result, "isError"); !ok {
		set(result, "isError", false)
	}
	if _, ok := member(result, "resultType"); !ok {
		set(result, "resultType", "complete")
	}
}

func set(object *strictjson.Object, key string, value any) {
	for i, k := range object.Keys {
		if k == key {
			object.Values[i] = value
			return
		}
	}
	object.Keys, object.Values = append(object.Keys, key), append(object.Values, value)
}

// parseReply reads one JSON-RPC reply the SDK wrote: exactly one object, nothing after it, for the expected
// dispatch id, with a result object or an error object.
func parseReply(body []byte, dispatchID string) (result, rpcError *strictjson.Object, ok bool) {
	value, err := strictjson.LoadsDecimal(body)
	document, isObject := value.(*strictjson.Object)
	if err != nil || !isObject || document == nil {
		return nil, nil, false
	}
	version, _ := member(document, "jsonrpc")
	id, _ := member(document, "id")
	resultValue, hasResult := member(document, "result")
	errorValue, hasError := member(document, "error")
	if version != "2.0" || id != dispatchID || hasResult == hasError {
		return nil, nil, false
	}
	result, _ = resultValue.(*strictjson.Object)
	rpcError, _ = errorValue.(*strictjson.Object)
	if (hasResult && result == nil) || (hasError && rpcError == nil) {
		return nil, nil, false
	}
	return result, rpcError, true
}

// encodeValue writes a strictjson value back as JSON, keeping member order and each number's digits.
func encodeValue(b *bytes.Buffer, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		encoder := json.NewEncoder(b)
		encoder.SetEscapeHTML(false)
		encoder.Encode(v)
		b.Truncate(b.Len() - 1) // the encoder's newline
	case *big.Int:
		b.WriteString(v.String())
	case strictjson.Decimal:
		b.WriteString(v.String())
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			b.WriteString("null")
		} else {
			b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		}
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeValue(b, item)
		}
		b.WriteByte(']')
	case *strictjson.Object:
		b.WriteByte('{')
		for i, key := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeValue(b, key)
			b.WriteByte(':')
			encodeValue(b, v.Values[i])
		}
		b.WriteByte('}')
	case json.RawMessage:
		b.Write(v)
	default:
		data, _ := json.Marshal(v)
		b.Write(data)
	}
}

// reply writes a JSON-RPC reply to the client's own id, exactly as the client wrote it.
func reply(clientID any, member string, value any) []byte {
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	encodeValue(&b, clientID)
	b.WriteString(`,"` + member + `":`)
	encodeValue(&b, value)
	b.WriteByte('}')
	return b.Bytes()
}

// rpcError is a JSON-RPC error reply to the client's id, or to null.
func rpcError(clientID any, code int, message string, data any) []byte {
	body := &strictjson.Object{Keys: []string{"code", "message"}, Values: []any{big.NewInt(int64(code)), message}}
	if data != nil {
		set(body, "data", data)
	}
	return reply(clientID, "error", body)
}

// reason is the {"reason": ...} data the gateway's own refusals carry.
func reason(text string) *strictjson.Object {
	return &strictjson.Object{Keys: []string{"reason"}, Values: []any{text}}
}

// echoID is the id a reply carries: the client's integer or string id, or null.
func echoID(id any) any {
	if isRequestID(id) {
		return id
	}
	return nil
}
