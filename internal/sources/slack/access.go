package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unique"

	"github.com/pwngh/gtm-connector/internal/sources/echo"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

const (
	tokenURL    = "https://slack.com/api/oauth.v2.access"
	identityURL = "https://slack.com/api/auth.test"

	expiryMargin    = 30 * time.Second     // a token this close to expiry is refreshed before use
	refreshLifetime = 30 * 24 * time.Hour  // how long a refresh token is usable
	maxAccessLife   = 24 * time.Hour       // the longest lifetime a rotating access token may state
	redacted        = "[redacted]"         // what a Secret prints as
	pending         = "refresh_pending"    // the saved sign-in needs a new login
	unchecked       = "identity_unchecked" // the token is saved, its identity not yet verified
	ready           = "ready"              // the token is verified for the saved identity
)

// allowedScopes are the Slack user scopes a sign-in may hold, in the order every scope list is kept.
var allowedScopes = []string{"channels:history", "groups:history"}

// Secret is an access or refresh token. It guards against a token being written out as plain text by
// accident: it prints as "[redacted]" under every format verb and refuses to be marshalled, as a value or as
// the key of a map, so a token leaves the program only where code calls Reveal on purpose. It is no guard
// against code that sets out to read it, with reflect, unsafe or a printer that follows pointers.
//
// The text is kept behind an unexported handle, not as a string of the Secret itself. encoding/json and
// reflect's String do not reach it. Two Secrets of the same text are equal, and the zero Secret is no token.
//
// A struct that holds a Secret in an unexported field is printed by reflection, with no method of the
// Secret or of the struct called, so no Format or GoString can make that print "[redacted]". fmt then prints
// the handle's address in place of the text. The address is the same for equal tokens within one process,
// so such output can show that two Secrets are equal, though never what they are. A type that holds a
// Secret keeps it in an exported field, as Credentials does, or prints itself.
type Secret struct{ text unique.Handle[string] }

// NewSecret is the Secret of a token's text. Empty text gives the zero Secret.
func NewSecret(text string) Secret {
	if text == "" {
		return Secret{}
	}
	return Secret{unique.Make(text)}
}

// Reveal is the token's text, for the request that sends it and the store that saves it.
func (s Secret) Reveal() string {
	if s == (Secret{}) {
		return ""
	}
	return s.text.Value()
}

// Format writes the redaction for every verb, %v and %#v included.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

var errSecretMarshalled = errors.New("slack: a credential is not marshalled")

// MarshalJSON refuses: a store writes a token by revealing it, never by marshalling the sign-in.
func (Secret) MarshalJSON() ([]byte, error) { return nil, errSecretMarshalled }

// MarshalText refuses, which is what an encoder asks of a map key.
func (Secret) MarshalText() ([]byte, error) { return nil, errSecretMarshalled }

// Identity is who a sign-in was verified for. EnterpriseID is empty outside an Enterprise Grid.
type Identity struct{ AppID, TeamID, UserID, EnterpriseID string }

// Credentials are the one saved Slack sign-in of a folder. A time that is not set is the zero time.
type Credentials struct {
	ClientID, RedirectURI string
	Identity              Identity
	WorkspaceOrigin       string // "https://NAME.slack.com", or empty when Slack did not state one
	RequestedScopes       []string
	GrantedScopes         []string
	State                 string // "ready", "identity_unchecked" or "refresh_pending"
	AccessToken           Secret
	RefreshToken          Secret // empty when the grant does not rotate
	AccessObtainedAt      time.Time
	AccessExpiresAt       time.Time // set with RefreshToken
	RefreshObtainedAt     time.Time // set with RefreshToken
	LastVerifiedAt        time.Time
}

