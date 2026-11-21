package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// TestNoCredentialIsPrinted makes reads in which Slack, the transport and the stores all hand tokens back,
// in accepted answers and in failures, and prints everything the package gives a caller: errors under every
// verb, reports, results, and the sign-in types themselves. No token is in any of it, and a refusal is its
// fixed code and nothing else.
func TestNoCredentialIsPrinted(t *testing.T) {
	tokens := []string{accessToken, refreshToken, newAccessToken, newRefresh}
	echo := accessToken + " " + refreshToken + " " + newAccessToken + " " + newRefresh
	var printed strings.Builder
	show := func(values ...any) {
		for _, value := range values {
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
				fmt.Fprintf(&printed, verb+"\n", value)
			}
		}
	}
	observe := func(name string, b *bench, read *ThreadRead, err error) {
		t.Helper()
		for unwrapped := err; unwrapped != nil; unwrapped = errors.Unwrap(unwrapped) {
			show(unwrapped)
		}
		if err != nil {
			if refused := code(err); refused == "" || err.Error() != refused || exitCodes[refused] == 0 {
				t.Errorf("%s: the error %q is not one fixed code", name, err)
			}
		}
		if read != nil {
			show(read, *read)
			printed.WriteString(dump(t, read.Snapshot))
			for _, record := range read.Records {
				printed.WriteString(dump(t, record))
			}
		}
		for _, report := range b.reports {
			show(report)
		}
		for _, request := range b.slack.sent {
			// A token travels in a header or a form body, never in a URL.
			printed.WriteString(request.url + "\n")
		}
	}

	scenarios := map[string]struct {
		replies []reply
		auth    []reply
		change  func(*bench)
		want    string
	}{
		"a refresh that is accepted": {replies: []reply{ok(expiredAnswer), page(msg(parentTS))},
			auth: []reply{ok(replacement), ok(identified)}, want: "ok"},
		"an error that echoes the tokens": {replies: []reply{ok(`{"ok":false,"error":"` + echo + `","detail":"Bearer ` + accessToken + `"}`)},
			want: "slack_response_invalid"},
		"a known error with the tokens beside it": {replies: []reply{ok(`{"ok":false,"error":"invalid_auth","token":"` + echo + `"}`)},
			want: "slack_reconnect_required"},
		"a body that is not JSON": {replies: []reply{ok(`Bearer ` + echo)}, want: "slack_response_invalid"},
		"a 429 body":              {replies: []reply{status(429, echo)}, want: "slack_rate_limited"},
		"a 500 body":              {replies: []reply{status(500, echo)}, want: "slack_unavailable"},
		"a 401 body":              {replies: []reply{status(401, echo)}, want: "slack_response_invalid"},
		"a transport failure":     {replies: []reply{{err: fmt.Errorf("Get %q: Authorization: Bearer %s", repliesURL, echo)}}, want: "slack_unavailable"},
		"a refresh for another user": {replies: []reply{ok(expiredAnswer)},
			auth: []reply{ok(strings.Replace(replacement, `{`, `{"user_id":"U87654321",`, 1))}, want: "slack_source_mismatch"},
		"a refresh that is not JSON": {replies: []reply{ok(expiredAnswer)}, auth: []reply{ok(replacement + echo)},
			want: "slack_response_invalid"},
		"a refresh Slack refuses": {replies: []reply{ok(expiredAnswer)},
			auth: []reply{ok(`{"ok":false,"error":"` + echo + `"}`)}, want: "slack_oauth_rejected"},
		"a refresh transport failure": {replies: []reply{ok(expiredAnswer)},
			auth: []reply{{err: fmt.Errorf("Post: refresh_token=%s", refreshToken)}}, want: "slack_unavailable"},
		"an identity for another team": {replies: []reply{ok(expiredAnswer)},
			auth: []reply{ok(replacement), ok(strings.Replace(strings.Replace(identified, "T12345678", "T87654321", 1),
				`{`, `{"token":"`+echo+`",`, 1))},
			want: "slack_source_mismatch"},
		"a sign-in store that fails": {change: func(b *bench) { b.saved.holdErr = fmt.Errorf("open: %s", echo) },
			want: "slack_storage_failed"},
		"a save that fails": {replies: []reply{ok(expiredAnswer)},
			change: func(b *bench) { b.saved.saveErr = fmt.Errorf("write %s", echo) }, want: "slack_storage_failed"},
		"a mapping store that fails": {change: func(b *bench) { b.reader.Mappings = failing{fmt.Errorf("read: %s", echo)} },
			want: "slack_storage_failed"},
		"a lock refusal with text around it": {
			change: func(b *bench) { b.saved.holdErr = fmt.Errorf("lock %s: %w", echo, refuse("slack_connection_busy")) },
			want:   "slack_connection_busy"},
		"a mapping refusal with text around it": {
			change: func(b *bench) {
				b.reader.Mappings = failing{fmt.Errorf("map %s: %w", echo, refuse("slack_mapping_invalid"))}
			},
			want: "slack_mapping_invalid"},
		"a refusal whose code is a token": {change: func(b *bench) { b.saved.holdErr = &Error{Code: echo} },
			want: "slack_reconnect_required"},
	}
	for name, given := range scenarios {
		b := newBench(t)
		if given.change != nil {
			given.change(b)
		}
		read, err := b.read(given.replies, given.auth...)
		if codeOf(err) != given.want {
			t.Errorf("%s: got %v, want %s", name, codeOf(err), given.want)
		}
		observe(name, b, read, err)
		show(b.saved.saved, b.saved.saves)
	}

	// The sign-in types, printed and logged as a caller might by mistake.
	document := signIn(newClock().wall, true, false)
	held := &access{document.AccessToken, document.Identity, document.GrantedScopes}
	show(document, *document, document.AccessToken, &document.RefreshToken, held, *held,
		[]Secret{accessToken}, map[string]Secret{"token": refreshToken}, struct{ Token Secret }{newAccessToken})
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("sign-in", "document", document, "token", document.AccessToken)
	slog.New(slog.NewTextHandler(&logged, nil)).Info("sign-in", "document", document, "token", document.AccessToken)
	printed.Write(logged.Bytes())
	if marshalled, err := json.Marshal(document); err == nil {
		t.Errorf("a sign-in was marshalled: %s", marshalled)
	} else {
		show(err)
	}

	text := printed.String()
	for _, token := range tokens {
		if strings.Contains(text, token) {
			at := strings.Index(text, token)
			t.Errorf("a token was printed: ...%s...", strings.ReplaceAll(text[max(0, at-120):at], token, "TOKEN"))
		}
		if strings.Contains(text, fmt.Sprintf("%x", token)) {
			t.Error("a token was printed in hex")
		}
	}
	if !strings.Contains(text, redacted) || printed.Len() < 10000 {
		t.Fatalf("the test printed %d bytes and no redaction", printed.Len())
	}
}
