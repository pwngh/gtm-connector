package cards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// nulInSlack is the demo book with a sentence that holds a NUL character at the start of each Slack message.
type nulInSlack struct{ Source }

func (s nulInSlack) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	system, records, err := s.Source.Read(ctx, tool, accountID)
	if tool != "read_slack_messages" || err != nil {
		return system, records, err
	}
	changed := make([]*strictjson.Object, len(records))
	for i, record := range records {
		changed[i] = record.Copy()
		body, _ := record.Get("body")
		changed[i].Set("body", "Zero\x00inside. "+body.(string))
	}
	return system, changed, nil
}

// A Slack message whose text holds a NUL character gives a card on PostgreSQL, as it does in memory and did
// on SQLite. The card shows the character as [U+0000]; its stored evidence keeps the message as read, with
// the JSON escape \u0000, which jsonb cannot hold and the database's checks once refused. The stored bytes
// are the sealed ones byte for byte, the card verifies against the stored receipts, the checks a restore
// runs find nothing, and the accounts that hold a card are still listed.
func TestACardWhoseSlackTextHoldsANULPublishesOnPostgres(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"card", "selected_brief"} {
		t.Run(kind, func(t *testing.T) {
			e := newE2E(t)
			m := e.maker(t, e.receipts, nil)
			m.Source = nulInSlack{m.Source}
			var sealed *Sealed
			var err error
			verify := VerifyCard
			if kind == "card" {
				sealed, err = m.Brief(ctx, e.brief("nul"))
			} else {
				sealed, err = m.Prepare(ctx, e.prepare("nul"))
				verify = VerifyPrepared
			}
			if err != nil {
				t.Fatalf("no card: %v", err)
			}
			var stored, digest []byte
			var rows []string
			if err := e.conn.QueryRow(ctx, `SELECT c.card_bytes, c.card_sha256,
				ARRAY(SELECT jsonb_array_elements_text(r.rows)) FROM cards c JOIN receipts r ON r.id = c.seal_receipt_id
				WHERE c.brief_id = $1`, sealed.BriefID).Scan(&stored, &digest, &rows); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(sealed.Payload)
			if !bytes.Equal(stored, sealed.Payload) || !bytes.Equal(digest, sum[:]) ||
				sealed.SHA256 != hex.EncodeToString(sum[:]) || len(rows) != 1 ||
				rows[0] != receipts.Reference(sealed.BriefID, stored) {
				t.Fatalf("the stored bytes are not the sealed ones: seal rows %q", rows)
			}
			if bytes.Count(stored, []byte(`Zero\u0000inside. `)) != 1 || bytes.IndexByte(stored, 0) >= 0 {
				t.Fatalf("the stored evidence does not hold the message as read")
			}
			document, _ := load(t, stored).(*strictjson.Object)
			if text, _ := get(document, "text").(string); text != sealed.Text ||
				!strings.Contains(text, `"Zero[U+0000]inside."`) || strings.ContainsRune(text, 0) {
				t.Fatalf("the card does not show the character as [U+0000]:\n%s", text)
			}
			if _, err := verify(ctx, stored, sealed.BriefID, StoreLog{Store: e.receipts, WorkspaceID: e.workspace},
				demoSources(nil)); err != nil {
				t.Fatalf("the stored card does not verify: %v", err)
			}
			if n := e.count(t, `SELECT count(*) FROM gtm_integrity_violations()`); n != 0 {
				t.Fatalf("the integrity check finds %d violations", n)
			}
			held, err := e.store.AccountsWithCards(ctx, e.workspace, e.reader.ID, DemoAsOf)
			if err != nil || !held["acc-003"] {
				t.Fatalf("the accounts that hold a card: %v, %v", held, err)
			}
		})
	}
}
