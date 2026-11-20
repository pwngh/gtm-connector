package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// The card rules hold for any writer: each case writes a card with SQL, past
// PublishCard's own checks, changing one thing from a card that commits.

// sqlCard finishes the seal naming the bytes, then writes the card, its
// units, gaps and read set in one transaction, and returns what the commit
// gave. The card counts the reads its bytes embed.
func sqlCard(t *testing.T, f *postgrestest.Fixture, pub postgres.CardPublication) error {
	t.Helper()
	payload, err := receipts.ParsePayload(pub.Bytes)
	ok(t, err)
	_, err = f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: pub.SealReceiptID,
		LeaseToken: pub.SealLeaseToken, Status: "ok", HTTPStatus: 200,
		Rows: []string{"briefs/" + pub.BriefID + "/" + sha(pub.Bytes)}})
	ok(t, err)
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var card string
		if err := tx.QueryRow(ctx, `
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version, card_bytes,
				card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count)
			VALUES ($1, $2, $3, $4, 3, '3', $5, sha256($5), $6, $7, $8, $9, $10) RETURNING id`,
			pub.WorkspaceID, pub.AccountRef, pub.BriefID, pub.Kind, pub.Bytes, pub.ParentReceiptID,
			pub.SealReceiptID, len(pub.Units), len(pub.Gaps), len(payload.ReadIDs)).Scan(&card); err != nil {
			return err
		}
		for _, u := range pub.Units {
			if _, err := tx.Exec(ctx, `INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool,
				shown, receipt_id, source_record_id, body) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10)`,
				pub.WorkspaceID, card, u.N, u.Kind, u.RecordRef, u.Tool, u.Shown, u.ReceiptID, u.SourceRecordID,
				u.Body); err != nil {
				return err
			}
		}
		for i, g := range pub.Gaps {
			if _, err := tx.Exec(ctx, `INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, count,
				text) VALUES ($1, $2, $3, $4, $5, $6, $7)`, pub.WorkspaceID, card, i, g.Code, g.Disposition, g.Count,
				g.Text); err != nil {
				return err
			}
		}
		for i, id := range pub.ReadReceiptIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO card_reads (workspace_id, card_id, ordinal, receipt_id)
				VALUES ($1, $2, $3, $4)`, pub.WorkspaceID, card, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// prepared is a prepared brief resting on one read of acc-003, under the
// fixture's reader and token.
func prepared(t *testing.T, f *postgrestest.Fixture, brief string) (postgres.CardPublication, postgres.Receipt, postgres.Receipt) {
	t.Helper()
	return postgrestest.Prepared(t, f.Store, f, brief+"-call", brief, f.Reader.ID, postgres.TokenID("raw-token"),
		postgrestest.Record("accounts", "acc-003", "id", "acc-003", "v", brief))
}

func reembed(t *testing.T, f *postgrestest.Fixture, pub *postgres.CardPublication, seal postgres.Receipt,
	reads ...postgres.Receipt) {
	t.Helper()
	embed(t, f, pub, seal, []postgres.RecordVersion{postgrestest.Record("accounts", "acc-003", "id", "acc-003",
		"v", pub.BriefID)}, reads...)
}

func TestTheTriggersHoldTheCardRules(t *testing.T) {
	f := postgrestest.New(t)
	t.Run("a card that commits", func(t *testing.T) {
		pub, _, _ := prepared(t, f, "sql-ok")
		ok(t, sqlCard(t, f, pub))
		pub, _ = cliCard(t, f, "sql-ok-cli")
		ok(t, sqlCard(t, f, pub))
	})
	for i, c := range []struct {
		name string
		edit func(t *testing.T, pub *postgres.CardPublication, seal, read postgres.Receipt)
		code string
	}{
		{"a brief receipt other than the seal", func(t *testing.T, pub *postgres.CardPublication, _, _ postgres.Receipt) {
			pub.ParentReceiptID = f.Parent.ID
		}, "23514"},
		{"a child of another call made for this brief", func(t *testing.T, pub *postgres.CardPublication, seal, _ postgres.Receipt) {
			_, other, _ := prepared(t, f, pub.BriefID+"-elsewhere")
			start := childStart(f, other, 2, time.Minute)
			start.BriefID = pub.BriefID
			r, _, err := f.Store.RunChildRead(ctx, start, func(context.Context) ([]postgres.RecordVersion, error) {
				return []postgres.RecordVersion{postgrestest.Record("accounts", "acc-003", "id", "acc-003", "v",
					pub.BriefID)}, nil
			})
			ok(t, err)
			ids, err := f.Store.ReceiptRecordIDs(ctx, f.Workspace.ID, r.ID)
			ok(t, err)
			pub.Units[0].ReceiptID, pub.Units[0].SourceRecordID = r.ID, ids["crm/accounts/acc-003"]
			reembed(t, f, pub, seal, r)
		}, "GT009"},
		{"a read the bytes embed and the read set leaves out", func(t *testing.T, pub *postgres.CardPublication, seal, read postgres.Receipt) {
			denied := child(t, f, seal, 2, time.Minute)
			_, err := f.Store.FailRead(ctx, denied.ID, denied.LeaseToken, 403, "request_failed", 1)
			ok(t, err)
			reembed(t, f, pub, seal, read, denied)
			pub.ReadReceiptIDs = pub.ReadReceiptIDs[:1]
		}, "GT009"},
		{"a card with no read set", func(t *testing.T, pub *postgres.CardPublication, _, _ postgres.Receipt) {
			pub.ReadReceiptIDs = nil
		}, "GT009"},
		{"an uncited read the source changed under", func(t *testing.T, pub *postgres.CardPublication, seal, read postgres.Receipt) {
			denied := child(t, f, seal, 2, time.Minute)
			_, err := f.Store.FailRead(ctx, denied.ID, denied.LeaseToken, 403, "request_failed", 1)
			ok(t, err)
			reembed(t, f, pub, seal, read, denied)
			_, err = f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
			ok(t, err)
		}, "GT009"},
		{"a read embedded with another start time", func(t *testing.T, pub *postgres.CardPublication, _, read postgres.Receipt) {
			pub.Bytes = []byte(strings.Replace(string(pub.Bytes),
				`"started_at": "`+read.StartedAt.UTC().Format("2006-01-02"),
				`"started_at": "2001-01-01`, 1))
		}, "GT009"},
		{"a unit under another tool than its read's", func(t *testing.T, pub *postgres.CardPublication, seal, read postgres.Receipt) {
			pub.Units[0].Tool = "list_opportunities"
			pub.Units[0].Body = postgrestest.UnitBody(1, "record", "crm/accounts/acc-003", "list_opportunities", "", nil)
			reembed(t, f, pub, seal, read)
		}, "GT009"},
		{"a record the bytes show other than its read bound", func(t *testing.T, pub *postgres.CardPublication, seal, read postgres.Receipt) {
			stored, err := f.Store.Receipt(ctx, f.Workspace.ID, read.ID)
			ok(t, err)
			pub.Bytes = postgrestest.CardBytes(t, seal, pub.AccountRef, pub.BriefID, []postgres.Receipt{stored},
				[]postgres.RecordVersion{postgrestest.Record("accounts", "acc-003", "id", "acc-003", "v", "other")},
				pub.Units, pub.Gaps)
		}, "GT009"},
		{"a card of kind cli", func(t *testing.T, pub *postgres.CardPublication, _, _ postgres.Receipt) {
			pub.Kind = "cli"
		}, "GT009"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pub, seal, read := prepared(t, f, fmt.Sprintf("sql-%d", i))
			c.edit(t, &pub, seal, read)
			if got := code(sqlCard(t, f, pub)); got != c.code {
				t.Fatalf("committed with %q, want %s", got, c.code)
			}
		})
	}
	// A seal of another tool, or a save_brief seal naming another brief, seals nothing.
	for name, seal := range map[string]postgres.ReceiptStart{
		"another tool": {OperationID: "sql-tool-seal", Tool: "save_brief_later",
			Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"sql-seal-a"}`)},
		"another brief": {OperationID: "sql-brief-seal", Tool: "save_brief",
			Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"brief-another"}`)},
	} {
		t.Run("a seal of "+name, func(t *testing.T) {
			pub, _ := cliCard(t, f, "sql-seal-"+map[string]string{"another tool": "a", "another brief": "b"}[name])
			seal.WorkspaceID, seal.PrincipalID, seal.TokenID = f.Workspace.ID, f.Reader.ID, postgres.TokenID("cli bearer")
			seal.AccountRef, seal.Lease = postgrestest.Account, time.Minute
			s, err := f.Store.StartReceipt(ctx, seal)
			ok(t, err)
			read, err := f.Store.Receipt(ctx, f.Workspace.ID, pub.ReadReceiptIDs[0])
			ok(t, err)
			pub.SealReceiptID, pub.SealLeaseToken, pub.ParentReceiptID = s.ID, s.LeaseToken, s.ID
			reembed(t, f, &pub, s, read)
			if got := code(sqlCard(t, f, pub)); got != "GT009" {
				t.Fatalf("committed with %q", got)
			}
		})
	}
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("the cards that committed break a rule: %v", violations)
	}
}