// Connection is the store of the one saved sign-in.
type Connection interface {
	// Hold runs held with the saved sign-in while holding the connection's lock, so that no other command
	// reads, refreshes or replaces it meanwhile, and returns what held returns. A lock that is taken refuses
	// with the code slack_connection_busy. A failure that is not an *Error reads as slack_storage_failed.
	// ctx ends when the read's time does, and a store that waits stops waiting then.
	Hold(ctx context.Context, held func(Saved) error) error
}

// Saved is the sign-in under its lock. Each ctx ends when the read's time does, and a store that waits
// stops waiting then. The ctx of a Save does not end when the caller cancels the read: a sign-in that
// records a spent refresh token, or the pair that replaced it, is saved though the caller has gone.
type Saved interface {
	// Load gives the saved sign-in, or nil when none is saved.
	Load(ctx context.Context) (*Credentials, error)
	// Save replaces the saved sign-in durably before it returns.
	Save(ctx context.Context, document *Credentials) error
}

// access is a usable token with the identity and scopes it was saved for. Its fields are exported so that
// the token's own Format applies wherever an access is printed.
type access struct {
	Token    Secret
	Identity Identity
	Scopes   []string
}

// IdentityBinding names a Slack identity without holding it: the digest a mapping and a folder keep.
func IdentityBinding(identity Identity) (*strictjson.Object, error) {
	var enterprise any
	if identity.EnterpriseID != "" {
		enterprise = identity.EnterpriseID
	}
	if !identifier(identity.AppID, "A") || !identifier(identity.TeamID, "T") || !identifier(identity.UserID, "UW") ||
		(enterprise != nil && !identifier(enterprise, "E")) {
		return nil, refuse("slack_source_mismatch")
	}
	document, _ := strictjson.Dumps(strictjson.NewObject("schema_version", 1, "provider", "slack",
		"app_id", identity.AppID, "team_id", identity.TeamID, "user_id", identity.UserID,
		"enterprise_id", enterprise), strictjson.DumpOptions{SortKeys: true, Compact: true})
	sum := sha256.Sum256([]byte(document))
	return strictjson.NewObject("schema_version", 1, "provider", "slack",
		"identity_sha256", hex.EncodeToString(sum[:])), nil
}

