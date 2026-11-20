package mcp

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type wire struct {
	server   *httptest.Server
	host     string // 127.0.0.1:port
	receipts *MemoryRecorder
	fx       *fixture
}

// newWire serves the gateway on a real TLS listener, with HTTP/2 offered.
func newWire(t *testing.T) *wire {
	w := &wire{receipts: &MemoryRecorder{}}
	w.fx = newFixture(t, w.receipts)
	w.server = httptest.NewUnstartedServer(nil)
	w.server.EnableHTTP2 = true
	w.server.StartTLS()
	t.Cleanup(w.server.Close)
	w.host = strings.TrimPrefix(w.server.URL, "https://")
	resource := w.server.URL + "/mcp"
	tokens := fixtureTokens(testWall)
	for _, token := range tokens {
		token.Resource = resource
	}
	w.server.Config.Handler = mustGateway(t, GatewayConfig{Resource: resource, Issuer: testIssuer, RateLimit: 60, WindowSeconds: 60,
		Verify: verifierFor(tokens), Receipts: w.receipts, Run: w.fx.Run, Wall: func() float64 { return testWall }})
	return w
}

const wireCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account","arguments":{"account_id":"acc-001"},` +
	`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`

// raw sends one HTTP/1.x request exactly as written: target, protocol and Host lines are the caller's.
func (w *wire) raw(t *testing.T, target, protocol, hostLines string) (int, string) {
	t.Helper()
	connection, err := tls.Dial("tcp", w.host, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprintf(connection, "POST %s %s\r\n%sContent-Type: application/json\r\nAccept: application/json\r\nMcp-Protocol-Version: 2026-07-28\r\n"+
		"Mcp-Method: tools/call\r\nMcp-Name: get_account\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		target, protocol, hostLines, tokenValue("crm"), len(wireCall), wireCall)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// Each request-target form the listener can receive, checked against the wire Host Python checks.
func TestRequestTargetForms(t *testing.T) {
	w := newWire(t)
	cases := []struct {
		label, target, protocol, hostLines string
		status                             int
		receipt                            string // "" for no receipt
	}{
		{"origin-form, exact Host", "/mcp", "HTTP/1.1", "Host: " + w.host + "\r\n", 200, "ok 200"},
		{"absolute-form, another Host line", "https://" + w.host + "/mcp", "HTTP/1.1", "Host: evil.example\r\n", 403, "error 403"},
		{"absolute-form, matching Host line", "https://" + w.host + "/mcp", "HTTP/1.1", "Host: " + w.host + "\r\n", 403, "error 403"},
		{"origin-form, wrong Host", "/mcp", "HTTP/1.1", "Host: evil.example\r\n", 403, "error 403"},
		{"HTTP/1.0 without Host", "/mcp", "HTTP/1.0", "", 403, "error 403"},
		// net/http answers these two before any handler runs: a deliberate difference, with no receipt.
		{"HTTP/1.1 without Host", "/mcp", "HTTP/1.1", "", 400, ""},
		{"two Host lines", "/mcp", "HTTP/1.1", "Host: " + w.host + "\r\nHost: " + w.host + "\r\n", 400, ""},
	}
	for _, c := range cases {
		before := len(w.receipts.Read())
		ran := len(w.fx.ran())
		status, body := w.raw(t, c.target, c.protocol, c.hostLines)
		rows := w.receipts.Read()[before:]
		got := ""
		if len(rows) == 1 {
			got = fmt.Sprintf("%s %d", rows[0].Status, *rows[0].HTTPStatus)
		}
		if status != c.status || got != c.receipt || len(rows) > 1 {
			t.Errorf("%s: %d %.120s, receipt %q (want %d, %q)", c.label, status, body, got, c.status, c.receipt)
		}
		if c.status != 200 && len(w.fx.ran()) != ran {
			t.Errorf("%s: the tool ran", c.label)
		}
		if c.status == 403 && !strings.Contains(body, "untrusted_origin_or_host") {
			t.Errorf("%s: %s", c.label, body)
		}
	}
}

// Over HTTP/2 the authority is the :authority pseudo-header; it must equal the resource's host exactly.
func TestHTTP2Authority(t *testing.T) {
	w := newWire(t)
	client := w.server.Client()
	for _, c := range []struct {
		authority string
		status    int
	}{{w.host, 200}, {"evil.example", 403}, {"localhost:" + strings.Split(w.host, ":")[1], 403}} {
		request, _ := http.NewRequest(http.MethodPost, w.server.URL+"/mcp", strings.NewReader(wireCall))
		request.Host = c.authority
		for k, v := range map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Mcp-Protocol-Version": testVersion,
			"Mcp-Method": "tools/call", "Mcp-Name": "get_account", "Authorization": "Bearer " + tokenValue("crm")} {
			request.Header.Set(k, v)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.ProtoMajor != 2 || response.StatusCode != c.status {
			t.Errorf(":authority %s: HTTP/%d %d %.120s", c.authority, response.ProtoMajor, response.StatusCode, body)
		}
	}
}
