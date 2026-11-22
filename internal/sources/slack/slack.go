// Package slack reads one mapped Slack thread for an account through Slack's Web API for
// read_slack_messages, and keeps the saved user sign-in usable during a read.
//
// A read is one operation. It asks the fixed endpoint conversations.replies for the selected thread up to
// the moment the read began, follows cursors to the end, and returns every message or none. Its requests,
// with the one token refresh and the identity check that follows it, share one 20-second deadline, ten HTTP
// attempts and 1 MiB of response bytes. Files, attachments and blocks are counted and left out. With
// users:read, the read then asks users.info for each distinct author, at most 50 requests under the same
// deadline and bytes (profiles.go).
//
// The reader holds no storage of its own. Mappings gives the thread selected for an account, and Connection
// the saved sign-in under its lock. Each is called under a context that ends at the read's deadline. A
// refusal is an *Error holding one fixed code: no provider text, no identifier and no credential reaches an
// error, a Report or a result, and an answer that holds a token of the sign-in is refused whole. A read
// stops when its context is cancelled. A Reader keeps no state between reads, so reads may run at once when
// its stores allow it.
package slack

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The limits table of docs/sources.md.
const (
	operationTime        = 20 * time.Second
	maxRequests          = 10
	maxResponseBytes     = 1 << 20   // across every attempt of one read
	maxAuthResponseBytes = 1 << 16   // one authentication response
	maxRows              = 100       // message rows, duplicates included
	maxTextBytes         = 16 << 10  // one message's text
	maxTotalTextBytes    = 64 << 10  // all unique message text
	maxOutputBytes       = 256 << 10 // the result as ASCII JSON
	maxCursorBytes       = 2048
	maxRateDelay         = 5 // seconds a 429 may ask the one retry to wait
)

const repliesURL = "https://slack.com/api/conversations.replies"

// supportedSubtypes are the message subtypes a read accepts. A message with no subtype is accepted too.
var supportedSubtypes = []string{"bot_message", "me_message", "file_share", "thread_broadcast", "tombstone"}

// exitCodes gives each reason a read can stop for the exit code docs/sources.md lists for it.
var exitCodes = map[string]int{
	"slack_selection_invalid": 2, "slack_mapping_missing": 3, "slack_mapping_conflict": 3,
	"slack_source_mismatch": 3, "slack_mapping_invalid": 4, "slack_snapshot_invalid": 4,
	"slack_thread_unavailable": 3, "slack_forbidden": 3, "slack_thread_changed": 3,
	"slack_thread_incomplete": 3, "slack_resource_limit": 3, "slack_output_too_large": 3,
	"slack_clock_invalid": 3, "slack_response_invalid": 5, "slack_rate_limited": 5,
	"slack_timeout": 5, "slack_unavailable": 5, "slack_reconnect_required": 3,
	"slack_storage_failed": 1, "slack_invalid_configuration": 2, "slack_connection_busy": 1,
	"slack_private_state_invalid": 3, "slack_credentials_invalid": 4, "slack_not_connected": 3,
	"slack_token_type_unsupported": 3, "slack_oauth_rejected": 3,
}

// Error is a refused read: one fixed code and nothing else.
type Error struct {
	Code string
	// Returned says what became of a grant a sign-in did not keep: "revoked" when Slack confirmed that the
	// grant's token is revoked, "unconfirmed" when it did not, and "" when no grant was given back: none
	// was obtained, its token does not expire, or it is the saved sign-in's own.
	Returned string
	// retry is the whole seconds a 429 asked to wait, plus one. Zero means it named no usable delay.
	retry int
}

func (e *Error) Error() string { return e.Code }

// ExitCode is the exit code of a command that stops for this reason, or 0 for a code a read never gives.
func (e *Error) ExitCode() int {
	if exit, known := exitCodes[e.Code]; known {
		return exit
	}
	return signInExits[e.Code]
}

func refuse(code string) error { return &Error{Code: code} }

// AuditFailed is a sign-in request whose receipt could not be committed, so the request was not made, or
// could not be finished, so what it gave is not used.
type AuditFailed struct{ Err error }

func (a *AuditFailed) Error() string { return "the receipt of a Slack sign-in request failed" }
func (a *AuditFailed) Unwrap() error { return a.Err }

