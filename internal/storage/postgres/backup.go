package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// notBackedUp are the tables a backup leaves out, each with the reason. Every
// other table a migration makes must be in backupTables.
var notBackedUp = map[string]string{
	"sessions":             "a restore signs everyone out",
	"consumed_presessions": "a restore signs everyone out",
	"restores":             "restore records itself into the database it fills",
	"schema_migrations":    "restore writes it as it applies each migration",
}

// backupTables are copied in this order, so every foreign key points at a
// table, or (for receipts) a row, that is already restored. since names the
// migration that made the table (its file name without the prefix), so a
// backup of a database without that migration does not list it.
var backupTables = []struct {
	name, order, since string
}{
	{"workspaces", "id", "identity_and_evidence"},
	{"principals", "id", "identity_and_evidence"},
	{"accounts", "workspace_id, ref", "identity_and_evidence"},
	{"account_grants", "workspace_id, principal_id, account_ref", "identity_and_evidence"},
	{"machine_tokens", "id", "identity_and_evidence"},
	{"connections", "id", "identity_and_evidence"},
	{"source_records", "id", "identity_and_evidence"},
	// A parent receipt always starts before its children.
	{"receipts", "started_at, id", "identity_and_evidence"},
	{"receipt_records", "receipt_id, ordinal", "identity_and_evidence"},
	{"receipt_deliveries", "receipt_id", "identity_and_evidence"},
	{"cards", "id", "identity_and_evidence"},
	{"evidence_units", "card_id, n", "identity_and_evidence"},
	{"gaps", "card_id, ordinal", "identity_and_evidence"},
	{"card_reads", "card_id, ordinal", "cli_reads_and_briefs"},
	// An import's bytes come before the revisions made from them, and a revision before the account that reads it.
	{"call_import_stores", "workspace_id", "call_imports"},
	{"call_import_contents", "workspace_id, content_sha256", "call_imports"},
	{"call_import_revisions", "workspace_id, import_id", "call_imports"},
	{"call_import_numbers", "workspace_id, account_number", "call_imports"},
	{"call_import_active", "workspace_id, account_ref", "call_imports"},
	{"card_lines", "card_id, ordinal", "card_lines"},
	{"cards_before_lines", "card_id", "card_lines"},
	{"slack_mappings", "workspace_id, account_ref", "slack_mappings"},
	// A card's seal is restored before the card.
	{"admitted_receipts", "workspace_id, audit_id", "admitted_python_cards"},
	{"admitted_cards", "workspace_id, brief_id", "admitted_python_cards"},
	{"deals", "workspace_id, ref", "deals"},
	{"deal_cards", "card_id", "deals"},
	// A decision is restored after the one it supersedes.
	{"deal_matches", "decided_at, id", "deal_matches"},
	{"deal_card_matches", "card_id, match_id", "deal_matches"},
	// A file's revision is restored before the activation that names it.
	{"config_revisions", "workspace_id, revision", "config_revisions"},
	{"config_activations", "activated_at, id", "config_revisions"},
}

// TableCopy describes one table in a backup.
type TableCopy struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
}

// Dump writes every table, in PostgreSQL's COPY text format, from one
// read-only snapshot. open gives the writer for each table. The database's
// migrations must be exactly this binary's. It returns the names of the
// applied migrations and what it wrote.
func (s *Store) Dump(ctx context.Context, open func(table string) (io.WriteCloser, error)) ([]string, []TableCopy, error) {
	state, err := s.MigrationState(ctx)
	if err != nil {
		return nil, nil, err
	}
	if state != MigrationsCurrent {
		return nil, nil, state.Err()
	}
	list, err := s.migrations()
	if err != nil {
		return nil, nil, err
	}
	var applied []string
	for _, m := range list {
		applied = append(applied, m.name)
	}
	copies, err := s.dump(ctx, applied, open)
	return applied, copies, err
}

// dump copies the tables of a database with these migrations applied.
func (s *Store) dump(ctx context.Context, applied []string, open func(table string) (io.WriteCloser, error)) ([]TableCopy, error) {
	var copies []TableCopy
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			for _, table := range backupTables {
				if !slices.Contains(applied, migrationFile(table.since)) {
					continue
				}
				columns, err := tableColumns(ctx, tx, table.name)
				if err != nil {
					return err
				}
				w, err := open(table.name)
				if err != nil {
					return err
				}
				tag, err := tx.Conn().PgConn().CopyTo(ctx, w, fmt.Sprintf(
					"COPY (SELECT %s FROM %s ORDER BY %s) TO STDOUT",
					quoteColumns(columns), pgx.Identifier{table.name}.Sanitize(), table.order))
				if closeErr := w.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					return fmt.Errorf("%s: %w", table.name, err)
				}
				copies = append(copies, TableCopy{table.name, columns, tag.RowsAffected()})
			}
			return nil
		})
	return copies, err
}

// Restore loads a dump into an empty database. applied names the migrations
// the dumped database held. In one transaction it applies those, copies
// every table with the tables' own triggers off, checks the row counts,
// checks the loaded rows against every rule the triggers enforce
// (gtm_integrity_violations), applies the rest of this binary's migrations
// and checks again, and records the restore as waiting for its credentials.
// FinishRestore records that the credentials are in place.
func (s *Store) Restore(ctx context.Context, archiveSHA256 string, applied []string, copies []TableCopy,
	open func(table string) (io.ReadCloser, error)) error {
	list, err := s.migrations()
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, name := range applied {
		if !slices.ContainsFunc(list, func(m migration) bool { return m.name == name }) {
			return fmt.Errorf("the backup holds migration %s, which this binary does not; "+
				"restore it with a binary that holds it", name)
		}
		if held[name] {
			return fmt.Errorf("the backup lists migration %s twice", name)
		}
		held[name] = true
	}
	if len(held) == 0 {
		return errors.New("the backup lists no migration")
	}
	tables := BackupTableNames(applied)
	if len(copies) != len(tables) {
		return errors.New("the backup does not list the expected tables")
	}
	for i, table := range tables {
		if copies[i].Name != table {
			return fmt.Errorf("the backup lists %q where %q belongs", copies[i].Name, table)
		}
	}
	// One transaction for the schema and the rows: a failed restore leaves
	// the database empty, so it can be run again.
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := requireUTF8(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
			return err
		}
		// Empty means no table, view, sequence or function in the schema at
		// all, and no reporting schema, checked under the lock that
		// migrations take.
		var objects int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			         WHERE n.nspname = current_schema())
			     + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			         WHERE n.nspname = current_schema())
			     + (SELECT count(*) FROM pg_namespace WHERE nspname = 'reporting')`).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return errors.New("restore needs an empty database; this one already holds tables or functions")
		}
		if _, err := tx.Exec(ctx, createMigrationsTable); err != nil {
			return err
		}
		for _, m := range list {
			if !held[m.name] {
				continue
			}
			if err := apply(ctx, tx, m); err != nil {
				return err
			}
		}
		// Two migrations that each add a column to one table, applied in
		// either order, leave the columns in either order; COPY names them,
		// so they are compared as a set.
		for _, c := range copies {
			columns, err := tableColumns(ctx, tx, c.Name)
			if err != nil {
				return err
			}
			slices.Sort(columns)
			if !slices.Equal(columns, slices.Sorted(slices.Values(c.Columns))) {
				return fmt.Errorf("%s: the backup's columns differ from its migrations' schema", c.Name)
			}
			if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{c.Name}.Sanitize()+" DISABLE TRIGGER USER"); err != nil {
				return err
			}
		}
		for _, c := range copies {
			r, err := open(c.Name)
			if err != nil {
				return err
			}
			tag, err := tx.Conn().PgConn().CopyFrom(ctx, r, fmt.Sprintf("COPY %s (%s) FROM STDIN",
				pgx.Identifier{c.Name}.Sanitize(), quoteColumns(c.Columns)))
			if closeErr := r.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("%s: %w", c.Name, err)
			}
			if tag.RowsAffected() != c.Rows {
				return fmt.Errorf("%s: restored %d rows, the backup lists %d", c.Name, tag.RowsAffected(), c.Rows)
			}
		}
		for _, c := range copies {
			if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{c.Name}.Sanitize()+" ENABLE TRIGGER USER"); err != nil {
				return err
			}
		}
		// The rows went in with the triggers off: check every rule the
		// triggers enforce on writing, at the backup's migrations; then apply
		// the rest, which refuse rows their rules do not admit, and check
		// again with every migration applied, before anything commits.
		if err := integrity(ctx, tx); err != nil {
			return err
		}
		for _, m := range list {
			if held[m.name] {
				continue
			}
			if err := apply(ctx, tx, m); err != nil {
				return err
			}
		}
		if err := integrity(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO restores (archive_sha256) VALUES ($1)", archiveSHA256)
		return err
	})
}

// integrity refuses rows that break a rule the triggers enforce on writing.
func integrity(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, "SELECT v FROM gtm_integrity_violations() v LIMIT 5")
	if err != nil {
		return err
	}
	violations, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("the backup holds rows the connector would refuse: %s", strings.Join(violations, "; "))
	}
	return nil
}

func tableColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT attname FROM pg_attribute
		 WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped
		 ORDER BY attnum`, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func quoteColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	return strings.Join(quoted, ", ")
}

// PendingRestore gives the archive digest of a restore whose credentials
// were not installed yet, or "".
func (s *Store) PendingRestore(ctx context.Context) (string, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT to_regclass('restores') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return "", err
	}
	var archive string
	err := s.pool.QueryRow(ctx, `SELECT archive_sha256 FROM restores WHERE credentials_installed_at IS NULL
		ORDER BY restored_at DESC LIMIT 1`).Scan(&archive)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return archive, err
}

// FinishRestore records that a restore's credentials are installed.
func (s *Store) FinishRestore(ctx context.Context, archiveSHA256 string) error {
	_, err := s.pool.Exec(ctx, `UPDATE restores SET credentials_installed_at = now()
		WHERE archive_sha256 = $1 AND credentials_installed_at IS NULL`, archiveSHA256)
	return err
}

// BackupTableNames lists the tables a backup of a database with these
// migrations applied holds, in restore order.
func BackupTableNames(applied []string) []string {
	var names []string
	for _, table := range backupTables {
		if slices.Contains(applied, migrationFile(table.since)) {
			names = append(names, table.name)
		}
	}
	return names
}
