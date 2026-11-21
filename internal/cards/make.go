package cards

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Source is the reads a card makes, one per tool: get_account, list_opportunities, read_call_notes and
// read_slack_messages. It returns the source system the records come from and the records with their
// record_ref. ctx carries the caller's cancellation and the card's deadline, and a read returns once it is
// done. demo.Store is one.
type Source interface {
	Read(ctx context.Context, tool, accountID string) (system string, records []*strictjson.Object, err error)
}

// ThreadReader is a Source of live Slack. Its read of read_slack_messages is one mapped thread: the messages,
// and the snapshot of the read that they are checked against. A Maker whose Slack is live reads the thread
// through ReadThread, under the same context as any other read, and puts the snapshot in the card's evidence.
type ThreadReader interface {
	ReadThread(ctx context.Context, accountID string) (records []*strictjson.Object, snapshot *strictjson.Object, err error)
}

// ErrNotSelected is a Source's answer when the account has no selection in that source, such as a Slack
// thread that was never mapped. The read finishes as error/200 and the card states it.
var ErrNotSelected = errors.New("cards: no selection for this account in this source")

// ErrBriefing is a read that failed, so no card is made (briefing.BriefingError).
var ErrBriefing = errors.New("cards: required evidence could not be collected; no brief generated")

// ReadError is ErrBriefing for one read: its tool, and 403 when the read was refused for a scope the card
// cannot go without.
type ReadError struct {
	Tool       string
	HTTPStatus int // 0 unless the read was refused
}

func (e *ReadError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("%s: HTTP %d: %s", e.Tool, e.HTTPStatus, ErrBriefing)
	}
	return e.Tool + ": " + ErrBriefing.Error()
}

func (e *ReadError) Unwrap() error { return ErrBriefing }

// RequestError refuses a card before any read: its account ID or its day is not one (collect_evidence's
// ValueError).
type RequestError struct{ Message string }

func (e *RequestError) Error() string { return e.Message }

// CodeError is a refusal of a prepared brief, by the code the tool result names.
type CodeError struct{ Code string }

func (e *CodeError) Error() string { return e.Code }

// The codes prepare_renewal_brief refuses with.
var (
	ErrAccountNotSelected = &CodeError{"account_not_selected"}
	ErrInvalidAsOf        = &CodeError{"invalid_as_of"}
	ErrDeadline           = &CodeError{"brief_deadline_exceeded"}
	ErrInvalidToken       = &CodeError{"invalid_token"}
	ErrTooLarge           = &CodeError{"brief_too_large"}
	ErrRejected           = &CodeError{"brief_rejected"}
	ErrEvidenceInvalid    = &CodeError{"brief_evidence_invalid"}
	ErrSourceFailed       = &CodeError{"brief_source_failed"}
)

// The bounds of a prepared brief.
const (
	BriefDeadline    = 45 * time.Second
	MaxArtifactBytes = 4 * 1024 * 1024
)

// DemoAsOf is the day the demo book is planned from.
const DemoAsOf = "2027-01-15"

// Principal is what the server established about the caller of one command or tool call, from a verified
// credential. A caller supplies none of it, and the maker never sees the credential itself.
type Principal struct {
	WorkspaceID string
	PrincipalID string
	TokenID     string   // receipts.Fingerprint of the verified bearer
	Scopes      []string // what the token grants
	// Live is whether the credential still verifies now. Nil means it does.
	Live func() bool
}

func (p Principal) live() bool { return p.Live == nil || p.Live() }

// Selection is what a selected-mode server opens: each selected account, and whether its Slack is.
type Selection map[string]bool

// Maker reads one account through its Source under receipts, builds the card, and publishes it.
type Maker struct {
	Receipts receipts.Store
	Source   Source
	Sources  evidence.Sources // what the folder is connected to
	// Connections names each source system's connection ("crm", "calls", "slack"), whose generation fences
	// its reads and the card's publication. A system with none is not fenced.
	Connections map[string]string
	// NewID gives the UUIDs brief IDs are made from. Nil is receipts.RandomID.
	NewID func() string
	// Now is the clock elapsed times and deadlines are read from. Nil is time.Now.
	Now func() time.Time
}

func (m *Maker) newID() string {
	if m.NewID == nil {
		return receipts.RandomID()
	}
	return m.NewID()
}

func (m *Maker) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

