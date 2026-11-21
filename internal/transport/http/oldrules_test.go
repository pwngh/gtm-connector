package httptransport_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// sealedUnderVersion3 is a sentence a card sealed under neutralizer version 3 holds: version 3 left a mail
// address in angle brackets as written, and today's rules show it as a link with no host.
const (
	sealedUnderVersion3 = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) <jovan.mercer@quarrystream.example>"
	shownToday          = "- Jovan Mercer (Enterprise Renewal Manager, QuarryStream Data) [link mail]"
)

// A unit saved under an older neutralizer version is shown neutralized under today's, in the card and on its
// own, while the card's bytes stay as sealed. A unit saved under today's version is shown as saved.
func TestAUnitSavedUnderOlderRulesIsShownUnderTodays(t *testing.T) {
	s := newSite(t, 0)
	f := s.Fixture
	brief := "brief-older-rules"
	record := postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account, "name", "Invented")
	pub, call, read := postgrestest.Prepared(t, f.Store, f, brief+"-call", brief, f.Reader.ID,
		postgres.TokenID("an invented bearer"), record)
	for i, version := range []int{3, evidence.SanitizerVersion} {
		n := len(pub.Units) + 1
		pub.Units = append(pub.Units, postgres.Unit{N: n, Kind: "sentence", RecordRef: record.RecordRef(),
			Tool: "get_account", Shown: "yes", ReceiptID: read.ID, SourceRecordID: pub.Units[0].SourceRecordID,
			Body: postgrestest.UnitBody(n, "sentence", record.RecordRef(), "get_account", "yes",
				map[string]any{"text": []string{sealedUnderVersion3, "Signed."}[i], "sanitizer_version": version})})
	}
	pub.Bytes = postgrestest.CardBytes(t, call, pub.AccountRef, brief, []postgres.Receipt{read},
		[]postgres.RecordVersion{record}, pub.Units, pub.Gaps)
	card, err := f.Store.PublishCard(t.Context(), pub)
	if err != nil {
		t.Fatal(err)
	}

	reader := s.browser(t)
	reader.signIn("demo-reader", readerPassword)
	detail := reader.do(http.MethodGet, "/api/v1/cards/"+card.ID, nil, nil)
	units, _ := detail.body["units"].([]any)
	if detail.status != 200 || len(units) != 3 {
		t.Fatalf("card: %d %s", detail.status, detail.raw)
	}
	text := func(unit any) any { return unit.(map[string]any)["body"].(map[string]any)["text"] }
	if got := text(units[1]); got != shownToday {
		t.Errorf("the card shows the older unit as %q", got)
	}
	if got := text(units[2]); got != "Signed." {
		t.Errorf("the card shows today's unit as %q", got)
	}
	unit := reader.do(http.MethodGet, "/api/v1/cards/"+card.ID+"/units/2", nil, nil)
	if body, _ := unit.body["body"].(map[string]any); unit.status != 200 || body["text"] != shownToday ||
		body["sanitizer_version"] != float64(3) {
		t.Errorf("unit 2: %d %s", unit.status, unit.raw)
	}
	sealed := reader.do(http.MethodGet, "/api/v1/cards/"+card.ID+"/bytes", nil, nil)
	if !bytes.Equal(sealed.raw, pub.Bytes) || !bytes.Contains(sealed.raw, []byte("<jovan.mercer@quarrystream.example>")) {
		t.Error("the card's bytes are not the sealed ones")
	}
	// The text route is where a reader checks the card against its seal, so it gives the sealed text exactly.
	document, err := strictjson.Loads(pub.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := document.(*strictjson.Object).Get("text")
	if text := reader.do(http.MethodGet, "/api/v1/cards/"+card.ID+"/text", nil, nil); string(text.raw) != want {
		t.Errorf("the text route gives %q, not the sealed text", text.raw)
	}
}
