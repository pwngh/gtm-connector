package salesforce

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// A token handed to the process, for a connector that runs where there is no sf, such as a container.
// The instance address is SALESFORCE_INSTANCE_URL, as gtm_connector/salesforce.py's from_env reads it. The
// token is SALESFORCE_ACCESS_TOKEN, or the contents of the file SALESFORCE_ACCESS_TOKEN_FILE names, never
// both. A file is how a token reaches a process that is already running: on a first HTTP 401 in an
// operation the variables, and the file, are read again once.
const (
	InstanceVariable  = "SALESFORCE_INSTANCE_URL"
	TokenVariable     = "SALESFORCE_ACCESS_TOKEN"
	TokenFileVariable = "SALESFORCE_ACCESS_TOKEN_FILE"
)

// maxTokenFile is the most a token file may hold: the longest token, and a line ending.
const maxTokenFile = 4096 + 2

// environment reads the login handed to the process. It names no org or user ahead: the reader learns
// both from the org on its first check, and every later token must open the same ones.
type environment struct{}

// read gives the instance address and the token. Missing or ambiguous variables, and a token file that
// cannot be read, are salesforce_invalid_configuration. Neither value reaches an error.
func (environment) read(context.Context) (login, error) {
	token, path := os.Getenv(TokenVariable), os.Getenv(TokenFileVariable)
	switch {
	case token != "" && path != "":
		return login{}, refuse(invalidConfiguration)
	case path != "":
		var err error
		if token, err = tokenFile(path); err != nil {
			return login{}, refuse(invalidConfiguration)
		}
	}
	return login{instanceURL: os.Getenv(InstanceVariable), accessToken: token}, nil
}

// tokenFile is the token a file holds: its contents, less one line ending. It must be a regular file.
// A secret mounted by an orchestrator may be reached through a symbolic link, which is followed.
func tokenFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", os.ErrInvalid
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxTokenFile+1))
	if err != nil || len(contents) > maxTokenFile {
		return "", os.ErrInvalid
	}
	text := strings.TrimSuffix(strings.TrimSuffix(string(contents), "\n"), "\r")
	return text, nil
}

// FromEnvironment makes the Reader of the org a token handed to the process opens: SALESFORCE_INSTANCE_URL
// and SALESFORCE_ACCESS_TOKEN or SALESFORCE_ACCESS_TOKEN_FILE. It applies the address and token rules of
// FromSF, and reads nothing from the org. Its first operation checks the org against the environment and
// learns the org and the user the token reads as.
func FromEnvironment(ctx context.Context, o Options) (*Reader, error) {
	environmentName := o.Environment
	if environmentName == "" {
		environmentName = "developer"
	}
	if !validEnvironment(environmentName) {
		return nil, refuse(invalidConfiguration)
	}
	given, err := environment{}.read(ctx)
	if err != nil {
		return nil, err
	}
	origin, err := configure(given.instanceURL, given.accessToken)
	if err != nil {
		return nil, err
	}
	return newReader(environmentName, origin, given, environment{}, o), nil
}

// newReader is the Reader of one login, which source gives again on a first HTTP 401.
func newReader(environmentName, origin string, given login, source loginSource, o Options) *Reader {
	transport := o.Transport
	if transport == nil {
		transport = DefaultTransport()
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	r := &Reader{environment: environmentName, instanceURL: origin, accessToken: given.accessToken,
		credentials: source, signedInTo: given.organizationID, username: given.username,
		now: now, turn: make(chan struct{}, 1), checked: map[string]bool{},
		client: &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	r.tokens.Add(given.accessToken)
	return r
}
