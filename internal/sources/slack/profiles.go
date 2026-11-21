package slack

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Author profiles. After a complete thread read, the reader asks users.info for each distinct author of the
// thread, under the same sign-in and deadline, so a card can place an author by the Slack identity and
// profile email rather than by a display name. A sign-in without users:read reads no profile, and one without
// users:read.email reads no email: the thread is read as before, and each author's member record says which
// scopes were missing.

// The scopes a profile read needs, in the kept order.
const (
	scopeUsersRead  = "users:read"
	scopeUsersEmail = "users:read.email"
)

// maxProfileRequests is how many users.info requests one read may make. Authors past it are not asked for.
// Profile requests have this cap of their own and do not count toward maxRequests, which bounds the thread
// read and the sign-in.
const maxProfileRequests = 50

// maxEmailBytes is the longest profile email that is kept.
const maxEmailBytes = 254

const usersInfoURL = "https://slack.com/api/users.info"

// The profile states a member record carries.
const (
	profileRead         = "read"
	profileMissingScope = "missing_scope"
	profileOverLimit    = "over_limit"
)

// profile is what users.info said of one author.
type profile struct {
	team  any // "T..." or nil
	email any // a string, or nil
}

// authors are the distinct user IDs of the messages, sorted. A message with no user_id, such as an app's,
// has no author to read.
func authors(messages []*message) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range messages {
		user, _ := m.manifest.Get("user_id")
		id, isText := user.(string)
		if isText && id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// missingScopes are the profile scopes the sign-in lacks, in the kept order.
func missingScopes(held []string) []string {
	missing := []string{}
	for _, scope := range []string{scopeUsersRead, scopeUsersEmail} {
		if !slices.Contains(held, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

// members reads the profile of each author and returns one member record for each, in user ID order. held
// is the sign-in that read the thread; it may be refreshed once more here, under the read's one refresh.
func (r *Reader) members(ctx context.Context, b *budget, p *progress, held *access, users []string,
	accountID string) ([]*strictjson.Object, error) {
	p.enter("profiles")
	missing := missingScopes(held.Scopes)
	readable := !slices.Contains(missing, scopeUsersRead)
	withEmail := !slices.Contains(missing, scopeUsersEmail)
	home := held.Identity.TeamID
	records := make([]*strictjson.Object, 0, len(users))
	for i, user := range users {
		state, found := profileMissingScope, profile{}
		switch {
		case readable && i < maxProfileRequests:
			var err error
			if found, held, err = r.profile(ctx, b, p, held, user, withEmail); err != nil {
				return nil, err
			}
			state = profileRead
		case readable:
			state = profileOverLimit
		}
		listed := make([]any, len(missing))
		for n, scope := range missing {
			listed[n] = scope
		}
		records = append(records, strictjson.NewObject("id", "mem-"+user, "account_id", accountID,
			"user_id", user, "team_id", found.team, "home", found.team != nil && found.team == any(home),
			"email", found.email, "profile", state, "missing_scopes", listed))
	}
	return records, nil
}

// profile asks users.info for one user. A token Slack refuses as expired is refreshed under the read's one
// refresh, and a 429 waits under the read's one rate retry, as a page does. It returns the access that
// answered.
func (r *Reader) profile(ctx context.Context, b *budget, p *progress, held *access, user string,
	withEmail bool) (profile, *access, error) {
	for {
		document, err := r.usersInfo(ctx, b, held, user)
		if errors.Is(err, errTokenExpired) {
			if b.tokenRetried {
				if err := r.reject(ctx, b, held); err != nil {
					return profile{}, nil, err
				}
				return profile{}, nil, refuse("slack_reconnect_required")
			}
			b.tokenRetried = true
			if held, err = r.acquire(ctx, b, p, held); err != nil {
				return profile{}, nil, err
			}
			p.enter("profiles")
			continue
		}
		if err != nil {
			var refused *Error
			if !errors.As(err, &refused) {
				return profile{}, nil, err
			}
			if refused.Code == "slack_reconnect_required" {
				if err := r.reject(ctx, b, held); err != nil {
					return profile{}, nil, err
				}
			}
			delay := time.Duration(refused.retry-1) * time.Second
			if refused.Code != "slack_rate_limited" || refused.retry == 0 || refused.retry-1 > maxRateDelay ||
				b.rateRetried {
				return profile{}, nil, err
			}
			left, expired := b.remaining()
			if expired != nil {
				return profile{}, nil, expired
			}
			if left <= delay {
				return profile{}, nil, err
			}
			b.rateRetried = true
			if err := r.wait(ctx, delay); err != nil {
				return profile{}, nil, err
			}
			continue
		}
		found, err := readProfile(document, user, withEmail)
		return found, held, err
	}
}

// readProfile checks a users.info answer for the user asked for: its ID, its team, and its profile email
// when the sign-in may read one. Anything else, or an answer for another user, refuses the whole read.
func readProfile(document *strictjson.Object, user string, withEmail bool) (profile, error) {
	invalid := refuse("slack_response_invalid")
	listed, _ := document.Get("user")
	item, isObject := object(listed)
	if !isObject {
		return profile{}, invalid
	}
	if id, _ := item.Get("id"); id != any(user) {
		return profile{}, invalid
	}
	team, present := item.Get("team_id")
	if !present || !identifierOf(team, "T", 8) {
		return profile{}, invalid
	}
	for _, flag := range []string{"deleted", "is_bot"} {
		if value, present := item.Get(flag); present {
			if _, isBool := value.(bool); !isBool {
				return profile{}, invalid
			}
		}
	}
	found := profile{team: team}
	if !withEmail {
		return found, nil
	}
	shown, present := item.Get("profile")
	if !present {
		return found, nil
	}
	details, isObject := object(shown)
	if !isObject {
		return profile{}, invalid
	}
	email, present := details.Get("email")
	if !present || email == nil {
		return found, nil
	}
	address, isText := email.(string)
	if !isText || !plainAddress(address) {
		return profile{}, invalid
	}
	found.email = address
	return found, nil
}

// plainAddress is whether text is a plain email address: at most 254 bytes of printable ASCII with no space,
// and exactly one "@" with text on both sides.
func plainAddress(text string) bool {
	if len(text) == 0 || len(text) > maxEmailBytes {
		return false
	}
	at := -1
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c <= ' ' || c > '~' {
			return false
		}
		if c == '@' {
			if at >= 0 {
				return false
			}
			at = i
		}
	}
	return at > 0 && at < len(text)-1
}

// usersInfo makes one users.info request: the fixed endpoint, never a URL the source gave.
func (r *Reader) usersInfo(ctx context.Context, b *budget, held *access, user string) (*strictjson.Object, error) {
	got, err := r.send(ctx, b, "profile", func(ctx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, usersInfoURL+"?user="+url.QueryEscape(user), nil)
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
	return answered(got, b)
}

// answered reads one Web API answer as a page request reads it: a 429 names its delay, a 5xx is
// slack_unavailable, only the exact token_expired 401 leads to a refresh, and any other refusal is named
// by providerErrors.
func answered(got *answer, b *budget) (*strictjson.Object, error) {
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
