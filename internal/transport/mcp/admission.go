package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"slices"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// ProtocolVersion is the only MCP revision the HTTP gateway admits.
const ProtocolVersion = "2026-07-28"

// MaxBody is the largest request body, in bytes, the gateway reads.
const MaxBody = 65536

// Refusal is a reason's HTTP status and JSON-RPC code, as request_checks.REFUSALS states them.
type Refusal struct {
	Status int
	Code   int
}

// Refusals is request_checks.REFUSALS.
var Refusals = map[string]Refusal{
	"request_too_large":            {413, -32600},
	"invalid_json":                 {400, -32700},
	"unsupported_media_type":       {415, -32600},
	"call_id_required":             {400, -32600},
	"method_not_found":             {404, -32601},
	"untrusted_origin_or_host":     {403, 1003},
	"invalid_token":                {401, 1001},
	"rate_limited":                 {429, 1002},
	"insufficient_scope":           {403, 1004},
	"crm_write_disabled":           {403, 1005},
	"invalid_arguments":            {400, -32602},
	"missing_protocol_version":     {400, -32602},
	"protocol_version_mismatch":    {400, -32020},
	"unsupported_protocol_version": {400, -32022},
}

// Token is a verified access token. A verifier returns nil for any token it does not know.
type Token struct {
	Scopes    []string
	ExpiresAt *float64 // seconds since the epoch; nil means the token never states an expiry and is refused
	Resource  string
	// Principal is who holds the token, as the verifier knows them. The gate decides nothing by it and hands
	// it to the tool with the admitted call.
	Principal string
}

// Window is one credential's rate window: its start on the rate clock and the attempts counted in it.
type Window struct {
	Begin float64
	Used  int
}

// Windows maps a credential's SHA-256 fingerprint to its window.
type Windows map[string]Window

// RequestFacts are what admission reads from one request, as request_checks.RequestFacts holds them.
// Header names are lowercase; Headers holds the last value of each, HeaderCounts how often each name came.
type RequestFacts struct {
	BodySize     int
	Parsed       any // a strictjson value, or nil when the body did not parse
	Scheme       string
	Headers      map[string]string
	HeaderCounts map[string]int
	RawToken     string
	IsCall       bool
	Tool         string
	Arguments    any
	Meta         any
}

// Policy is request_checks.RequestPolicy.
type Policy struct {
	Resource      string
	MetadataURL   string
	RateLimit     int
	WindowSeconds float64
}

// RequestError is one refusal with its reason, message and response headers.
type RequestError struct {
	Reason  string
	Message string
	Headers map[string]string
}

func (e *RequestError) Status() int { return Refusals[e.Reason].Status }
func (e *RequestError) Code() int   { return Refusals[e.Reason].Code }

// Decision is the first refusal, if any, and the updated windows when this request was charged.
type Decision struct {
	Error   *RequestError
	Windows Windows // nil when the request was not charged
}

func fingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ChargeRate counts one attempt against this credential's window, or refuses it when the window is full.
// The given windows are never changed.
func ChargeRate(windows Windows, raw string, rateTime float64, policy Policy) (Windows, *RequestError) {
	key := fingerprint(raw)
	updated := Windows{}
	for name, value := range windows {
		if rateTime-value.Begin < policy.WindowSeconds {
			updated[name] = value
		}
	}
	current, ok := updated[key]
	if !ok {
		current = Window{Begin: rateTime}
	}
	if current.Used >= policy.RateLimit {
		retry := max(1, int(math.Ceil(policy.WindowSeconds-(rateTime-current.Begin))))
		return updated, &RequestError{"rate_limited", fmt.Sprintf("rate_limited: retry after %d seconds", retry),
			map[string]string{"Retry-After": fmt.Sprint(retry)}}
	}
	updated[key] = Window{Begin: current.Begin, Used: current.Used + 1}
	return updated, nil
}

