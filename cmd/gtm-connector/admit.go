package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// admitted is what inspect says of a card or receipt admitted from an export.
const admittedNote = "admitted from an export, not re-read"

// admit admits the cards a Python state folder holds, and the receipts they rest on, into a workspace.
//
// The folder supplies each card, its reads and its seal, and nothing offline can show where it came from,
// so admit takes it on the operator's word: --from-org names the CRM identity the cards were read from,
// which must be the one each card records and the one the workspace is bound to, and --export-sha256 the
// digest of the export, which covers what of a seal no digest in the folder covers. Within that, each card
// is verified against its seal, every one of its receipts line for line, the workspace's sources, its own
// evidence for every quotation, and the workspace's imports for every transcript it quotes. A folder any
// file of which fails admits nothing.
func (r *run) admit(ctx context.Context) (any, error) {
	fromOrg, _ := r.args.text("--from-org")
	exported, _ := r.args.text("--export-sha256")
	if !digestPattern.MatchString(fromOrg) {
		return nil, refused("--from-org takes the CRM identity a card records: 64 lowercase hex digits.")
	}
	if !digestPattern.MatchString(exported) {
		return nil, refused("--export-sha256 takes the SHA-256 of the folder's audit.jsonl: 64 lowercase hex digits.")
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	bound := from.sources()
	if workspaceOrg := identityOf(bound.CRMBinding); workspaceOrg != fromOrg {
		return nil, &failure{code: 4, message: "This workspace is bound to CRM identity " + workspaceOrg +
			", not the one --from-org names. Nothing was admitted."}
	}
	folder, err := cards.OpenTrustedExport(r.args.positionals[0])
	if err != nil {
		return nil, unreadableFolder(err)
	}
	if folder.ExportSHA256 != exported {
		return nil, &failure{code: 4, message: "The folder's audit.jsonl is not the export whose SHA-256 " +
			"--export-sha256 gives. Nothing was admitted."}
	}
	verified, err := folder.VerifyAll(ctx, bound)
	var refusal *cards.FileError
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.As(err, &refusal):
		return nil, notAdmitted(refusal.BriefID, refusal.Err)
	case err != nil:
		return nil, err
	case len(verified) == 0:
		return nil, refused("This folder holds no saved card: no briefs or selected-briefs file.")
	}
	var admitted []postgres.AdmittedCard
	rows := map[string][]byte{}
	for _, card := range verified {
		failed, err := r.checkAdmitted(ctx, folder, card, fromOrg)
		if err != nil {
			return nil, err
		}
		if failed != nil {
			return nil, notAdmitted(card.BriefID, failed)
		}
		kind := "cli"
		if card.Kind == "selected_brief" {
			kind = "prepared"
		}
		admitted = append(admitted, postgres.AdmittedCard{BriefID: card.BriefID, Kind: kind,
			AccountID: card.AccountID, Bytes: card.Payload, SealID: card.SealID})
		for _, receipt := range append([]string{card.SealID}, card.ReadIDs...) {
			rows[receipt] = folder.Lines[receipt]
		}
	}
	added, err := r.store.Admit(ctx, r.workspace.ID, postgres.OperatorWord{FromOrg: fromOrg, ExportSHA256: exported},
		admitted, rows)
	if errors.Is(err, postgres.ErrAdmittedDiffers) {
		return nil, &failure{code: 3, message: "This workspace already holds another card or receipt with an ID " +
			"this folder uses, or a card made here with one of its brief IDs. Nothing was admitted."}
	}
	if err != nil {
		return nil, err
	}
	listed := make([]any, len(admitted))
	for i, c := range admitted {
		listed[i] = strictjson.NewObject("brief_id", c.BriefID, "account_id", c.AccountID, "kind", c.Kind,
			"added", slices.Contains(added.Cards, c.BriefID))
	}
	return strictjson.NewObject("cards", listed, "cards_added", len(added.Cards),
		"receipts_added", len(added.Receipts)), nil
}

// errNotThisOrg refuses a card read from another CRM identity than the one --from-org names.
var errNotThisOrg = errors.New("This card was read from another CRM identity than the one --from-org names.")

// errImportMissing refuses a card that quotes an imported transcript the workspace does not hold.
var errImportMissing = errors.New("This card quotes an imported transcript this workspace does not hold. " +
	"Import it with debug calls import first.")

// checkAdmitted is what admit checks of a verified card beyond its seal: the CRM identity it records, its
// receipts line for line, every quotation against its evidence, and every imported transcript it quotes
// against the workspace's imports. It gives the check the card failed, or an error of the database.
func (r *run) checkAdmitted(ctx context.Context, folder *cards.Folder, card *cards.Admitted, fromOrg string) (
	failed, err error) {
	value, err := strictjson.Loads(card.Payload)
	if err != nil {
		return cards.ErrInvalidCard, nil
	}
	snapshot, _ := get(value.(*strictjson.Object), "evidence").(*strictjson.Object)
	if identityOf(get(snapshot, "crm_binding")) != fromOrg {
		return errNotThisOrg, nil
	}
	if failed := folder.CheckExport(card); failed != nil {
		return failed, nil
	}
	if failed := cards.CheckQuotations(card.Payload); failed != nil {
		return failed, nil
	}
	transcripts, failed := cards.ImportedTranscripts(card.Payload)
	if failed != nil {
		return failed, nil
	}
	for _, transcript := range transcripts {
		held, err := r.store.HoldsImportedTranscript(ctx, r.workspace.ID, transcript.AccountID, transcript.ContentSHA256)
		if err != nil {
			return nil, err
		}
		if !held {
			return errImportMissing, nil
		}
	}
	return nil, nil
}

