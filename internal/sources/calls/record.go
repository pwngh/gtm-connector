package calls

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Association is how an import is bound to its account: the account a CRM read resolved, under the receipt
// of that read.
type Association struct {
	CRMBinding *strictjson.Object
	AccountID  string
	Method     string // account_id or account_number
	// AccountNumber is the private selector of an account_number resolution. No manifest holds it.
	AccountNumber       *string
	ResolutionReceiptID string
}

// public is the association as a manifest holds it.
func (a Association) public() *strictjson.Object {
	return strictjson.NewObject("crm_binding", a.CRMBinding, "account_id", a.AccountID, "method", a.Method,
		"resolution_receipt_id", a.ResolutionReceiptID)
}

// check is calls_import._validate_association.
func (a Association) check() error {
	if !evidence.ValidAssociation(a.public()) {
		return ErrAssociationInvalid
	}
	if a.Method == "account_id" {
		if a.AccountNumber != nil {
			return ErrAssociationInvalid
		}
		return nil
	}
	if a.AccountNumber == nil {
		return ErrAssociationInvalid
	}
	number, ok := evidence.SourceText(*a.AccountNumber, 160, true)
	if !ok || utf8.RuneCountInString(number) > 40 || strings.ContainsAny(number, "\r\n\t") {
		return ErrAssociationInvalid
	}
	return nil
}

// Import is one import ready to be saved: the file as parsed, its excerpt, its account, and its times in UTC
// as YYYY-MM-DDTHH:MM:SSZ. CalledAt is nil when the time of the call is unknown.
type Import struct {
	Parsed      *Parsed
	Excerpt     *Excerpt
	Association Association
	CalledAt    *string
	ImportedAt  string
	// Reassign moves content, or an AccountNumber, that belongs to another account.
	Reassign bool
}

// Check is what calls_import.store_import checks before it opens its store: the association, the times,
// and that the parsed file and its excerpt are what its own bytes give. The bytes are parsed again as a new
// import, under every rule Parse holds a file to, so no field of Parsed that a caller set by hand is taken
// on trust: an import that is saved is one Parse made.
func (i Import) Check() error {
	if err := i.Association.check(); err != nil {
		return err
	}
	if i.CalledAt != nil && !evidence.UTCStamp(*i.CalledAt) || !evidence.UTCStamp(i.ImportedAt) || i.Parsed == nil ||
		i.Excerpt == nil {
		return ErrInvalid
	}
	reparsed, err := Parse(i.Parsed.Raw, i.Parsed.Format, i.Parsed.SpeakerNames)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(reparsed, i.Parsed) {
		return ErrInvalid
	}
	var selection *Range
	if i.Excerpt.SelectionMode != "default" {
		selection = &i.Excerpt.Requested
	}
	selected, err := SelectExcerpt(reparsed, selection)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(selected, i.Excerpt) {
		return ErrInvalid
	}
	return nil
}

func (i Import) calledAt() any {
	if i.CalledAt == nil {
		return nil
	}
	return *i.CalledAt
}

