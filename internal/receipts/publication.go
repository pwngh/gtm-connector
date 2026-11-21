package receipts

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The rules every Store applies before it publishes a card, written once so that Memory and PostgreSQL
// refuse the same publications. A store finds the seal and checks it with CheckSeal; checks the grant of the
// seal's principal; gathers the reads the card's bytes name and the versions they bound, and checks them
// with CheckPublication; and last rechecks the generation of every one of those reads.

var (
	// ErrProvenance refuses a card that rests on evidence its receipts do not show. The seal finishes as
	// error/422 provenance.
	ErrProvenance = fmt.Errorf("%w: the card rests on evidence its receipts do not show", ErrInvalid)
	// ErrProjection refuses units and gaps that are not exactly the card's own. The seal finishes as
	// error/422 projection.
	ErrProjection = fmt.Errorf("%w: the units and gaps differ from the card's bytes", ErrInvalid)
)

// SaveBrief is the tool whose receipt seals a card made by brief or demo.
const SaveBrief = "save_brief"

// Payload is what a card's bytes say about their own evidence.
type Payload struct {
	BriefID, AccountID string
	// Prepared is a brief prepare_renewal_brief saved; otherwise the card was saved by brief.
	Prepared bool
	// SealID is sealed_by, or prepared_by's outer_audit_id; TokenID is prepared_by's token_id.
	SealID, TokenID string
	// Receipts are evidence.receipts, the reads the card rests on, in order; ReadIDs their audit IDs.
	Receipts    []*strictjson.Object
	ReadIDs     []string
	Units, Gaps []any
	records     map[string]*strictjson.Object
}

// Record is the first record of evidence.records with this reference, as the card's numbers resolve it.
func (p *Payload) Record(ref string) (*strictjson.Object, bool) {
	record, found := p.records[ref]
	return record, found
}

func text(o *strictjson.Object, key string) (string, bool) {
	value, _ := o.Get(key)
	s, ok := value.(string)
	return s, ok
}

// ParsePayload reads a card's bytes. It refuses bytes that are not a card naming its brief, its account, its
// seal, its units and gaps, and the receipts and records of its evidence.
func ParsePayload(data []byte) (*Payload, error) {
	value, err := strictjson.Loads(data)
	card, isObject := value.(*strictjson.Object)
	if err != nil || !isObject {
		return nil, fmt.Errorf("%w: the bytes are not a JSON object", ErrProjection)
	}
	p := &Payload{records: map[string]*strictjson.Object{}}
	var ok [4]bool
	p.BriefID, ok[0] = text(card, "brief_id")
	p.AccountID, ok[1] = text(card, "account_id")
	units, _ := card.Get("units")
	gaps, _ := card.Get("gaps")
	p.Units, ok[2] = units.([]any)
	p.Gaps, ok[3] = gaps.([]any)
	if !ok[0] || !ok[1] || !ok[2] || !ok[3] {
		return nil, fmt.Errorf("%w: the card does not name its brief, account, units and gaps", ErrProjection)
	}
	kind, hasKind := card.Get("kind")
	switch {
	case !hasKind:
		p.SealID, ok[0] = text(card, "sealed_by")
	case kind == "selected_brief":
		p.Prepared = true
		preparedBy, _ := card.Get("prepared_by")
		by, isObject := preparedBy.(*strictjson.Object)
		ok[0] = isObject
		if isObject {
			p.SealID, ok[0] = text(by, "outer_audit_id")
			p.TokenID, ok[1] = text(by, "token_id")
			ok[0] = ok[0] && ok[1]
		}
	default:
		ok[0] = false
	}
	if !ok[0] {
		return nil, fmt.Errorf("%w: the card does not name the receipt that sealed it", ErrProvenance)
	}
	snapshot, _ := card.Get("evidence")
	evidence, isObject := snapshot.(*strictjson.Object)
	if !isObject {
		return nil, fmt.Errorf("%w: the card holds no evidence", ErrProvenance)
	}
	brief, _ := text(evidence, "brief_id")
	account, _ := text(evidence, "account_id")
	receiptList, _ := evidence.Get("receipts")
	recordList, _ := evidence.Get("records")
	read, readsOK := receiptList.([]any)
	records, recordsOK := recordList.([]any)
	if brief != p.BriefID || account != p.AccountID || !readsOK || !recordsOK {
		return nil, fmt.Errorf("%w: the evidence is not this brief's", ErrProvenance)
	}
	seen := map[string]bool{}
	for _, value := range read {
		r, isObject := value.(*strictjson.Object)
		id, isText := "", false
		if isObject {
			id, isText = text(r, "audit_id")
		}
		if !isText || seen[id] {
			return nil, fmt.Errorf("%w: evidence.receipts names a read twice or not at all", ErrProvenance)
		}
		seen[id] = true
		p.Receipts, p.ReadIDs = append(p.Receipts, r), append(p.ReadIDs, id)
	}
	for _, value := range records {
		record, isObject := value.(*strictjson.Object)
		if !isObject {
			return nil, fmt.Errorf("%w: a record is not an object", ErrProvenance)
		}
		if ref, isText := text(record, "record_ref"); isText {
			if _, first := p.records[ref]; !first {
				p.records[ref] = record
			}
		}
	}
	return p, nil
}

