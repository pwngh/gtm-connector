package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/receipts"
)

var (
	// ErrNotPermitted means the principal may not do or read this. Unknown and
	// ungranted things answer the same.
	ErrNotPermitted = errors.New("not permitted")
	// ErrProvenance: a unit cites a read or record the card may not rest on.
	ErrProvenance = errors.New("a unit cites a read the card may not rest on")
	// ErrProjection: the units and gaps are not exactly the card bytes' own.
	ErrProjection = errors.New("the units and gaps differ from the card's bytes")
	// ErrSealMismatch: the seal receipt is of another account or brief.
	ErrSealMismatch = errors.New("the seal receipt names another account or brief")
	// ErrPublishTimeout: the publication's transaction waited longer on a lock
	// or ran a statement longer than it may. Nothing was written.
	ErrPublishTimeout = errors.New("the publication waited too long on the database")
)

// A publication's transaction gives up on a lock after publishLockTimeout
// and on any one statement after publishStatementTimeout, so a transaction
// that another one blocks ends by itself and frees its connection. A
// publication is a few statements on rows only this card writes, which takes
// milliseconds: five seconds on a lock means its holder is stuck, and ten
// seconds leaves room for the commit checks of a card of the largest size.
// Both are under the card maker's own limit of fifteen seconds
// (cards.SettleLimit), so the database gives up first and says why. Each
// transaction that starts or finishes a receipt runs under the same two,
// which a single row's insert or update meets with the same room.
const (
	publishLockTimeout      = 5 * time.Second
	publishStatementTimeout = 10 * time.Second
)

// lockNotAvailable and queryCanceled are the SQLSTATEs of a lock timeout and
// of a statement timeout. A cancel request ends a statement with the second
// too, so it is a timeout only while the caller's context has not ended.
const (
	lockNotAvailable = "55P03"
	queryCanceled    = "57014"
)

// Card is a stored card: its bytes, their digest, and how it was sealed.
type Card struct {
	ID, WorkspaceID, AccountRef, BriefID string
	Kind                                 string // cli, prepared or go
	SchemaVersion                        int
	RulesVersion                         *string
	Bytes                                []byte
	SHA256                               []byte
	ParentReceiptID, SealReceiptID       string
	PublishedAt                          time.Time
	// LineCount is how many lines the card holds, nil for a card published before lines were projected.
	LineCount *int
}

// Unit is one numbered unit of a card, as projected from its sealed bytes.
type Unit struct {
	CardID         string
	N              int
	Kind           string // record or sentence
	RecordRef      string
	Tool           string
	Shown          string // sentence units: yes, cut, later, long or more
	ReceiptID      string // the read receipt, when bound by ID
	SourceRecordID string // the record version, when bound
	Body           json.RawMessage
}

// Gap is one stated gap of a card.
type Gap struct {
	Code, Disposition string
	Count             *int
	ByRole            json.RawMessage
	Text              string
}

// Line is one line a card prints under ATTENTION or RECORD VALUES, as data.
type Line struct {
	Section string          // signal or value
	Body    json.RawMessage // evidence.Line.Value
}

// CardPublication is everything step 3 writes together.
type CardPublication struct {
	WorkspaceID, AccountRef, BriefID string
	// ParentReceiptID is the brief's receipt, and is the seal: a card made
	// here is sealed by its own brief receipt (receipts.CheckSeal).
	ParentReceiptID string
	// PrincipalID, when set, must be the seal's principal. The actor is
	// always the seal's principal; a seal without one is the local owner's.
	PrincipalID string
	// The seal receipt, started (pending) before the card existed.
	SealReceiptID, SealLeaseToken string
	// Kind is "go": only cards made here are published until admission of
	// Python cards exists.
	Kind          string
	SchemaVersion int
	RulesVersion  *string
	// Bytes is the card. Its "units" and "gaps" arrays are exactly Units'
	// bodies and Gaps, and its evidence.receipts embeds exactly the reads in
	// ReadReceiptIDs, in order. Its text prints exactly Lines under ATTENTION
	// and RECORD VALUES, signals first.
	Bytes          []byte
	Units          []Unit
	Gaps           []Gap
	Lines          []Line
	ReadReceiptIDs []string
	ElapsedMS      float64
}

