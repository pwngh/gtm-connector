package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"

	gtmfile "github.com/pwngh/gtm-connector/internal/config"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The config command keeps gtm.yaml, the one file a workspace is set by. preview reads a file, stores it as
// a draft under its revision, which is the SHA-256 of its bytes, and says what it would set. activate makes
// a stored draft the workspace's active file, by its exact revision, under an activate_config receipt. A
// build never accepts a setting it does not apply: a file that holds one is previewed, and cannot be
// activated. show prints a file, and history what was active and when.

var configActions = map[string]arguments{
	"preview":  {required: "file", help: configPreviewHelp},
	"activate": {required: "revision", help: configActivateHelp},
	"show":     {optional: "revision", help: configShowHelp},
	"history":  {help: configHistoryHelp},
}

var configRevision = regexp.MustCompile(`^[0-9a-f]{64}$`)

// runConfig runs the config command and gives its exit code.
func runConfig(ctx context.Context, tokens []string, s screen) int {
	if len(tokens) == 0 {
		return s.fail("config", refused("the following arguments are required: action"))
	}
	if tokens[0] == "-h" || tokens[0] == "--help" {
		fmt.Fprint(s.out, configHelp)
		return 0
	}
	action := tokens[0]
	spec, known := configActions[action]
	if !known {
		return s.fail("config", refused("argument action: invalid choice (choose from 'preview', 'activate', 'show', 'history')"))
	}
	command := "config " + action
	args, err := spec.parse(tokens[1:])
	if err != nil {
		return s.fail(command, refused("%s", err))
	}
	if args.help {
		fmt.Fprint(s.out, spec.help)
		return 0
	}
	r := &run{command: command, args: args, screen: s, name: defaultWorkspace}
	if name, given := args.text("--state-dir"); given {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	var result any
	switch action {
	case "preview":
		result, err = r.previewConfig(ctx)
	case "activate":
		result, err = r.activateConfig(ctx)
	case "show":
		result, err = r.showConfig(ctx)
	default:
		result, err = r.configHistory(ctx)
	}
	if err != nil {
		return s.fail(command, r.failed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
		return 0
	}
	fmt.Fprintln(s.out, r.configText(action, result))
	return 0
}

// fileBytes reads a settings file from disk: a regular file of at most gtmfile.MaxBytes, or a link to one.
// The file is opened without waiting, and what was opened is what is checked, so a pipe or a device put at
// the path is refused and never read. A refusal does not repeat the path.
func fileBytes(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, refused("No file at that path.")
	case err != nil:
		return nil, refused("The file at that path cannot be read.")
	}
	defer file.Close()
	info, err := file.Stat()
	switch {
	case err != nil:
		return nil, refused("The file at that path cannot be read.")
	case !info.Mode().IsRegular():
		return nil, refused("That path is not a file.")
	case info.Size() > gtmfile.MaxBytes:
		return nil, refused("The file is larger than %d bytes.", gtmfile.MaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, gtmfile.MaxBytes+1))
	if err != nil || len(data) > gtmfile.MaxBytes {
		return nil, refused("The file cannot be read whole within %d bytes.", gtmfile.MaxBytes)
	}
	return data, nil
}

// connectedKinds are the kinds of source the workspace is connected to, as a file names them.
func (r *run) connectedKinds(ctx context.Context) (map[string]bool, error) {
	kinds := map[string]bool{}
	from, err := r.reads(ctx)
	if errors.Is(err, errNotConnected) {
		return kinds, nil
	}
	if err != nil {
		return nil, err
	}
	switch from.backend("crm") {
	case "salesforce":
		kinds["salesforce"] = true
	case "sqlite":
		kinds["demo"] = true
	}
	if from.backend("calls") == "imported" {
		kinds["transcripts"] = true
	}
	if from.backend("slack") == "slack" {
		kinds["slack"] = true
	}
	return kinds, nil
}

// described is a file as preview and activate describe it: its revision, its sources with whether each is
// connected, what this build does not apply, and why it cannot be activated, if it cannot.
func (r *run) described(ctx context.Context, revision string, file *gtmfile.File) (*strictjson.Object, []string, error) {
	connected, err := r.connectedKinds(ctx)
	if err != nil {
		return nil, nil, err
	}
	var problems []string
	sources := []any{}
	for _, source := range file.Sources {
		sources = append(sources, strictjson.NewObject("name", source.Name, "kind", source.Kind, "connected", connected[source.Kind]))
		if !connected[source.Kind] {
			problems = append(problems, fmt.Sprintf("sources.%s: this workspace has no %s source connected", source.Name, source.Kind))
		}
	}
	notApplied := []any{}
	for _, path := range file.NotApplied {
		notApplied = append(notApplied, path)
	}
	if len(file.NotApplied) > 0 {
		problems = append(problems, "this build does not apply: "+strings.Join(file.NotApplied, ", "))
	}
	blocked := make([]any, len(problems))
	for i, problem := range problems {
		blocked[i] = problem
	}
	return strictjson.NewObject("revision", revision, "sources", sources, "not_applied", notApplied,
		"can_activate", len(problems) == 0, "problems", blocked), problems, nil
}

// activeRevision is the workspace's active revision, or nil.
func (r *run) activeRevision(ctx context.Context) (any, error) {
	_, active, found, err := r.store.ActiveConfig(ctx, r.workspace.ID)
	if err != nil || !found {
		return nil, err
	}
	return active.Revision, nil
}

func (r *run) workspaceForConfig(ctx context.Context) error {
	if err := r.open(ctx); err != nil {
		return err
	}
	if !r.exists {
		return &failure{code: 2, message: "No workspace has this name. Connect a source first: gtm-connector connect --help"}
	}
	return nil
}

// previewConfig reads a file, stores it as a draft when it reads, and describes it. A file that is refused
// is not stored.
func (r *run) previewConfig(ctx context.Context) (any, error) {
	data, err := fileBytes(r.args.positionals[0])
	if err != nil {
		return nil, err
	}
	file, err := gtmfile.Read(data)
	var refusedFile *gtmfile.Error
	if errors.As(err, &refusedFile) {
		return nil, refused("gtm.yaml %s.", refusedFile.Error())
	}
	if err != nil {
		return nil, err
	}
	if err := r.workspaceForConfig(ctx); err != nil {
		return nil, err
	}
	revision := gtmfile.Revision(data)
	out, _, err := r.described(ctx, revision, file)
	if err != nil {
		return nil, err
	}
	stored, err := r.store.SaveConfigRevision(ctx, r.workspace.ID, revision, data, file.Settings())
	if err != nil {
		return nil, err
	}
	active, err := r.activeRevision(ctx)
	if err != nil {
		return nil, err
	}
	out.Set("stored", stored)
	out.Set("active", active)
	return out, nil
}

// activateConfig makes a stored revision the workspace's active file. The file is read again as this build
// reads it: one that holds a setting this build does not apply, or names a source the workspace has not
// connected, is refused, and nothing changes.
func (r *run) activateConfig(ctx context.Context) (any, error) {
	revision := r.args.positionals[0]
	if !configRevision.MatchString(revision) {
		return nil, refused("A revision is the 64 characters config preview prints.")
	}
	if err := r.workspaceForConfig(ctx); err != nil {
		return nil, err
	}
	// A refused activation leaves its receipt, as an activation does, and changes nothing.
	refuse := func(status int, code string, told *failure) (any, error) {
		if err := r.store.RefuseActivation(ctx, r.workspace.ID, revision, status, code); err != nil {
			return nil, err
		}
		return nil, told
	}
	stored, err := r.store.ConfigRevision(ctx, r.workspace.ID, revision)
	if errors.Is(err, postgres.ErrNoRevision) {
		return refuse(404, "revision_not_found", &failure{code: 2, next: "gtm-connector config preview FILE" + r.here(),
			message: "This workspace holds no file of that revision. Preview the file first."})
	}
	if err != nil {
		return nil, err
	}
	file, err := gtmfile.Read(stored.Body)
	if err != nil {
		return refuse(409, "file_refused", &failure{code: 3, message: "The stored file no longer reads under this build: " + err.Error() + "."})
	}
	out, problems, err := r.described(ctx, revision, file)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		code := "source_not_connected"
		if len(file.NotApplied) > 0 {
			code = "setting_not_applied"
		}
		return refuse(409, code, &failure{code: 3, next: "gtm-connector config preview FILE" + r.here(),
			message: "This file cannot be activated: " + strings.Join(problems, "; ") + "."})
	}
	activation, changed, err := r.store.ActivateConfig(ctx, r.workspace.ID, revision)
	if err != nil {
		return nil, err
	}
	out.Set("active", activation.Revision)
	out.Set("activated_at", utcSeconds(activation.ActivatedAt))
	out.Set("receipt", activation.ReceiptID)
	out.Set("changed", changed)
	return out, nil
}

