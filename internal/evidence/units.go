package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The port of units.build, the Deal card: numbered units, the card's text and the lines of NOT READ, made
// from one evidence snapshot. Evidence is the JSON object briefing.assemble_evidence makes and a card file
// keeps, and Build checks it as units._validated does before it renders a character.

// Rules, as units.py names them.
const (
	SanitizerVersion = 3
	ShownSentences   = 12
	ShownRecords     = 5
	MaxSentence      = 300
	MaxLabel         = 200
	soonDays         = 30
	staleDays        = 60
	runGapMS         = 2000
	bridgeGapMS      = 10_000
)

// GapCodes is every line NOT READ can print, by code, with its disposition.
var GapCodes = map[string]string{
	"crm_unmapped_fields": "not_read", "calls_denied": "not_read", "calls_excerpt": "not_read",
	"calls_outside_records": "not_read", "slack_not_requested": "not_read", "slack_denied": "not_read",
	"slack_thread": "not_read", "slack_channel": "not_read", "calls_not_connected": "not_read",
	"slack_not_connected": "not_read", "slack_not_selected": "not_read",
	"crm_more_opportunities": "read_not_shown", "crm_more_contracts": "read_not_shown",
	"crm_more_contacts": "read_not_shown", "sentences_cut": "read_not_shown", "sentences_more": "read_not_shown",
	"sentences_long": "read_not_shown", "sentences_later": "read_not_shown",
	"crm_fields_empty": "note", "crm_no_opportunity": "note", "calls_no_record": "note", "slack_no_message": "note",
	"calls_no_transcript": "note",
	"speakers_unknown":    "note", "neutralized": "note", "calls_no_contact": "note",
}

// DemoSellers are the invented sellers the demo text names (seed.SELLERS).
var DemoSellers = []string{"Morgan Reed", "Quinn Moss", "Avery Linden"}

// Rejected is units.BriefRejected: the evidence cannot support a card.
type Rejected struct{ Message string }

func (e *Rejected) Error() string { return e.Message }

// invalid is a missing or mistyped field, which Python meets as KeyError, TypeError or ValueError.
var invalid = &Rejected{"Evidence contains missing or invalid fields."}

func reject(message string) { panic(&Rejected{message}) }

// Unit is one numbered unit of a card. A record unit has the first five fields only.
type Unit struct {
	N         int
	Kind      string // "record" or "sentence"
	Source    string // "crm", "calls" or "slack"
	RecordRef string
	Tool      string

	At               *string
	Speaker          *string
	Role             string // "customer", "seller" or "unknown"
	Basis            string
	Contact          *int
	Text             string
	Partial          bool
	Shown            string // "yes", "cut", "later", "long" or "more"
	Span             *UnitSpan
	RawSHA256        string
	SanitizerVersion int
}

// UnitSpan is where a sentence of an imported transcript lies: its cues or lines, and their times.
type UnitSpan struct {
	First, Last    int
	StartMS, EndMS *int
}

// Gap is one line under NOT READ.
type Gap struct {
	Code        string
	Disposition string
	Count       *big.Int
	ByRole      *RoleCounts
	Text        string
}

// RoleCounts counts sentences by role.
type RoleCounts struct{ Customer, Seller, Unknown int }

