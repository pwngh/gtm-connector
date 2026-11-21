package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// MaxFrame is the longest stdio line read whole. A longer line is refused unread: it gets no receipt,
// because nothing in it was read to tell whether it was a tool call.
const MaxFrame = 4 << 20

// StdioConfig is one stdio server's machine principal and limits.
type StdioConfig struct {
	Token         string // the machine principal's bearer, verified once for each request that needs it
	Resource      string
	RateLimit     int
	WindowSeconds float64
	Selected      bool
	Verify        Verifier
	Receipts      Recorder
	Run           ToolFunc
	Clock         func() float64
	Wall          func() float64
}

// stdioMethods are the requests the stdio server answers. initialize and ping serve legacy clients, as the
// bridge serves them today; every other request is the 404 the HTTP gateway gives.
var stdioMethods = map[string]bool{"initialize": true, "ping": true, "server/discover": true, "tools/list": true, "tools/call": true}

// gatedMethods pass the same admission as an HTTP request the stdio bridge forwards today.
var gatedMethods = map[string]bool{"tools/list": true, "tools/call": true, "prompts/get": true, "resources/read": true}

// ServeStdio serves MCP on in and out until in ends. The connector owns the framing: one JSON-RPC message
// per line, bounded before anything decodes it. Each admitted request reaches the SDK under a dispatch id of
// the connector's own, and its reply goes out to the client's exact id, after a call's receipt is finished.
func ServeStdio(ctx context.Context, config StdioConfig, in io.Reader, out io.Writer) error {
	c, err := newCore(settings{resource: config.Resource, rateLimit: config.RateLimit, windowSeconds: config.WindowSeconds,
		selected: config.Selected, verify: config.Verify, receipts: config.Receipts,
		run: config.Run, clock: config.Clock, wall: config.Wall}, nil)
	if err != nil {
		return err
	}
	conn := newStdioConn(c, config.Token, out)
	go conn.readLoop(in)
	err = c.server.Run(ctx, stdioTransport{conn})
	conn.abandon()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type stdioTransport struct{ conn *stdioConn }

func (t stdioTransport) Connect(context.Context) (sdk.Connection, error) { return t.conn, nil }

// pendingStdio is one request the SDK is answering.
type pendingStdio struct {
	clientID any    // the client's id as it wrote it
	key      string // that id's canonical text, for the in-flight check
	inv      *Invocation
	started  float64
}

type stdioConn struct {
	core     *core
	token    string
	incoming chan jsonrpc.Message
	closed   chan struct{}
	once     sync.Once

	writeMu sync.Mutex
	out     io.Writer

	mu       sync.Mutex
	pending  map[string]*pendingStdio // dispatch id -> request
	inflight map[string]string        // client id key -> dispatch id
}

func newStdioConn(c *core, token string, out io.Writer) *stdioConn {
	return &stdioConn{core: c, token: token, out: out, incoming: make(chan jsonrpc.Message), closed: make(chan struct{}),
		pending: map[string]*pendingStdio{}, inflight: map[string]string{}}
}

func (s *stdioConn) SessionID() string { return "" }

func (s *stdioConn) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// Read hands the SDK the next admitted message.
func (s *stdioConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case message, ok := <-s.incoming:
		if !ok {
			return nil, io.EOF
		}
		return message, nil
	case <-s.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stdioConn) send(line []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.out.Write(append(line, '\n'))
}

// Write takes the SDK's reply to a dispatch id, settles a call against its receipt, and writes the reply to
// the client's id. A reply with no pending request is never written, and nothing the server starts on its
// own is sent.
func (s *stdioConn) Write(ctx context.Context, message jsonrpc.Message) error {
	response, ok := message.(*jsonrpc.Response)
	if !ok {
		return nil
	}
	dispatchID, _ := response.ID.Raw().(string)
	s.mu.Lock()
	p := s.pending[dispatchID]
	delete(s.pending, dispatchID)
	if p != nil {
		delete(s.inflight, p.key)
	}
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	encoded, err := jsonrpc.EncodeMessage(response)
	if err != nil {
		encoded = nil
	}
	result, rpcErr, parsed := parseReply(encoded, dispatchID)
	if p.inv != nil {
		defer s.core.forget(dispatchID)
		if !parsed {
			result, rpcErr = nil, nil
		}
		settled := s.core.settle(ctx, p.inv, p.started, result, rpcErr, false)
		switch {
		case settled.withheld:
			s.send(rpcError(p.clientID, -32603, "execution_or_audit_unavailable; no result released", nil))
		case settled.err != nil:
			s.send(reply(p.clientID, "error", settled.err))
		default:
			s.send(reply(p.clientID, "result", settled.result))
		}
		return nil
	}
	switch {
	case !parsed:
		s.send(rpcError(p.clientID, -32603, "Internal error", nil))
	case rpcErr != nil:
		s.send(reply(p.clientID, "error", rpcErr))
	default:
		s.send(reply(p.clientID, "result", result))
	}
	return nil
}

// abandon finishes every call still pending when the server stops: nothing was released, so each receipt
// finishes as 503.
func (s *stdioConn) abandon() {
	s.mu.Lock()
	pending := s.pending
	s.pending, s.inflight = map[string]*pendingStdio{}, map[string]string{}
	s.mu.Unlock()
	for id, p := range pending {
		if p.inv != nil {
			s.core.settle(context.Background(), p.inv, p.started, nil, nil, true)
			s.core.forget(id)
		}
	}
}

// readLoop reads one line at a time, bounded, and passes each admitted message to Read.
func (s *stdioConn) readLoop(in io.Reader) {
	defer close(s.incoming)
	reader := bufio.NewReaderSize(in, 64<<10)
	for {
		line, tooLong, err := readFrame(reader)
		if tooLong {
			s.send(rpcError(nil, -32600, "request body exceeds 65536 bytes", reason("request_too_large")))
		} else if len(bytes.TrimSpace(line)) > 0 {
			if message := s.frame(line); message != nil {
				select {
				case s.incoming <- message:
				case <-s.closed:
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// readFrame reads up to a newline. Past MaxFrame it stops keeping bytes and reports the line too long.
func readFrame(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > MaxFrame+1 {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, tooLong, err
	}
}

// frame admits one line. It answers a refusal itself and returns nil, or returns the rebuilt message for
// the SDK.
func (s *stdioConn) frame(raw []byte) jsonrpc.Message {
	c := s.core
	ctx := context.Background()
	started := c.clock()
	value, err := strictjson.Loads(raw)
	if err != nil {
		// A line the strict parser refuses but that json.loads reads as a tool call keeps its receipt. So
		// does one refused only for its depth, which is not parsed: its method and tool name are read
		// without descending, and its receipt is under that tool's name.
		tool, isCall := "<unparsed>", false
		if errors.Is(err, strictjson.ErrTooDeep) {
			method, name, named := shallowCall(raw)
			if isCall = method == "tools/call"; isCall && named {
				tool = name
			}
		} else if loose, err := strictjson.LoadsLoose(raw); err == nil {
			isCall = memberOr(loose, "method", nil) == "tools/call"
		}
		if isCall {
			sum := sha256.Sum256(raw)
			token := s.token
			auditID, err := c.start(ctx, Start{Token: &token, Tool: tool,
				Arguments: strictjson.NewObject("unparsed_sha256", hex.EncodeToString(sum[:]))})
			if err != nil {
				s.send(rpcError(nil, -32603, "audit_unavailable; call was not executed", nil))
				return nil
			}
			if !c.refuseCall(ctx, auditID, 400, started) {
				s.send(rpcError(nil, -32603, "execution_or_audit_unavailable; no result released", nil))
				return nil
			}
		}
		s.send(rpcError(nil, -32700, "expected one valid JSON-RPC object", reason("invalid_json")))
		return nil
	}
	object, ok := value.(*strictjson.Object)
	if !ok {
		// One message per line: a batch is refused whole, as the HTTP gateway refuses it.
		s.send(rpcError(nil, -32700, "expected one valid JSON-RPC object", reason("invalid_json")))
		return nil
	}
	methodValue, hasMethod := member(object, "method")
	id, hasID := member(object, "id")
	if !hasMethod {
		_, isResult := member(object, "result")
		_, isError := member(object, "error")
		if !(hasID && (isResult || isError)) {
			s.send(rpcError(nil, -32600, "Body must be a single JSON-RPC request or notification object", nil))
		}
		return nil // the server asks the client nothing, so a reply from it answers nothing
	}
	method, _ := methodValue.(string)
	params, _ := memberOr(object, "params", nil).(*strictjson.Object)
	isCall := method == "tools/call"
	auditID := ""
	refuse := func(body []byte, status int) jsonrpc.Message {
		if auditID != "" && !c.refuseCall(ctx, auditID, status, started) {
			body = rpcError(echoID(id), -32603, "execution_or_audit_unavailable; no result released", nil)
		}
		if hasID {
			s.send(body)
		}
		return nil
	}
	if isCall {
		tool, ok := memberOr(params, "name", "<unparsed>").(string)
		if !ok {
			tool = "<invalid>"
		}
		var requestID any = "<invalid>"
		if !hasID || isRequestID(id) {
			requestID = plain(id)
		}
		token := s.token
		arguments := memberOr(params, "arguments", &strictjson.Object{})
		unrecordable := holds(arguments, "\x00")
		if unrecordable {
			arguments = strictjson.NewObject("unrecorded", "the arguments hold a NUL character")
		}
		if auditID, err = c.start(ctx, Start{Token: &token, Tool: tool, Arguments: arguments, RequestID: requestID}); err != nil {
			if hasID {
				s.send(rpcError(echoID(id), -32603, "audit_unavailable; call was not executed", nil))
			}
			return nil
		}
		if unrecordable {
			return refuse(rpcError(echoID(id), Refusals["invalid_arguments"].Code, "invalid tool arguments", reason("invalid_arguments")),
				Refusals["invalid_arguments"].Status)
		}
	}
	if !hasID {
		if isCall {
			// A call without an id would run and answer no one: refused, as the gateway refuses it.
			status := 400
			if e := s.admit(object, params, isCall).Error; e != nil {
				status = e.Status()
			}
			return refuse(nil, status)
		}
		if method == "notifications/initialized" {
			return s.decode(object, nil)
		}
		return nil
	}
	key := ""
	if isRequestID(id) {
		var b bytes.Buffer
		encodeValue(&b, id)
		key = b.String()
		s.mu.Lock()
		_, busy := s.inflight[key]
		s.mu.Unlock()
		if busy {
			return refuse(rpcError(id, -32600, "request ID already in use", nil), 400)
		}
	}
	var token *Token
	if gatedMethods[method] {
		decision := s.admit(object, params, isCall)
		token = decision.token
		if e := decision.Error; e != nil {
			return refuse(rpcError(echoID(id), e.Code(), e.Message, reason(e.Reason)), e.Status())
		}
	}
	if refusal, status := stdioLadder(object, method, params, id); refusal != nil {
		return refuse(refusal, status)
	}
	var inv *Invocation
	if isCall {
		tool, _ := member(params, "name")
		inv = &Invocation{AuditID: auditID, Tool: tool.(string), TokenID: fingerprint(s.token), Scopes: token.Scopes,
			Principal: token.Principal, Selected: c.selected, token: s.token}
	}
	dispatchID := c.register(inv)
	message := s.decode(object, &dispatchID)
	if message == nil {
		c.forget(dispatchID)
		return refuse(rpcError(id, -32603, "Internal error", nil), 500)
	}
	s.mu.Lock()
	s.pending[dispatchID] = &pendingStdio{clientID: id, key: key, inv: inv, started: started}
	s.inflight[key] = dispatchID
	s.mu.Unlock()
	return message
}

type stdioDecision struct {
	Decision
	token *Token
}

// admit runs the gateway's check on the facts the bridge's forwarded HTTPS request carries: its own host,
// content type, bearer and 2026-07-28 metadata, so only the principal, rate, call id, scope, write, argument
// and method checks can refuse. The machine bearer is verified once, here.
func (s *stdioConn) admit(object, params *strictjson.Object, isCall bool) stdioDecision {
	resource, _ := url.Parse(s.core.policy.Resource)
	tool, ok := memberOr(params, "name", "<unparsed>").(string)
	if !ok {
		tool = "<invalid>"
	}
	token := s.core.verify(s.token)
	var b bytes.Buffer
	encodeValue(&b, object)
	facts := RequestFacts{BodySize: b.Len(), Parsed: object, Scheme: "https",
		Headers:      map[string]string{"host": resource.Host, "content-type": "application/json", "mcp-protocol-version": ProtocolVersion},
		HeaderCounts: map[string]int{"host": 1, "content-type": 1, "authorization": 1},
		RawToken:     s.token, IsCall: isCall, Tool: tool, Arguments: memberOr(params, "arguments", &strictjson.Object{}),
		Meta: &strictjson.Object{Keys: []string{metaProtocolVersion}, Values: []any{ProtocolVersion}}}
	return stdioDecision{s.core.admit(facts, token), token}
}

// stdioLadder checks what the SDK would otherwise refuse in its own words: the envelope, a 2026-07-28
// request's metadata and schema, a call's shape, and the methods served.
func stdioLadder(object *strictjson.Object, method string, params *strictjson.Object, id any) ([]byte, int) {
	version, _ := member(object, "jsonrpc")
	rawParams, hasParams := member(object, "params")
	if version != "2.0" || (hasParams && rawParams != nil && params == nil) || !isRequestID(id) {
		return rpcError(nil, -32600, "Body must be a single JSON-RPC request or notification object", nil), 400
	}
	invalid := rpcError(id, -32602, "Invalid request parameters", "")
	meta, _ := memberOr(params, "_meta", nil).(*strictjson.Object)
	if _, modern := member(meta, metaProtocolVersion); modern {
		var missing []string
		for _, key := range []string{metaProtocolVersion, metaClientCapabilities} {
			if _, ok := member(meta, key); !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return rpcError(id, -32602, "params._meta is missing the required envelope key(s): "+strings.Join(missing, ", "), nil), 400
		}
		requested, _ := member(meta, metaProtocolVersion)
		if text, ok := requested.(string); !ok {
			return rpcError(id, -32602, "the protocol-version envelope value must be a string", nil), 400
		} else if text != ProtocolVersion {
			data := &strictjson.Object{Keys: []string{"supported", "requested"}, Values: []any{[]any{ProtocolVersion}, text}}
			return rpcError(id, -32022, "Unsupported protocol version", data), 400
		}
		if schemaMethods[method] && !validParams(method, params) {
			return invalid, 400
		}
	} else if method == "tools/call" {
		name, _ := member(params, "name")
		if !isString(name) || !optional(member(params, "arguments"))(isObject) {
			return invalid, 400
		}
	}
	if !stdioMethods[method] {
		return rpcError(id, -32601, "Method not found", method), 404
	}
	return nil, 0
}

// decode rebuilds a message for the SDK. A request takes its dispatch id; a call's params are rebuilt from
// the admitted name and arguments, with _meta written by the connector.
func (s *stdioConn) decode(object *strictjson.Object, dispatchID *string) jsonrpc.Message {
	method, _ := member(object, "method")
	params, hasParams := member(object, "params")
	rebuilt := &strictjson.Object{Keys: []string{"jsonrpc", "method"}, Values: []any{"2.0", method}}
	if dispatchID != nil {
		set(rebuilt, "id", *dispatchID)
	}
	if method == "tools/call" && dispatchID != nil {
		object := params.(*strictjson.Object)
		name, _ := member(object, "name")
		arguments, _ := member(object, "arguments")
		if args, ok := arguments.(*strictjson.Object); !ok || args == nil {
			arguments = &strictjson.Object{}
		}
		meta := &strictjson.Object{Keys: []string{dispatchKey}, Values: []any{*dispatchID}}
		if clientMeta, _ := member(object, "_meta"); memberOr(clientMeta, metaProtocolVersion, nil) != nil {
			meta = &strictjson.Object{Keys: []string{metaProtocolVersion, metaClientCapabilities, dispatchKey},
				Values: []any{ProtocolVersion, &strictjson.Object{}, *dispatchID}}
		}
		params, hasParams = &strictjson.Object{Keys: []string{"name", "arguments", "_meta"}, Values: []any{name, arguments, meta}}, true
	}
	if hasParams && params != nil {
		set(rebuilt, "params", params)
	}
	var b bytes.Buffer
	encodeValue(&b, rebuilt)
	message, err := jsonrpc.DecodeMessage(b.Bytes())
	if err != nil {
		return nil
	}
	return message
}
