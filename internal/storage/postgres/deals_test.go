package postgres_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// linkCard binds a card to a deal as any writer could, with SQL.
func linkCard(t *testing.T, f *postgrestest.Fixture, card, deal, account string) error {
	t.Helper()
	return sql(t, f, `INSERT INTO deal_cards (workspace_id, card_id, deal_ref, account_ref) VALUES ($1, $2, $3, $4)`,
		f.Workspace.ID, card, deal, account)
}

// The database keeps a deal's rules for any writer: it binds only what its two reads returned, for its own
// account and by the principal that registered it; it only closes, once; it is never deleted; and a card
// joins only an open deal of its own account, and only one published while that deal was open.
func TestDealRulesHoldForAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	accountRead := postgrestest.Read(t, f, "get_account", postgrestest.Account, "",
		postgrestest.Record("accounts", postgrestest.Account, "id", postgrestest.Account))
	opportunities := postgrestest.Read(t, f, "list_opportunities", postgrestest.Account, "",
		postgrestest.Record("opportunities", "opp-1", "id", "opp-1"))
	register := func(change func(*postgres.DealRegistration)) (postgres.Deal, error) {
		in := postgres.DealRegistration{WorkspaceID: f.Workspace.ID, AccountRef: postgrestest.Account, Word: "invented",
			Year: 2027, OpportunityRef: "opp-1", AccountReadID: accountRead.ID, OpportunitiesReadID: opportunities.ID}
		change(&in)
		return f.Store.RegisterDeal(ctx, in)
	}
	for name, change := range map[string]func(*postgres.DealRegistration){
		"an opportunity the read did not return": func(in *postgres.DealRegistration) { in.OpportunityRef = "opp-2" },
		"a contract the read did not return":     func(in *postgres.DealRegistration) { in.ContractRef = "ctr-1" },
		"the reads in each other's places": func(in *postgres.DealRegistration) {
			in.AccountReadID, in.OpportunitiesReadID = opportunities.ID, accountRead.ID
		},
		"reads made by another principal": func(in *postgres.DealRegistration) { in.PrincipalID = f.Reader.ID },
	} {
		if _, err := register(change); !errors.Is(err, postgres.ErrDealBindings) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A principal without a grant on the account registers nothing, whatever its reads returned.
	if _, err := register(func(in *postgres.DealRegistration) { in.PrincipalID = f.Outsider.ID }); !errors.Is(err,
		postgres.ErrNotPermitted) {
		t.Errorf("an ungranted principal: %v", err)
	}
	deal, err := register(func(*postgres.DealRegistration) {})
	if err != nil {
		t.Fatal(err)
	}
	if deal.Ref != "invented-2027-2e3hke" {
		t.Errorf("the deal is %s", deal.Ref)
	}
	if _, err := register(func(*postgres.DealRegistration) {}); !errors.Is(err, postgres.ErrDealOpen) {
		t.Errorf("a second open deal: %v", err)
	}

	for _, statement := range []string{
		`UPDATE deals SET renews_on = '2028-01-01'`,
		`UPDATE deals SET state = 'closed', closed_at = now(), opportunity_ref = NULL`,
		`DELETE FROM deals`,
		`TRUNCATE deals CASCADE`,
	} {
		if err := sql(t, f, statement); err == nil {
			t.Errorf("%s was allowed", statement)
		}
	}
	// The fixture's card was published before the deal opened, and a card of another account is not the deal's.
	if err := linkCard(t, f, f.Card.ID, deal.Ref, postgrestest.Account); err == nil {
		t.Errorf("a card published before the deal opened joined it")
	}
	other := postgrestest.Publish(t, f, "acc-004", f.Reader.ID, "brief-other")
	if err := linkCard(t, f, other.Card.ID, deal.Ref, "acc-004"); err == nil {
		t.Errorf("a card of another account joined the deal")
	}
	during := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-during")
	if err := linkCard(t, f, during.Card.ID, deal.Ref, postgrestest.Account); err != nil {
		t.Errorf("a card published while the deal was open: %v", err)
	}
	closed, changed, err := f.Store.CloseDeal(ctx, postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref,
		Reason: "Renewed"})
	if err != nil || !changed {
		t.Fatalf("close: %v %v", changed, err)
	}
	if closed.Close == nil || closed.Close.FinalCard != during.Card.ID {
		t.Errorf("the close names %+v, and the deal's newest card is %s", closed.Close, during.Card.ID)
	}
	if err := sql(t, f, `UPDATE deals SET state = 'open', closed_at = NULL`); err == nil {
		t.Errorf("a closed deal opened again")
	}
	after := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-after")
	if err := linkCard(t, f, after.Card.ID, deal.Ref, postgrestest.Account); err == nil {
		t.Errorf("a card joined a closed deal")
	}
	// The account's next deal of the year takes the next number.
	if next := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented"); next.Ref != "invented-2027-2e3hke-2" {
		t.Errorf("the next deal is %s", next.Ref)
	}
}

