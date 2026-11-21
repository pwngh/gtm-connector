package slack

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

const (
	repliesPath  = "/api/conversations.replies"
	refreshPath  = "/api/oauth.v2.access"
	identityPath = "/api/auth.test"
)

// replace saves another sign-in, as another command holding the connection would.
func (b *bench) replace(change func(*Credentials)) {
	document := signIn(b.clock.wall, true, false)
	change(document)
	b.saved.saved = document
}

func (b *bench) state() string { return b.saved.saved.State }

// offline requires that the saved sign-in is refused without a request, as one that needs a login is.
func (b *bench) offline() {
	b.t.Helper()
	before := len(b.slack.sent)
	b.refuses("slack_reconnect_required", nil)
	if len(b.slack.sent) != before || b.last().APICalls != 0 {
		b.t.Fatalf("a sign-in that needs a login made a request: %+v", b.last())
	}
}

func TestOneRefreshAfterSlackSaysTheTokenExpired(t *testing.T) {
	for name, expired := range map[string]reply{"in the document": ok(expiredAnswer), "with a 401": status(401, expiredAnswer)} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			read := b.reads([]reply{expired, page(msg(parentTS))}, ok(replacement), ok(identified))
			if want := []string{repliesPath, refreshPath, identityPath, repliesPath}; !slices.Equal(b.slack.paths(), want) {
				t.Fatalf("requests %v", b.slack.paths())
			}
			for name, want := range map[string]int{"http_attempts": 4, "auth_attempts": 2, "message_attempts": 2,
				"refresh_attempts": 1, "token_retries": 1, "pages": 1, "rate_retries": 0} {
				if got := count(t, read, name); got != want {
					t.Errorf("%s is %d, want %d", name, got, want)
				}
			}
			refresh, identity := b.slack.sent[1], b.slack.sent[2]
			if refresh.method != "POST" || refresh.url != tokenURL ||
				refresh.body != "grant_type=refresh_token&client_id=123456789.987654321&refresh_token="+refreshToken ||
				refresh.header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				refresh.header.Get("Authorization") != "" || refresh.header.Get("Accept-Encoding") != "identity" {
				t.Errorf("refresh request %+v", refresh)
			}
			if identity.method != "POST" || identity.url != identityURL || identity.body != "" ||
				identity.header.Get("Authorization") != "Bearer "+newAccessToken {
				t.Errorf("identity request %+v", identity)
			}
			if got := b.lastSent().header.Get("Authorization"); got != "Bearer "+newAccessToken {
				t.Errorf("the retry was sent with %v", shown(b.lastSent().header))
			}
			// The sign-in is saved as needing a login before the refresh token is spent, then as the
			// unverified replacement, then as ready.
			if want := []string{pending, unchecked, ready}; !slices.Equal(b.saved.states(), want) {
				t.Errorf("saved states %v", b.saved.states())
			}
			saved := b.saved.saved
			if saved.AccessToken != NewSecret(newAccessToken) || saved.RefreshToken != NewSecret(newRefresh) ||
				saved.WorkspaceOrigin != testOrigin || !saved.LastVerifiedAt.Equal(b.clock.wall) ||
				!saved.AccessExpiresAt.Equal(b.clock.wall.Add(time.Hour)) || !saved.valid() {
				t.Errorf("saved %+v", saved)
			}
			report := b.last()
			if report.ResponseBytes != report.AuthResponseBytes+report.MessageResponseBytes || report.AuthCalls != 2 {
				t.Errorf("report %+v", report)
			}
		})
	}
}

func TestATokenAtItsExpiryIsRefreshedBeforeUse(t *testing.T) {
	b := newBench(t)
	b.clock.advance(3569 * time.Second)
	b.reads([]reply{page(msg(parentTS))})
	if len(b.saved.saves) != 0 {
		t.Fatal("a token 31 seconds from expiry was refreshed")
	}
	b.clock.advance(time.Second)
	b.reads([]reply{page(msg(parentTS))}, ok(replacement), ok(identified))
	if want := []string{repliesPath, refreshPath, identityPath, repliesPath}; !slices.Equal(b.slack.paths(), want) {
		t.Fatalf("requests %v", b.slack.paths())
	}
	// A refresh token is usable for 30 days. Past them, and with a clock behind the sign-in, only a login helps.
	b.clock.advance(31 * 24 * time.Hour)
	b.offline()
	b = newBench(t)
	b.clock.advance(-time.Second)
	b.offline()
}

