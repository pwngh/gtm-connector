package cards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The parity families again, with the maker reading, sealing and publishing through PostgreSQL.
//
// PostgreSQL gives every receipt its ID (gen_random_uuid) and its start time (clock_timestamp) when it
// commits, and takes neither from a caller, so the frozen generator's receipt IDs and times cannot be
// reproduced. Before a card is compared with the oracle, those values, and nothing else, are put back,
// each at its own field's byte span: in the card, every embedded read's audit_id and started_at (and, in a
// prepared brief, its request_id's parent_audit_id), sealed_by or prepared_by's outer_audit_id, and within
// text only its "Sources read" line, which states the first and last start time of the ok reads; in the
// seal, its audit_id, its started_at, and the digest its row names, which is the digest of the card being
// compared. First, every value put back is checked against the receipts table, read with SQL apart from
// the store's own code: each embedded read and the seal are the rows' audit-log JSON, the reads started in
// list order and the seal after them, and the "Sources read" line is what the stored times give. The text
// is also built again from the stored card's evidence with the oracle's times put in, and must be the
// oracle's text byte for byte. Everything else is compared as stored: every other byte of the card, its
// SHA-256 after the replacement, every other column of the seal, and the unit and gap rows, type for type,
// against the oracle's units and gaps, each unit's read and record version against the verifier's binding.

func TestCardsOnPostgres(t *testing.T) {
	demoStoreFamilies(t, newPostgresCards)
	recordedInputFamilies(t, newPostgresCards)
}

// postgresCards builds a family's cards in one demo workspace of a fresh database, as its local owner.
type postgresCards struct {
	url         string
	conn        *pgx.Conn // the test's own, to look at the rows
	store       *postgres.Store
	receipts    *postgres.ReceiptStore
	workspace   string
	connections map[string]string
}

func newPostgresCards(t *testing.T) cardStore {
	ctx := context.Background()
	c := &postgresCards{url: pgtest.URL(t), connections: map[string]string{}}
	var err error
	if c.store, err = postgres.Open(ctx, c.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.store.Close)
	if err := c.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if c.conn, err = pgx.Connect(ctx, c.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.conn.Close(context.Background()) })
	c.open(t, "parity", "Parity workspace")
	c.receipts = &postgres.ReceiptStore{Store: c.store}
	return c
}

// open makes a demo workspace, with a connection for each source, the one the store's cards are built in.
func (c *postgresCards) open(t *testing.T, slug, name string) {
	t.Helper()
	ctx := context.Background()
	workspace, err := c.store.EnsureWorkspace(ctx, slug, "demo", name)
	if err != nil {
		t.Fatal(err)
	}
	c.workspace, c.connections = workspace.ID, map[string]string{}
	for _, source := range []string{"crm", "calls", "slack"} {
		connection, err := c.store.EnsureConnection(ctx, workspace, source, "demo")
		if err != nil {
			t.Fatal(err)
		}
		c.connections[source] = connection.ID
	}
}

// resolved commits the read an import's association names: a get_account read of the workspace's CRM that
// returned the account. It gives the read's ID. A recorded import names the ID the oracle's read had, so
// with id the read is committed under that ID, by a row this test writes itself; the store's own receipts
// take the database's IDs.
func (c *postgresCards) resolved(t *testing.T, account, id string) string {
	t.Helper()
	ctx := context.Background()
	lease := id
	if id == "" {
		started, err := c.store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: c.workspace,
			OperationID: "resolve " + account + " " + receipts.RandomID(), TokenID: receipts.Fingerprint("a bearer"),
			Tool: "get_account", Scope: "crm:read", AccountRef: account, ConnectionID: c.connections["crm"],
			Arguments: json.RawMessage(`{"account_id":"` + account + `"}`), Lease: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		id, lease = started.ID, started.LeaseToken
	} else if _, err := c.conn.Exec(ctx, `
		INSERT INTO receipts (id, workspace_id, operation_id, token_id, tool, scope, requested_account, account_ref,
			connection_id, connection_generation, arguments, request_id, lease_token, lease_expires_at)
		SELECT $1::uuid, k.workspace_id, 'resolve ' || $1::uuid::text, $2, 'get_account', 'crm:read', $3::text, $3::text, k.id,
			k.generation, json_build_object('account_id', $3::text), '1', $1::uuid, clock_timestamp() + interval '1 minute'
		  FROM connections k WHERE k.id = $4`, id, receipts.Fingerprint("a bearer"), account,
		c.connections["crm"]); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + account + `","record_ref":"crm/accounts/` + account + `"}`
	if _, err := c.store.FinishRead(ctx, id, lease, []postgres.RecordVersion{{Source: "crm", Table: "accounts",
		RecordID: account, Body: []byte(body)}}, 0); err != nil {
		t.Fatal(err)
	}
	return id
}

