package postgres_test

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

const (
	pythonBrief = "brief-2430d066000040008000000000000001"
	pythonSeal  = "2430d066-0000-4000-8000-000000000006"
	pythonRead  = "2430d066-0000-4000-8000-000000000002"
)

// pythonFolder is the recorded folder of a card saved by brief, and the operator's word for it.
func pythonFolder(t *testing.T) (*cards.Folder, postgres.OperatorWord) {
	t.Helper()
	folder, err := cards.OpenTrustedExport(oracletest.Path(t, "folders/cli_card"))
	ok(t, err)
	identity, _ := evidence.FixtureCRMBinding().Get("identity_sha256")
	return folder, postgres.OperatorWord{FromOrg: identity.(string), ExportSHA256: folder.ExportSHA256}
}

// The database holds an admitted card only under a seal that finished ok and 2xx naming its bytes, and with
// every read the card embeds, whatever writes it. Nothing of a refused card is written, an admitted row is
// never changed, and a receipt row that is not the row of its ID is refused.
func TestAnAdmittedCardRestsOnItsSealAndReads(t *testing.T) {
	f := postgrestest.New(t)
	folder, word := pythonFolder(t)
	payload := folder.Cards[pythonBrief]
	changed := append([]byte(nil), payload...)
	changed[len(changed)-2] ^= 0x01
	failedSeal := maps.Clone(folder.Lines)
	failedSeal[pythonSeal] = []byte(strings.Replace(string(failedSeal[pythonSeal]), `"http_status":200`,
		`"http_status":500`, 1))
	sealOnly := map[string][]byte{pythonSeal: folder.Lines[pythonSeal]}
	card := postgres.AdmittedCard{BriefID: pythonBrief, Kind: "cli", AccountID: "acc-003", Bytes: payload, SealID: pythonSeal}
	with := func(edit func(c *postgres.AdmittedCard)) postgres.AdmittedCard {
		c := card
		edit(&c)
		return c
	}
	for name, attempt := range map[string]struct {
		card postgres.AdmittedCard
		rows map[string][]byte
	}{
		"a changed byte":         {with(func(c *postgres.AdmittedCard) { c.Bytes = changed }), folder.Lines},
		"a read as its seal":     {with(func(c *postgres.AdmittedCard) { c.SealID = pythonRead }), folder.Lines},
		"a seal of another kind": {with(func(c *postgres.AdmittedCard) { c.Kind = "prepared" }), folder.Lines},
		"a seal of HTTP 500":     {card, failedSeal},
		"its reads left out":     {card, sealOnly},
	} {
		if _, err := f.Store.Admit(ctx, f.Workspace.ID, word, []postgres.AdmittedCard{attempt.card},
			attempt.rows); !errors.Is(err, postgres.ErrAdmittedDiffers) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := scalar(t, f.URL, "SELECT count(*)::text FROM admitted_cards"); n != "0" {
		t.Fatalf("%s cards admitted", n)
	}
	if n := scalar(t, f.URL, "SELECT count(*)::text FROM admitted_receipts"); n != "0" {
		t.Fatalf("%s receipts admitted with a refused card", n)
	}
	for _, row := range []string{`{}`, `[]`, `{"audit_id":null}`, `{"audit_id":"` + pythonRead + `"}`} {
		if err := sql(t, f, `INSERT INTO admitted_receipts (workspace_id, audit_id, row_text, from_org)
			VALUES ($1, $2, $3, $4)`, f.Workspace.ID, pythonSeal, row, word.FromOrg); err == nil {
			t.Errorf("the row %s was admitted as receipt %s", row, pythonSeal)
		}
	}
	// An admitted row is never changed.
	_, err := f.Store.Admit(ctx, f.Workspace.ID, word, []postgres.AdmittedCard{card}, folder.Lines)
	ok(t, err)
	if err := sql(t, f, "UPDATE admitted_cards SET kind = 'prepared'"); err == nil {
		t.Fatal("an admitted card was changed")
	}
	if err := sql(t, f, "DELETE FROM admitted_receipts"); err == nil {
		t.Fatal("an admitted receipt was removed")
	}
}

// A restore loads rows with the triggers off, and its integrity check refuses what the triggers would: an
// admitted card whose reads are not admitted with it, or whose seal failed.
func TestARestoreChecksTheAdmittedCards(t *testing.T) {
	for name, edit := range map[string]string{
		"its reads left out": `DELETE FROM admitted_receipts WHERE audit_id <> '` + pythonSeal + `'`,
		"a seal of HTTP 500": `UPDATE admitted_receipts SET row_text = replace(row_text, '"http_status":200',
			'"http_status":500') WHERE audit_id = '` + pythonSeal + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := postgrestest.New(t)
			folder, word := pythonFolder(t)
			_, err := f.Store.Admit(ctx, f.Workspace.ID, word, []postgres.AdmittedCard{{BriefID: pythonBrief,
				Kind: "cli", AccountID: "acc-003", Bytes: folder.Cards[pythonBrief], SealID: pythonSeal}}, folder.Lines)
			ok(t, err)
			conn, err := pgx.Connect(ctx, f.URL)
			ok(t, err)
			defer conn.Close(ctx)
			var violations []string
			err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				// As a restore loads rows: with the tables' own triggers off.
				if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, edit); err != nil {
					return err
				}
				rows, err := tx.Query(ctx, "SELECT v FROM gtm_integrity_violations() v")
				if err != nil {
					return err
				}
				violations, err = pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				return errors.New("rolled back")
			})
			if len(violations) != 1 || !strings.Contains(violations[0], pythonBrief) {
				t.Fatalf("violations %q (%v)", violations, err)
			}
		})
	}
}