// validBinding reports whether a value has the shape IdentityBinding gives.
func validBinding(value *strictjson.Object) bool {
	if value == nil || len(value.Keys) != 3 {
		return false
	}
	version, _ := value.Get("schema_version")
	provider, _ := value.Get("provider")
	sum, _ := text(value, "identity_sha256")
	if !isInt(version, 1) || provider != any("slack") || len(sum) != 64 {
		return false
	}
	for i := 0; i < len(sum); i++ {
		if c := sum[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// integer is a JSON integer that fits an int. A boolean or a number with a fraction is not one.
func integer(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case *big.Int:
		if n != nil && n.IsInt64() {
			return int(n.Int64()), true
		}
	}
	return 0, false
}

func isInt(value any, want int) bool {
	n, ok := integer(value)
	return ok && n == want
}

// validToken reports whether a token is 20 to 8,192 characters, each a printable ASCII character other
// than a space. A shorter one would be found in every answer Slack gives: a one-letter token is in the name
// of the member "ok".
func validToken(value any) (Secret, bool) {
	s, isText := value.(string)
	if !isText || !echo.Usable(s, 8192) {
		return Secret{}, false
	}
	return NewSecret(s), true
}

// scopeList puts a list of scopes in the kept order. A list that is empty, repeats a scope or names one
// outside allowedScopes is refused.
func scopeList(values []string) ([]string, bool) {
	if len(values) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !slices.Contains(allowedScopes, value) {
			return nil, false
		}
		seen[value] = true
	}
	var ordered []string
	for _, scope := range allowedScopes {
		if seen[scope] {
			ordered = append(ordered, scope)
		}
	}
	return ordered, true
}

// origin is a workspace URL reduced to "https://HOST", or "" unless it is the HTTPS root of one host under
// slack.com: no user, no port but 443, no path but "/", no query and no fragment. It accepts what Python's
// urlsplit accepts, which includes a host in brackets when it has the form of an IPvFuture literal.
func origin(value any) string {
	s, isText := value.(string)
	if !isText || len(s) > 256 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return ""
		}
	}
	const scheme = "https://"
	if len(s) < len(scheme) || lowerASCII(s[:len(scheme)]) != scheme {
		return ""
	}
	rest := s[len(scheme):]
	end := strings.IndexAny(rest, "/?#")
	netloc, after := rest, ""
	if end >= 0 {
		netloc, after = rest[:end], rest[end:]
	}
	// What follows the host is at most a "/", then an empty query and an empty fragment.
	after = strings.TrimPrefix(after, "/")
	after = strings.TrimPrefix(after, "?")
	after = strings.TrimPrefix(after, "#")
	if after != "" || strings.Contains(netloc, "@") {
		return ""
	}
	host, port, _ := strings.Cut(netloc, ":")
	if strings.ContainsAny(netloc, "[]") {
		// "[v", hex digits, a dot and the rest, "]", then nothing or a port.
		inner, tail, closed := strings.Cut(strings.TrimPrefix(netloc, "["), "]")
		version, _, dotted := strings.Cut(strings.TrimPrefix(inner, "v"), ".")
		if netloc[0] != '[' || !closed || (tail != "" && tail[0] != ':') || inner == "" || inner[0] != 'v' ||
			!dotted || version == "" || strings.Trim(version, "0123456789abcdefABCDEF") != "" {
			return ""
		}
		host, port = inner, strings.TrimPrefix(tail, ":")
	}
	if port != "" && (!digits(port, 256) || strings.TrimLeft(port, "0") != "443") {
		return ""
	}
	host = lowerASCII(host)
	name, isSlack := strings.CutSuffix(host, ".slack.com")
	if !isSlack || len(name) < 1 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return ""
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return ""
		}
	}
	return "https://" + host
}

// valid reports whether a saved sign-in keeps the rules it was saved under.
func (c *Credentials) valid() bool {
	major, minor, dotted := strings.Cut(c.ClientID, ".")
	requested, listed := scopeList(c.RequestedScopes)
	if !dotted || !digits(major, 32) || !digits(minor, 32) || !listed ||
		!slices.Equal(requested, c.RequestedScopes) || !slices.Equal(c.GrantedScopes, c.RequestedScopes) {
		return false
	}
	id := c.Identity
	if !identifierOf(id.AppID, "A", 8) || !identifierOf(id.TeamID, "T", 8) || !identifierOf(id.UserID, "UW", 8) ||
		(id.EnterpriseID != "" && !identifierOf(id.EnterpriseID, "E", 8)) {
		return false
	}
	if c.WorkspaceOrigin != "" && origin(c.WorkspaceOrigin) != c.WorkspaceOrigin {
		return false
	}
	if _, usable := validToken(c.AccessToken.Reveal()); !usable || c.AccessObtainedAt.IsZero() {
		return false
	}
	if c.RefreshToken == (Secret{}) {
		if !c.AccessExpiresAt.IsZero() || !c.RefreshObtainedAt.IsZero() {
			return false
		}
	} else {
		life := c.AccessExpiresAt.Sub(c.AccessObtainedAt)
		if _, usable := validToken(c.RefreshToken.Reveal()); !usable || c.AccessExpiresAt.IsZero() ||
			life < time.Second || life > maxAccessLife || !c.RefreshObtainedAt.Equal(c.AccessObtainedAt) {
			return false
		}
	}
	switch c.State {
	case ready, pending, unchecked:
	default:
		return false
	}
	if c.LastVerifiedAt.IsZero() {
		return c.State != ready
	}
	return !c.LastVerifiedAt.Before(c.AccessObtainedAt)
}

func (c Credentials) copy() Credentials {
	c.RequestedScopes = slices.Clone(c.RequestedScopes)
	c.GrantedScopes = slices.Clone(c.GrantedScopes)
	return c
}

