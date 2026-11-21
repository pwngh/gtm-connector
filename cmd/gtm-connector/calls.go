package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/unicodetab"
)

// debug calls: import one transcript export for one account, list the
// workspace's imports, or inspect one. An import reads the file and checks it before anything else is opened,
// then resolves the account through the workspace's CRM as a tool call under its own receipt, and saves the
// import bound to that receipt. list and inspect read the saved imports only: no source and no file.

// callsArgumentsRefused is the refusal of any command line debug calls cannot read.
const callsArgumentsRefused = "The calls arguments do not match this action. Check its required arguments and options."

// callsActions are the actions of debug calls, each with how it reads its command line.
var callsActions = map[string]arguments{
	"import": {options: []option{{name: "--account", value: true}, {name: "--account-number", value: true},
		{name: "--called-at", value: true}, {name: "--speaker-names"}, {name: "--cues", value: true},
		{name: "--lines", value: true}, {name: "--reassign"}}, required: "FILE", help: callsImportHelp},
	"list":    {options: []option{{name: "--account", value: true}, {name: "--after", value: true}}, help: callsListHelp},
	"inspect": {options: []option{{name: "--full"}}, required: "IMPORT_ID", help: callsInspectHelp},
}

// callFailures are what each refusal of the calls source says (cli.CALL_FAILURES).
var callFailures = map[string]string{
	"calls_import_invalid":   "The export or range is invalid. Use a valid UTF-8 TXT, VTT, or SRT transcript and range.",
	"calls_import_too_large": "The export exceeds 2 MiB or 10,000 units. Choose a smaller export.",
	"calls_excerpt_too_large": "The excerpt exceeds its text, unit, or metadata limit. Select fewer cues or " +
		"lines.",
	"calls_speaker_names_missing": "Not every line or cue of the file opens with a name and a colon. Import it " +
		"without --speaker-names.",
	"calls_import_not_found": "This import is not in the selected state folder. Run debug calls list to find its " +
		"identifier.",
	"calls_account_not_found": "No CRM account matched the selection. Check its ID or exact AccountNumber.",
	"calls_reassign_required": "This content belongs to another account. Use --reassign only to move that " +
		"association.",
	"calls_association_changed": "AccountNumber now resolves to another account. Review it before using --reassign.",
	"calls_source_mismatch":     "The calls source or CRM binding differs from this state. Connect its original sources.",
	"calls_account_ambiguous": "AccountNumber did not resolve to one literal match. Select its canonical ID with " +
		"--account.",
	"calls_import_corrupt": "The import failed integrity checks. Retain this state and reimport into a fresh folder.",
	"calls_association_invalid": "The CRM response and audit receipt did not agree. No transcript was " +
		"imported.",
	"calls_association_failed": "The authorized CRM read did not complete. No transcript was imported.",
	"calls_source_unavailable": "The local CRM connection could not be reached. Check the setup and repeat the " +
		"import.",
	"calls_import_io": "The export or private store could not be read or written. Check permissions and retry.",
}

// callsProgress is what an import says on stderr as it reaches each step.
var callsProgress = map[string]string{
	"reading_export":    "Reading the transcript export.",
	"preparing_state":   "Preparing the connected CRM and private state.",
	"resolving_account": "Resolving the account through an audited CRM read.",
	"saving_import":     "Saving the private transcript selection.",
}