// audited runs one sign-in request under Audit, when it is set.
func (r *Reader) audited(ctx context.Context, step string, request func(context.Context) error) error {
	if r.Audit == nil {
		return request(ctx)
	}
	var ran bool
	var own error
	err := r.Audit(ctx, step, func(ctx context.Context) error {
		ran, own = true, request(ctx)
		return own
	})
	if err != nil && (!ran || own == nil) {
		return &AuditFailed{err}
	}
	return err
}

// code is the fixed code of a refusal, or "" for any other error, such as the caller's cancel.
func code(err error) string {
	var refused *Error
	if errors.As(err, &refused) {
		return refused.Code
	}
	return ""
}

// Clock is the two clocks a read uses. A nil reading is the system's.
type Clock struct {
	Now       func() time.Time     // the wall clock: the read's cutoff, and token expiry
	Monotonic func() time.Duration // a steady clock from any origin: the deadline
}

var processStart = time.Now()

// now is the wall clock in UTC to the microsecond, the resolution every saved time has.
func (c Clock) now() time.Time {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	return now().UTC().Truncate(time.Microsecond)
}

func (c Clock) monotonic() time.Duration {
	if c.Monotonic != nil {
		return c.Monotonic()
	}
	return time.Since(processStart)
}

// Mappings is the store of the threads selected for accounts, one thread per account.
type Mappings interface {
	// Mapping gives the entry saved for the account, as `debug slack map` saved it and checked against the
	// folder's CRM and Slack connections. With no thread selected for the account it refuses with the code
	// slack_mapping_missing. A failure that is not an *Error reads as slack_storage_failed. ctx ends when
	// the read's time does, and a store that waits stops waiting then.
	Mapping(ctx context.Context, accountID string) (*strictjson.Object, error)
}

// Reader reads the thread mapped to an account. Making one performs no I/O.
type Reader struct {
	Mappings   Mappings
	Connection Connection
	// Binding names the Slack identity the folder is connected to, as IdentityBinding gives it. A mapping or
	// a saved sign-in for any other identity is refused.
	Binding *strictjson.Object
	// Transport sends the requests. Nil is the package's own: verified TLS, no proxy from the environment,
	// and HTTP/1.1 with one connection a request.
	Transport http.RoundTripper
	Clock     Clock
	// OnRead, when set, is given the report of every read, passed or failed.
	OnRead func(Report)
	// Audit, when set, runs each request a sign-in command makes to Slack (Connect, Status and Disconnect):
	// the code exchange, a refresh, the identity check and the revocation. It is given the step's name and
	// the request, and runs the request only once the request's receipt is committed. A failure of the
	// receipt itself comes back as an *AuditFailed.
	Audit func(ctx context.Context, step string, request func(context.Context) error) error
	// Confirm is asked, after Connect has checked a new approval's identity with Slack and before it saves
	// anything, whether the Slack user and workspace that approved are the operator's own. Only a yes saves
	// the sign-in.
	Confirm func(ctx context.Context, identity Identity, workspace string) (bool, error)

	sleep func(context.Context, time.Duration) error // the wait before the retry after a 429; nil sleeps
	idle  time.Duration                              // how long a body may stop arriving; zero is readTimeout
}

// ThreadRead is one complete read of a thread.
type ThreadRead struct {
	// Records are the messages, oldest first: id, account_id, channel, author, sent_at, body and
	// slack_message, which holds the message's Slack timestamp, subtype, author IDs, edit mark and the
	// counts of what was left out.
	Records []*strictjson.Object
	// Snapshot describes the read the messages are checked against: the mapping, the cutoff, the counts and
	// a digest of every message.
	Snapshot *strictjson.Object
	// Members are the thread's authors, one for each distinct user_id in user ID order: id ("mem-" and the
	// user ID), account_id, user_id, team_id, home (whether team_id is the sign-in's own team), email,
	// profile ("read", "missing_scope" or "over_limit") and missing_scopes, the ones of users:read and
	// users:read.email the sign-in lacks. They are not in the snapshot.
	Members []*strictjson.Object
	// Scopes are the scopes held by each sign-in that returned a page of this read, sorted. They are private
	// and never part of a tool result.
	Scopes []string
}

