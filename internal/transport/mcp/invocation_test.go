package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingVerifier counts how often the gateway verifies a bearer.
func countingVerifier(inner Verifier, count *atomic.Int64) Verifier {
	return func(raw string) *Token {
		count.Add(1)
		return inner(raw)
	}
}

// One admitted invocation: the bearer is verified once, and the tool sees the receipt and scopes it was
// admitted under, on both transports.
func TestToolSeesTheAdmittedInvocation(t *testing.T) {
	var verified atomic.Int64
	var seen *Invocation
	var h *harness
	h = newHarness(t, func(c *GatewayConfig) {
		c.Verify = countingVerifier(c.Verify, &verified)
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			seen = call.Invocation
			return h.fx.Run(ctx, call)
		}
	})
	h.records(h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil), "crm")
	rows := h.receipts.Read()
	if seen == nil || seen.AuditID != rows[0].AuditID || seen.Tool != "get_account" || strings.Join(seen.Scopes, " ") != "crm:read" {
		t.Fatalf("invocation %+v, receipt %s", seen, rows[0].AuditID)
	}
	if verified.Load() != 1 {
		t.Fatalf("HTTP verified the bearer %d times", verified.Load())
	}

	verified.Store(0)
	seen = nil
	var s *stdioHarness
	s = newStdio(t, []string{"crm:read"}, func(c *StdioConfig) {
		c.Verify = countingVerifier(c.Verify, &verified)
		c.Run = func(ctx context.Context, call Call) (json.RawMessage, error) {
			seen = call.Invocation
			return s.fx.Run(ctx, call)
		}
	})
	s.send(legacyStart, legacyReady)
	s.answer(1)
	verified.Store(0)
	s.send(call(2, "get_account", `{"account_id":"acc-001"}`))
	s.answer(2)
	if seen == nil || seen.AuditID != s.receipts.Read()[0].AuditID || verified.Load() != 1 {
		t.Fatalf("stdio invocation %+v, verified %d times", seen, verified.Load())
	}
}

// A receipt store that answers without an id has not committed a receipt: nothing runs.
func TestEmptyReceiptIDIsRefused(t *testing.T) {
	h := newHarness(t)
	h.receipts.NewID = func(int) string { return "" }
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"audit_unavailable; call was not executed"}` || len(h.fx.ran()) != 0 {
		t.Fatalf("HTTP: %d %s ran %v", response.Code, response.Body, h.fx.ran())
	}
	s := newStdio(t, []string{"crm:read"}, nil)
	s.receipts.NewID = func(int) string { return "" }
	s.send(legacyStart, legacyReady)
	s.answer(1)
	s.send(call(2, "get_account", `{"account_id":"acc-001"}`))
	if reply := s.answer(2); path(reply, "error", "message") != "audit_unavailable; call was not executed" || len(s.fx.ran()) != 0 {
		t.Fatalf("stdio: %v", reply)
	}
}

// An unexpected error from a tool body never reaches the client in its own words.
func TestUnexpectedToolErrorsAreFixed(t *testing.T) {
	h := newHarness(t, func(c *GatewayConfig) {
		c.Run = func(context.Context, Call) (json.RawMessage, error) {
			return nil, errors.New("connection to db://fixture-secret failed")
		}
	})
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
	text := jsonOf(path(decode(t, response.Body.Bytes()), "result", "content"))
	if response.Code != 200 || strings.Contains(response.Body.String(), "fixture-secret") ||
		!strings.Contains(text, "Error executing tool get_account: execution_failed") {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}

// A panic in a tool body is contained: nothing is released and the receipt finishes as 503.
func TestToolPanicIsWithheld(t *testing.T) {
	h := newHarness(t, func(c *GatewayConfig) {
		c.Run = func(context.Context, Call) (json.RawMessage, error) { panic("fixture panic") }
	})
	response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
	rows := h.receipts.Read()
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"execution_or_audit_unavailable; no result released"}` ||
		len(rows) != 1 || rows[0].Status != "error" || *rows[0].HTTPStatus != 503 || rows[0].Error == nil || *rows[0].Error != "execution_or_audit_failure" {
		t.Fatalf("HTTP: %d %s %+v", response.Code, response.Body, rows)
	}
	s := newStdio(t, []string{"crm:read"}, func(c *StdioConfig) {
		c.Run = func(context.Context, Call) (json.RawMessage, error) { panic("fixture panic") }
	})
	s.send(legacyStart, legacyReady)
	s.answer(1)
	s.send(call(2, "get_account", `{"account_id":"acc-001"}`))
	reply := s.answer(2)
	rows = s.receipts.Read()
	if path(reply, "error", "message") != "execution_or_audit_unavailable; no result released" || len(rows) != 1 || *rows[0].HTTPStatus != 503 {
		t.Fatalf("stdio: %v %+v", reply, rows)
	}
}

