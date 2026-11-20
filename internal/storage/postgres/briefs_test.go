package postgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// The store holds what receipts.Store needs for the CLI and for prepared
// briefs: fenced top-level reads, child reads that name their brief and
// their composite parent, seals that name their brief by their row, and
// arguments kept as given.

// cliRead starts a top-level CLI read of brief under the fixture's reader,
// as the CLI numbers its JSON-RPC requests.
func cliRead(t *testing.T, f *postgrestest.Fixture, operation, brief string, token string) postgres.Receipt {
	t.Helper()
	r, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: operation,
		PrincipalID: f.Reader.ID, TokenID: token, Tool: "get_account", Scope: "crm:read",
		AccountRef: postgrestest.Account, ConnectionID: f.Connection.ID,
		Arguments: json.RawMessage(`{"account_id":"acc-003"}`), RequestID: json.RawMessage(`1`), BriefID: brief,
		Lease: time.Minute})
	ok(t, err)
	return r
}

func TestTopLevelReadsAreFenced(t *testing.T) {
	f := postgrestest.New(t)
	r := cliRead(t, f, "cli-1", "brief-cli", "anonymous")
	if r.ConnectionID != f.Connection.ID || r.ConnectionGeneration != f.Connection.Generation || r.ParentID != "" {
		t.Fatalf("started %+v", r)
	}
	// It is a source read: it finishes through FinishRead, binding its rows.
	_, err := f.Store.FinishReceipt(ctx, finishOK(r, "crm/accounts/acc-003"))
	is(t, err, postgres.ErrSourceRead)
	done, err := f.Store.FinishRead(ctx, r.ID, r.LeaseToken, records("cli"), 1)
	ok(t, err)
	if done.Status != "ok" || len(done.Rows) != 2 {
		t.Fatalf("finished %+v", done)
	}

	// A change of source fences it.
	fenced := cliRead(t, f, "cli-2", "brief-cli", "anonymous")
	_, err = f.Store.BumpConnectionGeneration(ctx, f.Connection.ID)
	ok(t, err)
	_, err = f.Store.FinishRead(ctx, fenced.ID, fenced.LeaseToken, records("cli-2"), 1)
	is(t, err, postgres.ErrSourceChanged)
	if count(t, f, "SELECT count(*) FROM receipt_records WHERE receipt_id = $1", fenced.ID) != 0 {
		t.Fatal("a fenced read bound records")
	}

	// A retry without the connection is another request.
	retry := postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "cli-1", PrincipalID: f.Reader.ID,
		TokenID: "anonymous", Tool: "get_account", Scope: "crm:read", AccountRef: postgrestest.Account,
		Arguments: json.RawMessage(`{"account_id":"acc-003"}`), RequestID: json.RawMessage(`1`),
		BriefID: "brief-cli", Lease: time.Minute}
	_, err = f.Store.StartReceipt(ctx, retry)
	is(t, err, postgres.ErrOperationReused)

	// The scope must be the connection's source's, as the trigger requires.
	calls, err := f.Store.EnsureConnection(ctx, f.Workspace, "calls", "demo")
	ok(t, err)
	retry.OperationID, retry.ConnectionID = "cli-3", calls.ID
	_, err = f.Store.StartReceipt(ctx, retry)
	is(t, err, postgres.ErrWrongSource)
	// Another workspace's connection, or none by that ID, is not found.
	elsewhere, err := f.Store.EnsureWorkspace(ctx, "elsewhere", "demo", "Elsewhere")
	ok(t, err)
	foreign, err := f.Store.EnsureConnection(ctx, elsewhere, "crm", "demo")
	ok(t, err)
	for _, id := range []string{foreign.ID, "00000000-0000-4000-8000-000000000000"} {
		retry.OperationID, retry.ConnectionID = "cli-"+id, id
		_, err = f.Store.StartReceipt(ctx, retry)
		is(t, err, postgres.ErrNotFound)
	}
	if count(t, f, "SELECT count(*) FROM receipts WHERE operation_id LIKE 'cli-%'") != 2 {
		t.Fatal("a refused start left a receipt")
	}
}