// Report is what one read did, in counts and durations. It holds no source value.
type Report struct {
	Code  string // "ok", the code of the refusal, or "cancelled" when the caller cancelled the read
	Phase string // where a failed read stopped: "mapping", "credential_binding" or "thread_read"

	MappingValidated, CredentialBindingValidated, PaginationComplete bool

	Duration, MappingTime, CredentialTime, ThreadTime time.Duration

	APICalls, AuthCalls, MessageCalls                      int
	ProfileCalls                                           int // users.info requests, which APICalls leaves out
	ResponseBytes, AuthResponseBytes, MessageResponseBytes int
	Pages, RawRows, MessagesSeen, ReturnedMessages         int
	Duplicates, RefreshAttempts, TokenRetries, RateRetries int
}

// progress is the state of one read that its report is made from.
type progress struct {
	clock        Clock
	phase        string
	phaseStarted time.Duration
	spent        map[string]time.Duration

	mappingOK, credentialOK, complete bool
	pages, rawRows, duplicates, seen  int
}

func (p *progress) enter(phase string) {
	now := p.clock.monotonic()
	p.spent[p.phase] += max(0, now-p.phaseStarted)
	p.phase, p.phaseStarted = phase, now
}

// ReadSlackMessages reads the thread mapped to the account. A cancelled ctx stops the request in flight and
// the read returns ctx's error; every other failure is an *Error.
func (r *Reader) ReadSlackMessages(ctx context.Context, accountID string) (*ThreadRead, error) {
	b := newBudget(r.Clock)
	p := &progress{clock: r.Clock, phase: "mapping", phaseStarted: r.Clock.monotonic(),
		spent: map[string]time.Duration{}}
	read, err := r.read(ctx, b, p, accountID)
	err = readError(ctx, err)
	if r.OnRead != nil {
		p.enter(p.phase)
		report := Report{Code: "ok", MappingValidated: p.mappingOK, CredentialBindingValidated: p.credentialOK,
			PaginationComplete: p.complete, Duration: max(0, r.Clock.monotonic()-b.started),
			MappingTime: p.spent["mapping"], CredentialTime: p.spent["credential_binding"],
			ThreadTime: p.spent["thread_read"], APICalls: b.httpAttempts, AuthCalls: b.authAttempts,
			MessageCalls: b.messageAttempts, ProfileCalls: b.profileAttempts, ResponseBytes: b.responseBytes, AuthResponseBytes: b.authBytes,
			MessageResponseBytes: b.messageBytes, Pages: p.pages, RawRows: p.rawRows, MessagesSeen: p.seen,
			Duplicates: p.duplicates, RefreshAttempts: b.refreshAttempts, TokenRetries: flag(b.tokenRetried),
			RateRetries: flag(b.rateRetried)}
		if err != nil {
			report.Phase = p.phase
			if report.Code = code(err); report.Code == "" {
				report.Code = "cancelled"
			}
		} else {
			report.ReturnedMessages = len(read.Records)
		}
		r.OnRead(report)
	}
	if err != nil {
		return nil, err
	}
	return read, nil
}

// ReadThread is ReadSlackMessages as the read_slack_messages tool body gives it to a card: each record with
// its record_ref, and the snapshot.
func (r *Reader) ReadThread(ctx context.Context, accountID string) (records []*strictjson.Object, snapshot *strictjson.Object, err error) {
	read, err := r.ReadSlackMessages(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	return withRefs(read.Records, "messages"), read.Snapshot, nil
}

// ReadThreadWithMembers is ReadThread with the thread's authors as a card reads them: the members carry
// record_ref "slack/members/<id>".
func (r *Reader) ReadThreadWithMembers(ctx context.Context, accountID string) (messages, members []*strictjson.Object,
	snapshot *strictjson.Object, err error) {
	read, err := r.ReadSlackMessages(ctx, accountID)
	if err != nil {
		return nil, nil, nil, err
	}
	return withRefs(read.Records, "messages"), withRefs(read.Members, "members"), read.Snapshot, nil
}

func withRefs(records []*strictjson.Object, table string) []*strictjson.Object {
	out := make([]*strictjson.Object, len(records))
	for i, record := range records {
		id, _ := record.Get("id")
		out[i] = record.Copy()
		out[i].Set("record_ref", "slack/"+table+"/"+id.(string))
	}
	return out
}

func flag(set bool) int {
	if set {
		return 1
	}
	return 0
}

// readError is the error a read returns. The caller's cancel is returned as it is. A refusal that only
// the sign-in gives is renamed as the reader names it. Every other error leaves as one fixed code, without
// whatever a store or a transport wrapped around it.
func readError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	switch reason := code(err); reason {
	case "slack_scope_mismatch":
		return refuse("slack_forbidden")
	case "slack_identity_mismatch":
		return refuse("slack_source_mismatch")
	case "":
		return refuse("slack_unavailable")
	default:
		if _, known := exitCodes[reason]; known {
			return refuse(reason)
		}
		return refuse("slack_reconnect_required")
	}
}