// A deal's ID depends on its own account alone: an account whose name starts with the same word gets the
// same ID whether or not the other account has a deal, so a person granted one learns nothing of the other.
func TestADealIDNamesNoOtherAccount(t *testing.T) {
	ids := map[bool]string{}
	for _, beside := range []bool{false, true} {
		f := postgrestest.New(t)
		if err := f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Invented account"); err != nil {
			t.Fatal(err)
		}
		if beside {
			postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
		}
		ids[beside] = postgrestest.RegisterDeal(t, f, "acc-004", "", "invented").Ref
	}
	if ids[false] != "invented-2027-3fag4m" || ids[true] != ids[false] {
		t.Errorf("acc-004's deal is %s alone and %s beside acc-003's", ids[false], ids[true])
	}
}

// A restore runs the deal rules on the rows it loads: a card bound to a deal it was published after the
// deal closed is refused.
func TestARestoreRefusesACardOutsideItsDeal(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	if _, _, err := f.Store.CloseDeal(ctx, postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref,
		Reason: "Renewed"}); err != nil {
		t.Fatal(err)
	}
	after := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-after")
	ok(t, sql(t, f, `ALTER TABLE deal_cards DISABLE TRIGGER USER`))
	ok(t, linkCard(t, f, after.Card.ID, deal.Ref, postgrestest.Account))
	ok(t, sql(t, f, `ALTER TABLE deal_cards ENABLE TRIGGER USER`))
	files := dump(t, f)
	fresh, _ := freshStoreURL(t)
	err := fresh.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data))
	if err == nil || !strings.Contains(err.Error(), "outside the time deal") {
		t.Errorf("the restore: %v", err)
	}
}

// closeReceipt is a close_deal receipt that finished ok for the deal's account, by the owner, naming the
// deal, the reason and the final card ("" for none), as CloseDeal writes one.
func closeReceipt(t *testing.T, f *postgrestest.Fixture, deal postgres.Deal, reason, final string) postgres.Receipt {
	t.Helper()
	named := map[string]any{"deal": deal.Ref, "basis": "person", "reason": reason, "final_card": nil}
	if final != "" {
		named["final_card"] = final
	}
	arguments, err := json.Marshal(named)
	ok(t, err)
	started, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "close-" + time.Now().Format("150405.000000000"), TokenID: postgres.TokenID(""),
		Tool: "close_deal", AccountRef: deal.AccountRef, Arguments: arguments, RequestID: json.RawMessage(`1`),
		Lease: time.Minute})
	ok(t, err)
	finished, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: started.ID,
		LeaseToken: started.LeaseToken, Status: "ok", HTTPStatus: 200})
	ok(t, err)
	return finished
}

// receiptNaming is a close_deal receipt that finished ok for the account, by the owner, with these arguments.
func receiptNaming(t *testing.T, f *postgrestest.Fixture, account, arguments string) postgres.Receipt {
	t.Helper()
	started, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "close-" + time.Now().Format("150405.000000000"), TokenID: postgres.TokenID(""),
		Tool: "close_deal", AccountRef: account, Arguments: json.RawMessage(arguments), RequestID: json.RawMessage(`1`),
		Lease: time.Minute})
	ok(t, err)
	finished, err := f.Store.FinishReceipt(ctx, postgres.ReceiptFinish{ReceiptID: started.ID,
		LeaseToken: started.LeaseToken, Status: "ok", HTTPStatus: 200})
	ok(t, err)
	return finished
}

// closeWith closes a deal as any writer could, with SQL.
func closeWith(t *testing.T, f *postgrestest.Fixture, deal postgres.Deal, basis, reason, receipt, final any) error {
	t.Helper()
	return sql(t, f, `UPDATE deals SET state = 'closed', closed_at = '2020-01-01', close_basis = $3,
		close_reason = $4, close_receipt_id = $5, final_card_id = $6 WHERE workspace_id = $1 AND ref = $2`,
		f.Workspace.ID, deal.Ref, basis, reason, receipt, final)
}