// stopwatch gives the milliseconds since it started.
func (m *Maker) stopwatch() func() float64 {
	began := m.now()
	return func() float64 { return float64(m.now().Sub(began).Microseconds()) / 1000 }
}

// Sealed is a published card, with the receipt that seals it.
type Sealed struct {
	BriefID string
	Payload []byte
	SHA256  string
	Seal    receipts.Receipt
	Units   []evidence.Unit
	Gaps    []evidence.Gap
	Text    string
}

// rulesVersion is the version of the rules a card's text is made under.
var rulesVersion = strconv.Itoa(evidence.SanitizerVersion)

// The scopes the reads need.
var scopes = map[string]string{"get_account": "crm:read", "list_opportunities": "crm:read",
	"read_call_notes": "calls:read", "read_slack_messages": "slack:read"}

type planned struct{ tool, system string }

// plan is the reads a card makes: CRM always, calls when connected, Slack when asked for and connected.
func plan(s evidence.Sources, includeSlack bool) []planned {
	reads := []planned{{"get_account", "crm"}, {"list_opportunities", "crm"}}
	if s.CallsBackend != "none" {
		reads = append(reads, planned{"read_call_notes", "calls"})
	}
	if includeSlack && s.SlackBackend != "none" {
		reads = append(reads, planned{"read_slack_messages", "slack"})
	}
	return reads
}

func sourceNames(s evidence.Sources, includeSlack bool) *strictjson.Object {
	name := func(backend, other string) string {
		switch backend {
		case "fixture", "sqlite":
			return "demo_fixture"
		case "none":
			return "not_connected"
		}
		return other
	}
	names := strictjson.NewObject("crm", name(s.CRMBackend, "salesforce"),
		"calls", name(s.CallsBackend, "imported_transcript"))
	if includeSlack {
		names.Set("slack", name(s.SlackBackend, "slack"))
	}
	return names
}

func briefID(uuid string) string { return "brief-" + strings.ReplaceAll(uuid, "-", "") }

// checkRequest is collect_evidence's checks before any read, in its order and with its messages.
func checkRequest(s evidence.Sources, accountID, asOf string) error {
	if !evidence.ValidAccountID(accountID, s.CRMBackend == "salesforce") {
		if s.CRMBackend == "salesforce" {
			return &RequestError{"account ID must use acc-sf- followed by the 18-character Salesforce ID"}
		}
		return &RequestError{"account ID must have the form acc-001"}
	}
	// One message for every day that is not YYYY-MM-DD: date.fromisoformat's own quotes the text, which can
	// carry a real org's names.
	if _, err := evidence.Day(asOf); err != nil {
		return &RequestError{"as-of date must use YYYY-MM-DD"}
	}
	return nil
}

// versions are the records a read returned, as the record versions their receipt binds: each record's
// canonical encoding is its body.
func versions(records []*strictjson.Object) ([]receipts.RecordVersion, error) {
	out := make([]receipts.RecordVersion, len(records))
	for i, record := range records {
		ref, _ := record.Get("record_ref")
		text, _ := ref.(string)
		parts := strings.SplitN(text, "/", 3)
		body, err := strictjson.Dumps(record, strictjson.Prepared)
		if len(parts) != 3 || err != nil {
			return nil, fmt.Errorf("%w: a record without its reference", ErrBriefing)
		}
		out[i] = receipts.RecordVersion{Source: parts[0], Table: parts[1], RecordID: parts[2], Body: []byte(body)}
	}
	return out, nil
}

// replayed is the committed outcome of a read whose start found its receipt already there: its work ran,
// or is running, under the first start, so it does not run again. A pending read is another worker's
// (ErrInProgress); a finished one gives back what it bound. A receipt binds no thread snapshot, so a replayed
// read of a live Slack thread gives none, and no card is built from its messages.
func (m *Maker) replayed(ctx context.Context, r reading) (records []any, bound []receipts.RecordVersion,
	snapshot any, outcome string, err error) {
	started := r.started
	switch {
	case started.Status == "pending":
		return nil, nil, nil, "", fmt.Errorf("%s: %w", r.tool, receipts.ErrInProgress)
	case started.Status == "ok":
		bound, err = m.Receipts.Records(ctx, started.WorkspaceID, started.AuditID)
		if err != nil {
			return nil, nil, nil, "", err
		}
		records = make([]any, len(bound))
		for i, v := range bound {
			if records[i], err = strictjson.Loads(v.Body); err != nil {
				return nil, nil, nil, "", err
			}
		}
		return records, bound, nil, "ok", nil
	case started.Error != nil && *started.Error == "request_failed" && *started.HTTPStatus == 403:
		return nil, nil, nil, "denied", nil
	}
	// A read that failed and a read with no selection finish with the same receipt, error/200. A replay
	// cannot tell them apart, so it makes no card: a card saying that nothing was selected would be false
	// after a failed read. The command runs again under a new operation.
	return nil, nil, nil, "", &ReadError{Tool: r.tool}
}