// needsLogin reports whether only a new login can make the sign-in usable again.
func (c *Credentials) needsLogin(now time.Time) bool {
	switch {
	case c.State == pending || now.Before(c.AccessObtainedAt):
		return true
	case c.State == unchecked && !c.AccessExpiresAt.IsZero() && !now.Before(c.AccessExpiresAt):
		return true
	case !c.LastVerifiedAt.IsZero() && now.Before(c.LastVerifiedAt):
		return true
	}
	return !c.RefreshObtainedAt.IsZero() &&
		(now.Before(c.RefreshObtainedAt) || !now.Before(c.RefreshObtainedAt.Add(refreshLifetime)))
}

// storeError is a failure of a store as a refusal. A cancel stays what it is.
func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case code(err) != "" || errors.Is(err, context.Canceled):
		return err
	}
	return refuse("slack_storage_failed")
}

// kept is the saved sign-in under its lock, as one read uses it: every Load and Save runs within the read's
// time, and every token loaded joins those the read looks for in what Slack sends back.
type kept struct {
	under Saved
	ctx   context.Context
	b     *budget
}

func (k kept) load() (*Credentials, error) {
	var found *Credentials
	err := k.b.store(k.ctx, func(limited context.Context) (err error) {
		found, err = k.under.Load(limited)
		return err
	})
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
	k.b.hold(&document)
	return &document, nil
}

// save is not ended by the caller's cancel, only by the read's deadline.
func (k kept) save(document Credentials) error {
	document = document.copy()
	return storeError(k.b.store(context.WithoutCancel(k.ctx), func(limited context.Context) error {
		return k.under.Save(limited, &document)
	}))
}

// acquire gets a usable token under the connection's lock. Given the access a page request just refused as
// expired, it refreshes that token once; a token another command already replaced is used as it is.
func (r *Reader) acquire(ctx context.Context, b *budget, p *progress, expired *access) (*access, error) {
	p.credentialOK = false
	p.enter("credential_binding")
	if !validBinding(r.Binding) {
		return nil, refuse("slack_invalid_configuration")
	}
	var held *access
	bind := func(under Saved) error {
		saved := kept{under, ctx, b}
		document, err := saved.load()
		if err != nil {
			return err
		}
		force := false
		if document != nil {
			// The folder's pinned identity is checked under the same lock as the rotation. A sign-in that
			// was replaced by another identity is never verified or refreshed for this read.
			binding, err := IdentityBinding(document.Identity)
			if err != nil {
				return err
			}
			if !sameJSON(binding, r.Binding) || (expired != nil && document.Identity != expired.Identity) {
				return refuse("slack_identity_mismatch")
			}
			force = expired != nil && document.AccessToken == expired.Token
		}
		usable, err := r.usable(ctx, b, saved, document, force)
		if err != nil {
			return err
		}
		if _, err := b.remaining(); err != nil {
			return err
		}
		held = &access{usable.AccessToken, usable.Identity, slices.Clone(usable.GrantedScopes)}
		return nil
	}
	err := b.store(ctx, func(limited context.Context) error { return r.Connection.Hold(limited, bind) })
	if err = storeError(err); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	p.credentialOK = true
	p.enter("thread_read")
	return held, nil
}

