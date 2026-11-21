package postgres_test

import (
	"errors"
	"testing"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// The database holds an admitted card only under a seal that names its bytes, whatever writes it: a
// changed byte, or a seal that is not the card's, is refused at commit and nothing is written.
func TestAnAdmittedCardRestsOnASealOfItsBytes(t *testing.T) {
	f := postgrestest.New(t)
	folder, err := cards.OpenTrustedExport(oracletest.Path(t, "folders/cli_card"))
	ok(t, err)
	brief, seal, read := "brief-2430d066000040008000000000000001", "2430d066-0000-4000-8000-000000000006",
		"2430d066-0000-4000-8000-000000000002"
	payload := folder.Payload(brief)
	changed := append([]byte(nil), payload...)
	changed[len(changed)-2] ^= 0x01
	for name, card := range map[string]postgres.AdmittedCard{
		"a changed byte":         {BriefID: brief, Kind: "cli", AccountID: "acc-003", Bytes: changed, SealID: seal},
		"a read as its seal":     {BriefID: brief, Kind: "cli", AccountID: "acc-003", Bytes: payload, SealID: read},
		"a seal of another kind": {BriefID: brief, Kind: "prepared", AccountID: "acc-003", Bytes: payload, SealID: seal},
	} {
		if _, err := f.Store.Admit(ctx, f.Workspace.ID, []postgres.AdmittedCard{card}, folder.Lines); !errors.Is(err,
			postgres.ErrAdmittedDiffers) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := scalar(t, f.URL, "SELECT count(*)::text FROM admitted_cards"); n != "0" {
		t.Fatalf("%s cards admitted", n)
	}
	if n := scalar(t, f.URL, "SELECT count(*)::text FROM admitted_receipts"); n != "0" {
		t.Fatalf("%s receipts admitted with a refused card", n)
	}
	// An admitted row is never changed.
	_, err = f.Store.Admit(ctx, f.Workspace.ID, []postgres.AdmittedCard{{BriefID: brief, Kind: "cli",
		AccountID: "acc-003", Bytes: payload, SealID: seal}}, folder.Lines)
	ok(t, err)
	if err := sql(t, f, "UPDATE admitted_cards SET kind = 'prepared'"); err == nil {
		t.Fatal("an admitted card was changed")
	}
	if err := sql(t, f, "DELETE FROM admitted_receipts"); err == nil {
		t.Fatal("an admitted receipt was removed")
	}
}