// identityOf is the identity_sha256 a source binding records, or "".
func identityOf(binding any) string {
	object, _ := binding.(*strictjson.Object)
	identity, _ := get(object, "identity_sha256").(string)
	return identity
}

// unreadableFolder is the refusal of a folder that cannot be opened, in fixed words: an error from the
// file system names the path, which may hold a user name.
func unreadableFolder(err error) *failure {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return refused("This folder holds no audit.jsonl, the export of its audit log, or does not exist.")
	case errors.Is(err, cards.ErrLinkedLog):
		return &failure{code: 4, message: "The folder's audit log must not be a symbolic link."}
	case errors.Is(err, cards.ErrNotRegular):
		return &failure{code: 4, message: "A file of this folder is a link or not a regular file."}
	case errors.Is(err, cards.ErrFileTooLarge):
		return &failure{code: 4, message: "The folder's audit.jsonl is larger than an audit log export can be."}
	case errors.Is(err, cards.ErrDuplicateReceipt):
		return &failure{code: 4, message: "The folder's audit.jsonl holds one audit ID twice."}
	}
	var unread *os.PathError
	if errors.As(err, &unread) {
		return local("A file of this folder cannot be read.")
	}
	return &failure{code: 4, message: "The folder's audit.jsonl is not one audit receipt row a line."}
}

// notAdmitted is the refusal of a folder one card of which fails its check: the card's brief ID, which is a
// file name of the fixed brief ID form, and the check's sentence.
func notAdmitted(id string, err error) *failure {
	if errors.Is(err, cards.ErrBriefNotFound) || errors.Is(err, cards.ErrBriefID) {
		return &failure{code: 4, message: "A file of this folder is not named with a brief ID. Nothing was admitted."}
	}
	sentence := checkSentence(err)
	if !strings.HasSuffix(sentence, ".") {
		sentence += "."
	}
	return &failure{code: 4, message: fmt.Sprintf("%s: %s Nothing was admitted.", id, sentence)}
}

// checkSentence is the fixed sentence of a check a card failed. A prepared brief fails with a code
// read_renewal_brief answers with, given here as the sentence of the same check on a saved card.
func checkSentence(err error) string {
	switch {
	case errors.Is(err, cards.ErrBriefInvalid):
		return cards.ErrInvalidCard.Error()
	case errors.Is(err, cards.ErrNotAccepted):
		return cards.ErrNotSealed.Error()
	case errors.Is(err, cards.ErrBriefNotFound), errors.Is(err, cards.ErrBriefID):
		return cards.ErrNoCard.Error()
	}
	return err.Error()
}

// admittedLog is the admitted receipts of a workspace, read as the audit log an admitted card is verified
// against.
type admittedLog struct {
	store     *postgres.Store
	workspace string
}

func (l admittedLog) Receipts(ctx context.Context, ids []string) ([]*strictjson.Object, error) {
	lines, err := l.store.AdmittedReceipts(ctx, l.workspace, ids)
	if err != nil {
		return nil, err
	}
	log, err := cards.RowsFromLines(lines)
	if err != nil {
		return nil, err
	}
	return log.Receipts(ctx, ids)
}

// admittedCard is an admitted card of the workspace, only when it verifies against the admitted receipts
// and the sources the workspace is bound to, as a card made here does against its receipts.
func (r *run) admittedCard(ctx context.Context, brief string, bound evidence.Sources) (*strictjson.Object, error) {
	stored, err := r.store.AdmittedCardByBrief(ctx, r.workspace.ID, brief)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, refused("%s", cards.ErrNoCard)
	}
	if err != nil {
		return nil, err
	}
	value, err := strictjson.Loads(stored.Bytes)
	card, _ := value.(*strictjson.Object)
	if err != nil || card == nil {
		return nil, refused("%s", cards.ErrInvalidCard)
	}
	log := admittedLog{store: r.store, workspace: r.workspace.ID}
	if stored.Kind == "prepared" {
		_, err = cards.VerifyPrepared(ctx, stored.Bytes, brief, log, bound)
	} else {
		_, err = cards.VerifyCard(ctx, stored.Bytes, brief, log, bound)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, refused("%s", checkSentence(err))
	}
	r.admitted = &stored.Word
	return card, nil
}

// admittedValue is what inspect says of a card admitted from an export: the operator's word, and that it
// was not read again from its source.
func (r *run) admittedValue() *strictjson.Object {
	return strictjson.NewObject("from_org", r.admitted.FromOrg, "export_sha256", r.admitted.ExportSHA256,
		"note", admittedNote)
}

// admittedLine is the line text output prints first for a card admitted from an export.
func (r *run) admittedLine() string {
	return "Admitted from an export, not re-read: the folder's own receipts vouch for it, on the " +
		"operator's word that it came from CRM identity " + r.admitted.FromOrg + "."
}

// admittedReceipt is an admitted receipt of the workspace as audit gives a receipt.
func (r *run) admittedReceipt(ctx context.Context, id string) (*strictjson.Object, error) {
	rows, err := (admittedLog{store: r.store, workspace: r.workspace.ID}).Receipts(ctx, []string{id})
	if errors.Is(err, receipts.ErrNotFound) {
		return nil, refused("Audit receipt not found.")
	}
	if err != nil {
		return nil, err
	}
	decoded, err := receipts.Decode(rows[0])
	if err != nil {
		return nil, refused("Audit receipt contains invalid JSON.")
	}
	from, err := r.store.AdmittedReceiptFrom(ctx, r.workspace.ID, id)
	if err != nil {
		return nil, err
	}
	r.admitted = &postgres.OperatorWord{FromOrg: from}
	return decoded.Value(), nil
}
