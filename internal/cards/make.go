package cards

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Source is the reads a card makes, one per tool: get_account, list_opportunities, read_call_notes and
// read_slack_messages. It returns the source system the records come from and the records with their
// record_ref. demo.Store is one.
type Source interface {
	Read(tool, accountID string) (system string, records []*strictjson.Object, err error)
}

// ErrNotSelected is a Source's answer when the account has no selection in that source, such as a Slack
// thread that was never mapped. The read finishes as error/200 and the card states it.
var ErrNotSelected = errors.New("cards: no selection for this account in this source")

// ErrBriefing is a read that failed, so no card is made (briefing.BriefingError).
var ErrBriefing = errors.New("cards: required evidence could not be collected; no brief generated")

// Files holds sealed card bytes.
type Files interface {
	Save(name string, payload []byte) error
	Remove(name string)
}

// MemoryFiles is Files in a map.
type MemoryFiles map[string][]byte

func (m MemoryFiles) Save(name string, payload []byte) error {
	m[name] = append([]byte(nil), payload...)
	return nil
}

func (m MemoryFiles) Remove(name string) { delete(m, name) }

// Maker reads one account through its Source under receipts, builds the card, and seals it.
type Maker struct {
	Receipts receipts.Store
	Source   Source
	Sources  evidence.Sources // what the folder is connected to
	Files    Files
	// NewID gives the UUIDs brief IDs are made from. Nil is receipts.RandomID.
	NewID func() string
	// Stopwatch starts a timer whose reading is the milliseconds since. Nil reads the monotonic clock.
	Stopwatch func() func() float64
}

func (m *Maker) newID() string {
	if m.NewID == nil {
		return receipts.RandomID()
	}
	return m.NewID()
}

func (m *Maker) start() func() float64 {
	if m.Stopwatch == nil {
		began := time.Now()
		return func() float64 { return float64(time.Since(began).Microseconds()) / 1000 }
	}
	return m.Stopwatch()
}

// Sealed is a card as stored, with the receipt that seals it.
type Sealed struct {
	BriefID string
	Payload []byte
	SHA256  string
	Seal    receipts.Receipt
	Units   []evidence.Unit
	Gaps    []evidence.Gap
	Text    string
}

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

func checkRequest(s evidence.Sources, accountID, asOf string) error {
	if _, err := evidence.Day(asOf); err != nil {
		return errors.New("as-of date must use YYYY-MM-DD")
	}
	if !evidence.ValidAccountID(accountID, s.CRMBackend == "salesforce") {
		return errors.New("account ID does not match its CRM")
	}
	return nil
}

// read is one read under its own receipt. The receipt is committed before the source is touched and
// finished before the records are used. A read the grant does not cover finishes as a denial and reads
// nothing. The outcome is "ok", "denied" or "not_selected".
func (m *Maker) read(ctx context.Context, r reading) (records []any, receiptID string, outcome string, err error) {
	started, err := m.Receipts.Start(ctx, receipts.Start{TokenID: r.tokenID, Tool: r.tool,
		Arguments: strictjson.NewObject("account_id", r.accountID), RequestID: r.requestID, BriefID: &r.briefID})
	if err != nil {
		return nil, "", "", err
	}
	elapsed := m.start()
	finish := func(o receipts.Outcome) error {
		o.ElapsedMS = elapsed()
		_, err := m.Receipts.Finish(ctx, started.AuditID, o)
		return err
	}
	if !slices.Contains(r.scopes, scopes[r.tool]) {
		return nil, started.AuditID, "denied", finish(receipts.Denied())
	}
	system, found, readErr := m.Source.Read(r.tool, r.accountID)
	switch {
	case errors.Is(readErr, ErrNotSelected):
		return nil, started.AuditID, "not_selected", finish(receipts.NotSelected())
	case readErr != nil || system != r.system:
		if err := finish(receipts.Failed(200, "request_failed")); err != nil {
			return nil, "", "", err
		}
		return nil, "", "", fmt.Errorf("%s: %w", r.tool, ErrBriefing)
	}
	rows := make([]string, len(found))
	records = make([]any, len(found))
	for i, record := range found {
		ref, _ := record.Get("record_ref")
		rows[i], _ = ref.(string)
		records[i] = record
	}
	return records, started.AuditID, "ok", finish(receipts.Outcome{Status: "ok", HTTPStatus: 200, Rows: rows})
}

