package main

import (
	"slices"
	"strings"
)

// The commands take their arguments as gtm_connector/cli.py's argparse parser does, and refuse them with
// its messages. What was typed is never repeated back: it may be a URL or a username.

// option is one option of a command.
type option struct {
	name     string
	value    bool     // whether it takes one argument
	choices  []string // the values it takes, when they are fixed
	repeat   bool     // whether it may be given again, each time adding its argument to a list
	required bool     // whether the command needs it
}

// arguments is how one command reads its command line.
type arguments struct {
	options  []option // after --state-dir, which every command takes
	required string   // the name of the one positional argument it needs, or ""
	numbers  bool     // whether card numbers may follow that argument
	optional string   // the name of the one positional argument it may be given, or ""
	choices  []string // the values the required or the optional argument takes, when they are fixed
	help     string
}

// The options every command takes. --json is read before the rest and taken out, so here it is only a name
// a shortened option can be confused with.
var (
	helpOption  = option{name: "--help"}
	jsonOption  = option{name: "--json"}
	stateOption = option{name: "--state-dir", value: true}
)

var commands = map[string]arguments{
	"accounts": {options: []option{{name: "--query", value: true}}, help: accountsHelp},
	"brief": {options: []option{{name: "--as-of", value: true}, {name: "--without-calls"}}, required: "account_id",
		help: briefHelp},
	"inspect": {options: []option{{name: "--role", value: true, choices: []string{"customer", "seller", "unknown"}}},
		required: "ID", numbers: true, help: inspectHelp},
	"audit": {options: []option{{name: "--brief", value: true}, {name: "--tool", value: true},
		{name: "--caller", value: true}, {name: "--status", value: true, choices: []string{"ok", "error", "pending"}},
		{name: "--since", value: true}, {name: "--until", value: true}, {name: "--follow"}}, help: auditHelp},
	"connect": {options: []option{
		{name: "--scope", value: true, repeat: true, choices: []string{"crm:read", "calls:read", "crm:write", "slack:read"}},
		{name: "--org", value: true},
		{name: "--environment", value: true, choices: []string{"developer", "sandbox", "production"}},
		{name: "--seller", value: true, repeat: true}, {name: "--remove"},
		{name: "--transport", value: true, choices: []string{"http", "stdio"}}, {name: "--config-file", value: true}},
		optional: "provider", choices: append(append([]string{"status"}, providerNames()...), assistantApps...),
		help: connectHelp},
	"demo": {options: []option{{name: "--again"}}, help: demoHelp},
}

// parsed is one command line, read.
type parsed struct {
	help        bool
	values      map[string]string   // each option given, by name; an option without an argument holds ""
	lists       map[string][]string // the arguments of each option that may be given again
	positionals []string
}

// text is an option's argument and whether the option was given.
func (p parsed) text(name string) (string, bool) {
	value, given := p.values[name]
	return value, given
}

// usageError is argparse's refusal of a command line.
type usageError string

func (e usageError) Error() string { return string(e) }

// withoutJSON takes --json out of a command line and says whether it was there. It may stand anywhere
// before "--"; after that it is an argument like any other.
func withoutJSON(tokens []string) (rest []string, asJSON bool) {
	end := len(tokens)
	for i, token := range tokens {
		if token == "--" {
			end = i
			break
		}
	}
	for _, token := range tokens[:end] {
		if token == "--json" {
			asJSON = true
			continue
		}
		rest = append(rest, token)
	}
	return append(rest, tokens[end:]...), asJSON
}

// negativeNumber is whether a token opens like a number below zero, "-5" or "-.5", which argparse reads as
// an argument and not as an option.
func negativeNumber(token string) bool {
	digits := strings.TrimPrefix(strings.TrimPrefix(token, "-"), ".")
	return len(digits) < len(token) && digits != "" && digits[0] >= '0' && digits[0] <= '9'
}

// named reads a token as argparse does: whether it is an option, the options its name is or begins, and
// the argument joined to it with "=". An option may be shortened to any beginning. A number below zero,
// and a text with a space in it that names no option, are arguments.
func named(options []option, token string) (isOption bool, found []option, attached string, hasAttached bool) {
	if len(token) < 2 || token[0] != '-' {
		return false, nil, "", false
	}
	if token == "-h" {
		return true, []option{helpOption}, "", false
	}
	name, attached, hasAttached := strings.Cut(token, "=")
	for _, o := range options {
		if o.name == name {
			return true, []option{o}, attached, hasAttached
		}
		if strings.HasPrefix(name, "--") && strings.HasPrefix(o.name, name) {
			found = append(found, o)
		}
	}
	if len(found) == 0 && (negativeNumber(token) || strings.Contains(token, " ")) {
		return false, nil, "", false
	}
	return true, found, attached, hasAttached
}

