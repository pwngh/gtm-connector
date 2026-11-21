package postgres_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The line rules hold for any writer: each case writes a card with SQL, past PublishCard's own checks,
// changing one thing about its lines from a card that commits.
func TestTheTriggersHoldTheLineRules(t *testing.T) {
	f := postgrestest.New(t)
	withLines := func(t *testing.T, brief string, lines ...evidence.Line) postgres.CardPublication {
		pub, _, _ := prepared(t, f, brief)
		postgrestest.WithLines(t, &pub, lines...)
		return pub
	}
	body := func(l evidence.Line) json.RawMessage {
		out, err := strictjson.Dumps(l.Value(), strictjson.Prepared)
		ok(t, err)
		return json.RawMessage(out)
	}
	committed := withLines(t, "lines-ok", postgrestest.FixtureLines()...)
	ok(t, sqlCard(t, f, committed))
	for i, c := range []struct {
		name, message string
		pub           func(t *testing.T, brief string) postgres.CardPublication
	}{
		{"a line whose words are not the text's", "its lines differ from the sealed text",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub := withLines(t, brief, postgrestest.FixtureLines()...)
				changed := postgrestest.FixtureLines()[1]
				changed.Text = `"Another account"; industry Missing`
				pub.Lines[1].Body = body(changed)
				return pub
			}},
		{"a line the text prints left out", "its lines differ from the sealed text",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub := withLines(t, brief, postgrestest.FixtureLines()...)
				pub.Lines = pub.Lines[1:]
				return pub
			}},
		{"lines for a text that prints none", "its lines differ from the sealed text",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub, _, _ := prepared(t, f, brief)
				lines := withLines(t, brief+"-other", postgrestest.FixtureLines()...)
				pub.Lines = lines.Lines
				return pub
			}},
		{"a value before a signal", "a signal line after a value line",
			func(t *testing.T, brief string) postgres.CardPublication {
				pub := withLines(t, brief, postgrestest.FixtureLines()...)
				pub.Lines[0], pub.Lines[1] = pub.Lines[1], pub.Lines[0]
				return pub
			}},
		{"a line citing a unit the card does not hold", "cites a unit the card does not hold",
			func(t *testing.T, brief string) postgres.CardPublication {
				lines := postgrestest.FixtureLines()
				lines[1].Cites = []int{9}
				return withLines(t, brief, lines...)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, m := refusal(sqlCard(t, f, c.pub(t, fmt.Sprintf("lines-%d", i)))); got != "GT009" ||
				!strings.Contains(m, c.message) {
				t.Fatalf("committed with %s %q", got, m)
			}
		})
	}
	t.Run("a card made here with no line count", func(t *testing.T) {
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
	if violations := integrityViolations(t, f); len(violations) != 0 {
		t.Fatalf("the cards that committed break a rule: %v", violations)
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
		ok(t, conn.QueryRow(ctx, "SELECT gtm_card_text($1)", []byte(document)).Scan(&got))
		if want == "" && got != nil || want != "" && (got == nil || *got != want) {
			t.Errorf("%s: read %v", document, got)
		}
	}
}

// A card published before lines were projected has none, and says so: its view's lines are nil, where a
// card published since with a text that prints no lines has an empty list. It keeps through a backup.
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
	if view.Lines == nil || len(view.Lines) != 0 {
		t.Fatalf("a card since, with no lines printed: %v", view.Lines)
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
