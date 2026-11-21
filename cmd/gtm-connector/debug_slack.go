package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/sources/childenv"
	"github.com/pwngh/gtm-connector/internal/sources/slack"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// debug slack: connect, status and disconnect work on the workspace's saved
// sign-in, and each request they make to Slack runs under a receipt of its step in the workspace; status
// without --check and disconnect without --revoke make none and need no database. map saves the thread of one
// account after an audited CRM read; check reads that thread through read_slack_messages under a receipt
// and reports its steps in counts, never in text.

// slackActions are debug slack's actions, each with its options and help.
var slackActions = map[string]arguments{
	"connect": {options: []option{{name: "--client-id", value: true}, {name: "--redirect-uri", value: true},
		{name: "--user-scope", value: true, repeat: true,
			choices: []string{"channels:history", "groups:history", "users:read", "users:read.email"}},
		{name: "--team-id", value: true}}, help: slackConnectHelp},
	"status":     {options: []option{{name: "--check"}}, help: slackStatusHelp},
	"disconnect": {options: []option{{name: "--revoke"}}, help: slackDisconnectHelp},
	"map": {options: []option{{name: "--account", value: true}, {name: "--account-number", value: true},
		{name: "--thread-url", value: true}, {name: "--channel", value: true}, {name: "--thread-ts", value: true},
		{name: "--replace"}, {name: "--reassign"}}, help: slackMapHelp},
	"check": {options: []option{{name: "--account", value: true}}, help: slackCheckHelp},
}

var slackActionNames = []string{"connect", "status", "disconnect", "map", "check"}

// openBrowser opens the sign-in's start page in the system browser. Tests set their own.
var openBrowser slack.Browser = systemBrowser

// systemBrowser starts the desktop's opener, open on macOS and xdg-open elsewhere, and does not wait for it:
// an opener that stays running, as xdg-open can, never holds up the sign-in, and its exit is reaped apart.
// The page it is given is the sign-in's start page on the loopback listener, which holds no state. It runs
// with the environment browserEnvironment keeps, so no setting of the connector reaches the browser.
func systemBrowser(_ context.Context, page string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	opener := exec.Command(name, page)
	opener.Env = browserEnvironment(os.Environ())
	if err := opener.Start(); err != nil {
		return err
	}
	go func() { _ = opener.Wait() }()
	return nil
}

// askTerminal asks a question at the terminal that runs the command and gives the line typed in answer.
// It reads /dev/tty, not standard input, so the answer comes from the person at that terminal and never from
// a pipe or a file. Tests set their own.
var askTerminal = func(ctx context.Context, question string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	var closing sync.Once
	stop := func() { closing.Do(func() { _ = tty.Close() }) }
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	if _, err := io.WriteString(tty, question); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(tty, 256)).ReadString('\n')
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(line), nil
}

// confirmSignIn asks the operator, at the command's terminal, whether the Slack user and workspace a new
// approval came from are their own. Only "yes" saves the sign-in. The question names Slack's own IDs and the
// workspace origin Slack stated, which the operator compares with the account they approved with.
func (r *run) confirmSignIn(ctx context.Context, identity slack.Identity, workspace string) (bool, error) {
	where := workspace
	if where == "" {
		where = "a workspace whose address Slack did not state"
	}
	enterprise := ""
	if identity.EnterpriseID != "" {
		enterprise = ", organization " + identity.EnterpriseID
	}
	answer, err := askTerminal(ctx, fmt.Sprintf("Slack approved user %s in team %s%s (%s) for app %s.\n"+
		"Save this sign-in for workspace %s? Type yes to save it: ", identity.UserID, identity.TeamID, enterprise,
		where, identity.AppID, workspaceSlug(r.name)))
	return err == nil && answer == "yes", err
}

// browserEnvironment is what the opener is given of the connector's environment: only what a desktop needs
// to find and show a browser, of what childenv lets any child have. Nothing else passes, so no database URL,
// password or token can.
func browserEnvironment(environ []string) []string {
	var kept []string
	for _, entry := range childenv.WithoutSecrets(environ) {
		name, _, _ := strings.Cut(entry, "=")
		switch {
		case name == "PATH", name == "HOME", name == "LANG", name == "DISPLAY", name == "WAYLAND_DISPLAY",
			name == "DBUS_SESSION_BUS_ADDRESS", strings.HasPrefix(name, "XDG_"):
			kept = append(kept, entry)
		}
	}
	return kept
}

// debugSlack runs debug slack ACTION. Before the action it takes --state-dir and --help.
func debugSlack(ctx context.Context, tokens []string, s screen) int {
	group := arguments{help: slackHelp}
	var before []string
	for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
		before, tokens = append(before, tokens[0]), tokens[1:]
		if before[len(before)-1] == "--state-dir" && len(tokens) > 0 {
			before, tokens = append(before, tokens[0]), tokens[1:]
		}
	}
	parsedBefore, err := group.parse(before)
	switch {
	case err != nil:
		return s.fail("debug slack", refused("%s", err))
	case parsedBefore.help:
		fmt.Fprint(s.out, slackHelp)
		return 0
	case len(tokens) == 0:
		return s.fail("debug slack", refused("the following arguments are required: ACTION"))
	}
	action := tokens[0]
	spec, known := slackActions[action]
	if !known {
		return s.fail("debug slack", refused("argument ACTION: invalid choice (choose from %s)",
			quotedChoices(slackActionNames)))
	}
	command := "debug slack " + action
	args, err := spec.parse(tokens[1:])
	if err == nil && !args.help {
		err = slackArgumentsGiven(action, args)
	}
	if err != nil {
		return s.fail(command, refused("%s", err))
	}
	if args.help {
		fmt.Fprint(s.out, spec.help)
		return 0
	}
	if name, given := parsedBefore.text("--state-dir"); given {
		if _, again := args.text("--state-dir"); !again {
			args.values["--state-dir"] = name
		}
	}
	r := &run{command: command, args: args, screen: s, name: defaultWorkspace, action: action}
	if name, given := args.text("--state-dir"); given {
		r.name = name
	}
	defer func() {
		if r.store != nil {
			r.store.Close()
		}
	}()
	var result *strictjson.Object
	switch action {
	case "connect", "status", "disconnect":
		result, err = r.slackSignIn(ctx)
	case "map":
		result, err = r.slackMap(ctx)
	default:
		result, err = r.slackCheck(ctx)
	}
	if err != nil {
		return s.fail(command, r.slackFailed(ctx, err))
	}
	if s.json {
		text, err := strictjson.Dumps(result, strictjson.Artifact)
		if err != nil {
			return s.fail(command, r.failed(ctx, err))
		}
		fmt.Fprintln(s.out, text)
	} else {
		fmt.Fprintln(s.out, r.slackText(result))
	}
	// A disconnect whose revocation Slack did not confirm, and a check that failed, print their result and
	// then fail.
	if revoked, _ := get(result, "remote_access_token_revoked").(bool); action == "disconnect" &&
		get(result, "remote_revoke_requested") == true && !revoked {
		reason, _ := get(result, "revocation_error").(string)
		detail, known := slackAuthFailures[reason]
		if !known {
			detail = slackAuthFailures["slack_unavailable"]
		}
		return s.fail(command, &failure{code: 5, next: "gtm-connector debug slack disconnect --help",
			message: "Remote access-token revocation is unconfirmed. " + detail + " Manage the whole grant in Slack settings."})
	}
	if action == "check" && get(result, "status") != "passed" {
		return s.fail(command, &failure{code: 4, next: "gtm-connector debug slack check --help",
			message: "Slack thread checks failed. Apply the next action beside each failed step."})
	}
	return 0
}

