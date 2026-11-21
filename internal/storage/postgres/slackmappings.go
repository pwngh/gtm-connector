package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The Slack threads selected for a workspace's accounts, as slack_mapping.py keeps them in a state folder's
// slack-mappings.json: one thread per account and one account per thread, at most MaxSlackMappings, each
// made under the CRM and Slack bindings the workspace has.

// MaxSlackMappings is how many accounts of one workspace may have a thread.
const MaxSlackMappings = 100

// SlackMapping is one selection: the canonical JSON of the mapping and the members it is found by.
type SlackMapping struct {
	AccountRef, ChannelID, ThreadTS string
	// Mapping is the canonical JSON of the whole selection. Its crm_binding and slack_binding members are
	// the bindings it was made under.
	Mapping string
	// Generations are the connections the selection was made through, each with the generation it was
	// read under: the workspace's CRM and its Slack. A connection that has moved since refuses the save
	// with ErrSourceChanged.
	Generations map[string]int64
}

var (
	// ErrSlackMappingConflict refuses a selection whose account has another thread, without replace, or
	// whose thread belongs to another account, without reassign, or one past MaxSlackMappings.
	ErrSlackMappingConflict = errors.New("the selection conflicts with a saved Slack mapping")
	// ErrSlackMappingSource refuses a selection made under other bindings than the saved ones.
	ErrSlackMappingSource = errors.New("the saved Slack mappings were made under other sources")
)

// SlackMappingOf is the mapping saved for an account, or "" when the account has no thread.
func (s *Store) SlackMappingOf(ctx context.Context, workspaceID, accountRef string) (string, error) {
	if !validUUID(workspaceID) {
		return "", ErrNotFound
	}
	var mapping *string
	err := s.pool.QueryRow(ctx, `SELECT mapping FROM slack_mappings WHERE workspace_id = $1 AND account_ref = $2`,
		workspaceID, accountRef).Scan(&mapping)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && mapping == nil {
		return "", nil
	}
	return deref(mapping), err
}

// SaveSlackMapping saves a selection under the workspace's lock, as save_slack_mapping does. The account must
// be listed in the workspace. A selection the account already has is left as it is and returned with
// changed false. Otherwise an account with another thread needs replace, and a thread of another account
// needs reassign, which leaves that account with none. Every saved mapping must have been made under the
// same CRM and Slack bindings as this one. It returns the mapping saved for the account.
func (s *Store) SaveSlackMapping(ctx context.Context, workspaceID string, m SlackMapping, replace, reassign bool) (
	saved string, changed bool, err error) {
	if !validUUID(workspaceID) {
		return "", false, ErrNotFound
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var locked bool
		err := tx.QueryRow(ctx, `SELECT true FROM workspaces WHERE id = $1 FOR UPDATE`, workspaceID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// connect changes a connection under the same workspace lock, so none moves until this commits.
		for id, generation := range m.Generations {
			var now int64
			err := tx.QueryRow(ctx, `SELECT generation FROM connections WHERE workspace_id = $1 AND id = $2`,
				workspaceID, id).Scan(&now)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && now != generation {
				return ErrSourceChanged
			}
			if err != nil {
				return err
			}
		}
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM slack_mappings
			WHERE workspace_id = $1 AND mapping IS NOT NULL
			  AND (mapping::jsonb -> 'crm_binding' IS DISTINCT FROM $2::jsonb -> 'crm_binding'
			    OR mapping::jsonb -> 'slack_binding' IS DISTINCT FROM $2::jsonb -> 'slack_binding')`,
			workspaceID, m.Mapping).Scan(&others); err != nil {
			return err
		}
		if others > 0 {
			return ErrSlackMappingSource
		}
		var account, thread *string
		var accountHolds string
		err = tx.QueryRow(ctx, `SELECT mapping FROM slack_mappings WHERE workspace_id = $1 AND account_ref = $2`,
			workspaceID, m.AccountRef).Scan(&account)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT account_ref FROM slack_mappings
			WHERE workspace_id = $1 AND channel_id = $2 AND thread_ts = $3`,
			workspaceID, m.ChannelID, m.ThreadTS).Scan(&accountHolds)
		switch {
		case err == nil:
			thread = &accountHolds
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		switch {
		case account != nil && thread != nil && *thread == m.AccountRef:
			saved = *account
			return nil
		case account != nil && !replace, thread != nil && !reassign:
			return ErrSlackMappingConflict
		}
		if thread != nil {
			if _, err := tx.Exec(ctx, `UPDATE slack_mappings SET channel_id = NULL, thread_ts = NULL, mapping = NULL,
				updated_at = now() WHERE workspace_id = $1 AND account_ref = $2`, workspaceID, *thread); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO slack_mappings (workspace_id, account_ref, channel_id, thread_ts, mapping)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, account_ref) DO UPDATE
			   SET channel_id = excluded.channel_id, thread_ts = excluded.thread_ts, mapping = excluded.mapping,
			       updated_at = now()`,
			workspaceID, m.AccountRef, m.ChannelID, m.ThreadTS, m.Mapping); err != nil {
			return err
		}
		var mapped int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM slack_mappings WHERE workspace_id = $1 AND mapping IS NOT NULL`,
			workspaceID).Scan(&mapped); err != nil {
			return err
		}
		if mapped > MaxSlackMappings {
			return ErrSlackMappingConflict
		}
		saved, changed = m.Mapping, true
		return nil
	})
	return saved, changed, err
}