// runCalls runs debug calls with the arguments after "calls", and gives its exit code.
func runCalls(ctx context.Context, tokens []string, s screen) int {
	const command = "debug calls"
	misread := func() int {
		next := "gtm-connector debug calls --help"
		if _, known := callsActions[first(tokens)]; known {
			next = "gtm-connector debug calls " + tokens[0] + " --help"
		}
		return s.fail(command, &failure{code: 2, message: callsArgumentsRefused, next: next})
	}
	// The options of debug calls itself come before its action.
	name, named := "", false
	i := 0
	for ; i < len(tokens); i++ {
		isOption, found, value, attached := namedOption(tokens[i])
		if !isOption {
			break
		}
		if len(found) != 1 || found[0].name == jsonOption.name {
			return misread()
		}
		if found[0].name == helpOption.name {
			fmt.Fprint(s.out, callsHelp)
			return 0
		}
		if !attached {
			if i++; i == len(tokens) {
				return misread()
			}
			if next, _, _, _ := namedOption(tokens[i]); next {
				return misread()
			}
			value = tokens[i]
		}
		name, named = value, true
	}
	spec, known := callsActions[first(tokens[i:])]
	if !known {
		return misread()
	}
	action := tokens[i]
	args, err := spec.parse(tokens[i+1:])
	if err != nil {
		return misread()
	}
	if args.help {
		fmt.Fprint(s.out, spec.help)
		return 0
	}
	_, byID := args.text("--account")
	_, byNumber := args.text("--account-number")
	_, cues := args.text("--cues")
	_, lines := args.text("--lines")
	if action == "import" && (byID == byNumber || cues && lines) {
		return misread()
	}
	r := &run{command: command, args: args, screen: s, name: defaultWorkspace, action: action}
	if given, has := args.text("--state-dir"); has {
		name, named = given, true
	}
	if named {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	var result *strictjson.Object
	if action == "import" {
		result, err = r.importTranscript(ctx)
	} else {
		result, err = r.savedImports(ctx)
	}
	if err != nil {
		return s.fail(command, r.callsFailed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.callsFailed(ctx, err))
		}
		fmt.Fprintln(s.out, inertFormat(text))
		return 0
	}
	fmt.Fprintln(s.out, r.callsText(result))
	return 0
}

// inertFormat writes each format character (category Cf) of a JSON text, such as a bidirectional control
// from a transcript, as its \u escape, so that it cannot reorder what a terminal shows. JSON holds such a
// character only inside a string, where the escape reads back as the same character.
func inertFormat(text string) string {
	var b strings.Builder
	for _, c := range text {
		if unicodetab.Category(c) != "Cf" {
			b.WriteRune(c)
			continue
		}
		for _, unit := range utf16.Encode([]rune{c}) {
			fmt.Fprintf(&b, `\u%04x`, unit)
		}
	}
	return b.String()
}

