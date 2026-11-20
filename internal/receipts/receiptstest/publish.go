package receiptstest

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A card for the contract: the shape the maker writes, cut down to what publication checks. Its bytes name
// the brief, the account and the seal; evidence.receipts embeds each read as stored; evidence.records holds
// each record a unit shows; and one record unit and one gap are its projections.
type card struct {
	f        *Fixture
	brief    string
	seal     receipts.Receipt
	prepared bool
	// principal and token are the reads' and the seal's.
	principal, token string
	reads            []receipts.Receipt // as embedded, in order
	cited            receipts.Receipt   // the read unit 1 rests on
	record           *strictjson.Object
	pub              receipts.Publication
	doc              *strictjson.Object
}

// accountRecord is the CRM record get_account returns for an account, as a read binds it.
func accountRecord(account string) (*strictjson.Object, receipts.RecordVersion) {
	record := strictjson.NewObject("id", account, "name", "Invented account", "record_ref", "crm/accounts/"+account)
	body, _ := strictjson.Dumps(record, strictjson.Prepared)
	return record, receipts.RecordVersion{Source: "crm", Table: "accounts", RecordID: account, Body: []byte(body)}
}

// read starts and finishes one read for c's brief: a top-level read for a card, a child of the seal for a
// prepared brief. versions nil reads nothing; denied finishes it as a scope denial.
func (c *card) read(t *testing.T, operation, tool string, versions []receipts.RecordVersion, denied bool) receipts.Receipt {
	t.Helper()
	b := c.brief
	var r receipts.Receipt
	var err error
	if c.prepared {
		r, err = c.f.Store.StartChild(ctx, receipts.ChildStart{Parent: c.seal.AuditID, Ordinal: len(c.reads) + 1,
			Tool: tool, Scope: "crm:read", ConnectionID: c.f.Connection, AccountRef: c.f.Account,
			Arguments: strictjson.NewObject("account_id", c.f.Account), BriefID: &b})
	} else {
		r, err = c.f.Store.Start(ctx, receipts.Start{WorkspaceID: c.f.Workspace, OperationID: operation,
			PrincipalID: c.principal, TokenID: c.token, Tool: tool, Scope: "crm:read", AccountRef: c.f.Account,
			ConnectionID: c.f.Connection, Arguments: strictjson.NewObject("account_id", c.f.Account),
			RequestID: receipts.RPC(len(c.reads) + 1), BriefID: &b})
	}
	if err != nil {
		t.Fatal(err)
	}
	if denied {
		must(c.f.Store.Finish(ctx, r.Ticket(), receipts.Denied()))
	} else {
		must(c.f.Store.FinishRead(ctx, r.Ticket(), versions, 0))
	}
	return readOne(t, c.f, r.AuditID)
}

// newCard starts a seal and reads acc's record for a new brief named after the operation, as the reader
// principal under the bearer, and describes the card resting on that read.
func newCard(t *testing.T, f *Fixture, operation string, prepared bool, principal, bearer string) *card {
	t.Helper()
	c := &card{f: f, brief: "brief-" + operation, prepared: prepared, principal: principal, token: bearer}
	var err error
	if prepared {
		c.seal, err = f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: operation,
			PrincipalID: principal, TokenID: bearer, Tool: receipts.Composite, Scope: "crm:read",
			AccountRef: f.Account, Arguments: strictjson.NewObject("account_id", f.Account), RequestID: receipts.RPC(1)})
	} else {
		c.seal, err = f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: operation + "-seal",
			PrincipalID: principal, TokenID: bearer, Tool: receipts.SaveBrief, AccountRef: f.Account,
			Arguments: strictjson.NewObject("account_id", f.Account, "brief_id", c.brief), RequestID: receipts.Null()})
	}
	if err != nil {
		t.Fatal(err)
	}
	record, v := accountRecord(f.Account)
	c.record = record
	c.cited = c.read(t, operation+"-1", "get_account", []receipts.RecordVersion{v}, false)
	c.reads = []receipts.Receipt{c.cited}
	return c
}