func TestChildReadsNameTheirBriefAndStartAtOne(t *testing.T) {
	f := postgrestest.New(t)
	p := parent(t, f, "numbered", time.Minute)
	start := childStart(f, p, 1, time.Minute)
	start.BriefID = "brief-child"
	c, err := f.Store.StartChildRead(ctx, start)
	ok(t, err)
	if c.BriefID != "brief-child" || *c.Ordinal != 1 {
		t.Fatalf("child %+v", c)
	}
	// A retry for another brief is another request.
	start.BriefID = "brief-other"
	_, err = f.Store.StartChildRead(ctx, start)
	is(t, err, postgres.ErrOperationReused)

	for _, ordinal := range []int{0, -1} {
		_, err = f.Store.StartChildRead(ctx, childStart(f, p, ordinal, time.Minute))
		is(t, err, postgres.ErrParent)
	}
	// The trigger refuses an ordinal 0 from any writer.
	if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, parent_id, ordinal, token_id, tool, scope,
		connection_id, connection_generation, arguments, request_id, lease_token, lease_expires_at)
		VALUES ($1, 'zero', $2, 0, 'anonymous', 't', 'crm:read', $3, 1, '{}', 'null', gen_random_uuid(),
		now() + interval '1 minute')`, f.Workspace.ID, p.ID, f.Connection.ID)) != "GT017" {
		t.Fatal("a child numbered 0 was stored")
	}
}

func TestChildReadsNeedACompositeParent(t *testing.T) {
	f := postgrestest.New(t)
	seal, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "a-seal",
		TokenID: "anonymous", Tool: "save_brief", AccountRef: postgrestest.Account, Arguments: json.RawMessage(`{}`),
		Lease: time.Minute})
	ok(t, err)
	_, err = f.Store.StartChildRead(ctx, childStart(f, seal, 1, time.Minute))
	is(t, err, postgres.ErrParent)

	p := parent(t, f, "composite", time.Minute)
	c := child(t, f, p, 1, time.Minute)
	_, err = f.Store.StartChildRead(ctx, childStart(f, c, 1, time.Minute))
	is(t, err, postgres.ErrParent)
	_, err = f.Store.StartChildRead(ctx, childStart(f, postgres.Receipt{ID: "00000000-0000-4000-8000-000000000000"},
		1, time.Minute))
	is(t, err, postgres.ErrParent)

	// The trigger refuses the same from any writer, and a child that reads
	// no connection.
	if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, parent_id, ordinal, token_id, tool, scope,
			connection_id, connection_generation, arguments, request_id, lease_token, lease_expires_at)
		VALUES ($1, 'under-a-seal', $2, 1, 'anonymous', 't', 'crm:read', $3, 1, '{}', 'null', gen_random_uuid(),
			now() + interval '1 minute')`, f.Workspace.ID, seal.ID, f.Connection.ID)) != "GT017" {
		t.Error("a child of a seal was stored")
	}
	if code(sql(t, f, `INSERT INTO receipts (workspace_id, operation_id, parent_id, ordinal, token_id, tool,
			arguments, request_id, lease_token, lease_expires_at)
		VALUES ($1, 'no-connection', $2, 2, 'anonymous', 't', '{}', 'null', gen_random_uuid(),
			now() + interval '1 minute')`, f.Workspace.ID, p.ID)) != "GT017" {
		t.Error("a child that reads no connection was stored")
	}
}

