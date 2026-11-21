// Package slack builds the Block Kit body of a Slack alert and of a card summary from a card's facts. It
// sends nothing: no network, no clock, no credential.
//
// The account's name is the one string from a source a message carries. It goes alone in a plain_text
// object with emoji off, never in mrkdwn. Every mrkdwn object sets verbatim, holds only the connector's
// words, typed dates and numbers and links the connector built, and writes &, < and > as entities. A
// message never holds a quotation, a contact, an amount, a receipt or a digest, and has no element that
// needs Slack to call the server. Nothing is cut: a value too long for its block leaves the block out and
// says so, and a message over one of Slack's limits is refused.
package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pwngh/gtm-connector/internal/render"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// Slack's limits on a message, in characters.
const (
	MaxBlocks      = 50
	MaxHeader      = 150
	MaxText        = 3000
	MaxField       = 2000
	MaxFields      = 10
	MaxElements    = 10
	MaxMessageText = 40000
)

// Text is a text object.
type Text struct {
	Type     string `json:"type"`
	Emoji    *bool  `json:"emoji,omitempty"`
	Verbatim *bool  `json:"verbatim,omitempty"`
	Text     string `json:"text"`
}

// Block is a header, section, divider or context block.
type Block struct {
	Type     string `json:"type"`
	Text     *Text  `json:"text,omitempty"`
	Fields   []Text `json:"fields,omitempty"`
	Elements []Text `json:"elements,omitempty"`
}

// Message is an alert's body: the notification text, and blocks.
type Message struct {
	Text        string  `json:"text"`
	Mrkdwn      bool    `json:"mrkdwn"`
	UnfurlLinks bool    `json:"unfurl_links"`
	UnfurlMedia bool    `json:"unfurl_media"`
	Blocks      []Block `json:"blocks"`
}

// Summary is a card summary's body: blocks only.
type Summary struct {
	Blocks []Block `json:"blocks"`
}

var off, on = false, true

// plain is a plain_text object with emoji off: Slack formats none of it.
func plain(s string) Text { return Text{Type: "plain_text", Emoji: &off, Text: s} }

// mrkdwn is a mrkdwn object of the connector's own words, which Slack links nothing in by itself.
func mrkdwn(s string) Text { return Text{Type: "mrkdwn", Verbatim: &on, Text: s} }

// escape writes &, < and > as the entities Slack reads.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// link is a mrkdwn link to an address the connector built, with the connector's label.
func link(address, label string) string { return "<" + address + "|" + label + ">" }

// Length is how many characters Slack counts in s: UTF-16 code units, with &, < and > as the entities
// they are sent as.
func Length(s string) int {
	n := 0
	for _, c := range s {
		switch {
		case c == '&':
			n += 5
		case c == '<' || c == '>':
			n += 4
		case c >= 0x10000:
			n += 2
		default:
			n++
		}
	}
	return n
}

var marks = map[render.Status]string{render.AtRisk: ":red_circle:", render.Changed: ":large_orange_diamond:",
	render.OnTrack: ":white_check_mark:", render.ClosedWon: ":lock:", render.ClosedLost: ":lock:"}

const (
	unknownMeans = "Unknown means a source this rule needs was not read. It is not the same as no risk."
	whoMayOpen   = "Sign-in required. You see the card only if this deal is granted to you. No quotation is sent to Slack."
	sharedLine   = "Some evidence on this card is also on another deal of this account."
	nameTooLong  = "The account name is too long to show here."
)

// name is the block that shows the account's name, alone and inert, or says it is too long; none for an
// empty name.
func name(f render.Facts) []Block {
	s := f.Deal.AccountName
	if !strings.ContainsFunc(s, func(c rune) bool { return !unicodetab.IsSpace(c) }) {
		return nil
	}
	if Length(s) > MaxText {
		return []Block{{Type: "context", Elements: []Text{mrkdwn(nameTooLong)}}}
	}
	t := plain(s)
	return []Block{{Type: "section", Text: &t}}
}

// renewal is how far off the countdown is, as a field says it.
func renewal(f render.Facts) string {
	c := f.Countdown
	what := map[string]string{"renewal": "renewal date", "contract_end": "contract end"}[c.CountsTo]
	switch {
	case c.Days > 0:
		return fmt.Sprintf("%s, to %s %s", render.Count(c.Days, "day", "days"), what, c.Date)
	case c.Days < 0:
		return fmt.Sprintf("%s ago, %s %s", render.Count(-c.Days, "day", "days"), what, c.Date)
	}
	return fmt.Sprintf("Today, %s %s", what, c.Date)
}

