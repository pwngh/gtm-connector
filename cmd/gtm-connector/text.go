package main

import (
	"fmt"
	"strings"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// The read commands' results as text, in the forms gtm_connector/cli.py prints them. The wording can
// change; scripts read --json.

// human is a result as text.
func (r *run) human(result any) string {
	switch r.command {
	case "accounts":
		rows, whole := result.([]any)
		if !whole {
			rows, _ = get(result.(*strictjson.Object), "accounts").([]any)
		}
		cells := make([][]any, len(rows))
		for i, row := range rows {
			account := row.(*strictjson.Object)
			cells[i] = []any{get(account, "id"), get(account, "name"), get(account, "industry"),
				get(account, "renewal_date")}
		}
		return r.screen.table([]string{"ID", "ACCOUNT", "INDUSTRY", "RENEWAL"}, cells)
	case "brief":
		card := result.(*strictjson.Object)
		brief := get(card, "brief_id").(string)
		// A workspace's name can hold any byte. Escaped, the hint never writes a control character.
		return fmt.Sprintf("%s\nBrief: %s\n%s\n%s", get(card, "text"), brief,
			oneLine("List every unit: gtm-connector inspect "+brief+r.folder()),
			oneLine("Resolve a number: gtm-connector inspect "+brief+" N"+r.folder()))
	case "audit":
		return r.auditTable(result.([]any))
	}
	switch r.action {
	case "account":
		return r.accountText(result.(*strictjson.Object))
	case "brief":
		units := get(result.(*strictjson.Object), "units").([]any)
		if len(units) == 0 {
			return "No sentence on this card has that role."
		}
		lines := make([]string, len(units))
		for i, unit := range units {
			lines[i] = unitLine(unit.(*strictjson.Object))
		}
		return strings.Join(lines, "\n")
	case "numbers":
		tables := []string{}
		for _, traced := range result.([]any) {
			tables = append(tables, r.traceText(traced.(*strictjson.Object)))
		}
		return strings.Join(tables, "\n\n")
	}
	receipt := result.(*strictjson.Object)
	arguments, _ := strictjson.Dumps(get(receipt, "arguments"), strictjson.Quoted)
	rows := []string{}
	for _, row := range get(receipt, "rows").([]any) {
		rows = append(rows, oneLine(cellText(row)))
	}
	if len(rows) == 0 {
		rows = []string{"none"}
	}
	return r.auditTable([]any{receipt}) + "\n\nArguments\n" + arguments + "\n\nReturned rows\n" + strings.Join(rows, "\n")
}

// auditTable is one row per receipt: when it started, its tool, its caller's token ID, how it ended, how
// long it took, its ID and its error. A tool name the connector does not know prints as a quoted literal.
func (r *run) auditTable(receipts []any) string {
	cells := make([][]any, len(receipts))
	for i, value := range receipts {
		receipt := value.(*strictjson.Object)
		tool := get(receipt, "tool")
		if name, _ := tool.(string); mcp.Definitions[name].Scope == "" && name != "save_brief" {
			tool = strictjson.Quote(name)
		}
		elapsed := get(receipt, "elapsed_ms")
		if number, timed := elapsed.(float64); timed {
			elapsed = rounded(number)
		}
		failure := get(receipt, "error")
		if failure == nil {
			failure = ""
		}
		cells[i] = []any{get(receipt, "started_at"), tool, get(receipt, "token_id"), get(receipt, "status"), elapsed,
			get(receipt, "audit_id"), failure}
	}
	return r.screen.table([]string{"TIME", "TOOL", "CALLER", "DECISION", "MS", "RECEIPT", "REASON"}, cells)
}

// accountText is an account's fields, its contacts, and its newest card with the command that lists it.
// A connector ID prints as it is. A CRM value prints as a literal, as on the card.
func (r *run) accountText(account *strictjson.Object) string {
	var fields [][]any
	for i, key := range account.Keys {
		if key == "contacts" || key == "newest_brief_id" {
			continue
		}
		value := account.Values[i]
		if key != "id" {
			value = literal(value)
		}
		name := strings.ReplaceAll(key, "_", " ")
		fields = append(fields, []any{strings.ToUpper(name[:1]) + name[1:], value})
	}
	var contacts [][]any
	for _, value := range get(account, "contacts").([]any) {
		contact := value.(*strictjson.Object)
		contacts = append(contacts, []any{get(contact, "id"), literal(get(contact, "name")),
			literal(get(contact, "title")), literal(get(contact, "email"))})
	}
	text := r.screen.table([]string{"FIELD", "VALUE"}, fields) + "\n\nContacts\n" +
		r.screen.table([]string{"ID", "NAME", "TITLE", "EMAIL"}, contacts)
	if newest, ok := get(account, "newest_brief_id").(string); ok {
		text += "\n\n" + oneLine("Newest card: "+newest+"  (gtm-connector inspect "+newest+r.folder()+")")
	}
	return text
}

// notShown says why a sentence is not on its card, by its shown value.
var notShown = map[string]string{"cut": "cut off", "long": "too long to show", "later": "dated after the card's day",
	"more": "past the card's places"}

// label is a speaker label as a quoted literal, or that there is none.
func label(speaker any) string {
	if text, ok := speaker.(string); ok {
		return strictjson.Quote(text)
	}
	return "no speaker label"
}

// unitLine is one unit of a saved card on one line: a record by its table and ID, a sentence with where,
// who, and why.
func unitLine(unit *strictjson.Object) string {
	var fields []any
	if get(unit, "kind") == "record" {
		parts := strings.SplitN(get(unit, "record_ref").(string), "/", 3)
		fields = []any{get(unit, "n"), "record", parts[1], parts[2]}
	} else {
		// A record's time prints as it is when it is plain, and as a quoted literal otherwise.
		when := "time Missing"
		if at, ok := get(unit, "at").(string); ok && evidence.PlainInstant(at) {
			when = at
		} else if ok {
			when = "time " + strictjson.Quote(at)
		}
		if span, ok := get(unit, "span").(*strictjson.Object); ok {
			// A transcript's lines have no times, and its cues always have them.
			kind := "cues"
			if get(span, "start_ms") == nil {
				kind = "lines"
			}
			when += fmt.Sprintf(" %s %s-%s", kind, cellText(get(span, "first")), cellText(get(span, "last")))
		}
		shown := "shown"
		if state, _ := get(unit, "shown").(string); state != "yes" {
			if shown = notShown[state]; shown == "" {
				shown = "not recorded"
			}
		}
		fields = []any{get(unit, "n"), get(unit, "source"), when, get(unit, "role"), label(get(unit, "speaker")), shown,
			strictjson.Quote(cellText(get(unit, "text")))}
	}
	words := make([]string, len(fields))
	for i, field := range fields {
		words[i] = oneLine(cellText(field))
	}
	return strings.Join(words, "  ")
}

// traceText is one resolved number as a table.
func (r *run) traceText(traced *strictjson.Object) string {
	receipt := get(traced, "receipt").(*strictjson.Object)
	rows := [][]any{{"Number", get(traced, "n")}, {"Kind", get(traced, "kind")}, {"Record", get(traced, "record_ref")},
		{"Receipt", fmt.Sprintf("%s (%s, %s)", cellText(get(receipt, "audit_id")), cellText(get(receipt, "tool")),
			cellText(get(receipt, "status")))}}
	if get(traced, "kind") != "sentence" {
		fields := get(traced, "fields").(*strictjson.Object)
		for i, key := range fields.Keys {
			rows = append(rows, []any{key, literal(fields.Values[i])})
		}
		return r.screen.table([]string{"FIELD", "VALUE"}, rows)
	}
	following := get(traced, "next_by_speaker")
	if following == nil {
		following = "none"
	}
	at := get(traced, "at")
	if at == nil || at == "" {
		at = "Missing"
	}
	onCard := "not recorded"
	if state, _ := get(traced, "shown").(string); state == "yes" {
		onCard = "yes"
	} else if why, known := notShown[state]; known {
		onCard = "no: " + why
	}
	rows = append(rows, []any{"Text", strictjson.Quote(cellText(get(traced, "text")))},
		[]any{"Speaker", label(get(traced, "speaker"))}, []any{"Next by this speaker", following},
		[]any{"Role", fmt.Sprintf("%s (%s)", cellText(get(traced, "role")), cellText(get(traced, "basis")))},
		[]any{"Time", at}, []any{"On the card", onCard})
	if span, ok := get(traced, "span").(*strictjson.Object); ok {
		rows = append(rows, []any{"Cues or lines", cellText(get(span, "first")) + " to " + cellText(get(span, "last"))})
	}
	rows = append(rows, []any{"Source text SHA-256", get(traced, "raw_sha256")},
		[]any{"Neutralizer version", get(traced, "sanitizer_version")})
	return r.screen.table([]string{"FIELD", "VALUE"}, rows)
}