// The database keeps a close's rules for any writer: a deal closes only under a close_deal receipt that
// finished ok for its account and names the deal, the reason and the final card; the final card is the
// deal's newest; one receipt closes one deal; and the time is the database's.
func TestADealClosesOnlyAsItsReceiptNames(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	first := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-first")
	ok(t, linkCard(t, f, first.Card.ID, deal.Ref, postgrestest.Account))
	second := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-second")
	ok(t, linkCard(t, f, second.Card.ID, deal.Ref, postgrestest.Account))
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Invented account"))
	other := postgrestest.RegisterDeal(t, f, "acc-004", "", "other")

	read := postgrestest.Read(t, f, "get_account", postgrestest.Account, "")
	onFirst := closeReceipt(t, f, deal, "Renewed", first.Card.ID)
	onNone := closeReceipt(t, f, deal, "Renewed", "")
	ofOther := closeReceipt(t, f, other, "Renewed", "")
	pending, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "close-pending", TokenID: postgres.TokenID(""), Tool: "close_deal", AccountRef: deal.AccountRef,
		Arguments: json.RawMessage(`{"deal":"` + deal.Ref + `","basis":"person","reason":"Renewed","final_card":"` + second.Card.ID + `"}`),
		RequestID: json.RawMessage(`1`), Lease: time.Minute})
	ok(t, err)
	good := closeReceipt(t, f, deal, "Renewed", second.Card.ID)

	// A receipt names the close in one shape: the four members, each of its own type. One whose reason is the
	// JSON true does not name the reason "true", and one with no final_card member does not name "none".
	truthy := receiptNaming(t, f, deal.AccountRef, `{"deal":"`+deal.Ref+`","basis":"person","reason":true,"final_card":"`+second.Card.ID+`"}`)
	short := receiptNaming(t, f, other.AccountRef, `{"deal":"`+other.Ref+`","basis":"person","reason":"Renewed"}`)
	extra := receiptNaming(t, f, other.AccountRef, `{"deal":"`+other.Ref+`","basis":"person","reason":"Renewed","final_card":null,"more":1}`)
	// A reason the command would refuse is refused for any writer, also under a receipt that names it exactly.
	for label, reason := range map[string]string{"two lines": "Renewed\nagain", "501 characters": strings.Repeat("r", 501),
		"a space before it": " Renewed", "a no-break space alone": "\u00a0", "a no-break space after it": "Renewed\u00a0",
		"a line separator": "Renewed\u2028again", "a next-line character": "Renewed\u0085again", "a tab": "Renewed\tagain"} {
		named := closeReceipt(t, f, other, reason, "")
		if err := closeWith(t, f, other, "person", reason, named.ID, nil); err == nil {
			t.Errorf("a deal closed with a reason of %s", label)
		}
	}

	for label, refused := range map[string]error{
		"under a receipt whose reason is true":      closeWith(t, f, deal, "person", "true", truthy.ID, second.Card.ID),
		"under a receipt with no final card member": closeWith(t, f, other, "person", "Renewed", short.ID, nil),
		"under a receipt with a fifth member":       closeWith(t, f, other, "person", "Renewed", extra.ID, nil),
		"with no record of how":                     closeWith(t, f, deal, nil, nil, nil, nil),
		"as unrecorded":                             closeWith(t, f, deal, "unrecorded", nil, nil, nil),
		"with no receipt":                           closeWith(t, f, deal, "person", "Renewed", nil, second.Card.ID),
		"with no reason":                            closeWith(t, f, deal, "person", nil, good.ID, second.Card.ID),
		"under a read's receipt":                    closeWith(t, f, deal, "person", "Renewed", read.ID, second.Card.ID),
		"under a receipt still pending":             closeWith(t, f, deal, "person", "Renewed", pending.ID, second.Card.ID),
		"under another deal's receipt":              closeWith(t, f, deal, "person", "Renewed", ofOther.ID, nil),
		"for another reason than named":             closeWith(t, f, deal, "person", "Lost", good.ID, second.Card.ID),
		"on another card than named":                closeWith(t, f, deal, "person", "Renewed", good.ID, first.Card.ID),
		"on a card that is not its newest":          closeWith(t, f, deal, "person", "Renewed", onFirst.ID, first.Card.ID),
		"on no card while it has cards":             closeWith(t, f, deal, "person", "Renewed", onNone.ID, nil),
		"on a card of no deal":                      closeWith(t, f, deal, "person", "Renewed", good.ID, f.Card.ID),
	} {
		if refused == nil {
			t.Errorf("a deal closed %s", label)
		}
	}
	if n := count(t, f, `SELECT count(*) FROM deals WHERE state = 'closed'`); n != 0 {
		t.Fatalf("%d deals are closed after every refusal", n)
	}

	before := time.Now().Add(-time.Minute)
	ok(t, closeWith(t, f, deal, "person", "Renewed", good.ID, second.Card.ID))
	stored, err := f.Store.Deal(ctx, f.Workspace.ID, deal.Ref)
	ok(t, err)
	if stored.ClosedAt == nil || stored.ClosedAt.Before(before) {
		t.Errorf("the deal closed at %v, a time the writer gave", stored.ClosedAt)
	}
	// The receipt that closed one deal closes no other, and a closed deal's close does not change.
	if err := closeWith(t, f, other, "person", "Renewed", good.ID, nil); err == nil {
		t.Errorf("one receipt closed two deals")
	}
	if err := sql(t, f, `UPDATE deals SET close_reason = 'Lost' WHERE ref = $1`, deal.Ref); err == nil {
		t.Errorf("a closed deal's reason changed")
	}
	if err := sql(t, f, `UPDATE deals SET close_reason = 'Lost' WHERE ref = $1`, other.Ref); err == nil {
		t.Errorf("an open deal took a reason")
	}
}

