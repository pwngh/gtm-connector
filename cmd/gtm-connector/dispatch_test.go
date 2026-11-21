package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
		{[]string{"mcp", "stdio", "--scope", "crm:read"}, 3, "",
			"Error: mcp is not available in this build.\nNext: gtm-connector --help\n"},
		{[]string{"debug", "doctor", "--json"}, 3, "", `{"error": {"code": "refused", "message": "debug is not ` +
			`available in this build.", "next": "gtm-connector --help"}}` + "\n"},
		{[]string{"setup", "--json"}, 2, "", `{"error": {"code": "usage_error", "message": "setup writes text and ` +
			`takes no --json.", "next": "gtm-connector --help"}}` + "\n"},
		{[]string{"--help"}, 0, usage, ""},
		{[]string{"help"}, 0, usage, ""},
		{[]string{"help", "mcp"}, 0, usage, ""},
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