func optional[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// Value is the unit as a card file holds it.
func (u Unit) Value() *strictjson.Object {
	o := strictjson.NewObject("n", u.N, "kind", u.Kind, "source", u.Source, "record_ref", u.RecordRef, "tool", u.Tool)
	if u.Kind == "record" {
		return o
	}
	var span any
	if u.Span != nil {
		span = strictjson.NewObject("first", u.Span.First, "last", u.Span.Last,
			"start_ms", optional(u.Span.StartMS), "end_ms", optional(u.Span.EndMS))
	}
	for _, kv := range [][2]any{{"at", optional(u.At)}, {"speaker", optional(u.Speaker)}, {"role", u.Role},
		{"basis", u.Basis}, {"contact", optional(u.Contact)}, {"text", u.Text}, {"partial", u.Partial},
		{"shown", u.Shown}, {"span", span}, {"raw_sha256", u.RawSHA256},
		{"sanitizer_version", u.SanitizerVersion}} {
		o.Set(kv[0].(string), kv[1])
	}
	return o
}

// Value is the gap as a card file holds it.
func (g Gap) Value() *strictjson.Object {
	var byRole any
	if g.ByRole != nil {
		byRole = strictjson.NewObject("customer", g.ByRole.Customer, "seller", g.ByRole.Seller,
			"unknown", g.ByRole.Unknown)
	}
	var count any
	if g.Count != nil {
		count = new(big.Int).Set(g.Count)
	}
	return strictjson.NewObject("code", g.Code, "disposition", g.Disposition, "count", count,
		"by_role", byRole, "text", g.Text)
}

// UnitValues and GapValues are a card's units and gaps as its file holds them.
func UnitValues(units []Unit) []any {
	out := make([]any, len(units))
	for i, u := range units {
		out[i] = u.Value()
	}
	return out
}

func GapValues(gaps []Gap) []any {
	out := make([]any, len(gaps))
	for i, g := range gaps {
		out[i] = g.Value()
	}
	return out
}

// Build checks the evidence and returns its numbered units, the card, and the lines of NOT READ. Bad
// evidence gives no card.
func Build(evidence *strictjson.Object) (units []Unit, text string, gaps []Gap, err error) {
	defer func() {
		if r := recover(); r != nil {
			rejected, ok := r.(*Rejected)
			if !ok {
				panic(r)
			}
			units, text, gaps, err = nil, "", nil, rejected
		}
	}()
	units, text, gaps = build(evidence)
	return units, text, gaps, nil
}

// ---- Python's values, read as units.py reads them ----

func field(o *strictjson.Object, key string) any {
	v, ok := o.Get(key)
	if !ok {
		panic(invalid)
	}
	return v
}

func object(v any) *strictjson.Object {
	o, ok := v.(*strictjson.Object)
	if !ok {
		panic(invalid)
	}
	return o
}

func list(v any) []any {
	l, ok := v.([]any)
	if !ok {
		panic(invalid)
	}
	return l
}

func str(v any) string {
	s, ok := v.(string)
	if !ok {
		panic(invalid)
	}
	return s
}

// isInt is type(v) is int: an integer as strictjson reads it or as Go code writes it, never a bool.
func isInt(v any) (*big.Int, bool) {
	switch n := v.(type) {
	case *big.Int:
		return n, true
	case int:
		return big.NewInt(int64(n)), true
	case int64:
		return big.NewInt(n), true
	}
	return nil, false
}

// truthy is Python's bool(v).
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case *big.Int:
		return v.Sign() != 0
	case int:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case *strictjson.Object:
		return len(v.Keys) > 0
	}
	return true
}

// equal is Python's == between JSON values: numbers by value, bool as 0 and 1, objects and lists by members.
func equal(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x.Cmp(y) == 0
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		s, ok := b.(string)
		return ok && a == s
	case []any:
		l, ok := b.([]any)
		if !ok || len(l) != len(a) {
			return false
		}
		for i := range a {
			if !equal(a[i], l[i]) {
				return false
			}
		}
		return true
	case *strictjson.Object:
		o, ok := b.(*strictjson.Object)
		if !ok || len(o.Keys) != len(a.Keys) {
			return false
		}
		for i, key := range a.Keys {
			v, found := o.Get(key)
			if !found || !equal(a.Values[i], v) {
				return false
			}
		}
		return true
	}
	return false
}

// Equal is Python's == between two JSON values as strictjson reads them.
func Equal(a, b any) bool { return equal(a, b) }

func number(v any) (*big.Float, bool) {
	switch v := v.(type) {
	case bool:
		if v {
			return big.NewFloat(1), true
		}
		return big.NewFloat(0), true
	case int:
		return new(big.Float).SetInt64(int64(v)), true
	case *big.Int:
		return new(big.Float).SetInt(v), true
	case float64:
		return big.NewFloat(v), true
	}
	return nil, false
}

// PyStr is str(v) for None, a bool, a string or an integer; ok is false for any other value.
func PyStr(v any) (text string, ok bool) {
	switch v := v.(type) {
	case nil:
		return "None", true
	case bool:
		if v {
			return "True", true
		}
		return "False", true
	case string:
		return v, true
	}
	if n, isInt := isInt(v); isInt {
		return n.String(), true
	}
	return "", false
}

func pyStr(v any) string {
	text, ok := PyStr(v)
	if !ok {
		panic(invalid)
	}
	return text
}

