package slack

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// withScopes saves the bench's sign-in with these scopes requested and granted.
func (b *bench) withScopes(scopes ...string) {
	b.saved.saved.RequestedScopes, b.saved.saved.GrantedScopes = scopes, scopes
}

// profileAnswer is users.info's answer for one invented user. An empty email leaves the profile without one.
func profileAnswer(user, team, email string) reply {
	shown := strictjson.NewObject("real_name", "An invented name")
	if email != "" {
		shown.Set("email", email)
	}
	text, _ := strictjson.Dumps(strictjson.NewObject("ok", true, "user", strictjson.NewObject("id", user,
		"team_id", team, "deleted", false, "is_bot", false, "profile", shown)), strictjson.DumpOptions{Compact: true})
	return ok(text)
}

const otherUser = "U23456789"

func twoAuthors() []reply {
	return []reply{page(msg(parentTS, `"user":"`+otherUser+`"`), msg(replyTS),
		msg("1700000000.000003", `"subtype":"bot_message"`, `"bot_id":"BB12033"`, "-user"))}
}

func TestProfilesAreReadForEachAuthorWithBothScopes(t *testing.T) {
	b := newBench(t)
	b.withScopes("channels:history", "users:read", "users:read.email")
	// The thread's own team, then an author of another team, as in a shared channel. Users are asked for
	// in ID order, whatever order they wrote in.
	b.slack.profiles = []reply{profileAnswer("U12345678", "T12345678", "morgan@seller.example"),
		profileAnswer(otherUser, "T87654321", "dana@buyer.example")}
	read := b.reads(twoAuthors())
	if got := dump(t, []any{read.Members[0], read.Members[1]}); got != `[{"id":"mem-U12345678","account_id":"acc-001","user_id":"U12345678",`+
		`"team_id":"T12345678","home":true,"email":"morgan@seller.example","profile":"read","missing_scopes":[]},`+
		`{"id":"mem-U23456789","account_id":"acc-001","user_id":"U23456789","team_id":"T87654321","home":false,`+
		`"email":"dana@buyer.example","profile":"read","missing_scopes":[]}]` {
		t.Fatalf("members %s", got)
	}
	var asked []string
	for _, request := range b.slack.sent {
		if request.path == "/api/users.info" {
			if request.method != "GET" || request.header.Get("Authorization") != "Bearer "+accessToken {
				t.Fatalf("request %v", request)
			}
			asked = append(asked, request.query)
		}
	}
	if strings.Join(asked, " ") != "user=U12345678 user=U23456789" {
		t.Fatalf("asked %v", asked)
	}
	if report := b.last(); report.ProfileCalls != 2 || report.APICalls != 1 {
		t.Fatalf("report %+v", report)
	}
	// A card reads the members with their record_ref; the messages are as ReadThread gives them.
	b.slack.replies = twoAuthors()
	b.slack.profiles = []reply{profileAnswer("U12345678", "T12345678", ""), profileAnswer(otherUser, "T87654321", "")}
	messages, members, snapshot, err := b.reader.ReadThreadWithMembers(context.Background(), accountID)
	if err != nil || len(messages) != 3 || len(members) != 2 || snapshot == nil {
		t.Fatal(err)
	}
	if ref := member(t, members[1], "record_ref"); ref != "slack/members/mem-"+otherUser {
		t.Fatalf("record_ref %v", ref)
	}
	if member(t, members[0], "email") != nil {
		t.Fatal("a profile with no email gives none")
	}
}

func TestAMissingScopeReadsLessAndNeverFailsTheRead(t *testing.T) {
	b := newBench(t)
	b.withScopes("channels:history", "users:read")
	// Without users:read.email an email in the answer is not kept.
	b.slack.profiles = []reply{profileAnswer("U12345678", "T12345678", "morgan@seller.example"),
		profileAnswer(otherUser, "T12345678", "dana@buyer.example")}
	read := b.reads(twoAuthors())
	for _, m := range read.Members {
		if member(t, m, "email") != nil || member(t, m, "profile") != "read" ||
			dump(t, member(t, m, "missing_scopes")) != `["users:read.email"]` || member(t, m, "home") != true {
			t.Fatalf("member %s", dump(t, m))
		}
	}

	b = newBench(t)
	read = b.reads(twoAuthors())
	if len(read.Members) != 2 || b.last().ProfileCalls != 0 {
		t.Fatalf("%d members, %d profile calls", len(read.Members), b.last().ProfileCalls)
	}
	for _, m := range read.Members {
		if member(t, m, "profile") != "missing_scope" || member(t, m, "team_id") != nil || member(t, m, "home") != false ||
			dump(t, member(t, m, "missing_scopes")) != `["users:read","users:read.email"]` {
			t.Fatalf("member %s", dump(t, m))
		}
	}
}