type reading struct {
	tool, system, accountID, briefID, tokenID string
	scopes                                    []string
	requestID                                 receipts.RequestID
}

// collected is what the reads of one card gathered.
type collected struct {
	records                  []any
	ids                      []string
	notesStatus, slackStatus string
}

func (m *Maker) collect(ctx context.Context, reads []planned, base reading, requestID func(int) receipts.RequestID,
	includeSlack bool) (collected, error) {
	c := collected{records: []any{}, notesStatus: "available", slackStatus: "not_requested"}
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
		r := base
		r.tool, r.system, r.requestID = p.tool, p.system, requestID(i+1)
		records, id, outcome, err := m.read(ctx, r)
		if err != nil {
			return c, err
		}
		c.ids = append(c.ids, id)
		status := map[string]*string{"read_call_notes": &c.notesStatus, "read_slack_messages": &c.slackStatus}[p.tool]
		switch {
		case outcome == "denied" && status != nil:
			*status = "denied"
			continue
		case outcome == "denied":
			return c, fmt.Errorf("%s: HTTP 403: %w", p.tool, ErrBriefing)
		case outcome == "not_selected" && p.tool == "read_slack_messages" && m.Sources.SlackBackend == "slack":
			c.slackStatus = "not_selected"
			continue
		case outcome == "not_selected":
			return c, fmt.Errorf("%s: %w", p.tool, ErrBriefing)
		}
		c.records = append(c.records, records...)
	}
	return c, nil
}

func (m *Maker) evidence(ctx context.Context, id, accountID, asOf string, c collected) (*strictjson.Object, error) {
	read, err := m.Receipts.Read(ctx, c.ids)
	if err != nil {
		return nil, err
	}
	values := make([]any, len(read))
	for i, r := range read {
		values[i] = r.Value()
	}
	return evidence.Assemble(id, accountID, asOf, c.records, values, c.notesStatus, c.slackStatus, nil, m.Sources), nil
}

// Brief is briefing.create_brief: the card a person asks for with brief or demo. Each read is its own
// tool call, numbered as the CLI numbers its JSON-RPC requests; the save_brief receipt seals the file.
type Brief struct {
	AccountID    string
	AsOf         string
	Token        string   // the bearer the reads are made under
	Scopes       []string // what it grants
	IncludeSlack bool
}

// Brief reads, builds and seals one card. The seal receipt is committed before the file exists and finished
// with the whole file's digest; a failed save removes the file and finishes the seal as an error.
func (m *Maker) Brief(ctx context.Context, b Brief) (*Sealed, error) {
	id := briefID(m.newID())
	if err := checkRequest(m.Sources, b.AccountID, b.AsOf); err != nil {
		return nil, err
	}
	tokenID := receipts.Fingerprint(b.Token)
	base := reading{accountID: b.AccountID, briefID: id, tokenID: tokenID, scopes: b.Scopes}
	c, err := m.collect(ctx, plan(m.Sources, b.IncludeSlack), base, func(n int) receipts.RequestID {
		return receipts.RPC(n)
	}, b.IncludeSlack)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.evidence(ctx, id, b.AccountID, b.AsOf, c)
	if err != nil {
		return nil, err
	}
	units, text, gaps, err := evidence.Build(snapshot)
	if err != nil {
		return nil, fmt.Errorf("brief %s rejected; no card published: %w", id, err)
	}
	seal, err := m.Receipts.Start(ctx, receipts.Start{TokenID: tokenID, Tool: "save_brief",
		Arguments: strictjson.NewObject("account_id", b.AccountID, "brief_id", id), RequestID: receipts.Null()})
	if err != nil {
		return nil, err
	}
	elapsed := m.start()
	artifact := strictjson.NewObject("schema_version", 3, "brief_id", id, "account_id", b.AccountID,
		"as_of", b.AsOf, "status", "accepted", "rules_version", evidence.SanitizerVersion, "sealed_by", seal.AuditID,
		"sources", sourceNames(m.Sources, b.IncludeSlack), "text", text, "units", evidence.UnitValues(units),
		"gaps", evidence.GapValues(gaps), "evidence", snapshot)
	return m.seal(ctx, id, artifact, strictjson.Artifact, seal.AuditID, elapsed, "brief_save_failed", 500,
		units, gaps, text)
}