// selected is the mapping of one account and the two members a read uses.
type selected struct {
	mapping         *strictjson.Object
	channel, parent string
}

// selection loads the account's mapping. The store has checked it; the reader refuses one that is for
// another account or another Slack identity, or that it could not use.
func (r *Reader) selection(ctx context.Context, b *budget, accountID string) (selected, error) {
	var found *strictjson.Object
	err := b.store(ctx, func(limited context.Context) (err error) {
		found, err = r.Mappings.Mapping(limited, accountID)
		return err
	})
	if err != nil {
		reason := code(err)
		if reason == "" {
			return selected{}, refuse("slack_storage_failed")
		}
		if _, known := exitCodes[reason]; !known {
			return selected{}, refuse("slack_mapping_invalid")
		}
		return selected{}, refuse(reason)
	}
	if found == nil {
		return selected{}, refuse("slack_mapping_missing")
	}
	mapping := strictjson.Clone(found).(*strictjson.Object)
	if _, err := canonical(mapping); err != nil {
		return selected{}, refuse("slack_mapping_invalid")
	}
	if binding, _ := mapping.Get("slack_binding"); !sameJSON(binding, r.Binding) {
		return selected{}, refuse("slack_source_mismatch")
	}
	account, _ := mapping.Get("account_id")
	channel, isChannel := text(mapping, "channel_id")
	parent, isParent := text(mapping, "thread_ts")
	if account != any(accountID) || !isChannel || !isParent {
		return selected{}, refuse("slack_mapping_invalid")
	}
	return selected{mapping, channel, parent}, nil
}