// unit is the card's one unit, on its account record.
func (c *card) unit() *strictjson.Object {
	return strictjson.NewObject("kind", "record", "n", 1, "record_ref", "crm/accounts/"+c.f.Account,
		"tool", "get_account")
}

// build writes the publication and the document its bytes are made from; finish dumps the bytes.
func (c *card) build(t *testing.T) {
	t.Helper()
	_, v := accountRecord(c.f.Account)
	unit := c.unit()
	value, _ := strictjson.Dumps(unit, strictjson.Prepared)
	gap := strictjson.NewObject("code", "neutralized", "disposition", "note", "count", big.NewInt(1),
		"by_role", nil, "text", "1 sentence had markup neutralized.")
	receiptValues := make([]any, len(c.reads))
	ids := make([]string, len(c.reads))
	for i, r := range c.reads {
		receiptValues[i], ids[i] = r.Value(), r.AuditID
	}
	c.doc = strictjson.NewObject("brief_id", c.brief, "account_id", c.f.Account)
	kind := "card"
	if c.prepared {
		kind = "selected_brief"
		c.doc.Set("kind", "selected_brief")
		c.doc.Set("prepared_by", strictjson.NewObject("outer_audit_id", c.seal.AuditID, "token_id", c.seal.TokenID))
	} else {
		c.doc.Set("sealed_by", c.seal.AuditID)
	}
	c.doc.Set("units", []any{unit})
	c.doc.Set("gaps", []any{gap})
	c.doc.Set("evidence", strictjson.NewObject("brief_id", c.brief, "account_id", c.f.Account,
		"receipts", receiptValues, "records", []any{c.record}))
	c.pub = receipts.Publication{WorkspaceID: c.f.Workspace, AccountRef: c.f.Account, BriefID: c.brief,
		PrincipalID: c.seal.PrincipalID, Seal: c.seal.Ticket(), Kind: kind, SchemaVersion: 3, RulesVersion: "3",
		Reads: ids,
		Units: []receipts.UnitRow{{N: 1, Kind: "record", RecordRef: "crm/accounts/" + c.f.Account, Tool: "get_account",
			ReceiptID: c.cited.AuditID, ContentVersion: v.Version(), Value: []byte(value)}},
		Gaps: []receipts.GapRow{{Code: "neutralized", Disposition: "note", Text: "1 sentence had markup neutralized.",
			Count: big.NewInt(1)}}}
}

