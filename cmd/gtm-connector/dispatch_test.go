package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/receipts"
)

// line runs one command line as main does and gives its exit code and what it wrote.
func line(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = dispatch(ctx, argv, screen{out: &out, err: &errs})
	return code, out.String(), errs.String()
}

// The command line before any command: what to try first, the version, and the refusal of a command the
// binary does not have. None of it needs a database.
func TestTheCommandLineWithoutACommand(t *testing.T) {
	t.Setenv("GTM_DATABASE_URL", "")
	const listed = "argument COMMAND: invalid choice (choose from 'demo', 'connect', 'accounts', 'brief', 'inspect', " +
		"'audit', 'mcp', 'debug')"
	for _, c := range []struct {
		argv           []string
		code           int
		stdout, stderr string
	}{
		{nil, 0, description + "\n\nTry first:\n  gtm-connector demo\n  gtm-connector connect demo\n" +
			"  gtm-connector brief acc-007\n\nRun gtm-connector --help for all commands.\n", ""},
		{[]string{"--version"}, 0, "gtm-connector 0.1.0\n", ""},
		{[]string{"--json", "--version"}, 0, `{"version": "0.1.0"}` + "\n", ""},
		{[]string{"postgres://owner:invented-password@db.invented.example/gtm"}, 2, "",
			"Error: " + listed + "\nNext: gtm-connector --help\n"},
		{[]string{"mcp", "stdio", "--scope", "crm:write"}, 2, "", "Error: argument --scope: invalid choice " +
			"(choose from 'crm:read', 'calls:read', 'slack:read')\nNext: gtm-connector mcp --help\n"},
		{[]string{"debug", "doctor", "--json"}, 3, "", `{"error": {"code": "refused", "message": "debug doctor is not ` +
			`available in this build.", "next": "gtm-connector debug --help"}}` + "\n"},
		{[]string{"setup", "--json"}, 2, "", `{"error": {"code": "usage_error", "message": "setup writes text and ` +
			`takes no --json.", "next": "gtm-connector --help"}}` + "\n"},
		{[]string{"--help"}, 0, usage, ""},
		{[]string{"help"}, 0, usage, ""},
		{[]string{"help", "mcp"}, 0, mcpHelp, ""},
		{[]string{"help", "token"}, 0, tokenHelp, ""},
		{[]string{"help", "debug"}, 0, usage, ""},
		{[]string{"help", "connect"}, 0, connectHelp, ""},
		{[]string{"demo", "-h"}, 0, demoHelp, ""},
		{[]string{"demo", "--json"}, 2, "", `{"error": {"code": "usage_error", "message": "demo prints the cards it ` +
			`publishes as text. With --state-dir NAME it opens one renewal in that workspace and takes --json.", ` +
			`"next": "gtm-connector demo --help"}}` + "\n"},
		{[]string{"demo", "--again", "--state-dir", "invented"}, 2, "", "Error: --again publishes the demo book " +
			"again. It is given without --state-dir.\nNext: gtm-connector demo --help\n"},
	} {
		if code, stdout, stderr := line(t, c.argv...); code != c.code || stdout != c.stdout || stderr != c.stderr {
			t.Errorf("gtm-connector %v: exit %d\n%s%s", c.argv, code, stdout, stderr)
		}
	}
	// Every command the usage lists is one the binary takes, and the refusal of an unknown one names only
	// commands it takes.
	for _, name := range listedCommands {
		if _, stdout, stderr := line(t, name, "--help"); strings.Contains(stdout+stderr, "invalid choice") {
			t.Errorf("%s is listed and not taken", name)
		}
	}
}

// An error that is none of the kinds a command reports prints one fixed line. It holds nothing of the
// error's own text: not the host, the role, the database or the password a driver's message can name.
func TestAnUnexpectedErrorPrintsNoneOfItsText(t *testing.T) {
	const secret = "invented-password"
	answered := fmt.Errorf("database: %w", &pgconn.PgError{Severity: "FATAL", Code: "28P01",
		Message: `password authentication failed for user "invented_owner"`,
		Detail:  "Connection matched host db.invented.example with password " + secret})
	// A real refused connection, to a port of this machine that nothing listens on.
	_, unreached := pgconn.Connect(ctx, "postgres://invented_owner:"+secret+"@127.0.0.1:1/invented_database?connect_timeout=2")
	if unreached == nil || !strings.Contains(unreached.Error(), "invented_owner") {
		t.Fatalf("the refused connection: %v", unreached)
	}
	other := errors.New("dial db.invented.example: role invented_owner with password " + secret + " was rejected")
	for _, c := range []struct {
		err  error
		want string
	}{{answered, "PostgreSQL error 28P01"}, {unreached, "the database could not be reached"}, {other, "internal error"},
		{&pgconn.PgError{Code: "x' " + secret}, "internal error"}} {
		for _, asJSON := range []bool{false, true} {
			var out, errs bytes.Buffer
			s := screen{out: &out, err: &errs, json: asJSON}
			code := s.fail("accounts", (&run{name: defaultWorkspace}).failed(ctx, c.err))
			printed := out.String() + errs.String()
			if code != 1 || !strings.Contains(printed, c.want) || strings.Contains(printed, "invented") {
				t.Errorf("%v: exit %d\n%s", c.want, code, printed)
			}
		}
	}
}

