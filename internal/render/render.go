// Package render reads what a rendering of a card outside the web app needs: the deal, its rule results, and
// facts taken from the card's units, gaps and lines. It never reads the card's text. The Slack alert
// (render/slack) and the webhook body (render/webhook) are built from Facts and nothing else.
//
// Read refuses an event that does not hold together: an unknown rule code, a status its rules contradict, a
// citation of a unit the card does not hold, an unknown rule whose sources were all read, or a rule sentence
// that is not the connector's own words. A rule's sentence and threshold are the connector's words: ASCII
// letters, digits, spaces and plain punctuation, never a sentence or a contact of the card, and the pricing
// or seat conflict's sentence holds no digit, since a figure in it would be taken from a sentence.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Status is a deal's status: red, amber, green, or closed.
type Status string

const (
	AtRisk     Status = "red"
	Changed    Status = "amber"
	OnTrack    Status = "green"
	ClosedWon  Status = "closed_won"
	ClosedLost Status = "closed_lost"
)

var statusWords = map[Status]string{AtRisk: "At risk", Changed: "Changed", OnTrack: "On track",
	ClosedWon: "Closed won", ClosedLost: "Closed lost"}

// Word is the status as a message says it first.
func (s Status) Word() string { return statusWords[s] }

// Closed is whether the status is a closed deal's.
func (s Status) Closed() bool { return s == ClosedWon || s == ClosedLost }

// State is a rule's result on a card.
type State string

const (
	Raised  State = "raised"
	Clear   State = "clear"
	Unknown State = "unknown"
)

// RuleCodes is every rule, in the order a message lists them.
var RuleCodes = []string{"legal_security_blocker", "pricing_seat_conflict", "customer_silence",
	"close_date_slippage", "single_threaded"}

var ruleNames = map[string]string{
	"legal_security_blocker": "Legal or security blocker",
	"pricing_seat_conflict":  "Pricing or seat conflict",
	"customer_silence":       "Customer silence",
	"close_date_slippage":    "Close-date slippage",
	"single_threaded":        "Single-threaded deal",
}

// ruleNeeds is the sources each rule reads. A rule is unknown only when one of them was not read.
var ruleNeeds = map[string][]string{
	"legal_security_blocker": {"calls", "slack"},
	"pricing_seat_conflict":  {"crm", "calls", "slack"},
	"customer_silence":       {"calls", "slack"},
	"close_date_slippage":    {"crm"},
	"single_threaded":        {"calls", "slack"},
}

// RuleName is a rule's name as the guide and the card print it.
func RuleName(code string) string { return ruleNames[code] }

// Rule is one rule's result on the card, from the rule engine.
type Rule struct {
	Code     string
	State    State
	Previous State // its result on the card before; empty when it had none
	// Sentence, Threshold and Cites are set on a raised rule only: the connector's sentence with the
	// rule's numbers, ending in a full stop; the threshold it crossed, such as "2 moves", or empty; and the
	// units it cites, at least one.
	Sentence, Threshold string
	Cites               []int
}

// Name is the rule's name.
func (r Rule) Name() string { return ruleNames[r.Code] }

// Deal is the deal a card is of.
type Deal struct {
	// ID is the deal's ID: 1 to 64 ASCII letters, digits, '.', '_' or '-', so it carries no markup.
	ID string
	// AccountName is the account's neutralized name. Slack shows it as plain text; a webhook never
	// carries it.
	AccountName      string
	Status, Previous Status // Previous is empty when the deal had no status before
	// Shared is whether some unit on the card is also on another deal of the account.
	Shared bool
}

// Event is what a rendering is made from: the deal, its current card, the card before it when there was
// one, and every rule's result on the current card.
type Event struct {
	Deal     Deal
	Card     authz.CardView
	Previous *authz.CardView
	Rules    []Rule
}

// Site is where links point: the server's public address, such as https://gtm.example.internal, and the
// revision the binary was built from, which pins the guide links.
type Site struct {
	Address, Revision string
}

// Unread is a source the card did not read, with the connector's reason.
type Unread struct {
	Source, Code, Reason string
}

