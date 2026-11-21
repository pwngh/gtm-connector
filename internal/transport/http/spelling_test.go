package httptransport_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// respelled is a base64url text whose last character differs only in the bits base64 leaves unused
// there, which a lenient decoder reads as the same bytes. A 32-byte HMAC is 43 characters, two bits
// spare.
func respelled(text string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, text[len(text)-1])
	return text[:len(text)-1] + string(alphabet[last^1])
}

// csrfOf is the CSRF token the server derives from a session's or a pre-session's cookie, which anyone
// holding the cookie can derive too.
func csrfOf(cookie string) string {
	mac := hmac.New(sha256.New, []byte(cookie))
	mac.Write([]byte("gtm-connector csrf v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// A sign-in through the provider runs once however its flow cookie is spelled: a cookie that differs from
// the used one only in bits base64 leaves unused is refused, before its code reaches the provider, also a
// provider that would take the code twice.
func TestAFlowSignsInOnceHoweverItsCookieIsSpelled(t *testing.T) {
	s := companySiteFor(t)
	s.provider.ReuseCodes = true
	p := s.person(t)
	callback := p.approve("/deals")
	flow := p.cookie("__Host-gtm-oidc")
	if sent := p.get(callback); sent.status != http.StatusSeeOther || sent.location != s.origin+"/deals" {
		t.Fatalf("the first use: %d %q", sent.status, sent.location)
	}
	before := s.provider.Counts().Token
	q := s.person(t)
	q.setCookie("__Host-gtm-oidc", respelled(flow))
	sent := q.get(callback)
	if !strings.Contains(sent.location, "sign_in_error=expired") {
		t.Fatalf("a respelled flow cookie answered %d to %q", sent.status, sent.location)
	}
	if name := signedInAs(q.session()); name != "" || s.provider.Counts().Token != before {
		t.Fatalf("a respelled flow signed in as %q, or reached the token endpoint", name)
	}
}

// The demo sign-in's pre-session signs in once however its cookie is spelled.
func TestAPreSessionSignsInOnceHoweverItsCookieIsSpelled(t *testing.T) {
	s := newSite(t, 0)
	b := s.browser(t)
	b.start()
	pre := b.preCookie()
	login := map[string]string{"login": "demo-reader", "password": readerPassword}
	if r := b.write(http.MethodPost, "/api/v1/session", login); r.status != 200 {
		t.Fatalf("the first sign-in: %d %s", r.status, r.raw)
	}
	c := s.browser(t)
	c.setNamed("__Host-gtm-pre", respelled(pre))
	c.csrf = csrfOf(respelled(pre))
	if r := c.write(http.MethodPost, "/api/v1/session", login); r.status != 403 || r.code() != "csrf_invalid" {
		t.Fatalf("a respelled pre-session signed in again: %d %s", r.status, r.raw)
	}
	if n := liveSessionRows(t, s); n != 1 {
		t.Fatalf("%d live sessions", n)
	}
}
