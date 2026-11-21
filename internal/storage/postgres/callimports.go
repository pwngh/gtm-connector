package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// CallImports is the imported call transcripts of one workspace: calls_import.py's store on this database.
// It keeps each file's bytes once, every revision made from them, the account the bytes belong to, and
// the revision each account's cards read. Every read checks a revision against its stored bytes.
type CallImports struct {
	Store       *Store
	WorkspaceID string
}

var _ calls.Imports = CallImports{}

// callsError gives a failure of the database as calls.ErrIO, with its cause. A refusal passes as it is.
func callsError(err error) error {
	var refusal *calls.Error
	if err == nil || errors.As(err, &refusal) {
		return err
	}
	return fmt.Errorf("%w: %w", calls.ErrIO, err)
}

// queries is a pool or a transaction.
type queries interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// bindingText is a CRM binding as the workspace's row holds it: its canonical JSON. A value that is not a
// binding matches no store.
func bindingText(binding *strictjson.Object) (string, error) {
	if _, valid := evidence.CRMBackend(binding); !valid {
		return "", calls.ErrSourceMismatch
	}
	text, err := strictjson.Dumps(binding, strictjson.Prepared)
	if err != nil {
		return "", calls.ErrSourceMismatch
	}
	return text, nil
}

// bound refuses a workspace whose imports are bound to another CRM. A workspace with no import is bound to
// none yet.
func (c CallImports) bound(ctx context.Context, q queries, binding *strictjson.Object, lock bool) error {
	want, err := bindingText(binding)
	if err != nil {
		return err
	}
	statement := "SELECT crm_binding FROM call_import_stores WHERE workspace_id = $1"
	if lock {
		statement += " FOR UPDATE"
	}
	var stored string
	switch err := q.QueryRow(ctx, statement, c.WorkspaceID).Scan(&stored); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	case stored != want:
		return calls.ErrSourceMismatch
	}
	return nil
}