// CloseDeal closes a deal for the owner or a granted person under a close_deal receipt that names the
// reason and the deal's newest card. A person with no grant and an unknown deal are refused alike, under a
// receipt that names no account. A closed deal is given as it is, with no new receipt.
func TestCloseDealRecordsWhoWhyAndTheFinalCard(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	card := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-during")
	ok(t, linkCard(t, f, card.Card.ID, deal.Ref, postgrestest.Account))
	receipts := func(status string) int {
		return count(t, f, `SELECT count(*) FROM receipts WHERE tool = 'close_deal' AND status = $1`, status)
	}
	closing := postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref, Reason: "  Renewed for two years "}

	for _, reason := range []string{"", "   ", "two\nlines", strings.Repeat("r", 501), "bell\a"} {
		in := closing
		in.Reason = reason
		if _, _, err := f.Store.CloseDeal(ctx, in); !errors.Is(err, postgres.ErrCloseReason) {
			t.Errorf("the reason %q: %v", reason, err)
		}
	}
	// A person with no grant on the account, and any person for an unknown deal, get the same refusal.
	stranger := closing
	stranger.PrincipalID = f.Outsider.ID
	unknown := stranger
	unknown.Ref = "nothing-2027-aaaaaa"
	for label, in := range map[string]postgres.DealClose{"an ungranted person": stranger, "an unknown deal": unknown} {
		if _, _, err := f.Store.CloseDeal(ctx, in); !errors.Is(err, postgres.ErrNotPermitted) {
			t.Errorf("%s: %v", label, err)
		}
	}
	missing := closing
	missing.Ref = "nothing-2027-aaaaaa"
	if _, _, err := f.Store.CloseDeal(ctx, missing); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("the owner closing an unknown deal: %v", err)
	}
	if n := count(t, f, `SELECT count(*) FROM receipts WHERE tool = 'close_deal' AND status = 'error'
		AND http_status = 403 AND error = 'not_permitted' AND account_ref IS NULL`); n != 3 {
		t.Errorf("%d refusals have a receipt that names no account, of 3", n)
	}
	if n := count(t, f, `SELECT count(*) FROM deals WHERE state = 'closed'`); n != 0 || receipts("ok") != 0 {
		t.Fatalf("%d deals closed and %d closes finished ok before any was allowed", n, receipts("ok"))
	}

	reader := closing
	reader.PrincipalID = f.Reader.ID
	closed, changed, err := f.Store.CloseDeal(ctx, reader)
	if err != nil || !changed {
		t.Fatalf("close: %v %v", changed, err)
	}
	how := closed.Close
	if closed.State != "closed" || how == nil || how.Basis != "person" || how.Reason != "Renewed for two years" ||
		how.By != f.Reader.ID || how.FinalCard != card.Card.ID {
		t.Fatalf("the closed deal: %+v %+v", closed, how)
	}
	if n := count(t, f, `SELECT count(*) FROM receipts WHERE id = $1 AND tool = 'close_deal' AND status = 'ok'
		AND principal_id = $2 AND account_ref = $3 AND arguments::jsonb = jsonb_build_object('deal', $4::text,
		'basis', 'person', 'reason', 'Renewed for two years', 'final_card', $5::text)`,
		how.ReceiptID, f.Reader.ID, postgrestest.Account, deal.Ref, card.Card.ID); n != 1 {
		t.Errorf("the close's receipt does not name who, the deal, the reason and the final card")
	}
	// Closing it again gives it as it is, under no new receipt.
	again, changed, err := f.Store.CloseDeal(ctx, closing)
	if err != nil || changed || again.Close.ReceiptID != how.ReceiptID || receipts("ok") != 1 {
		t.Errorf("closing a closed deal: %v %v %+v, %d receipts", changed, err, again.Close, receipts("ok"))
	}
	// A card published for the deal after the close chose its final card stops the close: its receipt is
	// finished as an error, and the deal stays open for the next try, which closes on the newer card.
	racing := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	var late postgrestest.Published
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "close:started" && late.Card.ID == "" {
			late = postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-late")
			return linkCard(t, f, late.Card.ID, racing.Ref, postgrestest.Account)
		}
		return nil
	})
	raced := postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: racing.Ref, Reason: "Renewed"}
	if _, _, err := f.Store.CloseDeal(ctx, raced); !errors.Is(err, postgres.ErrDealChanged) {
		t.Errorf("a close that a newer card overtook: %v", err)
	}
	if n := count(t, f, `SELECT count(*) FROM receipts WHERE tool = 'close_deal' AND status = 'error'
		AND http_status = 409 AND error = 'deal_changed'`); n != 1 {
		t.Errorf("%d receipts say the deal changed, of 1", n)
	}
	retried, changed, err := f.Store.CloseDeal(ctx, raced)
	if err != nil || !changed || retried.Close.FinalCard != late.Card.ID {
		t.Errorf("the close tried again: %v %v %+v", changed, err, retried.Close)
	}
	postgres.SetCrash(f.Store, nil)
	// A deal with no card closes on none.
	next := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	bare, _, err := f.Store.CloseDeal(ctx, postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: next.Ref,
		Reason: "Perdu, décidé à 100\u00a0%"})
	if err != nil || bare.Close == nil || bare.Close.FinalCard != "" || bare.Close.By != "" {
		t.Errorf("a deal with no card: %v %+v", err, bare.Close)
	}
	if n := count(t, f, `SELECT count(*) FROM gtm_integrity_violations()`); n != 0 {
		t.Errorf("the integrity check finds %d violations", n)
	}
}

