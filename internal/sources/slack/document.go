package slack

import (
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The saved sign-in as a file holds it: the document the Python package wrote to a state folder's
// slack-connection/credentials.json, with the same members, so a store keeps one format. The tokens are in
// it as text. A store writes it only where no other user can read it, and nothing else prints it.

// documentKeys are the members of a saved sign-in, each present.
var documentKeys = []string{"schema_version", "provider", "client_id", "redirect_uri", "identity", "workspace_origin",
	"requested_scopes", "granted_scopes", "credential_state", "access_token", "refresh_token", "access_obtained_at",
	"access_expires_at", "refresh_obtained_at", "last_verified_at"}

// MaxDocumentBytes is the largest saved sign-in a store reads.
const MaxDocumentBytes = 64 << 10

// EncodeCredentials is a sign-in as its file holds it: sorted, ASCII JSON. A sign-in that does not keep the
// rules it is saved under is refused with slack_storage_failed, and nothing is written.
func EncodeCredentials(c *Credentials) ([]byte, error) {
	if c == nil || !c.valid() {
		return nil, refuse("slack_storage_failed")
	}
	optional := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return stamp(t)
	}
	text := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	list := func(values []string) []any {
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	}
	id := c.Identity
	document := strictjson.NewObject("schema_version", 1, "provider", "slack", "client_id", c.ClientID,
		"redirect_uri", c.RedirectURI, "identity", strictjson.NewObject("app_id", id.AppID, "team_id", id.TeamID,
			"user_id", id.UserID, "enterprise_id", text(id.EnterpriseID)),
		"workspace_origin", text(c.WorkspaceOrigin), "requested_scopes", list(c.RequestedScopes),
		"granted_scopes", list(c.GrantedScopes), "credential_state", c.State,
		"access_token", c.AccessToken.Reveal(), "refresh_token", text(c.RefreshToken.Reveal()),
		"access_obtained_at", stamp(c.AccessObtainedAt), "access_expires_at", optional(c.AccessExpiresAt),
		"refresh_obtained_at", optional(c.RefreshObtainedAt), "last_verified_at", optional(c.LastVerifiedAt))
	encoded, err := strictjson.Dumps(document, strictjson.DumpOptions{EnsureASCII: true, SortKeys: true})
	if err != nil || len(encoded) > MaxDocumentBytes {
		return nil, refuse("slack_storage_failed")
	}
	return []byte(encoded), nil
}

// DecodeCredentials reads a saved sign-in. Anything but a document EncodeCredentials gives, or one
// slack_auth.py writes that keeps the same rules, refuses with slack_credentials_invalid.
func DecodeCredentials(raw []byte) (*Credentials, error) {
	invalid := refuse("slack_credentials_invalid")
	if len(raw) > MaxDocumentBytes {
		return nil, invalid
	}
	value, err := strictjson.LoadsText(string(raw))
	document, isObject := object(value)
	if err != nil || !isObject || len(document.Keys) != len(documentKeys) {
		return nil, invalid
	}
	for _, key := range documentKeys {
		if _, present := document.Get(key); !present {
			return nil, invalid
		}
	}
	member := func(key string) any { value, _ := document.Get(key); return value }
	textOf := func(value any, nullable bool) (string, bool) {
		if value == nil {
			return "", nullable
		}
		s, isText := value.(string)
		return s, isText && s != ""
	}
	timeOf := func(value any, nullable bool) (time.Time, bool) {
		s, isText := textOf(value, nullable)
		if !isText || s == "" {
			return time.Time{}, isText
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		return t.UTC(), err == nil && stamp(t) == s
	}
	scopes := func(value any) ([]string, bool) {
		items, isList := value.([]any)
		var out []string
		for _, item := range items {
			s, isText := item.(string)
			if !isText {
				return nil, false
			}
			out = append(out, s)
		}
		return out, isList
	}
	c := &Credentials{}
	identity, isIdentity := object(member("identity"))
	if !isIdentity || len(identity.Keys) != 4 {
		return nil, invalid
	}
	idText := func(key string, nullable bool) (string, bool) {
		value, present := identity.Get(key)
		s, valid := textOf(value, nullable)
		return s, present && valid
	}
	var checks [16]bool
	c.Identity.AppID, checks[0] = idText("app_id", false)
	c.Identity.TeamID, checks[1] = idText("team_id", false)
	c.Identity.UserID, checks[2] = idText("user_id", false)
	c.Identity.EnterpriseID, checks[3] = idText("enterprise_id", true)
	c.ClientID, checks[4] = textOf(member("client_id"), false)
	c.RedirectURI, checks[5] = textOf(member("redirect_uri"), false)
	c.WorkspaceOrigin, checks[6] = textOf(member("workspace_origin"), true)
	c.RequestedScopes, checks[7] = scopes(member("requested_scopes"))
	c.GrantedScopes, checks[8] = scopes(member("granted_scopes"))
	c.State, checks[9] = textOf(member("credential_state"), false)
	access, accessOK := textOf(member("access_token"), false)
	refresh, refreshOK := textOf(member("refresh_token"), true)
	c.AccessToken, c.RefreshToken, checks[10] = NewSecret(access), NewSecret(refresh), accessOK && refreshOK
	c.AccessObtainedAt, checks[11] = timeOf(member("access_obtained_at"), false)
	c.AccessExpiresAt, checks[12] = timeOf(member("access_expires_at"), true)
	c.RefreshObtainedAt, checks[13] = timeOf(member("refresh_obtained_at"), true)
	c.LastVerifiedAt, checks[14] = timeOf(member("last_verified_at"), true)
	_, configured := callbackPort(c.RedirectURI)
	checks[15] = isInt(member("schema_version"), 1) && member("provider") == any("slack") && configured
	for _, passed := range checks {
		if !passed {
			return nil, invalid
		}
	}
	if !c.valid() {
		return nil, invalid
	}
	return c, nil
}