// usable makes the saved sign-in ready for a request, or refuses. An unverified token has its identity
// checked. A token at its expiry, or one Slack just refused as expired, is refreshed once: the sign-in is
// first saved as needing a login, so a refresh that fails halfway never leaves a spent refresh token
// looking usable.
func (r *Reader) usable(ctx context.Context, b *budget, saved kept, document *Credentials, force bool) (*Credentials, error) {
	if document == nil {
		return nil, refuse("slack_not_connected")
	}
	if document.needsLogin(r.Clock.now()) {
		return nil, refuse("slack_reconnect_required")
	}
	if document.State == unchecked {
		verified, err := r.verify(ctx, b, saved, *document)
		if err != nil {
			return nil, err
		}
		if !force {
			return verified, nil
		}
		document = verified
	}
	expiry := document.AccessExpiresAt
	if !force && (expiry.IsZero() || r.Clock.now().Add(expiryMargin).Before(expiry)) {
		return document, nil
	}
	quarantined := document.copy()
	quarantined.State = pending
	if document.RefreshToken == (Secret{}) {
		if force {
			if err := saved.save(quarantined); err != nil {
				return nil, err
			}
		}
		return nil, refuse("slack_reconnect_required")
	}
	if force && b.refreshAttempts >= 1 {
		if err := saved.save(quarantined); err != nil {
			return nil, err
		}
	}
	if err := b.beginRefresh(); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		// The caller has gone. The refresh token has not been sent, so the sign-in is left as it is.
		return nil, err
	}
	if err := saved.save(quarantined); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	obtained := r.Clock.now()
	granted, err := r.refresh(ctx, b, document)
	if err != nil {
		return nil, err
	}
	replacement, err := parseRefresh(granted, *document, obtained)
	if err != nil {
		return nil, err
	}
	// The pair that was replaced stays among the tokens the read looks for, beside the new one.
	b.hold(&replacement)
	if err := saved.save(replacement); err != nil {
		return nil, err
	}
	return r.verify(ctx, b, saved, replacement)
}

// verify checks a token's identity with Slack and saves the sign-in as ready. An answer that shows the
// token is for someone else, or is no longer accepted, saves the sign-in as needing a login.
func (r *Reader) verify(ctx context.Context, b *budget, saved kept, candidate Credentials) (*Credentials, error) {
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	workspace, err := r.identity(ctx, b, &candidate)
	if err != nil {
		switch code(err) {
		case "slack_identity_mismatch", "slack_reconnect_required", "slack_token_type_unsupported":
			quarantined := candidate.copy()
			quarantined.State = pending
			if err := saved.save(quarantined); err != nil {
				return nil, err
			}
		}
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	now := r.Clock.now()
	if now.Before(candidate.AccessObtainedAt) ||
		(!candidate.AccessExpiresAt.IsZero() && !now.Before(candidate.AccessExpiresAt)) {
		return nil, refuse("slack_reconnect_required")
	}
	verified := candidate.copy()
	verified.State, verified.LastVerifiedAt, verified.WorkspaceOrigin = ready, now, workspace
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	if err := saved.save(verified); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	return &verified, nil
}

// reject saves a sign-in that Slack just refused as needing a login, without a request. A token or an
// identity that another command saved since is left alone.
func (r *Reader) reject(ctx context.Context, b *budget, refused *access) error {
	quarantine := func(under Saved) error {
		saved := kept{under, ctx, b}
		document, err := saved.load()
		if err != nil || document == nil {
			return err
		}
		if document.Identity != refused.Identity || document.AccessToken != refused.Token || document.State == pending {
			return nil
		}
		document.State = pending
		return saved.save(*document)
	}
	return storeError(b.store(ctx, func(limited context.Context) error {
		return r.Connection.Hold(limited, quarantine)
	}))
}

// authRejections are the error names with which Slack says a saved sign-in is no longer accepted.
var authRejections = []string{"invalid_auth", "token_revoked", "token_expired", "account_inactive", "not_authed",
	"invalid_refresh_token", "refresh_token_expired", "invalid_grant"}

// authResult accepts a sign-in document whose ok is true and names the refusal of any other.
func authResult(document *strictjson.Object) error {
	ok, _ := document.Get("ok")
	accepted, isBool := ok.(bool)
	if !isBool {
		return refuse("slack_response_invalid")
	}
	if accepted {
		return nil
	}
	name, _ := text(document, "error")
	switch {
	case name == "ratelimited" || name == "rate_limited":
		return refuse("slack_rate_limited")
	case slices.Contains(authRejections, name):
		return refuse("slack_reconnect_required")
	case name == "not_allowed_token_type" || name == "not_allowed_token":
		return refuse("slack_token_type_unsupported")
	}
	return refuse("slack_oauth_rejected")
}

// authRequest makes one request that keeps the sign-in usable and returns Slack's accepted document. An
// answer that holds one of tokens is refused. Nil looks for none.
func (r *Reader) authRequest(ctx context.Context, b *budget, tokens *echo.Tokens, build func(context.Context) (*http.Request, error)) (*strictjson.Object, error) {
	got, err := r.send(ctx, b, "auth", build)
	if err != nil {
		return nil, err
	}
	switch {
	case got.status == http.StatusTooManyRequests:
		return nil, refuse("slack_rate_limited")
	case got.status >= 500:
		return nil, refuse("slack_unavailable")
	case got.status == http.StatusUnauthorized || got.status == http.StatusForbidden:
		return nil, refuse("slack_reconnect_required")
	case got.status != http.StatusOK:
		return nil, refuse("slack_response_invalid")
	}
	document, err := decode(got.body, tokens)
	if err != nil {
		return nil, err
	}
	if err := authResult(document); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	return document, nil
}

// refresh spends the refresh token for a new pair. The client is public: no secret is sent. The answer holds
// the new pair, so it is not searched for tokens, and nothing of it but those two tokens is kept as text.
func (r *Reader) refresh(ctx context.Context, b *budget, document *Credentials) (*strictjson.Object, error) {
	form := "grant_type=refresh_token&client_id=" + url.QueryEscape(document.ClientID) +
		"&refresh_token=" + url.QueryEscape(document.RefreshToken.Reveal())
	return r.authRequest(ctx, b, nil, func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request, nil
	})
}