// A restore loads deals with their triggers off, so its check refuses a close that its receipt does not name.
func TestARestoreRefusesACloseItsReceiptDoesNotName(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	receipt := closeReceipt(t, f, deal, "Renewed", "")
	ok(t, sql(t, f, `ALTER TABLE deals DISABLE TRIGGER USER`))
	ok(t, closeWith(t, f, deal, "person", "Lost", receipt.ID, nil))
	ok(t, sql(t, f, `ALTER TABLE deals ENABLE TRIGGER USER`))
	files := dump(t, f)
	fresh, _ := freshStoreURL(t)
	err := fresh.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data))
	if err == nil || !strings.Contains(err.Error(), "is not closed as its receipt names") {
		t.Errorf("the restore: %v", err)
	}
}

// A deal closed before closes were recorded is marked unrecorded by the migration, and an open one is left.
func TestTheCloseMigrationMarksEarlierClosesUnrecorded(t *testing.T) {
	before := postgres.MigrationVersion("a_deal_closes_with_a_reason_and_a_final_card") - 1
	if before < 13 {
		t.Fatalf("the close migration is numbered %d", before+1)
	}
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	ok(t, postgres.MigrateTo(store, ctx, before))
	// The rows are loaded as a restore loads them, with triggers off: only the deals matter here.
	deal := func(ref, account, state, closed string) string {
		return `INSERT INTO deals (workspace_id, ref, account_ref, state, account_read_id, opportunities_read_id,
			closed_at) VALUES ('11111111-1111-4111-8111-111111111111', '` + ref + `', '` + account + `', '` + state + `',
			gen_random_uuid(), gen_random_uuid(), ` + closed + `)`
	}
	exec(t, url, `SET session_replication_role = replica`,
		deal("old-2026-aaaaaa", "acc-001", "closed", "now()"), deal("new-2027-aaaaaa", "acc-001", "open", "NULL"))
	ok(t, store.Migrate(ctx))
	if got := scalar(t, url, `SELECT string_agg(ref || ' ' || coalesce(close_basis, 'none'), ', ' ORDER BY ref) FROM deals`); got !=
		"new-2027-aaaaaa none, old-2026-aaaaaa unrecorded" {
		t.Errorf("after the migration: %s", got)
	}
	if got := scalar(t, url, `SELECT count(*)::text FROM gtm_integrity_violations() v WHERE v LIKE '%closed%'`); got != "0" {
		t.Errorf("%s close violations for a deal closed before closes were recorded", got)
	}
}