// unreadGaps are the gaps that say a whole source was not read, with its source and the connector's reason.
var unreadGaps = map[string]Unread{
	"calls_not_connected": {"calls", "calls_not_connected", "Calls are not connected."},
	"calls_denied":        {"calls", "calls_denied", "Calls were not read: the grant lacks calls:read."},
	"slack_not_connected": {"slack", "slack_not_connected", "Slack is not connected."},
	"slack_not_selected":  {"slack", "slack_not_selected", "Slack was not read: no thread is selected for this account."},
	"slack_not_requested": {"slack", "slack_not_requested", "Slack was not read: it was not requested for this card."},
	"slack_denied":        {"slack", "slack_denied", "Slack was not read: the grant lacks slack:read."},
	"calls_not_matched":   {"calls", "calls_not_matched", "Calls were not read: the account's transcript is not matched to this deal."},
	"slack_not_matched":   {"slack", "slack_not_matched", "Slack was not read: the account's thread is not matched to this deal."},
}

// SourceName is a source as a message names it.
func SourceName(source string) string {
	return map[string]string{"crm": "CRM", "calls": "Calls", "slack": "Slack"}[source]
}

// Changes counts what is on the card and was not on the card before it.
type Changes struct {
	SinceCard string
	Since     time.Time
	// Sentences counts the new sentence units by role: customer, seller and unknown.
	Customer, Seller, NoRole int
	// Values counts the value lines whose value differs from every line of the same code before.
	Values int
}

// Any is whether anything is new.
func (c Changes) Any() bool { return c.Customer+c.Seller+c.NoRole+c.Values > 0 }

// Facts is everything a rendering may say, read from an Event.
type Facts struct {
	Deal                Deal
	CardID, AsOf        string
	Digest              string // the card's SHA-256, in hex
	PublishedAt         time.Time
	Countdown           *evidence.Countdown // nil when the card counts to no date
	Rules               []Rule              // every rule given, in RuleCodes order
	Raised, Unknown     []Rule
	Cleared             []Rule // raised on the card before, clear now
	Unread              []Unread
	Changes             *Changes // nil without a card before
	Address, Revision   string
	unknownBecause      map[string][]Unread
	unitCount           int
	sentences, contacts []string
}

// Because is the sources an unknown rule needed and the card did not read.
func (f Facts) Because(code string) []Unread { return f.unknownBecause[code] }

// CardLink is the deal's page in the web app.
func (f Facts) CardLink() string { return f.Address + "/deals/" + f.Deal.ID }

// PinnedLink is the deal's page opened on this card, whose unit numbers the facts cite.
func (f Facts) PinnedLink() string { return f.CardLink() + "?card=" + url.QueryEscape(f.CardID) }

// UnitLink is the deal's page opened on this card at unit n.
func (f Facts) UnitLink(n int) string { return fmt.Sprintf("%s&unit=%d", f.PinnedLink(), n) }

// APILink is this card in the API. Unit n of it, and the receipt of its read, are at APILink()+"/units/n".
func (f Facts) APILink() string { return f.Address + "/api/v1/cards/" + url.PathEscape(f.CardID) }

// GuideLink is the guide's section on flags at the revision the binary was built from.
func (f Facts) GuideLink() string {
	return "https://github.com/pwngh/gtm-connector/blob/" + f.Revision + "/docs/deals.md#what-flags-a-deal"
}

// idChar is whether c may be in an ID or a revision.
func idChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// ValidID is whether s is an ID a message may carry as it is: 1 to 64 ASCII letters, digits, '.', '_' or
// '-', not starting with '.'.
func ValidID(s string) bool {
	return s != "" && len(s) <= 64 && !strings.HasPrefix(s, ".") && !strings.ContainsFunc(s, func(c rune) bool { return !idChar(c) })
}

// wordChar is whether c may be in the connector's words on a rule: no character that Slack's markup or
// HTML reads, and nothing outside ASCII.
func wordChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(" .,;:()'/-%", c)
}

// ErrEvent is an event that does not hold together.
var ErrEvent = errors.New("render: the event does not hold together")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEvent, fmt.Sprintf(format, args...))
}

// address checks a public address: http or https, a host, and no user, path, query or fragment.
func address(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(raw, "|<> \"'`\\") {
		return "", refuse("the public address is not an http or https origin")
	}
	return u.Scheme + "://" + u.Host, nil
}

// unitBody is the fields of a unit's body a rendering compares.
type unitBody struct {
	Role, Text string
}

func body(u authz.CardView, i int) unitBody {
	var b unitBody
	_ = json.Unmarshal(u.Units[i].Body, &b)
	return b
}

// countdown is the card's countdown: the renewal date line's, else the contract end line's.
func countdown(lines []evidence.Line) *evidence.Countdown {
	var end *evidence.Countdown
	for _, l := range lines {
		if l.Countdown == nil {
			continue
		}
		if l.Code == "renewal_date" {
			c := *l.Countdown
			return &c
		}
		if end == nil {
			c := *l.Countdown
			end = &c
		}
	}
	return end
}