// noBot refuses a document that describes a bot grant or an organization-wide install.
func noBot(document *strictjson.Object) error {
	_, botUser := document.Get("bot_user_id")
	_, bot := document.Get("bot_id")
	install, stated := document.Get("is_enterprise_install")
	if _, isBool := install.(bool); botUser || bot || install == any(true) || (stated && !isBool) {
		return refuse("slack_token_type_unsupported")
	}
	return nil
}

// identity asks Slack who the token is for and requires the saved team and user. It returns the workspace
// origin Slack states, or "" when that is not a slack.com root. The origin is lowered from what Slack
// stated, so it is searched for tokens once more as it will be saved.
func (r *Reader) identity(ctx context.Context, b *budget, candidate *Credentials) (string, error) {
	document, err := r.authRequest(ctx, b, &b.tokens, func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, identityURL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Authorization", "Bearer "+candidate.AccessToken.Reveal())
		return request, nil
	})
	if err != nil {
		return "", err
	}
	workspace, err := verifyIdentity(document, candidate.Identity)
	if err == nil && b.tokens.InText(workspace) {
		return "", refuse("slack_response_invalid")
	}
	return workspace, err
}

// verifyIdentity reads Slack's accepted answer to the identity request against the saved identity.
func verifyIdentity(document *strictjson.Object, pinned Identity) (string, error) {
	if err := noBot(document); err != nil {
		return "", err
	}
	team, _ := document.Get("team_id")
	user, _ := document.Get("user_id")
	if !identifierOf(team, "T", 8) || !identifierOf(user, "UW", 8) {
		return "", refuse("slack_response_invalid")
	}
	if team != any(pinned.TeamID) || user != any(pinned.UserID) {
		return "", refuse("slack_identity_mismatch")
	}
	if enterprise, stated := document.Get("enterprise_id"); stated {
		if name, isText := enterprise.(string); (enterprise == nil) != (pinned.EnterpriseID == "") ||
			(enterprise != nil && (!isText || name != pinned.EnterpriseID)) {
			return "", refuse("slack_identity_mismatch")
		}
	}
	workspace, _ := document.Get("url")
	return origin(workspace), nil
}

