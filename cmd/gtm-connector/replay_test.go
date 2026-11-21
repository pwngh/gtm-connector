package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/oracletest"
)

// The command session is testdata/oracle/commands/session.jsonl: the Python CLI's arguments, exit code and
// output for each step, run in order against one state folder. The replay runs the Go binary through the same
// steps and compares it with the recording, after the normalization tools/oracle/commands.py applies.
//
// The recorder ran every step on its own clock: each receipt one whole second after the one before it, and no
// elapsed time. The recording's card lines, receipt times and elapsed_ms hold that clock. The replay does not
// set the binary's clock, so the steps that print those match only a binary that runs on the recorder's.

// notPorted names the recorded steps the Go binary does not reproduce yet. Each one is still run and must
// still differ from the recording. Porting a command removes its steps from this list, and a step that is not
// listed must match the recording.
var notPorted = []string{
	"no arguments as JSON", "version", "unknown command", "brief without an account",
	"connect demo", "connect status",
	"demo",
	"accounts", "accounts by name", "accounts by a literal pattern character",
	"brief", "brief without calls", "brief as of another day", "brief of an unknown account",
	"brief of a malformed account", "brief on an impossible day",
	"inspect an account", "inspect a card", "inspect numbers", "inspect by role",
	"inspect a number off the card", "inspect an unknown card", "inspect a receipt",
	"audit", "audit by tool", "audit by brief", "audit of a missing folder",
	"mcp help", "mcp without its action", "mcp without a scope",
	"brief on a text that is not a day", "brief with an argument it does not take",
}

// needsDatabase names the steps whose recorded outcome depends on what the state folder holds. The Go program
// keeps that state in PostgreSQL, so these steps run only with GTM_TEST_DATABASE_URL.
var needsDatabase = []string{
	"connect demo", "connect status",
	"demo",
	"accounts", "accounts by name", "accounts by a literal pattern character",
	"brief", "brief without calls", "brief as of another day", "brief of an unknown account",
	"inspect an account", "inspect a card", "inspect numbers", "inspect by role",
	"inspect a number off the card", "inspect an unknown card", "inspect a receipt",
	"audit", "audit by tool", "audit by brief", "audit of a missing folder",
}

// recorderGiven holds the one ID the recorder passes that no command printed: "inspect an unknown card" asks
// for a card that was never saved. The recording holds its placeholder only.
var recorderGiven = map[string]string{"<brief-5>": "brief-" + strings.Repeat("0", 32)}

// helpNotCompared marks a step whose output is help text. Only its exit code is compared.
const helpNotCompared = "help text is reviewed, not compared"

// step is one line of the recording.
type step struct {
	Label    string   `json:"label"`
	Argv     []string `json:"argv"`
	Exit     int      `json:"exit"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	Compared string   `json:"compared"`
}

func recordedSession(t *testing.T) []step {
	t.Helper()
	var steps []step
	for _, line := range oracletest.Lines(t, "commands/session.jsonl") {
		var s step
		if err := json.Unmarshal(line, &s); err != nil {
			t.Fatal(err)
		}
		if s.Compared != "exactly" && s.Compared != helpNotCompared {
			t.Fatalf("%q is compared %q, which the replay does not know", s.Label, s.Compared)
		}
		steps = append(steps, s)
	}
	return steps
}

// The values the recorder finds by their shape, in its order.
var shapes = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"uuid", regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)},
	{"brief", regexp.MustCompile(`brief-[0-9a-f]{32}`)},
	{"run", regexp.MustCompile(`(?:demo|inspect|import)-[0-9a-f]{32}`)},
}

// A port is the digits after the loopback address.
const loopback = "127.0.0.1:"

var (
	// A card's digest follows its brief ID in a seal receipt's row.
	sealed = regexp.MustCompile(`(briefs/<brief-[0-9]+>/)[0-9a-f]{64}`)
	// A numbered placeholder, as an argument of a later step holds it.
	numbered = regexp.MustCompile(`<(?:uuid|brief|run|port|token|time)-[0-9]+>`)
	// A character that makes shlex.quote put a path in quotes.
	needsQuotes = regexp.MustCompile(`[^a-zA-Z0-9_@%+=:,./-]`)
)

// The recorder issues every token and receipt time itself, inside the Python process, so it knows their
// values. The replay sees only what the binary prints. It takes them from the places a command prints one: a
// receipt's token_id and started_at, and the card line that names its first and last read.
func tokenAt(value string) *regexp.Regexp {
	return regexp.MustCompile(`"token_id": "(` + value + `)"`)
}

func timeAt(value string) *regexp.Regexp {
	return regexp.MustCompile(`"started_at": "(` + value + `)"|Sources read (` + value + `)(?: to (` + value + `))?`)
}

var (
	printedToken  = tokenAt(`[0-9a-f]{64}`)
	printedTime   = timeAt(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z`)
	recordedToken = tokenAt(`<token-[0-9]+>`)
	recordedTime  = timeAt(`<time-[0-9]+>`)
)