// renewalSentence is the countdown as the notification says it.
func renewalSentence(f render.Facts) string {
	c := f.Countdown
	switch {
	case c.Days > 0:
		return "Renewal in " + render.Count(c.Days, "day", "days") + "."
	case c.Days < 0:
		return "Renewal date passed " + render.Count(-c.Days, "day", "days") + " ago."
	}
	return "Renewal today."
}

func ruleNames(rules []render.Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Name()
	}
	return out
}

// raised is one raised rule's section: its name, the connector's sentence and threshold, and a link to the
// first unit it cites.
func raised(f render.Facts, r render.Rule) Block {
	text := ":red_circle: *" + r.Name() + "*\n" + escape(r.Sentence)
	if r.Threshold != "" {
		text += " Threshold: " + escape(r.Threshold) + "."
	}
	t := mrkdwn(text + " " + link(f.UnitLink(r.Cites[0]), "See the source"))
	return Block{Type: "section", Text: &t}
}

// unknown is the sections of the unknown rules, each with the connector's reasons, and the line that says
// what unknown means.
func unknown(f render.Facts) []Block {
	var out []Block
	for _, r := range f.Unknown {
		var reasons []string
		for _, u := range f.Because(r.Code) {
			reasons = append(reasons, escape(u.Reason))
		}
		t := mrkdwn(":grey_question: *" + r.Name() + ": unknown*\n" + strings.Join(reasons, " "))
		out = append(out, Block{Type: "section", Text: &t})
	}
	if len(out) > 0 {
		out = append(out, Block{Type: "context", Elements: []Text{mrkdwn(unknownMeans)}})
	}
	return out
}

// news is what is new on the card, counted, since the card before.
func news(c render.Changes) string {
	var parts []string
	for _, p := range []struct {
		n         int
		one, many string
	}{
		{c.Customer, "customer sentence", "customer sentences"},
		{c.Seller, "seller sentence", "seller sentences"},
		{c.NoRole, "sentence with no established speaker role", "sentences with no established speaker role"},
		{c.Values, "record value", "record values"},
	} {
		if p.n > 0 {
			parts = append(parts, render.Count(p.n, p.one, p.many))
		}
	}
	return render.List(parts) + " since the card of " + render.Minute(c.Since) + "."
}

func section(s string) Block { t := mrkdwn(s); return Block{Type: "section", Text: &t} }

// fields is the countdown and the card's time, after the fields in first and before those in later.
func fields(f render.Facts, first, later []Text) Block {
	out := slices.Clone(first)
	if f.Countdown != nil {
		out = append(out, mrkdwn("*Renewal*\n"+renewal(f)))
	}
	out = append(out, later...)
	out = append(out, mrkdwn("*Card*\n"+render.Minute(f.PublishedAt)))
	return Block{Type: "section", Fields: out}
}

// Alert is the message a channel gets when the deal's status changes: at risk, changed, on track, or
// closed. It is built from the event's facts and sends nothing.
func Alert(e render.Event, site render.Site) ([]byte, error) {
	f, err := render.Read(e, site)
	if err != nil {
		return nil, err
	}
	status := f.Deal.Status
	header := plain(status.Word() + ": " + f.Deal.ID)
	blocks := []Block{{Type: "header", Text: &header}}
	blocks = append(blocks, name(f)...)
	blocks = append(blocks, fields(f, nil, nil), Block{Type: "divider"})

	notice := []string{status.Word() + ": " + f.Deal.ID + "."}
	if f.Countdown != nil && !status.Closed() {
		notice = append(notice, renewalSentence(f))
	}
	open := "Open the card"
	switch status {
	case render.AtRisk:
		for _, r := range f.Raised {
			blocks = append(blocks, raised(f, r))
		}
		notice = append(notice, strings.Join(ruleNames(f.Raised), ", ")+".")
	case render.Changed:
		if f.Changes != nil && f.Changes.Any() {
			blocks = append(blocks, section(":large_orange_diamond: *New on the card*\n"+news(*f.Changes)))
		}
	case render.OnTrack:
		text := ""
		if cleared := ruleNames(f.Cleared); len(cleared) > 0 {
			verb := " no longer holds. "
			if len(cleared) > 1 {
				verb = " no longer hold. "
			}
			text = render.List(cleared) + verb
		}
		blocks = append(blocks, section(":white_check_mark: *No flag is raised*\n"+text+"Every connected source was read."))
	default:
		blocks = append(blocks, section(":lock: *Final card sealed*\n"+render.Minute(f.PublishedAt)+". Reads have stopped."))
		notice = append(notice, "Final card sealed.")
		open = "Open the final card"
	}
	if !status.Closed() {
		blocks = append(blocks, unknown(f)...)
		if len(f.Unknown) > 0 {
			notice = append(notice, render.Count(len(f.Unknown), "rule", "rules")+" unknown.")
		}
	}
	if f.Deal.Shared {
		blocks = append(blocks, Block{Type: "context", Elements: []Text{mrkdwn(sharedLine)}})
	}
	blocks = append(blocks, Block{Type: "divider"}, section(link(f.CardLink(), open)),
		Block{Type: "context", Elements: []Text{mrkdwn(whoMayOpen), mrkdwn(link(f.GuideLink(), "What do these flags mean?"))}})
	m := Message{Text: escape(strings.Join(notice, " ")), Blocks: blocks}
	if err := check(m.Text, blocks); err != nil {
		return nil, err
	}
	return encode(m)
}