func (c *card) finish(t *testing.T) {
	t.Helper()
	text, err := strictjson.Dumps(c.doc, strictjson.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	c.pub.Bytes = []byte(text)
}

func (c *card) publish(t *testing.T) (receipts.Card, error) {
	t.Helper()
	return c.f.Store.Publish(ctx, c.pub)
}

func evidenceOf(c *card) *strictjson.Object {
	e, _ := c.doc.Get("evidence")
	return e.(*strictjson.Object)
}

// published checks a card that was published: its bytes, digest, seal and projections.
func published(t *testing.T, c *card, got receipts.Card) {
	t.Helper()
	ref := receipts.Reference(c.brief, c.pub.Bytes)
	if got.Seal.Status != "ok" || len(got.Seal.Rows) != 1 || got.Seal.Rows[0] != ref ||
		got.SHA256 != ref[len(ref)-64:] || string(got.Bytes) != string(c.pub.Bytes) || !c.f.Published(t, c.brief) ||
		len(got.Units) != 1 || string(got.Units[0].Value) != string(c.pub.Units[0].Value) ||
		got.Units[0].ReceiptID != c.cited.AuditID || len(got.Gaps) != 1 || got.Gaps[0].Count.Int64() != 1 {
		t.Fatalf("published %+v", got)
	}
	if seal := readOne(t, c.f, c.seal.AuditID); !seal.Same(got.Seal) {
		t.Fatalf("the seal reads back as %s", encode(t, seal.Value()))
	}
}

func publishACard(t *testing.T, f *Fixture) {
	f.Grant(t)
	c := newCard(t, f, "card", false, f.Principal, token)
	// A denied read, which the card states, rests beside the read its unit cites.
	c.reads = append(c.reads, c.read(t, "card-2", "read_call_notes", nil, true))
	c.build(t)
	c.finish(t)
	published(t, c, must(c.publish(t)))
}

func publishAPreparedBrief(t *testing.T, f *Fixture) {
	f.Grant(t)
	c := newCard(t, f, "prepared", true, f.Principal, token)
	c.reads = append(c.reads, c.read(t, "", "read_call_notes", nil, true))
	c.build(t)
	c.finish(t)
	published(t, c, must(c.publish(t)))
}

// publishRefusals changes one thing at a time. Each refusal writes no card and leaves the seal as want
// says: pending, or finished as error with that status and code. An edit that changes the reads builds the
// card again.
func publishRefusals(t *testing.T, f *Fixture) {
	other := receipts.Fingerprint("another bearer")
	// reread replaces the card's reads with one new get_account read, made with change applied to the card.
	reread := func(t *testing.T, c *card, operation string, change func(c *card) func()) {
		restore := change(c)
		_, v := accountRecord(c.f.Account)
		c.cited = c.read(t, operation, "get_account", []receipts.RecordVersion{v}, false)
		restore()
		c.reads = []receipts.Receipt{c.cited}
		c.build(t)
	}
	for i, c := range []struct {
		name               string
		prepared, stranger bool
		edit               func(t *testing.T, c *card)
		err                error
		seal               string
	}{
		{name: "no grant", edit: func(*testing.T, *card) {}, err: receipts.ErrNotPermitted, seal: "403 not_permitted"},
		{name: "a seal whose principal holds no grant, published as the owner", stranger: true,
			edit: func(t *testing.T, c *card) { c.pub.PrincipalID = "" },
			err:  receipts.ErrNotPermitted, seal: "403 not_permitted"},
		{name: "an actor other than the seal's",
			edit: func(t *testing.T, c *card) { c.pub.PrincipalID = c.f.Stranger },
			err:  receipts.ErrNotPermitted, seal: "403 not_permitted"},
		{name: "a cited read the source changed under",
			edit: func(t *testing.T, c *card) { c.f.Bump(t) }, err: receipts.ErrSourceChanged, seal: "409 source_changed"},
		{name: "an uncited read the source changed under",
			edit: func(t *testing.T, c *card) {
				stale := c.read(t, c.brief+"-stale", "list_opportunities", []receipts.RecordVersion{}, false)
				c.f.Bump(t)
				reread(t, c, c.brief+"-fresh", func(*card) func() { return func() {} })
				c.reads = []receipts.Receipt{stale, c.cited}
				c.build(t)
			}, err: receipts.ErrSourceChanged, seal: "409 source_changed"},
		{name: "a unit on a read the card does not embed",
			edit: func(t *testing.T, c *card) {
				_, v := accountRecord(c.f.Account)
				extra := c.read(t, c.brief+"-extra", "get_account", []receipts.RecordVersion{v}, false)
				c.pub.Units[0].ReceiptID = extra.AuditID
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a unit on a read never made",
			edit: func(t *testing.T, c *card) { c.pub.Units[0].ReceiptID = receipts.RandomID() },
			err:  receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read under another token",
			edit: func(t *testing.T, c *card) {
				reread(t, c, c.brief+"-token", func(c *card) func() {
					saved := c.token
					c.token = other
					return func() { c.token = saved }
				})
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read by another principal under the same token",
			edit: func(t *testing.T, c *card) {
				reread(t, c, c.brief+"-principal", func(c *card) func() {
					saved := c.principal
					c.principal = c.f.Stranger
					return func() { c.principal = saved }
				})
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read of another brief",
			edit: func(t *testing.T, c *card) {
				reread(t, c, c.brief+"-brief", func(c *card) func() {
					saved := c.brief
					c.brief = saved + "-other"
					return func() { c.brief = saved }
				})
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read of another account",
			edit: func(t *testing.T, c *card) {
				b := c.brief
				r := must(c.f.Store.Start(ctx, receipts.Start{WorkspaceID: c.f.Workspace,
					OperationID: c.brief + "-account", PrincipalID: c.principal, TokenID: c.token, Tool: "get_account",
					Scope: "crm:read", AccountRef: c.f.OtherAccount, ConnectionID: c.f.Connection,
					Arguments: strictjson.NewObject("account_id", c.f.OtherAccount), RequestID: receipts.RPC(1),
					BriefID: &b}))
				_, v := accountRecord(c.f.Account)
				must(c.f.Store.FinishRead(ctx, r.Ticket(), []receipts.RecordVersion{v}, 0))
				c.cited = readOne(t, c.f, r.AuditID)
				c.reads = []receipts.Receipt{c.cited}
				c.build(t)
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a record version its read never bound",
			edit: func(t *testing.T, c *card) { c.pub.Units[0].ContentVersion = strings.Repeat("0", 64) },
			err:  receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a record the bytes show other than its read bound",
			edit: func(t *testing.T, c *card) { c.record.Set("name", "Another name") },
			err:  receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read embedded with another start time",
			edit: func(t *testing.T, c *card) {
				list, _ := evidenceOf(c).Get("receipts")
				list.([]any)[0].(*strictjson.Object).Set("started_at", "2001-01-01T00:00:00Z")
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a read the bytes embed and the publication leaves out",
			edit: func(t *testing.T, c *card) {
				c.reads = append(c.reads, c.read(t, c.brief+"-2", "read_call_notes", nil, true))
				c.build(t)
				c.pub.Reads = c.pub.Reads[:1]
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a unit under another tool than its read's",
			edit: func(t *testing.T, c *card) {
				unit := c.unit()
				unit.Set("tool", "list_opportunities")
				value, _ := strictjson.Dumps(unit, strictjson.Prepared)
				c.doc.Set("units", []any{unit})
				c.pub.Units[0].Tool, c.pub.Units[0].Value = "list_opportunities", []byte(value)
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "bytes naming another seal",
			edit: func(t *testing.T, c *card) { c.doc.Set("sealed_by", receipts.RandomID()) },
			err:  receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a prepared brief on a child of another call", prepared: true,
			edit: func(t *testing.T, c *card) {
				elsewhere := newCard(t, c.f, c.brief[len("brief-"):]+"-elsewhere", true, c.principal, c.token)
				// The other call's child is made for this card's brief: only its parent differs.
				reread(t, c, "", func(c *card) func() {
					saved := c.seal
					c.seal = elsewhere.seal
					return func() { c.seal = saved }
				})
			}, err: receipts.ErrProvenance, seal: "422 provenance"},
		{name: "a unit value other than the bytes'",
			edit: func(t *testing.T, c *card) { c.pub.Units[0].Value = []byte(`{"n": 1}`) },
			err:  receipts.ErrProjection, seal: "422 projection"},
		{name: "a gap count of another type",
			edit: func(t *testing.T, c *card) {
				gaps, _ := c.doc.Get("gaps")
				gaps.([]any)[0].(*strictjson.Object).Set("count", 1.0)
			}, err: receipts.ErrProjection, seal: "422 projection"},
		{name: "a seal of another tool",
			edit: func(t *testing.T, c *card) { c.reseal(t, receipts.SaveBrief+"_other", nil, c.brief) },
			err:  receipts.ErrInvalid, seal: "pending"},
		{name: "a save_brief seal naming another brief",
			edit: func(t *testing.T, c *card) { c.reseal(t, receipts.SaveBrief, nil, "brief-another") },
			err:  receipts.ErrInvalid, seal: "pending"},
		{name: "a save_brief seal with a brief_id",
			edit: func(t *testing.T, c *card) { b := c.brief; c.reseal(t, receipts.SaveBrief, &b, c.brief) },
			err:  receipts.ErrInvalid, seal: "pending"},
		{name: "a seal of another account",
			edit: func(t *testing.T, c *card) { c.pub.AccountRef = c.f.OtherAccount },
			err:  receipts.ErrInvalid, seal: "pending"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if i > 0 {
				f.Grant(t)
			}
			principal := f.Principal
			if c.stranger {
				principal = f.Stranger
			}
			k := newCard(t, f, fmt.Sprintf("refusal-%d", i), c.prepared, principal, token)
			k.build(t)
			c.edit(t, k)
			k.finish(t)
			_, err := k.publish(t)
			is(t, err, c.err, c.name)
			if f.Published(t, k.brief) {
				t.Fatal("a refused publication wrote a card")
			}
			seal := readOne(t, f, k.seal.AuditID)
			got := seal.Status
			if seal.Status == "error" {
				got = fmt.Sprintf("%d %s", *seal.HTTPStatus, *seal.Error)
			}
			if got != c.seal {
				t.Fatalf("the seal: %s, want %s", got, c.seal)
			}
		})
	}
}

// reseal replaces the card's seal with one of this tool, brief_id and arguments naming brief.
func (c *card) reseal(t *testing.T, tool string, briefID *string, named string) {
	t.Helper()
	seal := must(c.f.Store.Start(ctx, receipts.Start{WorkspaceID: c.f.Workspace,
		OperationID: c.brief + "-reseal", PrincipalID: c.principal, TokenID: c.token, Tool: tool,
		AccountRef: c.f.Account, Arguments: strictjson.NewObject("account_id", c.f.Account, "brief_id", named),
		RequestID: receipts.Null(), BriefID: briefID}))
	c.seal = seal
	c.build(t)
}

func publishRetries(t *testing.T, f *Fixture) {
	f.Grant(t)
	c := newCard(t, f, "retry", false, f.Principal, token)
	c.build(t)
	c.finish(t)
	first := must(c.publish(t))
	published(t, c, first)
	// A retry after a lost acknowledgement, with the same bytes, gets the committed card and projections,
	// whatever projections it sends.
	retry := c.pub
	retry.Bytes = append([]byte(nil), c.pub.Bytes...)
	retry.Units = []receipts.UnitRow{{N: 1, Kind: "record", RecordRef: "x", Tool: "y", Value: []byte(`{}`)}}
	retry.Gaps = nil
	again, err := f.Store.Publish(ctx, retry)
	if err != nil || string(again.Bytes) != string(first.Bytes) || !again.Seal.Same(first.Seal) ||
		len(again.Units) != 1 || string(again.Units[0].Value) != string(c.pub.Units[0].Value) ||
		again.Units[0].ReceiptID != c.cited.AuditID || again.Units[0].ContentVersion != c.pub.Units[0].ContentVersion ||
		len(again.Gaps) != 1 || again.Gaps[0].Code != "neutralized" {
		t.Fatalf("a retried publication: %+v %v", again, err)
	}
	// The card handed back is a copy.
	again.Bytes[0], again.Units[0].Value[0] = 'X', 'X'
	if third := must(f.Store.Publish(ctx, c.pub)); string(third.Bytes) != string(c.pub.Bytes) ||
		string(third.Units[0].Value) != string(c.pub.Units[0].Value) {
		t.Fatal("a returned card shares memory with the store")
	}
	lost := c.pub
	lost.Seal.Lease = receipts.RandomID()
	_, err = f.Store.Publish(ctx, lost)
	is(t, err, receipts.ErrLeaseLost, "a retry without the seal's lease")
	other := c.pub
	other.Bytes = append(append([]byte(nil), c.pub.Bytes...), '\n')
	_, err = f.Store.Publish(ctx, other)
	is(t, err, receipts.ErrFinished, "publishing other bytes under a finished seal")
	// A second seal for the same brief publishes nothing; it stays pending.
	twice := newCard(t, f, "retry-twice", false, f.Principal, token)
	twice.brief = c.brief
	twice.seal = must(f.Store.Start(ctx, receipts.Start{WorkspaceID: f.Workspace, OperationID: "retry-twice-seal-2",
		PrincipalID: f.Principal, TokenID: token, Tool: receipts.SaveBrief, AccountRef: f.Account,
		Arguments: strictjson.NewObject("account_id", f.Account, "brief_id", c.brief), RequestID: receipts.Null()}))
	twice.reads, twice.cited = c.reads, c.cited
	twice.build(t)
	twice.finish(t)
	_, err = f.Store.Publish(ctx, twice.pub)
	is(t, err, receipts.ErrInvalid, "publishing a brief twice")
	if seal := readOne(t, f, twice.seal.AuditID); seal.Status != "pending" {
		t.Fatalf("the second seal finished as %s", seal.Status)
	}
}
