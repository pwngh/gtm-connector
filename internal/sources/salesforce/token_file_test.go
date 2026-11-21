package salesforce

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// readWithin is the token file read through the variables, failed if it takes five seconds: a refused file
// must be refused at once, never waited on. A read that waits on a named pipe waits forever, so the bound
// only has to outlast a plain read on a busy runner.
func readWithin(t *testing.T, path string) (login, error) {
	t.Helper()
	t.Setenv(InstanceVariable, salesforcetest.Instance)
	t.Setenv(TokenVariable, "")
	t.Setenv(TokenFileVariable, path)
	type answer struct {
		given login
		err   error
	}
	answered := make(chan answer, 1)
	go func() {
		given, err := environment{}.read(context.Background())
		answered <- answer{given, err}
	}()
	select {
	case got := <-answered:
		return got.given, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("the token file was waited on")
		return login{}, nil
	}
}

// The token file is a regular file its owner alone can read, or a link to one in a folder others cannot
// write to. A named pipe, directly or through a link, is refused at once. A refusal of the mode or the
// place says so with the mode, and holds no path and no token.
func TestTheTokenFileMustBePrivateAndPlain(t *testing.T) {
	folder := t.TempDir()
	if err := os.Chmod(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, mode os.FileMode) string {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte(salesforcetest.Token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	link := func(name, target string) string {
		path := filepath.Join(folder, name)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	private := write("private", 0o600)
	pipe := filepath.Join(folder, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	inOpen := filepath.Join(open, "token")
	if err := os.WriteFile(inOpen, []byte(salesforcetest.Token), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"a private file":            private,
		"a file its owner may read": write("owner-read", 0o400),
		"a link to a private file":  link("to-private", private),
		"a link to a link":          link("to-link", link("first-link", private)),
	} {
		if given, err := readWithin(t, path); err != nil || given.accessToken != salesforcetest.Token {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		path, detail string
	}{
		"a file the group may read": {write("group", 0o640), "The token file can be read by others than its owner " +
			"(mode 0640). Make it readable by its owner alone: chmod 600."},
		"a file anyone may read": {write("anyone", 0o644), "The token file can be read by others than its owner " +
			"(mode 0644). Make it readable by its owner alone: chmod 600."},
		"a named pipe":               {pipe, ""},
		"a link to a named pipe":     {link("to-pipe", pipe), ""},
		"a link into an open folder": {link("to-open", inOpen), "The token file is a link into a folder that others can write to. Link to a file in a folder only its owner can change."},
		"a link to nothing":          {link("dangling", filepath.Join(folder, "missing")), ""},
		"a folder":                   {folder, ""},
		"a link to a readable file":  {link("to-anyone", filepath.Join(folder, "anyone")), "The token file can be read by others than its owner (mode 0644). Make it readable by its owner alone: chmod 600."},
	} {
		_, err := readWithin(t, c.path)
		var failed *Error
		if !errors.As(err, &failed) || failed.Code != invalidConfiguration || failed.Detail != c.detail {
			t.Errorf("%s: %#v", name, err)
			continue
		}
		if strings.Contains(failed.Detail, folder) || strings.Contains(failed.Detail, salesforcetest.Token) {
			t.Errorf("%s: the refusal holds the path or the token", name)
		}
	}
}

// A token that the org answers once and refuses at the question of its user, because it expired in
// between, is replaced once, and both first checks run again under the replacement. This holds for a token
// handed to the process and for an sf login.
func TestATokenThatExpiresDuringTheFirstCheckIsReplaced(t *testing.T) {
	const rotated = "invented-token-rotated-between-checks"
	expireAtUser := func(org *salesforcetest.Org, replace func()) {
		done := false
		org.Intercept = func(request *http.Request) *http.Response {
			if !done && request.URL.Path == salesforcetest.UserInfoPath {
				done = true
				org.Token = rotated
				replace()
			}
			return nil
		}
	}
	want := []string{queryPrefix + organizationQuery, salesforcetest.UserInfoPath, queryPrefix + organizationQuery,
		salesforcetest.UserInfoPath}
	check := func(t *testing.T, org *salesforcetest.Org, reader *Reader) {
		t.Helper()
		identity, err := reader.CheckEnvironment(context.Background())
		if err != nil || identity.UserID != salesforcetest.UserID || reader.accessToken != rotated {
			t.Fatalf("%+v %v", identity, err)
		}
		if strings.Join(org.Requests, "\n") != strings.Join(want, "\n") {
			t.Fatalf("the requests:\n%s", strings.Join(org.Requests, "\n"))
		}
	}
	t.Run("a token handed to the process", func(t *testing.T) {
		path := handed(t, salesforcetest.Token)
		org := salesforcetest.NewOrg()
		expireAtUser(org, func() { _ = os.WriteFile(path, []byte(rotated+"\n"), 0o600) })
		reader, err := FromEnvironment(context.Background(), Options{Environment: "production", Transport: org})
		if err != nil {
			t.Fatal(err)
		}
		check(t, org, reader)
	})
	t.Run("an sf login", func(t *testing.T) {
		org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
		expireAtUser(org, func() { sf.Token = rotated })
		reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production",
			Runner: sf, Transport: org})
		if err != nil {
			t.Fatal(err)
		}
		check(t, org, reader)
		if len(sf.Commands) != 4 {
			t.Fatalf("sf ran %d commands", len(sf.Commands))
		}
	})
	t.Run("the replacement's user is the one the pin is compared with", func(t *testing.T) {
		path := handed(t, salesforcetest.Token)
		org := salesforcetest.NewOrg()
		org.Users[rotated] = [2]string{"0059F2C41D7EA03ZZZ", "other@invented-books.example"}
		expireAtUser(org, func() { _ = os.WriteFile(path, []byte(rotated+"\n"), 0o600) })
		reader, err := FromEnvironment(context.Background(), Options{Environment: "production", Transport: org})
		if err != nil {
			t.Fatal(err)
		}
		// The first token proved no user, so the replacement's user is the one learned; the workspace's pin,
		// compared by the caller, refuses it there. The reader keeps no token that has not passed both checks.
		identity, err := reader.CheckEnvironment(context.Background())
		if err != nil || identity.UserID != "0059F2C41D7EA03ZZZ" {
			t.Fatalf("%+v %v", identity, err)
		}
	})
}
