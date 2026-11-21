package slack

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The sign-in commands: Connect, the browser sign-in with PKCE
// against an existing public client; Status, which reads the saved sign-in or checks it with Slack; and
// Disconnect, which removes it and may first ask Slack to revoke its access token. Each runs under the saved
// sign-in's lock. Each refusal is an *Error with one fixed code, and no token reaches a result or an error.

const (
	authorizeURL = "https://slack.com/oauth/v2/authorize"
	revokeURL    = "https://slack.com/api/auth.revoke"
)

// loginTime is how long a sign-in waits for the browser's approval.
var loginTime = 10 * time.Minute

// signInExits are the exit codes of the refusals only the sign-in commands give, as docs/sources.md
// lists them.
var signInExits = map[string]int{
	"slack_browser_unavailable": 1, "slack_callback_unavailable": 1, "slack_login_timeout": 1,
	"slack_login_denied": 3, "slack_login_unconfirmed": 3, "slack_scope_mismatch": 3, "slack_identity_mismatch": 3,
}

// confirmed asks Confirm whether the approval of this identity, in this workspace, is the operator's own,
// and refuses with slack_login_unconfirmed unless it says yes. A Reader with no Confirm saves no sign-in.
func (r *Reader) confirmed(ctx context.Context, identity Identity, workspace string) error {
	if r.Confirm == nil {
		return refuse("slack_login_unconfirmed")
	}
	yes, err := r.Confirm(ctx, identity, workspace)
	switch {
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return refuse("slack_login_timeout")
	case err != nil || !yes:
		return refuse("slack_login_unconfirmed")
	}
	return nil
}

// Configuration is what a sign-in was asked for: the existing app's public client ID, its exact registered
// redirect, the user scopes, and the workspace it must be, when one was named.
type Configuration struct {
	ClientID, RedirectURI string
	Scopes                []string
	TeamID                string
	// Pinned is the identity digest the workspace's Slack is pinned to, as IdentityBinding gives it, or ""
	// for a workspace that was never connected to Slack. A grant for any other user is refused before it
	// is saved.
	Pinned string
	port   int
}

// NewConfiguration checks what a sign-in was asked for before any file, browser or request is used. No
// scopes asks for channels:history. Anything else that is not as docs/sources.md states it refuses
// with slack_invalid_configuration.
func NewConfiguration(clientID, redirectURI string, scopes []string, teamID string) (Configuration, error) {
	invalid := refuse("slack_invalid_configuration")
	if scopes == nil {
		scopes = []string{"channels:history"}
	}
	ordered, listed := scopeList(scopes)
	// A sign-in reads a thread, so it holds a history scope; the profile scopes only add to one.
	history := slices.Contains(ordered, "channels:history") || slices.Contains(ordered, "groups:history")
	major, minor, dotted := strings.Cut(clientID, ".")
	port, redirect := callbackPort(redirectURI)
	if !dotted || !digits(major, 32) || !digits(minor, 32) || !listed || !history || !redirect ||
		(teamID != "" && !identifierOf(teamID, "T", 8)) {
		return Configuration{}, invalid
	}
	return Configuration{ClientID: clientID, RedirectURI: redirectURI, Scopes: ordered, TeamID: teamID, port: port}, nil
}

// callbackPort is the port of a redirect of the one form a sign-in takes, http://localhost:PORT/slack/callback
// with PORT from 1024 to 65535 and no leading zero.
func callbackPort(redirect string) (int, bool) {
	number, found := strings.CutPrefix(redirect, "http://localhost:")
	number, found2 := strings.CutSuffix(number, "/slack/callback")
	if !found || !found2 || len(number) < 4 || number[0] == '0' || !digits(number, 5) {
		return 0, false
	}
	port, _ := strconv.Atoi(number)
	return port, port >= 1024 && port <= 65535
}

// NeedsLogin reports whether only a new login can make the sign-in usable again.
func (c *Credentials) NeedsLogin(now time.Time) bool {
	return c.needsLogin(now.UTC().Truncate(time.Microsecond))
}