// Import is calls_import.store_import: in one transaction it saves the file's bytes, the revision of this
// request, and the pointers that make it the account's active import. The same bytes, account, call time and
// range find the revision saved before and change nothing. Bytes, or an AccountNumber, that belong to another
// account are refused unless the import reassigns them, and an account the workspace does not hold is refused.
// The result is what debug calls import prints.
func (c CallImports) Import(ctx context.Context, in calls.Import) (*strictjson.Object, error) {
	if err := in.Check(); err != nil {
		return nil, err
	}
	a, raw, digest := in.Association, in.Parsed.Raw, in.Parsed.ContentSHA256
	backend, _ := evidence.CRMBackend(a.CRMBinding)
	binding, err := bindingText(a.CRMBinding)
	if err != nil {
		return nil, err
	}
	var result *strictjson.Object
	err = c.Store.inTx(ctx, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM accounts WHERE workspace_id = $1 AND ref = $2)",
			c.WorkspaceID, a.AccountID).Scan(&known); err != nil {
			return err
		}
		if !known {
			return calls.ErrAccountNotFound
		}
		// The row of the workspace's binding is locked until commit, so imports run one at a time.
		if _, err := tx.Exec(ctx, `INSERT INTO call_import_stores (workspace_id, crm_binding) VALUES ($1, $2)
			ON CONFLICT (workspace_id) DO NOTHING`, c.WorkspaceID, binding); err != nil {
			return err
		}
		if err := c.bound(ctx, tx, a.CRMBinding, true); err != nil {
			return err
		}
		numberReassigned := false
		if a.AccountNumber != nil {
			var owner string
			err := tx.QueryRow(ctx, `SELECT account_ref FROM call_import_numbers
				WHERE workspace_id = $1 AND account_number = $2`, c.WorkspaceID, *a.AccountNumber).Scan(&owner)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return err
			case !evidence.ValidAccountID(owner, backend == "salesforce"):
				return calls.ErrCorrupt
			default:
				numberReassigned = owner != a.AccountID
			}
		}
		if numberReassigned && !in.Reassign {
			return calls.ErrAssociationChanged
		}
		var owner string
		var generation int64
		var same bool
		err := tx.QueryRow(ctx, `SELECT account_ref, generation, raw = $3 FROM call_import_contents
			WHERE workspace_id = $1 AND content_sha256 = $2`, c.WorkspaceID, digest, raw).Scan(&owner, &generation, &same)
		stored := !errors.Is(err, pgx.ErrNoRows)
		switch {
		case !stored:
			owner, generation = a.AccountID, 1
		case err != nil:
			return err
		case !same || !evidence.ValidAccountID(owner, backend == "salesforce") || generation < 1 ||
			generation > 1<<63-2:
			return calls.ErrCorrupt
		}
		contentReassigned := owner != a.AccountID
		if contentReassigned && !in.Reassign {
			return calls.ErrReassignRequired
		}
		if contentReassigned {
			generation++
		}
		if !stored {
			if _, err := tx.Exec(ctx, `INSERT INTO call_import_contents (workspace_id, content_sha256, raw, account_ref,
				generation) VALUES ($1, $2, $3, $4, $5)`, c.WorkspaceID, digest, raw, a.AccountID, generation); err != nil {
				return err
			}
		}
		key := in.RequestKey(generation)
		revision := calls.Revision{RequestKey: key, ContentSHA256: digest, Raw: raw}
		var manifest *strictjson.Object
		err = tx.QueryRow(ctx, `SELECT import_id, account_ref, manifest FROM call_import_revisions
			WHERE workspace_id = $1 AND request_key = $2`, c.WorkspaceID, key).
			Scan(&revision.ImportID, &revision.AccountID, &revision.Manifest)
		created := errors.Is(err, pgx.ErrNoRows)
		switch {
		case created:
			if revision, manifest, err = in.Revision(generation); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO call_import_revisions (workspace_id, import_id, request_key,
				content_sha256, account_ref, manifest) VALUES ($1, $2, $3, $4, $5, $6)`, c.WorkspaceID, revision.ImportID,
				revision.RequestKey, revision.ContentSHA256, revision.AccountID, revision.Manifest); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			// The revision saved before is read back as any read reads it, against the bytes just compared.
			opened, err := revision.Open(a.CRMBinding)
			if err != nil {
				return err
			}
			manifest = opened.Manifest
		}
		if err := c.Store.at("call import pointers"); err != nil {
			return err
		}
		if contentReassigned {
			// The account the bytes leave no longer reads a revision made from them.
			if _, err := tx.Exec(ctx, `UPDATE call_import_active SET import_id = NULL
				WHERE workspace_id = $1 AND account_ref = $2 AND import_id IN (
					SELECT import_id FROM call_import_revisions WHERE workspace_id = $1 AND content_sha256 = $3)`,
				c.WorkspaceID, owner, digest); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE call_import_contents SET account_ref = $3, generation = $4
				WHERE workspace_id = $1 AND content_sha256 = $2`, c.WorkspaceID, digest, a.AccountID, generation); err != nil {
				return err
			}
		}
		if a.AccountNumber != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO call_import_numbers (workspace_id, account_number, account_ref)
				VALUES ($1, $2, $3) ON CONFLICT (workspace_id, account_number)
				DO UPDATE SET account_ref = excluded.account_ref`, c.WorkspaceID, *a.AccountNumber, a.AccountID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO call_import_active (workspace_id, account_ref, import_id)
			VALUES ($1, $2, $3) ON CONFLICT (workspace_id, account_ref)
			DO UPDATE SET import_id = excluded.import_id`, c.WorkspaceID, a.AccountID, revision.ImportID); err != nil {
			return err
		}
		result = calls.Result(manifest, revision.ImportID, created, contentReassigned || numberReassigned,
			a.ResolutionReceiptID)
		return nil
	})
	if err != nil {
		return nil, callsError(err)
	}
	return result, nil
}