// PublishCard is step 3, one transaction. Under the locked seal it checks
// the seal (receipts.CheckSeal), the seal principal's live grant (or the
// owner when it has none), the bytes against everything the store holds for
// them (receipts.CheckPublication), and the generation of every read, holding
// share locks on the grant, the reads and their connections until commit.
// Then it writes the card bytes and digest, the unit, gap and line
// projections and the read set, and finishes the seal naming the digest. The database checks
// the same again at commit.
//
// A seal that cannot seal this card gives ErrSealMismatch and stays pending.
// A failed check writes no card, finishes the seal as an error and returns
// ErrNotPermitted (403), ErrSourceChanged (409), ErrProvenance or
// ErrProjection (422). A retry after a lost acknowledgement, by the holder of
// the seal's lease, returns the card.
//
// The transaction waits on a lock or runs one statement only so long
// (publishLockTimeout, publishStatementTimeout). Past either it writes
// nothing, leaves the seal pending and returns ErrPublishTimeout.
func (s *Store) PublishCard(ctx context.Context, in CardPublication) (Card, error) {
	return s.publishCard(ctx, in, nil)
}

// publishCard is PublishCard. held, when set, runs inside the transaction
// once the card is written and its seal finished, with the card and the seal
// as that transaction holds them; an error from it writes nothing. A caller
// that needs more of the stored rows reads them there, so nothing is left to
// fail once the commit has succeeded. held does not run for a retry, whose
// card an earlier transaction wrote.
func (s *Store) publishCard(ctx context.Context, in CardPublication,
	held func(tx pgx.Tx, card Card, seal Receipt) error) (Card, error) {
	if !validUUID(in.WorkspaceID) || !validUUID(in.SealReceiptID) || !validUUID(in.SealLeaseToken) {
		return Card{}, ErrNotFound
	}
	if in.Kind != "go" || in.ParentReceiptID != in.SealReceiptID {
		return Card{}, fmt.Errorf("%w: a card made here is of kind go, sealed by its own brief receipt",
			ErrSealMismatch)
	}
	digest := sha256.Sum256(in.Bytes)
	seal := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken, Status: "ok",
		HTTPStatus: 200, ElapsedMS: in.ElapsedMS,
		Rows: []string{"briefs/" + in.BriefID + "/" + hex.EncodeToString(digest[:])}}
	lineCount := len(in.Lines)
	card := Card{WorkspaceID: in.WorkspaceID, AccountRef: in.AccountRef, BriefID: in.BriefID, Kind: in.Kind,
		SchemaVersion: in.SchemaVersion, RulesVersion: in.RulesVersion, Bytes: in.Bytes, SHA256: digest[:],
		ParentReceiptID: in.ParentReceiptID, SealReceiptID: in.SealReceiptID, LineCount: &lineCount}
	var refused error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.bound(ctx, tx); err != nil {
			return err
		}
		var live bool
		sealed, err := scanReceiptWith(tx.QueryRow(ctx, `SELECT `+receiptColumns+`,
			r.lease_expires_at > clock_timestamp()
			FROM receipts r WHERE r.id = $1 AND r.workspace_id = $2 FOR UPDATE OF r`,
			in.SealReceiptID, in.WorkspaceID), &live)
		switch {
		case err != nil:
			return err
		case sealed.LeaseToken != in.SealLeaseToken:
			return ErrLeaseLost
		case sealed.Status != "pending":
			return errNotPending
		case !live:
			return ErrLeaseLost
		}
		view, err := converted(sealed, nil)
		if err != nil {
			return err
		}
		if err := receipts.CheckSeal(view, in.view(nil)); err != nil {
			return fmt.Errorf("%w: %w", ErrSealMismatch, err)
		}
		refused, err = s.recheck(ctx, tx, in, view)
		if err != nil {
			return err
		}
		if refused != nil {
			failure := ReceiptFinish{ReceiptID: in.SealReceiptID, LeaseToken: in.SealLeaseToken,
				Status: "error", HTTPStatus: 422, ElapsedMS: in.ElapsedMS, Error: "provenance"}
			switch {
			case errors.Is(refused, ErrNotPermitted):
				failure.HTTPStatus, failure.Error = 403, "not_permitted"
			case errors.Is(refused, ErrSourceChanged):
				failure.HTTPStatus, failure.Error = 409, "source_changed"
			case errors.Is(refused, ErrProjection):
				failure.Error = "projection"
			}
			_, err := finishLocked(ctx, tx, failure, false)
			return err
		}
		if err := s.at("publish:checked"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
				card_bytes, card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count,
				line_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, sha256($7), $8, $9, $10, $11, $12, $13)
			RETURNING id, published_at`,
			in.WorkspaceID, in.AccountRef, in.BriefID, in.Kind, in.SchemaVersion, in.RulesVersion,
			in.Bytes, in.ParentReceiptID, in.SealReceiptID, len(in.Units), len(in.Gaps), len(in.ReadReceiptIDs),
			lineCount).
			Scan(&card.ID, &card.PublishedAt); err != nil {
			return err
		}
		// One round trip for the projections and the read set.
		batch := &pgx.Batch{}
		for _, u := range in.Units {
			batch.Queue(`
				INSERT INTO evidence_units (workspace_id, card_id, n, kind, record_ref, tool, shown,
					receipt_id, source_record_id, body)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				in.WorkspaceID, card.ID, u.N, u.Kind, u.RecordRef, u.Tool, nullable(u.Shown),
				nullable(u.ReceiptID), nullable(u.SourceRecordID), u.Body)
		}
		for i, g := range in.Gaps {
			batch.Queue(`
				INSERT INTO gaps (workspace_id, card_id, ordinal, code, disposition, count, by_role, text)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				in.WorkspaceID, card.ID, i, g.Code, g.Disposition, g.Count, nullJSON(g.ByRole), g.Text)
		}
		for i, l := range in.Lines {
			batch.Queue(`INSERT INTO card_lines (workspace_id, card_id, ordinal, section, body)
				VALUES ($1, $2, $3, $4, $5)`, in.WorkspaceID, card.ID, i, l.Section, l.Body)
		}
		for i, id := range in.ReadReceiptIDs {
			batch.Queue(`INSERT INTO card_reads (workspace_id, card_id, ordinal, receipt_id) VALUES ($1, $2, $3, $4)`,
				in.WorkspaceID, card.ID, i, id)
		}
		results := tx.SendBatch(ctx, batch)
		for range batch.Len() {
			if _, err := results.Exec(); err != nil {
				results.Close()
				return fmt.Errorf("projections: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
		if err := s.at("publish:written"); err != nil {
			return err
		}
		finished, err := finishLocked(ctx, tx, seal, false)
		if err != nil {
			return err
		}
		if held != nil {
			if err := held(tx, card, finished); err != nil {
				return err
			}
		}
		return s.at("publish:sealed")
	})
	switch {
	case sqlState(err) == lockNotAvailable, sqlState(err) == queryCanceled && ctx.Err() == nil:
		return Card{}, ErrPublishTimeout
	case errors.Is(err, errNotPending):
		// A retry after a lost acknowledgement: the same bytes under this seal.
		published, lookupErr := s.cardBySeal(ctx, in.WorkspaceID, in.SealReceiptID)
		if lookupErr == nil && bytes.Equal(published.SHA256, digest[:]) {
			return published, nil
		}
		return Card{}, ErrAlreadyFinished
	case sqlState(err) == "GT009":
		return Card{}, fmt.Errorf("%w: %v", ErrProvenance, err)
	case err != nil:
		return Card{}, err
	case refused != nil:
		return Card{}, refused
	}
	return card, nil
}

// view is the publication as receipts' rules read it. versions gives each
// unit's content version by its record version ID.
func (in CardPublication) view(versions map[string]string) receipts.Publication {
	p := receipts.Publication{WorkspaceID: in.WorkspaceID, AccountRef: in.AccountRef, BriefID: in.BriefID,
		PrincipalID: in.PrincipalID, Kind: "card", Bytes: in.Bytes, Reads: in.ReadReceiptIDs}
	if payload, err := receipts.ParsePayload(in.Bytes); err == nil && payload.Prepared {
		p.Kind = "selected_brief"
	}
	for _, u := range in.Units {
		p.Units = append(p.Units, receipts.UnitRow{N: u.N, Kind: u.Kind, RecordRef: u.RecordRef, Tool: u.Tool,
			Shown: u.Shown, ReceiptID: u.ReceiptID, ContentVersion: versions[u.SourceRecordID], Value: u.Body})
	}
	for _, g := range in.Gaps {
		row := receipts.GapRow{Code: g.Code, Disposition: g.Disposition, Text: g.Text, ByRole: g.ByRole}
		if g.Count != nil {
			row.Count = big.NewInt(int64(*g.Count))
		}
		p.Gaps = append(p.Gaps, row)
	}
	for _, l := range in.Lines {
		p.Lines = append(p.Lines, receipts.LineRow{Section: l.Section, Value: l.Body})
	}
	return p
}

// recheck holds share locks on the actor's grant, the reads the bytes embed
// and their connections until commit, so neither a revocation nor a source
// change can land in between. It returns a refusal, or nil.
func (s *Store) recheck(ctx context.Context, tx pgx.Tx, in CardPublication, seal receipts.Receipt) (refusal, err error) {
	if in.PrincipalID != "" && seal.PrincipalID != in.PrincipalID {
		return ErrNotPermitted, nil
	}
	if seal.PrincipalID != "" {
		var granted bool
		err := tx.QueryRow(ctx, `
			SELECT true FROM account_grants g
			  JOIN principals p ON p.workspace_id = g.workspace_id AND p.id = g.principal_id
			 WHERE g.workspace_id = $1 AND g.principal_id = $2 AND g.account_ref = $3
			   AND g.revoked_at IS NULL AND p.disabled_at IS NULL
			   FOR SHARE OF g, p`, in.WorkspaceID, seal.PrincipalID, in.AccountRef).Scan(&granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if err := s.at("publish:granted"); err != nil {
		return nil, err
	}
	payload, err := receipts.ParsePayload(in.Bytes)
	if err != nil {
		return refusalOf(err), nil
	}
	var ids []string
	for _, id := range payload.ReadIDs {
		if validUUID(id) {
			ids = append(ids, id)
		}
	}
	reads, current := map[string]receipts.Receipt{}, map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT `+receiptColumns+`, c.generation = r.connection_generation
		  FROM receipts r JOIN connections c ON c.id = r.connection_id
		 WHERE r.id = ANY($1::uuid[]) AND r.workspace_id = $2
		   FOR SHARE OF r, c`, ids, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var fresh bool
		r, err := scanReceiptWith(rows, &fresh)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.LeaseToken = ""
		if reads[r.ID], err = converted(r, nil); err != nil {
			rows.Close()
			return nil, err
		}
		current[r.ID] = fresh
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	bound, versions, err := boundVersions(ctx, tx, in.WorkspaceID, ids)
	if err != nil {
		return nil, err
	}
	if err := receipts.CheckPublication(in.view(versions), payload, seal, reads, bound); err != nil {
		return refusalOf(err), nil
	}
	// Each unit's record version is the one its read bound, under its reference.
	cited := receipts.IndexBound(bound)
	for _, u := range in.Units {
		key := receipts.Citation{ReceiptID: u.ReceiptID, Ref: u.RecordRef, Version: versions[u.SourceRecordID]}
		if _, found := cited[key]; key.Version == "" || !found {
			return fmt.Errorf("%w: unit %d", ErrProvenance, u.N), nil
		}
	}
	for _, fresh := range current {
		if !fresh {
			return ErrSourceChanged, nil
		}
	}
	return nil, nil
}

// refusalOf is receipts' refusal in this package's terms.
func refusalOf(err error) error {
	if errors.Is(err, receipts.ErrProjection) {
		return fmt.Errorf("%w: %w", ErrProjection, err)
	}
	return fmt.Errorf("%w: %w", ErrProvenance, err)
}

// boundVersions gives, for each read, the versions it bound in order, and
// each version's content version by its record version ID.
func boundVersions(ctx context.Context, q querier, workspaceID string, reads []string) (
	map[string][]receipts.RecordVersion, map[string]string, error) {
	rows, err := q.Query(ctx, `
		SELECT b.receipt_id::text, v.id::text, v.source, v.record_table, v.record_id, v.content_version, v.raw_body
		  FROM receipt_records b JOIN source_records v ON v.workspace_id = b.workspace_id AND v.id = b.source_record_id
		 WHERE b.workspace_id = $1 AND b.receipt_id = ANY($2::uuid[])
		 ORDER BY b.receipt_id, b.ordinal`, workspaceID, reads)
	if err != nil {
		return nil, nil, err
	}
	bound, versions := map[string][]receipts.RecordVersion{}, map[string]string{}
	var receipt, id string
	var v receipts.RecordVersion
	_, err = pgx.ForEachRow(rows, []any{&receipt, &id, &v.Source, &v.Table, &v.RecordID, &v.ContentVersion, &v.Body},
		func() error {
			bound[receipt] = append(bound[receipt], receipts.RecordVersion{Source: v.Source, Table: v.Table,
				RecordID: v.RecordID, ContentVersion: v.ContentVersion, Body: append([]byte(nil), v.Body...)})
			versions[id] = v.ContentVersion
			return nil
		})
	return bound, versions, err
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

const cardColumns = `c.id, c.workspace_id, c.account_ref, c.brief_id, c.kind, c.schema_version,
	c.rules_version, c.card_bytes, c.card_sha256, c.parent_receipt_id, c.seal_receipt_id, c.published_at,
	c.line_count`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.AccountRef, &c.BriefID, &c.Kind, &c.SchemaVersion,
		&c.RulesVersion, &c.Bytes, &c.SHA256, &c.ParentReceiptID, &c.SealReceiptID, &c.PublishedAt, &c.LineCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, ErrNotFound
	}
	return c, err
}

