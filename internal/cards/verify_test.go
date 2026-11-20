package cards

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

const (
	cliCard  = "brief-2430d066000040008000000000000001"
	prepared = "brief-7a78ae31000040008000000000000002"
)

func openFolder(t *testing.T, name string) *Folder {
	t.Helper()
	f, err := OpenFolder(oracletest.Path(t, "folders/"+name))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// sameLog compares a Go store's receipts with a folder's rows, as their readers return them.
func sameLog(t *testing.T, store *receipts.Memory, f *Folder) {
	t.Helper()
	all := store.All()
	if len(all) != len(f.Log.(RowLog)) {
		t.Fatalf("Go wrote %d receipts; the folder holds %d", len(all), len(f.Log.(RowLog)))
	}
	for _, r := range all {
		rows, err := f.Log.Receipts(context.Background(), []string{r.AuditID})
		if err != nil {
			t.Fatalf("%s: %v", r.AuditID, err)
		}
		if !evidence.Equal(r.Value(), rows[0]) {
			got, _ := strictjson.Dumps(r.Value(), strictjson.Quoted)
			want, _ := strictjson.Dumps(rows[0], strictjson.Quoted)
			t.Errorf("receipt %s:\n got %s\nwant %s", r.AuditID, got, want)
		}
	}
}

// TestGoMakesTheFolderCards makes the two folder cards in Go, under the oracle's scopes, and compares the
// bytes and every receipt: the CLI card's JSON-RPC reads and save_brief seal, and the prepared brief's
// outer receipt and its child reads.
func TestGoMakesTheFolderCards(t *testing.T) {
	t.Run("cli_card", func(t *testing.T) {
		sc := newScope("folders/cli_card/1")
		m, store, _ := sc.maker(demoBook(t), demoSources(nil))
		sealed, err := m.Brief(context.Background(), Brief{AccountID: "acc-003", AsOf: "2027-01-15",
			Token: sc.token(accessToken + 1), Scopes: []string{"crm:read", "calls:read", "slack:read"}, IncludeSlack: true})
		if err != nil {
			t.Fatal(err)
		}
		f := openFolder(t, "cli_card")
		if sealed.BriefID != cliCard || !bytes.Equal(sealed.Payload, f.Cards[cliCard]) {
			t.Fatalf("card %s differs at %s", sealed.BriefID, firstDifference(string(sealed.Payload), string(f.Cards[cliCard])))
		}
		sameLog(t, store, f)
	})
	t.Run("prepared", func(t *testing.T) {
		sc := newScope("folders/prepared")
		m, store, _ := sc.maker(demoBook(t), demoSources(nil))
		sealed, err := m.Prepare(context.Background(), Prepare{AccountID: "acc-007", AsOf: "2027-01-15",
			Token: sc.token(accessToken + 1), Scopes: []string{"crm:read", "calls:read"}, IncludeSlack: true,
			RequestID: receipts.RPC(1)})
		if err != nil {
			t.Fatal(err)
		}
		f := openFolder(t, "prepared")
		if sealed.BriefID != prepared || !bytes.Equal(sealed.Payload, f.Prepared[prepared]) {
			t.Fatalf("brief %s differs: %d and %d bytes", sealed.BriefID, len(sealed.Payload), len(f.Prepared[prepared]))
		}
		sameLog(t, store, f)
	})
}

func TestVerifyAdmitsBothKindsAgainAndAgain(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ folder, brief, kind string }{
		{"cli_card", cliCard, "card"}, {"prepared", prepared, "selected_brief"}} {
		t.Run(c.folder, func(t *testing.T) {
			f := openFolder(t, c.folder)
			before := snapshot(t, f)
			first, err := f.Verify(ctx, c.brief, demoSources(nil))
			if err != nil {
				t.Fatal(err)
			}
			if first.Kind != c.kind || first.BriefID != c.brief || len(first.Units) == 0 {
				t.Fatalf("admitted %+v", first)
			}
			for i, b := range first.Units {
				if b.Unit.N != i+1 || b.ReceiptID == "" || !strings.HasPrefix(b.Record.ContentVersion, "sha256:") {
					t.Fatalf("unit %d bound as %+v", i+1, b)
				}
			}
			second, err := f.Verify(ctx, c.brief, demoSources(nil))
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("a second check gave %+v, %v", second, err)
			}
			if after := snapshot(t, f); after != before {
				t.Fatal("verifying changed the folder")
			}
			t.Logf("%s: admitted %s with %d units bound to their records and receipts", c.folder, c.kind, len(first.Units))
		})
	}
}