func TestATokenAnotherCommandRefreshedIsUsedAsItIs(t *testing.T) {
	b := newBench(t)
	b.slack.before = func(request sent) {
		if request.path == repliesPath && len(b.slack.sent) == 1 {
			b.replace(func(c *Credentials) { c.AccessToken, c.RefreshToken = NewSecret(newAccessToken), NewSecret(newRefresh) })
		}
	}
	b.refuses("slack_reconnect_required", []reply{ok(expiredAnswer), ok(expiredAnswer)})
	if b.last().RefreshAttempts != 0 || len(b.slack.sent) != 2 ||
		b.lastSent().header.Get("Authorization") != "Bearer "+newAccessToken {
		t.Fatalf("report %+v", b.last())
	}
	// The second token Slack refused is saved as needing a login.
	if b.state() != pending {
		t.Fatalf("saved state %s", b.state())
	}
	b.slack.before = nil
	b.offline()
}

func TestOnlyASignInThatReturnedAPageAddsItsScopes(t *testing.T) {
	both := []string{"channels:history", "groups:history"}
	narrowOn := func(b *bench, attempt int) {
		b.slack.before = func(request sent) {
			replies := 0
			for _, seen := range b.slack.sent {
				if seen.path == repliesPath {
					replies++
				}
			}
			if request.path == repliesPath && replies == attempt {
				b.replace(func(c *Credentials) { c.AccessToken, c.RefreshToken = NewSecret(newAccessToken), NewSecret(newRefresh) })
			}
		}
	}
	// A broad sign-in that expired before returning a page adds nothing.
	b := newBench(t)
	b.saved.saved.RequestedScopes, b.saved.saved.GrantedScopes = both, both
	narrowOn(b, 1)
	read := b.reads([]reply{ok(expiredAnswer), page(msg(parentTS))})
	if !slices.Equal(read.Scopes, []string{"channels:history"}) {
		t.Fatalf("scopes %v", read.Scopes)
	}
	// One that returned a page keeps its scopes after a narrower replacement.
	b = newBench(t)
	b.saved.saved.RequestedScopes, b.saved.saved.GrantedScopes = both, both
	narrowOn(b, 2)
	read = b.reads([]reply{page(msg(parentTS), cursor("next")), ok(expiredAnswer), page(msg(replyTS))})
	if !slices.Equal(read.Scopes, both) || b.lastSent().header.Get("Authorization") != "Bearer "+newAccessToken {
		t.Fatalf("scopes %v", read.Scopes)
	}
}

func TestExpiryThatARefreshCannotMend(t *testing.T) {
	t.Run("a token that does not rotate", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved = signIn(b.clock.wall, false, false)
		b.refuses("slack_reconnect_required", []reply{ok(expiredAnswer)})
		if len(b.slack.sent) != 1 || b.state() != pending {
			t.Fatalf("%d requests, saved %s", len(b.slack.sent), b.state())
		}
		b.offline()
	})
	t.Run("a second expiry in one read", func(t *testing.T) {
		b := newBench(t)
		b.refuses("slack_reconnect_required", []reply{ok(expiredAnswer), ok(expiredAnswer)}, ok(replacement), ok(identified))
		if len(b.slack.sent) != 4 || b.lastSent().header.Get("Authorization") != "Bearer "+newAccessToken || b.state() != pending {
			t.Fatalf("%d requests, saved %s", len(b.slack.sent), b.state())
		}
		b.offline()
	})
	t.Run("an expiry after this read already refreshed", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved = signIn(b.clock.wall, true, true)
		b.refuses("slack_reconnect_required", []reply{ok(expiredAnswer)}, ok(replacement), ok(identified))
		if want := []string{refreshPath, identityPath, repliesPath}; !slices.Equal(b.slack.paths(), want) {
			t.Fatalf("requests %v", b.slack.paths())
		}
		if b.last().RefreshAttempts != 1 || b.state() != pending {
			t.Fatalf("report %+v, saved %s", b.last(), b.state())
		}
	})
	for _, name := range []string{"invalid_auth", "token_revoked", "account_inactive", "not_authed"} {
		t.Run("Slack answers "+name, func(t *testing.T) {
			b := newBench(t)
			b.refuses("slack_reconnect_required", []reply{ok(`{"ok":false,"error":"` + name + `"}`)})
			if len(b.slack.sent) != 1 || b.state() != pending {
				t.Fatalf("%d requests, saved %s", len(b.slack.sent), b.state())
			}
			b.offline()
		})
	}
	t.Run("an unverified token past its expiry", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved = signIn(b.clock.wall, true, true)
		b.saved.saved.State = unchecked
		b.offline()
		if len(b.saved.saves) != 0 {
			t.Fatal("the sign-in was saved")
		}
	})
}