func (s *Store) cardBySeal(ctx context.Context, workspaceID, sealID string) (Card, error) {
	return scanCard(s.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards c
		WHERE c.workspace_id = $1 AND c.seal_receipt_id = $2`, workspaceID, sealID))
}

// Card reads one card with its bytes, for the owner.
func (s *Store) Card(ctx context.Context, workspaceID, cardID string) (Card, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return Card{}, ErrNotFound
	}
	return scanCard(s.pool.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM cards c WHERE c.workspace_id = $1 AND c.id = $2`, workspaceID, cardID))
}

const unitColumns = `u.card_id, u.n, u.kind, u.record_ref, u.tool, coalesce(u.shown, ''),
	coalesce(u.receipt_id::text, ''), coalesce(u.source_record_id::text, ''), u.body`

func scanUnit(row pgx.Row) (Unit, error) {
	var u Unit
	err := row.Scan(&u.CardID, &u.N, &u.Kind, &u.RecordRef, &u.Tool, &u.Shown, &u.ReceiptID,
		&u.SourceRecordID, &u.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Unit{}, ErrNotFound
	}
	return u, err
}

// Units lists a card's units in number order, for the owner.
func (s *Store) Units(ctx context.Context, workspaceID, cardID string) ([]Unit, error) {
	if !validUUID(workspaceID) || !validUUID(cardID) {
		return nil, ErrNotFound
	}
	return units(ctx, s.pool, workspaceID, cardID)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func units(ctx context.Context, q querier, workspaceID, cardID string) ([]Unit, error) {
	rows, err := q.Query(ctx, `SELECT `+unitColumns+` FROM evidence_units u
		WHERE u.workspace_id = $1 AND u.card_id = $2 ORDER BY u.n`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Unit, error) { return scanUnit(row) })
}

func gaps(ctx context.Context, q querier, workspaceID, cardID string) ([]Gap, error) {
	rows, err := q.Query(ctx, `SELECT code, disposition, count, by_role, text FROM gaps
		WHERE workspace_id = $1 AND card_id = $2 ORDER BY ordinal`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Gap, error) {
		var g Gap
		err := row.Scan(&g.Code, &g.Disposition, &g.Count, &g.ByRole, &g.Text)
		return g, err
	})
}

// lines lists a card's lines in order.
func lines(ctx context.Context, q querier, workspaceID, cardID string) ([]Line, error) {
	rows, err := q.Query(ctx, `SELECT section, body FROM card_lines
		WHERE workspace_id = $1 AND card_id = $2 ORDER BY ordinal`, workspaceID, cardID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Line, error) {
		var l Line
		err := row.Scan(&l.Section, &l.Body)
		return l, err
	})
}