// reading is one read's receipt, started by the caller; read runs it.
type reading struct {
	tool, system, accountID string
	principal               Principal
	started                 receipts.Receipt
	deadline                time.Time // zero for none
}

// source makes one read of the source. A live Slack read goes through the source's ThreadReader, which gives
// the thread's snapshot with its messages.
func (m *Maker) source(ctx context.Context, tool, accountID string) (system string,
	records []*strictjson.Object, snapshot any, err error) {
	threads, live := m.Source.(ThreadReader)
	if !live || tool != "read_slack_messages" || m.Sources.SlackBackend != "slack" {
		system, records, err = m.Source.Read(ctx, tool, accountID)
		return system, records, nil, err
	}
	records, thread, err := threads.ReadThread(ctx, accountID)
	if thread != nil {
		// The maker keeps its own copy, so the source cannot change the snapshot after it is checked.
		snapshot = strictjson.Clone(thread)
	}
	return "slack", records, snapshot, err
}

// read runs one read whose receipt is committed. The scope, the credential and the deadline are checked
// first; then the source is read, and its records are released only once FinishRead has bound their
// versions to the receipt. The outcome is "ok", "denied" or "not_selected". A live Slack read also gives the
// thread's snapshot.
//
// The source reads under the caller's context, bounded by the time left to the deadline. A read whose
// context is done when it returns releases nothing, whatever the source answered: its receipt finishes as
// error/504 past the deadline and as error/200 after a cancel.
func (m *Maker) read(ctx context.Context, r reading) (records []any, bound []receipts.RecordVersion,
	snapshot any, outcome string, err error) {
	elapsed := m.stopwatch()
	// A failed read's receipt is finished even when the caller has gone.
	finish := func(o receipts.Outcome) error {
		o.ElapsedMS = elapsed()
		_, err := m.Receipts.Finish(context.WithoutCancel(ctx), r.started.Ticket(), o)
		return err
	}
	if !r.principal.live() {
		return nil, nil, nil, "", errors.Join(ErrInvalidToken, finish(receipts.Failed(401, "request_failed")))
	}
	if !slices.Contains(r.principal.Scopes, scopes[r.tool]) {
		return nil, nil, nil, "denied", finish(receipts.Denied())
	}
	bounded := ctx
	if !r.deadline.IsZero() {
		now := m.now()
		if !now.Before(r.deadline) {
			return nil, nil, nil, "", errors.Join(ErrDeadline, finish(receipts.Failed(504, "request_failed")))
		}
		var cancel context.CancelFunc
		bounded, cancel = context.WithTimeout(ctx, r.deadline.Sub(now))
		defer cancel()
	}
	system, found, readErr := "", []*strictjson.Object(nil), bounded.Err()
	if readErr == nil {
		system, found, snapshot, readErr = m.await(bounded, r.tool, r.accountID)
	}
	switch stopped := bounded.Err(); {
	case errors.Is(stopped, context.DeadlineExceeded):
		return nil, nil, nil, "", errors.Join(ErrDeadline, finish(receipts.Failed(504, "request_failed")))
	case stopped != nil:
		return nil, nil, nil, "", errors.Join(&ReadError{Tool: r.tool}, stopped,
			finish(receipts.Failed(200, "request_failed")))
	case errors.Is(readErr, ErrNotSelected):
		return nil, nil, nil, "not_selected", finish(receipts.NotSelected())
	case readErr != nil || system != r.system:
		return nil, nil, nil, "", errors.Join(&ReadError{Tool: r.tool},
			finish(receipts.Failed(200, "request_failed")))
	}
	bound, err = versions(found)
	if err != nil {
		return nil, nil, nil, "", errors.Join(err, finish(receipts.Failed(200, "request_failed")))
	}
	if _, err := m.Receipts.FinishRead(ctx, r.started.Ticket(), bound, elapsed()); err != nil {
		return nil, nil, nil, "", err
	}
	records = make([]any, len(found))
	for i, record := range found {
		records[i] = record
	}
	return records, bound, snapshot, "ok", nil
}

