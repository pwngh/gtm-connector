// Package rendertest gives the recorded cards in testdata/oracle/cards as the card views a rendering takes,
// with the source strings no rendering may carry.
package rendertest

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/render"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Families is every file of recorded cards: 450 cards in all.
var Families = []string{"books_calls", "books_demo", "books_salesforce", "books_slack", "cases", "demo",
	"slack_threads", "varied_demo"}

// Recorded is one recorded card as a rendering takes it.
type Recorded struct {
	Family, Key string
	View        authz.CardView
	// Name is the account's name, neutralized as the server shows it.
	Name string
	// Sentences is the text of every sentence unit of two words or more, and Contacts every contact's name,
	// title and email as the card shows them that is render.Distinct: what no message may hold. A one-word
	// sentence, such as "you", or a name such as "a" cannot be told from the connector's words.
	Sentences, Contacts []string
}

// shown is a record's field neutralized on one line, or empty.
func shown(record *strictjson.Object, key string) string {
	value, _ := record.Get(key)
	s, _ := value.(string)
	safe, _, err := evidence.SafeText(s)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(safe), " ")
}

// Cards is the recorded cards of one family, each published at 15:00:04 UTC on its as_of day.
func Cards(t testing.TB, family string) []Recorded {
	t.Helper()
	var out []Recorded
	for _, line := range oracletest.Lines(t, "cards/"+family+".jsonl.gz") {
		var row struct {
			Key    string `json:"key"`
			Card   string `json:"card"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		value, err := strictjson.LoadsText(row.Card)
		if err != nil {
			t.Fatal(err)
		}
		card := value.(*strictjson.Object)
		get := func(key string) any { v, _ := card.Get(key); return v }
		ev := get("evidence").(*strictjson.Object)
		_, _, _, lines, err := evidence.Build(ev)
		if err != nil {
			t.Fatalf("%s %s: %v", family, row.Key, err)
		}
		digest, err := hex.DecodeString(row.SHA256)
		if err != nil || len(digest) != 32 {
			t.Fatalf("%s %s: digest: %v", family, row.Key, err)
		}
		asOf := get("as_of").(string)
		published, err := time.Parse(time.RFC3339, asOf+"T15:00:04Z")
		if err != nil {
			t.Fatal(err)
		}
		r := Recorded{Family: family, Key: row.Key}
		account := get("account_id").(string)
		r.View = authz.CardView{
			Card: postgres.Card{ID: "card-" + row.SHA256[:12], AccountRef: account, SHA256: digest,
				PublishedAt: published},
			AsOf: asOf, Lines: lines, Text: get("text").(string), Sources: map[string]string{},
		}
		if sources, ok := get("sources").(*strictjson.Object); ok {
			for _, k := range sources.Keys {
				v, _ := sources.Get(k)
				r.View.Sources[k], _ = v.(string)
			}
		}
		for _, value := range get("units").([]any) {
			raw, err := strictjson.Dumps(value, strictjson.Prepared)
			if err != nil {
				t.Fatal(err)
			}
			var u struct {
				N                       int
				Kind, Tool, Shown, Text string
				RecordRef               string `json:"record_ref"`
			}
			if err := json.Unmarshal([]byte(raw), &u); err != nil {
				t.Fatalf("%s %s: %v", family, row.Key, err)
			}
			r.View.Units = append(r.View.Units, postgres.Unit{CardID: r.View.Card.ID, N: u.N, Kind: u.Kind,
				RecordRef: u.RecordRef, Tool: u.Tool, Shown: u.Shown, Body: json.RawMessage(raw)})
			if u.Kind == "sentence" && len(strings.Fields(u.Text)) > 1 {
				r.Sentences = append(r.Sentences, u.Text)
			}
		}
		for _, value := range get("gaps").([]any) {
			g := value.(*strictjson.Object)
			field := func(key string) string { v, _ := g.Get(key); s, _ := v.(string); return s }
			r.View.Gaps = append(r.View.Gaps, postgres.Gap{Code: field("code"), Disposition: field("disposition"),
				Text: field("text")})
		}
		records, _ := ev.Get("records")
		for _, value := range records.([]any) {
			record := value.(*strictjson.Object)
			ref, _ := record.Get("record_ref")
			switch {
			case ref == "crm/accounts/"+account:
				r.Name = shown(record, "name")
			case strings.HasPrefix(fmt.Sprint(ref), "crm/contacts/"):
				for _, key := range []string{"name", "title", "email"} {
					if s := shown(record, key); render.Distinct(s) {
						r.Contacts = append(r.Contacts, s)
					}
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// Card is one recorded card of a family, by its key.
func Card(t testing.TB, family, key string) Recorded {
	t.Helper()
	for _, r := range Cards(t, family) {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no recorded card %s %s", family, key)
	return Recorded{}
}