func TestARefusedTokenNeverReplacesANewerSignIn(t *testing.T) {
	for _, name := range []string{"token_expired", "invalid_auth", "token_revoked", "account_inactive", "not_authed"} {
		for _, kind := range []string{"a new token", "a new identity with the same token"} {
			t.Run(name+" and "+kind, func(t *testing.T) {
				b := newBench(t)
				replaceOn, attempts := 1, 0
				var auth []reply
				replies := []reply{ok(`{"ok":false,"error":"` + name + `"}`)}
				if name == "token_expired" {
					replaceOn, auth = 2, []reply{ok(replacement), ok(identified)}
					replies = append(replies, replies[0])
				}
				b.slack.before = func(request sent) {
					if request.path != repliesPath {
						return
					}
					if attempts++; attempts != replaceOn {
						return
					}
					b.replace(func(c *Credentials) {
						if kind == "a new token" {
							c.AccessToken, c.RefreshToken = NewSecret("invented-newer-access-token"), NewSecret("invented-newer-refresh-token")
							return
						}
						// Matching token text alone does not identify a sign-in.
						c.Identity.TeamID = "T98765432"
						if name == "token_expired" {
							c.AccessToken = NewSecret(newAccessToken)
						}
					})
					b.saved.saves = nil
				}
				b.refuses("slack_reconnect_required", replies, auth...)
				if len(b.saved.saves) != 0 || b.state() != ready {
					t.Fatalf("the newer sign-in was saved over: %v", b.saved.states())
				}
				b.slack.before = nil
				before := len(b.slack.sent)
				if kind == "a new token" {
					b.reads([]reply{page(msg(parentTS))})
					if len(b.slack.sent) != before+1 || b.lastSent().header.Get("Authorization") != "Bearer invented-newer-access-token" {
						t.Fatalf("the newer token was not used")
					}
				} else {
					b.refuses("slack_source_mismatch", nil)
					if len(b.slack.sent) != before {
						t.Fatal("another identity's token was sent")
					}
				}
			})
		}
	}
}

func TestARefreshNeedsTwoAttemptsLeft(t *testing.T) {
	for _, attempts := range []int{9, 10} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			b := newBench(t)
			replies := []reply{page(msg(parentTS), cursor("cursor-1"))}
			for n := 2; n < attempts; n++ {
				replies = append(replies, page(cursor(fmt.Sprintf("cursor-%d", n))))
			}
			replies = append(replies, ok(expiredAnswer))
			b.refuses("slack_resource_limit", replies, ok(replacement), ok(identified))
			// The refresh token is not spent, so a new read can use it.
			if report := b.last(); len(b.slack.sent) != attempts || report.MessageCalls != attempts ||
				report.AuthCalls != 0 || len(b.saved.saves) != 0 {
				t.Fatalf("report %+v", report)
			}
			start := len(b.slack.sent)
			read := b.reads([]reply{ok(expiredAnswer), page(msg(parentTS))}, ok(replacement), ok(identified))
			if len(b.slack.sent)-start != 4 || count(t, read, "refresh_attempts") != 1 || count(t, read, "token_retries") != 1 ||
				b.lastSent().header.Get("Authorization") != "Bearer "+newAccessToken {
				t.Fatalf("%d requests", len(b.slack.sent)-start)
			}
		})
	}
}

func TestSignInRequestsDrawOnTheSameBudget(t *testing.T) {
	t.Run("response bytes", func(t *testing.T) {
		// The pages leave about 18,000 bytes, and the refresh answer is about 20,000.
		padded := strings.Replace(replacement, `{`, `{"ignored":"`+strings.Repeat("x", 20000)+`",`, 1)
		pages := []reply{page(msg(parentTS), cursor("next"), `"ignored":"`+strings.Repeat("x", 1030000)+`"`), ok(expiredAnswer)}
		for name, given := range map[string]struct {
			answer reply
			auth   func(Report) int // the sign-in bytes the read may have taken
		}{
			// An answer that states its length is refused before any of it is read.
			"stated": {ok(padded), func(Report) int { return 0 }},
			// One that states none is read to one byte past what the read had left.
			"not stated": {reply{status: 200, stream: stream(padded)},
				func(report Report) int { return maxResponseBytes + 1 - report.MessageResponseBytes }},
		} {
			b := newBench(t)
			b.refuses("slack_resource_limit", pages, given.answer)
			report := b.last()
			if len(b.slack.sent) != 3 || report.AuthResponseBytes != given.auth(report) ||
				report.ResponseBytes != report.MessageResponseBytes+report.AuthResponseBytes {
				t.Fatalf("%s: report %+v", name, report)
			}
			// The refresh token may be spent, so the sign-in stays saved as needing a login.
			if b.state() != pending {
				t.Fatalf("%s: saved %s", name, b.state())
			}
		}
	})
	t.Run("the deadline", func(t *testing.T) {
		b := newBench(t)
		b.slack.before = func(request sent) {
			switch request.path {
			case repliesPath:
				b.clock.advance(15 * time.Second)
			case identityPath:
				b.clock.advance(6 * time.Second)
			}
		}
		b.refuses("slack_timeout", []reply{ok(expiredAnswer)}, ok(replacement), ok(identified))
		if len(b.slack.sent) != 3 || b.last().Phase != "credential_binding" {
			t.Fatalf("report %+v", b.last())
		}
	})
	t.Run("an answer of its own past 64 KiB", func(t *testing.T) {
		for name, large := range map[string]reply{
			"stated":     ok(`{"ok":true,"x":"` + strings.Repeat("x", maxAuthResponseBytes) + `"}`),
			"not stated": {status: 200, stream: stream(`{"ok":true,"x":"` + strings.Repeat("x", maxAuthResponseBytes) + `"}`)},
		} {
			b := newBench(t)
			b.saved.saved.State = unchecked
			b.refuses("slack_response_invalid", nil, large)
			if len(b.slack.sent) != 1 || b.last().AuthCalls != 1 {
				t.Fatalf("%s: report %+v", name, b.last())
			}
		}
	})
	t.Run("a 429 is counted and not retried", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved.State = unchecked
		b.refuses("slack_rate_limited", nil, status(429, "private auth error", "Retry-After", "0"))
		if report := b.last(); report.APICalls != 1 || report.AuthResponseBytes != 18 || report.Phase != "credential_binding" {
			t.Fatalf("report %+v", report)
		}
	})
}