// first is the first of a list of words, or "".
func first(words []string) string {
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// namedOption reads one word before the action of debug calls, which takes --help and --state-dir there.
func namedOption(token string) (isOption bool, found []option, value string, attached bool) {
	return named([]option{helpOption, jsonOption, stateOption}, token)
}

// callsUsage is a refusal of what was asked, before anything is opened.
func callsUsage(message string) *failure {
	return &failure{code: 2, message: message, next: "gtm-connector debug calls --help"}
}

// callsFailed is an error of debug calls as it is reported: a refusal of the calls source by its code and
// its fixed sentence, any other as the read commands report it.
func (r *run) callsFailed(ctx context.Context, err error) *failure {
	var refusal *calls.Error
	if ctx.Err() == nil && errors.As(err, &refusal) {
		next := "gtm-connector debug calls import --help"
		switch refusal.Code {
		case calls.ErrNotFound.Code:
			next = "gtm-connector debug calls list" + r.folder()
		case calls.ErrAccountNotFound.Code, calls.ErrAccountAmbiguous.Code:
			next = "gtm-connector accounts --help"
		}
		return &failure{code: refusal.ExitCode(), message: refusal.Code + ": " + callFailures[refusal.Code], next: next}
	}
	return r.failed(ctx, err)
}

// progress says on stderr which step an import has reached: a line, or with --json one object.
func (r *run) progress(step string) {
	if !r.screen.json {
		fmt.Fprintln(r.screen.err, callsProgress[step])
		return
	}
	text, _ := strictjson.Dumps(strictjson.NewObject("progress", strictjson.NewObject("command", "debug calls import",
		"step", step)), strictjson.DumpOptions{EnsureASCII: true})
	fmt.Fprintln(r.screen.err, text)
}

// importTranscript is debug calls import. The workspace must have a CRM and imported calls connected. The
// file is read and parsed, and its excerpt selected, before any source is opened. The account is then
// resolved through the CRM under a receipt of its own, and the import is saved bound to that receipt.
func (r *run) importTranscript(ctx context.Context) (*strictjson.Object, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	if kind := from.kinds["calls"]; kind == nil || kind.provider != "calls" {
		return nil, &failure{code: 2, next: "gtm-connector connect --help",
			message: "Transcripts are not connected. Run gtm-connector connect calls" + r.here() + " first."}
	}
	made, err := r.preparedImport(from.backend("crm"))
	if err != nil {
		return nil, err
	}
	r.progress("preparing_state")
	if err := from.open(ctx, "crm", "calls"); err != nil {
		return nil, err
	}
	r.progress("resolving_account")
	account, _ := r.args.text("--account")
	if made.Association, err = from.resolveAccount(ctx, account); err != nil {
		return nil, err
	}
	r.progress("saving_import")
	made.ImportedAt = receipts.StartedAtText(time.Now())
	// The import is saved only while the CRM and the calls are still connected as the command read them:
	// a source detached or connected again since then refuses it as changed.
	generations := from.generations()
	result, err := postgres.CallImports{Store: r.store, WorkspaceID: r.workspace.ID,
		Generations: map[string]int64{"crm": generations["crm"], "calls": generations["calls"]}}.Import(ctx, *made)
	if err != nil {
		return nil, err
	}
	settleMatches(ctx, r.store, r.workspace.ID, account)
	r.announce(from.sourcesLine())
	return result, nil
}

// preparedImport checks import's options against the workspace's CRM, as the Python CLI checks them, and
// reads, parses and selects the file. Nothing is opened before this but the workspace's rows.
func (r *run) preparedImport(backend string) (*calls.Import, error) {
	account, byID := r.args.text("--account")
	if byID && !accountID.MatchString(account) {
		return nil, callsUsage("The account must be a canonical acc-NNN or acc-sf- Salesforce identifier.")
	}
	if _, byNumber := r.args.text("--account-number"); byNumber {
		if backend != "salesforce" {
			return nil, callsUsage("--account-number needs Salesforce as the connected CRM.")
		}
		return nil, &failure{code: 3, next: "gtm-connector debug calls import --help",
			message: "--account-number is not available in this build. Select the account with --account and its " +
				"acc-sf- ID. Nothing was imported."}
	}
	if strings.HasPrefix(account, "acc-sf-") != (backend == "salesforce") {
		return nil, callsUsage("The canonical account ID must match the connected CRM.")
	}
	made := &calls.Import{}
	if text, given := r.args.text("--called-at"); given {
		stamp, err := calls.CalledAt(text)
		if err != nil {
			return nil, callsUsage(err.Error())
		}
		made.CalledAt = &stamp
	}
	path := r.args.positionals[0]
	format, err := calls.FormatOf(path)
	if err != nil {
		return nil, callsUsage(err.Error())
	}
	var selection *calls.Range
	for _, unit := range []string{"--cues", "--lines"} {
		if text, given := r.args.text(unit); given {
			selected, err := calls.UnitRange(text, format, unit == "--lines")
			if err != nil {
				return nil, callsUsage(err.Error())
			}
			selection = &selected
		}
	}
	r.progress("reading_export")
	raw, err := calls.ReadExport(path)
	if err != nil {
		return nil, err
	}
	_, speakerNames := r.args.text("--speaker-names")
	if made.Parsed, err = calls.Parse(raw, format, speakerNames); err != nil {
		return nil, err
	}
	if made.Excerpt, err = calls.SelectExcerpt(made.Parsed, selection); err != nil {
		return nil, err
	}
	_, made.Reassign = r.args.text("--reassign")
	return made, nil
}

// resolveAccount is the CRM read an import is associated under: get_account for the account, as its tool,
// in a run of its own named import-, under a bearer made for the run that grants crm:read and is never
// stored or shown. Its receipt is committed before the read, also for the demo CRM, and the store keeps an
// import only under a finished read that returned its account in this workspace. An account the CRM holds
// is listed in the workspace first, as brief lists it, so that the receipt names it.
func (r *reads) resolveAccount(ctx context.Context, accountID string) (calls.Association, error) {
	crm, err := r.crm(ctx)
	if err != nil {
		return calls.Association{}, err
	}
	binding, _ := r.bound("crm").(*strictjson.Object)
	if err := r.list(ctx, accountID); err != nil {
		return calls.Association{}, resolutionFailed(err)
	}
	receipt, _, err := r.call(ctx, toolCall{token: receipts.Fingerprint(randomToken()), scopes: []string{"crm:read"},
		run: "import-" + strings.ReplaceAll(receipts.RandomID(), "-", ""), request: 1, tool: "get_account",
		arguments: strictjson.NewObject("account_id", accountID), account: accountID},
		func(ctx context.Context) ([]*strictjson.Object, error) {
			records, err := crm.Read(ctx, "get_account", accountID)
			if err == nil {
				_, err = accountOf(records, accountID, r.backend("crm") != "sqlite")
			}
			return records, err
		})
	if err != nil {
		return calls.Association{}, resolutionFailed(err)
	}
	return calls.Association{CRMBinding: binding, AccountID: accountID, Method: "account_id",
		ResolutionReceiptID: receipt.AuditID}, nil
}

// resolutionFailed is a resolution that did not give the account, as the import reports it. A refusal a
// person can act on, and a source that changed while it was read, stay as they are.
func resolutionFailed(err error) error {
	var known *failure
	switch {
	case errors.Is(err, errAccountNotFound):
		return calls.ErrAccountNotFound
	case errors.Is(err, errNoAccountData):
		return calls.ErrAssociationInvalid
	case errors.Is(err, receipts.ErrSourceChanged), errors.As(err, &known):
		return err
	}
	return calls.ErrAssociationFailed
}

// savedImports is debug calls list and debug calls inspect: the workspace's saved imports, read under the
// CRM binding they are bound to. No source is opened and no file is read.
func (r *run) savedImports(ctx context.Context) (*strictjson.Object, error) {
	if r.action == "inspect" && !calls.ValidImportID(r.args.positionals[0]) {
		return nil, callsUsage("The import ID must be the note- identifier returned by debug calls import or debug " +
			"calls list.")
	}
	after, paged := r.args.text("--after")
	if paged && !calls.ValidImportID(after) {
		return nil, callsUsage("--after requires the import ID returned as next_after by debug calls list.")
	}
	account, filtered := r.args.text("--account")
	if filtered && !accountID.MatchString(account) {
		return nil, callsUsage("The account must be a canonical acc-NNN or acc-sf- Salesforce identifier.")
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	noImports := &failure{code: 3, next: "gtm-connector debug calls import --help",
		message: "source_identity_mismatch: This workspace holds no imported transcripts. Select the workspace " +
			"that holds them with --state-dir. To start importing, run debug calls import --help."}
	if !r.exists {
		return nil, noImports
	}
	imports := postgres.CallImports{Store: r.store, WorkspaceID: r.workspace.ID}
	binding, err := imports.Binding(ctx)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, noImports
	}
	var result *strictjson.Object
	if r.action == "inspect" {
		_, full := r.args.text("--full")
		result, err = imports.Inspect(ctx, binding, r.args.positionals[0], full)
	} else {
		var only, start *string
		if filtered {
			only = &account
		}
		if paged {
			start = &after
		}
		result, err = imports.List(ctx, binding, only, 100, start)
	}
	if err != nil {
		return nil, err
	}
	r.announce("Source: user-supplied transcript export. Provider identity and source statements are unverified.")
	return result, nil
}

// callRange is a range of an import as the commands print it: its unit kind and first:last, and with context,
// that it includes the adjacent units.
func callRange(metadata *strictjson.Object, field string, context bool) string {
	selected := get(metadata, field).(*strictjson.Object)
	text := cellText(get(metadata, "unit_kind")) + " " + cellText(get(selected, "first")) + ":" +
		cellText(get(selected, "last"))
	if context {
		text += " (includes adjacent context)"
	}
	return text
}

// adjacent is what every excerpt adds around the range it cites.
const adjacent = "One adjacent unit on each available side is included in the cited range."

// callsText is a result of debug calls as text.
func (r *run) callsText(result *strictjson.Object) string {
	if imports, listed := get(result, "imports").([]any); listed {
		return r.importsText(imports, get(result, "next_after"))
	}
	metadata, inspected := get(result, "import").(*strictjson.Object)
	if !inspected {
		metadata = result
	}
	speakers := "not declared"
	if get(metadata, "speaker_names") == true {
		speakers = "the name that opens each line"
	}
	text := r.screen.table([]string{"FIELD", "VALUE"}, [][]any{
		{"Import", get(metadata, "import_id")}, {"Account", get(metadata, "account_id")},
		{"Call time", get(metadata, "called_at")}, {"Import time", get(metadata, "imported_at")},
		{"Requested excerpt", callRange(metadata, "requested_range", false)},
		{"Cited excerpt", callRange(metadata, "cited_range", false)}, {"Context", adjacent},
		{"Total units", get(metadata, "total_units")}, {"Speaker names", speakers}})
	if !inspected {
		return "Transcript " + cellText(get(result, "status")) + ". The excerpt is selected for this account.\n" + text
	}
	original, full := get(result, "original_text").(string)
	if full {
		text += "\n\nAll imported units"
	} else {
		text += "\n\nExact excerpt units"
	}
	for _, item := range get(result, "units").([]any) {
		unit := item.(*strictjson.Object)
		timing := ""
		if start := get(unit, "start_ms"); start != nil {
			timing = " (" + cellText(start) + " to " + cellText(get(unit, "end_ms")) + " ms)"
		}
		text += "\n" + cellText(get(unit, "index")) + timing + ": " + asciiQuoted(get(unit, "text"))
	}
	if full {
		text += "\n\nOriginal export text (quoted)\n" + asciiQuoted(original)
	}
	return text
}

// asciiQuoted is json.dumps(value, ensure_ascii=True): a text as one line of ASCII.
func asciiQuoted(value any) string {
	text, _ := strictjson.Dumps(value, strictjson.DumpOptions{EnsureASCII: true})
	return text
}

// importsText is a page of debug calls list: a block for each import on a terminal, a row for each in a
// pipe, and how to read the next page.
func (r *run) importsText(imports []any, next any) string {
	var text string
	if r.screen.terminal {
		blocks := make([]string, len(imports))
		for i, item := range imports {
			row := item.(*strictjson.Object)
			active, called := "no", get(row, "called_at")
			if get(row, "active") == true {
				active = "yes"
			}
			if called == nil {
				called = "unknown"
			}
			lines := []string{cellText(get(row, "import_id")),
				"  Account: " + cellText(get(row, "account_id")) + ". Active: " + active + ".",
				"  Called: " + cellText(called) + ". Imported: " + cellText(get(row, "imported_at")) + ".",
				"  Requested: " + callRange(row, "requested_range", false) + ". Cited: " +
					callRange(row, "cited_range", false) + "."}
			if get(row, "speaker_names") == true {
				lines = append(lines, "  Speaker names: the name that opens each line.")
			}
			for j, line := range lines {
				lines[j] = oneLine(line)
			}
			blocks[i] = strings.Join(lines, "\n")
		}
		if text = strings.Join(blocks, "\n\n"); text != "" {
			text += "\n" + adjacent
		}
	} else {
		rows := make([][]any, len(imports))
		for i, item := range imports {
			row := item.(*strictjson.Object)
			rows[i] = []any{get(row, "import_id"), get(row, "account_id"), get(row, "active"), get(row, "called_at"),
				get(row, "imported_at"), callRange(row, "cited_range", true), get(row, "speaker_names")}
		}
		text = r.screen.table([]string{"IMPORT", "ACCOUNT", "ACTIVE", "CALLED AT", "IMPORTED AT", "RANGE",
			"SPEAKER NAMES"}, rows)
	}
	if after, more := next.(string); more {
		if r.screen.terminal {
			resume := "gtm-connector debug calls list --after " + after
			if r.name != defaultWorkspace {
				resume += " \\\n  " + strings.TrimPrefix(r.folder(), " ")
			}
			text += "\nMore imports are available. Continue with:\n" + resume
		} else {
			text += "\nMore imports are available. Repeat debug calls list with --after " + after + "."
		}
	}
	if text == "" {
		return "No transcript imports are stored."
	}
	return text
}

const callsHelp = `usage: gtm-connector debug calls [-h] [--json] [--state-dir STATE_DIR]
                                 ACTION ...

Import and inspect private transcript exports without contacting a call provider.

positional arguments:
  ACTION
    import              Import an existing transcript and select an account
                        excerpt.
    list                List private import metadata without reading
                        transcripts or credentials.
    inspect             Inspect a selected transcript excerpt or explicitly
                        show the complete export.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).

Examples:
  gtm-connector debug calls import call.vtt --account acc-003
  gtm-connector debug calls list
  gtm-connector debug calls inspect IMPORT_ID --full
`

const callsImportHelp = `usage: gtm-connector debug calls import [-h] [--json] [--state-dir STATE_DIR]
                                        (--account ACCOUNT |
                                        --account-number ACCOUNT_NUMBER)
                                        [--called-at CALLED_AT]
                                        [--speaker-names] [--cues CUES |
                                        --lines LINES] [--reassign]
                                        FILE

Import an existing transcript and select an account excerpt.

positional arguments:
  FILE                  Read a UTF-8 TXT, VTT, or SRT export.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --account ACCOUNT     Associate this canonical CRM account ID.
  --account-number ACCOUNT_NUMBER
                        Resolve this exact Salesforce AccountNumber. Not
                        available in this build.
  --called-at CALLED_AT
                        Record the supplied meeting timestamp with its UTC
                        offset.
  --speaker-names       State that every line or cue opens with its speaker's
                        name and a colon ("Dana Crest: We will sign."). Each
                        name is then read as the speaker.
  --cues CUES           Select an inclusive one-based VTT or SRT cue range,
                        such as 3:8.
  --lines LINES         Select an inclusive one-based TXT line range, such as
                        3:8.
  --reassign            Explicitly reassign imported content locally.

Example: gtm-connector debug calls import call.vtt --account acc-003
`

const callsListHelp = `usage: gtm-connector debug calls list [-h] [--json] [--state-dir STATE_DIR]
                                      [--account ACCOUNT] [--after AFTER]

List private import metadata without reading transcripts or credentials.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --account ACCOUNT     Filter by this canonical CRM account ID.
  --after AFTER         Continue after the import ID returned as next_after.

Example: gtm-connector debug calls list --account acc-003
`

const callsInspectHelp = `usage: gtm-connector debug calls inspect [-h] [--json] [--state-dir STATE_DIR]
                                         [--full]
                                         IMPORT_ID

Inspect a selected transcript excerpt or explicitly show the complete export.

positional arguments:
  IMPORT_ID             Select the import identifier returned by debug calls
                        list.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --full                Show all units and the original export text.

Example: gtm-connector debug calls inspect IMPORT_ID --full
`