// snapshot is every byte a folder holds, as one string.
func snapshot(t *testing.T, f *Folder) string {
	var b strings.Builder
	for _, files := range []map[string][]byte{f.Cards, f.Prepared} {
		for _, name := range slices.Sorted(maps.Keys(files)) {
			b.WriteString(name + "\x00" + string(files[name]) + "\x00")
		}
	}
	rows, _ := f.Log.(RowLog)
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		text, _ := strictjson.Dumps(rows[id], strictjson.Quoted)
		b.WriteString(text)
	}
	return Reference("all", []byte(b.String()))
}

// change edits one row of a folder's log in place.
func change(t *testing.T, f *Folder, auditID string, edit func(row *strictjson.Object)) {
	t.Helper()
	row, ok := f.Log.(RowLog)[auditID]
	if !ok {
		t.Fatalf("no receipt %s", auditID)
	}
	edit(row)
}

func TestVerifyRefuses(t *testing.T) {
	ctx := context.Background()
	type tampering struct {
		name string
		edit func(t *testing.T, f *Folder, brief string, seal string, read string)
		want error
	}
	editBytes := func(old, new string) func(t *testing.T, f *Folder, brief, _, _ string) {
		return func(t *testing.T, f *Folder, brief, _, _ string) {
			files := f.Cards
			if _, ok := files[brief]; !ok {
				files = f.Prepared
			}
			if !bytes.Contains(files[brief], []byte(old)) {
				t.Fatalf("%q is not in the card", old)
			}
			files[brief] = bytes.Replace(files[brief], []byte(old), []byte(new), 1)
		}
	}
	cases := []tampering{
		{"a changed byte", editBytes("DEAL CARD", "DEAL CARE"), ErrNotSealed},
		{"a pending seal", func(t *testing.T, f *Folder, _, seal, _ string) {
			change(t, f, seal, func(row *strictjson.Object) {
				row.Set("status", "pending")
				row.Set("http_status", nil)
				row.Set("rows", "[]")
			})
		}, ErrSealPending},
		{"a changed seal row", func(t *testing.T, f *Folder, brief, seal, _ string) {
			change(t, f, seal, func(row *strictjson.Object) {
				row.Set("rows", `["briefs/`+brief+`/`+strings.Repeat("0", 64)+`"]`)
			})
		}, ErrNotSealed},
		{"a seal row of another tool", func(t *testing.T, f *Folder, _, seal, _ string) {
			change(t, f, seal, func(row *strictjson.Object) { row.Set("tool", "get_account") })
		}, ErrNotSealed},
		{"a changed read receipt", func(t *testing.T, f *Folder, _, _, read string) {
			change(t, f, read, func(row *strictjson.Object) { row.Set("started_at", "2027-01-15T15:00:09Z") })
		}, ErrReads},
		{"a read receipt with another row", func(t *testing.T, f *Folder, _, _, read string) {
			change(t, f, read, func(row *strictjson.Object) { row.Set("rows", `["crm/accounts/acc-001"]`) })
		}, ErrReads},
		{"a missing read receipt", func(t *testing.T, f *Folder, _, _, read string) {
			delete(f.Log.(RowLog), read)
		}, ErrReads},
	}
	folders := []struct {
		name, brief, seal, read, schema string
	}{
		{"cli_card", cliCard, "2430d066-0000-4000-8000-000000000006", "2430d066-0000-4000-8000-000000000002",
			`"schema_version": 3`},
		{"prepared", prepared, "7a78ae31-0000-4000-8000-000000000001", "7a78ae31-0000-4000-8000-000000000003",
			`"schema_version":3`},
	}
	for _, folder := range folders {
		all := append(cases,
			tampering{"schema version 2", editBytes(folder.schema, strings.Replace(folder.schema, "3", "2", 1)), ErrSchema2})
		for _, c := range all {
			t.Run(folder.name+"/"+c.name, func(t *testing.T) {
				f := openFolder(t, folder.name)
				c.edit(t, f, folder.brief, folder.seal, folder.read)
				if _, err := f.Verify(ctx, folder.brief, demoSources(nil)); !errors.Is(err, c.want) {
					t.Fatalf("got %v, want %v", err, c.want)
				}
			})
		}
		t.Run(folder.name+"/a linked audit log", func(t *testing.T) {
			dir := t.TempDir()
			source := oracletest.Path(t, "folders/"+folder.name)
			copyTree(t, source, dir)
			if err := os.Remove(filepath.Join(dir, "audit.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(source, "audit.jsonl"), filepath.Join(dir, "audit.jsonl")); err != nil {
				t.Fatal(err)
			}
			f, err := OpenFolder(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Verify(ctx, folder.brief, demoSources(nil)); !errors.Is(err, ErrLinkedLog) {
				t.Fatalf("got %v", err)
			}
		})
		t.Run(folder.name+"/a foreign binding", func(t *testing.T) {
			f := openFolder(t, folder.name)
			for _, bound := range []evidence.Sources{
				func() evidence.Sources {
					s := demoSources(nil)
					s.CRMBinding = strictjson.NewObject("schema_version", 1, "crm_backend", "sqlite",
						"identity_sha256", strings.Repeat("ab", 32))
					return s
				}(),
				func() evidence.Sources { s := demoSources(nil); s.SlackBackend = "none"; return s }(),
				func() evidence.Sources {
					s := demoSources(nil)
					s.CallsBackend, s.CallsBinding = "imported", evidence.FixtureCRMBinding()
					return s
				}(),
			} {
				if _, err := f.Verify(ctx, folder.brief, bound); !errors.Is(err, ErrForeign) {
					t.Fatalf("got %v", err)
				}
			}
		})
	}
	t.Run("a card of another folder", func(t *testing.T) {
		card, other := openFolder(t, "cli_card"), openFolder(t, "prepared")
		if _, err := VerifyCard(ctx, card.Cards[cliCard], cliCard, other.Log, false, demoSources(nil)); !errors.Is(err, ErrSealUnread) {
			t.Fatalf("got %v", err)
		}
		if _, err := card.Verify(ctx, "brief-"+strings.Repeat("0", 32), demoSources(nil)); !errors.Is(err, ErrNoCard) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a unit that does not resolve", func(t *testing.T) {
		// A sealed card whose unit names a record its read did not return: sealed by Go, so only the
		// number fails.
		sc := newScope("unresolved")
		m, store, _ := sc.maker(demoBook(t), demoSources(nil))
		sealed, err := m.Brief(ctx, Brief{AccountID: "acc-003", AsOf: "2027-01-15", Token: "t",
			Scopes: []string{"crm:read", "calls:read"}})
		if err != nil {
			t.Fatal(err)
		}
		artifact, _ := strictjson.Loads(sealed.Payload)
		units, _ := artifact.(*strictjson.Object).Get("units")
		units.([]any)[0].(*strictjson.Object).Set("record_ref", "crm/accounts/acc-001")
		if _, err := resolve(sealed.BriefID, mustGet(artifact, "evidence"), units.([]any)); !errors.Is(err, ErrUnresolved) {
			t.Fatalf("got %v", err)
		}
		if _, err := VerifyCard(ctx, sealed.Payload, sealed.BriefID, StoreLog{store}, false, demoSources(nil)); err != nil {
			t.Fatalf("the untouched card: %v", err)
		}
	})
}

func mustGet(v any, key string) *strictjson.Object {
	o, _ := v.(*strictjson.Object).Get(key)
	return o.(*strictjson.Object)
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// failingFiles refuses every save.
type failingFiles struct{ removed []string }

func (f *failingFiles) Save(string, []byte) error { return errors.New("disk full") }
func (f *failingFiles) Remove(name string)        { f.removed = append(f.removed, name) }

func TestGoCardsSealAndVerify(t *testing.T) {
	ctx := context.Background()
	sc := newScope("go cards")
	m, store, files := sc.maker(demoBook(t), demoSources(nil))
	sealed, err := m.Brief(ctx, Brief{AccountID: "acc-007", AsOf: "2027-01-15", Token: "a bearer",
		Scopes: []string{"crm:read", "calls:read", "slack:read"}, IncludeSlack: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCard(ctx, files[sealed.BriefID+".json"], sealed.BriefID, StoreLog{store}, false,
		demoSources(nil)); err != nil {
		t.Fatalf("a Go card: %v", err)
	}
	failing := &failingFiles{}
	m.Files = failing
	if _, err := m.Brief(ctx, Brief{AccountID: "acc-007", AsOf: "2027-01-15", Token: "a bearer",
		Scopes: []string{"crm:read"}}); err == nil {
		t.Fatal("a failed save made a card")
	}
	all := store.All()
	seal := all[len(all)-1]
	if seal.Tool != "save_brief" || seal.Status != "error" || *seal.HTTPStatus != 500 ||
		*seal.Error != "brief_save_failed" || len(seal.Rows) != 0 || len(failing.removed) != 1 {
		t.Fatalf("the failed seal: %+v, removed %v", seal, failing.removed)
	}
	// The denied read left its receipt and read nothing.
	denied := all[len(all)-2]
	if denied.Tool != "read_call_notes" || denied.Status != "error" || *denied.HTTPStatus != 403 || len(denied.Rows) != 0 {
		t.Fatalf("the denied read: %+v", denied)
	}
}