func TestASignInForAnotherIdentityIsRefused(t *testing.T) {
	t.Run("replaced while the read ran", func(t *testing.T) {
		b := newBench(t)
		b.slack.before = func(request sent) {
			if request.path == repliesPath {
				b.replace(func(c *Credentials) { c.Identity.UserID, c.AccessToken = "U98765432", NewSecret(newAccessToken) })
				b.saved.saves = nil
			}
		}
		b.refuses("slack_source_mismatch", []reply{ok(expiredAnswer)})
		if len(b.slack.sent) != 1 || b.last().CredentialBindingValidated || len(b.saved.saves) != 0 {
			t.Fatalf("report %+v", b.last())
		}
	})
	t.Run("the refreshed token is for another team", func(t *testing.T) {
		b := newBench(t)
		b.refuses("slack_source_mismatch", []reply{ok(expiredAnswer)}, ok(replacement),
			ok(strings.Replace(identified, "T12345678", "T98765432", 1)))
		report := b.last()
		if report.CredentialBindingValidated || report.Phase != "credential_binding" || report.MessageCalls != 1 || b.state() != pending {
			t.Fatalf("report %+v, saved %s", report, b.state())
		}
	})
	for _, state := range []string{"ready and near expiry", "unverified", "ready"} {
		t.Run("saved before the read, "+state, func(t *testing.T) {
			b := newBench(t)
			b.replace(func(c *Credentials) {
				c.Identity.TeamID, c.AccessToken = "T98765432", NewSecret("invented-other-workspace-access-token")
				switch state {
				case "ready and near expiry":
					c.AccessExpiresAt = b.clock.wall.Add(20 * time.Second)
				case "unverified":
					c.State = unchecked
				}
			})
			// Nothing is sent, verified or saved for a sign-in the folder is not connected to.
			b.refuses("slack_source_mismatch", nil, ok(replacement), ok(identified))
			if len(b.slack.sent) != 0 || b.last().APICalls != 0 || len(b.saved.saves) != 0 || b.last().CredentialBindingValidated {
				t.Fatalf("report %+v", b.last())
			}
		})
	}
}