// collected is what the reads of one card gathered.
type collected struct {
	records                  []any
	ids                      []string
	receiptOf                map[string]string // tool to its read receipt
	versionOf                map[string]string // record_ref to its content version
	notesStatus, slackStatus string
	threadSnapshot           any // of a live Slack read that returned messages
}

// collect makes the planned reads. start commits the receipt of the n-th read, from 1.
func (m *Maker) collect(ctx context.Context, reads []planned, accountID string, principal Principal,
	deadline time.Time, includeSlack bool, start func(n int, p planned) (receipts.Receipt, error)) (collected, error) {
	c := collected{records: []any{}, receiptOf: map[string]string{}, versionOf: map[string]string{},
		notesStatus: "available", slackStatus: "not_requested"}
	if m.Sources.CallsBackend == "none" {
		c.notesStatus = "not_connected"
	}
	switch {
	case m.Sources.SlackBackend == "none":
		c.slackStatus = "not_connected"
	case includeSlack:
		c.slackStatus = "available"
	}
	for i, p := range reads {
		if !deadline.IsZero() && !m.now().Before(deadline) {
			return c, ErrDeadline
		}
		started, err := start(i+1, p)
		if err != nil {
			return c, err
		}
		c.ids = append(c.ids, started.AuditID)
		c.receiptOf[p.tool] = started.AuditID
		r := reading{tool: p.tool, system: p.system, accountID: accountID, principal: principal, started: started,
			deadline: deadline}
		run := m.read
		if started.Replay {
			run = m.replayed
		}
		records, bound, snapshot, outcome, err := run(ctx, r)
		if err != nil {
			return c, err
		}
		status := map[string]*string{"read_call_notes": &c.notesStatus, "read_slack_messages": &c.slackStatus}[p.tool]
		switch {
		case outcome == "denied" && status != nil:
			*status = "denied"
			continue
		case outcome == "denied":
			return c, &ReadError{Tool: p.tool, HTTPStatus: 403}
		case outcome == "not_selected" && p.tool == "read_slack_messages" && m.Sources.SlackBackend == "slack":
			c.slackStatus = "not_selected"
			continue
		case outcome == "not_selected":
			return c, &ReadError{Tool: p.tool}
		}
		for _, v := range bound {
			c.versionOf[v.Ref()] = v.Version()
		}
		c.records = append(c.records, records...)
		if p.tool == "read_slack_messages" {
			c.threadSnapshot = snapshot
		}
	}
	// A caller who left once the last read had returned gets no card. The reads keep their receipts.
	return c, gone(ctx)
}

// gone is why no card is made once the caller has left, and nil while the caller waits.
func gone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the caller left before the card was published; no card made: %w", err)
	}
	return nil
}

// await makes one read of the source and waits for it no longer than ctx lasts. The read runs in its own
// goroutine. A source that ignores its context does not hold the maker: when ctx ends first, await returns
// ctx's error, and the read's answer is discarded whenever it comes. A read that panics in time panics here.
func (m *Maker) await(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, any, error) {
	type answer struct {
		system   string
		records  []*strictjson.Object
		snapshot any
		err      error
		panicked any
	}
	answered := make(chan answer, 1)
	go func() {
		var a answer
		defer func() {
			a.panicked = recover()
			answered <- a
		}()
		a.system, a.records, a.snapshot, a.err = m.source(ctx, tool, accountID)
	}()
	select {
	case a := <-answered:
		if a.panicked != nil {
			panic(a.panicked)
		}
		return a.system, a.records, a.snapshot, a.err
	case <-ctx.Done():
		return "", nil, nil, ctx.Err()
	}
}

func (m *Maker) evidence(ctx context.Context, workspaceID, id, accountID, asOf string, c collected) (*strictjson.Object, error) {
	read, err := m.Receipts.Read(ctx, workspaceID, c.ids)
	if err != nil {
		return nil, err
	}
	values := make([]any, len(read))
	for i, r := range read {
		values[i] = r.Value()
	}
	return evidence.Assemble(id, accountID, asOf, c.records, values, c.notesStatus, c.slackStatus, c.threadSnapshot,
		m.Sources), nil
}