// replay is the binary, the host it runs under, and the values numbered so far in the session.
type replay struct {
	binary, dir   string
	env           []string
	folder, state string
	tokens, times map[string]bool
	numbers       map[string]string // a value to its placeholder
	order         []string          // the values, in the order they were numbered
	values        map[string]string // a placeholder to the value an argument gets for it
}

func newReplay(folder string) *replay {
	r := &replay{folder: folder, state: filepath.Join(folder, "state"), tokens: map[string]bool{},
		times: map[string]bool{}, numbers: map[string]string{}, values: map[string]string{}}
	for placeholder, value := range recorderGiven {
		r.values[placeholder] = value
	}
	return r
}

// unquoted writes an argument the binary shell-quoted because it holds the folder as it would be without the
// quotes, so a folder whose name needs quoting normalizes as one that does not.
func unquoted(text, path string) string {
	if path != "" && !needsQuotes.MatchString(path) {
		return text
	}
	const quote = `'"'"'`
	inside := strings.ReplaceAll(path, "'", quote)
	quoted := regexp.MustCompile("'" + regexp.QuoteMeta(inside) + `((?:[^']|` + quote + `)*)'`)
	return quoted.ReplaceAllStringFunc(text, func(found string) string {
		return path + strings.ReplaceAll(found[1+len(inside):len(found)-1], quote, "'")
	})
}

// learn adds the tokens and receipt times a text prints to the ones the session knows.
func (r *replay) learn(text string) {
	for _, found := range printedToken.FindAllStringSubmatch(text, -1) {
		r.tokens[found[1]] = true
	}
	for _, found := range printedTime.FindAllStringSubmatch(text, -1) {
		for _, value := range found[1:] {
			if value != "" {
				r.times[value] = true
			}
		}
	}
}

