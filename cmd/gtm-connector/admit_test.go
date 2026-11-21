package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
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

// demoIdentity is the CRM identity the demo book's cards record, which --from-org names for them.
func demoIdentity() string {
	identity, _ := evidence.FixtureCRMBinding().Get("identity_sha256")
	return identity.(string)
}

// exportDigest is the SHA-256 of a folder's audit.jsonl, as the operator takes it when exporting.
func exportDigest(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// admitArgs is admit of a folder into a workspace, on the operator's word for the demo book and the
// folder's export as it is now.
func admitArgs(t *testing.T, dir, workspace string) []string {
	t.Helper()
	return []string{"admit", dir, "--state-dir", workspace, "--from-org", demoIdentity(),
		"--export-sha256", exportDigest(t, dir)}
}

// copyFolder is a copy of a recorded folder that a test may change.
func copyFolder(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dir, os.DirFS(oracletest.Path(t, "folders/"+name))); err != nil {
		t.Fatal(err)
	}
	return dir
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

// rewriteFile replaces old with new in a file, which must hold old once.
func rewriteFile(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte(old)) != 1 {
		t.Fatalf("%s holds %q %d times", filepath.Base(path), old, bytes.Count(data, []byte(old)))
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte(old), []byte(new), 1), 0o600); err != nil {
		t.Fatal(err)
	}
}

// admitted counts a workspace's admitted cards and receipts.
func (d *demoDatabase) admitted(t *testing.T, workspace string) (cards, receipts int) {
	t.Helper()
	return d.count(t, "SELECT count(*) FROM admitted_cards WHERE workspace_id = $1", workspace),
		d.count(t, "SELECT count(*) FROM admitted_receipts WHERE workspace_id = $1", workspace)
}

