package cards

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The four folders: a card saved by brief, a brief prepared without as_of under a grant that lacks Slack,
// one prepared with as_of under every scope, and one prepared under every scope for an account selected
// without Slack.
type folderCase struct {
	name, brief, seal, read, account string
	prepared                         bool
}

var folders = []folderCase{
	{"cli_card", "brief-2430d066000040008000000000000001", "2430d066-0000-4000-8000-000000000006",
		"2430d066-0000-4000-8000-000000000002", "acc-003", false},
	{"prepared", "brief-7a78ae31000040008000000000000002", "7a78ae31-0000-4000-8000-000000000001",
		"7a78ae31-0000-4000-8000-000000000003", "acc-007", true},
	{"prepared_as_of", "brief-3b4b6cec000040008000000000000002", "3b4b6cec-0000-4000-8000-000000000001",
		"3b4b6cec-0000-4000-8000-000000000003", "acc-003", true},
	{"prepared_no_slack", "brief-2aee567d000040008000000000000002", "2aee567d-0000-4000-8000-000000000001",
		"2aee567d-0000-4000-8000-000000000003", "acc-003", true},
}

func openFolder(t *testing.T, name string) *Folder {
	t.Helper()
	f, err := OpenTrustedExport(oracletest.Path(t, "folders/"+name))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *Folder) payload(brief string) *[]byte {
	if _, ok := f.Cards[brief]; ok {
		p := f.Cards[brief]
		return &p
	}
	p := f.Prepared[brief]
	return &p
}

func (f *Folder) setPayload(brief string, data []byte) {
	if _, ok := f.Cards[brief]; ok {
		f.Cards[brief] = data
	} else {
		f.Prepared[brief] = data
	}
}

// sameLog compares a Go store's receipts with a folder's rows, column by column.
func sameLog(t *testing.T, store *receipts.Memory, f *Folder) {
	t.Helper()
	all := store.All()
	if len(all) != len(f.Log) {
		t.Fatalf("Go wrote %d receipts; the folder holds %d", len(all), len(f.Log))
	}
	for _, r := range all {
		rows, err := f.Log.Receipts(context.Background(), []string{r.AuditID})
		if err != nil {
			t.Fatalf("%s: %v", r.AuditID, err)
		}
		logged, err := receipts.Decode(rows[0])
		if err != nil || !r.Same(logged) {
			got, _ := strictjson.Dumps(r.Value(), strictjson.Quoted)
			want, _ := strictjson.Dumps(rows[0], strictjson.Quoted)
			t.Errorf("receipt %s:\n got %s\nwant %s (%v)", r.AuditID, got, want, err)
		}
	}
}

func prepareOf(token string, account string, slack bool, arguments *strictjson.Object, scopes ...string) Prepare {
	return Prepare{Principal: owner(token, scopes...), Selection: Selection{account: slack}, OperationID: "prepare",
		RequestID: receipts.RPC(1), Arguments: arguments}
}

// TestGoMakesTheFolderCards makes the folder cards in Go, under the oracle's scopes, and compares the bytes
// and every receipt: the CLI card's JSON-RPC reads and save_brief seal, and each prepared brief's outer
// receipt, with its arguments as given, and its child reads.
func TestGoMakesTheFolderCards(t *testing.T) {
	ctx := context.Background()
	for _, c := range folders {
		t.Run(c.name, func(t *testing.T) {
			sc := newScope(map[string]string{"cli_card": "folders/cli_card/1", "prepared": "folders/prepared",
				"prepared_as_of": "folders/prepared_as_of", "prepared_no_slack": "folders/prepared_no_slack"}[c.name])
			m, store := sc.maker(demoBook(t), demoSources(nil))
			token := sc.token(accessToken + 1) // each of these scopes also started the connector
			var sealed *Sealed
			var err error
			switch c.name {
			case "cli_card":
				sealed, err = m.Brief(ctx, Brief{Principal: owner(token, "crm:read", "calls:read", "slack:read"),
					OperationID: "brief", AccountID: c.account, AsOf: DemoAsOf, IncludeSlack: true})
			case "prepared":
				sealed, err = m.Prepare(ctx, prepareOf(token, c.account, true,
					strictjson.NewObject("account_id", c.account), "crm:read", "calls:read"))
			case "prepared_as_of":
				sealed, err = m.Prepare(ctx, prepareOf(token, c.account, true,
					strictjson.NewObject("account_id", c.account, "as_of", "2027-01-20"), "crm:read", "calls:read",
					"slack:read"))
			case "prepared_no_slack":
				sealed, err = m.Prepare(ctx, prepareOf(token, c.account, false,
					strictjson.NewObject("account_id", c.account), "crm:read", "calls:read", "slack:read"))
			}
			if err != nil {
				t.Fatal(err)
			}
			f := openFolder(t, c.name)
			if want := *f.payload(c.brief); sealed.BriefID != c.brief || !bytes.Equal(sealed.Payload, want) {
				t.Fatalf("%s differs at %s", sealed.BriefID, oracletest.FirstDifference(string(sealed.Payload), string(want)))
			}
			sameLog(t, store, f)
		})
	}
}