// normalize is Session.normalize of tools/oracle/commands.py: the folder becomes <state> or <folder>, each ID,
// port, token fingerprint and receipt time becomes a placeholder numbered by where its value first appears in
// the session, and a seal's digest becomes <sha256>.
func (r *replay) normalize(text string) string {
	text = unquoted(text, r.folder)
	text = strings.ReplaceAll(strings.ReplaceAll(text, r.state, "<state>"), r.folder, "<folder>")
	type hit struct {
		start       int
		name, value string
	}
	var found []hit
	for _, shape := range shapes {
		for _, at := range shape.pattern.FindAllStringIndex(text, -1) {
			found = append(found, hit{at[0], shape.name, text[at[0]:at[1]]})
		}
	}
	for from := 0; ; {
		at := strings.Index(text[from:], loopback)
		if at < 0 {
			break
		}
		from += at + len(loopback)
		end := from
		for end < len(text) && end-from < 5 && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if end > from {
			found = append(found, hit{from, "port", text[from:end]})
		}
	}
	for name, known := range map[string]map[string]bool{"token": r.tokens, "time": r.times} {
		for value := range known {
			for from := 0; ; {
				at := strings.Index(text[from:], value)
				if at < 0 {
					break
				}
				found = append(found, hit{from + at, name, value})
				from += at + len(value)
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.value < b.value
	})
	for _, h := range found {
		if _, ok := r.numbers[h.value]; ok {
			continue
		}
		count := 1
		for _, placeholder := range r.numbers {
			if strings.HasPrefix(placeholder, "<"+h.name+"-") {
				count++
			}
		}
		placeholder := fmt.Sprintf("<%s-%d>", h.name, count)
		r.numbers[h.value] = placeholder
		r.order = append(r.order, h.value)
		if _, given := r.values[placeholder]; !given {
			r.values[placeholder] = h.value
		}
	}
	longest := append([]string(nil), r.order...)
	sort.SliceStable(longest, func(i, j int) bool { return len(longest[i]) > len(longest[j]) })
	for _, value := range longest {
		text = strings.ReplaceAll(text, value, r.numbers[value])
	}
	return sealed.ReplaceAllString(text, "${1}<sha256>")
}

// run runs one recorded step as the recorder did, with the placeholders in its arguments filled from this
// session, and returns what differs from the recording, or "" when nothing does.
func (r *replay) run(t *testing.T, s step) string {
	t.Helper()
	argv := make([]string, len(s.Argv))
	for i, recorded := range s.Argv {
		unknown := ""
		argv[i] = numbered.ReplaceAllStringFunc(recorded, func(placeholder string) string {
			value, ok := r.values[placeholder]
			if !ok {
				unknown = placeholder
			}
			return value
		})
		if unknown != "" {
			return fmt.Sprintf("the step did not run: no earlier step printed the value of %s", unknown)
		}
		argv[i] = strings.ReplaceAll(strings.ReplaceAll(argv[i], "<state>", r.state), "<folder>", r.folder)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, r.binary, argv...)
	command.Env, command.Dir, command.WaitDelay = r.env, r.dir, 5*time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	exit := 0
	var exited *exec.ExitError
	if err := command.Run(); ctx.Err() != nil {
		return "the step did not end within a minute"
	} else if errors.As(err, &exited) {
		exit = exited.ExitCode()
	} else if err != nil {
		t.Fatalf("the binary did not run: %v", err)
	}
	// The recorder knows a step's tokens and times before it normalizes the step's output, then normalizes
	// stdout, stderr and each argument in that order.
	r.learn(stdout.String())
	r.learn(stderr.String())
	out, errs := r.normalize(stdout.String()), r.normalize(stderr.String())
	for _, argument := range argv {
		r.normalize(argument)
	}
	var differs []string
	if exit != s.Exit {
		differs = append(differs, fmt.Sprintf("exit code %d, recorded %d", exit, s.Exit))
	}
	if s.Compared != helpNotCompared {
		if out != s.Stdout {
			differs = append(differs, "stdout "+oracletest.FirstDifference(out, s.Stdout))
		}
		if errs != s.Stderr {
			differs = append(differs, "stderr "+oracletest.FirstDifference(errs, s.Stderr))
		}
	}
	return strings.Join(differs, "\n")
}

func TestRecordedCommandSession(t *testing.T) {
	steps := recordedSession(t)
	labels := map[string]bool{}
	for _, s := range steps {
		if labels[s.Label] {
			t.Fatalf("two recorded steps are named %q", s.Label)
		}
		labels[s.Label] = true
		// Every token and receipt time the recording prints is in a place the replay learns them from.
		for _, text := range []string{s.Stdout, s.Stderr} {
			rest := recordedTime.ReplaceAllString(recordedToken.ReplaceAllString(text, ""), "")
			if strings.Contains(rest, "<token-") || strings.Contains(rest, "<time-") {
				t.Fatalf("%q prints a token or a receipt time where the replay does not look for one", s.Label)
			}
		}
	}
	listed := func(names []string, list string) map[string]bool {
		set := map[string]bool{}
		for _, name := range names {
			if !labels[name] || set[name] {
				t.Fatalf("%s lists %q, which is not one recorded step", list, name)
			}
			set[name] = true
		}
		return set
	}
	pending, stateful := listed(notPorted, "notPorted"), listed(needsDatabase, "needsDatabase")

	root := t.TempDir()
	r := newReplay(filepath.Join(root, "session"))
	r.binary, r.dir = filepath.Join(root, "gtm-connector"), filepath.Join(root, "empty")
	home, temporary := filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, dir := range []string{r.folder, r.dir, home, temporary} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", r.binary, ".")
	build.Env = append(build.Environ(), "GOTOOLCHAIN=local")
	if log, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, log)
	}
	// The recorder's fixed host. It passes HOME and TMPDIR through; here both are temporary, so no command
	// reaches this machine's own install. The binary gets no other variable.
	r.env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C", "TZ=UTC", "NO_COLOR=1", "TERM=dumb",
		"COLUMNS=100", "LINES=50", "HOME=" + home, "TMPDIR=" + temporary}
	database := os.Getenv("GTM_TEST_DATABASE_URL") != "" || os.Getenv("GTM_REQUIRE_DATABASE") == "1"
	if database {
		// A database as setup leaves it, the app role's URL, and an empty credentials folder.
		d := setUpDemo(t)
		r.env = append(r.env, "GTM_OWNER_DATABASE_URL="+d.ownerURL, "GTM_DATABASE_URL="+d.appURL,
			"GTM_CREDENTIALS_DIR="+privateDir(t))
	}

	matched, waiting := 0, 0
	for i, s := range steps {
		t.Run(fmt.Sprintf("%02d %s", i+1, s.Label), func(t *testing.T) {
			if stateful[s.Label] && !database {
				waiting++
				t.Skip("GTM_TEST_DATABASE_URL is not set")
			}
			differs := r.run(t, s)
			if differs == "" {
				matched++
			}
			switch {
			case differs == "" && pending[s.Label]:
				t.Errorf("the binary now matches this step: remove %q from notPorted", s.Label)
			case differs != "" && pending[s.Label]:
				t.Logf("not ported: %.300s", differs)
			case differs != "":
				t.Errorf("gtm-connector %s\n%s", strings.Join(s.Argv, " "), differs)
			}
		})
	}
	summary := fmt.Sprintf("command replay: the Go binary matches %d of %d recorded steps", matched, len(steps))
	if waiting > 0 {
		summary += fmt.Sprintf("; %d were not run, because GTM_TEST_DATABASE_URL is not set", waiting)
	}
	t.Log(summary)
}