// A client that goes away mid-call gets no release: the receipt finishes as 503, not 200.
func TestClientGoneMidCallIsRecordedAs503(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var h *harness
	h = newHarness(t, func(c *GatewayConfig) {
		c.Run = func(runCtx context.Context, call Call) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			return h.fx.Run(runCtx, call)
		}
	})
	request := newRequest(http.MethodPost, testResource,
		bytes.NewReader(callBody("1", "get_account", `{"account_id":"acc-001"}`, vectorMeta))).WithContext(ctx)
	for _, p := range vectorHeaders("tools/call", "get_account") {
		request.Header.Add(p[0], p[1])
	}
	go func() { <-started; cancel() }()
	h.gateway.ServeHTTP(httptest.NewRecorder(), request)
	rows := h.receipts.Read()
	if len(rows) != 1 || rows[0].Status != "error" || *rows[0].HTTPStatus != 503 {
		t.Fatalf("%+v", rows)
	}
}

// lockedWriter records what reaches the caller, safely while ServeHTTP runs.
type lockedWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *lockedWriter) Header() http.Header { return w.header }
func (w *lockedWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}
func (w *lockedWriter) Write(p []byte) (int, error) {
	w.WriteHeader(200)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *lockedWriter) written() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.body.Len()
}

// Nothing reaches the caller while the receipt is still being finished.
func TestBlockedFinishReleasesNothing(t *testing.T) {
	h := newHarness(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	h.receipts.BeforeFinish = func(string) {
		close(blocked)
		<-release
	}
	request := newRequest(http.MethodPost, testResource,
		bytes.NewReader(callBody("1", "get_account", `{"account_id":"acc-001"}`, vectorMeta)))
	for _, p := range vectorHeaders("tools/call", "get_account") {
		request.Header.Add(p[0], p[1])
	}
	writer := &lockedWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() { h.gateway.ServeHTTP(writer, request); close(done) }()
	<-blocked
	time.Sleep(100 * time.Millisecond)
	if status, size := writer.written(); status != 0 || size != 0 {
		t.Fatalf("released %d with %d bytes before the receipt finished", status, size)
	}
	close(release)
	<-done
	if status, size := writer.written(); status != 200 || size == 0 {
		t.Fatalf("after: %d %d", status, size)
	}
}

// A released result is a complete tool result: its records each carry a string record_ref, or nothing is
// released and the receipt finishes as 503, as Python's KeyError does.
func TestMalformedRecordsAreWithheld(t *testing.T) {
	for _, structured := range []string{
		`{"source":"crm","records":[{"id":"acc-001"}],"untrusted_data":true}`,
		`{"source":"crm","records":[{"id":"acc-001","record_ref":7}],"untrusted_data":true}`,
		`{"source":"crm","records":"crm/accounts/acc-001","untrusted_data":true}`,
		`{"source":"crm","records":["crm/accounts/acc-001"],"untrusted_data":true}`,
	} {
		h := newHarness(t, func(c *GatewayConfig) {
			c.Run = func(context.Context, Call) (json.RawMessage, error) { return json.RawMessage(structured), nil }
		})
		response := h.tool("get_account", map[string]any{"account_id": "acc-001"}, "crm", nil)
		rows := h.receipts.Read()
		if response.Code != 503 || strings.Contains(response.Body.String(), "acc-001") || len(rows) != 1 ||
			rows[0].Status != "error" || *rows[0].HTTPStatus != 503 {
			t.Errorf("%s: %d %s %+v", structured, response.Code, response.Body, rows)
		}
	}
}

// Configuration Python refuses is refused here, and a resource with no path is an error, not a panic.
func TestConfigurationIsValidated(t *testing.T) {
	base := GatewayConfig{Resource: testResource, Issuer: testIssuer, RateLimit: 60, WindowSeconds: 60,
		Verify: verifierFor(fixtureTokens(testWall)), Receipts: &MemoryRecorder{},
		Run: func(context.Context, Call) (json.RawMessage, error) { return nil, nil }}
	for _, change := range []func(*GatewayConfig){
		func(c *GatewayConfig) { c.Resource = "https:" },
		func(c *GatewayConfig) { c.Resource = "127.0.0.1:8765" },
		func(c *GatewayConfig) { c.Resource = "http://127.0.0.1:8765/mcp" },
		func(c *GatewayConfig) { c.RateLimit = 0 },
		func(c *GatewayConfig) { c.WindowSeconds = 0 },
		func(c *GatewayConfig) { c.Receipts = nil },
	} {
		config := base
		change(&config)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%+v panicked: %v", config, r)
				}
			}()
			if _, err := NewGateway(config); err == nil {
				t.Errorf("%+v was accepted", config)
			}
		}()
	}
	stdio := StdioConfig{Token: "x", Resource: testResource, RateLimit: 0, WindowSeconds: 60, Verify: base.Verify,
		Receipts: base.Receipts, Run: base.Run}
	if err := ServeStdio(context.Background(), stdio, nopReader{}, nopWriter{}); err == nil {
		t.Error("a zero stdio rate limit was accepted")
	}
}

type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, errors.New("closed") }
func (nopReader) Close() error             { return nil }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriter) Close() error                { return nil }