func quotedChoices(choices []string) string {
	return "'" + strings.Join(choices, "', '") + "'"
}

// parse reads one command's arguments, after the command's name and with --json taken out.
func (a arguments) parse(tokens []string) (parsed, error) {
	p := parsed{values: map[string]string{}, lists: map[string][]string{}}
	options := append([]option{helpOption, jsonOption, stateOption}, a.options...)
	unrecognized, literal := false, false
	for i := 0; i < len(tokens); {
		token := tokens[i]
		i++
		if !literal && token == "--" {
			literal = true
			continue
		}
		isOption, found, value, hasAttached := named(options, token)
		switch {
		case literal || !isOption:
			// The argument with fixed values is checked where it stands, before what follows it.
			if a.optional != "" && len(p.positionals) == 0 && !slices.Contains(a.choices, token) {
				return p, usageError("argument " + a.optional + ": invalid choice (choose from " +
					quotedChoices(a.choices) + ")")
			}
			p.positionals = append(p.positionals, token)
			continue
		case len(found) == 0:
			unrecognized = true
			continue
		case len(found) > 1:
			names := make([]string, len(found))
			for n, o := range found {
				names[n] = o.name
			}
			return p, usageError("ambiguous option, which could match " + strings.Join(names, ", "))
		}
		o := found[0]
		switch {
		case !o.value && hasAttached:
			return p, usageError("argument " + o.name + ": ignored explicit argument")
		case o.name == helpOption.name:
			p.help = true
			return p, nil
		case o.value && !hasAttached:
			if i == len(tokens) || tokens[i] == "--" {
				return p, usageError("argument " + o.name + ": expected one argument")
			}
			if next, _, _, _ := named(options, tokens[i]); next {
				return p, usageError("argument " + o.name + ": expected one argument")
			}
			value = tokens[i]
			i++
		}
		if o.choices != nil && !slices.Contains(o.choices, value) {
			return p, usageError("argument " + o.name + ": invalid choice (choose from " + quotedChoices(o.choices) + ")")
		}
		p.values[o.name] = value
		if o.repeat {
			p.lists[o.name] = append(p.lists[o.name], value)
		}
	}
	taken := 0
	var missing []string
	if a.required != "" {
		switch {
		case len(p.positionals) == 0:
			missing = append(missing, a.required)
		case a.choices != nil && !slices.Contains(a.choices, p.positionals[0]):
			return p, usageError("argument " + a.required + ": invalid choice (choose from " + quotedChoices(a.choices) + ")")
		}
		taken = 1
	}
	for _, o := range a.options {
		if _, given := p.values[o.name]; o.required && !given {
			missing = append(missing, o.name)
		}
	}
	if len(missing) > 0 {
		return p, usageError("the following arguments are required: " + strings.Join(missing, ", "))
	}
	if a.optional != "" {
		taken = min(1, len(p.positionals))
	}
	if a.numbers {
		for _, number := range p.positionals[taken:] {
			if !cardNumber(number) {
				return p, usageError("argument N: a number printed on the card, such as 5")
			}
		}
		taken = len(p.positionals)
	}
	if unrecognized || len(p.positionals) > taken {
		return p, usageError("unrecognized arguments")
	}
	return p, nil
}