func TestAnUnverifiedTokenIsVerifiedFirst(t *testing.T) {
	b := newBench(t)
	b.saved.saved.State = unchecked
	b.clock.advance(time.Minute)
	b.reads([]reply{page(msg(parentTS))}, ok(`{"ok":true,"team_id":"T12345678","user_id":"U12345678","enterprise_id":null}`))
	if want := []string{identityPath, repliesPath}; !slices.Equal(b.slack.paths(), want) {
		t.Fatalf("requests %v", b.slack.paths())
	}
	// Slack stated no workspace URL, so none is kept.
	if saved := b.saved.saved; saved.State != ready || !saved.LastVerifiedAt.Equal(b.clock.wall) || saved.WorkspaceOrigin != "" {
		t.Fatalf("saved %+v", saved)
	}
	for name, given := range map[string]struct {
		answer      reply
		want, saved string
	}{
		"a server error":             {status(500, "x"), "slack_unavailable", unchecked},
		"a 401":                      {status(401, identified), "slack_reconnect_required", pending},
		"a 403":                      {status(403, identified), "slack_reconnect_required", pending},
		"a redirect":                 {status(302, identified), "slack_response_invalid", unchecked},
		"a revoked token":            {ok(`{"ok":false,"error":"token_revoked"}`), "slack_reconnect_required", pending},
		"an expired token":           {ok(`{"ok":false,"error":"token_expired"}`), "slack_reconnect_required", pending},
		"a rate limit":               {ok(`{"ok":false,"error":"ratelimited"}`), "slack_rate_limited", unchecked},
		"a token type refusal":       {ok(`{"ok":false,"error":"not_allowed_token_type"}`), "slack_token_type_unsupported", pending},
		"another refusal":            {ok(`{"ok":false,"error":"private_sentinel"}`), "slack_oauth_rejected", unchecked},
		"a bot":                      {ok(strings.Replace(identified, `{`, `{"bot_id":"B12345678",`, 1)), "slack_token_type_unsupported", pending},
		"an organization install":    {ok(strings.Replace(identified, `{`, `{"is_enterprise_install":true,`, 1)), "slack_token_type_unsupported", pending},
		"an install flag as text":    {ok(strings.Replace(identified, `{`, `{"is_enterprise_install":"no",`, 1)), "slack_token_type_unsupported", pending},
		"another user":               {ok(strings.Replace(identified, "U12345678", "U98765432", 1)), "slack_source_mismatch", pending},
		"an enterprise it is not in": {ok(strings.Replace(identified, `{`, `{"enterprise_id":"E12345678",`, 1)), "slack_source_mismatch", pending},
		"a short team ID":            {ok(strings.Replace(identified, "T12345678", "T1234567", 1)), "slack_response_invalid", unchecked},
		"no user ID":                 {ok(`{"ok":true,"team_id":"T12345678"}`), "slack_response_invalid", unchecked},
		"text that is not JSON":      {ok(`private`), "slack_response_invalid", unchecked},
		"ok that is not a boolean":   {ok(`{"ok":"true"}`), "slack_response_invalid", unchecked},
		"an encoded body":            {status(200, identified, "Content-Encoding", "br"), "slack_response_invalid", unchecked},
		"a transport failure":        {reply{err: errors.New("connection reset")}, "slack_unavailable", unchecked},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.saved.saved.State = unchecked
			b.refuses(given.want, nil, given.answer)
			if len(b.slack.sent) != 1 || b.state() != given.saved || b.last().Phase != "credential_binding" {
				t.Fatalf("%d requests, saved %s", len(b.slack.sent), b.state())
			}
		})
	}
	t.Run("an enterprise sign-in", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved.State, b.saved.saved.Identity.EnterpriseID = unchecked, "E12345678"
		b.reader.Binding = binding(t, b.saved.saved.Identity)
		b.reader.Mappings.(mappings)[accountID].Set("slack_binding", b.reader.Binding)
		b.reads([]reply{page(msg(parentTS))}, ok(strings.Replace(identified, `{`, `{"enterprise_id":"E12345678",`, 1)))
		b.saved.saved.State = unchecked
		b.refuses("slack_source_mismatch", nil, ok(strings.Replace(identified, `{`, `{"enterprise_id":null,`, 1)))
	})
	t.Run("a token that expires while it is verified", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved.State = unchecked
		b.clock.advance(3590 * time.Second)
		b.slack.before = func(sent) { b.clock.advance(10 * time.Second) }
		b.refuses("slack_reconnect_required", nil, ok(identified))
	})
}