// showConfig is the active file, or a stored revision, with its bytes as text.
func (r *run) showConfig(ctx context.Context) (any, error) {
	if err := r.workspaceForConfig(ctx); err != nil {
		return nil, err
	}
	active, err := r.activeRevision(ctx)
	if err != nil {
		return nil, err
	}
	revision, _ := active.(string)
	if len(r.args.positionals) > 0 {
		if revision = r.args.positionals[0]; !configRevision.MatchString(revision) {
			return nil, refused("A revision is the 64 characters config preview prints.")
		}
	}
	if revision == "" {
		return strictjson.NewObject("revision", nil, "active", nil, "file", nil), nil
	}
	stored, err := r.store.ConfigRevision(ctx, r.workspace.ID, revision)
	if errors.Is(err, postgres.ErrNoRevision) {
		return nil, &failure{code: 2, message: "This workspace holds no file of that revision."}
	}
	if err != nil {
		return nil, err
	}
	return strictjson.NewObject("revision", revision, "active", active, "file", string(stored.Body),
		"stored_at", utcSeconds(stored.StoredAt)), nil
}

// configHistory lists what was active and when, newest first.
func (r *run) configHistory(ctx context.Context) (any, error) {
	if err := r.workspaceForConfig(ctx); err != nil {
		return nil, err
	}
	history, err := r.store.ConfigHistory(ctx, r.workspace.ID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(history))
	for i, a := range history {
		out[i] = strictjson.NewObject("revision", a.Revision, "activated_at", utcSeconds(a.ActivatedAt), "receipt", a.ReceiptID)
	}
	return out, nil
}