// cardNumber is whether a text is a number written in ASCII digits. One the card does not have is refused
// by the card, which names its range.
func cardNumber(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	if digits == "" {
		return false
	}
	for _, c := range []byte(digits) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

const accountsHelp = `usage: gtm-connector accounts [-h] [--json] [--state-dir STATE_DIR]
                              [--query QUERY]

Search the connected CRM by account name.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --query QUERY         Match this literal part of an account name.

Example: gtm-connector accounts --query Thornfield
`

const briefHelp = `usage: gtm-connector brief [-h] [--json] [--state-dir STATE_DIR]
                           [--as-of AS_OF] [--without-calls]
                           account_id

Print the Deal card for one account from the connected sources.

positional arguments:
  account_id            Select an account, such as acc-003.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --as-of AS_OF         Use YYYY-MM-DD for renewal planning. Demo data uses
                        2027-01-15.
  --without-calls       Omit the calls:read grant.

Example: gtm-connector brief acc-003
`

const inspectHelp = `usage: gtm-connector inspect [-h] [--json] [--state-dir STATE_DIR]
                             [--role {customer,seller,unknown}]
                             ID [N ...]

Show an account, a saved card, numbers on it, or an audit receipt.

positional arguments:
  ID                    An account ID such as acc-003, a brief ID, or a
                        receipt ID from audit.
  N                     With a brief ID: resolve these numbers printed on its
                        card. Give them together, after the ID.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --role {customer,seller,unknown}
                        With a brief ID alone: list only the sentences of this
                        speaker role.

Examples:
  gtm-connector inspect acc-003
  gtm-connector inspect BRIEF_ID
  gtm-connector inspect BRIEF_ID --role customer
  gtm-connector inspect BRIEF_ID 5 6
  gtm-connector inspect RECEIPT_ID
`

const auditHelp = `usage: gtm-connector audit [-h] [--json] [--state-dir STATE_DIR]
                           [--brief BRIEF] [--tool TOOL] [--caller CALLER]
                           [--status {ok,error,pending}] [--since SINCE]
                           [--until UNTIL] [--follow]

Read tool-call receipts from the audit log.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Read this workspace (default: demo).
  --brief BRIEF         Select this brief or run correlation ID.
  --tool TOOL           Select the exact tool name.
  --caller CALLER       Select a full credential fingerprint or anonymous.
  --status {ok,error,pending}
                        Select this receipt outcome.
  --since SINCE         Include calls at or after this ISO timestamp with a
                        UTC offset.
  --until UNTIL         Include calls before this ISO timestamp with a UTC
                        offset.
  --follow              Follow new or changed results until Ctrl-C.

Example: gtm-connector audit --tool save_brief
`

const connectHelp = `usage: gtm-connector connect [-h] [--json] [--state-dir STATE_DIR]
                             [--scope {crm:read,calls:read,crm:write,slack:read}]
                             [--org ORG]
                             [--environment {developer,sandbox,production}]
                             [--seller SELLER] [--remove]
                             [--transport {http,stdio}]
                             [--config-file CONFIG_FILE]
                             [{status,demo,salesforce,calls,slack,inspector,claude-code,claude-desktop,cursor}]

Connect a source or an assistant app, or show what is connected.

positional arguments:
  {status,demo,salesforce,calls,slack,inspector,claude-code,claude-desktop,cursor}
                        Connect demo data, Salesforce, imported call
                        transcripts, or the saved Slack sign-in, or print the
                        setup of an assistant app. status, the default, shows
                        what is connected.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --scope {crm:read,calls:read,crm:write,slack:read}
                        With an app: grant this scope. Repeat for each.
  --org ORG             With salesforce: read credentials from this signed-in
                        sf CLI alias.
  --environment {developer,sandbox,production}
                        With salesforce: require this environment (default:
                        developer).
  --seller SELLER       With salesforce, calls, or slack: list this name or
                        Slack ID as a seller. Repeat for each. The list
                        replaces the saved one.
  --remove              Detach this source. Removing the CRM removes its calls
                        and Slack.
  --transport {http,stdio}
                        With an app: use a supplied HTTP grant (default) or a
                        local stdio bridge.
  --config-file CONFIG_FILE
                        With --transport stdio, merge the connector's entry
                        into this client configuration file. A backup is saved
                        first.

Examples:
  gtm-connector connect demo
  gtm-connector connect status
  gtm-connector connect demo --remove

This build connects demo data, imported call transcripts, and the saved Slack
sign-in beside a real CRM. It refuses salesforce and the assistant apps, and
never reads demo data in their place.
`

const demoHelp = `usage: gtm-connector demo [-h] [--json] [--state-dir STATE_DIR] [--again]

Open one invented law-firm renewal as a Deal card, or publish the demo book.

options:
  -h, --help            show this help message and exit
  --json                With --state-dir: write the documented JSON result for
                        scripts.
  --state-dir STATE_DIR
                        Open the renewal in this workspace: look the account
                        up, show a read refused for a grant without
                        calls:read, and print its card.
  --again               Without --state-dir: publish a new card for every
                        account, also those that hold one.

Without --state-dir, demo publishes a card for each account of the demo book
that holds none, for the demo sign-in of the web app.

Examples:
  gtm-connector demo
  gtm-connector demo --state-dir renewal-demo
`
