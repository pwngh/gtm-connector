package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// The four recorded Python state folders: a card saved by brief, and three briefs prepared with
// prepare_renewal_brief.
var pythonFolders = []struct{ name, brief, seal, account string }{
	{"cli_card", "brief-2430d066000040008000000000000001", "2430d066-0000-4000-8000-000000000006", "acc-003"},
	{"prepared", "brief-7a78ae31000040008000000000000002", "7a78ae31-0000-4000-8000-000000000001", "acc-007"},
	{"prepared_as_of", "brief-3b4b6cec000040008000000000000002", "3b4b6cec-0000-4000-8000-000000000001", "acc-003"},
	{"prepared_no_slack", "brief-2aee567d000040008000000000000002", "2aee567d-0000-4000-8000-000000000001", "acc-003"},
}

type admitResult struct {
	Cards []struct {
		BriefID   string `json:"brief_id"`
		AccountID string `json:"account_id"`
		Kind      string
		Added     bool
	}
	CardsAdded    int `json:"cards_added"`
	ReceiptsAdded int `json:"receipts_added"`
}

// pythonCard is a folder's one card file and its bytes.
func pythonCard(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	for _, sub := range []string{"briefs", "selected-briefs"} {
		files, _ := filepath.Glob(filepath.Join(dir, sub, "*.json"))
		if len(files) == 1 {
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			return files[0], data
		}
	}
	t.Fatalf("%s holds no one card", dir)
	return "", nil
}

// admitted counts a workspace's admitted cards and receipts.
func (d *demoDatabase) admitted(t *testing.T, workspace string) (cards, receipts int) {
	t.Helper()
	return d.count(t, "SELECT count(*) FROM admitted_cards WHERE workspace_id = $1", workspace),
		d.count(t, "SELECT count(*) FROM admitted_receipts WHERE workspace_id = $1", workspace)
}