// projections are a card's units and gaps as its publication writes them.
func projections(units []evidence.Unit, gaps []evidence.Gap, c collected) ([]receipts.UnitRow, []receipts.GapRow, error) {
	rows := make([]receipts.UnitRow, len(units))
	for i, u := range units {
		value, err := strictjson.Dumps(u.Value(), strictjson.Prepared)
		if err != nil {
			return nil, nil, err
		}
		rows[i] = receipts.UnitRow{N: u.N, Kind: u.Kind, RecordRef: u.RecordRef, Tool: u.Tool,
			ReceiptID: c.receiptOf[u.Tool], ContentVersion: c.versionOf[u.RecordRef], Value: []byte(value)}
		if u.Kind == "sentence" {
			rows[i].Shown = u.Shown
		}
	}
	gapRows := make([]receipts.GapRow, len(gaps))
	for i, g := range gaps {
		gapRows[i] = receipts.GapRow{Code: g.Code, Disposition: g.Disposition, Text: g.Text, Count: g.Count}
		if g.ByRole != nil {
			value, _ := g.Value().Get("by_role")
			byRole, err := strictjson.Dumps(value, strictjson.Prepared)
			if err != nil {
				return nil, nil, err
			}
			gapRows[i].ByRole = []byte(byRole)
		}
	}
	return rows, gapRows, nil
}

// publish writes the card and finishes its seal in one step. A refused recheck finishes the seal itself; any
// other failure leaves it pending, so it is finished here as the save failure.
//
// The caller's cancel counts up to the call of Publish and not after it. A caller who has left by then gets
// no card, and the seal finishes as the failure. From that call on the publication runs to its end under a
// context the caller cannot cancel: a cancel in the middle of the store's transaction could leave the caller
// told of a failure while the card and its seal are committed.
func (m *Maker) publish(ctx context.Context, p receipts.Publication, units []evidence.Unit, gaps []evidence.Gap,
	text string, c collected, failure receipts.Outcome) (*Sealed, error) {
	uncancelled := context.WithoutCancel(ctx)
	var err error
	if p.Units, p.Gaps, err = projections(units, gaps, c); err == nil {
		if err = gone(ctx); err == nil {
			var card receipts.Card
			if card, err = m.Receipts.Publish(uncancelled, p); err == nil {
				return &Sealed{BriefID: p.BriefID, Payload: card.Bytes, SHA256: card.SHA256, Seal: card.Seal,
					Units: units, Gaps: gaps, Text: text}, nil
			}
		}
	}
	if !errors.Is(err, receipts.ErrNotPermitted) && !errors.Is(err, receipts.ErrSourceChanged) {
		failure.ElapsedMS = p.ElapsedMS
		_, _ = m.Receipts.Finish(uncancelled, p.Seal, failure)
	}
	return nil, err
}

// Brief is briefing.create_brief: the card a person asks for with brief or demo. Each read is its own
// tool call, numbered as the CLI numbers its JSON-RPC requests; the save_brief receipt seals the card.
type Brief struct {
	Principal Principal
	// OperationID is the same across retries of the command. The n-th read's operation is OperationID/n
	// and the seal's OperationID/seal.
	OperationID  string
	AccountID    string
	AsOf         string
	IncludeSlack bool
}

func (m *Maker) connection(system string) string { return m.Connections[system] }

