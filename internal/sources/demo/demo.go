// Package demo is the invented CRM, calls and Slack stores of the demo book, and their four reads as the
// gateway's tool bodies give them.
//
// A Store is loaded from a dump of the three SQLite stores, every table with its columns and its rows in
// rowid order, as testdata/oracle/cards/demo_book.json holds them, and takes rows inserted later. Values are
// kept as given: nil, string, or an integer as *big.Int. A read returns its rows ordered by ID, each row's
// columns in table order followed by its record_ref.
//
// Book is that dump, built into the program; its test keeps it equal to the oracle's.
package demo

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// The tables each read uses, by database.
var schema = map[string][]string{
	"crm":   {"accounts", "contacts", "opportunities", "activities"},
	"calls": {"call_notes"},
	"slack": {"messages"},
}

// Book is the demo stores as the demo seeds them: testdata/oracle/cards/demo_book.json.
//
//go:embed book.json
var Book []byte

// Seeded is a Store holding Book.
func Seeded() (*Store, error) { return Load(Book) }

// Account is one CRM account of a Store.
type Account struct{ ID, Name string }

// Accounts lists the CRM accounts in ID order.
func (s *Store) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tables["crm/accounts"]
	name := -1
	for i, c := range t.columns {
		if c == "name" {
			name = i
		}
	}
	out := make([]Account, len(t.rows))
	for i, row := range t.rows {
		out[i].ID = row[0].(string)
		if name >= 0 {
			out[i].Name, _ = row[name].(string)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// searchRows is how many accounts a search returns (store._SEARCH_ROWS).
const searchRows = 100

// asciiLower is SQLite's lower(): only the letters A to Z change.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// holds is whether a name holds the words of a query in their order, each as literal text, with anything
// between them, so a name written with two spaces is found by its words. Only the letters A to Z fold. A
// query with no word matches every name.
func holds(name, query string) bool {
	rest := asciiLower(name)
	for _, word := range strings.FieldsFunc(asciiLower(query), unicodetab.IsSpace) {
		at := strings.Index(rest, word)
		if at < 0 {
			return false
		}
		rest = rest[at+len(word):]
	}
	return true
}

// SearchAccounts is the accounts whose name holds the query's words (see holds), ordered by name without
// ASCII case and then by ID. It returns the first 100, each with its columns in table order, and whether
// more matched.
func (s *Store) SearchAccounts(query string) (accounts []*strictjson.Object, more bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tables["crm/accounts"]
	name := -1
	for i, c := range t.columns {
		if c == "name" {
			name = i
		}
	}
	type match struct {
		folded string
		row    []any
	}
	var found []match
	for _, row := range t.rows {
		text := ""
		if name >= 0 {
			text, _ = row[name].(string)
		}
		if holds(text, query) {
			found = append(found, match{asciiLower(text), row})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].folded != found[j].folded {
			return found[i].folded < found[j].folded
		}
		return found[i].row[0].(string) < found[j].row[0].(string)
	})
	if more = len(found) > searchRows; more {
		found = found[:searchRows]
	}
	accounts = make([]*strictjson.Object, len(found))
	for i, m := range found {
		accounts[i] = &strictjson.Object{Keys: append([]string(nil), t.columns...), Values: append([]any(nil), m.row...)}
	}
	return accounts, more
}

// GetAccount is store.get_account: the account's columns and then its contacts, ordered by ID, or false
// when the book holds no such account.
func (s *Store) GetAccount(accountID string) (*strictjson.Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := s.rows("crm", "accounts", "id", accountID)
	if len(found) == 0 {
		return nil, false
	}
	account := found[0]
	account.Delete("record_ref")
	contacts := []any{}
	for _, contact := range s.rows("crm", "contacts", "account_id", accountID) {
		contact.Delete("record_ref")
		contacts = append(contacts, contact)
	}
	account.Set("contacts", contacts)
	return account, true
}

// ErrAccountNotFound is get_account's refusal of an unknown account.
var ErrAccountNotFound = errors.New("account not found")

type table struct {
	columns []string
	rows    [][]any
}

// Store holds the demo stores in memory.
type Store struct {
	mu     sync.RWMutex
	tables map[string]*table // by "database/table"
}

// Load reads a dump of the demo stores: {"crm": {"accounts": {"columns": [...], "rows": [[...]]}, ...},
// "calls": {...}, "slack": {...}}. Other top-level members are notes and are ignored.
func Load(document []byte) (*Store, error) {
	value, err := strictjson.Loads(document)
	if err != nil {
		return nil, err
	}
	root, ok := value.(*strictjson.Object)
	if !ok {
		return nil, errors.New("demo: the dump is not an object")
	}
	s := &Store{tables: map[string]*table{}}
	for database, names := range schema {
		tables, _ := root.Get(database)
		object, ok := tables.(*strictjson.Object)
		if !ok {
			return nil, fmt.Errorf("demo: the dump has no %s store", database)
		}
		for _, name := range names {
			dumped, _ := object.Get(name)
			t, err := readTable(dumped)
			if err != nil {
				return nil, fmt.Errorf("demo: %s/%s: %w", database, name, err)
			}
			s.tables[database+"/"+name] = t
		}
	}
	return s, nil
}

func readTable(value any) (*table, error) {
	object, ok := value.(*strictjson.Object)
	if !ok {
		return nil, errors.New("missing")
	}
	columns, _ := object.Get("columns")
	rows, _ := object.Get("rows")
	names, ok1 := columns.([]any)
	list, ok2 := rows.([]any)
	if !ok1 || !ok2 {
		return nil, errors.New("needs columns and rows")
	}
	t := &table{}
	for _, name := range names {
		text, ok := name.(string)
		if !ok {
			return nil, errors.New("a column name is not a string")
		}
		t.columns = append(t.columns, text)
	}
	for _, row := range list {
		values, ok := row.([]any)
		if !ok || len(values) != len(t.columns) {
			return nil, errors.New("a row does not match the columns")
		}
		if err := check(values); err != nil {
			return nil, err
		}
		t.rows = append(t.rows, values)
	}
	return t, nil
}

// check refuses a row the stores cannot hold: an ID that is not a string, or a value that is not nil, a
// string or an integer.
func check(values []any) error {
	if len(values) == 0 {
		return errors.New("demo: a row has no values")
	}
	if _, ok := values[0].(string); !ok {
		return fmt.Errorf("demo: the ID %v is not a string", values[0])
	}
	for _, v := range values {
		switch n := v.(type) {
		case *big.Int:
			if n == nil {
				return errors.New("demo: a nil integer")
			}
		case nil, string:
		default:
			return fmt.Errorf("a value of type %T is not a demo store value", v)
		}
	}
	return nil
}

// Insert adds one row to a table, as INSERT INTO table VALUES (...) does. The ID must be new.
func (s *Store) Insert(database, name string, values []any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tables[database+"/"+name]
	if !ok {
		return fmt.Errorf("demo: no table %s/%s", database, name)
	}
	if len(values) != len(t.columns) {
		return fmt.Errorf("demo: %s/%s takes %d values", database, name, len(t.columns))
	}
	if err := check(values); err != nil {
		return err
	}

	for _, row := range t.rows {
		if row[0] == values[0] {
			return fmt.Errorf("demo: %s/%s already has %v", database, name, values[0])
		}
	}
	t.rows = append(t.rows, append([]any(nil), values...))
	return nil
}

// Read is one of the four source reads a card makes, for one account: the records with their record_ref,
// and the source they come from. The book is in memory, so no read waits on ctx.
func (s *Store) Read(_ context.Context, tool, accountID string) (source string, records []*strictjson.Object, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch tool {
	case "get_account":
		account := s.rows("crm", "accounts", "id", accountID)
		if len(account) == 0 {
			return "", nil, ErrAccountNotFound
		}
		records = append(account[:1], s.rows("crm", "contacts", "account_id", accountID)...)
		return "crm", records, nil
	case "list_opportunities":
		return "crm", s.rows("crm", "opportunities", "account_id", accountID), nil
	case "read_call_notes":
		return "calls", s.rows("calls", "call_notes", "account_id", accountID), nil
	case "read_slack_messages":
		return "slack", s.rows("slack", "messages", "account_id", accountID), nil
	}
	return "", nil, fmt.Errorf("demo: no read %q", tool)
}

// SearchRows is how many accounts one search returns.
const SearchRows = 100

// Search is search_accounts on the demo CRM: the accounts whose name holds the query's words (see holds),
// ordered by name without ASCII case and then by ID. It returns the first SearchRows of them and whether
// more matched. accounts, when not nil, limits the search to those IDs before
// the order and the bound.
func (s *Store) Search(query string, accounts []string) (records []*strictjson.Object, more bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tables["crm/accounts"]
	name := t.column("name")
	var found [][]any
	for _, row := range t.rows {
		text, _ := row[name].(string)
		if holds(text, query) && (accounts == nil || slices.Contains(accounts, row[0].(string))) {
			found = append(found, row)
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := asciiLower(found[i][name].(string)), asciiLower(found[j][name].(string))
		if a != b {
			return a < b
		}
		return found[i][0].(string) < found[j][0].(string)
	})
	if more = len(found) > SearchRows; more {
		found = found[:SearchRows]
	}
	return t.records("crm", "accounts", found), more
}

// Opportunities is list_opportunities without an account (store.list_opportunities): every opportunity in ID
// order, or those of accounts when it is not nil.
func (s *Store) Opportunities(accounts []string) []*strictjson.Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tables["crm/opportunities"]
	account := t.column("account_id")
	var found [][]any
	for _, row := range t.rows {
		if id, _ := row[account].(string); accounts == nil || slices.Contains(accounts, id) {
			found = append(found, row)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i][0].(string) < found[j][0].(string) })
	return t.records("crm", "opportunities", found)
}

// column is the position of a named column.
func (t *table) column(name string) int {
	for i, c := range t.columns {
		if c == name {
			return i
		}
	}
	return -1
}

// records are rows as a read returns them: the columns in table order, then the record_ref.
func (t *table) records(database, name string, rows [][]any) []*strictjson.Object {
	records := make([]*strictjson.Object, len(rows))
	for i, row := range rows {
		record := &strictjson.Object{Keys: append([]string(nil), t.columns...), Values: append([]any(nil), row...)}
		record.Set("record_ref", fmt.Sprintf("%s/%s/%s", database, name, row[0]))
		records[i] = record
	}
	return records
}

// rows is SELECT * FROM table WHERE column = value ORDER BY id, as records.
func (s *Store) rows(database, name, column, value string) []*strictjson.Object {
	t := s.tables[database+"/"+name]
	where := t.column(column)
	var found [][]any
	for _, row := range t.rows {
		if row[where] == value {
			found = append(found, row)
		}
	}
	// SQLite orders TEXT by its bytes, which is Go's string order.
	sort.SliceStable(found, func(i, j int) bool { return found[i][0].(string) < found[j][0].(string) })
	return t.records(database, name, found)
}
