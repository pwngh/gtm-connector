package cards

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// MaxSelected is the most accounts one selection holds.
const MaxSelected = 100

// The account IDs each CRM backend gives. The classes are spelled out, so only these ASCII characters match.
var selectionAccount = map[string]*regexp.Regexp{
	"sqlite":     regexp.MustCompile(`^acc-[0-9]{3}$`),
	"salesforce": regexp.MustCompile(`^acc-sf-[A-Za-z0-9]{18}$`),
}

const selectionShape = `Use {"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}]} ` +
	"with 1 to 100 distinct accounts."

// ParseSelection reads a selection document (selection.parse_selection): exactly a schema_version of 1 and
// 1 to 100 distinct accounts, each exactly an account_id of the CRM backend and whether its Slack is selected.
func ParseSelection(document []byte, crmBackend string) (Selection, error) {
	pattern, known := selectionAccount[crmBackend]
	if !known {
		return nil, errors.New("Selections support the sqlite and salesforce CRM backends.")
	}
	var value any
	err := errors.New("not UTF-8")
	if utf8.Valid(document) {
		value, err = strictjson.LoadsText(string(document))
	}
	if err != nil {
		return nil, errors.New("The selection file is not valid UTF-8 JSON with unique keys. " + selectionShape)
	}
	root, _ := value.(*strictjson.Object)
	version, _ := members(root, "schema_version", "accounts")["schema_version"].(*big.Int)
	accounts, _ := members(root, "schema_version", "accounts")["accounts"].([]any)
	if version == nil || version.Cmp(big.NewInt(1)) != 0 || len(accounts) < 1 || len(accounts) > MaxSelected {
		return nil, errors.New("The selection file has an unsupported shape. " + selectionShape)
	}
	selection := Selection{}
	for _, entry := range accounts {
		object, _ := entry.(*strictjson.Object)
		fields := members(object, "account_id", "slack")
		account, isText := fields["account_id"].(string)
		slack, isFlag := fields["slack"].(bool)
		if !isText || !isFlag {
			return nil, errors.New("Each selected account needs exactly account_id and slack. " + selectionShape)
		}
		if !pattern.MatchString(account) {
			example := "acc-007"
			if crmBackend == "salesforce" {
				example = "acc-sf- followed by its 18-character ID"
			}
			return nil, fmt.Errorf("A selected account ID does not match the %s CRM. Use %s.", crmBackend, example)
		}
		if _, twice := selection[account]; twice {
			return nil, errors.New("The selection file lists an account twice. " + selectionShape)
		}
		selection[account] = slack
	}
	return selection, nil
}

// members are an object's values by key, when its keys are exactly these. Any other value gives none.
func members(object *strictjson.Object, keys ...string) map[string]any {
	if object == nil || len(object.Keys) != len(keys) {
		return nil
	}
	out := map[string]any{}
	for _, key := range keys {
		value, found := object.Get(key)
		if !found {
			return nil
		}
		out[key] = value
	}
	return out
}