func integrityViolations(t *testing.T, f *postgrestest.Fixture) []string {
	t.Helper()
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, "SELECT v FROM gtm_integrity_violations() v")
	ok(t, err)
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	ok(t, err)
	return found
}

// withTriggersOff writes as a restore does, with the triggers off, inside a
// transaction it rolls back, and gives the violations the integrity check
// then reports.
func withTriggersOff(t *testing.T, f *postgrestest.Fixture, statement string, args ...any) []string {
	t.Helper()
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	ok(t, err)
	defer tx.Rollback(ctx)
	for _, s := range []string{"ALTER TABLE receipts DISABLE TRIGGER USER", statement,
		"ALTER TABLE receipts ENABLE TRIGGER USER"} {
		var params []any
		if s == statement {
			params = args
		}
		_, err := tx.Exec(ctx, s, params...)
		ok(t, err)
	}
	rows, err := tx.Query(ctx, "SELECT v FROM gtm_integrity_violations() v")
	ok(t, err)
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	ok(t, err)
	return found
}

// A child read carries its parent's principal and token, for any writer.
func TestChildReadsTakeTheirParentsIdentity(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "identity", time.Minute)
	for name, values := range map[string][]any{
		"another principal": {f.Outsider.ID, p.TokenID},
		"another token":     {p.PrincipalID, postgres.TokenID("outsider bearer")},
	} {
		statement := `INSERT INTO receipts (workspace_id, operation_id, parent_id, ordinal, principal_id, token_id, tool,
			scope, connection_id, connection_generation, arguments, request_id, lease_token, lease_expires_at)
			VALUES ($1, $2, $3, 1, $4, $5, 'get_account', 'crm:read', $6, 1, '{}', 'null', gen_random_uuid(),
			now() + interval '1 minute')`
		args := append([]any{f.Workspace.ID, "identity#" + name, p.ID}, append(values, f.Connection.ID)...)
		if got := code(sql(t, f, statement, args...)); got != "GT017" {
			t.Errorf("a child with %s: %q", name, got)
		}
		if violations := withTriggersOff(t, f, statement, args...); len(violations) != 1 ||
			!strings.Contains(violations[0], "is not a numbered source read") {
			t.Errorf("a restored child with %s: %v", name, violations)
		}
	}
}

