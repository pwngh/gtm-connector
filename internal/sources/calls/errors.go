// Package calls imports one call transcript for one account and serves it to the card maker
// (gtm_connector/calls_import.py). It parses a UTF-8 TXT, WebVTT or SRT export into transcript units, selects
// an excerpt, and builds the manifest and record a stored import is read back as. It contacts no provider.
// The store of imports is internal/storage/postgres.
package calls

// Error is a refusal, by its fixed code. It never carries text of the file or of a source.
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

// The refusals of an import, a read and the commands over them.
var (
	ErrInvalid             = &Error{"calls_import_invalid"}
	ErrTooLarge            = &Error{"calls_import_too_large"}
	ErrExcerptTooLarge     = &Error{"calls_excerpt_too_large"}
	ErrSpeakerNamesMissing = &Error{"calls_speaker_names_missing"}
	ErrNotFound            = &Error{"calls_import_not_found"}
	ErrAccountNotFound     = &Error{"calls_account_not_found"}
	ErrReassignRequired    = &Error{"calls_reassign_required"}
	ErrAssociationChanged  = &Error{"calls_association_changed"}
	ErrSourceMismatch      = &Error{"calls_source_mismatch"}
	ErrAccountAmbiguous    = &Error{"calls_account_ambiguous"}
	ErrCorrupt             = &Error{"calls_import_corrupt"}
	ErrAssociationInvalid  = &Error{"calls_association_invalid"}
	ErrAssociationFailed   = &Error{"calls_association_failed"}
	ErrSourceUnavailable   = &Error{"calls_source_unavailable"}
	ErrIO                  = &Error{"calls_import_io"}
)

var categories = map[*Error]string{
	ErrInvalid: "usage", ErrTooLarge: "usage", ErrExcerptTooLarge: "usage", ErrSpeakerNamesMissing: "usage",
	ErrNotFound: "usage", ErrAccountNotFound: "usage",
	ErrReassignRequired: "refusal", ErrAssociationChanged: "refusal", ErrSourceMismatch: "refusal",
	ErrAccountAmbiguous: "refusal",
	ErrCorrupt:          "check", ErrAssociationInvalid: "check", ErrAssociationFailed: "check",
	ErrSourceUnavailable: "upstream", ErrIO: "io",
}

// Category is the kind of refusal: usage, refusal, check, upstream or io.
func (e *Error) Category() string {
	if category, ok := categories[e]; ok {
		return category
	}
	return "check"
}

// ExitCode is the exit code of a command that ends with this refusal.
func (e *Error) ExitCode() int {
	return map[string]int{"usage": 2, "refusal": 3, "check": 4, "upstream": 5, "io": 1}[e.Category()]
}