// less orders two sort keys of one kind, as Python compares them; strings by code point.
func less(a, b any) bool {
	switch a := a.(type) {
	case string:
		return a < str(b)
	case *big.Int:
		n, ok := isInt(b)
		if !ok {
			panic(invalid)
		}
		return a.Cmp(n) < 0
	case nil:
		if b != nil {
			panic(invalid)
		}
		return false
	}
	panic(invalid)
}

func length(s string) int { return utf8.RuneCountInString(s) }

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// ---- units._validated ----

var evidenceKeys = []string{"brief_id", "account_id", "as_of", "records", "receipts", "notes_status",
	"slack_status", "crm_backend", "crm_binding", "calls_backend", "calls_binding", "slack_backend",
	"slack_binding", "thread_snapshot", "sellers", "demo_sellers"}

type sourceRule struct{ system, prefix, tool string }

var sourceRules = map[string]sourceRule{
	"accounts":      {"crm", "acc", "get_account"},
	"contacts":      {"crm", "con", "get_account"},
	"contracts":     {"crm", "ctr", "get_account"},
	"opportunities": {"crm", "opp", "list_opportunities"},
	"call_notes":    {"calls", "note", "read_call_notes"},
	"messages":      {"slack", "msg", "read_slack_messages"},
}

func oneOf(v any, choices ...string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, c := range choices {
		if s == c {
			return true
		}
	}
	return false
}

func lowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidBinding is units._valid_binding: {schema_version: 1, key: backend, identity_sha256}.
func ValidBinding(value any, key, backend string) bool {
	o, ok := value.(*strictjson.Object)
	if !ok || len(o.Keys) != 3 {
		return false
	}
	version, ok1 := o.Get("schema_version")
	named, ok2 := o.Get(key)
	identity, ok3 := o.Get("identity_sha256")
	n, isN := isInt(version)
	text, isText := identity.(string)
	return ok1 && ok2 && ok3 && isN && n.IsInt64() && n.Int64() == 1 && named == backend && isText && lowerHex64(text)
}