// json keeps a duplicate key that jsonb dropped; neither a Go writer nor SQL
// stores one.
func TestArgumentsWithADuplicateKeyAreRefused(t *testing.T) {
	f := postgrestest.New(t)
	for _, arguments := range []string{`{"account_id": "acc-003", "account_id": "acc-004"}`,
		`{"nested": [{"a": 1, "a": 2}]}`, `{"a": 1} {"b": 2}`} {
		_, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
			OperationID: "dup-" + sha([]byte(arguments))[:8], TokenID: "anonymous", Tool: "get_account",
			Arguments: json.RawMessage(arguments), Lease: time.Minute})
		is(t, err, postgres.ErrInvalidArguments)
		start := childStart(f, parent(t, f, "dup-parent-"+sha([]byte(arguments))[:8], time.Minute), 1, time.Minute)
		start.Arguments = json.RawMessage(arguments)
		_, err = f.Store.StartChildRead(ctx, start)
		is(t, err, postgres.ErrInvalidArguments)
	}
	for _, arguments := range []string{`{"account_id": "acc-003", "account_id": "acc-004"}`,
		`{"nested": [{"a": 1, "a": 2}]}`} {
		if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, token_id, tool, arguments, request_id,
			lease_token, lease_expires_at) VALUES ($1, 'dup-sql', 'anonymous', 't', $2, 'null', gen_random_uuid(),
			now() + interval '1 minute')`, f.Workspace.ID, arguments)) != "23514" {
			t.Errorf("SQL stored %s", arguments)
		}
	}
}

// Only a source read claims a record row, for any writer and in a restore.
func TestOnlySourceReadsClaimRecordRows(t *testing.T) {
	f := postgrestest.New(t)
	call, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "rows",
		TokenID: "anonymous", Tool: "get_account", Scope: "crm:read", Arguments: json.RawMessage(`{}`),
		Lease: time.Minute})
	ok(t, err)
	_, err = f.Store.FinishReceipt(ctx, finishOK(call, "crm/accounts/acc-003"))
	is(t, err, postgres.ErrRecordRows)
	if code(sql(t, f, `UPDATE receipts SET status = 'ok', http_status = 200, rows = '["calls/call_notes/n-1"]',
		row_count = 1, finished_at = now(), finishing_lease = lease_token WHERE id = $1`, call.ID)) != "GT014" {
		t.Fatal("SQL let a call that reads no source claim a record row")
	}
	if violations := withTriggersOff(t, f, `UPDATE receipts SET status = 'ok', http_status = 200,
		rows = '["slack/messages/m-1"]', row_count = 1, finished_at = now() WHERE id = $1`, call.ID); len(violations) != 1 ||
		!strings.Contains(violations[0], "claims a record row") {
		t.Fatalf("a restored call claiming a record row: %v", violations)
	}
	_, err = f.Store.FinishReceipt(ctx, finishOK(call, "briefs/brief-1/"+strings.Repeat("0", 64)))
	ok(t, err)
}

// A read finishing ok across a source change is refused by the database too.
func TestTheDatabaseFencesAReadAcrossASourceChange(t *testing.T) {
	f := postgrestest.New(t)
	c := child(t, f, parent(t, f, "fence-sql", time.Minute), 1, time.Minute)
	_, err := f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
	ok(t, err)
	if got := code(sql(t, f, `UPDATE receipts SET status = 'ok', http_status = 200, finished_at = now(),
		finishing_lease = lease_token WHERE id = $1`, c.ID)); got != "GT013" {
		t.Fatalf("a read finished ok across a source change: %q", got)
	}
	// And a card on a read from before the change does not commit.
	pub, _, _ := prepared(t, f, "fence-sql-card")
	_, err = f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
	ok(t, err)
	if got := code(sqlCard(t, f, pub)); got != "GT009" {
		t.Fatalf("a card on a stale read committed: %q", got)
	}
}

// A publication paused after its checks holds off a revocation of its
// actor's grant and a change of its source until it commits: both wait on
// the share locks the checks took.
func TestAPausedPublicationHoldsOffARevokeAndABump(t *testing.T) {
	for name, change := range map[string]func(f *postgrestest.Fixture) error{
		"a revocation": func(f *postgrestest.Fixture) error {
			return f.Store.RevokeAccountGrant(context.Background(), f.Workspace.ID, f.Reader.ID, postgrestest.Account)
		},
		"a source change": func(f *postgrestest.Fixture) error {
			_, err := f.Store.BumpConnectionGeneration(context.Background(), f.Connection.ID)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := postgrestest.New(t)
			pub, _ := publication(t, f, "paused")
			paused, release := make(chan struct{}), make(chan struct{})
			postgres.SetCrash(f.Store, func(at string) error {
				if at == "publish:checked" {
					close(paused)
					<-release
				}
				return nil
			})
			defer postgres.SetCrash(f.Store, nil)
			published := make(chan error, 1)
			go func() {
				_, err := f.Store.PublishCard(context.Background(), pub)
				published <- err
			}()
			<-paused
			changed := make(chan error, 1)
			other, err := postgres.Open(ctx, f.URL)
			ok(t, err)
			defer other.Close()
			go func() {
				changed <- change(&postgrestest.Fixture{Store: other, Workspace: f.Workspace,
					Reader: f.Reader, Connection: f.Connection})
			}()
			select {
			case err := <-changed:
				close(release)
				t.Fatalf("%s committed while the publication was paused: %v", name, err)
			case <-time.After(500 * time.Millisecond):
			}
			close(release)
			ok(t, <-published)
			ok(t, <-changed)
			if count(t, f, "SELECT count(*) FROM cards WHERE brief_id = 'paused'") != 1 {
				t.Fatal("no card")
			}
		})
	}
}

func TestPublishCardRefusesASealItDoesNotHold(t *testing.T) {
	f := postgrestest.New(t)
	pub, _ := publication(t, f, "held")
	lost := pub
	lost.SealLeaseToken = "00000000-0000-4000-8000-000000000000"
	_, err := f.Store.PublishCard(ctx, lost)
	is(t, err, postgres.ErrLeaseLost)
	_, err = f.Store.PublishCard(ctx, pub)
	ok(t, err)
	// After it published, a retry without the lease is told nothing.
	_, err = f.Store.PublishCard(ctx, lost)
	if !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("a retry without the lease: %v", err)
	}
}

// A restore loads rows with the triggers off; its integrity check holds the
// read set to the reads the card's bytes embed.
func TestARestoreChecksTheReadSet(t *testing.T) {
	f := postgrestest.New(t)
	pub, seal, read := prepared(t, f, "restored-reads")
	start := childStart(f, seal, 2, time.Minute)
	start.Tool, start.BriefID = "read_call_notes", "restored-reads"
	denied, err := f.Store.StartChildRead(ctx, start)
	ok(t, err)
	_, err = f.Store.FailRead(ctx, denied.ID, denied.LeaseToken, 403, "request_failed", 1)
	ok(t, err)
	reembed(t, f, &pub, seal, read, denied)
	card, err := f.Store.PublishCard(ctx, pub)
	ok(t, err)
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	ok(t, err)
	defer tx.Rollback(ctx)
	for _, statement := range []string{"ALTER TABLE card_reads DISABLE TRIGGER USER",
		"DELETE FROM card_reads WHERE card_id = '" + card.ID + "' AND receipt_id = '" + denied.ID + "'",
		"ALTER TABLE card_reads ENABLE TRIGGER USER"} {
		_, err := tx.Exec(ctx, statement)
		ok(t, err)
	}
	rows, err := tx.Query(ctx, "SELECT v FROM gtm_integrity_violations() v")
	ok(t, err)
	violations, err := pgx.CollectRows(rows, pgx.RowTo[string])
	ok(t, err)
	if len(violations) != 1 || !strings.Contains(violations[0], "does not hold exactly its units, gaps and reads") {
		t.Fatalf("a card missing an uncited read from its read set: %v", violations)
	}
}