// canonical is calls_import._canonical: sorted keys, no spaces, and text as UTF-8.
func canonical(value any) string {
	text, err := strictjson.Dumps(value, strictjson.Prepared)
	if err != nil {
		panic(err) // every value here is built from strings, integers, lists and objects
	}
	return text
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// requestKey is calls_import._request_key: what makes two requests the same import. The receipt of the
// resolution, the private selector and the time of the import are not part of it.
func requestKey(contentSHA256, format any, speakerNames bool, binding, accountID, method, calledAt, generation, mode,
	requested, cited any) string {
	key := strictjson.NewObject("content_sha256", contentSHA256, "format", format,
		"parser_version", evidence.ParserVersion,
		"association", strictjson.NewObject("crm_binding", binding, "account_id", accountID, "method", method),
		"called_at", calledAt, "association_generation", generation, "selection_mode", mode,
		"requested_range", requested, "cited_range", cited)
	if speakerNames {
		key.Set("speaker_names", true)
	}
	return digest(canonical(key))
}

// RequestKey is the key of this import at one generation of its content's association.
func (i Import) RequestKey(generation int64) string {
	a := i.Association
	return requestKey(i.Parsed.ContentSHA256, i.Parsed.Format, i.Parsed.SpeakerNames, a.CRMBinding, a.AccountID, a.Method,
		i.calledAt(), generation, i.Excerpt.SelectionMode, i.Excerpt.Requested.value(), i.Excerpt.Cited.value())
}

// manifestRequestKey is the key of the request a checked manifest was saved for.
func manifestRequestKey(manifest *strictjson.Object) string {
	association := member(manifest, "association").(*strictjson.Object)
	_, declared := manifest.Get("speaker_names")
	return requestKey(member(manifest, "content_sha256"), member(manifest, "format"), declared,
		member(association, "crm_binding"), member(association, "account_id"), member(association, "method"),
		member(manifest, "called_at"), member(manifest, "association_generation"), member(manifest, "selection_mode"),
		member(manifest, "requested_range"), member(manifest, "cited_range"))
}

// Revision is one saved import as its store holds it: the manifest's canonical text, under the ID its digest
// gives and the key a repeat of its request finds it by, and the bytes of the file.
type Revision struct {
	ImportID      string
	RequestKey    string
	ContentSHA256 string
	AccountID     string
	Manifest      string
	Raw           []byte
}

// Revision is the revision this import makes at one generation of its content's association, with its
// manifest. The record the manifest makes passes the check every later read makes.
func (i Import) Revision(generation int64) (Revision, *strictjson.Object, error) {
	p, e := i.Parsed, i.Excerpt
	manifest := strictjson.NewObject("schema_version", 1, "source", "user_supplied_export", "format", p.Format,
		"parser_version", evidence.ParserVersion, "content_sha256", p.ContentSHA256, "raw_byte_count", len(p.Raw),
		"association", i.Association.public(), "association_generation", generation, "called_at", i.calledAt(),
		"imported_at", i.ImportedAt, "selection_mode", e.SelectionMode, "unit_kind", e.UnitKind,
		"requested_range", e.Requested.value(), "cited_range", e.Cited.value(), "total_units", e.TotalUnits,
		"units", unitValues(e.Units))
	if p.SpeakerNames {
		manifest.Set("speaker_names", true)
	}
	revision := Revision{RequestKey: i.RequestKey(generation), ContentSHA256: p.ContentSHA256,
		AccountID: i.Association.AccountID, Manifest: canonical(manifest), Raw: p.Raw}
	revision.ImportID = "note-" + digest(revision.Manifest)
	_, saved, err := revision.snapshot(i.Association.CRMBinding)
	return revision, saved, err
}

// member is a member of an object, or nil when the object has none by that name.
func member(o *strictjson.Object, key string) any {
	value, _ := o.Get(key)
	return value
}

// integer is a whole number a manifest holds, or -1 for any other value.
func integer(value any) int64 {
	if n, ok := value.(*big.Int); ok && n.IsInt64() {
		return n.Int64()
	}
	return -1
}

// snapshot reads the manifest and makes its record, checked as calls_import.validate_imported_record checks
// a saved record: against itself, the account and the CRM binding, without the file's bytes. The ID is the
// digest of the manifest's text exactly as stored, and that text is the manifest's canonical encoding, so a
// manifest written any other way does not pass under the ID of the canonical one.
func (r Revision) snapshot(binding *strictjson.Object) (record, manifest *strictjson.Object, err error) {
	if len(r.Manifest) > evidence.MaxRecordBytes || r.ImportID != "note-"+digest(r.Manifest) {
		return nil, nil, ErrCorrupt
	}
	value, err := strictjson.LoadsText(r.Manifest)
	if err != nil {
		return nil, nil, ErrCorrupt
	}
	manifest, _ = value.(*strictjson.Object)
	record, ok := evidence.ImportedRecord(manifest)
	if !ok {
		return nil, nil, ErrCorrupt
	}
	if encoded, err := strictjson.Dumps(manifest, strictjson.Prepared); err != nil || encoded != r.Manifest {
		return nil, nil, ErrCorrupt
	}
	accountID, ok := member(record, "account_id").(string)
	if !ok || !evidence.ValidImportedRecord(record, accountID, binding) ||
		member(record, "id") != r.ImportID || accountID != r.AccountID {
		return nil, nil, ErrCorrupt
	}
	return record, manifest, nil
}

// Opened is a saved import read back: its record, its manifest and the whole file as parsed.
type Opened struct {
	Record   *strictjson.Object
	Manifest *strictjson.Object
	Parsed   *Parsed
}

// Open is calls_import._load_record: the record of a saved import, after its manifest is checked against
// its row and its excerpt is selected again from the stored bytes. An import that fails any check is
// corrupt, whatever the reason.
func (r Revision) Open(binding *strictjson.Object) (*Opened, error) {
	record, manifest, err := r.snapshot(binding)
	if err != nil {
		return nil, err
	}
	if member(manifest, "content_sha256") != r.ContentSHA256 || manifestRequestKey(manifest) != r.RequestKey ||
		len(r.Raw) < 1 || len(r.Raw) > evidence.MaxRawBytes {
		return nil, ErrCorrupt
	}
	_, declared := manifest.Get("speaker_names")
	parsed, err := parse(r.Raw, member(manifest, "format").(string), declared, true)
	if err != nil {
		return nil, ErrCorrupt
	}
	var selection *Range
	requested := member(manifest, "requested_range").(*strictjson.Object)
	if member(manifest, "selection_mode") != "default" {
		selection = &Range{int(integer(member(requested, "first"))), int(integer(member(requested, "last")))}
	}
	selected, err := SelectExcerpt(parsed, selection)
	if err != nil {
		return nil, ErrCorrupt
	}
	again := strictjson.NewObject("units", unitValues(selected.Units), "total_units", selected.TotalUnits,
		"requested_range", selected.Requested.value(), "cited_range", selected.Cited.value(),
		"unit_kind", selected.UnitKind, "selection_mode", selected.SelectionMode)
	for n, key := range again.Keys {
		if !evidence.Equal(again.Values[n], member(manifest, key)) {
			return nil, ErrCorrupt
		}
	}
	if int64(len(r.Raw)) != integer(member(manifest, "raw_byte_count")) ||
		parsed.ContentSHA256 != member(manifest, "content_sha256") {
		return nil, ErrCorrupt
	}
	return &Opened{Record: record, Manifest: manifest, Parsed: parsed}, nil
}

// metadata is calls_import._metadata: what the commands show of an import without its text.
func metadata(manifest *strictjson.Object, importID string) *strictjson.Object {
	association := member(manifest, "association").(*strictjson.Object)
	shown := strictjson.NewObject("import_id", importID, "account_id", member(association, "account_id"))
	for _, key := range []string{"format", "called_at", "imported_at", "content_sha256", "requested_range",
		"cited_range", "total_units", "unit_kind", "selection_mode"} {
		shown.Set(key, strictjson.Clone(member(manifest, key)))
	}
	_, declared := manifest.Get("speaker_names")
	shown.Set("speaker_names", declared)
	return shown
}

// Result is what calls_import.store_import returns for an import its store saved or found: whether it is
// new, whether it moved content or an AccountNumber to this account, and the import's metadata.
func Result(manifest *strictjson.Object, importID string, created, reassigned bool, receiptID string) *strictjson.Object {
	status := "unchanged"
	if created {
		status = "imported"
	}
	result := strictjson.NewObject("schema_version", 1, "source", "imported_transcript", "status", status,
		"created", created, "selected", true, "reassigned", reassigned, "resolution_receipt_id", receiptID)
	shown := metadata(manifest, importID)
	for n, key := range shown.Keys {
		result.Set(key, shown.Values[n])
	}
	return result
}

// ValidImportID is whether text is an import ID: note- and the SHA-256 of a manifest.
func ValidImportID(text string) bool {
	digits, ok := strings.CutPrefix(text, "note-")
	if !ok || len(digits) != 64 {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if !digit(digits[i]) && (digits[i] < 'a' || digits[i] > 'f') {
			return false
		}
	}
	return true
}

// Listed is one saved import in a listing, and the account it is the active import of, if any.
type Listed struct {
	Revision      Revision // without the file's bytes
	ActiveAccount *string
}

// Listing is calls_import.list_imports' result for the imports found in order of their IDs: at most limit
// of them, and where the next page starts when there are more. No file's bytes are read.
func Listing(found []Listed, binding *strictjson.Object, limit int) (*strictjson.Object, error) {
	imports := []any{}
	var last any
	for n, row := range found {
		record, manifest, err := row.Revision.snapshot(binding)
		if err != nil || row.ActiveAccount != nil && *row.ActiveAccount != member(record, "account_id") {
			return nil, ErrCorrupt
		}
		if n == limit {
			last = member(imports[limit-1].(*strictjson.Object), "import_id")
			break
		}
		shown := metadata(manifest, row.Revision.ImportID)
		shown.Set("active", row.ActiveAccount != nil)
		imports = append(imports, shown)
	}
	return strictjson.NewObject("schema_version", 1, "source", "imported_transcript", "imports", imports,
		"next_after", last), nil
}

// Inspection is calls_import.inspect_import's result: the saved excerpt of one import. Only full adds every
// unit of the file and its original text.
func Inspection(r Revision, binding *strictjson.Object, activeAccount *string, full bool) (*strictjson.Object, error) {
	opened, err := r.Open(binding)
	if err != nil {
		return nil, err
	}
	if activeAccount != nil && *activeAccount != member(opened.Record, "account_id") {
		return nil, ErrCorrupt
	}
	shown := metadata(opened.Manifest, r.ImportID)
	shown.Set("active", activeAccount != nil)
	result := strictjson.NewObject("schema_version", 1, "source", "imported_transcript", "import", shown,
		"body", member(opened.Record, "body"), "units", strictjson.Clone(member(opened.Manifest, "units")))
	if full {
		result.Set("units", unitValues(opened.Parsed.Units))
		result.Set("original_text", string(r.Raw))
	}
	return result, nil
}