// Brief reads, builds and publishes one card.
func (m *Maker) Brief(ctx context.Context, b Brief) (*Sealed, error) {
	id := briefID(m.newID())
	if err := checkRequest(m.Sources, b.AccountID, b.AsOf); err != nil {
		return nil, err
	}
	p := b.Principal
	c, err := m.collect(ctx, plan(m.Sources, b.IncludeSlack), b.AccountID, p, time.Time{}, b.IncludeSlack,
		func(n int, read planned) (receipts.Receipt, error) {
			return m.Receipts.Start(ctx, receipts.Start{WorkspaceID: p.WorkspaceID,
				OperationID: fmt.Sprintf("%s/%d", b.OperationID, n), PrincipalID: p.PrincipalID, TokenID: p.TokenID,
				Tool: read.tool, Scope: scopes[read.tool], AccountRef: b.AccountID,
				ConnectionID: m.connection(read.system), Arguments: strictjson.NewObject("account_id", b.AccountID),
				RequestID: receipts.RPC(n), BriefID: &id})
		})
	if err != nil {
		return nil, err
	}
	snapshot, err := m.evidence(ctx, p.WorkspaceID, id, b.AccountID, b.AsOf, c)
	if err != nil {
		return nil, err
	}
	units, text, gaps, err := evidence.Build(snapshot)
	if err != nil {
		return nil, fmt.Errorf("brief %s rejected; no card published: %w", id, err)
	}
	// No seal is started for a caller who has left.
	if err := gone(ctx); err != nil {
		return nil, err
	}
	seal, err := m.Receipts.Start(ctx, receipts.Start{WorkspaceID: p.WorkspaceID, OperationID: b.OperationID + "/seal",
		PrincipalID: p.PrincipalID, TokenID: p.TokenID, Tool: receipts.SaveBrief, AccountRef: b.AccountID,
		Arguments: strictjson.NewObject("account_id", b.AccountID, "brief_id", id), RequestID: receipts.Null()})
	if err != nil {
		return nil, err
	}
	if err := ownStart(seal); err != nil {
		return nil, err
	}
	elapsed := m.stopwatch()
	artifact := strictjson.NewObject("schema_version", 3, "brief_id", id, "account_id", b.AccountID,
		"as_of", b.AsOf, "status", "accepted", "rules_version", evidence.SanitizerVersion, "sealed_by", seal.AuditID,
		"sources", sourceNames(m.Sources, b.IncludeSlack), "text", text, "units", evidence.UnitValues(units),
		"gaps", evidence.GapValues(gaps), "evidence", snapshot)
	encoded, err := strictjson.Dumps(artifact, strictjson.Artifact)
	if err != nil {
		failure := receipts.Failed(500, "brief_save_failed")
		failure.ElapsedMS = elapsed()
		_, _ = m.Receipts.Finish(ctx, seal.Ticket(), failure)
		return nil, err
	}
	return m.publish(ctx, receipts.Publication{WorkspaceID: p.WorkspaceID, AccountRef: b.AccountID, BriefID: id,
		PrincipalID: p.PrincipalID, Seal: seal.Ticket(), Kind: "card", SchemaVersion: 3, RulesVersion: rulesVersion, Bytes: []byte(encoded),
		Reads: c.ids, ElapsedMS: elapsed()}, units, gaps, text, c, receipts.Failed(500, "brief_save_failed"))
}

// Prepare is prepare_renewal_brief, run as the gateway runs it: the outer call's receipt is committed first
// and records the call's arguments as given; each source read is a child read of it; and the outer
// receipt, finished with the published bytes' digest, is the brief's seal.
type Prepare struct {
	Principal   Principal
	Selection   Selection
	OperationID string
	RequestID   receipts.RequestID
	// Arguments are the call's arguments as received: account_id, and as_of when the caller gave one.
	Arguments *strictjson.Object
}

// resolveAsOf is selected_briefs._as_of: an omitted or null as_of is the demo book's day, or today in UTC
// for Salesforce; a given one must be YYYY-MM-DD.
func (m *Maker) resolveAsOf(arguments *strictjson.Object) (string, error) {
	value, _ := arguments.Get("as_of")
	if value == nil {
		if m.Sources.CRMBackend == "salesforce" {
			return m.now().UTC().Format("2006-01-02"), nil
		}
		return DemoAsOf, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", ErrInvalidAsOf
	}
	if d, err := evidence.DateFromISO(text); err != nil || d.Format("2006-01-02") != text {
		return "", ErrInvalidAsOf
	}
	return text, nil
}

