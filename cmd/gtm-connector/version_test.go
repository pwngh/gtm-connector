package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// version prints the commit the binary was built from: the one given at
// link time, as the image build gives it, or else the one Go stamped from
// the checkout it was built in.
func TestVersionPrintsTheCommit(t *testing.T) {
	build := func(name string, flags ...string) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), name)
		cmd := exec.Command("go", append(append([]string{"build"}, flags...), "-o", out, ".")...)
		cmd.Env = append(cmd.Environ(), "GOTOOLCHAIN=local")
		if log, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, log)
		}
		printed, err := exec.Command(out, "version").Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(printed)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	if got := build("linked", "-ldflags", "-X main.revision="+commit); !strings.HasPrefix(got, "gtm-connector "+commit+" go") {
		t.Fatalf("version printed %q", got)
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("not a Git checkout")
	}
	if got := build("stamped"); !strings.HasPrefix(got, "gtm-connector "+strings.TrimSpace(string(head))) {
		t.Fatalf("version printed %q, the checkout is at %s", got, head)
	}
}