// PublicStatus is what connect and status return of a sign-in, which holds no token and no identity. A nil
// sign-in is one that is not saved. verification is "saved" for a look at the saved sign-in, or "live" when
// the command checked it with Slack.
func PublicStatus(c *Credentials, verification string, now time.Time) *strictjson.Object {
	if c == nil {
		return strictjson.NewObject("schema_version", 1, "configured", false, "credential_state", "missing",
			"verification", verification, "verified_component", "authentication", "requested_scopes", []any{},
			"granted_scopes", []any{}, "rotation", nil, "access_expires_at", nil, "last_verified_at", nil,
			"needs_login", true)
	}
	list := func(scopes []string) []any {
		values := make([]any, len(scopes))
		for i, scope := range scopes {
			values[i] = scope
		}
		return values
	}
	optional := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return stamp(t)
	}
	return strictjson.NewObject("schema_version", 1, "configured", true, "credential_state", c.State,
		"verification", verification, "verified_component", "authentication",
		"requested_scopes", list(c.RequestedScopes), "granted_scopes", list(c.GrantedScopes),
		"rotation", c.RefreshToken != (Secret{}), "access_expires_at", optional(c.AccessExpiresAt),
		"last_verified_at", optional(c.LastVerifiedAt), "needs_login", c.NeedsLogin(now))
}

// Progress is told each step of a sign-in command as it starts and passes: "browser", "exchange",
// "refresh", "identity", "save" and "revoke", each "waiting", "running" or "passed".
type Progress func(step, status string)

func (p Progress) tell(step, status string) {
	if p != nil {
		p(step, status)
	}
}

// Browser opens a URL in the system browser.
type Browser func(ctx context.Context, url string) error

