package slack

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/echo"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// readTimeout is how long a response may take to start, and how long may pass between two reads of its
// body. A connection may take sourcehttp.ConnectTime to open, and its handshake as long. Each is cut to
// the time the read has left.
const readTimeout = 5 * time.Second

// budget is the one deadline and the cumulative counters of a read. The requests that keep the sign-in
// usable draw on the same budget as the pages.
type budget struct {
	clock     Clock
	started   time.Duration
	deadline  time.Duration
	startedAt time.Time

	httpAttempts, authAttempts, messageAttempts int
	responseBytes, authBytes, messageBytes      int
	refreshAttempts                             int
	tokenRetried, rateRetried                   bool

	// tokens are every access and refresh token the read has loaded or been given. A pair that a refresh
	// replaced stays among them to the end of the read.
	tokens echo.Tokens
}

// hold puts a sign-in's tokens among those the read looks for in what Slack sends back.
func (b *budget) hold(document *Credentials) {
	b.tokens.Add(document.AccessToken.Reveal())
	b.tokens.Add(document.RefreshToken.Reveal())
}

func newBudget(clock Clock) *budget {
	started := clock.monotonic()
	return &budget{clock: clock, started: started, deadline: started + operationTime, startedAt: clock.now()}
}

// remaining is the time left before the deadline, or the refusal slack_timeout once it has passed.
func (b *budget) remaining() (time.Duration, error) {
	left := b.deadline - b.clock.monotonic()
	if left <= 0 {
		return 0, refuse("slack_timeout")
	}
	return left, nil
}

// store runs one call to a store under a context that ends when the read's time does. A call that returns
// after that time is refused as slack_timeout whatever it returned: a store that gave up waiting may
// answer as if it had found nothing. The caller's own cancel stays what it is.
func (b *budget) store(ctx context.Context, call func(context.Context) error) error {
	left, err := b.remaining()
	if err != nil {
		return err
	}
	limited, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	err = call(limited)
	if errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	if limited.Err() != nil {
		return refuse("slack_timeout")
	}
	if _, late := b.remaining(); late != nil {
		return late
	}
	return err
}

// beginRequest counts one HTTP attempt, of a page ("messages") or of the sign-in ("auth").
func (b *budget) beginRequest(kind string) error {
	if _, err := b.remaining(); err != nil {
		return err
	}
	if b.httpAttempts >= maxRequests {
		return refuse("slack_resource_limit")
	}
	b.httpAttempts++
	if kind == "auth" {
		b.authAttempts++
	} else {
		b.messageAttempts++
	}
	return nil
}

// room is how many response bytes the read may still take.
func (b *budget) room() int { return maxResponseBytes - b.responseBytes }

// addBytes counts response bytes as they arrive, so an oversized body is refused before it is held.
func (b *budget) addBytes(amount int, kind string) error {
	b.responseBytes += amount
	if kind == "auth" {
		b.authBytes += amount
	} else {
		b.messageBytes += amount
	}
	if b.responseBytes > maxResponseBytes {
		return refuse("slack_resource_limit")
	}
	_, err := b.remaining()
	return err
}

// beginRefresh allows the one refresh of a read. A rotation is followed by an identity check, so both
// attempts must be left before the refresh token is spent.
func (b *budget) beginRefresh() error {
	if _, err := b.remaining(); err != nil {
		return err
	}
	if b.refreshAttempts >= 1 {
		return refuse("slack_reconnect_required")
	}
	if maxRequests-b.httpAttempts < 2 {
		return refuse("slack_resource_limit")
	}
	b.refreshAttempts++
	return nil
}

var ownTransport = sync.OnceValue(func() http.RoundTripper { return newTransport(sourcehttp.Direct, nil) })

// newTransport is the reader's own transport, the one the Salesforce reader makes too: verified TLS, no
// proxy, HTTP/1.1 only, and nothing a server sends unasked in the process log. It makes one connection a
// request. On a reused connection that closes before it answers, net/http sends a GET again by itself, and
// that second send would not be counted.
func newTransport(open sourcehttp.Dial, roots *x509.CertPool) *http.Transport {
	transport := sourcehttp.Transport(open, roots)
	transport.ResponseHeaderTimeout = readTimeout
	return transport
}

