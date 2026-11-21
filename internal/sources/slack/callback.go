package slack

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
)

// The one authorization callback of a sign-in, received as gtm_connector/slack_callback.py receives it: on
// loopback sockets of the registered port only, from a request whose every part is checked, and finished
// before the browser is told the sign-in is saved. A request that is not the callback is answered and
// ignored, up to maxCallbackRequests of them.

const (
	maxCallbackRequests = 128
	maxRequestLine      = 8192
	maxHeaderBytes      = 16384
	maxQuery            = 8192
	requestTime         = 2 * time.Second
	browserTime         = 10 * time.Second
	maxAuthorizeURL     = 16384
)

var (
	callbackSaved = []byte("<!doctype html><title>Slack authentication</title>" +
		"<p>Slack authentication is saved. Return to the terminal.</p>")
	callbackFailed = []byte("<!doctype html><title>Slack authentication</title>" +
		"<p>Slack authentication did not finish. Return to the terminal.</p>")
	callbackInvalid = []byte("<!doctype html><title>Local callback</title><p>This request was not accepted.</p>")
)

// receiveConsent listens on the redirect's port, opens the browser at the authorization page made for a new
// state, and waits for the callback that carries that state. Given an approval's code it runs finish, and
// answers the browser only after finish returns. The whole wait ends after loginTime.
func receiveConsent(ctx context.Context, cfg Configuration, authorize func(state string) string, open Browser,
	finish func(ctx context.Context, code string) error) error {
	ctx, cancel := context.WithTimeout(ctx, loginTime)
	defer cancel()
	timedOut := func() error {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return refuse("slack_login_timeout")
	}
	state := randomText(32)
	listeners, err := listenLoopback(cfg.port)
	if err != nil {
		return err
	}
	closeAll := func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
	defer closeAll()
	page := authorize(state)
	if page == "" || len(page) > maxAuthorizeURL || strings.ContainsFunc(page, func(c rune) bool { return c < 32 || c == 127 }) {
		return refuse("slack_browser_unavailable")
	}
	opening, stop := context.WithTimeout(ctx, browserTime)
	err = open(opening, page)
	stop()
	switch {
	case ctx.Err() != nil:
		return timedOut()
	case err != nil:
		return refuse("slack_browser_unavailable")
	}
	connections := make(chan net.Conn)
	for _, listener := range listeners {
		go func(listener net.Listener) {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				select {
				case connections <- conn:
				case <-ctx.Done():
					_ = conn.Close()
					return
				}
			}
		}(listener)
	}
	for range maxCallbackRequests {
		var conn net.Conn
		select {
		case conn = <-connections:
		case <-ctx.Done():
			return timedOut()
		}
		fields, status := callbackFields(ctx, conn, cfg.port, state)
		if fields == nil {
			respond(conn, status, callbackInvalid)
			_ = conn.Close()
			continue
		}
		closeAll()
		err := func() error {
			defer conn.Close()
			if reason, refused := fields["error"]; refused {
				respond(conn, 400, callbackFailed)
				if reason == "access_denied" {
					return refuse("slack_login_denied")
				}
				return refuse("slack_oauth_rejected")
			}
			if err := finish(ctx, fields["code"]); err != nil {
				respond(conn, 500, callbackFailed)
				return err
			}
			if ctx.Err() != nil {
				respond(conn, 500, callbackFailed)
				return timedOut()
			}
			respond(conn, 200, callbackSaved)
			return nil
		}()
		return err
	}
	return refuse("slack_callback_unavailable")
}

// listenLoopback listens on 127.0.0.1 and ::1 at the port. A machine without one of the two families
// listens on the other. A port that is taken refuses with slack_callback_unavailable.
func listenLoopback(port int) ([]net.Listener, error) {
	var listeners []net.Listener
	for _, address := range []struct{ network, host string }{{"tcp4", "127.0.0.1"}, {"tcp6", "::1"}} {
		listener, err := net.Listen(address.network, net.JoinHostPort(address.host, strconv.Itoa(port)))
		if err != nil {
			if errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT) ||
				errors.Is(err, syscall.EADDRNOTAVAIL) {
				continue
			}
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, refuse("slack_callback_unavailable")
		}
		listeners = append(listeners, listener)
	}
	if len(listeners) == 0 {
		return nil, refuse("slack_callback_unavailable")
	}
	return listeners, nil
}

