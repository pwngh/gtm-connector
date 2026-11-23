package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// gtm.yaml as the database keeps it: every revision a workspace previewed, written once under the SHA-256 of
// its bytes, and the activations that made one of them the workspace's active file.

// ConfigRevision is one stored revision of a workspace's file.
type ConfigRevision struct {
	Revision string
	Body     []byte
	// Settings are the settings the connector read from the file, as JSON.
	Settings json.RawMessage
	StoredAt time.Time
}

// ConfigActivation is one activation: the revision, when, and its activate_config receipt.
type ConfigActivation struct {
	Revision    string
	ReceiptID   string
	ActivatedAt time.Time
}

// ErrNoRevision is a revision the workspace has not stored.
var ErrNoRevision = errors.New("the workspace holds no file of this revision")

var revisionForm = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SaveConfigRevision stores a file's bytes under their revision with the settings read from them, and says
// whether it was new. The same bytes stored again change nothing.
func (s *Store) SaveConfigRevision(ctx context.Context, workspaceID, revision string, body []byte, settings json.RawMessage) (bool, error) {
	if !validUUID(workspaceID) {
		return false, ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO config_revisions (workspace_id, revision, body, settings)
		VALUES ($1, $2, $3, $4) ON CONFLICT (workspace_id, revision) DO NOTHING`, workspaceID, revision, body, []byte(settings))
	return err == nil && tag.RowsAffected() == 1, err
}

// ConfigRevision is one stored revision, or ErrNoRevision.
func (s *Store) ConfigRevision(ctx context.Context, workspaceID, revision string) (ConfigRevision, error) {
	if !validUUID(workspaceID) || !revisionForm.MatchString(revision) {
		return ConfigRevision{}, ErrNoRevision
	}
	out := ConfigRevision{Revision: revision}
	var settings []byte
	err := s.pool.QueryRow(ctx, `SELECT body, settings, stored_at FROM config_revisions
		WHERE workspace_id = $1 AND revision = $2`, workspaceID, revision).Scan(&out.Body, &settings, &out.StoredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfigRevision{}, ErrNoRevision
	}
	out.Settings = settings
	return out, err
}

// ActiveConfig is the workspace's active file: its latest activation and that revision. found is false for a
// workspace that has activated none, which runs on the defaults.
func (s *Store) ActiveConfig(ctx context.Context, workspaceID string) (file ConfigRevision, active ConfigActivation, found bool, err error) {
	history, err := s.ConfigHistory(ctx, workspaceID, 1)
	if err != nil || len(history) == 0 {
		return ConfigRevision{}, ConfigActivation{}, false, err
	}
	file, err = s.ConfigRevision(ctx, workspaceID, history[0].Revision)
	return file, history[0], err == nil, err
}

// ConfigHistory lists a workspace's activations, newest first, at most limit of them (0 for all).
func (s *Store) ConfigHistory(ctx context.Context, workspaceID string, limit int) ([]ConfigActivation, error) {
	if !validUUID(workspaceID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT revision, receipt_id::text, activated_at FROM config_activations
		WHERE workspace_id = $1 ORDER BY activated_at DESC LIMIT NULLIF($2, 0)`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ConfigActivation, error) {
		var a ConfigActivation
		return a, row.Scan(&a.Revision, &a.ReceiptID, &a.ActivatedAt)
	})
}

// startActivation commits the pending activate_config receipt of one activation, naming the revision alone.
func (s *Store) startActivation(ctx context.Context, workspaceID, revision string) (Receipt, error) {
	if !revisionForm.MatchString(revision) {
		revision = "<invalid>"
	}
	arguments, err := json.Marshal(map[string]string{"revision": revision})
	if err != nil {
		return Receipt{}, err
	}
	run := newDecisionRun()
	id := "config-" + strings.TrimPrefix(run.id, "match-")
	return s.StartReceipt(ctx, ReceiptStart{WorkspaceID: workspaceID, OperationID: id + "/1", TokenID: run.token,
		Tool: "activate_config", Arguments: arguments, RequestID: json.RawMessage("1"), BriefID: id, Lease: time.Minute})
}

// RefuseActivation leaves the receipt of an activation that was refused before the file could be activated:
// an activate_config receipt naming the revision, finished as an error with the status and code. It changes
// nothing else.
func (s *Store) RefuseActivation(ctx context.Context, workspaceID, revision string, status int, code string) error {
	if !validUUID(workspaceID) {
		return ErrNotFound
	}
	receipt, err := s.startActivation(ctx, workspaceID, revision)
	if err != nil {
		return err
	}
	return s.failDecision(ctx, receipt, status, code)
}

// ActivateConfig makes a stored revision the workspace's active file, for the owner, under an activate_config
// receipt that names the revision and finishes ok in the transaction that activates it. Activating the file
// that is active already changes nothing and gives that activation, with changed false and no receipt. A
// revision the workspace has not stored gives ErrNoRevision under a receipt finished as error/404. Of two
// activations of one revision at once, one activates it and the other is given that activation with changed
// false, under a receipt finished as error/409.
func (s *Store) ActivateConfig(ctx context.Context, workspaceID, revision string) (a ConfigActivation, changed bool, err error) {
	if !validUUID(workspaceID) {
		return ConfigActivation{}, false, ErrNotFound
	}
	if history, err := s.ConfigHistory(ctx, workspaceID, 1); err != nil {
		return ConfigActivation{}, false, err
	} else if len(history) == 1 && history[0].Revision == revision {
		return history[0], false, nil
	}
	receipt, err := s.startActivation(ctx, workspaceID, revision)
	if err != nil {
		return ConfigActivation{}, false, err
	}
	if _, err := s.ConfigRevision(ctx, workspaceID, revision); err != nil {
		status, code := 500, "request_failed"
		if errors.Is(err, ErrNoRevision) {
			status, code = 404, "revision_not_found"
		}
		return ConfigActivation{}, false, errors.Join(err, s.failDecision(ctx, receipt, status, code))
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := finishLocked(ctx, tx, ReceiptFinish{ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken,
			Status: "ok", HTTPStatus: 200}, false); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO config_activations (workspace_id, revision, receipt_id)
			VALUES ($1, $2, $3) RETURNING revision, receipt_id::text, activated_at`, workspaceID, revision, receipt.ID).
			Scan(&a.Revision, &a.ReceiptID, &a.ActivatedAt)
	})
	if sqlState(err) == "GT026" {
		// Another activation of this revision finished first: the file is active, and not by this request.
		if failed := s.failDecision(ctx, receipt, 409, "already_active"); failed != nil {
			return ConfigActivation{}, false, failed
		}
		history, err := s.ConfigHistory(ctx, workspaceID, 1)
		if err != nil || len(history) == 0 {
			return ConfigActivation{}, false, errors.Join(errors.New("the active file could not be read"), err)
		}
		return history[0], false, nil
	}
	if err != nil {
		return ConfigActivation{}, false, errors.Join(err, s.failDecision(ctx, receipt, 500, "request_failed"))
	}
	return a, true, nil
}
