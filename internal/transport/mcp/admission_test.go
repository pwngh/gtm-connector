package mcp

import (
	"maps"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// These transcribe tests/test_request_checks.py: the same facts, policy, clocks and expectations.

func object(pairs ...any) *strictjson.Object {
	o := &strictjson.Object{}
	for i := 0; i < len(pairs); i += 2 {
		o.Keys = append(o.Keys, pairs[i].(string))
		o.Values = append(o.Values, pairs[i+1])
	}
	return o
}

const pureRaw = "fixture-pure-check-token"

func pureSetup() (RequestFacts, Policy, *Token) {
	policy := Policy{Resource: "https://127.0.0.1:8765/mcp", MetadataURL: "https://127.0.0.1:8765/metadata",
		RateLimit: 2, WindowSeconds: 60, WritesDisabled: false}
	request := RequestFacts{
		BodySize: 100, Parsed: object("id", big.NewInt(1)), Scheme: "https",
		Headers: map[string]string{"host": "127.0.0.1:8765", "mcp-protocol-version": "2026-07-28",
			"content-type": "application/json"},
		HeaderCounts: map[string]int{"host": 1, "authorization": 1, "content-type": 1},
		RawToken:     pureRaw, IsCall: true, Tool: "get_account",
		Arguments: object("account_id", "acc-001"),
		Meta:      object("io.modelcontextprotocol/protocolVersion", "2026-07-28"),
	}
	expires := 2000.0
	return request, policy, &Token{Scopes: []string{"crm:read"}, ExpiresAt: &expires, Resource: policy.Resource}
}

// test_refusals_have_fixed_reasons_statuses_and_codes
func TestRefusalsHaveFixedReasonsStatusesAndCodes(t *testing.T) {
	base, policy, token := pureSetup()
	expires := 2000.0
	writeToken := &Token{Scopes: []string{"crm:write"}, ExpiresAt: &expires, Resource: policy.Resource}
	with := func(change func(*RequestFacts)) RequestFacts {
		request := base
		request.Headers = maps.Clone(base.Headers)
		request.HeaderCounts = maps.Clone(base.HeaderCounts)
		change(&request)
		return request
	}
	key := fingerprint(pureRaw)
	cases := []struct {
		reason       string
		status, code int
		request      RequestFacts
		policy       *Policy
		windows      Windows
		token        **Token
	}{
		{"request_too_large", 413, -32600, with(func(r *RequestFacts) { r.BodySize = 65537 }), nil, nil, nil},
		{"invalid_json", 400, -32700, with(func(r *RequestFacts) { r.Parsed = nil }), nil, nil, nil},
		{"unsupported_media_type", 415, -32600, with(func(r *RequestFacts) { r.Headers["content-type"] = "text/plain" }), nil, nil, nil},
		{"unsupported_media_type", 415, -32600, with(func(r *RequestFacts) { r.HeaderCounts["content-type"]++ }), nil, nil, nil},
		{"call_id_required", 400, -32600, with(func(r *RequestFacts) { r.Parsed = object() }), nil, nil, nil},
		{"call_id_required", 400, -32600, with(func(r *RequestFacts) { r.Parsed = object("id", true) }), nil, nil, nil},
		{"untrusted_origin_or_host", 403, 1003, with(func(r *RequestFacts) { r.Scheme = "http" }), nil, nil, nil},
		{"invalid_token", 401, 1001, base, nil, nil, new((*Token)(nil))},
		{"rate_limited", 429, 1002, base, nil, Windows{key: {90, 2}}, nil},
		{"insufficient_scope", 403, 1004, with(func(r *RequestFacts) { r.Tool = "read_call_notes" }), nil, nil, nil},
		{"crm_write_disabled", 403, 1005, with(func(r *RequestFacts) { r.Tool = "log_activity" }),
			&Policy{Resource: policy.Resource, MetadataURL: policy.MetadataURL, RateLimit: 2, WindowSeconds: 60, WritesDisabled: true},
			nil, &writeToken},
		{"invalid_arguments", 400, -32602, with(func(r *RequestFacts) { r.Arguments = object() }), nil, nil, nil},
		{"missing_protocol_version", 400, -32602, with(func(r *RequestFacts) { r.Meta = object() }), nil, nil, nil},
		{"protocol_version_mismatch", 400, -32020, with(func(r *RequestFacts) {
			r.Headers = map[string]string{"host": "127.0.0.1:8765", "content-type": "application/json; charset=utf-8"}
		}), nil, nil, nil},
		{"unsupported_protocol_version", 400, -32022, with(func(r *RequestFacts) {
			r.Headers["mcp-protocol-version"] = "2099-01-01"
			r.Meta = object("io.modelcontextprotocol/protocolVersion", "2099-01-01")
		}), nil, nil, nil},
		{"method_not_found", 404, -32601, with(func(r *RequestFacts) {
			r.IsCall, r.Parsed = false, object("id", big.NewInt(1), "method", "prompts/get")
		}), nil, nil, nil},
		{"method_not_found", 404, -32601, with(func(r *RequestFacts) {
			r.IsCall, r.Parsed = false, object("id", big.NewInt(1), "method", "resources/read")
		}), nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			p, tok, windows := policy, token, c.windows
			if c.policy != nil {
				p = *c.policy
			}
			if c.token != nil {
				tok = *c.token
			}
			if windows == nil {
				windows = Windows{}
			}
			decision := CheckRequest(c.request, p, windows, Definitions, tok, 1000, 100)
			if decision.Error == nil {
				t.Fatalf("admitted; want %s", c.reason)
			}
			got := [3]any{decision.Error.Reason, decision.Error.Status(), decision.Error.Code()}
			if got != [3]any{c.reason, c.status, c.code} {
				t.Fatalf("got %v, want %v", got, [3]any{c.reason, c.status, c.code})
			}
		})
	}
}