// callbackFields reads one request and gives its query fields when it is the callback for this state: a
// GET of /slack/callback with the Host localhost:PORT, no body, and either a code or an error. Any other
// request gives nil and the status to answer it with.
func callbackFields(ctx context.Context, conn net.Conn, port int, state string) (map[string]string, int) {
	deadline := time.Now().Add(requestTime)
	if limit, set := ctx.Deadline(); set && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetReadDeadline(deadline)
	var data []byte
	chunk := make([]byte, 1024)
	for !bytes.Contains(data, []byte("\r\n\r\n")) {
		n, err := conn.Read(chunk)
		data = append(data, chunk[:n]...)
		end := bytes.Index(data, []byte("\r\n"))
		if (end < 0 && len(data) > maxRequestLine) || end+2 > maxRequestLine ||
			(end >= 0 && len(data)-end-2 > maxHeaderBytes) {
			return nil, 400
		}
		if err != nil && !bytes.Contains(data, []byte("\r\n\r\n")) {
			return nil, 400
		}
	}
	head, trailing, _ := bytes.Cut(data, []byte("\r\n\r\n"))
	if len(trailing) > 0 {
		return nil, 400
	}
	lines := strings.Split(string(head), "\r\n")
	parts := strings.Split(lines[0], " ")
	for _, c := range []byte(lines[0]) {
		if c > 127 {
			return nil, 400
		}
	}
	if len(parts) != 3 {
		return nil, 400
	}
	method, target, version := parts[0], parts[1], parts[2]
	if method != "GET" {
		return nil, 405
	}
	if (version != "HTTP/1.0" && version != "HTTP/1.1") || !strings.HasPrefix(target, "/") || strings.Contains(target, "#") {
		return nil, 400
	}
	path, query, asked := strings.Cut(target, "?")
	if path != "/slack/callback" {
		return nil, 404
	}
	if !asked || len(query) > maxQuery || len(lines)-1 > 32 {
		return nil, 400
	}
	headers := map[string]string{}
	for _, line := range lines[1:] {
		name, value, colon := strings.Cut(line, ":")
		if !colon || name == "" || strings.ContainsFunc(name, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+.^_`|~-", c))
		}) {
			return nil, 400
		}
		for _, c := range []byte(value) {
			if c < 32 && c != 9 || c == 127 {
				return nil, 400
			}
		}
		key := strings.ToLower(name)
		if _, again := headers[key]; again {
			return nil, 400
		}
		headers[key] = strings.Trim(value, " \t")
	}
	if strings.ToLower(headers["host"]) != fmt.Sprintf("localhost:%d", port) {
		return nil, 400
	}
	if _, chunked := headers["transfer-encoding"]; chunked {
		return nil, 400
	}
	if length, stated := headers["content-length"]; stated && length != "0" {
		return nil, 400
	}
	if len(evidence.Percents([]rune(query))) > 0 {
		return nil, 400
	}
	fields, valid := queryFields(query)
	if !valid {
		return nil, 400
	}
	given := fields["state"]
	if len(given) > 128 || strings.ContainsFunc(given, func(c rune) bool { return c > 127 }) ||
		subtle.ConstantTimeCompare([]byte(given), []byte(state)) != 1 {
		return nil, 400
	}
	code, coded := fields["code"]
	reason, refused := fields["error"]
	if coded == refused {
		return nil, 400
	}
	visible := func(s string, most int) bool {
		return len(s) <= most && !strings.ContainsFunc(s, func(c rune) bool { return c < 33 || c > 126 })
	}
	if coded && (len(fields) != 2 || !visible(code, 4096)) {
		return nil, 400
	}
	if refused && (!visible(reason, 128) || len(fields["error_description"]) > 1024 || len(fields["error_uri"]) > 1024) {
		return nil, 400
	}
	return fields, 0
}

// queryFields reads a query as parse_qsl reads it with strict parsing and at most five fields: every field
// is NAME=VALUE, percent escapes decode to UTF-8, a plus is a space, and a field is one of the callback's
// own, given once, with a value that has no control character.
func queryFields(query string) (map[string]string, bool) {
	pairs := strings.Split(query, "&")
	if len(pairs) > 5 {
		return nil, false
	}
	fields := map[string]string{}
	for _, pair := range pairs {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, false
		}
		name, errName := url.QueryUnescape(name)
		value, errValue := url.QueryUnescape(value)
		if errName != nil || errValue != nil || !utf8.ValidString(name) || !utf8.ValidString(value) {
			return nil, false
		}
		switch name {
		case "state", "code", "error", "error_description", "error_uri":
		default:
			return nil, false
		}
		if _, again := fields[name]; again || value == "" ||
			strings.ContainsFunc(value, func(c rune) bool { return c < 32 || c == 127 }) {
			return nil, false
		}
		fields[name] = value
	}
	return fields, true
}

// respond answers the browser and closes nothing. A browser that has gone does not undo a finished sign-in.
func respond(conn net.Conn, status int, body []byte) {
	reasons := map[int]string{200: "OK", 400: "Bad Request", 404: "Not Found", 405: "Method Not Allowed",
		500: "Internal Server Error"}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\n"+
		"Cache-Control: no-store\r\nReferrer-Policy: no-referrer\r\nX-Content-Type-Options: nosniff\r\n"+
		"Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; base-uri 'none'\r\n"+
		"Connection: close\r\n\r\n%s", status, reasons[status], len(body), body)
}
