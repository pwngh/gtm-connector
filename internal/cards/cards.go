// Package cards makes, seals, verifies and admits Deal cards.
//
// A card is its stored bytes plus their SHA-256. Sealing writes the bytes and finishes the seal receipt
// with the digest; verification re-hashes the stored bytes and never re-encodes them, so Go admits a card
// Python made without reproducing Python's encoder. Two kinds are admitted: a card file saved by brief or
// demo (sealed by its save_brief receipt) and a brief saved by prepare_renewal_brief (sealed by that outer
// receipt). Cards Go makes are byte-identical to the oracle's: the same json.dumps variant per kind.
//
// # The unit identity model
//
// A record version is identified by its source, table, record ID and content version (RecordVersion). For
// Slack the record ID will be the logical message ID, scoped to the provider, workspace, channel and
// message ts; a card made before that keeps its msg- ID, aliased to the logical ID once the Slack reader
// supplies it. For a record of an admitted Python card, the content version is the SHA-256 of the record as
// read, in the canonical encoding (keys sorted, no spaces, UTF-8), so two reads that returned the same
// values name the same version.
//
// A unit is identified by its card and its number (UnitRef). It is bound to the record version it came from
// and to the ID of the read receipt that returned that record (UnitBinding). A Python card binds a unit to
// its receipt through the unit's tool name, one receipt per tool per card, and is admitted and traced that
// way; binding by receipt ID is how Go-made units will bind once section 4, item 2 of the backend plan is
// decided.
//
// An approval basis is a digest of what was approved: the cited record versions and the digests of their
// units' text (UnitBinding.TextSHA256), the artifact version digests, the template and policy versions, and
// the scope, owner and eligibility. It excludes receipt IDs, snapshot IDs and read times, so a refresh, which
// is a new card for the same account, keeps an approval current unless a cited record version changed or
// was withdrawn. The workflow builds the basis; this package supplies the bindings it is built from.
package cards

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// RecordVersion identifies one version of one source record.
type RecordVersion struct {
	Source         string // "crm", "calls" or "slack"
	Table          string
	RecordID       string
	ContentVersion string // "sha256:" and the digest of the record's canonical encoding
}

// UnitRef identifies a unit: its card and its number.
type UnitRef struct {
	BriefID string
	N       int
}

// UnitBinding binds a unit to the record version it came from and the read receipt that returned it.
type UnitBinding struct {
	Unit       UnitRef
	Record     RecordVersion
	ReceiptID  string
	TextSHA256 string // the digest of a sentence unit's sealed text; empty for a record unit
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// contentVersion is a record's content version: the digest of its canonical encoding.
func contentVersion(record *strictjson.Object) (string, error) {
	canonical, err := strictjson.Dumps(record, strictjson.Prepared)
	if err != nil {
		return "", err
	}
	return "sha256:" + sha([]byte(canonical)), nil
}

// Reference is the row a seal names: briefs/<brief ID>/<digest of the bytes>.
func Reference(briefID string, payload []byte) string {
	return "briefs/" + briefID + "/" + sha(payload)
}
