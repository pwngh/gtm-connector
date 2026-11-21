package postgres_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// contactLine is a contact line citing unit 1, with its name and title as data.
func contactLine() evidence.Line {
	name, title := `Dana "D" Wren`, `Head of légal \ ops`
	return evidence.Line{Section: "value", Code: "contact", Label: "Contact",
		Text: strictjson.Quote(name) + "; role " + strictjson.Quote(title) + "; email Missing", Cites: []int{1},
		Contact: &evidence.Person{Name: &name, Title: &title}}
}

// body is a line's body as a projection stores it, with one change made to its value first.
func body(t *testing.T, l evidence.Line, change func(o *strictjson.Object)) json.RawMessage {
	t.Helper()
	value := l.Value()
	change(value)
	out, err := strictjson.Dumps(value, strictjson.Prepared)
	ok(t, err)
	return json.RawMessage(out)
}

// withText replaces a card's text in its bytes.
func withText(t *testing.T, pub *postgres.CardPublication, text string) {
	t.Helper()
	value, err := strictjson.Loads(pub.Bytes)
	ok(t, err)
	value.(*strictjson.Object).Set("text", text)
	out, err := strictjson.Dumps(value, strictjson.Artifact)
	ok(t, err)
	pub.Bytes = []byte(out)
}

// The line rules hold for any writer: each case writes a card with SQL, past PublishCard's own checks,
// changing one thing about its lines from a card that commits. A typed fact never disagrees with the text it
// sits beside, and every card published now prints and counts its record values.
func TestTheTriggersHoldTheLineRules(t *testing.T) {
	f := postgrestest.New(t)
	lines := append(postgrestest.FixtureLines(), contactLine())
	withLines := func(t *testing.T, brief string, lines ...evidence.Line) postgres.CardPublication {
		pub, _, _ := prepared(t, f, brief)
		postgrestest.WithLines(t, &pub, lines...)
		return pub
	}
	committed := withLines(t, "lines-ok", lines...)
	ok(t, sqlCard(t, f, committed))
	// changed is the card with line i's stored value changed and its text as it was.
	changed := func(i int, change func(o *strictjson.Object)) func(t *testing.T, brief string) postgres.CardPublication {
		return func(t *testing.T, brief string) postgres.CardPublication {
			pub := withLines(t, brief, lines...)
			pub.Lines[i].Body = body(t, lines[i], change)
			return pub
		}
	}
	const renewal, arr, account, contact = 1, 2, 3, 4
	facts := "carries facts its text does not print"
	for i, c := range []struct {
		name, message string
		pub           func(t *testing.T, brief string) postgres.CardPublication
	}{
		{"a line whose words are not the text's", "its lines differ from the sealed text",
			changed(renewal, func(o *strictjson.Object) { o.Set("text", "2027-02-08, in 25 days") })},
		{"a countdown's days, its text kept", facts, changed(renewal, func(o *strictjson.Object) {
			c, _ := o.Get("countdown")
			c.(*strictjson.Object).Set("days", 25)
		})},
		{"a countdown's date, its text kept", facts, changed(renewal, func(o *strictjson.Object) {
			c, _ := o.Get("countdown")
			c.(*strictjson.Object).Set("date", "2027-02-09")
		})},
		{"a countdown to a contract on the renewal line", facts, changed(renewal, func(o *strictjson.Object) {
			c, _ := o.Get("countdown")
			c.(*strictjson.Object).Set("counts_to", "contract_end")
		})},
		{"a renewal line without its countdown", facts,
			changed(renewal, func(o *strictjson.Object) { o.Set("countdown", nil) })},
		{"an amount, its text kept", facts, changed(arr, func(o *strictjson.Object) {
			a, _ := o.Get("amount")
			a.(*strictjson.Object).Set("amount", "450001.00")
		})},
		{"a currency, its text kept", facts, changed(arr, func(o *strictjson.Object) {
			a, _ := o.Get("amount")
			a.(*strictjson.Object).Set("currency", "EUR")
		})},
		{"an amount on another line", facts, changed(account, func(o *strictjson.Object) {
			o.Set("amount", strictjson.NewObject("amount", "1.00", "currency", nil))
		})},
		{"a contact's name, its text kept", facts, changed(contact, func(o *strictjson.Object) {
			p, _ := o.Get("contact")
			p.(*strictjson.Object).Set("name", "Someone else")
		})},
		{"a contact's title, its text kept", facts, changed(contact, func(o *strictjson.Object) {
			p, _ := o.Get("contact")
			p.(*strictjson.Object).Set("title", nil)
		})},
		{"a value under another code with its label", facts, changed(account, func(o *strictjson.Object) {
			o.Set("code", "crm_risk")
		})},
		{"a line the text prints left out", "its lines differ from the sealed text",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub := withLines(t, brief, lines...)
				pub.Lines = pub.Lines[1:]
				return pub
			}},
		{"lines other than the text prints", "its lines differ from the sealed text",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub, _, _ := prepared(t, f, brief)
				pub.Lines = withLines(t, brief+"-other", lines...).Lines
				return pub
			}},
		{"a value before a signal", "a signal line after a value line",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub := withLines(t, brief, lines...)
				pub.Lines[0], pub.Lines[1] = pub.Lines[1], pub.Lines[0]
				return pub
			}},
		{"a line citing a unit the card does not hold", "cites a unit the card does not hold",
			func(t *testing.T, brief string) postgres.CardPublication {
				cited := postgrestest.FixtureLines()
				cited[3].Cites = []int{9}
				return withLines(t, brief, cited...)
			}},
		{"a text that prints no record values, and no lines", "prints no record values",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub, _, _ := prepared(t, f, brief)
				withText(t, &pub, "DEAL CARD acc-003\n")
				pub.Lines = nil
				return pub
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, m := refusal(sqlCard(t, f, c.pub(t, fmt.Sprintf("lines-%d", i)))); got != "GT009" ||
				!strings.Contains(m, c.message) {
				t.Fatalf("committed with %s %q", got, m)
			}
		})
	}
	t.Run("a card with no line count", func(t *testing.T) {
		pub, seal := publication(t, f, "lines-uncounted")
		err := sql(t, f, `INSERT INTO cards (workspace_id, account_ref, brief_id, kind, schema_version, rules_version,
			card_bytes, card_sha256, parent_receipt_id, seal_receipt_id, unit_count, gap_count, read_count)
			VALUES ($1, 'acc-003', 'lines-uncounted', 'go', 3, '3', $3, sha256($3), $4, $2, 1, 1, 1)`,
			f.Workspace.ID, seal.ID, pub.Bytes, pub.ParentReceiptID)
		if c, m := refusal(err); c != "GT009" || !strings.Contains(m, "counts its lines") {
			t.Fatalf("committed with %s %q", c, m)
		}
	})
	t.Run("a line added to a published card", func(t *testing.T) {
		card, err := f.Store.CardByBrief(ctx, f.Workspace.ID, "lines-ok")
		ok(t, err)
		err = sql(t, f, `INSERT INTO card_lines (workspace_id, card_id, ordinal, section, body) VALUES ($1, $2, $3, 'value', $4)`,
			f.Workspace.ID, card.ID, len(committed.Lines), committed.Lines[1].Body)
		if c, m := refusal(err); c != "GT009" || !strings.Contains(m, "a line was added") {
			t.Fatalf("committed with %s %q", c, m)
		}
	})
	t.Run("a card listed as published before lines", func(t *testing.T) {
		card, err := f.Store.CardByBrief(ctx, f.Workspace.ID, "lines-ok")
		ok(t, err)
		err = sql(t, f, `INSERT INTO cards_before_lines (workspace_id, card_id) VALUES ($1, $2)`, f.Workspace.ID, card.ID)
		if c, m := refusal(err); c != "GT009" || !strings.Contains(m, "fixed when lines began") {
			t.Fatalf("committed with %s %q", c, m)
		}
	})
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("the cards that committed break a rule: %v", violations)
	}
}