func (r *Reader) transport() http.RoundTripper {
	if r.Transport != nil {
		return r.Transport
	}
	return ownTransport()
}

// errTokenExpired is Slack's exact token_expired answer to a page request.
var errTokenExpired = errors.New("slack: the access token has expired")

// answer is one HTTP response read whole.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// headerValue is a response header as one string, or fallback when the response does not carry it.
func headerValue(header http.Header, name, fallback string) string {
	values := header.Values(name)
	if len(values) == 0 {
		return fallback
	}
	return strings.Join(values, ", ")
}

func digits(s string, most int) bool {
	if len(s) == 0 || len(s) > most {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// lowerASCII lowers the letters A to Z and leaves every other byte as it is.
func lowerASCII(s string) string {
	lowered := []byte(s)
	for i, c := range lowered {
		if c >= 'A' && c <= 'Z' {
			lowered[i] = c + 'a' - 'A'
		}
	}
	return string(lowered)
}

// send makes one attempt within the budget and reads the response whole. Redirects are not followed. The
// request asks for an unencoded body, and any other encoding is refused, so the bytes counted are the bytes
// on the wire.
func (r *Reader) send(ctx context.Context, b *budget, kind string, build func(context.Context) (*http.Request, error)) (*answer, error) {
	if err := ctx.Err(); err != nil {
		// The caller has gone: nothing is sent and no attempt is counted.
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, refuse("slack_timeout")
	}
	if err := b.beginRequest(kind); err != nil {
		return nil, err
	}
	left, err := b.remaining()
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	var stalled atomic.Bool
	failed := func() error {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return ctx.Err()
		case ctx.Err() != nil || bounded.Err() != nil || stalled.Load():
			return refuse("slack_timeout")
		}
		return nil
	}
	request, err := build(bounded)
	if err != nil {
		return nil, refuse("slack_unavailable")
	}
	// The connection is not kept for another request, whatever transport carries this one.
	request.Close = true
	response, err := r.transport().RoundTrip(request)
	if err != nil {
		if stopped := failed(); stopped != nil {
			return nil, stopped
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, refuse("slack_timeout")
		}
		return nil, refuse("slack_unavailable")
	}
	// The request is ended before its body is closed. net/http reads up to 256 KiB more of a body that is
	// closed early on a connection it means to keep, and those bytes would be in no count.
	defer func() {
		cancel()
		_ = response.Body.Close()
	}()

	length, stated := response.Header["Content-Length"]
	if lowerASCII(headerValue(response.Header, "Content-Encoding", "identity")) != "identity" {
		return nil, refuse("slack_response_invalid")
	}
	if stated {
		value := strings.Join(length, ", ")
		if !digits(value, 10) {
			return nil, refuse("slack_response_invalid")
		}
		size, _ := strconv.Atoi(value)
		// A body stated to be larger than the read may still take is refused before any of it is read,
		// for a sign-in answer as for a page.
		switch {
		case kind == "auth" && size > maxAuthResponseBytes:
			return nil, refuse("slack_response_invalid")
		case size > b.room():
			return nil, refuse("slack_resource_limit")
		}
	}
	// A body that stops arriving is cut off after readTimeout, well before the deadline.
	pause := readTimeout
	if r.idle > 0 {
		pause = r.idle
	}
	idle := time.AfterFunc(pause, func() {
		stalled.Store(true)
		cancel()
	})
	defer idle.Stop()
	var body []byte
	chunk := make([]byte, 32<<10)
	for {
		idle.Reset(pause)
		// No read takes more than one byte past what is left, of the read's bytes or of a sign-in answer's
		// own. That one byte is the refusal.
		most := min(len(chunk), b.room()+1)
		if kind == "auth" {
			most = min(most, maxAuthResponseBytes-len(body)+1)
		}
		n, readErr := response.Body.Read(chunk[:most])
		if n > 0 {
			if err := b.addBytes(n, kind); err != nil {
				return nil, err
			}
			body = append(body, chunk[:n]...)
			if kind == "auth" && len(body) > maxAuthResponseBytes {
				return nil, refuse("slack_response_invalid")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if stopped := failed(); stopped != nil {
				return nil, stopped
			}
			var timeout net.Error
			if errors.As(readErr, &timeout) && timeout.Timeout() {
				return nil, refuse("slack_timeout")
			}
			return nil, refuse("slack_unavailable")
		}
	}
	return &answer{response.StatusCode, response.Header, body}, nil
}

// decode parses a response body as strict JSON and requires an object. A body that holds one of tokens,
// byte for byte, is refused unparsed: a token Slack sends back as text or as a cursor never becomes a record
// or part of the next request. So is one whose parsed text holds a token, or would once a card composes it
// or lowers its case. Nil tokens looks for none.
func decode(body []byte, tokens *echo.Tokens) (*strictjson.Object, error) {
	if tokens != nil && tokens.InBytes(body) {
		return nil, refuse("slack_response_invalid")
	}
	value, err := strictjson.LoadsText(string(body))
	if err != nil {
		return nil, refuse("slack_response_invalid")
	}
	document, isObject := value.(*strictjson.Object)
	if !isObject || document == nil || tokens != nil && tokens.InValue(document) {
		return nil, refuse("slack_response_invalid")
	}
	return document, nil
}

// providerErrors are Slack's error names a page request is refused with, by the reader's code for each.
// Every other name is slack_response_invalid.
var providerErrors = map[string]string{
	"channel_not_found": "slack_thread_unavailable", "thread_not_found": "slack_thread_unavailable",
	"not_in_channel": "slack_forbidden", "missing_scope": "slack_forbidden",
	"access_denied": "slack_forbidden", "no_permission": "slack_forbidden",
	"invalid_auth": "slack_reconnect_required", "token_revoked": "slack_reconnect_required",
	"account_inactive": "slack_reconnect_required", "not_authed": "slack_reconnect_required",
	"ratelimited": "slack_rate_limited", "rate_limited": "slack_rate_limited",
}

// providerResult accepts a page document whose ok is true and names the refusal of any other.
func providerResult(document *strictjson.Object) error {
	ok, _ := document.Get("ok")
	accepted, isBool := ok.(bool)
	if !isBool {
		return refuse("slack_response_invalid")
	}
	if accepted {
		return nil
	}
	name, _ := document.Get("error")
	if name == any("token_expired") {
		return errTokenExpired
	}
	if text, isText := name.(string); isText {
		if reason, known := providerErrors[text]; known {
			return refuse(reason)
		}
	}
	return refuse("slack_response_invalid")
}

// replies gets one page of the thread: the fixed endpoint, never a URL the source gave.
func (r *Reader) replies(ctx context.Context, b *budget, held *access, channel, parent, latest string, cursor *string, limit int) (*strictjson.Object, error) {
	query := "channel=" + url.QueryEscape(channel) + "&ts=" + url.QueryEscape(parent) +
		"&latest=" + url.QueryEscape(latest) + "&inclusive=true&limit=" + strconv.Itoa(limit)
	if cursor != nil {
		query += "&cursor=" + url.QueryEscape(*cursor)
	}
	got, err := r.send(ctx, b, "messages", func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, repliesURL+"?"+query, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+held.Token.Reveal())
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Accept-Encoding", "identity")
		return request, nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	switch {
	case got.status == http.StatusTooManyRequests:
		limited := &Error{Code: "slack_rate_limited"}
		if after := headerValue(got.header, "Retry-After", ""); digits(after, 4) {
			if seconds, _ := strconv.Atoi(after); seconds <= 3600 {
				limited.retry = seconds + 1
			}
		}
		return nil, limited
	case got.status >= 500:
		return nil, refuse("slack_unavailable")
	case got.status == http.StatusUnauthorized:
		// Only the exact token_expired answer leads to a refresh. Any other 401 is refused.
		document, err := decode(got.body, &b.tokens)
		if err != nil {
			return nil, err
		}
		if err := providerResult(document); err != nil {
			return nil, err
		}
		return nil, refuse("slack_response_invalid")
	case got.status == http.StatusForbidden:
		return nil, refuse("slack_forbidden")
	case got.status != http.StatusOK:
		return nil, refuse("slack_response_invalid")
	}
	document, err := decode(got.body, &b.tokens)
	if err != nil {
		return nil, err
	}
	if err := providerResult(document); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	return document, nil
}