// CheckRequest returns the first refusal in request_checks.check_request's fixed order, or none.
// Inputs are never changed. token is nil when the bearer did not verify.
func CheckRequest(request RequestFacts, policy Policy, windows Windows, definitions map[string]ToolDefinition,
	token *Token, wallTime, rateTime float64) Decision {
	refuse := func(updated Windows, reason, message string, headers map[string]string) Decision {
		return Decision{Error: &RequestError{reason, message, headers}, Windows: updated}
	}
	if request.BodySize > MaxBody {
		return refuse(nil, "request_too_large", "request body exceeds 65536 bytes", nil)
	}
	parsed, ok := request.Parsed.(*strictjson.Object)
	if !ok || parsed == nil {
		return refuse(nil, "invalid_json", "expected one valid JSON-RPC object", nil)
	}
	resource, err := url.Parse(policy.Resource)
	_, hasOrigin := request.Headers["origin"]
	if request.Scheme != "https" || hasOrigin || request.HeaderCounts["host"] != 1 || err != nil ||
		request.Headers["host"] != resource.Host {
		return refuse(nil, "untrusted_origin_or_host", "untrusted origin or host", nil)
	}
	raw := request.RawToken
	if request.HeaderCounts["authorization"] != 1 {
		raw = ""
	}
	if raw == "" || !validToken(token, policy, wallTime) {
		return refuse(nil, "invalid_token", "invalid or expired access token", map[string]string{
			"WWW-Authenticate": fmt.Sprintf(`Bearer error="invalid_token", resource_metadata="%s"`, policy.MetadataURL),
		})
	}
	var updated Windows
	if request.IsCall {
		// Every call a credential makes counts against its window, refused or not.
		var limited *RequestError
		updated, limited = ChargeRate(windows, raw, rateTime, policy)
		if limited != nil {
			return Decision{Error: limited, Windows: updated}
		}
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(request.Headers["content-type"], ";", 2)[0]))
	if request.HeaderCounts["content-type"] != 1 || contentType != "application/json" {
		return refuse(updated, "unsupported_media_type", "Content-Type must be application/json", nil)
	}
	if request.IsCall {
		id, _ := member(parsed, "id")
		if !isRequestID(id) {
			// Without an id the call is a notification: nothing would run and nothing would answer.
			return refuse(updated, "call_id_required", "a tool call requires a string or integer id", nil)
		}
		if request.Tool == writeTool {
			// No scope opens the write tool, so its refusal asks for none.
			return refuse(updated, "crm_write_disabled", "crm_write_disabled", nil)
		}
		definition, known := definitions[request.Tool]
		if known && definition.Scope != "" && !slices.Contains(token.Scopes, definition.Scope) {
			challenge := fmt.Sprintf(`Bearer error="insufficient_scope", scope="%s", resource_metadata="%s"`,
				definition.Scope, policy.MetadataURL)
			return refuse(updated, "insufficient_scope", fmt.Sprintf("insufficient_scope: %s required", definition.Scope),
				map[string]string{"WWW-Authenticate": challenge})
		}
		if known && !ValidArguments(request.Tool, definition, request.Arguments) {
			return refuse(updated, "invalid_arguments", "invalid tool arguments", nil)
		}
	}
	var version any
	if meta, ok := request.Meta.(*strictjson.Object); ok {
		version, _ = member(meta, "io.modelcontextprotocol/protocolVersion")
	}
	if version == nil {
		return refuse(updated, "missing_protocol_version", "request metadata requires protocolVersion", nil)
	}
	header, present := request.Headers["mcp-protocol-version"]
	if text, ok := version.(string); !ok || !present || header != text {
		return refuse(updated, "protocol_version_mismatch", "protocol version header must match request metadata", nil)
	}
	if version != ProtocolVersion {
		return refuse(updated, "unsupported_protocol_version", "supported protocol version: "+ProtocolVersion, nil)
	}
	if method, _ := member(parsed, "method"); method == "prompts/get" || method == "resources/read" {
		// The connector serves tools only, so no prompt or resource exists to return.
		return refuse(updated, "method_not_found", "Method not found", nil)
	}
	return Decision{Windows: updated}
}

// validToken reports whether a verified token admits anything now: it states an expiry that has not come,
// it was issued for this resource, and every scope it carries is a read scope, as auth.verify_token_now
// requires. A token with a scope outside that set is refused whole, never trimmed to the scopes that are.
func validToken(token *Token, policy Policy, wallTime float64) bool {
	if token == nil || token.ExpiresAt == nil || *token.ExpiresAt <= wallTime || token.Resource != policy.Resource {
		return false
	}
	for _, scope := range token.Scopes {
		if !slices.Contains(ReadScopes, scope) {
			return false
		}
	}
	return true
}

// isRequestID reports whether a strictjson value is a JSON-RPC id Python accepts: an integer or a string,
// never a boolean.
func isRequestID(value any) bool {
	switch value.(type) {
	case string, *big.Int:
		return true
	}
	return false
}