func asciiAlnum(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// ValidAccountID is whether an account ID has its CRM's form: acc-NNN, or acc-sf- and an 18-character
// Salesforce ID.
func ValidAccountID(accountID string, salesforce bool) bool {
	if salesforce {
		return strings.HasPrefix(accountID, "acc-sf-") && asciiAlnum(accountID[7:], 18)
	}
	if len(accountID) != 7 || !strings.HasPrefix(accountID, "acc-") {
		return false
	}
	_, ok := digits(accountID[4:])
	return ok
}

func validID(v any) bool {
	s, ok := v.(string)
	return ok && utf8.ValidString(s) && id([]rune(s))
}

type validated struct {
	groups   map[string][]*strictjson.Object
	receipts map[string]*strictjson.Object
}

func validate(evidence *strictjson.Object) validated {
	if evidence == nil || len(evidence.Keys) != len(evidenceKeys) {
		reject("Evidence has an unsupported shape.")
	}
	for _, key := range evidenceKeys {
		if _, ok := evidence.Get(key); !ok {
			reject("Evidence has an unsupported shape.")
		}
	}
	crm, calls, slack := field(evidence, "crm_backend"), field(evidence, "calls_backend"), field(evidence, "slack_backend")
	notesStatus, slackStatus := field(evidence, "notes_status"), field(evidence, "slack_status")
	if !oneOf(crm, "sqlite", "salesforce") || !oneOf(calls, "fixture", "imported", "none") ||
		!oneOf(slack, "fixture", "slack", "none") ||
		!oneOf(notesStatus, "available", "denied", "not_connected") ||
		!oneOf(slackStatus, "available", "denied", "not_requested", "not_connected", "not_selected") ||
		(calls == "none") != (notesStatus == "not_connected") ||
		(slack == "none") != (slackStatus == "not_connected") ||
		slackStatus == "not_selected" && slack != "slack" {
		reject("Evidence names an unknown source or outcome.")
	}
	salesforce, imported := crm == "salesforce", calls == "imported"
	withCalls := calls != "none"
	withSlack := oneOf(slackStatus, "available", "denied", "not_selected")
	liveSlack := withSlack && slack == "slack"
	crmBinding, callsBinding := field(evidence, "crm_binding"), field(evidence, "calls_binding")
	if imported && (!ValidBinding(callsBinding, "crm_backend", crm.(string)) || !equal(crmBinding, callsBinding)) {
		reject("Imported evidence requires one supported CRM binding for both sources.")
	}
	if liveSlack && (!ValidBinding(crmBinding, "crm_backend", crm.(string)) ||
		!ValidBinding(field(evidence, "slack_binding"), "provider", "slack") ||
		imported && !equal(callsBinding, crmBinding)) {
		reject("Live Slack evidence requires its exact source selections and bindings.")
	}
	if !liveSlack && field(evidence, "thread_snapshot") != nil {
		reject("Only a live Slack read carries a thread snapshot.")
	}
	for _, key := range []string{"sellers", "demo_sellers"} {
		sellers, ok := field(evidence, key).([]any)
		bad := !ok || len(sellers) > 200
		for _, seller := range sellers {
			s, isText := seller.(string)
			bad = bad || !isText || length(s) < 1 || length(s) > 200
		}
		if bad {
			reject("Seller identities must be a short list of names or IDs.")
		}
	}
	accountValue, briefValue := field(evidence, "account_id"), field(evidence, "brief_id")
	if !validID(accountValue) || !validID(briefValue) {
		reject("Invalid account or brief identifier.")
	}
	accountID, briefID := accountValue.(string), briefValue.(string)
	if !ValidAccountID(accountID, salesforce) {
		reject("The account identifier does not match its CRM.")
	}
	records, okRecords := field(evidence, "records").([]any)
	receipts, okReceipts := field(evidence, "receipts").([]any)
	if !okRecords || !okReceipts {
		reject("Evidence records and receipts must be lists.")
	}
	tables := map[string]bool{}
	for name := range sourceRules {
		switch {
		case name == "contracts" && !salesforce, name == "messages" && !withSlack, name == "call_notes" && !withCalls:
		default:
			tables[name] = true
		}
	}
	groups := map[string][]*strictjson.Object{}
	expected := map[string][]string{}
	for name := range tables {
		groups[name] = []*strictjson.Object{}
		expected[sourceRules[name].tool] = []string{}
	}
	seen := map[string]bool{}
	for _, value := range records {
		record := object(value)
		reference, okRef := field(record, "record_ref").(string)
		identifier, okID := field(record, "id").(string)
		if !okRef || !okID {
			reject("Record identifiers must be strings.")
		}
		parts := strings.Split(reference, "/")
		if len(parts) != 3 || !tables[parts[1]] || seen[reference] {
			reject("Unknown or duplicate source reference.")
		}
		table := parts[1]
		rule := sourceRules[table]
		if !validID(identifier) || !strings.HasPrefix(identifier, rule.prefix+"-") ||
			reference != rule.system+"/"+table+"/"+identifier {
			reject("Record reference does not identify its source.")
		}
		_, hasSlack := record.Get("slack_message")
		if hasSlack != (liveSlack && table == "messages") {
			reject("A live Slack message requires the live Slack source and its snapshot.")
		}
		_, hasManifest := record.Get("manifest")
		if hasManifest != (imported && table == "call_notes") {
			reject("An imported transcript requires its declared source binding.")
		}
		if salesforce && rule.system == "crm" {
			backend, _ := record.Get("backend")
			native, _ := record.Get("source_id")
			nativeID, ok := native.(string)
			if backend != "salesforce" || !ok || !asciiAlnum(nativeID, 18) || identifier != rule.prefix+"-sf-"+nativeID {
				reject("Salesforce record identity does not match its source.")
			}
		}
		owner := any(identifier)
		if table != "accounts" {
			owner = field(record, "account_id")
		}
		if owner != accountID {
			reject("Evidence contains a record for a different account.")
		}
		if imported && table == "call_notes" && !validImportedRecord(record, accountID, callsBinding) {
			reject("Imported transcript evidence does not match its immutable selected revision.")
		}
		seen[reference] = true
		groups[table] = append(groups[table], record)
		expected[rule.tool] = append(expected[rule.tool], reference)
	}
	if len(groups["accounts"]) != 1 {
		reject("Evidence must contain exactly one account.")
	}
	if imported && len(groups["call_notes"]) > 1 {
		reject("Imported calls hold at most one selected transcript excerpt for an account.")
	}
	if liveSlack && slackStatus != "available" {
		if len(groups["messages"]) > 0 || field(evidence, "thread_snapshot") != nil {
			reject("A Slack read that returned nothing cannot contain a snapshot or messages.")
		}
	} else if liveSlack && !validSlackSnapshot(groups["messages"], field(evidence, "thread_snapshot"), accountID,
		crmBinding, field(evidence, "slack_binding")) {
		reject("Live Slack evidence does not match its complete bounded source snapshot.")
	}
	found := map[string]*strictjson.Object{}
	receiptIDs := map[string]bool{}
	for _, value := range receipts {
		receipt := object(value)
		tool, auditID := field(receipt, "tool"), field(receipt, "audit_id")
		toolName, isText := tool.(string)
		if _, planned := expected[toolName]; !isText || !planned || found[toolName] != nil {
			reject("Evidence requires one receipt per briefing tool.")
		}
		auditText, _ := auditID.(string)
		if !validID(auditID) || receiptIDs[auditText] || !equal(field(receipt, "brief_id"), briefID) ||
			!equal(field(receipt, "arguments"), strictjson.NewObject("account_id", accountID)) {
			reject("Audit receipt does not match this brief and account.")
		}
		refs, okRefs := field(receipt, "rows").([]any)
		var rows []string
		for _, ref := range refs {
			text, ok := ref.(string)
			okRefs = okRefs && ok
			rows = append(rows, text)
		}
		count, isCount := isInt(field(receipt, "row_count"))
		if !okRefs || !isCount || !count.IsInt64() || count.Int64() != int64(len(rows)) ||
			!sameRows(rows, expected[toolName]) {
			reject("Audit receipt does not match returned records.")
		}
		denied := toolName == "read_call_notes" && notesStatus == "denied" ||
			toolName == "read_slack_messages" && slackStatus == "denied"
		unselected := toolName == "read_slack_messages" && slackStatus == "not_selected"
		status, code := "ok", 200
		switch {
		case denied:
			status, code = "error", 403
		case unselected:
			status, code = "error", 200
		}
		if !equal(field(receipt, "status"), status) || !equal(field(receipt, "http_status"), code) {
			reject("Audit receipt has an unsupported outcome.")
		}
		if (denied || unselected) && len(rows) > 0 {
			reject("A read that returned nothing must not contain records.")
		}
		receiptIDs[auditText] = true
		found[toolName] = receipt
	}
	if len(found) != len(expected) {
		reject("Evidence is missing a briefing tool receipt.")
	}
	return validated{groups: groups, receipts: found}
}

func sameRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// ---- briefing.assemble_evidence ----

// Sources is what a folder is connected to: each source's backend and binding, and the operator's sellers.
type Sources struct {
	CRMBackend, CallsBackend, SlackBackend string
	CRMBinding, CallsBinding, SlackBinding any
	Sellers                                []string
}

// Assemble is briefing.assemble_evidence's snapshot: the records and receipts of one card's reads, the
// outcome of each optional source, and the sellers its roles come from.
func Assemble(briefID, accountID, asOf string, records, receipts []any, notesStatus, slackStatus string,
	threadSnapshot any, s Sources) *strictjson.Object {
	sellers := make([]any, len(s.Sellers))
	for i, seller := range s.Sellers {
		sellers[i] = seller
	}
	demo := []any{}
	if s.CallsBackend == "fixture" || s.SlackBackend == "fixture" && slackStatus != "not_requested" {
		for _, seller := range DemoSellers {
			demo = append(demo, seller)
		}
	}
	return strictjson.NewObject("brief_id", briefID, "account_id", accountID, "as_of", asOf,
		"records", records, "receipts", receipts, "notes_status", notesStatus, "slack_status", slackStatus,
		"crm_backend", s.CRMBackend, "crm_binding", s.CRMBinding, "calls_backend", s.CallsBackend,
		"calls_binding", s.CallsBinding, "slack_backend", s.SlackBackend, "slack_binding", s.SlackBinding,
		"thread_snapshot", threadSnapshot, "sellers", sellers, "demo_sellers", demo)
}

// FixtureCRMBinding is the binding of the demo CRM: the digest of its fixed identity
// {"backend": "sqlite", "fixture_book": "renewal-book-v2", "schema_version": 1} (runtime._calls_binding).
func FixtureCRMBinding() *strictjson.Object {
	identity := strictjson.NewObject("schema_version", 1, "backend", "sqlite", "fixture_book", "renewal-book-v2")
	canonical, err := strictjson.Dumps(identity, strictjson.Prepared)
	if err != nil {
		panic(err)
	}
	return strictjson.NewObject("schema_version", 1, "crm_backend", "sqlite", "identity_sha256", digest(canonical))
}