func TestStoreFailures(t *testing.T) {
	invalid := map[string]func(*Credentials){
		"a ready sign-in never verified":     func(c *Credentials) { c.LastVerifiedAt = time.Time{} },
		"verified before it was obtained":    func(c *Credentials) { c.LastVerifiedAt = c.AccessObtainedAt.Add(-time.Second) },
		"an unknown state":                   func(c *Credentials) { c.State = "fresh" },
		"a token with a space":               func(c *Credentials) { c.AccessToken = NewSecret("invented token") },
		"no token":                           func(c *Credentials) { c.AccessToken = Secret{} },
		"a refresh token with no expiry":     func(c *Credentials) { c.AccessExpiresAt = time.Time{} },
		"an expiry with no refresh token":    func(c *Credentials) { c.RefreshToken = Secret{} },
		"a lifetime past a day":              func(c *Credentials) { c.AccessExpiresAt = c.AccessObtainedAt.Add(25 * time.Hour) },
		"a refresh obtained at another time": func(c *Credentials) { c.RefreshObtainedAt = c.RefreshObtainedAt.Add(time.Second) },
		"a scope outside the two": func(c *Credentials) {
			c.RequestedScopes, c.GrantedScopes = []string{"chat:write"}, []string{"chat:write"}
		},
		"scopes out of order": func(c *Credentials) {
			c.RequestedScopes = []string{"groups:history", "channels:history"}
			c.GrantedScopes = c.RequestedScopes
		},
		"granted scopes that differ":          func(c *Credentials) { c.GrantedScopes = []string{"groups:history"} },
		"no scopes":                           func(c *Credentials) { c.RequestedScopes, c.GrantedScopes = nil, nil },
		"a client ID that is not two numbers": func(c *Credentials) { c.ClientID = "client" },
		"a short team ID":                     func(c *Credentials) { c.Identity.TeamID = "T1" },
		"an origin outside slack.com":         func(c *Credentials) { c.WorkspaceOrigin = "https://invented.example" },
		"an origin with a path":               func(c *Credentials) { c.WorkspaceOrigin = testOrigin + "/" },
		"no obtained time":                    func(c *Credentials) { c.AccessObtainedAt = time.Time{} },
	}
	for name, change := range invalid {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			change(b.saved.saved)
			b.refuses("slack_credentials_invalid", nil)
			if len(b.slack.sent) != 0 || len(b.saved.saves) != 0 {
				t.Fatal("an invalid sign-in was used")
			}
		})
	}
	t.Run("no sign-in", func(t *testing.T) {
		b := newBench(t)
		b.saved.saved = nil
		b.refuses("slack_not_connected", nil)
	})
	t.Run("a lock that is taken", func(t *testing.T) {
		b := newBench(t)
		b.saved.holdErr = refuse("slack_connection_busy")
		b.refuses("slack_connection_busy", nil)
	})
	t.Run("a store that fails", func(t *testing.T) {
		b := newBench(t)
		b.saved.holdErr = fmt.Errorf("open: %s", accessToken)
		b.refuses("slack_storage_failed", nil)
	})
	t.Run("a refusal only the sign-in flow gives", func(t *testing.T) {
		b := newBench(t)
		b.saved.holdErr = &Error{Code: "slack_login_denied"}
		b.refuses("slack_reconnect_required", nil)
	})
	t.Run("a save that fails before the refresh", func(t *testing.T) {
		b := newBench(t)
		b.saved.saveErr = errors.New("disk full")
		b.refuses("slack_storage_failed", []reply{ok(expiredAnswer)}, ok(replacement), ok(identified))
		// The refresh token is never sent unless the sign-in was first saved as needing a login.
		if want := []string{repliesPath}; !slices.Equal(b.slack.paths(), want) {
			t.Fatalf("requests %v", b.slack.paths())
		}
	})
	t.Run("a save that fails when a token is rejected", func(t *testing.T) {
		b := newBench(t)
		b.saved.saveErr = errors.New("disk full")
		b.refuses("slack_storage_failed", []reply{ok(`{"ok":false,"error":"invalid_auth"}`)})
	})
}

// A token that an answer could hold by chance, or that holds a space, a control character or anything
// outside ASCII, is no credential: saved, it is an invalid sign-in that sends nothing, and in a refresh answer it is refused.
func TestATokenAnAnswerCouldHoldByChanceIsNoCredential(t *testing.T) {
	twenty := strings.Repeat("x", 20)
	for name, token := range map[string]string{
		"one letter, as in the member ok": "o",
		"nineteen characters":             twenty[1:],
		"a space":                         twenty + " x",
		"a control character":             twenty + "\x7f",
		"a letter outside ASCII":          twenty + "é",
	} {
		for field, change := range map[string]func(*Credentials){
			"access":  func(c *Credentials) { c.AccessToken = NewSecret(token) },
			"refresh": func(c *Credentials) { c.RefreshToken = NewSecret(token) },
		} {
			b := newBench(t)
			change(b.saved.saved)
			read, err := b.read([]reply{page(msg(parentTS))})
			if read != nil || codeOf(err) != "slack_credentials_invalid" || len(b.slack.sent) != 0 {
				t.Errorf("%s as the saved %s token: the code %s after %d requests", name, field, codeOf(err), len(b.slack.sent))
			}
		}
		for _, replaced := range []string{newAccessToken, newRefresh} {
			b := newBench(t)
			answer := strings.Replace(replacement, `"`+replaced+`"`, strictjson.Quote(token), 1)
			read, err := b.read([]reply{ok(expiredAnswer), page(msg(parentTS))}, ok(answer), ok(identified))
			if read != nil || codeOf(err) != "slack_response_invalid" || b.state() != pending ||
				b.saved.saved.AccessToken != NewSecret(accessToken) {
				t.Errorf("%s in a refresh answer: the code %s, saved %s", name, codeOf(err), b.state())
			}
		}
	}
	// Twenty characters of letters, digits, dots, hyphens and underscores are a token.
	b := newBench(t)
	b.saved.saved.AccessToken, b.saved.saved.RefreshToken = NewSecret("Abc123._-"+twenty), NewSecret(twenty)
	b.reads([]reply{page(msg(parentTS))})
}