func (c *postgresCards) maker(t *testing.T, sc *scope, s setup, row fixtureRow, b Brief) (*Maker, Brief) {
	if s.calls != nil {
		// The oracle kept a state folder for each book, and the books number their accounts alike. So each
		// book's imports, receipts and cards are in a workspace of their own: the store of imports and the
		// maker's principal name the same one.
		c.open(t, "calls-"+s.calls.book, "Call cards of "+s.calls.book)
	}
	if err := c.store.EnsureAccount(context.Background(), c.workspace, row.AccountID, "Invented account"); err != nil {
		t.Fatal(err)
	}
	if s.calls != nil {
		imports := postgres.CallImports{Store: c.store, WorkspaceID: c.workspace}
		s.source = s.calls.source(t, row, imports, func(made calls.Import) (*strictjson.Object, error) {
			c.resolved(t, row.AccountID, made.Association.ResolutionReceiptID)
			return imports.Import(context.Background(), made)
		})
	}
	b.Principal.WorkspaceID = c.workspace
	// Operation IDs are unique in a workspace; the oracle's are per scope.
	digest := sha256.Sum256([]byte(s.scope))
	b.OperationID = "parity-" + hex.EncodeToString(digest[:16])
	return &Maker{Receipts: c.receipts, Source: s.source, Sources: s.sources, Connections: c.connections,
		NewID: sc.uuid, Now: still}, b
}

func (c *postgresCards) published(t *testing.T, sealed *Sealed, s setup, row fixtureRow,
	mismatches *oracletest.Mismatches) ([]byte, string, receipts.Receipt) {
	stored, problems := c.check(t, sealed, s.sources, []byte(row.Card), false)
	for _, problem := range problems {
		mismatches.Add("%s: %s", row.Key, problem)
	}
	value, err := strictjson.Loads(row.Seal)
	if err != nil {
		t.Fatal(err)
	}
	oracleSeal, err := receipts.Decode(value)
	if err != nil {
		t.Fatal(err)
	}
	normalised, err := normalise(stored, []byte(row.Card), false)
	if err != nil {
		mismatches.Add("%s: %v", row.Key, err)
		return stored, sealed.SHA256, sealed.Seal
	}
	seal := sealed.Seal
	seal.AuditID, seal.StartedAt = oracleSeal.AuditID, oracleSeal.StartedAt
	seal.Rows = []string{receipts.Reference(sealed.BriefID, normalised)}
	sum := sha256.Sum256(normalised)
	return normalised, hex.EncodeToString(sum[:]), seal
}