// The cards the Python package saved are admitted through the command, kept byte for byte, and read back
// through inspect: the units of each card, a number on it, and its seal receipt. Admitting again adds
// nothing, and a backup carries them into a restored database.
func TestAdmitKeepsThePythonCardsAndInspectReadsThem(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "old-cards")
	for i, folder := range pythonFolders {
		dir := oracletest.Path(t, "folders/"+folder.name)
		var got admitResult
		d.result(t, &got, "admit", dir, "--state-dir", "old-cards")
		lines := bytes.Count(oracletest.Read(t, "folders/"+folder.name+"/audit.jsonl"), []byte("\n"))
		if len(got.Cards) != 1 || got.Cards[0].BriefID != folder.brief || got.Cards[0].AccountID != folder.account ||
			!got.Cards[0].Added || got.CardsAdded != 1 || got.ReceiptsAdded != lines {
			t.Fatalf("%s: admitted %+v, not its card and %d receipts", folder.name, got, lines)
		}
		if kind := map[bool]string{true: "cli", false: "prepared"}[i == 0]; got.Cards[0].Kind != kind {
			t.Fatalf("%s: kind %s", folder.name, got.Cards[0].Kind)
		}

		// Kept as the folder holds it.
		_, want := pythonCard(t, dir)
		conn, err := pgx.Connect(ctx, d.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		var kept []byte
		err = conn.QueryRow(ctx, "SELECT card_bytes FROM admitted_cards WHERE workspace_id = $1 AND brief_id = $2",
			workspace.ID, folder.brief).Scan(&kept)
		conn.Close(ctx)
		if err != nil || !bytes.Equal(kept, want) {
			t.Fatalf("%s: the kept bytes differ from the folder's: %v", folder.name, err)
		}

		// inspect lists the card's units as sealed, at neutralizer version 3 as the Python package sealed them.
		var sealed struct{ Units []map[string]any }
		if err := json.Unmarshal(want, &sealed); err != nil {
			t.Fatal(err)
		}
		var listed struct {
			BriefID   string `json:"brief_id"`
			AccountID string `json:"account_id"`
			Units     []map[string]any
		}
		d.result(t, &listed, "inspect", folder.brief, "--state-dir", "old-cards")
		if listed.BriefID != folder.brief || listed.AccountID != folder.account || len(listed.Units) == 0 ||
			len(listed.Units) != len(sealed.Units) {
			t.Fatalf("%s: inspect listed %d units of %d", folder.name, len(listed.Units), len(sealed.Units))
		}
		for n, unit := range listed.Units {
			if version, sentence := unit["sanitizer_version"]; sentence && version != float64(3) {
				t.Fatalf("%s: unit %d at neutralizer version %v", folder.name, n+1, version)
			}
		}

		// A number on it resolves to its record and the Python receipt that read it.
		var traced []struct {
			N       int
			Receipt struct {
				AuditID string `json:"audit_id"`
				Status  string
			}
		}
		d.result(t, &traced, "inspect", folder.brief, "1", "--state-dir", "old-cards")
		if len(traced) != 1 || traced[0].N != 1 || traced[0].Receipt.Status != "ok" ||
			!strings.HasPrefix(traced[0].Receipt.AuditID, folder.seal[:8]) {
			t.Fatalf("%s: number 1 traced to %+v", folder.name, traced)
		}

		// The seal is a receipt inspect shows.
		var seal struct {
			AuditID string `json:"audit_id"`
			Tool    string
			Rows    []string
		}
		d.result(t, &seal, "inspect", folder.seal, "--state-dir", "old-cards")
		if seal.AuditID != folder.seal || len(seal.Rows) != 1 || !strings.HasPrefix(seal.Rows[0], "briefs/"+folder.brief+"/") {
			t.Fatalf("%s: the seal receipt %+v", folder.name, seal)
		}
	}

	// Admitting every folder again adds nothing.
	cardsBefore, receiptsBefore := d.admitted(t, workspace.ID)
	if cardsBefore != 4 || receiptsBefore != 19 {
		t.Fatalf("admitted %d cards and %d receipts, not 4 and 19", cardsBefore, receiptsBefore)
	}
	for _, folder := range pythonFolders {
		var again admitResult
		d.result(t, &again, "admit", oracletest.Path(t, "folders/"+folder.name), "--state-dir", "old-cards")
		if len(again.Cards) != 1 || again.Cards[0].Added || again.CardsAdded != 0 || again.ReceiptsAdded != 0 {
			t.Fatalf("%s admitted again: %+v", folder.name, again)
		}
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != cardsBefore || receipts != receiptsBefore {
		t.Fatalf("admitting again changed the counts to %d cards and %d receipts", cards, receipts)
	}

	// Text output names each card and what was added.
	code, stdout, stderr := d.read(t, "admit", oracletest.Path(t, "folders/cli_card"), "--state-dir", "old-cards")
	if code != 0 || !strings.Contains(stdout, "brief-2430d066000040008000000000000001\tacc-003\tsaved by brief or demo\talready admitted") ||
		!strings.Contains(stdout, "0 cards and 0 receipts added.") {
		t.Fatalf("admit text: exit %d\n%s%s", code, stdout, stderr)
	}

	// A backup carries the admitted cards into a restored database, whose integrity check passes.
	source := privateDir(t)
	creds, _ := openCredentials(source, false)
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts([]string{"127.0.0.1:8443"})); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup(ctx, d.owner, creds, archive); err != nil {
		t.Fatal(err)
	}
	fresh := openFresh(t)
	if err := restore(ctx, fresh, filepath.Join(t.TempDir(), "restored"), archive); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, freshURLs[fresh])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var cards, receipts int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM admitted_cards), (SELECT count(*) FROM admitted_receipts)`).
		Scan(&cards, &receipts); err != nil || cards != 4 || receipts != 19 {
		t.Fatalf("restored %d admitted cards and %d receipts: %v", cards, receipts, err)
	}
}

// A folder whose card differs by one byte from the bytes its seal names is refused with a fixed message,
// and nothing of it is admitted.
func TestAdmitRefusesAChangedByte(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "changed")
	for _, folder := range pythonFolders {
		dir := filepath.Join(t.TempDir(), folder.name)
		if err := os.CopyFS(dir, os.DirFS(oracletest.Path(t, "folders/"+folder.name))); err != nil {
			t.Fatal(err)
		}
		path, data := pythonCard(t, dir)
		// The card's text, as indented or compact JSON holds it.
		key := []byte(`"text": "DEAL CARD`)
		if !bytes.Contains(data, key) {
			key = []byte(`"text":"DEAL CARD`)
		}
		at := bytes.Index(data, key) + len(key) - len("DEAL CARD")
		if at < len(key) {
			t.Fatalf("%s: no card text to change", folder.name)
		}
		changed := append([]byte(nil), data...)
		changed[at] ^= 0x01 // a letter of the card's text, still valid JSON
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		code, message, _ := d.refusal(t, 4, "admit", dir, "--state-dir", "changed")
		want := folder.brief + ": This file is not the card that was sealed: the card or its seal receipt was changed. " +
			"Nothing was admitted."
		if code != "check_failed" || message != want {
			t.Fatalf("%s: %s %q", folder.name, code, message)
		}
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != 0 || receipts != 0 {
		t.Fatalf("a refused folder admitted %d cards and %d receipts", cards, receipts)
	}
	// And inspect finds no card there.
	if _, message, _ := d.refusal(t, 2, "inspect", pythonFolders[0].brief, "--state-dir", "changed"); message !=
		"No saved card has this brief ID in this state folder." {
		t.Fatalf("inspect: %q", message)
	}
}

// A receipt row the workspace holds with other bytes is refused whole, and a workspace with no CRM
// admits nothing.
func TestAdmitRefusesAnotherRowUnderAnAdmittedID(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "rows")
	var first admitResult
	d.result(t, &first, "admit", oracletest.Path(t, "folders/cli_card"), "--state-dir", "rows")
	dir := filepath.Join(t.TempDir(), "cli_card")
	if err := os.CopyFS(dir, os.DirFS(oracletest.Path(t, "folders/cli_card"))); err != nil {
		t.Fatal(err)
	}
	// The same rows as values, written with a space more: the card still verifies, and its receipts differ.
	log := filepath.Join(dir, "audit.jsonl")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, bytes.ReplaceAll(data, []byte(`{"audit_id"`), []byte(`{ "audit_id"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := d.refusal(t, 3, "admit", dir, "--state-dir", "rows"); code != "refused" {
		t.Fatalf("another row: %s", code)
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != 1 || receipts != first.ReceiptsAdded {
		t.Fatalf("after the refusal: %d cards and %d receipts", cards, receipts)
	}
	if _, message, _ := d.refusal(t, 2, "admit", oracletest.Path(t, "folders/cli_card"), "--state-dir", "nothing"); !strings.HasPrefix(message, "No CRM is connected.") {
		t.Fatalf("a workspace with no CRM: %q", message)
	}
}