func TestVerifyAdmitsEveryKindAgainAndAgain(t *testing.T) {
	ctx := context.Background()
	for _, c := range folders {
		t.Run(c.name, func(t *testing.T) {
			f := openFolder(t, c.name)
			before := snapshot(f)
			first, err := f.Verify(ctx, c.brief, demoSources(nil))
			if err != nil {
				t.Fatal(err)
			}
			kind := map[bool]string{false: "card", true: "selected_brief"}[c.prepared]
			if first.Kind != kind || first.BriefID != c.brief || first.AccountID != c.account || len(first.Units) == 0 {
				t.Fatalf("admitted %+v", first)
			}
			for i, b := range first.Units {
				if b.Unit.N != i+1 || b.ReceiptID == "" || len(b.Record.ContentVersion) != 64 {
					t.Fatalf("unit %d bound as %+v", i+1, b)
				}
			}
			second, err := f.Verify(ctx, c.brief, demoSources(nil))
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("a second check gave %+v, %v", second, err)
			}
			if after := snapshot(f); after != before {
				t.Fatal("verifying changed the folder")
			}
			t.Logf("%s: admitted %s with %d units bound to their records and receipts", c.name, kind, len(first.Units))
		})
	}
}

// snapshot is every byte a folder holds, as one digest.
func snapshot(f *Folder) string {
	var b strings.Builder
	for _, files := range []map[string][]byte{f.Cards, f.Prepared} {
		for _, name := range slices.Sorted(maps.Keys(files)) {
			b.WriteString(name + "\x00" + string(files[name]) + "\x00")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(f.Log)) {
		text, _ := strictjson.Dumps(f.Log[id], strictjson.Quoted)
		b.WriteString(text)
	}
	return Reference("all", []byte(b.String()))
}

// set changes one column of one exported row.
func set(column string, value any) func(row *strictjson.Object) {
	return func(row *strictjson.Object) { row.Set(column, value) }
}

// reseal makes the log's seal name the folder's current bytes, so only what else changed is refused.
func reseal(f *Folder, c folderCase) {
	f.Log[c.seal].Set("rows", `["`+Reference(c.brief, *f.payload(c.brief))+`"]`)
}

// editCard replaces text in the card's bytes, in both the card and, where the snapshot repeats it, its log.
func editCard(t *testing.T, f *Folder, c folderCase, old, new string, count int) {
	t.Helper()
	data := *f.payload(c.brief)
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("%q is not in the card", old)
	}
	f.setPayload(c.brief, bytes.Replace(data, []byte(old), []byte(new), count))
}

// sameValue fails unless two card files parse to one value, so an edit changed bytes only.
func sameValue(t *testing.T, a, b []byte) {
	t.Helper()
	va, errA := strictjson.Loads(a)
	vb, errB := strictjson.Loads(b)
	if errA != nil || errB != nil || bytes.Equal(a, b) || !evidence.Equal(va, vb) {
		t.Fatalf("the edit is not value-preserving (%v, %v)", errA, errB)
	}
}