// A restore checks the lines it loads as publication does: an archive edited so a card's lines say other
// than its sealed text, or so a card published since lines were kept looks as if it came before them, is
// refused whole, however its counts and digests are made to agree.
func TestRestoreRefusesLinesThePublicationRefuses(t *testing.T) {
	f := postgrestest.New(t)
	files := dump(t, f)
	lineCount := fmt.Sprintf("\t%d\n", len(postgrestest.FixtureLines()))
	if strings.Count(files.data["cards"], lineCount) != 1 || !strings.Contains(files.data["card_lines"], `"days": 24`) {
		t.Fatalf("the archive does not hold the fixture card as this test edits it:\n%s", files.data["cards"])
	}
	for name, edit := range map[string]func(map[string]string){
		"a countdown's days, its text kept": func(m map[string]string) {
			m["card_lines"] = strings.Replace(m["card_lines"], `"days": 24`, `"days": 25`, 1)
		},
		"a line's words changed, its count kept": func(m map[string]string) {
			m["card_lines"] = strings.Replace(m["card_lines"], "at risk.", "at once.", 1)
		},
		"a card since made to look as if it came before lines": func(m map[string]string) {
			m["cards"] = strings.Replace(m["cards"], lineCount, "\t\\N\n", 1)
			m["card_lines"] = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			copied := map[string]string{}
			for k, v := range files.data {
				copied[k] = v
			}
			edit(copied)
			fresh := freshStore(t)
			err := fresh.Restore(ctx, strings.Repeat("a", 64), files.version, recount(files.copies, copied), opener(copied))
			if err == nil || !strings.Contains(err.Error(), "the backup holds rows the connector would refuse") {
				t.Fatalf("restored: %v", err)
			}
			if v, _ := fresh.SchemaVersion(ctx); v != 0 {
				t.Fatal("a refused restore left a schema")
			}
		})
	}
}