// changes counts what the card holds that the card before did not: sentence units by role, by record and
// sentence, and value lines, by the date a countdown counts to or by their value.
func changes(cur, prev authz.CardView) Changes {
	key := func(l evidence.Line) string {
		if l.Countdown != nil {
			return l.Code + "\x00" + l.Countdown.Date
		}
		return l.Code + "\x00" + l.Text
	}
	seen := map[string]bool{}
	for i, u := range prev.Units {
		if u.Kind == "sentence" {
			seen[u.RecordRef+"\x00"+body(prev, i).Text] = true
		}
	}
	for _, l := range prev.Lines {
		if l.Section == "value" {
			seen[key(l)] = true
		}
	}
	c := Changes{SinceCard: prev.Card.ID, Since: prev.Card.PublishedAt.UTC()}
	for i, u := range cur.Units {
		b := body(cur, i)
		if u.Kind != "sentence" || seen[u.RecordRef+"\x00"+b.Text] {
			continue
		}
		switch b.Role {
		case "customer":
			c.Customer++
		case "seller":
			c.Seller++
		default:
			c.NoRole++
		}
	}
	for _, l := range cur.Lines {
		if l.Section == "value" && !seen[key(l)] {
			c.Values++
		}
	}
	return c
}

// Distinct is whether a contact's field can be told from the connector's own words: it holds two letters
// or digits or more. A name such as "a" cannot.
func Distinct(s string) bool {
	n := 0
	for _, c := range s {
		if unicodetab.ReWord(c) {
			n++
		}
	}
	return n > 1
}

// sourceStrings are the card's sentences of two words or more and its contacts' Distinct names and titles:
// what a rule's words must not hold. A one-word sentence, such as "you", cannot be told from the connector's
// own words.
func sourceStrings(card authz.CardView) (sentences, contacts []string) {
	for i, u := range card.Units {
		if t := body(card, i).Text; u.Kind == "sentence" && len(strings.Fields(t)) > 1 {
			sentences = append(sentences, t)
		}
	}
	for _, l := range card.Lines {
		if l.Contact == nil {
			continue
		}
		for _, s := range []*string{l.Contact.Name, l.Contact.Title} {
			if s != nil && Distinct(*s) {
				contacts = append(contacts, *s)
			}
		}
	}
	return sentences, contacts
}

// Holds is whether s holds source as a whole: with no letter, digit or underscore right before or after it.
func Holds(s, source string) bool {
	for from := 0; from <= len(s); {
		at := strings.Index(s[from:], source)
		if at < 0 {
			return false
		}
		at += from
		before, _ := utf8.DecodeLastRuneInString(s[:at])
		after, _ := utf8.DecodeRuneInString(s[at+len(source):])
		if !unicodetab.ReWord(before) && !unicodetab.ReWord(after) {
			return true
		}
		from = at + 1
	}
	return false
}

// words checks a rule's sentence or threshold: the connector's words, holding no sentence or contact of the
// card.
func (f Facts) words(code, what, s string) error {
	if strings.ContainsFunc(s, func(c rune) bool { return !wordChar(c) }) || len(s) > 500 {
		return refuse("the %s of %s is not the connector's words", what, code)
	}
	if code == "pricing_seat_conflict" && strings.ContainsFunc(s, func(c rune) bool { return c >= '0' && c <= '9' }) {
		return refuse("the %s of %s holds a figure", what, code)
	}
	for _, source := range append(slices.Clone(f.sentences), f.contacts...) {
		if Holds(s, source) {
			return refuse("the %s of %s holds a string of the card's sources", what, code)
		}
	}
	return nil
}