func TestRefreshAnswers(t *testing.T) {
	flat := replacement
	nested := `{"ok":true,"authed_user":` + strings.Replace(replacement, `"ok":true,`, "", 1) + `}`
	with := func(document, members string) string { return strings.Replace(document, `{`, `{`+members+`,`, 1) }
	without := func(document, member string) string { return strings.Replace(document, member+",", "", 1) }
	for name, given := range map[string]struct{ answer, want string }{
		"flat":                               {flat, "ok"},
		"nested":                             {nested, "ok"},
		"every identity stated and the same": {with(flat, `"app_id":"A12345678","team":{"id":"T12345678"},"team_id":"T12345678","user_id":"U12345678","id":"U12345678","enterprise":null,"enterprise_id":null`), "ok"},
		"another app":                        {with(flat, `"app_id":"A87654321"`), "slack_source_mismatch"},
		"another team":                       {with(flat, `"team":{"id":"T87654321"}`), "slack_source_mismatch"},
		"another team ID":                    {with(flat, `"team_id":"T87654321"`), "slack_source_mismatch"},
		"another user":                       {with(flat, `"user_id":"U87654321"`), "slack_source_mismatch"},
		"an enterprise":                      {with(flat, `"enterprise":{"id":"E12345678"}`), "slack_source_mismatch"},
		"an enterprise ID":                   {with(flat, `"enterprise_id":"E12345678"`), "slack_source_mismatch"},
		"two user IDs that disagree":         {with(strings.Replace(nested, `"authed_user":{`, `"authed_user":{"id":"U87654321",`, 1), `"user_id":"U12345678"`), "slack_source_mismatch"},
		"a team with no ID":                  {with(flat, `"team":{}`), "slack_response_invalid"},
		"a team that is text":                {with(flat, `"team":"T12345678"`), "slack_response_invalid"},
		"a short user ID":                    {with(flat, `"user_id":"U1"`), "slack_response_invalid"},
		"an enterprise with no ID":           {with(flat, `"enterprise":{}`), "slack_response_invalid"},
		"a lifetime that is a boolean":       {strings.Replace(flat, "3600", "true", 1), "slack_response_invalid"},
		"a lifetime with a fraction":         {strings.Replace(flat, "3600", "3600.0", 1), "slack_response_invalid"},
		"a lifetime past a day":              {strings.Replace(flat, "3600", "86401", 1), "slack_response_invalid"},
		"a lifetime of zero":                 {strings.Replace(flat, "3600", "0", 1), "slack_response_invalid"},
		"a null refresh token":               {strings.Replace(flat, `"invented-replacement-refresh-token"`, "null", 1), "slack_response_invalid"},
		"no refresh token and no lifetime":   {`{"ok":true,"token_type":"user","scope":"channels:history","access_token":"invented-replacement-access-token"}`, "slack_response_invalid"},
		"an access token with a space":       {strings.Replace(flat, "invented-replacement-access-token", "invented token", 1), "slack_response_invalid"},
		"a nested grant that is a list":      {`{"ok":true,"authed_user":[]}`, "slack_response_invalid"},
		"a bearer token":                     {strings.Replace(flat, `"token_type":"user"`, `"token_type":"bearer"`, 1), "slack_token_type_unsupported"},
		"no token type":                      {without(flat, `"token_type":"user"`), "slack_token_type_unsupported"},
		"a bot user":                         {with(flat, `"bot_user_id":"U00000000"`), "slack_token_type_unsupported"},
		"a nested bot":                       {strings.Replace(nested, `"authed_user":{`, `"authed_user":{"bot_id":"B00000000",`, 1), "slack_token_type_unsupported"},
		"tokens at both levels":              {with(nested, `"access_token":"invented-outer-token"`), "slack_token_type_unsupported"},
		"more scopes than requested":         {strings.Replace(flat, `"channels:history"`, `"channels:history,groups:history"`, 1), "slack_forbidden"},
		"another scope":                      {strings.Replace(flat, `"channels:history"`, `"chat:write"`, 1), "slack_forbidden"},
		"no scope":                           {without(flat, `"scope":"channels:history"`), "slack_forbidden"},
		"Slack refuses the refresh token":    {`{"ok":false,"error":"invalid_refresh_token"}`, "slack_reconnect_required"},
		"Slack refuses the client":           {`{"ok":false,"error":"invalid_client_id"}`, "slack_oauth_rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			read, err := b.read([]reply{ok(expiredAnswer), page(msg(parentTS))}, ok(given.answer), ok(identified))
			if codeOf(err) != given.want {
				t.Fatalf("got %v, want %s", codeOf(err), given.want)
			}
			if given.want == "ok" {
				if read == nil || b.state() != ready || b.saved.saved.AccessToken != NewSecret(newAccessToken) {
					t.Fatalf("saved %s", b.state())
				}
				return
			}
			// A refresh that was sent and not accepted leaves the sign-in needing a login, and the
			// replacement is never saved or used.
			if want := []string{repliesPath, refreshPath}; !slices.Equal(b.slack.paths(), want) {
				t.Fatalf("requests %v", b.slack.paths())
			}
			if b.state() != pending || b.saved.saved.AccessToken != NewSecret(accessToken) {
				t.Fatalf("saved %s", b.state())
			}
		})
	}
}