func TestVerifyRefuses(t *testing.T) {
	ctx := context.Background()
	type tampering struct {
		name  string
		edit  func(t *testing.T, f *Folder, c folderCase)
		cli   error // what a CLI card gives; nil where the case is for prepared briefs only
		ready error // what a prepared brief gives; nil where the case is for CLI cards only
	}
	seal := func(edit func(row *strictjson.Object)) func(t *testing.T, f *Folder, c folderCase) {
		return func(t *testing.T, f *Folder, c folderCase) { edit(f.Log[c.seal]) }
	}
	read := func(edit func(row *strictjson.Object)) func(t *testing.T, f *Folder, c folderCase) {
		return func(t *testing.T, f *Folder, c folderCase) { edit(f.Log[c.read]) }
	}
	cases := []tampering{
		{"a changed byte", func(t *testing.T, f *Folder, c folderCase) {
			editCard(t, f, c, "DEAL CARD", "DEAL CARE", 1)
		}, ErrNotSealed, ErrNotAccepted},
		// The seal names the stored bytes, never a re-encoding: bytes that parse to the same value are refused.
		{"a letter written as a JSON escape", func(t *testing.T, f *Folder, c folderCase) {
			before := *f.payload(c.brief)
			editCard(t, f, c, "DEAL CARD", `DEAL C\u0041RD`, 1)
			sameValue(t, before, *f.payload(c.brief))
		}, ErrNotSealed, ErrNotAccepted},
		{"a trailing newline", func(t *testing.T, f *Folder, c folderCase) {
			before := *f.payload(c.brief)
			f.setPayload(c.brief, append(append([]byte(nil), before...), '\n'))
			sameValue(t, before, *f.payload(c.brief))
		}, ErrNotSealed, ErrNotAccepted},
		{"a pending seal", seal(func(row *strictjson.Object) {
			row.Set("status", "pending")
			row.Set("http_status", nil)
			row.Set("rows", "[]")
			row.Set("row_count", 0)
		}), ErrSealPending, ErrSealPending},
		{"a seal with a wrong digest", func(t *testing.T, f *Folder, c folderCase) {
			f.Log[c.seal].Set("rows", `["briefs/`+c.brief+`/`+strings.Repeat("0", 64)+`"]`)
		}, ErrNotSealed, ErrNotAccepted},
		{"a seal row of another tool", seal(set("tool", "get_account")), ErrNotSealed, ErrNotAccepted},
		{"a seal that failed", seal(set("http_status", 500)), ErrNotSealed, ErrNotAccepted},
		{"a seal with an error", seal(set("error", "brief_save_failed")), ErrNotSealed, nil},
		{"a seal with a request ID", seal(set("request_id", "7")), ErrNotSealed, nil},
		{"a seal of a child read", seal(set("request_id",
			`{"kind": "brief_child", "parent_audit_id": "x", "ordinal": 1}`)), ErrNotSealed, nil},
		{"a seal with a brief ID", seal(set("brief_id", "brief-2430d066000040008000000000000001")), ErrNotSealed, nil},
		{"a seal for another account", seal(set("arguments",
			`{"account_id": "acc-001", "brief_id": "brief-2430d066000040008000000000000001"}`)), ErrNotSealed, ErrNotAccepted},
		{"a seal for another brief", seal(set("arguments",
			`{"account_id": "acc-003", "brief_id": "brief-ffffffffffffffffffffffffffffffff"}`)), ErrNotSealed, nil},
		{"a seal under another token", seal(set("token_id", receipts.Anonymous)), ErrNotSealed, ErrNotAccepted},
		{"a changed read receipt", read(set("started_at", "2027-01-15T15:00:09Z")), ErrReads, ErrReads},
		{"a read receipt with a wrong row", read(set("rows", `["crm/accounts/acc-001", "crm/contacts/con-003"]`)),
			ErrReads, ErrReads},
		{"a read receipt whose count is true", read(set("row_count", true)), ErrReads, ErrReads},
		{"a read receipt that failed", read(set("http_status", 500)), ErrReads, ErrReads},
		{"a missing read receipt", func(t *testing.T, f *Folder, c folderCase) { delete(f.Log, c.read) },
			ErrReads, ErrReads},
		{"schema version 2", func(t *testing.T, f *Folder, c folderCase) {
			if c.prepared {
				editCard(t, f, c, `"schema_version":3`, `"schema_version":2`, 1)
			} else {
				editCard(t, f, c, `"schema_version": 3`, `"schema_version": 2`, 1)
			}
		}, ErrSchema2, ErrSchema2},
		// The bytes change and the seal is made to name them, so only the unit is wrong.
		{"a sentence whose text digest is not its record's", func(t *testing.T, f *Folder, c folderCase) {
			value, _ := strictjson.Loads(*f.payload(c.brief))
			units, _ := value.(*strictjson.Object).Get("units")
			for _, u := range units.([]any) {
				if kind, _ := u.(*strictjson.Object).Get("kind"); kind == "sentence" {
					digest, _ := u.(*strictjson.Object).Get("raw_sha256")
					editCard(t, f, c, digest.(string), strings.Repeat("0", 64), -1)
					break
				}
			}
			reseal(f, c)
		}, ErrUnresolved, ErrUnresolved},
		{"a unit whose record its read did not return", func(t *testing.T, f *Folder, c folderCase) {
			// Unit 1 is the account, read by get_account; the opportunity is in list_opportunities' rows only.
			old := map[bool]string{false: `"record_ref": "crm/accounts/` + c.account + `",
      "tool": "get_account"`, true: `"record_ref":"crm/accounts/` + c.account + `","source":"crm","tool":"get_account"`}[c.prepared]
			opportunity := "crm/opportunities/opp-" + c.account[4:]
			editCard(t, f, c, old, strings.Replace(old, "crm/accounts/"+c.account, opportunity, 1), 1)
			reseal(f, c)
		}, ErrUnresolved, ErrUnresolved},
		{"reads of another invocation", func(t *testing.T, f *Folder, c folderCase) {
			other := "00000000-0000-4000-8000-00000000beef"
			editCard(t, f, c, `"parent_audit_id":"`+c.seal+`"`, `"parent_audit_id":"`+other+`"`, -1)
			for _, row := range f.Log {
				text, _ := row.Get("request_id")
				row.Set("request_id", strings.ReplaceAll(text.(string), c.seal, other))
			}
			reseal(f, c)
		}, nil, ErrForeignReads},
		{"reads under another token", func(t *testing.T, f *Folder, c folderCase) {
			token, _ := f.Log[c.read].Get("token_id")
			data := bytes.Replace(*f.payload(c.brief), []byte(`"token_id":"`+token.(string)+`","tool":"get_account"`),
				[]byte(`"token_id":"anonymous","tool":"get_account"`), 1)
			f.setPayload(c.brief, data)
			f.Log[c.read].Set("token_id", receipts.Anonymous)
			reseal(f, c)
		}, nil, ErrForeignReads},
		{"two reads with one ordinal", func(t *testing.T, f *Folder, c folderCase) {
			editCard(t, f, c, `"ordinal":2,`, `"ordinal":1,`, 1)
			for id, row := range f.Log {
				if text, _ := row.Get("request_id"); strings.Contains(text.(string), `"ordinal": 2}`) {
					f.Log[id].Set("request_id", strings.Replace(text.(string), `"ordinal": 2}`, `"ordinal": 1}`, 1))
				}
			}
			reseal(f, c)
		}, nil, ErrForeignReads},
	}
	for _, c := range folders {
		for _, tc := range cases {
			want := tc.cli
			if c.prepared {
				want = tc.ready
			}
			if want == nil {
				continue
			}
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				f := openFolder(t, c.name)
				tc.edit(t, f, c)
				if _, err := f.Verify(ctx, c.brief, demoSources(nil)); !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
		for _, name := range []string{"audit.jsonl", "audit.sqlite3.gz"} {
			t.Run(c.name+"/a linked "+name, func(t *testing.T) {
				dir, source := t.TempDir(), oracletest.Path(t, "folders/"+c.name)
				copyTree(t, source, dir)
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(source, name), filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				if _, err := OpenTrustedExport(dir); !errors.Is(err, ErrLinkedLog) {
					t.Fatalf("got %v", err)
				}
			})
		}
		t.Run(c.name+"/a linked card", func(t *testing.T) {
			dir, source := t.TempDir(), oracletest.Path(t, "folders/"+c.name)
			copyTree(t, source, dir)
			sub := map[bool]string{false: "briefs", true: "selected-briefs"}[c.prepared]
			card := filepath.Join(dir, sub, c.brief+".json")
			if err := os.Remove(card); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(source, sub, c.brief+".json"), card); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenTrustedExport(dir); !errors.Is(err, ErrNotRegular) {
				t.Fatalf("got %v", err)
			}
		})
		t.Run(c.name+"/a foreign binding", func(t *testing.T) {
			f := openFolder(t, c.name)
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
				if _, err := f.Verify(ctx, c.brief, bound); !errors.Is(err, ErrForeign) {
					t.Fatalf("got %v", err)
				}
			}
		})
	}
	t.Run("a card of another folder", func(t *testing.T) {
		card, other := openFolder(t, "cli_card"), openFolder(t, "prepared")
		if _, err := VerifyCard(ctx, card.Cards[folders[0].brief], folders[0].brief, other.Log, demoSources(nil)); !errors.Is(err, ErrSealUnread) {
			t.Fatalf("got %v", err)
		}
		if _, err := card.Verify(ctx, "brief-"+strings.Repeat("0", 32), demoSources(nil)); !errors.Is(err, ErrNoCard) {
			t.Fatalf("got %v", err)
		}
		if _, err := VerifyCard(ctx, card.Cards[folders[0].brief], "brief-X", card.Log, demoSources(nil)); !errors.Is(err, ErrBriefID) {
			t.Fatalf("got %v", err)
		}
		if _, err := VerifyPrepared(ctx, nil, "../brief", card.Log, demoSources(nil)); !errors.Is(err, ErrBriefNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a prepared brief over 4 MiB", func(t *testing.T) {
		f := openFolder(t, "prepared")
		big := append(append([]byte(nil), f.Prepared[folders[1].brief]...), bytes.Repeat([]byte(" "), MaxArtifactBytes)...)
		if _, err := VerifyPrepared(ctx, big, folders[1].brief, f.Log, demoSources(nil)); !errors.Is(err, ErrBriefInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an export that holds one audit ID twice", func(t *testing.T) {
		data := oracletest.Read(t, "folders/cli_card/audit.jsonl")
		first := data[:bytes.IndexByte(data, '\n')+1]
		if _, err := ReadTrustedRows(append(append([]byte(nil), data...), first...)); !errors.Is(err, ErrDuplicateReceipt) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a unit whose record_ref is not text", func(t *testing.T) {
		const id = "brief-00000000000000000000000000000001"
		row := func(audit, tool string, arguments, request, rows string, brief any) *strictjson.Object {
			return strictjson.NewObject("audit_id", audit, "started_at", "2027-01-15T15:00:00Z", "token_id",
				receipts.Anonymous, "tool", tool, "arguments", arguments, "request_id", request, "brief_id", brief,
				"status", "ok", "http_status", 200, "row_count", 1, "rows", rows, "elapsed_ms", 0.0, "error", nil)
		}
		read := row("r1", "get_account", `{"account_id": "acc-001"}`, "1", `["crm/accounts/acc-001"]`, id)
		readValue, _ := TrustedRows{"r1": read}.Receipts(ctx, []string{"r1"})
		snapshot := strictjson.NewObject("brief_id", id, "account_id", "acc-001",
			"records", []any{strictjson.NewObject("record_ref", 5)}, "receipts", []any{readValue[0]},
			"crm_backend", "sqlite", "crm_binding", evidence.FixtureCRMBinding(), "calls_backend", "fixture",
			"calls_binding", nil, "slack_backend", "fixture", "slack_binding", nil)
		artifact := strictjson.NewObject("schema_version", 3, "brief_id", id, "account_id", "acc-001",
			"sealed_by", "seal1", "text", "", "units", []any{strictjson.NewObject("n", 1, "kind", "record",
				"tool", "get_account", "record_ref", 5)}, "evidence", snapshot)
		text, _ := strictjson.Dumps(artifact, strictjson.Artifact)
		log := TrustedRows{"r1": read, "seal1": row("seal1", "save_brief", `{"account_id": "acc-001", "brief_id": "`+id+`"}`,
			"null", `["`+Reference(id, []byte(text))+`"]`, nil)}
		if _, err := VerifyCard(ctx, []byte(text), id, log, demoSources(nil)); !errors.Is(err, ErrUnresolved) {
			t.Fatalf("got %v", err)
		}
	})
}

// The columns of an audit row besides its ID, and a change to each. A change to a column marked true leaves
// the row a valid receipt, so only the comparison with the sealed copy can refuse it.
var readColumns = []struct {
	name    string
	decodes bool
	change  func(old any) any
}{
	{"started_at", true, func(old any) any { return "2027-02-01T00:00:00Z" }},
	{"token_id", true, func(old any) any {
		if old == receipts.Anonymous {
			return strings.Repeat("c", 64)
		}
		return receipts.Anonymous
	}},
	{"tool", true, func(old any) any {
		if old == "list_contacts" {
			return "get_account"
		}
		return "list_contacts"
	}},
	{"arguments", true, func(old any) any { return `{"account_id": "acc-999"}` }},
	{"request_id", true, func(old any) any { return "999" }},
	{"brief_id", true, func(old any) any {
		if old == nil {
			return "brief-" + strings.Repeat("f", 32)
		}
		return nil
	}},
	{"status", false, func(old any) any {
		if old == "error" {
			return "ok"
		}
		return "error"
	}},
	{"http_status", true, func(old any) any { return 203 }},
	{"row_count", false, func(old any) any { return 99 }},
	{"rows", true, func(old any) any {
		if old == "[]" {
			return `["moved/row"]`
		}
		return strings.Replace(old.(string), `["`, `["moved/`, 1)
	}},
	{"elapsed_ms", true, func(old any) any { return 1.5 }},
	{"error", true, func(old any) any { return "changed" }},
}

// Every read a card seals is compared with its log row in every column but its ID: one column of one row
// changed in the log alone is refused, for each read of each folder.
func TestVerifyComparesEveryColumnOfEveryRead(t *testing.T) {
	ctx := context.Background()
	for _, c := range folders {
		base := openFolder(t, c.name)
		value, err := strictjson.Loads(*base.payload(c.brief))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, _ := get(value.(*strictjson.Object), "evidence").(*strictjson.Object)
		sealed, _ := get(snapshot, "receipts").([]any)
		if len(sealed) < 2 {
			t.Fatalf("%s seals %d reads", c.name, len(sealed))
		}
		for i, r := range sealed {
			id := get(r.(*strictjson.Object), "audit_id").(string)
			if keys := base.Log[id].Keys; len(keys) != len(readColumns)+1 {
				t.Fatalf("%s: a log row has columns %v", c.name, keys)
			}
			for _, column := range readColumns {
				t.Run(fmt.Sprintf("%s/read %d/%s", c.name, i+1, column.name), func(t *testing.T) {
					f := &Folder{Cards: base.Cards, Prepared: base.Prepared, Log: maps.Clone(base.Log)}
					row := strictjson.Clone(base.Log[id]).(*strictjson.Object)
					old, _ := row.Get(column.name)
					changed := column.change(old)
					if evidence.Equal(old, changed) {
						t.Fatalf("%v is not a change", changed)
					}
					row.Set(column.name, changed)
					f.Log[id] = row
					if column.decodes && old != "[]" {
						rows, err := f.Log.Receipts(ctx, []string{id})
						if err != nil {
							t.Fatal(err)
						}
						if _, err := receipts.Decode(rows[0]); err != nil {
							t.Fatalf("the changed row is not a receipt: %v", err)
						}
					}
					if _, err := f.Verify(ctx, c.brief, demoSources(nil)); !errors.Is(err, ErrReads) {
						t.Fatalf("got %v, want %v", err, ErrReads)
					}
				})
			}
		}
	}
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

func TestGoCardsPublishAndVerify(t *testing.T) {
	ctx := context.Background()
	sc := newScope("go cards")
	m, store := sc.maker(demoBook(t), demoSources(nil))
	sealed, err := m.Brief(ctx, Brief{Principal: owner("a bearer", "crm:read", "calls:read", "slack:read"),
		OperationID: "brief", AccountID: "acc-007", AsOf: DemoAsOf, IncludeSlack: true})
	if err != nil {
		t.Fatal(err)
	}
	card, found := store.Published(testWorkspace, sealed.BriefID)
	if !found || len(card.Units) != len(sealed.Units) || card.Units[0].ReceiptID == "" || card.Units[0].ContentVersion == "" {
		t.Fatalf("published %+v", card)
	}
	admitted, err := VerifyCard(ctx, card.Bytes, sealed.BriefID, StoreLog{store, testWorkspace}, demoSources(nil))
	if err != nil {
		t.Fatalf("a Go card: %v", err)
	}
	for i, b := range admitted.Units {
		if b.ReceiptID != card.Units[i].ReceiptID || b.Record.ContentVersion != card.Units[i].ContentVersion {
			t.Fatalf("unit %d: verified as %+v, published as %+v", i+1, b, card.Units[i])
		}
	}
}

// recording is a Store whose operations can fail, which records what reached it.
type recording struct {
	*receipts.Memory
	failStart, failFinishRead, failPublish bool
	published                              int
}

var errUnavailable = errors.New("the receipt store is unavailable")

func (r *recording) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	if r.failStart {
		return receipts.Receipt{}, errUnavailable
	}
	return r.Memory.Start(ctx, s)
}

func (r *recording) FinishRead(ctx context.Context, t receipts.Ticket, v []receipts.RecordVersion, e float64) (receipts.Receipt, error) {
	if r.failFinishRead {
		return receipts.Receipt{}, errUnavailable
	}
	return r.Memory.FinishRead(ctx, t, v, e)
}

func (r *recording) Publish(ctx context.Context, p receipts.Publication) (receipts.Card, error) {
	r.published++
	if r.failPublish {
		return receipts.Card{}, errUnavailable
	}
	return r.Memory.Publish(ctx, p)
}

// countingSource counts its reads, and can change a connection's generation while it reads.
type countingSource struct {
	Source
	reads  int
	during func()
}

func (s *countingSource) Read(tool, accountID string) (string, []*strictjson.Object, error) {
	s.reads++
	if s.during != nil {
		s.during()
	}
	return s.Source.Read(tool, accountID)
}

func TestReceiptsComeFirstAndReleaseLast(t *testing.T) {
	ctx := context.Background()
	brief := Brief{Principal: owner("a bearer", "crm:read", "calls:read"), OperationID: "brief", AccountID: "acc-003",
		AsOf: DemoAsOf}
	setup := func() (*Maker, *recording, *countingSource) {
		m, store := newScope("ordering").maker(demoBook(t), demoSources(nil))
		source := &countingSource{Source: m.Source}
		r := &recording{Memory: store}
		m.Receipts, m.Source = r, source
		return m, r, source
	}
	t.Run("an unavailable start reads nothing", func(t *testing.T) {
		m, r, source := setup()
		r.failStart = true
		if _, err := m.Brief(ctx, brief); !errors.Is(err, errUnavailable) || source.reads != 0 || len(r.All()) != 0 {
			t.Fatalf("got %v after %d reads", err, source.reads)
		}
	})
	t.Run("a failed finish releases nothing", func(t *testing.T) {
		m, r, source := setup()
		r.failFinishRead = true
		sealed, err := m.Brief(ctx, brief)
		if !errors.Is(err, errUnavailable) || sealed != nil || source.reads != 1 || r.published != 0 {
			t.Fatalf("got %v, %v after %d reads and %d publications", sealed, err, source.reads, r.published)
		}
	})
	t.Run("a failed publication finishes the seal as a failed save", func(t *testing.T) {
		m, r, _ := setup()
		r.failPublish = true
		if _, err := m.Brief(ctx, brief); !errors.Is(err, errUnavailable) {
			t.Fatal(err)
		}
		all := r.All()
		seal := all[len(all)-1]
		if seal.Tool != "save_brief" || seal.Status != "error" || *seal.HTTPStatus != 500 ||
			*seal.Error != "brief_save_failed" || len(seal.Rows) != 0 {
			t.Fatalf("the seal: %+v", seal)
		}
	})
	t.Run("a source changed during a read gives no card", func(t *testing.T) {
		m, r, source := setup()
		source.during = func() { r.SetGeneration(testWorkspace, "crm-1", 2) }
		if _, err := m.Brief(ctx, brief); !errors.Is(err, receipts.ErrSourceChanged) || r.published != 0 {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a source changed before publication gives no card", func(t *testing.T) {
		m, r, source := setup()
		source.during = func() {
			if source.reads == 3 {
				r.SetGeneration(testWorkspace, "crm-1", 2)
			}
		}
		if _, err := m.Brief(ctx, brief); !errors.Is(err, receipts.ErrSourceChanged) {
			t.Fatalf("got %v", err)
		}
		all := r.All()
		if seal := all[len(all)-1]; seal.Tool != "save_brief" || *seal.HTTPStatus != 409 {
			t.Fatalf("the seal: %+v", seal)
		}
	})
	t.Run("a revoked grant gives no card", func(t *testing.T) {
		m, r, _ := setup()
		b := brief
		b.Principal.PrincipalID = "principal-1"
		if _, err := m.Brief(ctx, b); !errors.Is(err, receipts.ErrNotPermitted) {
			t.Fatalf("got %v", err)
		}
		r.Grant(testWorkspace, "principal-1", "acc-003")
		b.OperationID = "brief again"
		if _, err := m.Brief(ctx, b); err != nil {
			t.Fatalf("with the grant: %v", err)
		}
	})
}

// steps is a clock that moves on by step each time it is read.
func steps(step time.Duration) func() time.Time {
	now := still()
	return func() time.Time {
		now = now.Add(step)
		return now
	}
}

func TestPrepareRefuses(t *testing.T) {
	ctx := context.Background()
	arguments := strictjson.NewObject("account_id", "acc-007")
	all := []string{"crm:read", "calls:read", "slack:read"}
	run := func(t *testing.T, edit func(m *Maker, p *Prepare), want error) *receipts.Memory {
		t.Helper()
		m, store := newScope("refusals").maker(demoBook(t), demoSources(nil))
		p := prepareOf("a bearer", "acc-007", true, arguments, all...)
		edit(m, &p)
		if sealed, err := m.Prepare(ctx, p); !errors.Is(err, want) || sealed != nil {
			t.Fatalf("got %v, %v; want %v", sealed, err, want)
		}
		outer, _ := store.ByOperation(ctx, testWorkspace, "prepare")
		if outer.Status != "error" || *outer.HTTPStatus != 200 || *outer.Error != "request_failed" {
			t.Fatalf("the outer receipt: %+v", outer)
		}
		return store
	}
	t.Run("an account the selection does not hold", func(t *testing.T) {
		run(t, func(m *Maker, p *Prepare) { p.Selection = Selection{"acc-003": true} }, ErrAccountNotSelected)
	})
	t.Run("an as_of that is not a day", func(t *testing.T) {
		for _, asOf := range []any{"2027-1-20", "20270120", 20270120} {
			run(t, func(m *Maker, p *Prepare) {
				p.Arguments = strictjson.NewObject("account_id", "acc-007", "as_of", asOf)
			}, ErrInvalidAsOf)
		}
	})
	t.Run("the deadline", func(t *testing.T) {
		store := run(t, func(m *Maker, p *Prepare) { m.Now = steps(30 * time.Second) }, ErrDeadline)
		reads := store.All()[1:]
		if len(reads) != 1 || *reads[0].HTTPStatus != 504 {
			t.Fatalf("the reads: %+v", reads)
		}
	})
	t.Run("a token that stops verifying", func(t *testing.T) {
		checks := 0
		store := run(t, func(m *Maker, p *Prepare) {
			p.Principal.Live = func() bool { checks++; return checks < 3 }
		}, ErrInvalidToken)
		reads := store.All()[1:]
		if last := reads[len(reads)-1]; *last.HTTPStatus != 401 {
			t.Fatalf("the last read: %+v", last)
		}
	})
	t.Run("a token that stops verifying before publication", func(t *testing.T) {
		checks := 0
		run(t, func(m *Maker, p *Prepare) {
			p.Principal.Live = func() bool { checks++; return checks <= 4 }
		}, ErrInvalidToken)
	})
	t.Run("a brief over 4 MiB", func(t *testing.T) {
		run(t, func(m *Maker, p *Prepare) {
			book := demoBook(t)
			if err := book.Insert("calls", "call_notes", []any{"note-007z", "acc-007", "2027-01-14T15:00:00Z", nil,
				strings.Repeat("Long words. ", MaxArtifactBytes/12+1)}); err != nil {
				t.Fatal(err)
			}
			m.Source = book
		}, ErrTooLarge)
	})
}

// TestNoWindowBetweenCheckAndOpen swaps a card for a link, or for another file, after it was checked and
// before it is opened.
func TestNoWindowBetweenCheckAndOpen(t *testing.T) {
	for name, swap := range map[string]func(path, elsewhere string) error{
		"a link": func(path, elsewhere string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(elsewhere, path)
		},
		// A link to the very file that was checked passes a comparison of the two; only not following it
		// refuses it.
		"a link to the same file": func(path, _ string) error {
			if err := os.Link(path, path+".same"); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(path+".same", path)
		},
		"another file": func(path, elsewhere string) error {
			data, err := os.ReadFile(elsewhere)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path+".new", data, 0o600); err != nil {
				return err
			}
			return os.Rename(path+".new", path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, source := t.TempDir(), oracletest.Path(t, "folders/cli_card")
			copyTree(t, source, dir)
			card := filepath.Join(dir, "briefs", folders[0].brief+".json")
			betweenCheckAndOpen = func(path string) {
				if path == card {
					if err := swap(card, filepath.Join(source, "briefs", folders[0].brief+".json")); err != nil {
						t.Error(err)
					}
				}
			}
			defer func() { betweenCheckAndOpen = nil }()
			if _, err := OpenTrustedExport(dir); !errors.Is(err, ErrNotRegular) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// TestBriefChecksItsRequestAsPythonDoes: the account first, then the day, with collect_evidence's messages,
// and no receipt for a refused request. A day that is not one is never quoted back: an invented canary stands
// for a real org's name typed where the day belongs.
func TestBriefChecksItsRequestAsPythonDoes(t *testing.T) {
	const canary = "zz-canary-renewal-7Q4"
	for _, c := range []struct{ account, asOf, want string }{
		{"acc-1", "soon", "account ID must have the form acc-001"},
		{"acc-001", "soon", "as-of date must use YYYY-MM-DD"},
		{"acc-001", canary, "as-of date must use YYYY-MM-DD"},
		{"acc-001", "2027-02-30", "as-of date must use YYYY-MM-DD"},
		{"acc-001", "20270115", "as-of date must use YYYY-MM-DD"},
	} {
		m, store := newScope("request").maker(demoBook(t), demoSources(nil))
		_, err := m.Brief(context.Background(), Brief{Principal: owner("t", "crm:read"), OperationID: "b",
			AccountID: c.account, AsOf: c.asOf})
		if err == nil || err.Error() != c.want || strings.Contains(err.Error(), canary) || len(store.All()) != 0 {
			t.Errorf("%s %s: got %v", c.account, c.asOf, err)
		}
	}
	m, _ := newScope("request").maker(demoBook(t), evidence.Sources{CRMBackend: "salesforce"})
	if _, err := m.Brief(context.Background(), Brief{AccountID: "acc-001", AsOf: "soon"}); err == nil ||
		err.Error() != "account ID must use acc-sf- followed by the 18-character Salesforce ID" {
		t.Errorf("a Salesforce account: %v", err)
	}
}