// A source connected again while a card was being read fences the card's reads. The command says so and
// exits as refused.
func TestASourceChangedDuringAReadIsRefused(t *testing.T) {
	changed := (&run{name: defaultWorkspace}).failed(ctx, fmt.Errorf("get_account: %w", receipts.ErrSourceChanged))
	if changed.code != 3 || changed.message != "A source of this workspace was connected again while it was read; "+
		"nothing was published. Run the command again." {
		t.Errorf("exit %d: %s", changed.code, changed.message)
	}
}

// A store that fails under a command prints its SQLSTATE and nothing of the statement: here the app role
// has lost its right to read the connections.
func TestAFailingStorePrintsItsCodeOnly(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	if _, err := conn(t, d.ownerURL).Exec(ctx, "REVOKE SELECT ON connections FROM PUBLIC, "+d.appRole(t)); err != nil {
		t.Fatal(err)
	}
	for _, tokens := range [][]string{{"accounts"}, {"brief", "acc-003"}, {"connect"}, {"connect", "demo"},
		{"demo", "--state-dir", "demo"}} {
		if code, message, _ := d.refusal(t, 1, tokens...); code != "local_failure" || message != "PostgreSQL error 42501" {
			t.Errorf("%v: %s, %s", tokens, code, message)
		}
	}
}

// setup, serve, backup, restore and health print a fixed line for what they were given and what failed
// under them: a database URL set as the listen address is not quoted back, and a credentials folder that
// does not exist is named by what it is, never by its path.
func TestTheInstallCommandsPrintNoneOfWhatTheyWereGiven(t *testing.T) {
	const url = "postgres://invented_owner:invented-password@db.invented.example/invented_database"
	folder := filepath.Join(t.TempDir(), "invented-missing-folder")
	t.Setenv("GTM_CONTAINER_LISTEN", "")
	t.Setenv("GTM_PUBLIC_ORIGIN", "")
	t.Setenv("GTM_ALLOWED_HOSTS", "")
	t.Setenv("GTM_DATABASE_URL", url)
	t.Setenv("GTM_OWNER_DATABASE_URL", url)
	t.Setenv("GTM_CREDENTIALS_DIR", folder)
	t.Setenv("GTM_LISTEN", url)
	for _, argv := range [][]string{{"setup"}, {"serve"}, {"health"}, {"backup", "--out", filepath.Join(folder, "backup.tar")},
		{"restore", "--in", filepath.Join(folder, "backup.tar")}} {
		if code, stdout, stderr := line(t, argv...); code != 1 || stdout != "" || stderr != "gtm-connector: GTM_LISTEN is not host:port\n" {
			t.Errorf("%v with a URL as GTM_LISTEN: exit %d\n%s%s", argv, code, stdout, stderr)
		}
	}
	t.Setenv("GTM_LISTEN", "127.0.0.1:0")
	if code, _, stderr := line(t, "backup", "--out", filepath.Join(folder, "backup.tar")); code != 1 ||
		stderr != "gtm-connector: the credentials folder does not exist\n" {
		t.Errorf("backup with no credentials folder: exit %d\n%s", code, stderr)
	}
}

// serve with a database it cannot reach says so in a fixed line when its wait is cut short, and the URL's
// role, password, host and database stay out of it.
func TestServeWithAnUnreachableDatabasePrintsAFixedLine(t *testing.T) {
	t.Setenv("GTM_CONTAINER_LISTEN", "")
	t.Setenv("GTM_LISTEN", "127.0.0.1:0")
	t.Setenv("GTM_CREDENTIALS_DIR", t.TempDir())
	t.Setenv("GTM_DATABASE_URL", "postgres://invented_owner:invented-password@127.0.0.1:1/invented_database?connect_timeout=1")
	waited, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	var out, errs bytes.Buffer
	if code := dispatch(waited, []string{"serve"}, screen{out: &out, err: &errs}); code != 1 ||
		errs.String() != "gtm-connector: the database could not be reached\n" {
		t.Errorf("serve: exit %d\n%s%s", code, out.String(), errs.String())
	}
}

// An error made elsewhere prints its kind, never its text, whatever wraps it; one this program made prints
// as it is.
func TestAnInstallCommandsErrorIsItsOwnOrAFixedLine(t *testing.T) {
	_, missing := os.Open(filepath.Join(t.TempDir(), "invented-backup.tar"))
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("restore: %w", missing), "a file could not be read or written (open: no such file or directory)"},
		{fmt.Errorf("backup: %w", &pgconn.PgError{Code: "42501", Message: "permission denied for invented_table"}),
			"PostgreSQL error 42501"},
		{errors.New("usage: gtm-connector backup --out FILE"), "usage: gtm-connector backup --out FILE"},
		{fmt.Errorf("%w (run setup first)", absent{}), "does not exist (run setup first)"},
		{errors.Join(errors.New("the stored archive does not match"), missing), "a file could not be read or written " +
			"(open: no such file or directory)"},
	} {
		if got := plain(ownWords(c.err)); got != c.want {
			t.Errorf("%v: %s", c.err, got)
		}
	}
}