func TestProfileRequestsHaveACapOfTheirOwn(t *testing.T) {
	b := newBench(t)
	b.withScopes("channels:history", "users:read")
	rows := []string{msg(parentTS, `"user":"U00000000"`)}
	b.slack.profiles = []reply{profileAnswer("U00000000", "T12345678", "")}
	for i := 1; i <= maxProfileRequests; i++ {
		user := fmt.Sprintf("U%08d", i)
		rows = append(rows, msg(fmt.Sprintf("1700000000.%06d", i+1), `"user":"`+user+`"`))
		b.slack.profiles = append(b.slack.profiles, profileAnswer(user, "T12345678", ""))
	}
	read := b.reads([]reply{page(rows...)})
	if len(read.Members) != maxProfileRequests+1 || b.last().ProfileCalls != maxProfileRequests {
		t.Fatalf("%d members, %d profile calls", len(read.Members), b.last().ProfileCalls)
	}
	if last := read.Members[maxProfileRequests]; member(t, last, "profile") != "over_limit" || member(t, last, "team_id") != nil {
		t.Fatalf("last member %s", dump(t, last))
	}
}

func TestAProfileAnswerThatIsNotTheUserRefusesTheRead(t *testing.T) {
	answers := map[string]reply{
		"another user":         profileAnswer(otherUser, "T12345678", ""),
		"no team":              ok(`{"ok":true,"user":{"id":"U12345678"}}`),
		"a team of no form":    profileAnswer("U12345678", "team-1", ""),
		"no user":              ok(`{"ok":true}`),
		"a flag of no type":    ok(`{"ok":true,"user":{"id":"U12345678","team_id":"T12345678","deleted":"no"}}`),
		"an email of no form":  profileAnswer("U12345678", "T12345678", "two words@seller.example"),
		"a profile of no type": ok(`{"ok":true,"user":{"id":"U12345678","team_id":"T12345678","profile":[]}}`),
	}
	for name, answer := range answers {
		b := newBench(t)
		b.withScopes("channels:history", "users:read", "users:read.email")
		b.slack.profiles = []reply{answer}
		b.refuses("slack_response_invalid", []reply{page(msg(parentTS))})
		if b.last().Phase != "profiles" {
			t.Errorf("%s: phase %q", name, b.last().Phase)
		}
	}
	b := newBench(t)
	b.withScopes("channels:history", "users:read")
	b.slack.profiles = []reply{ok(`{"ok":false,"error":"missing_scope"}`)}
	b.refuses("slack_forbidden", []reply{page(msg(parentTS))})
}

func TestAProfileRequestRetriesOnceAfterA429(t *testing.T) {
	b := newBench(t)
	b.withScopes("channels:history", "users:read")
	b.slack.profiles = []reply{status(429, `{"ok":false,"error":"ratelimited"}`, "Retry-After", "1"),
		profileAnswer("U12345678", "T12345678", "")}
	read := b.reads([]reply{page(msg(parentTS))})
	if member(t, read.Members[0], "profile") != "read" || b.last().ProfileCalls != 2 || b.last().RateRetries != 1 {
		t.Fatalf("report %+v", b.last())
	}
	b = newBench(t)
	b.withScopes("channels:history", "users:read")
	limited := status(429, `{"ok":false,"error":"ratelimited"}`, "Retry-After", "1")
	b.slack.profiles = []reply{limited, limited}
	b.refuses("slack_rate_limited", []reply{page(msg(parentTS))})
}

func TestASignInAsksForAHistoryScope(t *testing.T) {
	if _, err := NewConfiguration(testClient, testRedirect, []string{"users:read", "users:read.email"}, ""); codeOf(err) != "slack_invalid_configuration" {
		t.Fatalf("profile scopes alone: %v", codeOf(err))
	}
	cfg, err := NewConfiguration(testClient, testRedirect, []string{"users:read.email", "users:read", "channels:history"}, "")
	if err != nil || strings.Join(cfg.Scopes, ",") != "channels:history,users:read,users:read.email" {
		t.Fatalf("%v %v", cfg.Scopes, err)
	}
}
