package salesforce

import (
	"context"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/sourcehttp"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The limits of a login read. sf can take tens of seconds to start on a busy machine. Both commands of one
// login read share loginTime, which is apart from the budget of any read of the org.
const (
	commandTime    = 120 * time.Second
	loginTime      = 180 * time.Second
	maxOutputBytes = 1024 * 1024
)

// Runner runs sf with these arguments to its end, and gives what it wrote to standard output and its exit
// status. When ctx ends first it ends sf, with anything sf started, and returns an error.
type Runner interface {
	Run(ctx context.Context, arguments []string) (output []byte, status int, err error)
}

// errTooMuchOutput is a command that wrote more than a login answer holds.
var errTooMuchOutput = errors.New("salesforce: sf wrote too much")

// Exec runs the sf program: with no input, its error output discarded, telemetry, updates and log files
// off, and in a session of its own so that it can be ended with everything it started.
type Exec struct {
	// Path is the program. Empty is the sf on PATH.
	Path string
}

func (e Exec) Run(ctx context.Context, arguments []string) ([]byte, int, error) {
	if ctx.Err() != nil {
		return nil, 0, context.Cause(ctx)
	}
	if !sourcehttp.Live.Load() {
		// Only the program's main lets a process run sf, so no test reaches the sf of the machine it runs on.
		return nil, 0, sourcehttp.ErrNotLive
	}
	path := e.Path
	if path == "" {
		path = "sf"
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, 0, err
	}
	defer read.Close()
	command := exec.Command(path, arguments...)
	command.Env = append(os.Environ(), "SF_DISABLE_TELEMETRY=true", "SF_AUTOUPDATE_DISABLE=true",
		"SF_DISABLE_AUTOUPDATE=true", "SF_DISABLE_LOG_FILE=true", "SFDX_DISABLE_LOG_FILE=true")
	command.Stdout = write
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = command.Start()
	write.Close()
	if err != nil {
		return nil, 0, err
	}
	pid := command.Process.Pid
	// Until sf is reaped its ID names its process group, so the group can be ended without reaching
	// any other process.
	stop := func() {
		if syscall.Kill(-pid, syscall.SIGKILL) != nil {
			_ = command.Process.Kill()
		}
		read.Close()
		_ = command.Wait()
	}
	type answer struct {
		output []byte
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		output, err := io.ReadAll(io.LimitReader(read, maxOutputBytes+1))
		answered <- answer{output, err}
	}()
	var got answer
	select {
	case got = <-answered:
	case <-ctx.Done():
		// sf may have exited and left a descendant holding its output open, so the group goes either way.
		stop()
		return nil, 0, context.Cause(ctx)
	}
	if got.err != nil || len(got.output) > maxOutputBytes {
		stop()
		return nil, 0, errors.Join(errTooMuchOutput, got.err)
	}
	// The output is closed. sf is polled, not waited on, so that it is not reaped while it may still have
	// to be ended.
	for pause := time.Millisecond; ; pause = min(2*pause, 50*time.Millisecond) {
		var state syscall.WaitStatus
		reaped, err := syscall.Wait4(pid, &state, syscall.WNOHANG, nil)
		switch {
		case err == syscall.EINTR:
			continue
		case err != nil:
			return nil, 0, err
		case reaped == pid:
			_ = command.Process.Release()
			if ctx.Err() != nil {
				// The cancel landed as sf exited. A descendant that is still running keeps the group's ID
				// in use, so the signal reaches only that group.
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				return nil, 0, context.Cause(ctx)
			}
			if !state.Exited() {
				return got.output, -1, nil
			}
			return got.output, state.ExitStatus(), nil
		}
		select {
		case <-ctx.Done():
			stop()
			return nil, 0, context.Cause(ctx)
		case <-time.After(pause):
		}
	}
}

// credentials reads the instance address and the token of one sf alias. Nothing sf writes, and neither
// value, reaches an error.
type credentials struct {
	alias  string
	runner Runner
	// The limits, which a test shortens.
	commandTime, loginTime time.Duration
}

// aliasPattern is what an sf alias may be here: 1 to 64 letters, digits, dots, hyphens and underscores,
// opening with a letter or a digit. A username holds "@", and an access token holds "!" or runs longer, so
// neither passes for an alias.
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidAlias reports whether a text may be an sf alias. Anything else never reaches sf.
func ValidAlias(alias string) bool { return aliasPattern.MatchString(alias) }

// newCredentials refuses a text that cannot be an alias before sf is run.
func newCredentials(alias string, runner Runner) (*credentials, error) {
	if !ValidAlias(alias) {
		return nil, refuse(authRequired)
	}
	return &credentials{alias: alias, runner: runner, commandTime: commandTime, loginTime: loginTime}, nil
}

// loginSource gives a login: sf for an alias, or the variables of the process.
type loginSource interface {
	read(ctx context.Context) (login, error)
}

// login is what one login read gives, held in memory: the origin, the org's ID, the user sf signed in as,
// and the token.
type login struct {
	instanceURL, organizationID, username, accessToken string
}

// read runs sf org display and sf org auth show-access-token, and gives the login. sf org display must name
// the alias it was asked for as the login's alias, so a username or anything else sf would resolve is not
// taken. The two commands run one after the other, so the alias may change between them: the org ID and
// the username are what sf org display named, and the reader checks the token against both before keeping
// it.
func (c *credentials) read(ctx context.Context) (login, error) {
	limited, cancel := context.WithTimeout(ctx, c.loginTime)
	defer cancel()
	shown, err := c.command(ctx, limited, []string{"display"}, "instanceUrl", "id", "username", "alias")
	if err != nil {
		return login{}, err
	}
	organizationID, err := nativeID(shown[1])
	if err != nil || !strings.HasPrefix(organizationID, "00D") || shown[3] != c.alias {
		return login{}, refuse(authRequired)
	}
	token, err := c.command(ctx, limited, []string{"auth", "show-access-token"}, "accessToken")
	if err != nil {
		return login{}, err
	}
	return login{instanceURL: shown[0], organizationID: organizationID, username: shown[2], accessToken: token[0]}, nil
}

// command runs one sf org command for the alias and gives the named text fields of its JSON result.
func (c *credentials) command(caller, login context.Context, words []string, names ...string) ([]string, error) {
	limited, cancel := context.WithTimeout(login, c.commandTime)
	defer cancel()
	arguments := append(append([]string{"org"}, words...), "--target-org", c.alias, "--json")
	output, status, err := c.runner.Run(limited, arguments)
	switch {
	case caller.Err() != nil:
		return nil, stopped(caller)
	case limited.Err() != nil:
		// sf did not answer in time. That says nothing about the saved login.
		return nil, refuse(cliTimeout)
	case err != nil || status != 0 || len(output) > maxOutputBytes:
		return nil, refuse(authRequired)
	}
	parsed, err := strictjson.Loads(output)
	document, _ := parsed.(*strictjson.Object)
	code, _ := document.Get("status")
	result, _ := document.Get("result")
	number, isInt := code.(*big.Int)
	fields, _ := result.(*strictjson.Object)
	if err != nil || !isInt || number.Sign() != 0 || fields == nil {
		return nil, refuse(authRequired)
	}
	found := make([]string, len(names))
	for i, name := range names {
		value, _ := fields.Get(name)
		if found[i], _ = value.(string); found[i] == "" {
			return nil, refuse(authRequired)
		}
	}
	return found, nil
}
