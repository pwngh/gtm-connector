package slack

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A saved sign-in is read back as it was saved, in the format slack_auth.py writes, and anything that is not
// one is refused as invalid.
func TestASavedSignInReadsBackAsItWasSaved(t *testing.T) {
	now := newClock().wall
	for _, rotating := range []bool{false, true} {
		saved := signIn(now, rotating, false)
		raw, err := EncodeCredentials(saved)
		if err != nil {
			t.Fatal(err)
		}
		back, err := DecodeCredentials(raw)
		if err != nil || back.AccessToken != saved.AccessToken || back.RefreshToken != saved.RefreshToken ||
			back.Identity != saved.Identity || !back.AccessObtainedAt.Equal(saved.AccessObtainedAt) ||
			!back.AccessExpiresAt.Equal(saved.AccessExpiresAt) || back.State != saved.State {
			t.Errorf("rotating %v: read back %v", rotating, err)
		}
	}
	// As slack_auth.py writes it: sorted keys and json.dumps's separators.
	python := `{"access_expires_at": null, "access_obtained_at": "2026-11-18T12:00:00Z", "access_token": "` + accessToken +
		`", "client_id": "123456789.987654321", "credential_state": "ready", "granted_scopes": ["channels:history"], ` +
		`"identity": {"app_id": "A12345678", "enterprise_id": null, "team_id": "T12345678", "user_id": "U12345678"}, ` +
		`"last_verified_at": "2026-11-18T12:00:00Z", "provider": "slack", "redirect_uri": "http://localhost:8765/slack/callback", ` +
		`"refresh_obtained_at": null, "refresh_token": null, "requested_scopes": ["channels:history"], "schema_version": 1, ` +
		`"workspace_origin": "https://invented-workspace.slack.com"}`
	if read, err := DecodeCredentials([]byte(python)); err != nil || read.Identity != testIdentity {
		t.Errorf("the document slack_auth.py writes: %v", err)
	}
	for name, broken := range map[string]string{
		"a member left out":        strings.Replace(python, `"provider": "slack", `, "", 1),
		"a member added":           strings.Replace(python, `"provider": "slack"`, `"provider": "slack", "extra": 1`, 1),
		"a time not in its form":   strings.Replace(python, `"2026-11-18T12:00:00Z"`, `"2026-11-18T12:00:00.000000Z"`, 1),
		"a ready sign-in unproved": strings.Replace(python, `"last_verified_at": "2026-11-18T12:00:00Z"`, `"last_verified_at": null`, 1),
		"a redirect of another form": strings.Replace(python, "http://localhost:8765/slack/callback",
			"http://127.0.0.1:8765/slack/callback", 1),
		"not JSON": python[:40],
	} {
		if _, err := DecodeCredentials([]byte(broken)); codeOf(err) != "slack_credentials_invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := EncodeCredentials(&Credentials{}); codeOf(err) != "slack_storage_failed" {
		t.Errorf("an empty sign-in was written: %v", err)
	}
}