// ActiveImport gives the account's active import with the bytes of its file, read in one statement, or nil
// when the account has none.
func (c CallImports) ActiveImport(ctx context.Context, binding *strictjson.Object, accountID string) (
	*calls.Active, error) {
	want, err := bindingText(binding)
	if err != nil {
		return nil, err
	}
	// The join is from the store's row, so a workspace with imports always gives one row to check the
	// binding by, whether or not the account has an active import.
	var stored string
	var importID, requestKey, contentSHA256, account, manifest, owner *string
	var generation *int64
	var raw []byte
	err = c.Store.pool.QueryRow(ctx, `
		SELECT s.crm_binding, r.import_id, r.request_key, r.content_sha256, r.account_ref, r.manifest, k.raw,
		       k.account_ref, k.generation
		  FROM call_import_stores s
		  LEFT JOIN call_import_active a ON a.workspace_id = s.workspace_id AND a.account_ref = $2
		  LEFT JOIN call_import_revisions r ON r.workspace_id = a.workspace_id AND r.import_id = a.import_id
		  LEFT JOIN call_import_contents k ON k.workspace_id = r.workspace_id AND k.content_sha256 = r.content_sha256
		 WHERE s.workspace_id = $1`, c.WorkspaceID, accountID).
		Scan(&stored, &importID, &requestKey, &contentSHA256, &account, &manifest, &raw, &owner, &generation)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, callsError(err)
	case stored != want:
		return nil, calls.ErrSourceMismatch
	}
	if importID == nil {
		return nil, nil
	}
	return &calls.Active{Revision: calls.Revision{ImportID: *importID, RequestKey: *requestKey,
		ContentSHA256: *contentSHA256, AccountID: *account, Manifest: *manifest, Raw: raw},
		ContentAccount: *owner, ContentGeneration: *generation}, nil
}

// List is calls_import.list_imports: a page of the workspace's imports in order of their IDs, of one account
// or of all, starting after an ID. No file's bytes are read.
func (c CallImports) List(ctx context.Context, binding *strictjson.Object, accountID *string, limit int,
	after *string) (*strictjson.Object, error) {
	backend, valid := evidence.CRMBackend(binding)
	if !valid || limit < 1 || limit > 100 ||
		accountID != nil && !evidence.ValidAccountID(*accountID, backend == "salesforce") ||
		after != nil && !calls.ValidImportID(*after) {
		return nil, calls.ErrInvalid
	}
	if err := c.bound(ctx, c.Store.pool, binding, false); err != nil {
		return nil, callsError(err)
	}
	start := ""
	if after != nil {
		start = *after
	}
	rows, err := c.Store.pool.Query(ctx, `
		SELECT r.import_id, r.request_key, r.content_sha256, r.account_ref, r.manifest, a.account_ref
		  FROM call_import_revisions r
		  LEFT JOIN call_import_active a ON a.workspace_id = r.workspace_id AND a.import_id = r.import_id
		 WHERE r.workspace_id = $1 AND r.import_id > $2 AND ($3::text IS NULL OR r.account_ref = $3)
		 ORDER BY r.import_id LIMIT $4`, c.WorkspaceID, start, accountID, limit+1)
	if err != nil {
		return nil, callsError(err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (calls.Listed, error) {
		var listed calls.Listed
		r := &listed.Revision
		err := row.Scan(&r.ImportID, &r.RequestKey, &r.ContentSHA256, &r.AccountID, &r.Manifest, &listed.ActiveAccount)
		return listed, err
	})
	if err != nil {
		return nil, callsError(err)
	}
	return calls.Listing(found, binding, limit)
}

// Inspect is calls_import.inspect_import: the saved excerpt of one import, checked against its stored bytes.
// Only full adds every unit of the file and its original text.
func (c CallImports) Inspect(ctx context.Context, binding *strictjson.Object, importID string, full bool) (
	*strictjson.Object, error) {
	if !calls.ValidImportID(importID) {
		return nil, calls.ErrInvalid
	}
	if err := c.bound(ctx, c.Store.pool, binding, false); err != nil {
		return nil, callsError(err)
	}
	revision := calls.Revision{ImportID: importID}
	var active *string
	err := c.Store.pool.QueryRow(ctx, `
		SELECT r.request_key, r.content_sha256, r.account_ref, r.manifest, k.raw, a.account_ref
		  FROM call_import_revisions r
		  JOIN call_import_contents k ON k.workspace_id = r.workspace_id AND k.content_sha256 = r.content_sha256
		  LEFT JOIN call_import_active a ON a.workspace_id = r.workspace_id AND a.import_id = r.import_id
		 WHERE r.workspace_id = $1 AND r.import_id = $2`, c.WorkspaceID, importID).
		Scan(&revision.RequestKey, &revision.ContentSHA256, &revision.AccountID, &revision.Manifest, &revision.Raw, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, calls.ErrNotFound
	}
	if err != nil {
		return nil, callsError(err)
	}
	return calls.Inspection(revision, binding, active, full)
}