// CheckSeal refuses a seal that cannot seal this publication: one of another workspace or account, a child,
// one that names a brief_id (the oracle's seals never do), and any tool but the one this kind of card is
// sealed by. A card is sealed by a save_brief receipt with no request ID whose arguments are exactly its
// account and brief; a prepared brief by its own prepare_renewal_brief call, whose arguments name its
// account. The seal stays pending.
func CheckSeal(seal Receipt, p Publication) error {
	_, _, isChild := seal.RequestID.Parent()
	arguments, _ := seal.Arguments.(*strictjson.Object)
	account := ""
	if arguments != nil {
		account, _ = text(arguments, "account_id")
	}
	switch {
	case seal.WorkspaceID != p.WorkspaceID || seal.AccountRef != p.AccountRef || isChild || seal.BriefID != nil:
	case p.Kind == "card" && seal.Tool == SaveBrief && seal.RequestID.Value() == nil &&
		sameValue(seal.Arguments, strictjson.NewObject("account_id", p.AccountRef, "brief_id", p.BriefID)):
		return nil
	case p.Kind == "selected_brief" && seal.Tool == Composite && account == p.AccountRef:
		return nil
	}
	return fmt.Errorf("%w: a card is sealed by its own save_brief receipt, a prepared brief by its own %s call",
		ErrInvalid, Composite)
}

// sameValue compares two JSON values exactly: types, numbers as written, and every key.
func sameValue(a, b any) bool {
	x, errA := strictjson.Dumps(a, strictjson.Prepared)
	y, errB := strictjson.Dumps(b, strictjson.Prepared)
	return errA == nil && errB == nil && x == y
}

// underBrief is whether a read is one of this brief's: of the card's workspace and account, made for this
// brief through a connection, by the seal's principal under the seal's token, and either a top-level read
// (a card saved by brief) or a child of the seal (a prepared brief).
func underBrief(r, seal Receipt, p Publication) bool {
	parent, _, isChild := r.RequestID.Parent()
	return r.WorkspaceID == p.WorkspaceID && r.AccountRef == p.AccountRef && r.BriefID != nil &&
		*r.BriefID == p.BriefID && r.ConnectionID != "" && r.PrincipalID == seal.PrincipalID &&
		r.TokenID == seal.TokenID &&
		(seal.Tool == SaveBrief && !isChild || seal.Tool == Composite && isChild && parent == seal.AuditID)
}