// The expected texts are what Session.normalize of tools/oracle/commands.py gives for the same two texts, in
// a folder whose name the shell quotes, with the same tokens and times known.
func TestReplayNormalizesAsTheRecorderDoes(t *testing.T) {
	const (
		folder = "/tmp/it's a folder/oracle-x"
		first  = `{"state": "/tmp/it's a folder/oracle-x/state", "next": "gtm-connector demo --state-dir ` +
			`'/tmp/it'"'"'s a folder/oracle-x/missing'", "audit_id": "11111111-2222-4333-8444-555555555555", ` +
			`"brief_id": "brief-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "run_id": "demo-0123456789abcdef0123456789abcdef", ` +
			`"url": "http://127.0.0.1:49152/mcp", ` +
			`"token_id": "abababababababababababababababababababababababababababababababab", ` +
			`"started_at": "2030-05-06T07:08:09.123456Z", "rows": ["briefs/brief-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/` +
			`9999999999999999999999999999999999999999999999999999999999999999"], ` +
			`"raw_sha256": "9999999999999999999999999999999999999999999999999999999999999999", ` +
			`"text": "Sources read 2030-05-06T07:08:10Z to 2030-05-06T07:08:11Z."}` + "\n"
		one = `{"state": "<state>", "next": "gtm-connector demo --state-dir <folder>/missing", ` +
			`"audit_id": "<uuid-1>", "brief_id": "<brief-1>", "run_id": "<run-1>", "url": "http://127.0.0.1:<port-1>/mcp", ` +
			`"token_id": "<token-1>", "started_at": "<time-1>", "rows": ["briefs/<brief-1>/<sha256>"], ` +
			`"raw_sha256": "9999999999999999999999999999999999999999999999999999999999999999", ` +
			`"text": "Sources read <time-2> to <time-3>."}` + "\n"
		second = `[{"audit_id": "99999999-2222-4333-8444-555555555555", "started_at": "2030-05-06T07:08:11Z", ` +
			`"token_id": "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", ` +
			`"brief_id": "inspect-ffffffffffffffffffffffffffffffff", "port": 49152, "at": "2030-05-06T07:08:12Z"}, ` +
			`{"audit_id": "11111111-2222-4333-8444-555555555555", "started_at": "2030-05-06T07:08:10Z", ` +
			`"token_id": "abababababababababababababababababababababababababababababababab", ` +
			`"brief_id": "brief-0123456789abcdef0123456789abcdef", "state": '/tmp/it'"'"'s a folder/oracle-x/state'}]` + "\n"
		two = `[{"audit_id": "<uuid-2>", "started_at": "<time-3>", "token_id": "<token-2>", "brief_id": "<run-2>", ` +
			`"port": <port-1>, "at": "2030-05-06T07:08:12Z"}, {"audit_id": "<uuid-1>", "started_at": "<time-2>", ` +
			`"token_id": "<token-1>", "brief_id": "<brief-2>", "state": <state>}]` + "\n"
	)
	r := newReplay(folder)
	for _, text := range []struct{ printed, normalized string }{{first, one}, {second, two}} {
		r.learn(text.printed)
		if got := r.normalize(text.printed); got != text.normalized {
			t.Errorf("normalized text %s", oracletest.FirstDifference(got, text.normalized))
		}
	}
	printed, given := r.values["<brief-2>"], r.values["<brief-5>"]
	if printed != "brief-0123456789abcdef0123456789abcdef" || given != recorderGiven["<brief-5>"] {
		t.Errorf("an argument would get %q for <brief-2> and %q for <brief-5>", printed, given)
	}
}