// refusedWhole is admit's refusal of a folder, which must exit 4 with this message and admit nothing.
func (d *demoDatabase) refusedWhole(t *testing.T, workspace postgres.Workspace, want string, args ...string) {
	t.Helper()
	if code, message, _ := d.refusal(t, 4, args...); code != "check_failed" || message != want {
		t.Fatalf("%s %q, not %q", code, message, want)
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != 0 || receipts != 0 {
		t.Fatalf("a refused folder admitted %d cards and %d receipts", cards, receipts)
	}
}

// The cards the Python package saved are admitted through the command on the operator's word, kept byte
// for byte, and read back through inspect, which says they were admitted from an export: the units of each
// card, a number on it, and its seal receipt. Admitting again adds nothing, and a backup carries them into
// a restored database.
func TestAdmitKeepsThePythonCardsAndInspectReadsThem(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "old-cards")
	for i, folder := range pythonFolders {
		dir := oracletest.Path(t, "folders/"+folder.name)
		var got admitResult
		d.result(t, &got, admitArgs(t, dir, "old-cards")...)
		lines := bytes.Count(oracletest.Read(t, "folders/"+folder.name+"/audit.jsonl"), []byte("\n"))
		if len(got.Cards) != 1 || got.Cards[0].BriefID != folder.brief || got.Cards[0].AccountID != folder.account ||
			!got.Cards[0].Added || got.CardsAdded != 1 || got.ReceiptsAdded != lines {
			t.Fatalf("%s: admitted %+v, not its card and %d receipts", folder.name, got, lines)
		}
		if kind := map[bool]string{true: "cli", false: "prepared"}[i == 0]; got.Cards[0].Kind != kind {
			t.Fatalf("%s: kind %s", folder.name, got.Cards[0].Kind)
		}

		// Kept as the folder holds it, with the operator's word recorded.
		_, want := pythonCard(t, dir)
		conn, err := pgx.Connect(ctx, d.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		var kept []byte
		var from, export string
		err = conn.QueryRow(ctx, `SELECT card_bytes, from_org, export_sha256 FROM admitted_cards
			WHERE workspace_id = $1 AND brief_id = $2`, workspace.ID, folder.brief).Scan(&kept, &from, &export)
		conn.Close(ctx)
		if err != nil || !bytes.Equal(kept, want) || from != demoIdentity() || export != exportDigest(t, dir) {
			t.Fatalf("%s: the kept card differs from the folder's: %v", folder.name, err)
		}

		// inspect lists the card's units as sealed, at neutralizer version 3 as the Python package sealed them,
		// and says the card was admitted from an export.
		var sealed struct{ Units []map[string]any }
		if err := json.Unmarshal(want, &sealed); err != nil {
			t.Fatal(err)
		}
		var listed struct {
			BriefID   string `json:"brief_id"`
			AccountID string `json:"account_id"`
			Units     []map[string]any
			Admitted  struct {
				FromOrg      string `json:"from_org"`
				ExportSHA256 string `json:"export_sha256"`
				Note         string
			}
		}
		d.result(t, &listed, "inspect", folder.brief, "--state-dir", "old-cards")
		if listed.BriefID != folder.brief || listed.AccountID != folder.account || len(listed.Units) == 0 ||
			len(listed.Units) != len(sealed.Units) {
			t.Fatalf("%s: inspect listed %d units of %d", folder.name, len(listed.Units), len(sealed.Units))
		}
		if listed.Admitted.Note != admittedNote || listed.Admitted.FromOrg != demoIdentity() ||
			listed.Admitted.ExportSHA256 != exportDigest(t, dir) {
			t.Fatalf("%s: inspect says %+v", folder.name, listed.Admitted)
		}
		for n, unit := range listed.Units {
			if version, sentence := unit["sanitizer_version"]; sentence && version != float64(3) {
				t.Fatalf("%s: unit %d at neutralizer version %v", folder.name, n+1, version)
			}
		}
		code, stdout, _ := d.read(t, "inspect", folder.brief, "--state-dir", "old-cards")
		if code != 0 || !strings.HasPrefix(stdout, "Admitted from an export, not re-read: the folder's own receipts "+
			"vouch for it, on the operator's word that it came from CRM identity "+demoIdentity()+".\n") {
			t.Fatalf("%s: inspect text:\n%s", folder.name, stdout)
		}

		// A number on it resolves to its record and the Python receipt that read it.
		var traced []struct {
			N       int
			Receipt struct {
				AuditID string `json:"audit_id"`
				Status  string
			}
			Admitted struct{ Note string }
		}
		d.result(t, &traced, "inspect", folder.brief, "1", "--state-dir", "old-cards")
		if len(traced) != 1 || traced[0].N != 1 || traced[0].Receipt.Status != "ok" ||
			!strings.HasPrefix(traced[0].Receipt.AuditID, folder.seal[:8]) || traced[0].Admitted.Note != admittedNote {
			t.Fatalf("%s: number 1 traced to %+v", folder.name, traced)
		}

		// The seal is a receipt inspect shows, as admitted.
		var seal struct {
			AuditID  string `json:"audit_id"`
			Rows     []string
			Admitted struct{ Note string }
		}
		d.result(t, &seal, "inspect", folder.seal, "--state-dir", "old-cards")
		if seal.AuditID != folder.seal || len(seal.Rows) != 1 || !strings.HasPrefix(seal.Rows[0], "briefs/"+folder.brief+"/") ||
			seal.Admitted.Note != admittedNote {
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
		d.result(t, &again, admitArgs(t, oracletest.Path(t, "folders/"+folder.name), "old-cards")...)
		if len(again.Cards) != 1 || again.Cards[0].Added || again.CardsAdded != 0 || again.ReceiptsAdded != 0 {
			t.Fatalf("%s admitted again: %+v", folder.name, again)
		}
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != cardsBefore || receipts != receiptsBefore {
		t.Fatalf("admitting again changed the counts to %d cards and %d receipts", cards, receipts)
	}

	// Text output names each card and what was added.
	code, stdout, stderr := d.read(t, admitArgs(t, oracletest.Path(t, "folders/cli_card"), "old-cards")...)
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

// admit cannot tell where an export came from, so it admits nothing without the operator's word: the CRM
// identity the cards were read from, which must be the one the cards record and the workspace is bound to,
// and the digest of the export.
func TestAdmitTakesTheOperatorsWordForTheExport(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "word")
	dir := oracletest.Path(t, "folders/cli_card")
	if _, message, _ := d.refusal(t, 2, "admit", dir, "--state-dir", "word", "--export-sha256",
		exportDigest(t, dir)); message != "the following arguments are required: --from-org" {
		t.Fatalf("no --from-org: %q", message)
	}
	if _, message, _ := d.refusal(t, 2, "admit", dir, "--state-dir", "word", "--from-org",
		demoIdentity()); message != "the following arguments are required: --export-sha256" {
		t.Fatalf("no --export-sha256: %q", message)
	}
	other := strings.Repeat("ab", 32)
	d.refusedWhole(t, workspace, "This workspace is bound to CRM identity "+demoIdentity()+", not the one --from-org "+
		"names. Nothing was admitted.", "admit", dir, "--state-dir", "word", "--from-org", other,
		"--export-sha256", exportDigest(t, dir))
	if _, message, _ := d.refusal(t, 2, "admit", dir, "--state-dir", "word", "--from-org", "00D000000000001",
		"--export-sha256", exportDigest(t, dir)); message != "--from-org takes the CRM identity a card records: "+
		"64 lowercase hex digits." {
		t.Fatalf("a --from-org that is no identity: %q", message)
	}
	if _, message, _ := d.refusal(t, 2, "admit", dir, "--state-dir", "word", "--from-org", demoIdentity(),
		"--export-sha256", "abc"); message != "--export-sha256 takes the SHA-256 of the folder's audit.jsonl: "+
		"64 lowercase hex digits." {
		t.Fatalf("an --export-sha256 that is no digest: %q", message)
	}
}

// One changed byte anywhere in what a folder holds is refused on its first admission: in the card, in the
// seal's timing (which no digest in the folder covers, and the export digest does), in a read receipt the
// card embeds, and in the way a line is written.
func TestAdmitRefusesAChangedByte(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "changed")
	for _, folder := range pythonFolders {
		dir := copyFolder(t, folder.name)
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
		d.refusedWhole(t, workspace, folder.brief+": This file is not the card that was sealed: the card or its seal "+
			"receipt was changed. Nothing was admitted.", admitArgs(t, dir, "changed")...)
	}

	seal := pythonFolders[0].seal
	exported := exportDigest(t, oracletest.Path(t, "folders/cli_card"))
	dir := copyFolder(t, "cli_card")
	rewriteFile(t, filepath.Join(dir, "audit.jsonl"), `c52d9c7552b\"]","elapsed_ms":0.0`,
		`c52d9c7552b\"]","elapsed_ms":0.1`)
	d.refusedWhole(t, workspace, "The folder's audit.jsonl is not the export whose SHA-256 --export-sha256 gives. "+
		"Nothing was admitted.", "admit", dir, "--state-dir", "changed", "--from-org", demoIdentity(),
		"--export-sha256", exported)

	// A read receipt the card embeds, changed in the export the operator vouches for.
	dir = copyFolder(t, "cli_card")
	rewriteFile(t, filepath.Join(dir, "audit.jsonl"), `"row_count":1,"rows":"[\"crm/opportunities/opp-003\"]","elapsed_ms":0.0`,
		`"row_count":1,"rows":"[\"crm/opportunities/opp-003\"]","elapsed_ms":0.1`)
	d.refusedWhole(t, workspace, pythonFolders[0].brief+": The sealed card's read receipts are not the ones in the "+
		"audit log of this state folder. Nothing was admitted.", admitArgs(t, dir, "changed")...)

	// The seal, and a read, written with a space more: the same values, another line.
	for _, id := range []string{seal, "2430d066-0000-4000-8000-000000000003"} {
		dir = copyFolder(t, "cli_card")
		rewriteFile(t, filepath.Join(dir, "audit.jsonl"), `{"audit_id":"`+id, `{ "audit_id":"`+id)
		d.refusedWhole(t, workspace, pythonFolders[0].brief+": A receipt in audit.jsonl is not the receipt the card "+
			"embeds, or not written as the export writes it. Nothing was admitted.", admitArgs(t, dir, "changed")...)
	}

	// And inspect finds no card there.
	if _, message, _ := d.refusal(t, 2, "inspect", pythonFolders[0].brief, "--state-dir", "changed"); message !=
		"No saved card has this brief ID in this state folder." {
		t.Fatalf("inspect: %q", message)
	}
}

// resealed rewrites a folder's card as edit leaves it, and its seal to name the new bytes, so the card and
// its seal agree: what the folder vouches for is then only what the card itself can show.
func resealed(t *testing.T, dir string, edit func(card *strictjson.Object)) {
	t.Helper()
	path, data := pythonCard(t, dir)
	value, err := strictjson.Loads(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := strictjson.Dumps(value, strictjson.Artifact)
	if err != nil || again != string(data) {
		t.Fatalf("the card does not dump back to its bytes: %v", err)
	}
	edit(value.(*strictjson.Object))
	text, err := strictjson.Dumps(value, strictjson.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	before, after := sha256.Sum256(data), sha256.Sum256([]byte(text))
	rewriteFile(t, filepath.Join(dir, "audit.jsonl"), hex.EncodeToString(before[:]), hex.EncodeToString(after[:]))
}

// A sealed quotation must be a sentence of the record it cites, under that speaker: a seal that names the
// bytes does not vouch for that, since the folder supplies the seal too.
func TestAdmitRefusesAQuotationItsRecordDoesNotHold(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "quotes")
	const said = "We will have the order form countersigned by January 30."
	for name, edit := range map[string]func(card *strictjson.Object){
		"the text": func(card *strictjson.Object) {
			units, _ := card.Get("units")
			unit := units.([]any)[3].(*strictjson.Object)
			if text, _ := unit.Get("text"); text != said {
				t.Fatalf("unit 4 says %v", text)
			}
			unit.Set("text", "We will cancel the renewal on January 30.")
			text, _ := card.Get("text")
			card.Set("text", strings.Replace(text.(string), said, "We will cancel the renewal on January 30.", 1))
		},
		"the speaker": func(card *strictjson.Object) {
			units, _ := card.Get("units")
			units.([]any)[3].(*strictjson.Object).Set("speaker", "Dana Crest")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := copyFolder(t, "cli_card")
			resealed(t, dir, edit)
			d.refusedWhole(t, workspace, pythonFolders[0].brief+": A quotation on this card is not a sentence its "+
				"record holds under that speaker. Nothing was admitted.", admitArgs(t, dir, "quotes")...)
		})
	}
}

// Every card file a folder holds is checked, so a good card file does not hide a damaged or oversized
// prepared brief of the same brief ID.
func TestAdmitChecksEveryFileOfTheFolder(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "files")
	brief := pythonFolders[0].brief
	for name, content := range map[string][]byte{
		"damaged":   []byte("{"),
		"oversized": bytes.Repeat([]byte(" "), cards.MaxArtifactBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := copyFolder(t, "cli_card")
			if err := os.MkdirAll(filepath.Join(dir, "selected-briefs"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "selected-briefs", brief+".json"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			d.refusedWhole(t, workspace, brief+": invalid saved brief evidence. Nothing was admitted.",
				admitArgs(t, dir, "files")...)
		})
	}
}

// A receipt row the workspace holds with other bytes is refused whole, and a workspace with no CRM
// admits nothing.
func TestAdmitRefusesAnotherRowUnderAnAdmittedID(t *testing.T) {
	d := setUpDemo(t)
	workspace := d.connectDemo(t, "rows")
	conn, err := pgx.Connect(ctx, d.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	seal := pythonFolders[0].seal
	_, err = conn.Exec(ctx, `INSERT INTO admitted_receipts (workspace_id, audit_id, row_text, from_org)
		VALUES ($1, $2, $3, $4)`, workspace.ID, seal, `{"audit_id":"`+seal+`"}`, demoIdentity())
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := d.refusal(t, 3, admitArgs(t, oracletest.Path(t, "folders/cli_card"), "rows")...); code != "refused" {
		t.Fatalf("another row: %s", code)
	}
	if cards, receipts := d.admitted(t, workspace.ID); cards != 0 || receipts != 1 {
		t.Fatalf("after the refusal: %d cards and %d receipts", cards, receipts)
	}
	if _, message, _ := d.refusal(t, 2, admitArgs(t, oracletest.Path(t, "folders/cli_card"), "nothing")...); !strings.HasPrefix(message, "No CRM is connected.") {
		t.Fatalf("a workspace with no CRM: %q", message)
	}
}

// exportFolder writes a card made in a workspace as a Python state folder: its file, and its seal and reads
// as the lines of an audit.jsonl export.
func (d *demoDatabase) exportFolder(t *testing.T, workspace postgres.Workspace, brief string) string {
	t.Helper()
	card, err := d.owner.CardByBrief(ctx, workspace.ID, brief)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := receiptsOf(card.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(dir, "briefs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "briefs", brief+".json"), card.Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var export bytes.Buffer
	for _, id := range payload {
		receipt, err := d.owner.Receipt(ctx, workspace.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		value, err := receipt.AuditValue()
		if err != nil {
			t.Fatal(err)
		}
		line, err := cards.ExportLine(value)
		if err != nil {
			t.Fatal(err)
		}
		export.WriteString(line + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "audit.jsonl"), export.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// receiptsOf is the IDs of a card's reads, then its seal.
func receiptsOf(card []byte) ([]string, error) {
	var parsed struct {
		SealedBy string `json:"sealed_by"`
		Evidence struct {
			Receipts []struct {
				AuditID string `json:"audit_id"`
			}
		}
	}
	if err := json.Unmarshal(card, &parsed); err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range parsed.Evidence.Receipts {
		ids = append(ids, r.AuditID)
	}
	return append(ids, parsed.SealedBy), nil
}

// A card that quotes an imported transcript is admitted only into a workspace that holds that import, the
// bytes it stores hashing to the content_sha256 the card's record names.
func TestAdmitNeedsTheImportedTranscriptTheCardQuotes(t *testing.T) {
	d := setUpDemo(t)
	folder := exports(t)
	file := filepath.Join(folder, "meeting.vtt")
	if err := os.WriteFile(file, []byte(meeting), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(folder, "other.vtt")
	if err := os.WriteFile(other, []byte(strings.Replace(meeting, "this quarter", "next quarter", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	connected := func(name string) postgres.Workspace {
		d.succeeded(t, "connect", "demo", "--state-dir", name)
		d.succeeded(t, "connect", "calls", "--seller", "Morgan Reed", "--state-dir", name)
		workspace, err := d.owner.Workspace(ctx, workspaceSlug(name))
		if err != nil {
			t.Fatal(err)
		}
		return workspace
	}
	made := connected("made")
	d.succeeded(t, "debug", "calls", "import", file, "--account", "acc-007", "--state-dir", "made")
	stdout, _ := d.succeeded(t, "--json", "brief", "acc-007", "--state-dir", "made")
	brief, _ := get(object(t, stdout), "brief_id").(string)
	dir := d.exportFolder(t, made, brief)

	without := connected("without")
	d.refusedWhole(t, without, brief+": This card quotes an imported transcript this workspace does not hold. "+
		"Import it with debug calls import first. Nothing was admitted.", admitArgs(t, dir, "without")...)

	different := connected("different")
	d.succeeded(t, "debug", "calls", "import", other, "--account", "acc-007", "--state-dir", "different")
	d.refusedWhole(t, different, brief+": This card quotes an imported transcript this workspace does not hold. "+
		"Import it with debug calls import first. Nothing was admitted.", admitArgs(t, dir, "different")...)

	connected("same")
	d.succeeded(t, "debug", "calls", "import", file, "--account", "acc-007", "--state-dir", "same")
	var got admitResult
	d.result(t, &got, admitArgs(t, dir, "same")...)
	if got.CardsAdded != 1 || len(got.Cards) != 1 || got.Cards[0].BriefID != brief {
		t.Fatalf("the card with its import: %+v", got)
	}
	var listed struct{ Units []map[string]any }
	d.result(t, &listed, "inspect", brief, "--state-dir", "same")
	if len(listed.Units) == 0 {
		t.Fatal("inspect listed no unit")
	}
}
