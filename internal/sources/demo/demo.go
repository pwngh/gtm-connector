// Package demo is the invented CRM, calls and Slack stores of the demo book, and their four reads as the
// gateway's tool bodies give them (gtm_connector/store.py, calls.py and slack.py).
//
// A Store is loaded from a dump of the three SQLite stores, every table with its columns and its rows in
// rowid order, as testdata/oracle/cards/demo_book.json holds them, and takes rows inserted later. Values are
// kept as given: nil, string, or an integer as *big.Int. A read returns its rows ordered by ID, each row's
// columns in table order followed by its record_ref.
package demo

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The tables each read uses, by database.
var schema = map[string][]string{
	"crm":   {"accounts", "contacts", "opportunities", "activities"},
	"calls": {"call_notes"},
	"slack": {"messages"},
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
// and the source they come from.
func (s *Store) Read(tool, accountID string) (source string, records []*strictjson.Object, err error) {
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

// rows is SELECT * FROM table WHERE column = value ORDER BY id, as records.
func (s *Store) rows(database, name, column, value string) []*strictjson.Object {
	t := s.tables[database+"/"+name]
	where := -1
	for i, c := range t.columns {
		if c == column {
			where = i
		}
	}
	var found [][]any
	for _, row := range t.rows {
		if row[where] == value {
			found = append(found, row)
		}
	}
	// SQLite orders TEXT by its bytes, which is Go's string order.
	sort.SliceStable(found, func(i, j int) bool { return found[i][0].(string) < found[j][0].(string) })
	records := make([]*strictjson.Object, len(found))
	for i, row := range found {
		record := &strictjson.Object{Keys: append([]string(nil), t.columns...), Values: append([]any(nil), row...)}
		record.Set("record_ref", fmt.Sprintf("%s/%s/%s", database, name, row[0]))
		records[i] = record
	}
	return records
}
