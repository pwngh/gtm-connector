package salesforce

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// answering is an sf that gives one answer to every command.
func answering(output string, status int, err error) *salesforcetest.SF {
	sf := salesforcetest.NewSF()
	sf.Answer = func(context.Context, []string) ([]byte, int, error) { return []byte(output), status, err }
	return sf
}

// Anything but a JSON answer of status 0 with the field as text is a login that cannot be used. The error is
// the code alone, whatever sf wrote.
func TestAnAnswerSFDidNotGiveIsNoLogin(t *testing.T) {
	private := "https://invented-private.my.salesforce.com"
	for name, sf := range map[string]*salesforcetest.SF{
		"sf failed":            answering(`{"status": 0, "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`, 1, nil),
		"sf could not start":   answering("", 0, errors.New("exec: sf: not found in "+private)),
		"a status that is not": answering(`{"status": 1, "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`, 0, nil),
		"a status as text":     answering(`{"status": "0", "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`, 0, nil),
		"a status of false":    answering(`{"status": false, "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`, 0, nil),
		"no result":            answering(`{"status": 0, "instanceUrl": "`+private+`"}`, 0, nil),
		"a result as a list":   answering(`{"status": 0, "result": ["`+private+`"]}`, 0, nil),
		"an empty field":       answering(`{"status": 0, "result": {"id": "00D54481A5CC257EID", "instanceUrl": ""}}`, 0, nil),
		"a field as a number":  answering(`{"status": 0, "result": {"id": "00D54481A5CC257EID", "instanceUrl": 7}}`, 0, nil),
		"a key given twice":    answering(`{"status": 0, "status": 0, "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`, 0, nil),
		"text before the JSON": answering(`Warning: `+private+` {"status": 0, "result": {}}`, 0, nil),
		"more than a mebibyte": answering(`{"status": 0, "result": {"id": "00D54481A5CC257EID", "instanceUrl": "`+private+`"}}`+
			strings.Repeat(" ", maxOutputBytes), 0, nil),
		"nothing": answering("", 0, nil),
	} {
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf})
		if reader != nil || err == nil || err.Error() != authRequired {
			t.Errorf("%s: %v", name, err)
		}
		if len(sf.Commands) != 1 {
			t.Errorf("%s: sf ran %d times", name, len(sf.Commands))
		}
	}
}

// An alias sf could read as an option, or one holding a space or a control character, never reaches sf.
func TestAnAliasThatIsNotOneNeverReachesSF(t *testing.T) {
	for _, alias := range []string{"", "-o", "--target-org=other", "two words", "tab\there", "line\nbreak", "del\x7f",
		strings.Repeat("a", 65), "someone@example.com", "00D54481A5CC257EID!AQEAQInventedAccessTokenText",
		"invented!token", "café"} {
		sf := salesforcetest.NewSF()
		if _, err := FromSF(context.Background(), alias, Options{Runner: sf}); codeOf(err) != authRequired {
			t.Errorf("%q: %v", alias, err)
		}
		if len(sf.Commands) != 0 {
			t.Errorf("%q: sf ran %v", alias, sf.Commands)
		}
	}
}

// A login is taken only when sf org display names it under the alias it was asked for: sf also resolves a
// username or a token given as --target-org, and names no alias, or another, for it.
func TestALoginIsTakenOnlyUnderItsOwnAlias(t *testing.T) {
	for name, alias := range map[string]any{"no alias": nil, "another alias": "other-alias", "an empty alias": ""} {
		sf := salesforcetest.NewSF()
		sf.Answer = func(_ context.Context, arguments []string) ([]byte, int, error) {
			result := strictjson.NewObject("id", salesforcetest.OrganizationID, "instanceUrl", salesforcetest.Instance,
				"username", salesforcetest.Username)
			if alias != nil {
				result.Set("alias", alias)
			}
			text, _ := strictjson.Dumps(strictjson.NewObject("status", 0, "result", result), strictjson.Quoted)
			return []byte(text), 0, nil
		}
		if _, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: sf}); codeOf(err) != authRequired ||
			len(sf.Commands) != 1 {
			t.Errorf("%s: %v after %d commands", name, err, len(sf.Commands))
		}
	}
}

