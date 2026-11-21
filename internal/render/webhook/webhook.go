// Package webhook builds the signed body a receiver's own system gets when a deal's risk changes, its card
// changes or it closes, from a card's facts. It sends nothing: no network, no clock, no credential beyond
// the secret it is handed.
//
// A body holds codes, numbers, dates, IDs and links the connector built: never a name, a title, a sentence,
// an amount or a rule's sentence. A receiver that needs them calls the API with its own sign-in, where its
// grant is checked. The fields are in a fixed order, so the same event gives the same bytes.
package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/render"
)

// Type is what happened.
type Type string

const (
	RiskChanged Type = "risk.changed" // a rule started or stopped failing
	CardChanged Type = "card.changed" // a refresh published a new card
	DealClosed  Type = "deal.closed"  // the final card was sealed
)

// Delivery is the event as its receiver tells one from another: an ID that stays the same across retries,
// its type, and when it happened.
type Delivery struct {
	ID         string
	Type       Type
	OccurredAt time.Time
}

type dealPart struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	PreviousStatus *string `json:"previous_status"`
}

type cardPart struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	AsOf   string `json:"as_of"`
}

type renewalPart struct {
	Days     int    `json:"days"`
	Date     string `json:"date"`
	CountsTo string `json:"counts_to"`
}

type rulePart struct {
	Code          string  `json:"code"`
	State         string  `json:"state"`
	PreviousState *string `json:"previous_state"`
	Units         []int   `json:"units"`
	Help          string  `json:"help"`
}

type unreadPart struct {
	Source string `json:"source"`
	Code   string `json:"code"`
}

type sentencesPart struct {
	Customer int `json:"customer"`
	Seller   int `json:"seller"`
	Unknown  int `json:"unknown"`
}

type changesPart struct {
	SinceCard string        `json:"since_card"`
	Since     string        `json:"since"`
	Sentences sentencesPart `json:"sentences"`
	Values    int           `json:"values"`
}

type linksPart struct {
	Card string `json:"card"`
	API  string `json:"api"`
}

type body struct {
	ID         string       `json:"id"`
	Type       Type         `json:"type"`
	OccurredAt string       `json:"occurred_at"`
	Deal       dealPart     `json:"deal"`
	Card       cardPart     `json:"card"`
	Renewal    *renewalPart `json:"renewal"`
	Rules      []rulePart   `json:"rules"`
	Unread     []unreadPart `json:"unread"`
	Changes    *changesPart `json:"changes"`
	Links      linksPart    `json:"links"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Body is the JSON body of one delivery. A deal.closed event needs a closed deal; a card.changed event, an
// open deal and the card before; a risk.changed event, an open deal whose status or a rule's state moved.
func Body(d Delivery, e render.Event, site render.Site) ([]byte, error) {
	f, err := render.Read(e, site)
	if err != nil {
		return nil, err
	}
	if !render.ValidID(d.ID) || d.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: the delivery has no ID or no time", render.ErrEvent)
	}
	moved := f.Deal.Status != f.Deal.Previous
	for _, r := range f.Rules {
		moved = moved || r.State != r.Previous
	}
	closed := f.Deal.Status.Closed()
	switch {
	case d.Type == DealClosed && !closed,
		d.Type == CardChanged && (closed || f.Changes == nil),
		d.Type == RiskChanged && (closed || !moved),
		d.Type != DealClosed && d.Type != CardChanged && d.Type != RiskChanged:
		return nil, fmt.Errorf("%w: a %q event does not fit the deal", render.ErrEvent, d.Type)
	}
	b := body{ID: d.ID, Type: d.Type, OccurredAt: stamp(d.OccurredAt),
		Deal:   dealPart{f.Deal.ID, string(f.Deal.Status), optional(string(f.Deal.Previous))},
		Card:   cardPart{f.CardID, f.Digest, f.AsOf},
		Rules:  []rulePart{},
		Unread: []unreadPart{},
		Links:  linksPart{Card: f.PinnedLink(), API: f.APILink()}}
	if c := f.Countdown; c != nil {
		b.Renewal = &renewalPart{c.Days, c.Date, c.CountsTo}
	}
	for _, r := range f.Rules {
		units := append([]int{}, r.Cites...)
		b.Rules = append(b.Rules, rulePart{r.Code, string(r.State), optional(string(r.Previous)), units, f.GuideLink()})
	}
	for _, u := range f.Unread {
		b.Unread = append(b.Unread, unreadPart{u.Source, u.Code})
	}
	if c := f.Changes; c != nil {
		b.Changes = &changesPart{c.SinceCard, stamp(c.Since), sentencesPart{c.Customer, c.Seller, c.NoRole}, c.Values}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Header is the header that carries a delivery's signature.
const Header = "Gtm-Signature"

// Tolerance is how far from the receiver's clock a signed timestamp may be.
const Tolerance = 5 * time.Minute

// MinSecret is the shortest secret Sign takes, in bytes.
const MinSecret = 32

// ErrSecret is a secret shorter than MinSecret.
var ErrSecret = errors.New("webhook: the secret is shorter than 32 bytes")

func mac(secret []byte, unix int64, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(strconv.FormatInt(unix, 10) + "."))
	h.Write(body)
	return h.Sum(nil)
}

// Sign is the value of the signature header for a body sent at a time: "t=" and the time in Unix seconds,
// then ",v1=" and the hex HMAC-SHA-256, under the secret, of the time in Unix seconds, a full stop and the
// body's exact bytes.
func Sign(secret []byte, at time.Time, body []byte) (string, error) {
	if len(secret) < MinSecret {
		return "", ErrSecret
	}
	unix := at.Unix()
	return "t=" + strconv.FormatInt(unix, 10) + ",v1=" + hex.EncodeToString(mac(secret, unix, body)), nil
}

// The refusals of Verify.
var (
	ErrMalformed = errors.New("webhook: the signature header is not t=SECONDS,v1=HEX")
	ErrStale     = errors.New("webhook: the signed time is outside the tolerance")
	ErrSignature = errors.New("webhook: the signature does not match the body")
)

// Verify is the check a receiver makes: the header has exactly the form Sign writes, its time is within
// tolerance of now either way, and its HMAC is the body's under the secret, compared in constant time.
func Verify(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) error {
	t, v, ok := strings.Cut(header, ",")
	t, okT := strings.CutPrefix(t, "t=")
	v, okV := strings.CutPrefix(v, "v1=")
	unix, err := strconv.ParseInt(t, 10, 64)
	sum, errHex := hex.DecodeString(v)
	if !ok || !okT || !okV || err != nil || errHex != nil || len(sum) != sha256.Size {
		return ErrMalformed
	}
	if age := now.Sub(time.Unix(unix, 0)); age > tolerance || age < -tolerance {
		return ErrStale
	}
	if !hmac.Equal(sum, mac(secret, unix, body)) {
		return ErrSignature
	}
	return nil
}
