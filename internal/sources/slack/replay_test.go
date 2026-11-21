package slack

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// upstream is the invented Slack the oracle's threads were read from, as the removed
// Python suite served them: one thread in pages of a fixed size, each page the bytes Python's json.dumps gave. It checks every
// request against the one the Python reader made at that point.
type upstream struct {
	t       *testing.T
	rows    []any
	channel string
	cutoff  string
	page    int
	served  int // requests answered
	next    int // the row the next request starts at
}

func (u *upstream) RoundTrip(request *http.Request) (*http.Response, error) {
	t := u.t
	// The Python reader asked for 100 rows less those it had. The number is written here, not taken from
	// the reader under test, so a change to the reader's own limit shows as a different request.
	const asked = 100
	want := fmt.Sprintf("channel=%s&ts=%s&latest=%s&inclusive=true&limit=%d", u.channel,
		member(t, u.rows[0], "ts"), u.cutoff, asked-u.next)
	if u.next > 0 {
		want += fmt.Sprintf("&cursor=page-%d", u.next)
	}
	if request.Method != "GET" || request.URL.Scheme+"://"+request.URL.Host+request.URL.Path != repliesURL ||
		request.URL.RawQuery != want {
		t.Errorf("request %d: %s %s, want the query %s", u.served, request.Method, request.URL, want)
	}
	if got := request.Header; len(got) != 3 || got.Get("Authorization") != "Bearer "+accessToken ||
		got.Get("Accept") != "application/json" || got.Get("Accept-Encoding") != "identity" {
		t.Errorf("request %d: headers %v", u.served, shown(got))
	}
	if request.Body != nil {
		t.Errorf("request %d has a body", u.served)
	}
	cutoff, _ := timestamp(u.cutoff)
	var sent []any
	for _, row := range u.rows {
		if at, valid := timestamp(member(t, row, "ts")); valid && at <= cutoff {
			sent = append(sent, row)
		}
	}
	end := min(u.next+min(u.page, asked-u.next), len(sent))
	more, cursor := end < len(sent), ""
	if more {
		cursor = fmt.Sprintf("page-%d", end)
	}
	body := dump(t, strictjson.NewObject("ok", true, "messages", sent[u.next:end], "has_more", more,
		"response_metadata", strictjson.NewObject("next_cursor", cursor)))
	u.served++
	u.next = end
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {fmt.Sprint(len(body))}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

// TestRecordedThreadsReplay reads every thread the card fixtures record through the reader, offline. The
// requests are the ones the Python reader made, in its order, and the messages and the snapshot are the
// ones the row's "read" holds, which the card tests serve as the thread: digests and response byte counts
// included.
func TestRecordedThreadsReplay(t *testing.T) {
	// The recorder's reader clock stood at 2027-01-16T00:00:00Z, and its sign-in did not rotate.
	wall := time.Date(2027, 1, 16, 0, 0, 0, 0, time.UTC)
	bound := binding(t, testIdentity)
	threads, exchanges, unmapped := 0, 0, 0
	for family, rowsKey := range map[string]string{"books_slack": "thread", "slack_threads": "rows"} {
		for number, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
			row := parse(t, string(line))
			key := fmt.Sprintf("%s %s", family, member(t, row, "key"))
			accountID := member(t, row, "account_id").(string)
			input := member(t, row, "input")
			recorded := member(t, input, "read")
			if got := member(t, recorded, "binding"); !sameJSON(got, bound) {
				t.Fatalf("%s: the recorded folder is connected to another identity", key)
			}
			clock := &testClock{wall: wall, tick: time.Duration(number) * time.Second}
			saved := &connection{saved: signIn(wall, false, false)}
			reader := &Reader{Connection: saved, Binding: bound, Clock: clock.clock()}

			served, isThread := member(t, input, rowsKey).([]any)
			snapshot, isRead := member(t, recorded, "snapshot").(*strictjson.Object)
			if isThread != (isRead && snapshot != nil) {
				t.Fatalf("%s: the recorded thread and snapshot disagree", key)
			}
			if !isThread {
				// No thread was mapped to this account: the read stops before any credential or request.
				reader.Mappings = mappings{}
				reader.Transport = &script{t: t}
				if _, _, err := reader.ReadThread(context.Background(), accountID); codeOf(err) != "slack_mapping_missing" {
					t.Errorf("%s: %v, want slack_mapping_missing", key, err)
				}
				if saved.holds != 0 {
					t.Errorf("%s: an unmapped account read the sign-in", key)
				}
				unmapped++
				continue
			}
			page, _ := member(t, input, "page").(*big.Int)
			slack := &upstream{t: t, rows: served, page: int(page.Int64()),
				channel: member(t, snapshot, "mapping", "channel_id").(string),
				cutoff:  member(t, input, "cutoff").(string)}
			reader.Mappings = mappings{accountID: member(t, snapshot, "mapping").(*strictjson.Object)}
			reader.Transport = slack
			read, err := reader.ReadSlackMessages(context.Background(), accountID)
			if err != nil {
				t.Errorf("%s: %v", key, err)
				continue
			}
			if got, want := dump(t, read.Snapshot), dump(t, snapshot); got != want {
				t.Errorf("%s: snapshot\n got %s\nwant %s", key, got, want)
			}
			want := member(t, recorded, "messages").([]any)
			got := make([]any, len(read.Records))
			for i, record := range withRefs(read.Records, "messages") {
				got[i] = record
			}
			if got, want := dump(t, got), dump(t, want); got != want {
				t.Errorf("%s: records\n got %s\nwant %s", key, got, want)
			}
			if attempts, _ := integer(member(t, snapshot, "counts", "http_attempts")); slack.served != attempts {
				t.Errorf("%s: %d requests, and the recorded read made %d", key, slack.served, attempts)
			}
			if len(saved.saves) != 0 || len(read.Scopes) != 1 || read.Scopes[0] != "channels:history" {
				t.Errorf("%s: saves %d, scopes %v", key, len(saved.saves), read.Scopes)
			}
			threads++
			exchanges += slack.served
		}
	}
	if threads != 104 || exchanges != 177 || unmapped != 10 {
		t.Fatalf("replayed %d threads in %d exchanges, with %d unmapped accounts; the oracle holds 104, 177 and 10",
			threads, exchanges, unmapped)
	}
}
