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

// backupTables are copied in this order, so every foreign key points at a
// table, or (for receipts) a row, that is already restored. Sessions are left
// out: a restore signs everyone out.
var backupTables = []struct{ name, order string }{
	{"workspaces", "id"},
	{"principals", "id"},
	{"accounts", "workspace_id, ref"},
	{"account_grants", "workspace_id, principal_id, account_ref"},
	{"machine_tokens", "id"},
	{"connections", "id"},
	{"source_records", "id"},
	// A parent receipt always starts before its children.
	{"receipts", "started_at, id"},
	{"receipt_records", "receipt_id, ordinal"},
	{"receipt_deliveries", "receipt_id"},
	{"cards", "id"},
	{"evidence_units", "card_id, n"},
	{"gaps", "card_id, ordinal"},
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
	var copies []TableCopy
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			for _, table := range backupTables {
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
	return version, copies, err
}

// Restore loads a dump into an empty database. In one transaction it creates
// the schema at the dump's version and copies every table with the tables'
// own triggers off (the rows were checked when first written), checking the
// row counts. Then it migrates to the latest version.
func (s *Store) Restore(ctx context.Context, schemaVersion int, copies []TableCopy,
	open func(table string) (io.ReadCloser, error)) error {
	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current != 0 {
		return errors.New("restore needs an empty database; this one is already set up")
	}
	if schemaVersion < 1 || schemaVersion > LatestSchemaVersion() {
		return fmt.Errorf("the backup is at schema %d; this binary knows 1 to %d", schemaVersion, LatestSchemaVersion())
	}
	if len(copies) != len(backupTables) {
		return errors.New("the backup does not list the expected tables")
	}
	for i, table := range backupTables {
		if copies[i].Name != table.name {
			return fmt.Errorf("the backup lists %q where %q belongs", copies[i].Name, table.name)
		}
	}
	list, err := migrations()
	if err != nil {
		return err
	}
	// One transaction for the schema and the rows: a failed restore leaves
	// the database empty, so it can be run again.
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, createMigrationsTable); err != nil {
			return err
		}
		var applied int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
			return err
		}
		if applied != 0 {
			return errors.New("restore needs an empty database; this one is already set up")
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
		return nil
	})
	if err != nil {
		return err
	}
	return s.Migrate(ctx)
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