// A close made in a transaction whose snapshot is pinned could miss a card bound to the deal since, and name
// a final card that is not the newest. The database refuses a close there, whatever it names.
func TestACloseUnderAPinnedSnapshotIsRefused(t *testing.T) {
	for _, level := range []string{"REPEATABLE READ", "SERIALIZABLE"} {
		f := postgrestest.New(t)
		deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
		first := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-first")
		ok(t, linkCard(t, f, first.Card.ID, deal.Ref, postgrestest.Account))
		onFirst := closeReceipt(t, f, deal, "Renewed", first.Card.ID)

		conn, err := pgx.Connect(ctx, f.URL)
		ok(t, err)
		defer conn.Close(ctx)
		_, err = conn.Exec(ctx, "BEGIN ISOLATION LEVEL "+level)
		ok(t, err)
		var deals int
		ok(t, conn.QueryRow(ctx, "SELECT count(*) FROM deals").Scan(&deals))
		// Another writer binds a newer card, which this transaction's snapshot does not hold.
		second := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-second")
		ok(t, linkCard(t, f, second.Card.ID, deal.Ref, postgrestest.Account))
		_, err = conn.Exec(ctx, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'person',
			close_reason = 'Renewed', close_receipt_id = $3, final_card_id = $4 WHERE workspace_id = $1 AND ref = $2`,
			f.Workspace.ID, deal.Ref, onFirst.ID, first.Card.ID)
		if err == nil {
			_, err = conn.Exec(ctx, "COMMIT")
		}
		if err == nil {
			t.Errorf("%s: a deal closed on a card that was not its newest", level)
		}
	}
}

// A close on a card that is no card of the deal is refused by a restore, and its check names it also where
// the key that holds the final card to its deal was off as the row was written.
func TestARestoreRefusesACloseOnACardOfNoDeal(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	receipt := closeReceipt(t, f, deal, "Renewed", f.Card.ID)
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `SET session_replication_role = replica`)
	ok(t, err)
	_, err = conn.Exec(ctx, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'person',
		close_reason = 'Renewed', close_receipt_id = $3, final_card_id = $4 WHERE workspace_id = $1 AND ref = $2`,
		f.Workspace.ID, deal.Ref, receipt.ID, f.Card.ID)
	ok(t, err)
	if n := count(t, f, `SELECT count(*) FROM gtm_integrity_violations() v
		WHERE v LIKE '%closed on a card that is not its own'`); n != 1 {
		t.Errorf("the integrity check names %d closes on a card of no deal, of 1", n)
	}
	files := dump(t, f)
	fresh, _ := freshStoreURL(t)
	if err := fresh.Restore(ctx, strings.Repeat("e", 64), files.applied, files.copies, opener(files.data)); err == nil {
		t.Errorf("the restore took a close on a card of no deal")
	}
}

// A close that names the card it made closes only on that card, and only a deal still open: a deal that got
// a newer card, or that another close closed, is left as it is under a receipt finished as error/409. A
// grant that ends while the close runs stops it under a receipt finished as error/403.
func TestACloseLosesToWhatChangedTheDeal(t *testing.T) {
	f := postgrestest.New(t)
	refused := func(status int, code string) int {
		return count(t, f, `SELECT count(*) FROM receipts WHERE tool = 'close_deal' AND status = 'error'
			AND http_status = $1 AND error = $2 AND account_ref = $3`, status, code, postgrestest.Account)
	}
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	made := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-made")
	ok(t, linkCard(t, f, made.Card.ID, deal.Ref, postgrestest.Account))
	newer := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-newer")
	ok(t, linkCard(t, f, newer.Card.ID, deal.Ref, postgrestest.Account))
	closing := postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref, Reason: "Renewed", Card: made.Card.ID}

	// Another card was published for the deal after the one this close made.
	if _, _, err := f.Store.CloseDeal(ctx, closing); !errors.Is(err, postgres.ErrDealChanged) || refused(409, "deal_changed") != 1 {
		t.Fatalf("a close on a card that is no longer the newest: %v, %d receipts", err, refused(409, "deal_changed"))
	}
	// The grant ends between the first check and the close.
	reader := closing
	reader.PrincipalID, reader.Card = f.Reader.ID, newer.Card.ID
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "close:started" {
			return sql(t, f, `UPDATE account_grants SET revoked_at = now() WHERE principal_id = $1`, f.Reader.ID)
		}
		return nil
	})
	if _, _, err := f.Store.CloseDeal(ctx, reader); !errors.Is(err, postgres.ErrNotPermitted) || refused(403, "not_permitted") != 1 {
		t.Fatalf("a close whose grant ended while it ran: %v, %d receipts", err, refused(403, "not_permitted"))
	}
	// Another close wins while this one runs.
	other := postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref, Reason: "Lost"}
	won := false
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "close:started" && !won {
			won = true
			_, _, err := f.Store.CloseDeal(ctx, other)
			return err
		}
		return nil
	})
	closing.Card = newer.Card.ID
	if _, _, err := f.Store.CloseDeal(ctx, closing); !errors.Is(err, postgres.ErrDealChanged) || refused(409, "deal_changed") != 2 {
		t.Fatalf("a close that lost to another: %v, %d receipts", err, refused(409, "deal_changed"))
	}
	postgres.SetCrash(f.Store, nil)
	stored, err := f.Store.Deal(ctx, f.Workspace.ID, deal.Ref)
	ok(t, err)
	if stored.Close == nil || stored.Close.Reason != "Lost" || stored.Close.FinalCard != newer.Card.ID {
		t.Errorf("the deal closed as %+v", stored.Close)
	}
	// A close that names its card finds the deal closed already, and says so: it is not given as closed by it.
	if _, _, err := f.Store.CloseDeal(ctx, closing); !errors.Is(err, postgres.ErrDealChanged) || refused(409, "deal_changed") != 3 {
		t.Errorf("a close that names its card, of a closed deal: %v, %d receipts", err, refused(409, "deal_changed"))
	}
}