// loaded is the saved sign-in, checked as a read checks it, or nil when none is saved.
func loaded(ctx context.Context, under Saved) (*Credentials, error) {
	found, err := under.Load(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	if found == nil {
		return nil, nil
	}
	if !found.valid() {
		return nil, refuse("slack_credentials_invalid")
	}
	document := found.copy()
	return &document, nil
}

// randomText is n random bytes as unpadded base64url, as secrets.token_urlsafe gives them.
func randomText(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Connect signs in to an existing public Slack app: it opens the browser at Slack's authorization page,
// waits up to ten minutes for the approval to reach the registered localhost redirect, exchanges the code
// with its PKCE verifier, checks the identity the token is for, and saves the sign-in as ready. A folder
// that holds a sign-in for one identity is never given another. It returns the public status.
func (r *Reader) Connect(ctx context.Context, cfg Configuration, open Browser, progress Progress) (*strictjson.Object, error) {
	if cfg.port == 0 {
		return nil, refuse("slack_invalid_configuration")
	}
	var result *strictjson.Object
	err := r.Connection.Hold(ctx, func(under Saved) error {
		previous, err := loaded(ctx, under)
		if err != nil {
			return err
		}
		verifier := randomText(32)
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		authorize := func(state string) string {
			query := "client_id=" + url.QueryEscape(cfg.ClientID) + "&scope=&user_scope=" +
				url.QueryEscape(strings.Join(cfg.Scopes, ",")) + "&redirect_uri=" + url.QueryEscape(cfg.RedirectURI) +
				"&state=" + url.QueryEscape(state) + "&code_challenge=" + challenge + "&code_challenge_method=S256"
			if cfg.TeamID != "" {
				query += "&team=" + url.QueryEscape(cfg.TeamID)
			}
			return authorizeURL + "?" + query
		}
		finish := func(ctx context.Context, code string) error {
			b := newBudget(r.Clock)
			progress.tell("exchange", "running")
			// The exchange's receipt is finished with what its answer gives once it is read: a grant that
			// is not one this sign-in can keep fails it.
			var candidate Credentials
			err := r.audited(ctx, "slack_sign_in_exchange", func(ctx context.Context) error {
				obtained := r.Clock.now()
				document, err := r.exchange(ctx, b, code, verifier, cfg)
				if err != nil {
					return err
				}
				if candidate, err = parseInitial(document, cfg, obtained); err != nil {
					return err
				}
				if previous != nil && previous.Identity != candidate.Identity {
					return refuse("slack_identity_mismatch")
				}
				if cfg.Pinned != "" {
					binding, err := IdentityBinding(candidate.Identity)
					if err != nil {
						return err
					}
					if digest, _ := text(binding, "identity_sha256"); digest != cfg.Pinned {
						return refuse("slack_identity_mismatch")
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			b.hold(&candidate)
			progress.tell("identity", "running")
			workspace, err := r.checked(ctx, b, kept{under, ctx, b, true}, candidate)
			if err != nil {
				return err
			}
			progress.tell("identity", "passed")
			// Anyone who reads the start page first can finish a sign-in with an approval of their own, so
			// nothing is saved until the person at the command's terminal says this approval is theirs.
			if err := r.confirmed(ctx, candidate.Identity, workspace); err != nil {
				return err
			}
			// The wait for that answer is not the sign-in's: the save has a budget of its own.
			after := newBudget(r.Clock)
			after.hold(&candidate)
			progress.tell("save", "running")
			verified, err := r.ready(ctx, after, kept{under, ctx, after, true}, candidate, workspace)
			if err != nil {
				return err
			}
			progress.tell("save", "passed")
			result = PublicStatus(verified, "live", r.Clock.now())
			return nil
		}
		progress.tell("browser", "waiting")
		return receiveConsent(ctx, cfg, authorize, open, finish)
	})
	if err = storeError(err); err != nil {
		return nil, err
	}
	return result, nil
}

// exchange spends the approval's code for the grant, with the PKCE verifier and no secret. Its answer
// holds the new tokens, so it is not searched for them. A refusal of the code is slack_oauth_rejected.
func (r *Reader) exchange(ctx context.Context, b *budget, grant, verifier string, cfg Configuration) (*strictjson.Object, error) {
	form := "grant_type=authorization_code&client_id=" + url.QueryEscape(cfg.ClientID) + "&code=" +
		url.QueryEscape(grant) + "&redirect_uri=" + url.QueryEscape(cfg.RedirectURI) + "&code_verifier=" +
		url.QueryEscape(verifier)
	got, err := r.send(ctx, b, "auth", func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request, nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case got.status == http.StatusTooManyRequests:
		return nil, refuse("slack_rate_limited")
	case got.status >= 500:
		return nil, refuse("slack_unavailable")
	case got.status == http.StatusUnauthorized || got.status == http.StatusForbidden:
		return nil, refuse("slack_oauth_rejected")
	case got.status != http.StatusOK:
		return nil, refuse("slack_response_invalid")
	}
	document, err := decode(got.body, nil)
	if err != nil {
		return nil, err
	}
	if err := authResult(document); err != nil {
		if code(err) == "slack_reconnect_required" {
			// A code Slack does not accept is a rejected authorization, not a sign-in to renew.
			return nil, refuse("slack_oauth_rejected")
		}
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	return document, nil
}

// parseInitial reads the answer to the exchange into a sign-in that is not saved yet: a user grant with
// exactly the scopes asked for, of the workspace named when one was, as slack_auth._parse_initial reads it.
func parseInitial(document *strictjson.Object, cfg Configuration, obtained time.Time) (Credentials, error) {
	invalid, unsupported := refuse("slack_response_invalid"), refuse("slack_token_type_unsupported")
	none := Credentials{}
	if err := noBot(document); err != nil {
		return none, err
	}
	for _, key := range []string{"access_token", "refresh_token", "token_type"} {
		if _, flat := document.Get(key); flat {
			return none, unsupported
		}
	}
	nested, _ := document.Get("authed_user")
	user, isObject := object(nested)
	if !isObject {
		return none, invalid
	}
	if err := noBot(user); err != nil {
		return none, err
	}
	if kind, _ := user.Get("token_type"); kind != any("user") {
		return none, unsupported
	}
	token, _ := user.Get("access_token")
	accessToken, usable := validToken(token)
	if !usable {
		return none, invalid
	}
	signIn := Credentials{ClientID: cfg.ClientID, RedirectURI: cfg.RedirectURI, State: unchecked,
		AccessToken: accessToken, AccessObtainedAt: obtained}
	_, rotates := user.Get("refresh_token")
	_, expires := user.Get("expires_in")
	if rotates || expires {
		lifetime, _ := user.Get("expires_in")
		seconds, whole := integer(lifetime)
		token, _ := user.Get("refresh_token")
		refreshToken, usable := validToken(token)
		if !whole || seconds < 1 || seconds > int(maxAccessLife/time.Second) || !usable {
			return none, invalid
		}
		signIn.RefreshToken, signIn.RefreshObtainedAt = refreshToken, obtained
		signIn.AccessExpiresAt = obtained.Add(time.Duration(seconds) * time.Second)
	}
	stated, isText := text(user, "scope")
	scopes, listed := scopeList(strings.Split(stated, ","))
	if !isText || !listed || !slices.Equal(scopes, cfg.Scopes) {
		return none, refuse("slack_scope_mismatch")
	}
	signIn.RequestedScopes, signIn.GrantedScopes = scopes, slices.Clone(scopes)
	app, _ := document.Get("app_id")
	team, _ := document.Get("team")
	teamObject, isTeam := object(team)
	var teamID, enterprise any
	if isTeam {
		teamID, _ = teamObject.Get("id")
	}
	userID, _ := user.Get("id")
	if grid, _ := document.Get("enterprise"); grid != nil {
		item, isGrid := object(grid)
		if !isGrid {
			return none, invalid
		}
		enterprise, _ = item.Get("id")
		if !identifierOf(enterprise, "E", 8) {
			return none, invalid
		}
	}
	if !identifierOf(app, "A", 8) || !identifierOf(teamID, "T", 8) || !identifierOf(userID, "UW", 8) {
		return none, invalid
	}
	signIn.Identity = Identity{AppID: app.(string), TeamID: teamID.(string), UserID: userID.(string)}
	if enterprise != nil {
		signIn.Identity.EnterpriseID = enterprise.(string)
	}
	if cfg.TeamID != "" && cfg.TeamID != signIn.Identity.TeamID {
		return none, refuse("slack_identity_mismatch")
	}
	return signIn, nil
}

// Status is the saved sign-in's public status. With check it is first checked with Slack, as a read would
// use it: a token near its expiry is refreshed once and checked, and any other is checked as it is. A check
// never reads a message.
func (r *Reader) Status(ctx context.Context, check bool, progress Progress) (*strictjson.Object, error) {
	if !check {
		var result *strictjson.Object
		err := r.Connection.Hold(ctx, func(under Saved) error {
			document, err := loaded(ctx, under)
			result = PublicStatus(document, "saved", r.Clock.now())
			return err
		})
		if err = storeError(err); err != nil {
			return nil, err
		}
		return result, nil
	}
	b := newBudget(r.Clock)
	var checked *Credentials
	err := b.store(ctx, func(limited context.Context) error {
		return r.Connection.Hold(limited, func(under Saved) error {
			saved := kept{under, ctx, b, false}
			document, err := saved.load()
			if err != nil {
				return err
			}
			if document != nil && document.State == ready && !document.needsLogin(r.Clock.now()) &&
				(document.AccessExpiresAt.IsZero() || r.Clock.now().Add(expiryMargin).Before(document.AccessExpiresAt)) {
				// A token that needs no refresh is saved as unchecked first, so a check that fails halfway
				// never leaves a refused token looking ready.
				unverified := document.copy()
				unverified.State = unchecked
				if err := saved.save(unverified); err != nil {
					return err
				}
				document = &unverified
			}
			if document != nil && !document.needsLogin(r.Clock.now()) {
				if document.State != unchecked {
					progress.tell("refresh", "running")
				}
				progress.tell("identity", "running")
			}
			checked, err = r.usable(ctx, b, saved, document, false)
			if err == nil {
				progress.tell("identity", "passed")
			}
			return err
		})
	})
	if err = storeError(err); err != nil {
		return nil, err
	}
	return PublicStatus(checked, "live", r.Clock.now()), nil
}

// Disconnect removes the saved sign-in. With revoke it first asks Slack to revoke the saved access token;
// a revocation Slack does not confirm is reported in the result, and the sign-in is removed all the same.
// Repeating it succeeds.
func (r *Reader) Disconnect(ctx context.Context, revoke bool, progress Progress) (*strictjson.Object, error) {
	removed, revoked := false, false
	var failed any
	err := r.Connection.Hold(ctx, func(under Saved) error {
		erasable, isErasable := under.(Erasable)
		if !isErasable {
			return refuse("slack_storage_failed")
		}
		document, err := loaded(ctx, under)
		if err != nil {
			return err
		}
		switch {
		case revoke && document != nil:
			progress.tell("revoke", "running")
			if err := r.revoke(ctx, document); err != nil {
				if code(err) == "" {
					return err
				}
				failed = code(err)
			} else {
				revoked = true
				progress.tell("revoke", "passed")
			}
		case revoke:
			failed = "slack_not_connected"
		}
		if document != nil {
			removed, err = erasable.Remove(ctx)
		}
		return err
	})
	if err = storeError(err); err != nil {
		return nil, err
	}
	return strictjson.NewObject("schema_version", 1, "status", "disconnected", "local_credentials_removed", removed,
		"remote_revoke_requested", revoke, "remote_access_token_revoked", revoked, "revocation_error", failed), nil
}

// Erasable is a saved sign-in that can be removed. Remove reports whether there was one to remove.
type Erasable interface {
	Saved
	Remove(ctx context.Context) (bool, error)
}

// revoke asks Slack to revoke the access token, without a refresh, and requires Slack to confirm it.
func (r *Reader) revoke(ctx context.Context, document *Credentials) error {
	return r.audited(ctx, "slack_sign_in_revoke", func(ctx context.Context) error {
		return r.revokeRequest(ctx, document)
	})
}

func (r *Reader) revokeRequest(ctx context.Context, document *Credentials) error {
	b := newBudget(r.Clock)
	b.hold(document)
	answer, err := r.authRequest(ctx, b, &b.tokens, func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, revokeURL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Authorization", "Bearer "+document.AccessToken.Reveal())
		return request, nil
	})
	if err != nil {
		return err
	}
	if confirmed, _ := answer.Get("revoked"); confirmed != any(true) {
		return refuse("slack_response_invalid")
	}
	return nil
}