// slackArgumentsGiven refuses an action given without what it needs, or given both of two options that
// exclude each other, as argparse does.
func slackArgumentsGiven(action string, args parsed) error {
	has := func(name string) bool { _, given := args.text(name); return given }
	var missing []string
	switch action {
	case "connect":
		for _, name := range []string{"--client-id", "--redirect-uri"} {
			if !has(name) {
				missing = append(missing, name)
			}
		}
	case "check":
		if !has("--account") {
			missing = append(missing, "--account")
		}
	case "map":
		for _, pair := range [][2]string{{"--account", "--account-number"}, {"--thread-url", "--channel"}} {
			switch {
			case has(pair[0]) && has(pair[1]):
				return usageError("argument " + pair[1] + ": not allowed with argument " + pair[0])
			case !has(pair[0]) && !has(pair[1]):
				return usageError("one of the arguments " + pair[0] + " " + pair[1] + " is required")
			}
		}
	}
	if len(missing) > 0 {
		return usageError("the following arguments are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// slackProgress tells each step on stderr: as a JSON line with --json, and otherwise the steps that start.
func (r *run) slackProgress(step, status string) {
	messages := map[string]string{
		"browser waiting":           "Waiting for Slack browser approval. Complete it within ten minutes.",
		"exchange running":          "Exchanging the approved Slack authorization.",
		"refresh running":           "Refreshing Slack authentication once.",
		"identity running":          "Checking Slack authentication.",
		"revoke running":            "Requesting revocation of the saved Slack access token.",
		"preparing_state running":   "Preparing the selected sources and private state.",
		"resolving_account running": "Resolving the account through an audited CRM read.",
		"saving_selection running":  "Saving the private Slack thread selection.",
		"private_state running":     "Checking the existing private source state.",
		"thread_read running":       "Reading the selected Slack thread through the audited tool.",
	}
	if r.screen.json {
		line, _ := strictjson.Dumps(strictjson.NewObject("progress", strictjson.NewObject("command", r.command,
			"step", step, "status", status)), strictjson.DumpOptions{EnsureASCII: true})
		fmt.Fprintln(r.screen.err, line)
	} else if message, shown := messages[step+" "+status]; shown {
		fmt.Fprintln(r.screen.err, message)
	}
}

// slackReader is the Slack reader of the run's workspace, as the sign-in commands use it.
func (r *run) slackReader() (*slack.Reader, error) {
	signIn, err := signInOf(workspaceSlug(r.name))
	if err != nil {
		return nil, err
	}
	return &slack.Reader{Connection: signIn, Transport: slackTransport, Clock: slackClock}, nil
}

// slackSignIn is connect, status or disconnect.
func (r *run) slackSignIn(ctx context.Context) (*strictjson.Object, error) {
	reader, err := r.slackReader()
	if err != nil {
		return nil, err
	}
	switch r.action {
	case "connect":
		clientID, _ := r.args.text("--client-id")
		redirect, _ := r.args.text("--redirect-uri")
		team, _ := r.args.text("--team-id")
		cfg, err := slack.NewConfiguration(clientID, redirect, r.args.lists["--user-scope"], team)
		if err != nil {
			return nil, err
		}
		// A workspace whose Slack was connected stays pinned to that user, also once its sign-in was removed,
		// so a sign-in for another user is refused before it is saved.
		if err := r.open(ctx); err != nil {
			return nil, err
		}
		if r.exists {
			rows, err := r.store.Connections(ctx, r.workspace.ID)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				if row.Source == "slack" {
					cfg.Pinned = row.IdentitySHA256
				}
			}
		}
		reader.Audit = r.signInSteps("slack:read", "live")
		reader.Confirm = r.confirmSignIn
		return reader.Connect(ctx, cfg, openBrowser, r.slackProgress)
	case "status":
		_, check := r.args.text("--check")
		if check {
			if err := r.open(ctx); err != nil {
				return nil, err
			}
			reader.Audit = r.signInSteps("slack:read", "live")
		}
		return reader.Status(ctx, check, r.slackProgress)
	}
	_, revoke := r.args.text("--revoke")
	if revoke {
		if err := r.open(ctx); err != nil {
			return nil, err
		}
		reader.Audit = r.signInSteps("slack:read", "live")
	}
	return reader.Disconnect(ctx, revoke, r.slackProgress)
}

// slackSources opens the workspace's reads for map and check, which need its CRM and live Slack.
func (r *run) slackSources(ctx context.Context) (*reads, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	from, err := r.reads(ctx)
	if err != nil {
		return nil, err
	}
	if from.backend("slack") != "slack" {
		return nil, &failure{code: 2, next: "gtm-connector connect --help", message: "Slack is not connected. Run " +
			"gtm-connector debug slack connect" + r.here() + " to sign in, then gtm-connector connect slack" + r.here() + "."}
	}
	return from, nil
}

// slackMap is debug slack map: one audited CRM read finds the account, and its thread is saved.
func (r *run) slackMap(ctx context.Context) (*strictjson.Object, error) {
	started := time.Now()
	from, err := r.slackSources(ctx)
	if err != nil {
		return nil, err
	}
	salesforce := from.backend("crm") == "salesforce"
	account, _ := r.args.text("--account")
	var asked threadAsked
	asked.link, asked.byLink = r.args.text("--thread-url")
	asked.channel, _ = r.args.text("--channel")
	asked.stamp, asked.stamped = r.args.text("--thread-ts")
	if _, byNumber := r.args.text("--account-number"); byNumber {
		if !salesforce {
			return nil, slackRefusal("slack_selection_invalid")
		}
		return nil, &failure{code: 3, next: "gtm-connector debug slack map --help", message: "--account-number is " +
			"not available in this build. Pass the account ID with --account; accounts finds it."}
	}
	if !evidence.ValidAccountID(account, salesforce) {
		return nil, slackRefusal("slack_selection_invalid")
	}
	// The link's own origin checks its form only. The thread is taken from it under the saved workspace.
	if _, err := threadSelection(asked, "", true); err != nil {
		return nil, err
	}
	signIn, err := signInOf(r.workspace.Slug)
	if err != nil {
		return nil, err
	}
	saved, err := signIn.signedIn(ctx)
	if err != nil {
		return nil, err
	}
	selection, err := threadSelection(asked, saved.WorkspaceOrigin, false)
	if err != nil {
		return nil, err
	}
	binding, err := slack.IdentityBinding(saved.Identity)
	if err != nil || get(binding, "identity_sha256") != from.rows["slack"].IdentitySHA256 {
		return nil, slackRefusal("slack_source_mismatch")
	}
	r.slackProgress("preparing_state", "running")
	r.announce(from.sourcesLine())
	r.slackProgress("resolving_account", "running")
	// The account is found as an import finds it: one get_account read under a receipt committed first.
	association, err := from.resolveAccount(ctx, account)
	switch {
	case errors.Is(err, calls.ErrAccountNotFound):
		return nil, slackRefusal("slack_account_not_found")
	case errors.Is(err, calls.ErrAssociationInvalid):
		return nil, slackRefusal("slack_association_invalid")
	case errors.Is(err, calls.ErrAssociationFailed):
		return nil, slackRefusal("slack_association_failed")
	case err != nil:
		return nil, err
	}
	receipt := association.ResolutionReceiptID
	r.slackProgress("saving_selection", "running")
	manifest := strictjson.NewObject("schema_version", 1, "account_id", account, "crm_binding", from.bound("crm"),
		"slack_binding", from.bound("slack"), "channel_id", selection[0], "thread_ts", selection[1],
		"selected_at", receipts.StartedAtText(clockNow()), "resolution_receipt_id", receipt)
	canonical, err := strictjson.Dumps(manifest, strictjson.Prepared)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(canonical))
	manifest.Set("mapping_id", "map-"+hex.EncodeToString(sum[:]))
	mapping, err := strictjson.Dumps(manifest, strictjson.Prepared)
	if err != nil {
		return nil, err
	}
	_, isReplace := r.args.text("--replace")
	_, isReassign := r.args.text("--reassign")
	// The mapping is saved only while the CRM and Slack are still the connections it was made through.
	generations := map[string]int64{}
	for _, source := range []string{"crm", "slack"} {
		generations[from.rows[source].ID] = from.rows[source].Generation
	}
	_, changed, err := r.store.SaveSlackMapping(ctx, r.workspace.ID, postgres.SlackMapping{AccountRef: account,
		ChannelID: selection[0], ThreadTS: selection[1], Mapping: mapping, Generations: generations},
		isReplace, isReassign)
	switch {
	case errors.Is(err, postgres.ErrSourceChanged):
		return nil, receipts.ErrSourceChanged
	case errors.Is(err, postgres.ErrSlackMappingConflict):
		return nil, slackRefusal("slack_mapping_conflict")
	case errors.Is(err, postgres.ErrSlackMappingSource):
		return nil, slackRefusal("slack_source_mismatch")
	case err != nil:
		return nil, err
	}
	settleMatches(ctx, r.store, r.workspace.ID, account)
	status := "unchanged"
	if changed {
		status = "saved"
	}
	return strictjson.NewObject("schema_version", 1, "status", status, "selection", "saved",
		"thread_access", "unchecked", "counts", strictjson.NewObject("selected_threads", 1),
		"duration_ms", roundTo3(durationMS(time.Since(started))), "next", "gtm-connector debug slack check --help"), nil
}

// threadAsked is the thread a map was given: a link, or a channel with a timestamp.
type threadAsked struct {
	link, channel, stamp string
	byLink, stamped      bool
}

// threadSelection is the channel and parent timestamp a map names, as parse_thread_selection reads them:
// from a thread link of the saved workspace, or from --channel with --thread-ts. With own, a link is read
// under its own origin, which checks its form only.
func threadSelection(asked threadAsked, origin string, own bool) ([2]string, error) {
	invalid := slackRefusal("slack_selection_invalid")
	if !asked.byLink {
		if asked.channel == "" || !asked.stamped || !slackChannel(asked.channel) || !threadStamp(asked.stamp) {
			return [2]string{}, invalid
		}
		return [2]string{asked.channel, asked.stamp}, nil
	}
	link := asked.link
	if asked.stamped || len(link) > 4096 || strings.ContainsFunc(link, func(c rune) bool { return c < 33 || c > 126 }) {
		return [2]string{}, invalid
	}
	scheme, rest, found := strings.Cut(link, "://")
	if !found || strings.ToLower(scheme) != "https" {
		return [2]string{}, invalid
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	netloc, rest := rest[:end], rest[end:]
	if own {
		origin = "https://" + netloc
	}
	saved, isHTTPS := strings.CutPrefix(origin, "https://")
	host := slackHost(netloc)
	if host == "" || !isHTTPS || host != slackHost(saved) {
		return [2]string{}, invalid
	}
	rest, fragment, _ := strings.Cut(rest, "#")
	path, query, _ := strings.Cut(rest, "?")
	parts := strings.Split(path, "/")
	if fragment != "" || len(parts) != 4 || parts[0] != "" || parts[1] != "archives" || !slackChannel(parts[2]) ||
		!strings.HasPrefix(parts[3], "p") || len(parts[3]) < 8 || len(parts[3]) > 19 || !allDigits(parts[3][1:]) {
		return [2]string{}, invalid
	}
	channel, digits := parts[2], parts[3][1:]
	pathStamp := digits[:len(digits)-6] + "." + digits[len(digits)-6:]
	if !threadStamp(pathStamp) || len(evidence.Percents([]rune(query))) > 0 {
		return [2]string{}, invalid
	}
	stamp := pathStamp
	if query != "" {
		pairs := strings.Split(query, "&")
		seen := map[string]bool{}
		if len(pairs) > 2 {
			return [2]string{}, invalid
		}
		for _, pair := range pairs {
			name, value, found := strings.Cut(pair, "=")
			name, errName := url.QueryUnescape(name)
			value, errValue := url.QueryUnescape(value)
			if !found || errName != nil || errValue != nil || seen[name] || (name != "thread_ts" && name != "cid") {
				return [2]string{}, invalid
			}
			seen[name] = true
			switch {
			case name == "cid" && value != channel:
				return [2]string{}, invalid
			case name == "thread_ts":
				stamp = value
			}
		}
	}
	if !threadStamp(stamp) || stampMicros(stamp) > stampMicros(pathStamp) {
		return [2]string{}, invalid
	}
	return [2]string{channel, stamp}, nil
}

// slackHost is the lowered host of a netloc that is a workspace's under slack.com, with no user and no port
// but 443, or "".
func slackHost(netloc string) string {
	if strings.Contains(netloc, "@") {
		return ""
	}
	host, port, hasPort := strings.Cut(netloc, ":")
	if hasPort && port != "" && (!allDigits(port) || strings.TrimLeft(port, "0") != "443") {
		return ""
	}
	host = strings.ToLower(host)
	name, isSlack := strings.CutSuffix(host, ".slack.com")
	if !isSlack || name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' ||
		strings.ContainsFunc(name, func(c rune) bool { return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') }) {
		return ""
	}
	return host
}

func allDigits(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return c < '0' || c > '9' })
}

// slackChannel is a channel ID: C or G, then 1 to 31 capitals or digits.
func slackChannel(s string) bool {
	return len(s) >= 2 && len(s) <= 32 && (s[0] == 'C' || s[0] == 'G') &&
		!strings.ContainsFunc(s[1:], func(c rune) bool { return !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') })
}

// threadStamp is a Slack timestamp: whole seconds with no leading zero up to the year 9999, a dot, and
// exactly six digits.
func threadStamp(s string) bool {
	seconds, fraction, dotted := strings.Cut(s, ".")
	if !dotted || !allDigits(seconds) || len(seconds) > 12 || (len(seconds) > 1 && seconds[0] == '0') ||
		len(fraction) != 6 || !allDigits(fraction) {
		return false
	}
	whole, _ := strconv.ParseInt(seconds, 10, 64)
	return whole <= 253402300799
}

// stampMicros is a valid Slack timestamp in microseconds.
func stampMicros(s string) int64 {
	seconds, fraction, _ := strings.Cut(s, ".")
	whole, _ := strconv.ParseInt(seconds, 10, 64)
	part, _ := strconv.ParseInt(fraction, 10, 64)
	return whole*1_000_000 + part
}

// The phases of a read, in the order a check reports them, and what a passed step says.
var (
	slackPhases       = []string{"mapping", "credential_binding", "thread_read"}
	slackStepMessages = map[string]string{
		"private_state":      "Existing private source state and the local check gateway are ready.",
		"mapping":            "The saved account association and thread selection are valid.",
		"credential_binding": "The selected Slack credential matches the saved connection identity.",
		"thread_read":        "The selected thread text passed the bounded paging and source checks.",
		"receipt":            "The tool attempt has a matching committed audit receipt.",
	}
)

// slackStep is one step of a check's report.
func slackStep(name, status string, count int, duration float64, apiCalls, responseBytes any, code string) *strictjson.Object {
	message, next := slackStepMessages[name], ""
	if status != "pass" {
		message, next = slackDetails(code)
	} else {
		code = "ok"
	}
	return strictjson.NewObject("name", name, "status", status, "count", count, "duration_ms", roundTo3(duration),
		"api_calls", apiCalls, "response_bytes", responseBytes, "code", code, "message", message, "next", next)
}

// roundTo3 is round(ms, 3), and durationMS a duration in milliseconds.
func roundTo3(ms float64) float64 { return math.Round(ms*1000) / 1000 }

func durationMS(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

// slackCheck is debug slack check: the mapped thread of one account read as read_slack_messages, under a
// receipt committed first, and checked against its snapshot and its receipt. It reads no CRM and no calls.
func (r *run) slackCheck(ctx context.Context) (*strictjson.Object, error) {
	account, _ := r.args.text("--account")
	if !evidence.ValidAccountID(account, false) && !evidence.ValidAccountID(account, true) {
		return nil, slackRefusal("slack_selection_invalid")
	}
	from, err := r.slackSources(ctx)
	if err != nil {
		return nil, err
	}
	r.announce("Source: live Slack. This check reads only the selected mapped thread.")
	started := time.Now()
	r.slackProgress("private_state", "running")
	if !evidence.ValidAccountID(account, from.backend("crm") == "salesforce") {
		return nil, slackRefusal("slack_selection_invalid")
	}
	opened, err := from.slack(ctx)
	if err != nil {
		return nil, err
	}
	live, isLive := opened.(liveSlack)
	if !isLive {
		return nil, slackRefusal("slack_source_mismatch")
	}
	steps := []any{slackStep("private_state", "pass", 1, durationMS(time.Since(started)), 0, 0, "")}
	r.slackProgress("private_state", "pass")
	r.slackProgress("thread_read", "running")
	reader := *live.reader
	var reports []slack.Report
	reader.OnRead = func(report slack.Report) { reports = append(reports, report) }
	token, run := randomToken(), "slack-check-"+strings.ReplaceAll(receipts.RandomID(), "-", "")
	var snapshot *strictjson.Object
	finished, records, err := from.call(ctx, toolCall{token: receipts.Fingerprint(token), scopes: []string{"slack:read"},
		run: run, request: 1, tool: "read_slack_messages", arguments: strictjson.NewObject("account_id", account),
		account: account}, func(ctx context.Context) ([]*strictjson.Object, error) {
		read, sealed, err := reader.ReadThread(ctx, account)
		snapshot = sealed
		return read, err
	})
	code := "ok"
	var refused *slack.Error
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, receipts.ErrSourceChanged):
		return nil, err
	case errors.As(err, &refused):
		code = refused.Code
	case err != nil:
		code = "slack_check_failed"
	case !evidence.ValidSlackSnapshot(records, snapshot, account, from.bound("crm"), from.bound("slack")):
		code, records = "slack_snapshot_invalid", nil
	}
	sourceOK := code == "ok"
	receiptStarted := time.Now()
	receiptOK := slackReceiptValid(finished, run, account, receipts.Fingerprint(token), records, sourceOK)
	receiptMS := durationMS(time.Since(receiptStarted))
	counts := map[string]any{}
	if len(reports) == 1 {
		report := reports[0]
		number := map[string]int{"api_calls": report.APICalls, "auth_calls": report.AuthCalls,
			"message_calls": report.MessageCalls, "response_bytes": report.ResponseBytes, "pages": report.Pages,
			"raw_rows": report.RawRows, "duplicates": report.Duplicates, "refresh_attempts": report.RefreshAttempts,
			"token_retries": report.TokenRetries, "rate_retries": report.RateRetries}
		for key, value := range number {
			counts[key] = value
		}
		validated := map[string]bool{"mapping": report.MappingValidated,
			"credential_binding": report.CredentialBindingValidated, "thread_read": report.PaginationComplete}
		spent := map[string]time.Duration{"mapping": report.MappingTime, "credential_binding": report.CredentialTime,
			"thread_read": report.ThreadTime}
		for _, phase := range slackPhases {
			calls, bytes := 0, 0
			switch phase {
			case "credential_binding":
				calls, bytes = report.AuthCalls, report.AuthResponseBytes
			case "thread_read":
				calls, bytes = report.MessageCalls, report.MessageResponseBytes
			}
			passed := validated[phase] && (phase != "thread_read" || sourceOK)
			failed := report.Phase == phase || calls > 0 || (phase == "thread_read" && validated[phase] && !sourceOK)
			phaseCode := "prerequisite_failed"
			switch {
			case report.Phase == phase:
				phaseCode = report.Code
			case phase == "thread_read" && !sourceOK:
				phaseCode = code
			}
			status, count := "not_run", 0
			switch {
			case passed:
				status, count = "pass", 1
				if phase == "thread_read" {
					count = len(records)
				}
			case failed:
				status = "fail"
			}
			steps = append(steps, slackStep(phase, status, count, durationMS(spent[phase]), calls, bytes, phaseCode))
		}
		if sourceOK && (snapshot == nil || report.Code != "ok" || report.ReturnedMessages != len(records) ||
			!slackCountsAgree(get(snapshot, "counts"), number, len(records))) {
			steps[len(steps)-1] = slackStep("thread_read", "fail", 0, durationMS(report.ThreadTime),
				report.MessageCalls, report.MessageResponseBytes, "slack_metrics_unavailable")
		}
	} else {
		// With no report of the read, the work it did is unknown, unless the read never began.
		var unknown any
		if finished.Status == "" {
			unknown = 0
		}
		for _, key := range []string{"api_calls", "auth_calls", "message_calls", "response_bytes", "pages", "raw_rows",
			"duplicates", "refresh_attempts", "token_retries", "rate_retries"} {
			counts[key] = unknown
		}
		for _, phase := range slackPhases {
			if phase == "thread_read" {
				steps = append(steps, slackStep(phase, "fail", 0, 0, unknown, unknown, "slack_metrics_unavailable"))
			} else {
				steps = append(steps, slackStep(phase, "not_run", 0, 0, unknown, unknown, "prerequisite_failed"))
			}
		}
	}
	receiptStatus, receiptCount := "fail", 0
	if receiptOK {
		receiptStatus, receiptCount = "pass", 1
	}
	steps = append(steps, slackStep("receipt", receiptStatus, receiptCount, receiptMS, 0, 0, "slack_receipt_invalid"))
	passed, next := true, ""
	for _, step := range steps {
		object := step.(*strictjson.Object)
		r.slackProgress(get(object, "name").(string), get(object, "status").(string))
		if get(object, "status") != "pass" {
			passed = false
		}
		if get(object, "status") == "fail" && next == "" {
			next, _ = get(object, "next").(string)
		}
	}
	status, messages := "failed", 0
	if passed {
		status, messages = "passed", len(records)
	}
	shown := strictjson.NewObject()
	for _, key := range []string{"auth_calls", "message_calls", "pages", "raw_rows", "duplicates", "refresh_attempts",
		"token_retries", "rate_retries"} {
		shown.Set(key, counts[key])
	}
	shown.Set("messages", messages)
	return strictjson.NewObject("schema_version", 1, "status", status, "crm_access_checked", false,
		"calls_access_checked", false, "api_calls", counts["api_calls"], "response_bytes", counts["response_bytes"],
		"counts", shown, "steps", steps, "next", next, "duration_ms", roundTo3(durationMS(time.Since(started)))), nil
}

// slackCountsAgree is whether a snapshot's counts are the read's report.
func slackCountsAgree(counts any, number map[string]int, messages int) bool {
	o, isObject := counts.(*strictjson.Object)
	if !isObject {
		return false
	}
	pairs := map[string]string{"api_calls": "http_attempts", "auth_calls": "auth_attempts",
		"message_calls": "message_attempts", "response_bytes": "response_bytes", "pages": "pages",
		"raw_rows": "raw_rows", "duplicates": "duplicates", "refresh_attempts": "refresh_attempts",
		"token_retries": "token_retries", "rate_retries": "rate_retries"}
	for key, field := range pairs {
		if cellText(get(o, field)) != fmt.Sprint(number[key]) {
			return false
		}
	}
	return cellText(get(o, "messages")) == fmt.Sprint(messages)
}

// slackReceiptValid is whether the read's receipt is the one this check made, finished as the read ended,
// binding exactly the records it returned.
func slackReceiptValid(receipt receipts.Receipt, run, account, token string, records []*strictjson.Object, ok bool) bool {
	arguments, isObject := receipt.Arguments.(*strictjson.Object)
	want := "error"
	if ok {
		want = "ok"
	}
	if receipt.AuditID == "" || receipt.BriefID == nil || *receipt.BriefID != run || receipt.Tool != "read_slack_messages" ||
		!isObject || len(arguments.Keys) != 1 || get(arguments, "account_id") != account || receipt.TokenID != token ||
		receipt.Status != want || receipt.HTTPStatus == nil || *receipt.HTTPStatus < 200 || *receipt.HTTPStatus > 599 {
		return false
	}
	var references []string
	if ok {
		for _, record := range records {
			reference, _ := get(record, "record_ref").(string)
			references = append(references, reference)
		}
	}
	rows := slices.Clone(receipt.Rows)
	sort.Strings(rows)
	sort.Strings(references)
	return slices.Equal(rows, references)
}

// slackText is a debug slack result as text.
func (r *run) slackText(result *strictjson.Object) string {
	switch r.action {
	case "map":
		return "Slack thread selection " + cellText(get(result, "status")) + ". Thread access has not been checked.\n" +
			"Next: gtm-connector debug slack check --help"
	case "check":
		headline := "Slack thread check " + cellText(get(result, "status")) + ". CRM and calls access were not checked.\n"
		keys := []string{"name", "status", "count", "duration_ms", "api_calls", "response_bytes"}
		headers := []string{"STEP", "STATUS", "COUNT", "MS", "API CALLS", "BYTES"}
		var rows [][]any
		var failed []string
		for _, step := range get(result, "steps").([]any) {
			object := step.(*strictjson.Object)
			var row []any
			for _, key := range keys {
				row = append(row, get(object, key))
			}
			if !r.screen.terminal {
				row = append(row, get(object, "message"), get(object, "next"))
			} else if get(object, "status") == "fail" {
				text := oneLine(cellText(get(object, "message")))
				if next, _ := get(object, "next").(string); next != "" {
					text += "\nNext: " + oneLine(next)
				}
				failed = append(failed, text)
			}
			rows = append(rows, row)
		}
		if !r.screen.terminal {
			headers = append(headers, "DETAIL", "NEXT")
		}
		text := headline + r.screen.table(headers, rows)
		for _, detail := range failed {
			text += "\n\n" + detail
		}
		return text
	case "disconnect":
		local := "No local Slack credentials were present."
		if get(result, "local_credentials_removed") == true {
			local = "Local Slack credentials were removed."
		}
		if get(result, "remote_revoke_requested") != true {
			return local + "\nRemote access was not revoked."
		}
		remote := "Remote access-token revocation is unconfirmed."
		if get(result, "remote_access_token_revoked") == true {
			remote = "Slack confirmed revocation of this access token."
		}
		return local + "\n" + remote + "\nApp removal and other credentials are managed separately in Slack."
	}
	if get(result, "configured") != true {
		return "No Slack connection is saved. An existing registered public app is required.\n" +
			"Next: gtm-connector debug slack connect --help"
	}
	status := "gtm-connector debug slack status --check" + r.here()
	var headline, next string
	needsLogin := get(result, "needs_login") == true
	switch state := get(result, "credential_state"); {
	case state == "identity_unchecked" && !needsLogin:
		headline, next = "Saved credentials require an authentication check.", status
	case state == "refresh_pending":
		headline, next = "A previous credential update or authentication check did not finish safely. "+
			"Browser authorization is required.", "gtm-connector debug slack connect --help"
	case needsLogin:
		headline, next = "Saved Slack authentication is no longer usable. Browser authorization is required.",
			"gtm-connector debug slack connect --help"
	case get(result, "verification") == "live" && r.action == "connect":
		headline = "Slack authentication is saved and verified. No messages were read."
	case get(result, "verification") == "live":
		headline = "Slack authentication is verified. No messages were read."
	default:
		headline, next = "Saved Slack authentication is available. This command did not contact Slack.", status
	}
	var scopes []string
	for _, scope := range get(result, "granted_scopes").([]any) {
		scopes = append(scopes, scope.(string))
	}
	rotation := "no"
	if get(result, "rotation") == true {
		rotation = "yes"
	}
	expires := get(result, "access_expires_at")
	if expires == nil {
		expires = "No scheduled expiry; access can still be revoked."
	}
	text := headline + "\n" + r.screen.table([]string{"FIELD", "VALUE"}, [][]any{
		{"Granted user scopes", strings.Join(scopes, ", ")}, {"Token rotation", rotation}, {"Access expires", expires},
		{"Last authentication check", get(result, "last_verified_at")}})
	if next != "" {
		text += "\nNext: " + next
	}
	return text
}

// slackFailed is an error of debug slack as the command reports it.
func (r *run) slackFailed(ctx context.Context, err error) *failure {
	var refused *slack.Error
	switch {
	case ctx.Err() != nil && (r.action == "map" || r.action == "check"):
		return &failure{code: 130, next: "gtm-connector debug slack " + r.action + " --help",
			message: "The Slack source operation was interrupted. Existing state and committed receipts are retained. " +
				"Check the selected state before retrying."}
	case ctx.Err() != nil:
		return &failure{code: 130, next: "gtm-connector debug slack status" + r.here(),
			message: "Slack authorization was interrupted. Inspect saved status; completed remote approval or " +
				"local saves may remain."}
	case !errors.As(err, &refused):
		return r.failed(ctx, err)
	case r.action == "map" || r.action == "check":
		return slackSourceFailure(refused)
	}
	code, exit := refused.Code, refused.ExitCode()
	if _, known := slackAuthFailures[code]; !known || exit == 0 {
		code, exit = "slack_storage_failed", 1
	}
	next := "gtm-connector debug slack connect --help"
	switch code {
	case "slack_response_invalid", "slack_rate_limited", "slack_timeout", "slack_unavailable":
		next = "gtm-connector debug slack status" + r.here()
	case "slack_connection_busy", "slack_storage_failed":
		next = "gtm-connector debug slack " + r.action + " --help"
	}
	return &failure{code: exit, next: next, message: code + ": " + slackAuthFailures[code]}
}

// slackRefusal is the failure of a fixed code of the Slack source.
func slackRefusal(code string) *failure { return slackSourceFailure(&slack.Error{Code: code}) }

// slackSourceFailure is a refusal of map, check or a card's Slack read: its code, what to do, and its exit
// code. A sign-in refusal says what slack_auth.py says of it.
func slackSourceFailure(refused *slack.Error) *failure {
	exit, known := slackExits[refused.Code]
	if !known {
		return &failure{code: 1, next: "gtm-connector debug slack status --help",
			message: "slack_storage_failed: " + slackAuthFailures["slack_storage_failed"]}
	}
	if message, auth := slackAuthFailures[refused.Code]; auth && slackAuthOnly[refused.Code] {
		return &failure{code: exit, next: "gtm-connector debug slack connect --help", message: refused.Code + ": " + message}
	}
	message, next := slackDetails(refused.Code)
	return &failure{code: exit, next: next, message: refused.Code + ": " + message}
}

// slackDetails is slack_workflow.slack_failure_details: what a code means and the command to run next.
func slackDetails(code string) (string, string) {
	if detail, known := slackDiagnostics[code]; known {
		return detail[0], detail[1]
	}
	detail := slackDiagnostics["slack_check_failed"]
	return detail[0], detail[1]
}

// slackExits are the exit codes of every refusal of the Slack commands, as docs/sources.md lists them.
var slackExits = map[string]int{
	"slack_invalid_configuration": 2, "slack_connection_busy": 1, "slack_private_state_invalid": 3,
	"slack_credentials_invalid": 4, "slack_not_connected": 3, "slack_reconnect_required": 3,
	"slack_browser_unavailable": 1, "slack_callback_unavailable": 1, "slack_login_timeout": 1,
	"slack_login_denied": 3, "slack_login_unconfirmed": 3, "slack_oauth_rejected": 3, "slack_scope_mismatch": 3,
	"slack_identity_mismatch": 3, "slack_token_type_unsupported": 3, "slack_selection_invalid": 2, "slack_crm_configuration_invalid": 2,
	"slack_account_not_found": 2, "slack_account_ambiguous": 3, "slack_association_failed": 4,
	"slack_association_invalid": 4, "slack_crm_unavailable": 5, "slack_mapping_missing": 3,
	"slack_mapping_conflict": 3, "slack_source_mismatch": 3, "slack_identity_unchecked": 3,
	"slack_thread_unavailable": 3, "slack_forbidden": 3, "slack_thread_changed": 3, "slack_thread_incomplete": 3,
	"slack_resource_limit": 3, "slack_output_too_large": 3, "slack_clock_invalid": 3, "slack_mapping_invalid": 4,
	"slack_snapshot_invalid": 4, "slack_response_invalid": 5, "slack_rate_limited": 5, "slack_timeout": 5,
	"slack_unavailable": 5, "slack_storage_failed": 1,
}

// slackAuthOnly are the refusals of the saved sign-in that a source command reports in the sign-in's words.
var slackAuthOnly = map[string]bool{"slack_not_connected": true, "slack_reconnect_required": true,
	"slack_credentials_invalid": true, "slack_private_state_invalid": true, "slack_connection_busy": true}

// slackAuthFailures are cli.SLACK_FAILURES: what each refusal of a sign-in command means.
var slackAuthFailures = map[string]string{
	"slack_invalid_configuration": "The Slack connection options are invalid. Use an existing public app and its " +
		"exact registered localhost redirect.",
	"slack_connection_busy": "Another Slack connection command is running. Finish or cancel it, then retry.",
	"slack_private_state_invalid": "The connection folder is not valid private Slack state. " +
		"Use the original private connection folder or an unused one.",
	"slack_credentials_invalid": "The saved credentials failed validation. Retain the file privately and " +
		"reconnect in an unused connection folder.",
	"slack_not_connected":        "No usable Slack connection is saved. Connect with an existing registered public app.",
	"slack_browser_unavailable":  "The system browser could not open. Retry from a session with a working browser.",
	"slack_callback_unavailable": "The registered localhost callback could not start. Free its port and retry.",
	"slack_login_timeout":        "Browser approval did not finish within ten minutes. Repeat the connect command.",
	"slack_login_denied":         "Slack authorization was declined. Retry only when you intend to approve the user scopes.",
	"slack_login_unconfirmed": "The Slack approval was not confirmed at this terminal, and nothing was saved. " +
		"Repeat the connect command, and type yes only for the Slack user you approved as.",
	"slack_oauth_rejected": "Slack rejected authorization. Check the existing app's PKCE, registered redirect, " +
		"and workspace approval settings.",
	"slack_scope_mismatch": "The grant does not match the selected history scopes. " +
		"Use an existing grant with exactly those scopes.",
	"slack_identity_mismatch": "The connection identity does not match. Restore the original identity or " +
		"use another private connection folder.",
	"slack_token_type_unsupported": "The grant is not a supported user token. Use a user-only PKCE desktop grant.",
	"slack_reconnect_required":     "Saved Slack authentication is no longer usable. Browser authorization is required.",
	"slack_response_invalid": "Slack returned an invalid response. Inspect the saved state before retrying; " +
		"an unsafe credential update requires reconnecting.",
	"slack_rate_limited": "Slack limited this request. Wait before explicitly retrying the operation.",
	"slack_timeout":      "The Slack request exceeded its deadline. Check connectivity and explicitly retry.",
	"slack_unavailable":  "Slack could not complete the request. Check connectivity and explicitly retry.",
	"slack_storage_failed": "The private Slack operation could not finish. Check private storage access. " +
		"Completed remote authorization may remain active and can be removed in Slack settings.",
}

const (
	slackMapNext     = "gtm-connector debug slack map --help"
	slackCheckNext   = "gtm-connector debug slack check --help"
	slackStatusNext  = "gtm-connector debug slack status --help"
	slackConnectNext = "gtm-connector debug slack connect --help"
)

// slackDiagnostics are slack_workflow._DIAGNOSTICS: what each code of the Slack source means, and the
// command to run next. The commands named are this build's.
var slackDiagnostics = map[string][2]string{
	"slack_selection_invalid": {"The account or thread selection is invalid. Check the account and link, " +
		"or use --channel with --thread-ts.", slackMapNext},
	"slack_mapping_missing":  {"No thread is selected for this account. Save a thread selection first.", slackMapNext},
	"slack_mapping_conflict": {"The selection conflicts with a saved mapping. Review --replace and --reassign.", slackMapNext},
	"slack_mapping_invalid":  {"The saved mapping is invalid. Preserve the private state and inspect its setup.", slackMapNext},
	"slack_source_mismatch":  {"The source identity does not match the saved selection. Restore the original setup.", slackMapNext},
	"slack_identity_unchecked": {"The saved Slack identity needs verification. Run debug slack status --check first.",
		slackStatusNext},
	"slack_account_not_found": {"CRM did not return the selected account. Find its canonical account ID.",
		"gtm-connector accounts --help"},
	"slack_account_ambiguous": {"CRM could not resolve one literal account match. Use its canonical --account ID.", slackMapNext},
	"slack_association_failed": {"The audited CRM lookup did not succeed. Check the selected CRM connection.",
		slackMapNext},
	"slack_association_invalid": {"The CRM result or its receipt did not validate. No selection was saved.", slackMapNext},
	"slack_crm_unavailable": {"The CRM lookup could not finish. Check CRM access before selecting a thread.",
		"gtm-connector connect --help"},
	"slack_crm_configuration_invalid": {"The CRM connection options are invalid. Correct the selected connection.",
		slackMapNext},
	"slack_snapshot_invalid": {"The Slack snapshot failed its consistency checks. Preserve it and repeat the read.",
		slackCheckNext},
	"slack_thread_unavailable": {"The selected thread was not accessible. Check its parent selection and visibility.",
		slackMapNext},
	"slack_forbidden": {"Slack did not permit the selected thread read. Check the history grant and thread access.",
		slackConnectNext},
	"slack_thread_changed": {"Repeated messages disagreed during paging. Retry the read when the thread is stable.",
		slackCheckNext},
	"slack_thread_incomplete": {"The selected thread could not be read completely. Check its parent and retry.",
		slackMapNext},
	"slack_resource_limit":   {"The read reached its request, row or byte bound. Select a smaller thread.", slackMapNext},
	"slack_output_too_large": {"The thread text exceeds the source-output bound. Select a smaller thread.", slackMapNext},
	"slack_clock_invalid":    {"The local clock moved backward. Correct the clock before retrying.", slackCheckNext},
	"slack_response_invalid": {"Slack returned an unsupported response. Retry or select a supported text thread.",
		slackCheckNext},
	"slack_rate_limited": {"Slack's rate limit could not be retried within the read budget. Wait before retrying.",
		slackCheckNext},
	"slack_timeout":     {"The Slack operation exceeded its deadline. Inspect authentication and retry.", slackStatusNext},
	"slack_unavailable": {"Slack could not be reached. Retry when the connection is available.", slackCheckNext},
	"slack_reconnect_required": {"Saved Slack authentication is unusable. Browser authorization is required.",
		slackConnectNext},
	"slack_not_connected": {"No usable Slack connection is saved. Authorize an existing public app.", slackConnectNext},
	"slack_invalid_configuration": {"The Slack connection options are invalid. Check the registered app setup.",
		slackConnectNext},
	"slack_oauth_rejected": {"Slack rejected the authorization request. Check the existing app and its approval.",
		slackConnectNext},
	"slack_connection_busy": {"Another command holds the Slack connection. Finish or cancel it before retrying.",
		slackStatusNext},
	"slack_private_state_invalid": {"The Slack credential folder is unsafe. Use the original private folder.",
		slackStatusNext},
	"slack_credentials_invalid": {"The saved Slack credentials are invalid. Preserve them and reconnect separately.",
		slackConnectNext},
	"slack_identity_mismatch": {"The Slack identity changed. Restore the identity selected for this source.", slackMapNext},
	"slack_scope_mismatch": {"The Slack grant does not match the selected history scopes. Authorize them explicitly.",
		slackConnectNext},
	"slack_token_type_unsupported": {"The Slack credential is not a supported user grant. Authorize a user grant.",
		slackConnectNext},
	"slack_storage_failed": {"Private Slack state could not be read or saved. Correct local storage access.",
		slackStatusNext},
	"slack_check_failed": {"The Slack check could not validate a complete result. Inspect setup before retrying.",
		slackCheckNext},
	"slack_metrics_unavailable": {"The check could not verify source-work counts. No successful check is reported.",
		slackCheckNext},
	"slack_receipt_invalid": {"The check could not match a final receipt to the validated result. " +
		"No successful check is reported.", "gtm-connector audit --help"},
	"prerequisite_failed": {"This step could not finish because an earlier required step failed.", ""},
}

const slackHelp = `usage: gtm-connector debug slack [-h] [--json] [--state-dir STATE_DIR]
                                 ACTION ...

Manage Slack authentication, select a thread, or check its audited read.

positional arguments:
  ACTION
    connect             Authorize an existing public Slack app through the
                        system browser.
    status              Inspect saved authentication, or explicitly verify it
                        with Slack.
    disconnect          Remove local credentials, with an optional access-
                        token revocation attempt.
    map                 Save one thread selection for an account after an
                        audited CRM lookup.
    check               Read the selected thread through the audited Slack
                        tool without accessing CRM or calls.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo). Its sign-in is
                        kept in the credentials folder.

Examples:
  gtm-connector debug slack connect --help
  gtm-connector debug slack status
  gtm-connector debug slack map --help
  gtm-connector debug slack check --help
  gtm-connector debug slack disconnect
`

const slackConnectHelp = `usage: gtm-connector debug slack connect [-h] [--json] [--state-dir STATE_DIR]
                                         --client-id CLIENT_ID
                                         --redirect-uri REDIRECT_URI
                                         [--user-scope {channels:history,groups:history,users:read,users:read.email}]
                                         [--team-id TEAM_ID]

Authorize an existing public Slack app through the system browser.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --client-id CLIENT_ID
                        Select an existing PKCE-enabled public Slack app.
  --redirect-uri REDIRECT_URI
                        Use the exact registered
                        http://localhost:PORT/slack/callback redirect.
  --user-scope {channels:history,groups:history,users:read,users:read.email}
                        Request this user scope. Repeat for each; default:
                        channels:history. users:read and users:read.email
                        let a card read each author's team and profile
                        email.
  --team-id TEAM_ID     Require this workspace identity without displaying it.

Example: gtm-connector debug slack connect --client-id EXISTING_PUBLIC_CLIENT_ID --redirect-uri http://localhost:8765/slack/callback
Replace EXISTING_PUBLIC_CLIENT_ID with your existing app's public client ID. PKCE and that exact localhost redirect must already be registered. Browser approval has a ten-minute deadline. No messages are read.
`

const slackStatusHelp = `usage: gtm-connector debug slack status [-h] [--json] [--state-dir STATE_DIR]
                                        [--check]

Inspect saved authentication, or explicitly verify it with Slack.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --check               Verify authentication with Slack, refreshing once if
                        needed. No messages are read.

Example: gtm-connector debug slack status
`

const slackDisconnectHelp = `usage: gtm-connector debug slack disconnect [-h] [--json]
                                            [--state-dir STATE_DIR] [--revoke]

Remove local credentials, with an optional access-token revocation attempt.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --revoke              Attempt access-token revocation before local removal,
                        without refreshing.

Example: gtm-connector debug slack disconnect
Local removal does not revoke remote access. Even confirmed access-token revocation does not prove app removal or revocation of other credentials. Manage the whole grant in Slack.
`

const slackMapHelp = `usage: gtm-connector debug slack map [-h] [--json] [--state-dir STATE_DIR]
                                     (--account ACCOUNT |
                                     --account-number ACCOUNT_NUMBER)
                                     (--thread-url THREAD_URL |
                                     --channel CHANNEL)
                                     [--thread-ts THREAD_TS] [--replace]
                                     [--reassign]

Save one thread selection for an account after an audited CRM lookup.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --account ACCOUNT     Associate this canonical CRM account ID.
  --account-number ACCOUNT_NUMBER
                        Resolve this exact Salesforce AccountNumber. Not in
                        this build.
  --thread-url THREAD_URL
                        Select a copied thread link from the saved Slack
                        workspace.
  --channel CHANNEL     Select this channel ID together with --thread-ts.
  --thread-ts THREAD_TS
                        Use the exact parent timestamp with --channel.
  --replace             Replace this account's selected thread.
  --reassign            Move a thread from another account.

Example: gtm-connector debug slack map --account acc-003 --channel C12345678 --thread-ts 1790000000.000001
`

const slackCheckHelp = `usage: gtm-connector debug slack check [-h] [--json] [--state-dir STATE_DIR]
                                       --account ACCOUNT

Read the selected thread through the audited Slack tool without accessing CRM or calls.

options:
  -h, --help            show this help message and exit
  --json                Write the documented JSON result for scripts.
  --state-dir STATE_DIR
                        Use this workspace (default: demo).
  --account ACCOUNT     Check this mapped canonical CRM account ID.

Example: gtm-connector debug slack check --account acc-003
`