// The database keeps the rules of a close on the CRM's stage for any writer: it has an outcome, no reason and
// no person; its receipt names the deal, the opportunity and the outcome too; and its final card rests on a
// read of the deal's opportunity. A person's close has no outcome.
func TestACloseOnTheCRMsStageHoldsForAnyWriter(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	card := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-during")
	ok(t, linkCard(t, f, card.Card.ID, deal.Ref, postgrestest.Account))
	stage := func(outcome any, reason, receipt any) error {
		return sql(t, f, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'crm_stage',
			close_reason = $3, close_receipt_id = $4, final_card_id = $5, close_outcome = $6
			WHERE workspace_id = $1 AND ref = $2`, f.Workspace.ID, deal.Ref, reason, receipt, card.Card.ID, outcome)
	}
	named := func(members string) postgres.Receipt {
		return receiptNaming(t, f, deal.AccountRef, `{"deal":"`+deal.Ref+`","basis":"crm_stage","reason":null,"final_card":"`+
			card.Card.ID+`"`+members+`}`)
	}
	whole := named(`,"opportunity":"opp-1","outcome":"won"`)
	for label, refused := range map[string]error{
		"with no outcome":                             stage(nil, nil, whole.ID),
		"with an outcome that is neither":             stage("open", nil, whole.ID),
		"with a reason":                               stage("won", "Renewed", whole.ID),
		"with another outcome than its receipt names": stage("lost", nil, whole.ID),
		"under a receipt that names no opportunity":   stage("won", nil, named(`,"outcome":"won"`).ID),
		"under a receipt that names another":          stage("won", nil, named(`,"opportunity":"opp-2","outcome":"won"`).ID),
		"on a card that did not read its opportunity": stage("won", nil, whole.ID),
	} {
		if refused == nil {
			t.Errorf("a deal closed on the stage %s", label)
		}
	}
	if err := stage("won", nil, whole.ID); err == nil || !strings.Contains(err.Error(), "did not read its opportunity") {
		t.Errorf("a close whose receipt is whole, on a card that read no opportunity: %v", err)
	}
	// A card that read opportunities, and not the deal's, closes nothing on the stage; one that read the
	// deal's does.
	another := postgrestest.PublishReading(t, f, "list_opportunities", postgrestest.Account, f.Reader.ID, "brief-another",
		postgrestest.Record("opportunities", "opp-2", "id", "opp-2", "account_id", postgrestest.Account))
	ok(t, linkCard(t, f, another.Card.ID, deal.Ref, postgrestest.Account))
	on := func(card string) error {
		receipt := receiptNaming(t, f, deal.AccountRef, `{"deal":"`+deal.Ref+`","basis":"crm_stage","reason":null,"final_card":"`+
			card+`","opportunity":"opp-1","outcome":"won"}`)
		return sql(t, f, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'crm_stage',
			close_receipt_id = $3, final_card_id = $4, close_outcome = 'won' WHERE workspace_id = $1 AND ref = $2`,
			f.Workspace.ID, deal.Ref, receipt.ID, card)
	}
	if err := on(another.Card.ID); err == nil || !strings.Contains(err.Error(), "did not read its opportunity") {
		t.Errorf("a close on a card that read another opportunity: %v", err)
	}
	// An open deal holds no part of a close: a deal registered with a reason is refused, and the same deal
	// without one is taken.
	ok(t, f.Store.EnsureAccount(ctx, f.Workspace.ID, "acc-004", "Invented account"))
	accountRead := postgrestest.Read(t, f, "get_account", "acc-004", "",
		postgrestest.Record("accounts", "acc-004", "id", "acc-004", "name", "Invented account"))
	opportunities := postgrestest.Read(t, f, "list_opportunities", "acc-004", "")
	register := func(reason any) error {
		return sql(t, f, `INSERT INTO deals (workspace_id, ref, account_ref, account_read_id, opportunities_read_id,
			close_reason) VALUES ($1, 'raw-2027-aaaaaa', 'acc-004', $2, $3, $4)`, f.Workspace.ID, accountRead.ID,
			opportunities.ID, reason)
	}
	if err := register("Renewed"); err == nil {
		t.Errorf("an open deal was registered with a reason")
	}
	ok(t, register(nil))

	// A person's close has no outcome.
	person := closeReceipt(t, f, deal, "Renewed", another.Card.ID)
	if err := sql(t, f, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'person',
		close_reason = 'Renewed', close_receipt_id = $3, final_card_id = $4, close_outcome = 'won'
		WHERE workspace_id = $1 AND ref = $2`, f.Workspace.ID, deal.Ref, person.ID, another.Card.ID); err == nil {
		t.Errorf("a person's close took an outcome")
	}
	if n := count(t, f, `SELECT count(*) FROM deals WHERE state = 'closed'`); n != 0 {
		t.Errorf("%d deals are closed after every refusal", n)
	}
	// The deal's newest card read its opportunity: the close on the stage is taken.
	reading := postgrestest.PublishReading(t, f, "list_opportunities", postgrestest.Account, f.Reader.ID, "brief-reading",
		postgrestest.Record("opportunities", "opp-1", "id", "opp-1", "account_id", postgrestest.Account))
	ok(t, linkCard(t, f, reading.Card.ID, deal.Ref, postgrestest.Account))
	ok(t, on(reading.Card.ID))
	if n := count(t, f, `SELECT count(*) FROM gtm_integrity_violations()`); n != 0 {
		t.Errorf("the integrity check finds %d violations", n)
	}
}

// CloseDeal says the deal closed once the close has committed, also when the read that follows fails: a
// caller must not take the error for a close that did not happen.
func TestACommittedCloseIsReportedAsOne(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	postgres.SetCrash(f.Store, func(at string) error {
		if at == "close:committed" {
			return errors.New("the connection dropped")
		}
		return nil
	})
	_, changed, err := f.Store.CloseDeal(ctx, postgres.DealClose{WorkspaceID: f.Workspace.ID, Ref: deal.Ref, Reason: "Renewed"})
	postgres.SetCrash(f.Store, nil)
	if err == nil || !changed {
		t.Errorf("a close whose follow-up read failed: changed %v, %v", changed, err)
	}
	if stored, err := f.Store.Deal(ctx, f.Workspace.ID, deal.Ref); err != nil || stored.State != "closed" {
		t.Errorf("the deal: %+v %v", stored, err)
	}
}

// A rule reads the tables of its own schema. A writer's temporary table named receipts does not stand in
// for the receipts: here it holds the close's receipt with another reason, and the close that names that
// reason is still refused.
func TestARuleDoesNotReadATemporaryTableInARealOnesPlace(t *testing.T) {
	f := postgrestest.New(t)
	deal := postgrestest.RegisterDeal(t, f, postgrestest.Account, "", "invented")
	real := closeReceipt(t, f, deal, "Renewed", "")
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	for _, statement := range []string{
		`CREATE TEMP TABLE receipts (LIKE public.receipts)`,
		`INSERT INTO receipts SELECT * FROM public.receipts WHERE id = '` + real.ID + `'`,
		`UPDATE receipts SET arguments = jsonb_set(arguments::jsonb, '{reason}', '"Lost"')::json`,
	} {
		if _, err := conn.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	_, err = conn.Exec(ctx, `UPDATE deals SET state = 'closed', closed_at = now(), close_basis = 'person',
		close_reason = 'Lost', close_receipt_id = $3 WHERE workspace_id = $1 AND ref = $2`, f.Workspace.ID, deal.Ref, real.ID)
	if err == nil || !strings.Contains(err.Error(), "is not closed as its receipt names") {
		t.Errorf("a close on the word of a temporary table: %v", err)
	}
	// Every function of the schema that is not declared immutable carries this path, with temporary tables
	// last. One added without it, or with another path, fails here: a path that leaves pg_temp out still has
	// temporary tables searched first.
	path := "search_path=pg_catalog, " + scalar(t, f.URL, `SELECT quote_ident(current_schema())`) + ", pg_temp"
	rows, err := conn.Query(ctx, `SELECT p.oid::regprocedure::text FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname LIKE 'gtm\_%' AND p.provolatile <> 'i'
		  AND NOT coalesce(p.proconfig, '{}') @> ARRAY[$1]
		ORDER BY 1`, path)
	ok(t, err)
	bare, err := pgx.CollectRows(rows, pgx.RowTo[string])
	ok(t, err)
	if len(bare) != 0 {
		t.Errorf("these functions read tables along the session's path; give each SET search_path: %v", bare)
	}
	var carrying int
	ok(t, conn.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname LIKE 'gtm\_%' AND p.proconfig @> ARRAY[$1]`, path).Scan(&carrying))
	if carrying < 30 {
		t.Errorf("%d functions carry a path; the schema has more rules than that", carrying)
	}
}