func TestWorkspaceOrigin(t *testing.T) {
	// Each value with what slack_auth._origin of the Python package gives for it.
	for value, want := range map[string]string{
		"https://invented-workspace.slack.com/":              "https://invented-workspace.slack.com",
		"https://invented-workspace.slack.com":               "https://invented-workspace.slack.com",
		"HTTPS://Invented-Workspace.SLACK.com":               "https://invented-workspace.slack.com",
		"https://a.slack.com:443/":                           "https://a.slack.com",
		"https://a.slack.com:0443":                           "https://a.slack.com",
		"https://a.slack.com:":                               "https://a.slack.com",
		"https://a.slack.com/?#":                             "https://a.slack.com",
		"https://a.slack.com?":                               "https://a.slack.com",
		"https://a.slack.com#":                               "https://a.slack.com",
		"https://" + strings.Repeat("a", 63) + ".slack.com":  "https://" + strings.Repeat("a", 63) + ".slack.com",
		"https://" + strings.Repeat("a", 64) + ".slack.com":  "",
		"http://a.slack.com/":                                "",
		"https://a.slack.com:8443/":                          "",
		"https://a.slack.com:443x/":                          "",
		"https://a.slack.com:443:443/":                       "",
		"https://user@a.slack.com/":                          "",
		"https://@a.slack.com/":                              "",
		"https://a.slack.com/archives":                       "",
		"https://a.slack.com//":                              "",
		"https://a.slack.com/?x=1":                           "",
		"https://a.slack.com/#x":                             "",
		"https://a.slack.com/#?":                             "",
		"https://a.b.slack.com/":                             "",
		"https://slack.com/":                                 "",
		"https://.slack.com/":                                "",
		"https://-a.slack.com/":                              "",
		"https://a-.slack.com/":                              "",
		"https://a_b.slack.com/":                             "",
		"https://a.slack.com.invalid.example/":               "",
		"https://a.slack.comx/":                              "",
		"https:/a.slack.com/":                                "",
		"https:a.slack.com/":                                 "",
		"a.slack.com":                                        "",
		"https://a.slack.com/ ":                              "",
		" https://a.slack.com/":                              "",
		"https://a.slack.com/\n":                             "",
		"\x00https://invented.slack.com/":                    "",
		"https://é.slack.com/":                               "",
		"https://" + strings.Repeat("a.", 120) + "slack.com": "",
		"":                             "",
		"https://[v1.slack.com]/":      "https://v1.slack.com",
		"https://[vA.slack.com]:443":   "https://va.slack.com",
		"https://[V1.slack.com]/":      "",
		"https://[vg.slack.com]/":      "",
		"https://[v.slack.com]/":       "",
		"https://[a.slack.com]/":       "",
		"https://[v1.slack.com]x/":     "",
		"https://x[v1.slack.com]/":     "",
		"https://[v1.slack.com/":       "",
		"https://v1.slack.com]/":       "",
		"https://[v1.slack.com]:8443/": "",
	} {
		if got := origin(value); got != want {
			t.Errorf("origin(%q) is %q, want %q", value, got, want)
		}
	}
	for _, value := range []any{nil, 7, true, []any{"https://a.slack.com"}} {
		if got := origin(value); got != "" {
			t.Errorf("origin(%v) is %q", value, got)
		}
	}
}

func TestIdentityBinding(t *testing.T) {
	// The digest slack_mapping.slack_identity_binding of the Python package gives this identity, which the
	// oracle's folders record.
	const recorded = "b7584ac2479befe3718970371e04fec56c8099bda74f65809596f370b01e4c71"
	if got := member(t, binding(t, testIdentity), "identity_sha256"); got != recorded {
		t.Fatalf("binding %v", got)
	}
	grid := testIdentity
	grid.EnterpriseID = "E12345678"
	if sameJSON(binding(t, grid), binding(t, testIdentity)) || !validBinding(binding(t, grid)) {
		t.Fatal("an enterprise does not change the binding")
	}
	for _, bad := range []Identity{{}, {AppID: "a1", TeamID: "T1", UserID: "U1"}, {AppID: "A1", TeamID: "T1", UserID: "B1"},
		{AppID: "A1", TeamID: "T1", UserID: "U1", EnterpriseID: "T1"}} {
		if _, err := IdentityBinding(bad); codeOf(err) != "slack_source_mismatch" {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}