// Read checks an event and gives the facts a rendering is made from.
func Read(e Event, site Site) (Facts, error) {
	addr, err := address(site.Address)
	if err != nil {
		return Facts{}, err
	}
	if !ValidID(site.Revision) {
		return Facts{}, refuse("the revision is not a revision")
	}
	d := e.Deal
	if !ValidID(d.ID) {
		return Facts{}, refuse("the deal ID is not 1 to 64 letters, digits, '.', '_' or '-'")
	}
	if statusWords[d.Status] == "" || d.Previous != "" && statusWords[d.Previous] == "" {
		return Facts{}, refuse("the deal's status is not a status")
	}
	card := e.Card
	if !ValidID(card.Card.ID) || card.Lines == nil {
		return Facts{}, refuse("the card has no ID or no lines")
	}
	f := Facts{Deal: d, CardID: card.Card.ID, AsOf: card.AsOf, Digest: fmt.Sprintf("%x", card.Card.SHA256),
		PublishedAt: card.Card.PublishedAt.UTC(), Countdown: countdown(card.Lines), Address: addr,
		Revision: site.Revision, unknownBecause: map[string][]Unread{}, unitCount: len(card.Units)}
	f.sentences, f.contacts = sourceStrings(card)
	unread := map[string][]Unread{}
	for _, g := range card.Gaps {
		if u, ok := unreadGaps[g.Code]; ok && g.Disposition == "not_read" {
			f.Unread = append(f.Unread, u)
			unread[u.Source] = append(unread[u.Source], u)
		}
	}
	given := map[string]Rule{}
	for _, r := range e.Rules {
		if ruleNames[r.Code] == "" {
			return Facts{}, refuse("%q is not a rule", r.Code)
		}
		if _, twice := given[r.Code]; twice {
			return Facts{}, refuse("%s is given twice", r.Code)
		}
		if r.State != Raised && r.State != Clear && r.State != Unknown ||
			r.Previous != "" && r.Previous != Raised && r.Previous != Clear && r.Previous != Unknown {
			return Facts{}, refuse("%s has no state", r.Code)
		}
		r.Cites = slices.Clone(r.Cites)
		given[r.Code] = r
	}
	for _, code := range RuleCodes {
		r, ok := given[code]
		if !ok {
			continue
		}
		f.Rules = append(f.Rules, r)
		if r.State != Raised && (r.Sentence != "" || r.Threshold != "" || len(r.Cites) > 0) {
			return Facts{}, refuse("%s is not raised and carries a sentence or citations", code)
		}
		switch r.State {
		case Raised:
			if !strings.HasSuffix(r.Sentence, ".") || len(r.Cites) == 0 {
				return Facts{}, refuse("%s is raised without a sentence or a citation", code)
			}
			if err := f.words(code, "sentence", r.Sentence); err != nil {
				return Facts{}, err
			}
			if err := f.words(code, "threshold", r.Threshold); err != nil {
				return Facts{}, err
			}
			for _, n := range r.Cites {
				if n < 1 || n > f.unitCount {
					return Facts{}, refuse("%s cites unit %d, which the card does not hold", code, n)
				}
			}
			f.Raised = append(f.Raised, r)
		case Unknown:
			for _, source := range ruleNeeds[code] {
				f.unknownBecause[code] = append(f.unknownBecause[code], unread[source]...)
			}
			if len(f.unknownBecause[code]) == 0 {
				return Facts{}, refuse("%s is unknown, and every source it needs was read", code)
			}
			f.Unknown = append(f.Unknown, r)
		case Clear:
			if r.Previous == Raised {
				f.Cleared = append(f.Cleared, r)
			}
		}
	}
	if e.Previous != nil {
		if e.Previous.Card.AccountRef != card.Card.AccountRef || e.Previous.Lines == nil ||
			!ValidID(e.Previous.Card.ID) || !e.Previous.Card.PublishedAt.Before(card.Card.PublishedAt) {
			return Facts{}, refuse("the card before is not an earlier card of this account with lines")
		}
		c := changes(card, *e.Previous)
		f.Changes = &c
	}
	switch {
	case d.Status == AtRisk && len(f.Raised) == 0:
		return Facts{}, refuse("an at-risk deal has no raised rule")
	case !d.Status.Closed() && d.Status != AtRisk && len(f.Raised) > 0:
		return Facts{}, refuse("a deal with a raised rule is not at risk")
	case d.Status == Changed && len(f.Unknown) == 0 && (f.Changes == nil || !f.Changes.Any()):
		return Facts{}, refuse("a changed deal has nothing new and no unknown rule")
	case d.Status == OnTrack && (len(f.Unknown) > 0 || slices.ContainsFunc(f.Unread, connected)):
		return Facts{}, refuse("a deal with an unknown rule or an unread source is not on track")
	}
	return f, nil
}

// connected is whether an unread source was connected and still not read.
func connected(u Unread) bool { return !strings.HasSuffix(u.Code, "_not_connected") }

// List joins names as a sentence does: "A", "A and B", "A, B and C".
func List(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Count is a count and its noun: "1 rule", "2 rules".
func Count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Minute is a time as a message prints it: 2027-01-15 15:00 UTC.
func Minute(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }
