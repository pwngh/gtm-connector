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
// table, or (for receipts) a row, that is already restored. since is the
// schema version that made the table, so a backup of an older version lists
// only its own tables.
var backupTables = []struct {
	name, order string
	since       int
}{
	{"workspaces", "id", 1},
	{"principals", "id", 1},
	{"accounts", "workspace_id, ref", 1},
	{"account_grants", "workspace_id, principal_id, account_ref", 1},
	{"machine_tokens", "id", 1},
	{"connections", "id", 1},
	{"source_records", "id", 1},
	// A parent receipt always starts before its children.
	{"receipts", "started_at, id", 1},
	{"receipt_records", "receipt_id, ordinal", 1},
	{"receipt_deliveries", "receipt_id", 1},
	{"cards", "id", 1},
	{"evidence_units", "card_id, n", 1},
	{"gaps", "card_id, ordinal", 1},
	{"card_reads", "card_id, ordinal", 2},
	// An import's bytes come before the revisions made from them, and a revision before the account that reads it.
	{"call_import_stores", "workspace_id", 4},
	{"call_import_contents", "workspace_id, content_sha256", 4},
	{"call_import_revisions", "workspace_id, import_id", 4},
	{"call_import_numbers", "workspace_id, account_number", 4},
	{"call_import_active", "workspace_id, account_ref", 4},
	{"card_lines", "card_id, ordinal", migrationVersion("card_lines")},
	{"cards_before_lines", "card_id", migrationVersion("card_lines")},
	{"slack_mappings", "workspace_id, account_ref", migrationVersion("slack_mappings")},
	// A card's seal is restored before the card.
	{"admitted_receipts", "workspace_id, audit_id", migrationVersion("admitted_python_cards")},
	{"admitted_cards", "workspace_id, brief_id", migrationVersion("admitted_python_cards")},
	{"deals", "workspace_id, ref", migrationVersion("deals")},
	{"deal_cards", "card_id", migrationVersion("deals")},
}

// TableCopy describes one table in a backup.
type TableCopy struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
}

// Dump writes every table, in PostgreSQL's COPY text format, from one
// read-only snapshot. open gives the writer for each table. It returns the
// schema version and what it wrote.
func (s *Store) Dump(ctx context.Context, open func(table string) (io.WriteCloser, error)) (int, []TableCopy, error) {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return 0, nil, err
	}
	if version != LatestSchemaVersion() {
		return 0, nil, fmt.Errorf("the database is at schema %d; run serve or setup to migrate it to %d first",
			version, LatestSchemaVersion())
	}
	copies, err := s.dump(ctx, version, open)
	return version, copies, err
}

// dump copies the tables of this schema version.
func (s *Store) dump(ctx context.Context, version int, open func(table string) (io.WriteCloser, error)) ([]TableCopy, error) {
	var copies []TableCopy
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			for _, table := range backupTables {
				if table.since > version {
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

// Restore loads a dump into an empty database. In one transaction it creates
// the schema at the dump's version, copies every table with the tables' own
// triggers off, checks the row counts, checks the loaded rows against every
// rule the triggers enforce (gtm_integrity_violations), applies the later
// migrations and checks again at the latest version, and records the restore
// as waiting for its credentials. FinishRestore records that the credentials
// are in place.
func (s *Store) Restore(ctx context.Context, archiveSHA256 string, schemaVersion int, copies []TableCopy,
	open func(table string) (io.ReadCloser, error)) error {
	if schemaVersion < 1 || schemaVersion > LatestSchemaVersion() {
		return fmt.Errorf("the backup is at schema %d; this binary knows 1 to %d", schemaVersion, LatestSchemaVersion())
	}
	tables := BackupTableNames(schemaVersion)
	if len(copies) != len(tables) {
		return errors.New("the backup does not list the expected tables")
	}
	for i, table := range tables {
		if copies[i].Name != table {
			return fmt.Errorf("the backup lists %q where %q belongs", copies[i].Name, table)
		}
	}
	list, err := migrations()
	if err != nil {
		return err
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
		for _, m := range list[:schemaVersion] {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)",
				m.version, m.name, m.sha256); err != nil {
				return err
			}
		}
		for _, c := range copies {
			columns, err := tableColumns(ctx, tx, c.Name)
			if err != nil {
				return err
			}
			if !slices.Equal(columns, c.Columns) {
				return fmt.Errorf("%s: the backup's columns differ from schema %d", c.Name, schemaVersion)
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
		// triggers enforce on writing, at the backup's version; then apply
		// the later migrations, which refuse rows their rules do not admit,
		// and check again at the latest version, before anything commits.
		if err := integrity(ctx, tx); err != nil {
			return err
		}
		for _, m := range list[schemaVersion:] {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)",
				m.version, m.name, m.sha256); err != nil {
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

// BackupTableNames lists the tables a backup at this schema version holds,
// in restore order.
func BackupTableNames(version int) []string {
	var names []string
	for _, table := range backupTables {
		if table.since <= version {
			names = append(names, table.name)
		}
	}
	return names
}