// The text a card's lines are checked against is read from its bytes even where its evidence holds U+0000,
// which PostgreSQL reads in no JSON string, and a text that writes the escape out keeps it.
func TestTheTextIsReadPastAnEscapedNul(t *testing.T) {
	f := postgrestest.New(t)
	conn, err := pgx.Connect(ctx, f.URL)
	ok(t, err)
	defer conn.Close(ctx)
	for document, want := range map[string]string{
		`{"evidence": {"records": [{"body": "a\u0000\u0000b"}]}, "text": "DEAL CARD\n"}`: "DEAL CARD\n",
		`{"text": "kept \\u0000 as written", "body": "\\\u0000"}`:                        `kept \u0000 as written`,
		`{"units": []}`: "",
	} {
		var got *string
		ok(t, conn.QueryRow(ctx, "SELECT gtm_card_document($1) ->> 'text'", []byte(document)).Scan(&got))
		if want == "" && got != nil || want != "" && (got == nil || *got != want) {
			t.Errorf("%s: read %v", document, got)
		}
	}
}

// A card published before lines were projected has none, and says so: its view's lines are nil, where a
// card published since has its lines. It keeps through a backup.
func TestACardFromBeforeLinesHasNone(t *testing.T) {
	f := postgrestest.BeforeLines(t)
	if f.Card.LineCount != nil {
		t.Fatalf("the card counts %d lines", *f.Card.LineCount)
	}
	viewer := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID}
	view, err := f.Store.ViewCard(ctx, viewer, f.Card.ID)
	ok(t, err)
	if view.Lines != nil || len(view.Units) != 2 {
		t.Fatalf("the card before lines: %d lines, %d units", len(view.Lines), len(view.Units))
	}
	since := postgrestest.Publish(t, f, postgrestest.Account, f.Reader.ID, "brief-since")
	view, err = f.Store.ViewCard(ctx, viewer, since.Card.ID)
	ok(t, err)
	if len(view.Lines) != 1 || view.Lines[0].Code != "account" {
		t.Fatalf("a card since: %v", view.Lines)
	}
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("%v", violations)
	}
	files := dump(t, f)
	fresh, url := freshStoreURL(t)
	ok(t, fresh.Restore(ctx, strings.Repeat("e", 64), files.version, files.copies, opener(files.data)))
	if got := scalar(t, url, `SELECT coalesce(line_count::text, 'null') FROM cards WHERE brief_id = 'brief-1'`); got != "null" {
		t.Fatalf("restored with a line count of %s", got)
	}
}

// A card published on the schema before lines, by the binary of that schema, is listed as published before
// lines when the migration runs: it keeps no count and no lines, breaks no rule, and cards published
// afterwards count theirs.
func TestAnUpgradeKeepsCardsFromBeforeLines(t *testing.T) {
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	ok(t, err)
	t.Cleanup(store.Close)
	ok(t, postgres.MigrateTo(store, ctx, postgres.MigrationVersion("card_lines")-1))
	f := &postgrestest.Fixture{URL: url, Store: store}
	f.Workspace, err = store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	ok(t, err)
	f.Reader, err = store.CreatePrincipal(ctx, f.Workspace, "person", "Demo reader")
	ok(t, err)
	ok(t, store.EnsureAccount(ctx, f.Workspace.ID, postgrestest.Account, "Invented account"))
	ok(t, store.GrantAccount(ctx, f.Workspace.ID, f.Reader.ID, postgrestest.Account))
	f.Connection, err = store.EnsureConnection(ctx, f.Workspace, "crm", "demo")
	ok(t, err)
	before, _, _ := prepared(t, f, "brief-before")
	id := postgrestest.PublishUncounted(t, f, before)

	ok(t, store.Migrate(ctx))
	if got := scalar(t, url, `SELECT count(*)::text FROM cards_before_lines WHERE card_id = '`+id+`'`); got != "1" {
		t.Fatal("the card from before is not listed")
	}
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("%v", violations)
	}
	viewer := postgres.Viewer{WorkspaceID: f.Workspace.ID, PrincipalID: f.Reader.ID}
	view, err := store.ViewCard(ctx, viewer, id)
	ok(t, err)
	if view.Card.LineCount != nil || view.Lines != nil {
		t.Fatalf("the card from before: count %v, lines %v", view.Card.LineCount, view.Lines)
	}
	// Published afterwards, so after the card from before.
	time.Sleep(time.Millisecond)
	after, _, _ := prepared(t, f, "brief-after")
	card, err := store.PublishCard(ctx, after)
	ok(t, err)
	if card.LineCount == nil || *card.LineCount != 1 {
		t.Fatalf("a card since counts %v lines", card.LineCount)
	}
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("%v", violations)
	}
}