func (m *Maker) seal(ctx context.Context, id string, artifact *strictjson.Object, options strictjson.DumpOptions,
	sealID string, elapsed func() float64, failure string, failureStatus int, units []evidence.Unit,
	gaps []evidence.Gap, text string) (*Sealed, error) {
	encoded, err := strictjson.Dumps(artifact, options)
	payload := []byte(encoded)
	if err == nil {
		err = m.Files.Save(id+".json", payload)
	}
	if err != nil {
		// A file whose seal did not finish is not a card.
		m.Files.Remove(id + ".json")
		outcome := receipts.Failed(failureStatus, failure)
		outcome.ElapsedMS = elapsed()
		_, _ = m.Receipts.Finish(ctx, sealID, outcome)
		return nil, err
	}
	finished, err := m.Receipts.Finish(ctx, sealID, receipts.Outcome{Status: "ok", HTTPStatus: 200,
		Rows: []string{Reference(id, payload)}, ElapsedMS: elapsed()})
	if err != nil {
		m.Files.Remove(id + ".json")
		return nil, err
	}
	return &Sealed{BriefID: id, Payload: payload, SHA256: sha(payload), Seal: finished, Units: units, Gaps: gaps,
		Text: text}, nil
}

// Prepare is selected_briefs.prepare_brief, run as the gateway runs prepare_renewal_brief: the outer call's
// receipt is committed first, each source read is a child read of it, and the outer receipt, finished with
// the saved bytes' digest, is the brief's seal.
type Prepare struct {
	AccountID    string
	AsOf         string
	Token        string
	Scopes       []string
	IncludeSlack bool // the selection includes this account's Slack
	RequestID    receipts.RequestID
}

func (m *Maker) Prepare(ctx context.Context, p Prepare) (*Sealed, error) {
	tokenID := receipts.Fingerprint(p.Token)
	outer, err := m.Receipts.Start(ctx, receipts.Start{TokenID: tokenID, Tool: "prepare_renewal_brief",
		Arguments: strictjson.NewObject("account_id", p.AccountID), RequestID: p.RequestID})
	if err != nil {
		return nil, err
	}
	elapsed := m.start()
	fail := func(err error) (*Sealed, error) {
		outcome := receipts.Failed(200, "request_failed")
		outcome.ElapsedMS = elapsed()
		_, _ = m.Receipts.Finish(ctx, outer.AuditID, outcome)
		return nil, err
	}
	id := briefID(m.newID())
	if err := checkRequest(m.Sources, p.AccountID, p.AsOf); err != nil {
		return fail(err)
	}
	withSlack := m.Sources.SlackBackend != "none"
	includeSlack := p.IncludeSlack && withSlack
	base := reading{accountID: p.AccountID, briefID: id, tokenID: tokenID, scopes: p.Scopes}
	c, err := m.collect(ctx, plan(m.Sources, includeSlack), base, func(n int) receipts.RequestID {
		return receipts.Child(outer.AuditID, n)
	}, includeSlack)
	if err != nil {
		return fail(err)
	}
	snapshot, err := m.evidence(ctx, id, p.AccountID, p.AsOf, c)
	if err != nil {
		return fail(err)
	}
	units, text, gaps, err := evidence.Build(snapshot)
	if err != nil {
		return fail(fmt.Errorf("brief_rejected: %w", err))
	}
	artifact := strictjson.NewObject("kind", "selected_brief", "schema_version", 3, "brief_id", id,
		"account_id", p.AccountID, "as_of", p.AsOf,
		"prepared_by", strictjson.NewObject("outer_audit_id", outer.AuditID, "token_id", tokenID),
		"bindings", bindings(m.Sources), "slack_scopes", nil,
		"sources", sourceNames(m.Sources, includeSlack || !withSlack),
		"text", text, "units", evidence.UnitValues(units), "gaps", evidence.GapValues(gaps), "evidence", snapshot)
	return m.seal(ctx, id, artifact, strictjson.Prepared, outer.AuditID, elapsed, "request_failed", 200,
		units, gaps, text)
}

// bindings is a prepared brief's record of the sources it was read from.
func bindings(s evidence.Sources) *strictjson.Object {
	return strictjson.NewObject("crm_backend", s.CRMBackend, "crm", s.CRMBinding, "calls_backend", s.CallsBackend,
		"calls", s.CallsBinding, "slack_backend", s.SlackBackend, "slack", s.SlackBinding)
}