// cliCard reads acc-003 as the CLI does, a top-level read for brief, and
// describes the card sealed by a save_brief receipt with no brief_id.
func cliCard(t *testing.T, f *postgrestest.Fixture, brief string) (postgres.CardPublication, postgres.Receipt) {
	t.Helper()
	token := postgres.TokenID("cli bearer")
	r := cliRead(t, f, brief+"-read", brief, token)
	_, err := f.Store.FinishRead(ctx, r.ID, r.LeaseToken, records(brief), 1)
	ok(t, err)
	versions, err := f.Store.ReceiptRecordIDs(ctx, f.Workspace.ID, r.ID)
	ok(t, err)
	seal, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: brief + "-seal", PrincipalID: f.Reader.ID, TokenID: token, Tool: "save_brief",
		AccountRef: postgrestest.Account, Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"` + brief + `"}`),
		Lease: time.Minute})
	ok(t, err)
	rules := "3"
	units := []postgres.Unit{{N: 1, Kind: "record", RecordRef: "crm/accounts/acc-003", Tool: "get_account",
		ReceiptID: r.ID, SourceRecordID: versions["crm/accounts/acc-003"],
		Body: postgrestest.UnitBody(1, "record", "crm/accounts/acc-003", "get_account", "", nil)}}
	gaps := []postgres.Gap{{Code: "neutralized", Disposition: "note", Text: "none"}}
	return postgres.CardPublication{WorkspaceID: f.Workspace.ID, AccountRef: postgrestest.Account, BriefID: brief,
		ParentReceiptID: seal.ID, PrincipalID: f.Reader.ID, SealReceiptID: seal.ID, SealLeaseToken: seal.LeaseToken,
		Kind: "go", SchemaVersion: 3, RulesVersion: &rules, Bytes: postgrestest.CardBytes(brief, units, gaps),
		Units: units, Gaps: gaps, ReadReceiptIDs: []string{r.ID}}, seal
}

func TestACLISealWithoutABriefIDPublishes(t *testing.T) {
	f := postgrestest.New(t)
	pub, seal := cliCard(t, f, "brief-cli")
	if seal.BriefID != "" {
		t.Fatal("the seal names a brief_id")
	}
	// A denied read the card states rests beside the one its unit cites.
	denied := cliRead(t, f, "brief-cli-denied", "brief-cli", postgres.TokenID("cli bearer"))
	_, err := f.Store.FailRead(ctx, denied.ID, denied.LeaseToken, 403, "request_failed", 1)
	ok(t, err)
	pub.ReadReceiptIDs = append(pub.ReadReceiptIDs, denied.ID)
	card, err := f.Store.PublishCard(ctx, pub)
	ok(t, err)
	sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
	ok(t, err)
	if card.ParentReceiptID != seal.ID || sealed.Status != "ok" || sealed.BriefID != "" {
		t.Fatalf("card %+v, seal %+v", card, sealed)
	}
}

func TestACLICardRestsOnlyOnItsOwnBriefsReads(t *testing.T) {
	f := postgrestest.New(t)
	for name, change := range map[string]func(t *testing.T, pub *postgres.CardPublication){
		"a read of another brief": func(t *testing.T, pub *postgres.CardPublication) {
			other := cliRead(t, f, pub.BriefID+"-other", "brief-other", postgres.TokenID("cli bearer"))
			_, err := f.Store.FinishRead(ctx, other.ID, other.LeaseToken, records(pub.BriefID+"-other"), 1)
			ok(t, err)
			pub.ReadReceiptIDs = append(pub.ReadReceiptIDs, other.ID)
		},
		"a read under another token": func(t *testing.T, pub *postgres.CardPublication) {
			other := cliRead(t, f, pub.BriefID+"-token", pub.BriefID, postgres.TokenID("another bearer"))
			_, err := f.Store.FinishRead(ctx, other.ID, other.LeaseToken, records(pub.BriefID+"-token"), 1)
			ok(t, err)
			pub.ReadReceiptIDs = append(pub.ReadReceiptIDs, other.ID)
		},
		"a pending read": func(t *testing.T, pub *postgres.CardPublication) {
			pending := cliRead(t, f, pub.BriefID+"-pending", pub.BriefID, postgres.TokenID("cli bearer"))
			pub.ReadReceiptIDs = append(pub.ReadReceiptIDs, pending.ID)
		},
		"a unit on a failed read": func(t *testing.T, pub *postgres.CardPublication) {
			failed := cliRead(t, f, pub.BriefID+"-failed", pub.BriefID, postgres.TokenID("cli bearer"))
			_, err := f.Store.FailRead(ctx, failed.ID, failed.LeaseToken, 403, "request_failed", 1)
			ok(t, err)
			pub.Units[0].ReceiptID = failed.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			pub, seal := cliCard(t, f, "brief-"+map[string]string{"a read of another brief": "a",
				"a read under another token": "b", "a pending read": "c", "a unit on a failed read": "d"}[name])
			change(t, &pub)
			_, err := f.Store.PublishCard(ctx, pub)
			is(t, err, postgres.ErrProvenance)
			sealed, err := f.Store.Receipt(ctx, f.Workspace.ID, seal.ID)
			ok(t, err)
			if sealed.Status != "error" || *sealed.Error != "provenance" {
				t.Fatalf("seal %+v", sealed)
			}
		})
	}
	// The trigger holds a top-level read of another brief off a card too.
	pub, seal := cliCard(t, f, "brief-e")
	other := cliRead(t, f, "brief-e-other", "brief-other", postgres.TokenID("cli bearer"))
	_, err := f.Store.FinishRead(ctx, other.ID, other.LeaseToken, records("brief-e-other"), 1)
	ok(t, err)
	_, err = f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: seal.ID, LeaseToken: seal.LeaseToken,
		Status: "ok", HTTPStatus: 200, Rows: []string{"briefs/brief-e/" + sha(pub.Bytes)}})
	ok(t, err)
	if code(sql(t, f, `WITH c AS (
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version, card_bytes,
				card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count)
			VALUES ($1, 'acc-003', 'brief-e', 'cli', 3, '3', $2, sha256($2), $3, $3, 1, 1) RETURNING id),
		u AS (INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, receipt_id, body)
			SELECT $1, id, 1, 'record', 'crm/accounts/acc-003', 'get_account', $4, $5 FROM c)
		INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, text)
			SELECT $1, id, 0, 'neutralized', 'note', 'none' FROM c`,
		f.Workspace.ID, pub.Bytes, seal.ID, other.ID, pub.Units[0].Body)) != "GT009" {
		t.Fatal("the database stored a card on another brief's read")
	}
}

func TestASealNamingAnotherBriefIsRefused(t *testing.T) {
	f := postgrestest.New(t)
	pub, _ := cliCard(t, f, "brief-named")
	seal, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "named-seal", PrincipalID: f.Reader.ID, TokenID: postgres.TokenID("cli bearer"),
		Tool: "save_brief", AccountRef: postgrestest.Account, BriefID: "brief-another",
		Arguments: json.RawMessage(`{}`), Lease: time.Minute})
	ok(t, err)
	pub.SealReceiptID, pub.SealLeaseToken, pub.ParentReceiptID = seal.ID, seal.LeaseToken, seal.ID
	_, err = f.Store.PublishCard(ctx, pub)
	is(t, err, postgres.ErrSealMismatch)
}

func TestArgumentsKeepTheirOrder(t *testing.T) {
	f := postgrestest.New(t)
	given := `{"brief_id": "b", "account_id": "acc-003", "nested": {"z": 1, "a": 1.0}}`
	r, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "ordered",
		TokenID: "anonymous", Tool: "save_brief", Arguments: json.RawMessage(given), Lease: time.Minute})
	ok(t, err)
	stored, err := f.Store.Receipt(ctx, f.Workspace.ID, r.ID)
	ok(t, err)
	if string(stored.Arguments) != given || string(r.Arguments) != given {
		t.Fatalf("arguments came back as %s", stored.Arguments)
	}
	// The same arguments in another order are the same request on retry.
	_, err = f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID, OperationID: "ordered",
		TokenID: "anonymous", Tool: "save_brief", Lease: time.Minute,
		Arguments: json.RawMessage(`{"account_id":"acc-003","brief_id":"b","nested":{"a":1.0,"z":1}}`)})
	ok(t, err)
	// The update trigger still refuses a change to them.
	if code(sql(t, f, `UPDATE receipts SET arguments = '{"account_id": "acc-004"}', finishing_lease = lease_token
		WHERE id = $1`, r.ID)) != "GT002" {
		t.Fatal("a receipt's arguments changed")
	}
}

func sha(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
