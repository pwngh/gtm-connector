package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// admit admits the cards a Python state folder holds, and the receipts they rest on, into a workspace.
// Each card is verified against its seal and its reads in the folder's audit log export and against the
// sources the workspace is connected to, and kept as the folder holds it. A folder any card of which fails
// admits nothing.
func (r *run) admit(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	folder, err := cards.OpenTrustedExport(r.args.positionals[0])
	if err != nil {
		return nil, unreadableFolder(err)
	}
	ids := folder.BriefIDs()
	if len(ids) == 0 {
		return nil, refused("This folder holds no saved card: no briefs or selected-briefs file.")
	}
	var admitted []postgres.AdmittedCard
	rows := map[string][]byte{}
	for _, id := range ids {
		verified, err := folder.Verify(ctx, id, from.sources())
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, notAdmitted(id, err)
		}
		kind := "cli"
		if verified.Kind == "selected_brief" {
			kind = "prepared"
		}
		admitted = append(admitted, postgres.AdmittedCard{BriefID: id, Kind: kind, AccountID: verified.AccountID,
			Bytes: folder.Payload(id), SealID: verified.SealID})
		for _, receipt := range append([]string{verified.SealID}, verified.ReadIDs...) {
			rows[receipt] = folder.Lines[receipt]
		}
	}
	added, err := r.store.Admit(ctx, r.workspace.ID, admitted, rows)
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
	return card, nil
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
	return decoded.Value(), nil
}