// parseRefresh reads Slack's answer to a refresh into the sign-in that replaces previous. The grant must
// be a rotating user token with exactly the scopes that were requested, and every identity it states must
// be the saved one. The replacement is saved unverified.
func parseRefresh(document *strictjson.Object, previous Credentials, obtained time.Time) (Credentials, error) {
	invalid, unsupported := refuse("slack_response_invalid"), refuse("slack_token_type_unsupported")
	none := Credentials{}
	if err := noBot(document); err != nil {
		return none, err
	}
	user := document
	if nested, isNested := document.Get("authed_user"); isNested {
		for _, key := range []string{"access_token", "refresh_token", "token_type"} {
			if _, both := document.Get(key); both {
				return none, unsupported
			}
		}
		var isObject bool
		if user, isObject = object(nested); !isObject {
			return none, invalid
		}
		if err := noBot(user); err != nil {
			return none, err
		}
	}
	if kind, _ := user.Get("token_type"); kind != any("user") {
		return none, unsupported
	}
	token, _ := user.Get("access_token")
	accessToken, usable := validToken(token)
	if !usable {
		return none, invalid
	}
	lifetime, _ := user.Get("expires_in")
	seconds, whole := integer(lifetime)
	token, _ = user.Get("refresh_token")
	refreshToken, usable := validToken(token)
	if !whole || seconds < 1 || seconds > int(maxAccessLife/time.Second) || !usable {
		return none, invalid
	}
	stated, isText := text(user, "scope")
	if !isText {
		return none, refuse("slack_scope_mismatch")
	}
	scopes, listed := scopeList(strings.Split(stated, ","))
	if !listed || !slices.Equal(scopes, previous.RequestedScopes) {
		return none, refuse("slack_scope_mismatch")
	}

	// Each identity the answer states is read where Slack may put it, and all must be the saved ones.
	pinned, matches := previous.Identity, true
	check := func(from *strictjson.Object, key, prefixes, saved string) bool {
		value, present := from.Get(key)
		if !present {
			return true
		}
		if !identifierOf(value, prefixes, 8) {
			return false
		}
		matches = matches && value == any(saved)
		return true
	}
	enterprise := func(value any) bool {
		if value == nil {
			matches = matches && pinned.EnterpriseID == ""
			return true
		}
		if !identifierOf(value, "E", 8) {
			return false
		}
		matches = matches && value == any(pinned.EnterpriseID)
		return true
	}
	if !check(user, "id", "UW", pinned.UserID) || !check(document, "user_id", "UW", pinned.UserID) ||
		!check(document, "app_id", "A", pinned.AppID) {
		return none, invalid
	}
	if team, present := document.Get("team"); present {
		item, isObject := object(team)
		if _, named := item.Get("id"); !isObject || !named || !check(item, "id", "T", pinned.TeamID) {
			return none, invalid
		}
	}
	if !check(document, "team_id", "T", pinned.TeamID) {
		return none, invalid
	}
	if grid, present := document.Get("enterprise"); present && grid != nil {
		item, isObject := object(grid)
		name, named := item.Get("id")
		if !isObject || !named || name == nil || !enterprise(name) {
			return none, invalid
		}
	} else if present {
		enterprise(nil)
	}
	if grid, present := document.Get("enterprise_id"); present && !enterprise(grid) {
		return none, invalid
	}
	if !matches {
		return none, refuse("slack_identity_mismatch")
	}

	replacement := previous.copy()
	replacement.AccessToken, replacement.RefreshToken = accessToken, refreshToken
	replacement.AccessObtainedAt, replacement.RefreshObtainedAt = obtained, obtained
	replacement.AccessExpiresAt = obtained.Add(time.Duration(seconds) * time.Second)
	replacement.State, replacement.LastVerifiedAt = unchecked, time.Time{}
	return replacement, nil
}