// configText is a config command's result as a person reads it.
func (r *run) configText(action string, result any) string {
	if action == "history" {
		listed := result.([]any)
		if len(listed) == 0 {
			return "No file has been activated. This workspace runs on the defaults."
		}
		rows := make([][]any, len(listed))
		for i, value := range listed {
			a := value.(*strictjson.Object)
			rows[i] = []any{get(a, "activated_at"), get(a, "revision"), get(a, "receipt")}
		}
		return r.screen.table([]string{"ACTIVATED", "REVISION", "RECEIPT"}, rows)
	}
	out := result.(*strictjson.Object)
	if action == "show" {
		if get(out, "revision") == nil {
			return "No file has been activated. This workspace runs on the defaults."
		}
		state := "stored, not active"
		if get(out, "revision") == get(out, "active") {
			state = "active"
		}
		// The file is shown line by line with anything that does not print written out, whatever stored it.
		lines := strings.Split(strings.TrimRight(get(out, "file").(string), "\n"), "\n")
		for i, line := range lines {
			lines[i] = oneLine(line)
		}
		return fmt.Sprintf("Revision %v (%s)\n\n%s", get(out, "revision"), state, strings.Join(lines, "\n"))
	}
	fields := [][]any{{"Revision", get(out, "revision")}}
	for _, value := range get(out, "sources").([]any) {
		source := value.(*strictjson.Object)
		state := "connected"
		if get(source, "connected") != true {
			state = "not connected in this workspace"
		}
		fields = append(fields, []any{"Source " + get(source, "name").(string), fmt.Sprintf("%v, %s", get(source, "kind"), state)})
	}
	for _, path := range get(out, "not_applied").([]any) {
		fields = append(fields, []any{"Not applied", path})
	}
	text := r.screen.table([]string{"FIELD", "VALUE"}, fields)
	switch {
	case action == "activate" && get(out, "changed") == true:
		return text + fmt.Sprintf("\n\nActive since %v. Receipt %v.", get(out, "activated_at"), get(out, "receipt"))
	case action == "activate":
		return text + fmt.Sprintf("\n\nThis file is active already, since %v.", get(out, "activated_at"))
	case get(out, "revision") == get(out, "active"):
		return text + "\n\nThis file is the active one."
	case get(out, "can_activate") == true:
		return text + "\n\n" + oneLine(fmt.Sprintf("Stored as a draft. Activate it: gtm-connector config activate %v%s", get(out, "revision"), r.here()))
	}
	problems := []string{}
	for _, problem := range get(out, "problems").([]any) {
		problems = append(problems, problem.(string))
	}
	return text + "\n\n" + oneLine("Stored as a draft. It cannot be activated: "+strings.Join(problems, "; ")+".")
}

const configHelp = `usage: gtm-connector config [-h] {preview,activate,show,history} ...

Keep gtm.yaml, the one file a workspace is set by: preview a file, activate a previewed one by its exact
revision, and see what is active.

positional arguments:
  {preview,activate,show,history}
    preview             Read a file, store it as a draft and say what it would set.
    activate            Make a previewed file the active one, by its revision.
    show                Print the active file, or a stored revision.
    history             List what was active and when.

options:
  -h, --help            show this help message and exit

Each action takes --json, --state-dir WORKSPACE and --help.
Example: gtm-connector config preview gtm.yaml --state-dir acme
`

const configPreviewHelp = `usage: gtm-connector config preview [-h] [--json] [--state-dir STATE_DIR] file

Read gtm.yaml, store it as a draft under its revision, and say what it would set: its sources and whether the
workspace has each connected, and the settings this build does not apply. A file that is refused is not
stored. A revision is the SHA-256 of the file's bytes.

positional arguments:
  file                  The file, at most 65536 bytes.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
`

const configActivateHelp = `usage: gtm-connector config activate [-h] [--json] [--state-dir STATE_DIR]
                                    revision

Make a previewed file the workspace's active file. The activation gets an activate_config receipt. A file
that holds a setting this build does not apply, or names a source the workspace has not connected, is
refused, and the active file stays.

positional arguments:
  revision              The 64 characters config preview printed.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
`

const configShowHelp = `usage: gtm-connector config show [-h] [--json] [--state-dir STATE_DIR] [revision]

Print the workspace's active file and its revision, or a stored revision.

positional arguments:
  revision              A stored revision (default: the active one).

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
`

const configHistoryHelp = `usage: gtm-connector config history [-h] [--json] [--state-dir STATE_DIR]

List the files that were active and when, newest first, each with its activate_config receipt.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
`