// The callback takes only the request Slack's redirect makes: a GET of the callback path, with the Host
// localhost:PORT, no body, this sign-in's state and a code, from the browser that loaded the start page. The
// start page is answered once, and sets the cookie that binds the callback to that browser, named for the
// sign-in's port and a random suffix: a second request of it is refused, and so is a callback without the
// cookie, with another value, or with the value under another sign-in's name. Each other request is
// answered and ignored.
func TestTheCallbackTakesOnlyItsOwnRequest(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg, err := NewConfiguration(testClient, fmt.Sprintf("http://localhost:%d/slack/callback", port), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// cookie is the Set-Cookie header of the last answer that had one.
	var cookie string
	send := func(request string) string {
		conn, err := net.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return err.Error()
		}
		defer conn.Close()
		fmt.Fprint(conn, request)
		reader := bufio.NewReader(conn)
		status, _ := reader.ReadString('\n')
		led := ""
		for {
			line, err := reader.ReadString('\n')
			if location, found := strings.CutPrefix(line, "Location: "); found {
				// A redirect gives where it leads.
				led = " " + strings.TrimSpace(location)
			}
			if set, found := strings.CutPrefix(line, "Set-Cookie: "); found {
				cookie = strings.TrimSpace(set)
			}
			if err != nil || line == "\r\n" {
				return strings.TrimSpace(status) + led
			}
		}
	}
	host := fmt.Sprintf("Host: localhost:%d\r\n", port)
	// bound is the Cookie header the browser sends the callback, once the start page set it.
	bound := ""
	stray := []struct{ request, want string }{
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "Cookie: NAME=guessed\r\n\r\n",
			"HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "Cookie: NAME=VALUE; NAME=VALUE\r\n\r\n",
			"HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "Cookie: " +
			fmt.Sprintf("gtm_slack_sign_in_%d_another", port) + "=VALUE\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"POST /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 405 Method Not Allowed"},
		{"GET /other?code=abc&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 404 Not Found"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "Content-Length: 3\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=other HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=a%zzc&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&code=abd&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE&extra=1 HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&&state=STATE HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 400 Bad Request"},
		{"GET /slack/callback?code=abc&state=STATE HTTP/1.1\r\n" + host + "Origin: https://invented-elsewhere.example\r\n\r\n",
			"HTTP/1.1 400 Bad Request"},
		{"GET /start/guessed HTTP/1.1\r\n" + host + "\r\n", "HTTP/1.1 404 Not Found"},
	}
	answers := make(chan string, len(stray)+2)
	var finished string
	open := func(_ context.Context, page string) error {
		// The browser is given the start page alone, which leads once to the authorization page.
		start, found := strings.CutPrefix(page, fmt.Sprintf("http://localhost:%d/start/", port))
		if !found || strings.Contains(page, "state") {
			return fmt.Errorf("the browser was given %q", page)
		}
		go func() {
			led := send("GET /start/" + start + " HTTP/1.1\r\n" + host + "Sec-Fetch-Site: none\r\n\r\n")
			state, found := strings.CutPrefix(led, "HTTP/1.1 302 Found https://slack.com/x?state=")
			// The cookie is named for this sign-in: its port and a random suffix.
			pair, attributes, _ := strings.Cut(cookie, ";")
			name, value, _ := strings.Cut(pair, "=")
			suffix, named := strings.CutPrefix(name, fmt.Sprintf("gtm_slack_sign_in_%d_", port))
			if attributes != " Path=/slack/callback; Max-Age=600; HttpOnly; SameSite=Lax" || len(value) < 43 ||
				!named || len(suffix) != 16 {
				answers <- fmt.Sprintf("the start page set the cookie %q", cookie)
			}
			bound = "Cookie: other=1; " + name + "=" + value + "\r\n"
			if again := send("GET /start/" + start + " HTTP/1.1\r\n" + host + "\r\n"); !found || again != "HTTP/1.1 404 Not Found" {
				answers <- fmt.Sprintf("the start page led to %q, then %q", led, again)
			}
			for i, s := range stray {
				request := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s.request, "STATE", state),
					"VALUE", value), "NAME", name)
				if i > 3 {
					// Each stray after the first four carries the cookie, so it is refused for what it is.
					request = strings.Replace(request, host, host+bound, 1)
				}
				if got := send(request); got != s.want {
					answers <- fmt.Sprintf("%q: %s", s.request, got)
				}
			}
			answers <- send("GET /slack/callback?code=invented-code&state=" + state + " HTTP/1.1\r\n" + host + bound + "\r\n")
		}()
		return nil
	}
	err = receiveConsent(context.Background(), cfg, func(state string) string { return "https://slack.com/x?state=" + state },
		open, func(_ context.Context, code string) error { finished = code; return nil })
	if err != nil || finished != "invented-code" {
		t.Fatalf("the callback: %v, finished with %q", err, finished)
	}
	if got := <-answers; got != "HTTP/1.1 200 OK" {
		t.Errorf("a stray request was taken or the callback answered: %s", got)
	}

	// A start page asked for first by a request from another site, or one that carries a sign-in cookie, is
	// refused and gone: the browser that comes after is answered 404, and no callback is taken.
	for _, first := range []string{"Sec-Fetch-Site: cross-site\r\n", "Sec-Fetch-Site: same-site\r\n",
		"Origin: http://localhost:3000\r\n", "Cookie: gtm_slack_sign_in_8765_earlier=x\r\n"} {
		answers := make(chan string, 2)
		open := func(_ context.Context, page string) error {
			start := strings.TrimPrefix(page, fmt.Sprintf("http://localhost:%d", port))
			go func() {
				answers <- send("GET " + start + " HTTP/1.1\r\n" + host + first + "\r\n")
				answers <- send("GET " + start + " HTTP/1.1\r\n" + host + "\r\n")
			}()
			return nil
		}
		saved := loginTime
		loginTime = 300 * time.Millisecond
		err := receiveConsent(context.Background(), cfg, func(state string) string { return "https://slack.com/x?state=" + state },
			open, func(context.Context, string) error { t.Error("a callback was taken"); return nil })
		loginTime = saved
		if refused, browser := <-answers, <-answers; codeOf(err) != "slack_login_timeout" ||
			refused != "HTTP/1.1 400 Bad Request" || browser != "HTTP/1.1 404 Not Found" {
			t.Errorf("a start page asked for first with %q: %q, then %q (%v)", first, refused, browser, err)
		}
	}

	// With no callback, the wait ends at its time.
	saved := loginTime
	loginTime = 200 * time.Millisecond
	t.Cleanup(func() { loginTime = saved })
	err = receiveConsent(context.Background(), cfg, func(string) string { return "https://slack.com/x" },
		func(context.Context, string) error { return nil }, func(context.Context, string) error { return nil })
	if codeOf(err) != "slack_login_timeout" {
		t.Errorf("a callback that never came: %v", err)
	}
}