// Summarize is what a channel configured for the deal's alerts sees under a pasted link to the deal: no more
// than an alert may hold.
func Summarize(e render.Event, site render.Site) ([]byte, error) {
	f, err := render.Read(e, site)
	if err != nil {
		return nil, err
	}
	status := f.Deal.Status
	var flags []Text
	if len(f.Raised) > 0 && !status.Closed() {
		flags = []Text{mrkdwn("*Flags*\n" + strings.Join(ruleNames(f.Raised), ", "))}
	}
	blocks := append(name(f), fields(f, []Text{mrkdwn("*Status*\n" + marks[status] + " " + status.Word())}, flags))
	if len(f.Unread) > 0 && !status.Closed() {
		var sources []string
		for _, u := range f.Unread {
			if s := render.SourceName(u.Source); !slices.Contains(sources, s) {
				sources = append(sources, s)
			}
		}
		text := ":grey_question: Not read: " + render.List(sources) + "."
		if names := ruleNames(f.Unknown); len(names) == 1 {
			text += " " + names[0] + " is unknown."
		} else if len(names) > 1 {
			text += " " + render.List(names) + " are unknown."
		}
		blocks = append(blocks, Block{Type: "context", Elements: []Text{mrkdwn(text)}})
	}
	if f.Deal.Shared {
		blocks = append(blocks, Block{Type: "context", Elements: []Text{mrkdwn(sharedLine)}})
	}
	blocks = append(blocks, Block{Type: "context", Elements: []Text{
		mrkdwn("Sign-in required. No quotation is sent to Slack."),
		mrkdwn(link(f.CardLink()+"#flags", "What do these mean on this card?"))}})
	if err := check("", blocks); err != nil {
		return nil, err
	}
	return encode(Summary{Blocks: blocks})
}

// ErrLimit is a message over one of Slack's limits. Nothing is cut to fit.
var ErrLimit = errors.New("slack: the message is over a limit of Slack's")

// check holds a message to Slack's limits: blocks per message, the header's length, fields and elements per
// block, and every text object 1 to its limit in characters.
func check(notification string, blocks []Block) error {
	if len(blocks) > MaxBlocks || Length(notification) > MaxMessageText {
		return ErrLimit
	}
	within := func(t Text, max int) bool { n := Length(t.Text); return n >= 1 && n <= max }
	for _, b := range blocks {
		switch b.Type {
		case "header":
			if b.Text == nil || b.Text.Type != "plain_text" || !within(*b.Text, MaxHeader) {
				return ErrLimit
			}
		case "section":
			if b.Text == nil && len(b.Fields) == 0 || len(b.Fields) > MaxFields ||
				b.Text != nil && !within(*b.Text, MaxText) {
				return ErrLimit
			}
			for _, t := range b.Fields {
				if !within(t, MaxField) {
					return ErrLimit
				}
			}
		case "context":
			if len(b.Elements) == 0 || len(b.Elements) > MaxElements {
				return ErrLimit
			}
			for _, t := range b.Elements {
				if !within(t, MaxText) {
					return ErrLimit
				}
			}
		}
	}
	return nil
}

// encode writes a body as JSON in its fields' order, with no HTML escaping, so the same input gives the
// same bytes.
func encode(v any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