// test_a_missing_or_expired_token_is_refused_without_charging_the_window
func TestMissingOrExpiredTokenIsRefusedWithoutCharging(t *testing.T) {
	request, policy, token := pureSetup()
	key := fingerprint(pureRaw)
	windows := Windows{key: {90, 1}}
	missing := CheckRequest(request, policy, windows, Definitions, nil, 1000, 100)
	expired := CheckRequest(request, policy, windows, Definitions, token, 2000, 100)
	for _, decision := range []Decision{missing, expired} {
		if decision.Error == nil || decision.Error.Status() != 401 || decision.Windows != nil {
			t.Fatalf("got %+v, want 401 with no charge", decision)
		}
	}
	if !reflect.DeepEqual(windows, Windows{key: {90, 1}}) {
		t.Fatalf("windows changed: %v", windows)
	}
}

// test_repeated_denial_returns_same_quota_update_without_mutating_inputs
func TestRepeatedDenialReturnsSameQuotaUpdate(t *testing.T) {
	request, policy, token := pureSetup()
	request.Tool = "read_call_notes"
	key := fingerprint(pureRaw)
	windows := Windows{key: {90, 0}, "expired-fixture-window": {40, 1}}
	before := maps.Clone(windows)
	first := CheckRequest(request, policy, windows, Definitions, token, 1000, 100)
	second := CheckRequest(request, policy, windows, Definitions, token, 1000, 100)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("decisions differ: %+v %+v", first, second)
	}
	if first.Error.Status() != 403 || !strings.Contains(first.Error.Headers["WWW-Authenticate"], `scope="calls:read"`) {
		t.Fatalf("got %+v", first.Error)
	}
	if !reflect.DeepEqual(first.Windows, Windows{key: {90, 1}}) {
		t.Fatalf("windows %v", first.Windows)
	}
	if !reflect.DeepEqual(windows, before) {
		t.Fatalf("input windows changed: %v", windows)
	}
}

// test_limited_call_prunes_expired_windows_without_incrementing
func TestLimitedCallPrunesExpiredWindows(t *testing.T) {
	request, policy, token := pureSetup()
	key := fingerprint(pureRaw)
	windows := Windows{key: {90, 2}, "expired-fixture-window": {40, 1}}
	decision := CheckRequest(request, policy, windows, Definitions, token, 1000, 100)
	if decision.Error.Status() != 429 || !reflect.DeepEqual(decision.Error.Headers, map[string]string{"Retry-After": "50"}) {
		t.Fatalf("got %+v", decision.Error)
	}
	if !reflect.DeepEqual(decision.Windows, Windows{key: {90, 2}}) {
		t.Fatalf("windows %v", decision.Windows)
	}
	if len(windows) != 2 {
		t.Fatalf("input windows changed: %v", windows)
	}
}