// A Reader with no runner and no transport runs no program and dials nothing in a process whose main did
// not allow it: this test's own sf, first on PATH, never starts, and the reader's own transport refuses to
// dial before it looks the host up.
func TestAReaderWithNoOptionsReachesNothing(t *testing.T) {
	path, folder := program(t, "touch \"$folder/started\"\n")
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := FromSF(context.Background(), salesforcetest.Alias, Options{}); codeOf(err) != authRequired {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(folder, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sf ran")
	}
	request, _ := http.NewRequest(http.MethodGet, salesforcetest.Instance+"/services/data/v68.0/query", nil)
	if _, err := DefaultTransport().RoundTrip(request); !errors.Is(err, sourcehttp.ErrNotLive) {
		t.Fatalf("the default transport: %v", err)
	}
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Runner: salesforcetest.NewSF()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != unavailable {
		t.Fatalf("a read over the default transport: %v", err)
	}
}

// An sf that outlasts its limit is stopped and reported as a timeout, which says nothing about the login. A
// caller that leaves stops it too, and gets its own context's error.
func TestSFIsStoppedAtItsLimitAndOnCancel(t *testing.T) {
	never := salesforcetest.NewSF()
	never.Answer = func(ctx context.Context, _ []string) ([]byte, int, error) {
		<-ctx.Done()
		return nil, 0, context.Cause(ctx)
	}
	for name, limits := range map[string][2]time.Duration{
		"one command": {20 * time.Millisecond, time.Minute},
		"the login":   {time.Minute, 20 * time.Millisecond},
	} {
		login, err := newCredentials(salesforcetest.Alias, never)
		if err != nil {
			t.Fatal(err)
		}
		login.commandTime, login.loginTime = limits[0], limits[1]
		if _, err := login.read(context.Background()); codeOf(err) != cliTimeout {
			t.Errorf("%s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	reader, err := FromSF(ctx, salesforcetest.Alias, Options{Runner: never})
	if reader != nil || !errors.Is(err, context.Canceled) || codeOf(err) != "" {
		t.Fatal(err)
	}
}

// program writes an invented sf program and gives its path and its folder.
func program(t *testing.T, script string) (path, folder string) {
	t.Helper()
	folder = t.TempDir()
	path = filepath.Join(folder, "sf")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nfolder=$(dirname \"$0\")\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, folder
}

// live lets this process run a program as sf until the test ends. Each test that does gives the program
// itself.
func live(t *testing.T) {
	t.Helper()
	sourcehttp.Live.Store(true)
	t.Cleanup(func() { sourcehttp.Live.Store(false) })
}

func TestExecRunsTheProgramWithItsNoiseOff(t *testing.T) {
	live(t)
	path, _ := program(t, `echo "$* telemetry=$SF_DISABLE_TELEMETRY update=$SF_AUTOUPDATE_DISABLE,$SF_DISABLE_AUTOUPDATE `+
		`log=$SF_DISABLE_LOG_FILE,$SFDX_DISABLE_LOG_FILE"
echo "to the error stream" >&2
read line && echo "input: $line"
exit 3
`)
	output, status, err := Exec{Path: path}.Run(context.Background(), []string{"org", "display", "--json"})
	want := "org display --json telemetry=true update=true,true log=true,true\n"
	if err != nil || status != 3 || string(output) != want {
		t.Fatalf("%q, %d, %v", output, status, err)
	}
	if _, _, err := (Exec{Path: filepath.Join(t.TempDir(), "absent")}).Run(context.Background(), nil); err == nil {
		t.Fatal("a program that is not there ran")
	}
}

// A cancel ends sf and what sf started, even when sf has exited and a child still holds its output open.
func TestExecEndsEverythingSFStarted(t *testing.T) {
	live(t)
	for name, script := range map[string]string{
		"sf waits on its child":                   "sleep 600 &\necho \"$$ $!\" > \"$folder/pids.part\" && mv \"$folder/pids.part\" \"$folder/pids\"\nwait\n",
		"sf exits and its child holds the output": "sleep 600 &\necho \"$$ $!\" > \"$folder/pids.part\" && mv \"$folder/pids.part\" \"$folder/pids\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			path, folder := program(t, script)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var pids []int
			go func() {
				defer cancel()
				for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					written, err := os.ReadFile(filepath.Join(folder, "pids"))
					if err != nil {
						continue
					}
					for _, field := range strings.Fields(string(written)) {
						pid, _ := strconv.Atoi(field)
						pids = append(pids, pid)
					}
					return
				}
			}()
			output, _, err := Exec{Path: path}.Run(ctx, []string{"org", "display"})
			if output != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("%q, %v", output, err)
			}
			if len(pids) != 2 || pids[0] <= 1 || pids[1] <= 1 {
				t.Fatalf("sf did not start: %v", pids)
			}
			running := func(pid int) bool { return syscall.Kill(pid, 0) != syscall.ESRCH }
			// sf is reaped before Run returns. Its child is gone once the system has reaped it.
			for deadline := time.Now().Add(10 * time.Second); running(pids[1]) && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			if running(pids[0]) || running(pids[1]) {
				t.Fatalf("sf runs: %t; its child runs: %t", running(pids[0]), running(pids[1]))
			}
		})
	}
}