func (c *postgresCards) lines(t *testing.T, sealed *Sealed, _ setup) []receipts.LineRow {
	t.Helper()
	rows, err := c.conn.Query(context.Background(), `SELECT l.section, l.body::text FROM card_lines l
		JOIN cards c ON c.id = l.card_id WHERE c.workspace_id = $1 AND c.brief_id = $2 ORDER BY l.ordinal`,
		c.workspace, sealed.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (receipts.LineRow, error) {
		var l receipts.LineRow
		var body string
		err := row.Scan(&l.Section, &body)
		l.Value = []byte(body)
		return l, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// check reads what PostgreSQL holds for a card the maker published, and gives the stored bytes and every
// way they or their rows differ from what the maker sealed, the receipts table, the verifier and want.
func (c *postgresCards) check(t *testing.T, sealed *Sealed, sources evidence.Sources, want []byte,
	prepared bool) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	var stored, digest []byte
	var kind, card, parent, seal string
	err := c.conn.QueryRow(ctx, `SELECT card_bytes, card_sha256, kind, id::text, parent_receipt_id::text,
		seal_receipt_id::text FROM cards WHERE workspace_id = $1 AND brief_id = $2`, c.workspace, sealed.BriefID).
		Scan(&stored, &digest, &kind, &card, &parent, &seal)
	if err != nil {
		t.Fatalf("the published card: %v", err)
	}
	sum := sha256.Sum256(stored)
	if !bytes.Equal(stored, sealed.Payload) || !bytes.Equal(digest, sum[:]) || hex.EncodeToString(sum[:]) != sealed.SHA256 ||
		sealed.Seal.Rows[0] != receipts.Reference(sealed.BriefID, stored) || kind != "go" || parent != seal ||
		seal != sealed.Seal.AuditID {
		add("the stored card, its digest and its seal disagree")
	}
	// The embedded reads and the seal are the receipts table's rows, in order of their start.
	payload, err := receipts.ParsePayload(stored)
	if err != nil {
		t.Fatal(err)
	}
	var last time.Time
	for i, embedded := range payload.Receipts {
		row, started := rawReceipt(t, c.conn, payload.ReadIDs[i])
		if !sameJSON(row, embedded) {
			add("read %d is embedded other than its row", i+1)
		}
		if !started.After(last) {
			add("read %d did not start after the read before it", i+1)
		}
		last = started
	}
	// A card's seal starts after its reads; a prepared brief's call starts before them.
	row, started := rawReceipt(t, c.conn, seal)
	if !sameJSON(row, sealed.Seal.Value()) {
		got, _ := strictjson.Dumps(sealed.Seal.Value(), strictjson.Quoted)
		add("the seal %s is not its row %v", got, row)
	}
	if first, _ := time.Parse(time.RFC3339Nano, get(payload.Receipts[0], "started_at").(string)); prepared &&
		!started.Before(first) || !prepared && !started.After(last) {
		add("the seal started out of order with the reads")
	}
	if got := countRows(t, c.conn, `SELECT count(*) FROM card_reads WHERE card_id = $1`, card); got != len(payload.ReadIDs) {
		add("%d reads recorded, the card embeds %d", got, len(payload.ReadIDs))
	}
	// The text, built again from the stored evidence with the oracle's times put in, is the oracle's.
	wantPayload, err := receipts.ParsePayload(want)
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := textAt(t, stored, wantPayload); !ok || text != get(load(t, want).(*strictjson.Object), "text") {
		add("the text, built again under the oracle's times, is not the oracle's")
	}
	// The unit and gap rows, type for type, are the oracle's units and gaps; each unit binds the read and
	// version the verifier resolves.
	log := StoreLog{Store: c.receipts, WorkspaceID: c.workspace}
	var admitted *Admitted
	if prepared {
		admitted, err = VerifyPrepared(ctx, stored, sealed.BriefID, log, sources)
	} else {
		admitted, err = VerifyCard(ctx, stored, sealed.BriefID, log, sources)
	}
	if err != nil {
		add("the stored card does not verify: %v", err)
		return stored, problems
	}
	rows, err := c.conn.Query(ctx, `SELECT u.body::text, u.receipt_id::text, v.content_version,
		v.source || '/' || v.record_table || '/' || v.record_id
		  FROM evidence_units u JOIN source_records v ON v.id = u.source_record_id
		 WHERE u.card_id = $1 ORDER BY u.n`, card)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var body, receipt, version, ref string
	_, err = pgx.ForEachRow(rows, []any{&body, &receipt, &version, &ref}, func() error {
		n++
		if n > len(wantPayload.Units) || n > len(admitted.Units) || !sameJSON(load(t, []byte(body)), wantPayload.Units[n-1]) {
			add("unit %d's row is not the oracle's unit", n)
			return nil
		}
		b := admitted.Units[n-1]
		if receipt != b.ReceiptID || version != b.Record.ContentVersion || ref != b.Record.Source+"/"+b.Record.Table+
			"/"+b.Record.RecordID {
			add("unit %d binds %s %s, the verifier %s %s", n, receipt, version, b.ReceiptID, b.Record.ContentVersion)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(wantPayload.Units) {
		add("%d unit rows for %d units", n, len(wantPayload.Units))
	}
	rows, err = c.conn.Query(ctx, `SELECT json_build_object('code', code, 'disposition', disposition,
		'count', count, 'by_role', by_role, 'text', text)::text FROM gaps WHERE card_id = $1 ORDER BY ordinal`, card)
	if err != nil {
		t.Fatal(err)
	}
	gaps, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != len(wantPayload.Gaps) {
		add("%d gap rows for %d gaps", len(gaps), len(wantPayload.Gaps))
	}
	for i := range min(len(gaps), len(wantPayload.Gaps)) {
		if !sameJSON(load(t, []byte(gaps[i])), wantPayload.Gaps[i]) {
			add("gap %d's row is not the oracle's gap", i+1)
		}
	}
	return stored, problems
}

// sameJSON compares two JSON values type for type, as strictjson writes them.
func sameJSON(a, b any) bool {
	x, errA := strictjson.Dumps(a, strictjson.Prepared)
	y, errB := strictjson.Dumps(b, strictjson.Prepared)
	return errA == nil && errB == nil && x == y
}

func countRows(t *testing.T, conn *pgx.Conn, query string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rawReceipt is a receipt's audit-log JSON built from its row with SQL and this test's own code, apart from
// the store's, and its start time.
func rawReceipt(t *testing.T, conn *pgx.Conn, id string) (*strictjson.Object, time.Time) {
	t.Helper()
	var started time.Time
	var token, tool, arguments, requestID, status, rows string
	var brief, errorCode *string
	var httpStatus *int
	var rowCount int
	var elapsed *float64
	if err := conn.QueryRow(context.Background(), `SELECT started_at, token_id, tool, arguments::text,
		request_id::text, brief_id, status, http_status, row_count, rows::text, elapsed_ms, error
		FROM receipts WHERE id = $1`, id).Scan(&started, &token, &tool, &arguments, &requestID, &brief, &status,
		&httpStatus, &rowCount, &rows, &elapsed, &errorCode); err != nil {
		t.Fatal(err)
	}
	optional := func(p any) any {
		switch v := p.(type) {
		case *string:
			if v != nil {
				return *v
			}
		case *int:
			if v != nil {
				return *v
			}
		case *float64:
			if v != nil {
				return *v
			}
		}
		return nil
	}
	return strictjson.NewObject("audit_id", id, "started_at", receipts.StartedAtText(started), "token_id", token,
		"tool", tool, "arguments", load(t, []byte(arguments)), "request_id", load(t, []byte(requestID)),
		"brief_id", optional(brief), "status", status, "http_status", optional(httpStatus), "row_count", rowCount,
		"rows", load(t, []byte(rows)), "elapsed_ms", optional(elapsed), "error", optional(errorCode)), started
}

// textAt builds a card's text again from its stored evidence with want's read times put in.
func textAt(t *testing.T, stored []byte, want *receipts.Payload) (string, bool) {
	t.Helper()
	value := load(t, stored).(*strictjson.Object)
	snapshot, _ := value.Get("evidence")
	list, _ := snapshot.(*strictjson.Object).Get("receipts")
	reads := list.([]any)
	if len(reads) != len(want.Receipts) {
		return "", false
	}
	for i, r := range reads {
		at, _ := want.Receipts[i].Get("started_at")
		r.(*strictjson.Object).Set("started_at", at)
	}
	_, text, _, _, err := evidence.Build(snapshot.(*strictjson.Object))
	return text, err == nil
}

// sourcesRead is the text's line on when the ok reads started, as evidence.Build writes it.
func sourcesRead(reads []*strictjson.Object) string {
	var first, last time.Time
	for _, r := range reads {
		at, err := time.Parse(time.RFC3339Nano, get(r, "started_at").(string))
		if err != nil || get(r, "status") != "ok" {
			continue
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	from, to := first.Format("2006-01-02T15:04:05Z"), last.Format("2006-01-02T15:04:05Z")
	if from == to {
		return "Sources read " + from + "."
	}
	return "Sources read " + from + " to " + to + "."
}

// spans maps each scalar value of a JSON document to the byte span it is written at, by its path: object
// keys and array indexes joined with ".".
func spans(data []byte) (map[string][2]int, error) {
	found := map[string][2]int{}
	i := 0
	blank := func() {
		for i < len(data) && strings.IndexByte(" \t\r\n", data[i]) >= 0 {
			i++
		}
	}
	quoted := func() (string, error) {
		start := i
		for i++; i < len(data) && data[i] != '"'; i++ {
			if data[i] == '\\' {
				i++
			}
		}
		if i >= len(data) {
			return "", errors.New("an unterminated string")
		}
		i++
		var s string
		return s, json.Unmarshal(data[start:i], &s)
	}
	join := func(path, key string) string {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	var value func(path string) error
	value = func(path string) error {
		blank()
		if i >= len(data) {
			return errors.New("the document ends early")
		}
		switch data[i] {
		case '{', '[':
			closing, index := byte('}'), 0
			if data[i] == '[' {
				closing = ']'
			}
			i++
			blank()
			if i < len(data) && data[i] == closing {
				i++
				return nil
			}
			for {
				key := fmt.Sprint(index)
				if closing == '}' {
					blank()
					var err error
					if key, err = quoted(); err != nil {
						return err
					}
					blank()
					if i >= len(data) || data[i] != ':' {
						return errors.New("a key without its value")
					}
					i++
				}
				if err := value(join(path, key)); err != nil {
					return err
				}
				index++
				blank()
				switch {
				case i < len(data) && data[i] == ',':
					i++
				case i < len(data) && data[i] == closing:
					i++
					return nil
				default:
					return errors.New("a list or object without its end")
				}
			}
		case '"':
			start := i
			if _, err := quoted(); err != nil {
				return err
			}
			found[path] = [2]int{start, i}
		default:
			start := i
			for i < len(data) && strings.IndexByte(",]} \t\r\n", data[i]) < 0 {
				i++
			}
			found[path] = [2]int{start, i}
		}
		return nil
	}
	return found, value("")
}

// normalise puts want's receipt IDs and read times into stored, each at its own field's span, as the
// comment at the top of this file says, and nothing else.
func normalise(stored, want []byte, prepared bool) ([]byte, error) {
	got, err := spans(stored)
	if err != nil {
		return nil, err
	}
	oracle, err := spans(want)
	if err != nil {
		return nil, err
	}
	storedPayload, err := receipts.ParsePayload(stored)
	if err != nil {
		return nil, err
	}
	wantPayload, err := receipts.ParsePayload(want)
	if err != nil {
		return nil, err
	}
	if len(storedPayload.Receipts) != len(wantPayload.Receipts) {
		return nil, fmt.Errorf("the card embeds %d reads, the oracle's %d", len(storedPayload.Receipts),
			len(wantPayload.Receipts))
	}
	fields := []string{"sealed_by"}
	if prepared {
		fields = []string{"prepared_by.outer_audit_id"}
	}
	for i := range storedPayload.Receipts {
		read := fmt.Sprintf("evidence.receipts.%d.", i)
		fields = append(fields, read+"audit_id", read+"started_at")
		if prepared {
			fields = append(fields, read+"request_id.parent_audit_id")
		}
	}
	type patch struct {
		at   [2]int
		with []byte
	}
	var patches []patch
	for _, field := range fields {
		at, found := got[field]
		from, oracleFound := oracle[field]
		if !found || !oracleFound {
			return nil, fmt.Errorf("%s is missing", field)
		}
		patches = append(patches, patch{at, want[from[0]:from[1]]})
	}
	// Within text, only the "Sources read" line.
	at, found := got["text"]
	if !found {
		return nil, errors.New("the card has no text")
	}
	line, oracleLine := sourcesRead(storedPayload.Receipts), sourcesRead(wantPayload.Receipts)
	text := string(stored[at[0]:at[1]])
	if strings.Count(text, line) != 1 || !bytes.Contains(want[oracle["text"][0]:oracle["text"][1]], []byte(oracleLine)) {
		return nil, fmt.Errorf("the text does not state %q once", line)
	}
	patches = append(patches, patch{at, []byte(strings.Replace(text, line, oracleLine, 1))})
	sort.Slice(patches, func(i, j int) bool { return patches[i].at[0] > patches[j].at[0] })
	out := append([]byte(nil), stored...)
	for _, p := range patches {
		out = append(out[:p.at[0]], append(append([]byte(nil), p.with...), out[p.at[1]:]...)...)
	}
	return out, nil
}

// The folder cards on PostgreSQL: a card made by brief and three prepared briefs, the last for an account
// selected without Slack. Each is compared with the oracle's file by bytes, and each receipt with the
// oracle's audit row, with the IDs and times put back as above.
func TestFolderCardsOnPostgres(t *testing.T) {
	ctx := context.Background()
	for _, fc := range folders {
		t.Run(fc.name, func(t *testing.T) {
			c := newPostgresCards(t).(*postgresCards)
			if err := c.store.EnsureAccount(ctx, c.workspace, fc.account, "Invented account"); err != nil {
				t.Fatal(err)
			}
			sc := newScope(map[string]string{"cli_card": "folders/cli_card/1", "prepared": "folders/prepared",
				"prepared_as_of": "folders/prepared_as_of", "prepared_no_slack": "folders/prepared_no_slack"}[fc.name])
			token := sc.token(accessToken + 1)
			if fc.prepared {
				sc.uuid() // the oracle's outer receipt drew the first ID
			}
			m := &Maker{Receipts: c.receipts, Source: demoBook(t), Sources: demoSources(nil),
				Connections: c.connections, NewID: sc.uuid, Now: still}
			principal := owner(token, "crm:read", "calls:read", "slack:read")
			principal.WorkspaceID = c.workspace
			var sealed *Sealed
			var err error
			switch fc.name {
			case "cli_card":
				sealed, err = m.Brief(ctx, Brief{Principal: principal, OperationID: "brief", AccountID: fc.account,
					AsOf: DemoAsOf, IncludeSlack: true})
			case "prepared":
				principal.Scopes = []string{"crm:read", "calls:read"}
				sealed, err = m.Prepare(ctx, prepareOf("", fc.account, true, strictjson.NewObject("account_id",
					fc.account)).with(principal))
			case "prepared_as_of":
				sealed, err = m.Prepare(ctx, prepareOf("", fc.account, true, strictjson.NewObject("account_id",
					fc.account, "as_of", "2027-01-20")).with(principal))
			case "prepared_no_slack":
				sealed, err = m.Prepare(ctx, prepareOf("", fc.account, false, strictjson.NewObject("account_id",
					fc.account)).with(principal))
			}
			if err != nil {
				t.Fatal(err)
			}
			f := openFolder(t, fc.name)
			want := *f.payload(fc.brief)
			stored, problems := c.check(t, sealed, demoSources(nil), want, fc.prepared)
			for _, problem := range problems {
				t.Error(problem)
			}
			normalised, err := normalise(stored, want, fc.prepared)
			if err != nil {
				t.Fatal(err)
			}
			if sealed.BriefID != fc.brief || !bytes.Equal(normalised, want) {
				t.Fatalf("%s differs at %s", sealed.BriefID, oracletest.FirstDifference(string(normalised), string(want)))
			}
			// Each receipt is the oracle's row, but for its ID, its time and its parent's ID.
			storedPayload, _ := receipts.ParsePayload(stored)
			wantPayload, _ := receipts.ParsePayload(want)
			pairs := map[string]string{storedPayload.SealID: wantPayload.SealID}
			for i, id := range storedPayload.ReadIDs {
				pairs[id] = wantPayload.ReadIDs[i]
			}
			if len(pairs) != len(f.Log) {
				t.Fatalf("Go wrote %d receipts; the folder holds %d", len(pairs), len(f.Log))
			}
			for mine, theirs := range pairs {
				row, _ := rawReceipt(t, c.conn, mine)
				logged, err := f.Log.Receipts(ctx, []string{theirs})
				if err != nil {
					t.Fatal(err)
				}
				oracleRow := logged[0]
				row.Set("audit_id", theirs)
				at, _ := oracleRow.Get("started_at")
				row.Set("started_at", at)
				if request, isObject := get(row, "request_id").(*strictjson.Object); isObject {
					request.Set("parent_audit_id", wantPayload.SealID)
				}
				if mine == storedPayload.SealID {
					row.Set("rows", []any{receipts.Reference(fc.brief, normalised)})
				}
				if !sameJSON(row, oracleRow) {
					got, _ := strictjson.Dumps(row, strictjson.Quoted)
					t.Errorf("receipt %s:\n got %s\nwant %v", theirs, got, oracleRow)
				}
			}
		})
	}
}

// with makes a prepared brief's call under this principal.
func (p Prepare) with(principal Principal) Prepare {
	p.Principal = principal
	return p
}

// e2e is a demo workspace on a fresh database whose reader holds a grant on acc-003, for the maker to read
// the demo book through.
type e2e struct {
	*postgresCards
	reader postgres.Principal
	reads  map[string]int
}

func newE2E(t *testing.T) *e2e {
	c := newPostgresCards(t).(*postgresCards)
	ctx := context.Background()
	if err := c.store.EnsureAccount(ctx, c.workspace, "acc-003", "Invented account"); err != nil {
		t.Fatal(err)
	}
	w, err := c.store.EnsureWorkspace(ctx, "parity", "demo", "Parity workspace")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := c.store.CreatePrincipal(ctx, w, "person", "Demo reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.GrantAccount(ctx, c.workspace, reader.ID, "acc-003"); err != nil {
		t.Fatal(err)
	}
	return &e2e{postgresCards: c, reader: reader, reads: map[string]int{}}
}

// during runs before each source read of the demo book, after its receipt committed.
type during struct {
	Source
	before func(tool string)
}

func (d during) Read(ctx context.Context, tool, accountID string) (string, []*strictjson.Object, error) {
	d.before(tool)
	return d.Source.Read(ctx, tool, accountID)
}

// maker reads the demo book; before runs before each read and counts it.
func (e *e2e) maker(t *testing.T, store receipts.Store, before func(tool string)) *Maker {
	return &Maker{Receipts: store, Source: during{demoBook(t), func(tool string) {
		e.reads[tool]++
		if before != nil {
			before(tool)
		}
	}}, Sources: demoSources(nil), Connections: e.connections}
}

func (e *e2e) principal() Principal {
	return Principal{WorkspaceID: e.workspace, PrincipalID: e.reader.ID, TokenID: receipts.Fingerprint("a bearer"),
		Scopes: []string{"crm:read", "calls:read", "slack:read"}}
}

func (e *e2e) brief(operation string) Brief {
	return Brief{Principal: e.principal(), OperationID: operation, AccountID: "acc-003", AsOf: DemoAsOf,
		IncludeSlack: true}
}

func (e *e2e) prepare(operation string) Prepare {
	return Prepare{Principal: e.principal(), Selection: Selection{"acc-003": true}, OperationID: operation,
		RequestID: receipts.RPC(1), Arguments: strictjson.NewObject("account_id", "acc-003")}
}

func (e *e2e) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The receipts rule, end to end on PostgreSQL: no read without a committed receipt, no card from a read the
// source changed under, and no card for a principal whose grant was revoked before publication.
func TestReceiptsRuleOnPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("the store is unavailable at Start: nothing is read", func(t *testing.T) {
		e := newE2E(t)
		down, err := postgres.Open(ctx, e.url)
		if err != nil {
			t.Fatal(err)
		}
		down.Close()
		m := e.maker(t, &postgres.ReceiptStore{Store: down}, nil)
		if _, err := m.Brief(ctx, e.brief("down-brief")); err == nil {
			t.Fatal("a card without a receipt store")
		}
		if _, err := m.Prepare(ctx, e.prepare("down-prepare")); err == nil {
			t.Fatal("a prepared brief without a receipt store")
		}
		if len(e.reads) != 0 || e.count(t, "SELECT count(*) FROM receipts") != 0 {
			t.Fatalf("read %v with no receipt", e.reads)
		}
	})
	t.Run("a generation bump during a read gives no card", func(t *testing.T) {
		e := newE2E(t)
		bump := func(tool string) {
			if tool == "list_opportunities" {
				if _, err := e.store.BumpConnectionGeneration(ctx, e.connections["crm"]); err != nil {
					t.Error(err)
				}
			}
		}
		m := e.maker(t, e.receipts, bump)
		_, err := m.Brief(ctx, e.brief("bumped-brief"))
		if !errors.Is(err, receipts.ErrSourceChanged) {
			t.Fatalf("the CLI card: %v", err)
		}
		_, err = m.Prepare(ctx, e.prepare("bumped-prepare"))
		if !errors.Is(err, receipts.ErrSourceChanged) {
			t.Fatalf("the prepared brief: %v", err)
		}
		if n := e.count(t, "SELECT count(*) FROM cards"); n != 0 {
			t.Fatalf("%d cards", n)
		}
		// Each fenced read finished as source_changed and bound nothing; the prepared brief's call failed.
		if e.count(t, `SELECT count(*) FROM receipts WHERE tool = 'list_opportunities' AND status = 'error'
			AND http_status = 409 AND error = 'source_changed' AND row_count = 0`) != 2 ||
			e.count(t, `SELECT count(*) FROM receipt_records b JOIN receipts r ON r.id = b.receipt_id
			WHERE r.tool = 'list_opportunities'`) != 0 ||
			e.count(t, `SELECT count(*) FROM receipts WHERE operation_id = 'bumped-prepare' AND status = 'error'`) != 1 {
			t.Fatal("the fenced reads were not recorded as fenced")
		}
		// After the change, a new card reads at the new generation.
		if _, err := e.maker(t, e.receipts, nil).Brief(ctx, e.brief("after-the-bump")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a grant revoked before publication gives no card", func(t *testing.T) {
		e := newE2E(t)
		revoke := func(tool string) {
			if tool == "read_slack_messages" { // the last read
				if err := e.store.RevokeAccountGrant(ctx, e.workspace, e.reader.ID, "acc-003"); err != nil {
					t.Error(err)
				}
			}
		}
		_, err := e.maker(t, e.receipts, revoke).Brief(ctx, e.brief("revoked-brief"))
		if !errors.Is(err, receipts.ErrNotPermitted) {
			t.Fatalf("the CLI card: %v", err)
		}
		if err := e.store.GrantAccount(ctx, e.workspace, e.reader.ID, "acc-003"); err != nil {
			t.Fatal(err)
		}
		_, err = e.maker(t, e.receipts, revoke).Prepare(ctx, e.prepare("revoked-prepare"))
		if !errors.Is(err, receipts.ErrNotPermitted) {
			t.Fatalf("the prepared brief: %v", err)
		}
		if n := e.count(t, "SELECT count(*) FROM cards"); n != 0 {
			t.Fatalf("%d cards", n)
		}
		if e.count(t, `SELECT count(*) FROM receipts WHERE (operation_id = 'revoked-brief/seal'
			OR operation_id = 'revoked-prepare') AND status = 'error' AND http_status = 403
			AND error = 'not_permitted'`) != 2 {
			t.Fatal("the seals were not finished as not permitted")
		}
	})
}

// A prepared brief on PostgreSQL: its reads are children of its call, which seals it, and it verifies.
func TestPreparedBriefOnPostgres(t *testing.T) {
	ctx := context.Background()
	e := newE2E(t)
	sealed, err := e.maker(t, e.receipts, nil).Prepare(ctx, e.prepare("prepared"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var stored []byte
	var parent, seal string
	if err := conn.QueryRow(ctx, `SELECT card_bytes, parent_receipt_id::text, seal_receipt_id::text FROM cards
		WHERE brief_id = $1`, sealed.BriefID).Scan(&stored, &parent, &seal); err != nil {
		t.Fatal(err)
	}
	if parent != sealed.Seal.AuditID || seal != parent || !bytes.Equal(stored, sealed.Payload) ||
		e.count(t, `SELECT count(*) FROM receipts WHERE parent_id = $1 AND brief_id = $2`, parent, sealed.BriefID) != 4 {
		t.Fatalf("published under %s, sealed by %s", parent, seal)
	}
	if _, err := VerifyPrepared(ctx, stored, sealed.BriefID, StoreLog{Store: e.receipts, WorkspaceID: e.workspace},
		demoSources(nil)); err != nil {
		t.Fatal(err)
	}
}
