package cards

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Each document below with what selection.parse_selection answered to it: the selection, or its refusal
// word for word.
func TestParseSelectionAsPython(t *testing.T) {
	const shape = ` Use {"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}]} with 1 to 100 distinct accounts.`
	const (
		notJSON   = "The selection file is not valid UTF-8 JSON with unique keys." + shape
		badShape  = "The selection file has an unsupported shape." + shape
		badEntry  = "Each selected account needs exactly account_id and slack." + shape
		twice     = "The selection file lists an account twice." + shape
		notSQLite = "A selected account ID does not match the sqlite CRM. Use acc-007."
	)
	many := func(n int) string {
		entries := make([]string, n)
		for i := range entries {
			entries[i] = fmt.Sprintf(`{"account_id": "acc-%03d", "slack": false}`, i)
		}
		return `{"schema_version": 1, "accounts": [` + strings.Join(entries, ", ") + `]}`
	}
	one := `[{"account_id": "acc-007", "slack": true}]`
	for _, c := range []struct {
		backend, document string
		want              Selection
		refusal           string
	}{
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}, {"account_id": "acc-012", "slack": false}]}`,
			Selection{"acc-007": true, "acc-012": false}, ""},
		{"salesforce", `{"schema_version": 1, "accounts": [{"account_id": "acc-sf-001000000000001AAA", "slack": false}]}`,
			Selection{"acc-sf-001000000000001AAA": false}, ""},
		{"sqlite", `{"accounts": [{"slack": true, "account_id": "acc-007"}], "schema_version": 1}`, Selection{"acc-007": true}, ""},
		{"hubspot", `{"schema_version": 1, "accounts": ` + one + `}`, nil, "Selections support the sqlite and salesforce CRM backends."},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}`, nil, notJSON},
		{"sqlite", "\xff" + `{"schema_version": 1}`, nil, notJSON},
		{"sqlite", `{"schema_version": 1, "schema_version": 1, "accounts": ` + one + `}`, nil, notJSON},
		{"sqlite", "\ufeff" + `{"schema_version": 1, "accounts": ` + one + `}`, nil, notJSON},
		{"sqlite", `[]`, nil, badShape},
		{"sqlite", `{"schema_version": 2, "accounts": ` + one + `}`, nil, badShape},
		{"sqlite", `{"schema_version": 1.0, "accounts": ` + one + `}`, nil, badShape},
		{"sqlite", `{"schema_version": true, "accounts": ` + one + `}`, nil, badShape},
		{"sqlite", `{"schema_version": 1, "accounts": []}`, nil, badShape},
		{"sqlite", `{"schema_version": 1, "accounts": {"acc-007": true}}`, nil, badShape},
		{"sqlite", `{"schema_version": 1, "accounts": ` + one + `, "note": "x"}`, nil, badShape},
		{"sqlite", `{"schema_version": 1}`, nil, badShape},
		{"sqlite", many(101), nil, badShape},
		{"sqlite", `{"schema_version": 1, "accounts": ["acc-007"]}`, nil, badEntry},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007"}]}`, nil, badEntry},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": 1}]}`, nil, badEntry},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": 7, "slack": true}]}`, nil, badEntry},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true, "calls": true}]}`, nil, badEntry},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-0077", "slack": true}]}`, nil, notSQLite},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007\n", "slack": true}]}`, nil, notSQLite},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-00` + "\u0667" + `", "slack": true}]}`, nil, notSQLite},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-sf-001000000000001AAA", "slack": true}]}`, nil, notSQLite},
		{"salesforce", `{"schema_version": 1, "accounts": ` + one + `}`, nil,
			"A selected account ID does not match the salesforce CRM. Use acc-sf- followed by its 18-character ID."},
		{"sqlite", `{"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}, {"account_id": "acc-007", "slack": false}]}`, nil, twice},
	} {
		got, err := ParseSelection([]byte(c.document), c.backend)
		refusal := ""
		if err != nil {
			refusal = err.Error()
		}
		if refusal != c.refusal || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %.80q:\n got %v %q\nwant %v %q", c.backend, c.document, got, refusal, c.want, c.refusal)
		}
	}
	if full, err := ParseSelection([]byte(many(MaxSelected)), "sqlite"); err != nil || len(full) != MaxSelected {
		t.Errorf("a selection of %d accounts: %d (%v)", MaxSelected, len(full), err)
	}
}