func (m *Maker) Prepare(ctx context.Context, p Prepare) (*Sealed, error) {
	principal := p.Principal
	accountValue, _ := p.Arguments.Get("account_id")
	accountID, _ := accountValue.(string)
	outer, err := m.Receipts.Start(ctx, receipts.Start{WorkspaceID: principal.WorkspaceID, OperationID: p.OperationID,
		PrincipalID: principal.PrincipalID, TokenID: principal.TokenID, Tool: receipts.Composite, Scope: "crm:read",
		AccountRef: accountID, Arguments: p.Arguments, RequestID: p.RequestID})
	if err != nil {
		return nil, err
	}
	// Only the worker whose start committed the call runs it and settles it.
	if err := ownStart(outer); err != nil {
		return nil, err
	}
	elapsed, deadline := m.stopwatch(), m.now().Add(BriefDeadline)
	// A call that fails is finished even when the caller has gone.
	fail := func(err error) (*Sealed, error) {
		outcome := receipts.Failed(200, "request_failed")
		outcome.ElapsedMS = elapsed()
		_, finishErr := m.Receipts.Finish(context.WithoutCancel(ctx), outer.Ticket(), outcome)
		return nil, errors.Join(err, finishErr)
	}
	if !slices.Contains(principal.Scopes, "crm:read") {
		return fail(ErrBriefing)
	}
	if _, selected := p.Selection[accountID]; !selected {
		return fail(ErrAccountNotSelected)
	}
	asOf, err := m.resolveAsOf(p.Arguments)
	if err != nil {
		return fail(err)
	}
	id := briefID(m.newID())
	includeSlack := p.Selection[accountID] && m.Sources.SlackBackend != "none"
	if includeSlack && m.Sources.SlackBackend == "slack" {
		// A prepared card that holds live Slack text records the scopes of the Slack credential that read
		// it, and a later release is checked against them. No reader reports those scopes yet, so a
		// prepared card with live Slack is refused rather than sealed without them.
		return fail(ErrBriefing)
	}
	c, err := m.collect(ctx, plan(m.Sources, includeSlack), accountID, principal, deadline, includeSlack,
		func(n int, read planned) (receipts.Receipt, error) {
			return m.Receipts.StartChild(ctx, receipts.ChildStart{Parent: outer.AuditID, Ordinal: n, Tool: read.tool,
				Scope: scopes[read.tool], ConnectionID: m.connection(read.system), AccountRef: accountID,
				Arguments: strictjson.NewObject("account_id", accountID), BriefID: &id})
		})
	if err != nil {
		var code *CodeError
		if !errors.As(err, &code) {
			err = errors.Join(ErrSourceFailed, err)
		}
		return fail(err)
	}
	snapshot, err := m.evidence(ctx, principal.WorkspaceID, id, accountID, asOf, c)
	if err != nil {
		return fail(errors.Join(ErrEvidenceInvalid, err))
	}
	units, text, gaps, err := evidence.Build(snapshot)
	if err != nil {
		return fail(errors.Join(ErrRejected, err))
	}
	withSlack := m.Sources.SlackBackend != "none"
	artifact := strictjson.NewObject("kind", "selected_brief", "schema_version", 3, "brief_id", id,
		"account_id", accountID, "as_of", asOf,
		"prepared_by", strictjson.NewObject("outer_audit_id", outer.AuditID, "token_id", principal.TokenID),
		"bindings", bindings(m.Sources), "slack_scopes", nil,
		"sources", sourceNames(m.Sources, includeSlack || !withSlack),
		"text", text, "units", evidence.UnitValues(units), "gaps", evidence.GapValues(gaps), "evidence", snapshot)
	encoded, err := strictjson.Dumps(artifact, strictjson.Prepared)
	switch {
	case err != nil:
		return fail(errors.Join(ErrEvidenceInvalid, err))
	case len(encoded) > MaxArtifactBytes:
		return fail(ErrTooLarge)
	case !m.now().Before(deadline):
		return fail(ErrDeadline)
	case !principal.live():
		return fail(ErrInvalidToken)
	}
	return m.publish(ctx, receipts.Publication{WorkspaceID: principal.WorkspaceID, AccountRef: accountID, BriefID: id,
		PrincipalID: principal.PrincipalID, Seal: outer.Ticket(), Kind: "selected_brief", SchemaVersion: 3, RulesVersion: rulesVersion,
		Bytes: []byte(encoded), Reads: c.ids, ElapsedMS: elapsed()}, units, gaps, text, c,
		receipts.Failed(200, "request_failed"))
}

// ownStart refuses to run work whose receipt another start committed: it is running under that start
// (ErrInProgress), or it finished there (receipts.ErrFinished), and its outcome stands.
func ownStart(r receipts.Receipt) error {
	switch {
	case !r.Replay:
		return nil
	case r.Status == "pending":
		return fmt.Errorf("%s %s: %w", r.Tool, r.AuditID, receipts.ErrInProgress)
	}
	return fmt.Errorf("%s %s finished as %s: %w", r.Tool, r.AuditID, r.Status, receipts.ErrFinished)
}

// bindings is a prepared brief's record of the sources it was read from.
func bindings(s evidence.Sources) *strictjson.Object {
	return strictjson.NewObject("crm_backend", s.CRMBackend, "crm", s.CRMBinding, "calls_backend", s.CallsBackend,
		"calls", s.CallsBinding, "slack_backend", s.SlackBackend, "slack", s.SlackBinding)
}