// CheckPublication refuses a card whose bytes and projections do not rest on exactly what the store holds.
// reads holds each read the bytes name that the store found in the workspace, and bound the versions each
// bound. It checks that:
//   - the bytes are this brief's card for this account, under this seal;
//   - the units and gaps are exactly the bytes' own, in order and type for type (ErrProjection);
//   - Reads is exactly the reads evidence.receipts names, in order;
//   - each of those reads is this brief's (underBrief), has finished, and is embedded exactly as stored;
//   - each unit cites one of them that finished ok, under the same tool, and that bound the unit's record
//     version, whose body is exactly the record the bytes show for the unit's reference.
//
// The generation of each read is the store's to recheck afterwards.
func CheckPublication(p Publication, payload *Payload, seal Receipt, reads map[string]Receipt,
	bound map[string][]RecordVersion) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrProvenance}, args...)...)
	}
	if payload.BriefID != p.BriefID || payload.AccountID != p.AccountRef || payload.SealID != seal.AuditID ||
		payload.Prepared != (seal.Tool == Composite) || payload.Prepared && payload.TokenID != seal.TokenID {
		return refuse("the bytes are not this brief's card under this seal")
	}
	if err := checkProjection(payload, p.Units, p.Gaps); err != nil {
		return err
	}
	if len(p.Reads) != len(payload.ReadIDs) {
		return refuse("the publication names %d reads, the card %d", len(p.Reads), len(payload.ReadIDs))
	}
	inCard := map[string]bool{}
	for i, id := range payload.ReadIDs {
		inCard[id] = true
		r, found := reads[id]
		embedded, err := Decode(payload.Receipts[i])
		switch {
		case p.Reads[i] != id:
			return refuse("read %d is %s in the publication and %s in the card", i+1, p.Reads[i], id)
		case !found || err != nil || !embedded.Same(r):
			return refuse("read %s is not embedded as the store holds it", id)
		case r.Status == "pending" || !underBrief(r, seal, p):
			return refuse("read %s is not a finished read of this brief", id)
		}
	}
	cited := IndexBound(bound)
	// The record the bytes show for each reference, encoded once however many units cite it; nil when the
	// bytes show none or it does not encode.
	shown := map[string]*string{}
	for _, u := range p.Units {
		r, found := reads[u.ReceiptID]
		if !inCard[u.ReceiptID] || !found || r.Status != "ok" || r.Tool != u.Tool {
			return refuse("unit %d cites a read the card does not rest on", u.N)
		}
		version, isBound := cited[Citation{u.ReceiptID, u.RecordRef, u.ContentVersion}]
		body, done := shown[u.RecordRef]
		if !done {
			if record, inBytes := payload.Record(u.RecordRef); inBytes {
				if text, err := strictjson.Dumps(record, strictjson.Prepared); err == nil {
					body = &text
				}
			}
			shown[u.RecordRef] = body
		}
		if !isBound || body == nil || *body != string(version.Body) {
			return refuse("unit %d's record is not the version its read bound", u.N)
		}
	}
	return nil
}

// Citation names a record version by the read that bound it, its reference and its content version.
type Citation struct{ ReceiptID, Ref, Version string }

// IndexBound gives, for each citation, the first version its read bound under that reference and content
// version, so a card's units find theirs in time that does not grow with the read.
func IndexBound(bound map[string][]RecordVersion) map[Citation]RecordVersion {
	index := map[Citation]RecordVersion{}
	for receipt, versions := range bound {
		for _, v := range versions {
			key := Citation{receipt, v.Ref(), v.Version()}
			if _, seen := index[key]; !seen {
				index[key] = v
			}
		}
	}
	return index
}

// checkProjection requires units and gaps to be exactly the payload's, in order and type for type.
func checkProjection(payload *Payload, units []UnitRow, gaps []GapRow) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrProjection}, args...)...)
	}
	if len(units) != len(payload.Units) || len(gaps) != len(payload.Gaps) {
		return refuse("%d units and %d gaps for a card of %d and %d", len(units), len(gaps), len(payload.Units),
			len(payload.Gaps))
	}
	for i, u := range units {
		value, err := strictjson.Loads(u.Value)
		want, isObject := payload.Units[i].(*strictjson.Object)
		if u.N != i+1 || err != nil || !isObject || !sameValue(value, want) {
			return refuse("unit %d", i+1)
		}
		kind, _ := text(want, "kind")
		ref, _ := text(want, "record_ref")
		tool, _ := text(want, "tool")
		shown, _ := text(want, "shown")
		if kind != u.Kind || ref != u.RecordRef || tool != u.Tool || kind == "sentence" && shown != u.Shown {
			return refuse("unit %d's columns differ from its value", i+1)
		}
	}
	for i, g := range gaps {
		var count, byRole any
		if g.Count != nil {
			count = new(big.Int).Set(g.Count)
		}
		if g.ByRole != nil {
			var err error
			if byRole, err = strictjson.Loads(g.ByRole); err != nil {
				return refuse("gap %s", g.Code)
			}
		}
		row := strictjson.NewObject("code", g.Code, "disposition", g.Disposition, "count", count, "by_role", byRole,
			"text", g.Text)
		if !sameValue(row, payload.Gaps[i]) {
			return refuse("gap %s", g.Code)
		}
	}
	return nil
}

// IsRecordRow is whether a receipt row names a source record (crm/, calls/ or slack/), which only a source
// read may claim, and only for the versions it bound.
func IsRecordRow(row string) bool {
	source, _, _ := strings.Cut(row, "/")
	return source == "crm" || source == "calls" || source == "slack"
}