func (r *Reader) read(ctx context.Context, b *budget, p *progress, accountID string) (*ThreadRead, error) {
	chosen, err := r.selection(ctx, b, accountID)
	if err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	// The read asks for nothing newer than its own start, so a thread that is still growing reads whole.
	cutoff, cutoffMicros, err := wallTimestamp(b.startedAt)
	if err != nil {
		return nil, err
	}
	parentMicros, valid := timestamp(chosen.parent)
	if !valid {
		return nil, refuse("slack_response_invalid")
	}
	if parentMicros > cutoffMicros {
		return nil, refuse("slack_thread_unavailable")
	}
	p.mappingOK = true

	held, err := r.acquire(ctx, b, p, nil)
	if err != nil {
		return nil, err
	}
	thread := thread{channel: chosen.channel, parent: chosen.parent, parentMicros: parentMicros,
		cutoffMicros: cutoffMicros}
	var (
		cursor      *string
		seenCursors = map[string]bool{}
		messages    []*message
		byStamp     = map[string]*message{}
		textBytes   int
		scopes      = map[string]bool{}
	)
	for {
		if p.rawRows >= maxRows {
			return nil, refuse("slack_resource_limit")
		}
		limit := maxRows - p.rawRows
		document, err := r.replies(ctx, b, held, chosen.channel, chosen.parent, cutoff, cursor, limit)
		if errors.Is(err, errTokenExpired) {
			// Only Slack's exact token_expired answer earns the one retry, with the one refresh.
			if b.tokenRetried {
				if err := r.reject(ctx, b, held); err != nil {
					return nil, err
				}
				return nil, refuse("slack_reconnect_required")
			}
			b.tokenRetried = true
			if held, err = r.acquire(ctx, b, p, held); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			var refused *Error
			if !errors.As(err, &refused) {
				return nil, err
			}
			if refused.Code == "slack_reconnect_required" {
				if err := r.reject(ctx, b, held); err != nil {
					return nil, err
				}
			}
			delay := time.Duration(refused.retry-1) * time.Second
			if refused.Code != "slack_rate_limited" || refused.retry == 0 || refused.retry-1 > maxRateDelay ||
				b.rateRetried || b.httpAttempts >= maxRequests {
				return nil, err
			}
			left, expired := b.remaining()
			if expired != nil {
				return nil, expired
			}
			if left <= delay {
				return nil, err
			}
			b.rateRetried = true
			if err := r.wait(ctx, delay); err != nil {
				return nil, err
			}
			if _, err := b.remaining(); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := b.remaining(); err != nil {
			return nil, err
		}
		listed, _ := document.Get("messages")
		rows, isList := listed.([]any)
		if !isList {
			return nil, refuse("slack_response_invalid")
		}
		p.rawRows += len(rows)
		if len(rows) > limit {
			return nil, refuse("slack_resource_limit")
		}
		p.pages++
		if p.pages == 1 {
			// The first page holds the selected parent exactly once. A reply selected as the parent does not.
			parents := 0
			for _, row := range rows {
				if o, isObject := row.(*strictjson.Object); isObject && o != nil {
					if stamp, _ := o.Get("ts"); stamp == any(chosen.parent) {
						parents++
					}
				}
			}
			if parents != 1 {
				return nil, refuse("slack_thread_incomplete")
			}
		}
		for _, row := range rows {
			m, err := thread.normalize(row)
			if err != nil {
				return nil, err
			}
			if earlier, again := byStamp[m.stamp]; again {
				if !sameJSON(earlier.manifest, m.manifest) {
					return nil, refuse("slack_thread_changed")
				}
				p.duplicates++
				continue
			}
			byStamp[m.stamp] = m
			messages = append(messages, m)
			p.seen = len(messages)
			if textBytes += len(m.text); textBytes > maxTotalTextBytes {
				return nil, refuse("slack_output_too_large")
			}
		}
		// Only a sign-in that returned a page of the thread adds its scopes. One that expired before
		// returning anything contributed no text.
		for _, scope := range held.Scopes {
			scopes[scope] = true
		}
		following, err := nextCursor(document, cursor)
		if err != nil {
			return nil, err
		}
		if following == nil {
			p.complete = true
			break
		}
		if seenCursors[*following] {
			return nil, refuse("slack_thread_incomplete")
		}
		seenCursors[*following] = true
		cursor = following
	}

	fetchedAt := r.Clock.now()
	if fetchedAt.Before(b.startedAt) {
		return nil, refuse("slack_clock_invalid")
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].micros < messages[j].micros })
	counts := strictjson.NewObject("http_attempts", b.httpAttempts, "auth_attempts", b.authAttempts,
		"message_attempts", b.messageAttempts, "response_bytes", b.responseBytes, "pages", p.pages,
		"raw_rows", p.rawRows, "messages", len(messages), "duplicates", p.duplicates,
		"refresh_attempts", b.refreshAttempts, "token_retries", flag(b.tokenRetried),
		"rate_retries", flag(b.rateRetried))
	for _, kind := range omitted {
		total := 0
		for _, m := range messages {
			total += m.omissions[kind]
		}
		counts.Set("omitted_"+kind, total)
	}
	digests := make([]any, len(messages))
	for i, m := range messages {
		digests[i] = digest(m.manifest)
	}
	snapshot := strictjson.NewObject("schema_version", 1, "source", "slack", "mapping", chosen.mapping,
		"cutoff_ts", cutoff, "started_at", stamp(b.startedAt), "fetched_at", stamp(fetchedAt),
		"pagination_complete", true, "counts", counts, "manifest_sha256", digests)
	sealed := digest(snapshot)
	snapshot.Set("snapshot_sha256", sealed)
	records := make([]*strictjson.Object, len(messages))
	for i, m := range messages {
		records[i] = m.record(sealed, accountID, chosen.channel)
	}
	result := make([]any, len(records))
	for i, record := range withRefs(records, "messages") {
		result[i] = record
	}
	output, _ := canonical(strictjson.NewObject("source", "slack", "records", result, "untrusted_data", true,
		"thread_snapshot", snapshot))
	if len(output) > maxOutputBytes {
		return nil, refuse("slack_output_too_large")
	}
	// The authors' profiles are read once the thread is whole, under the sign-in that read its last page.
	members, err := r.members(ctx, b, p, held, authors(messages), accountID)
	if err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	granted := make([]string, 0, len(scopes))
	for scope := range scopes {
		granted = append(granted, scope)
	}
	sort.Strings(granted)
	return &ThreadRead{Records: records, Snapshot: snapshot, Members: members, Scopes: granted}, nil
}

// wait is the pause a 429 asked for before the one retry. The caller has checked that it ends before the
// deadline.
func (r *Reader) wait(ctx context.Context, delay time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return refuse("slack_timeout")
	}
}
