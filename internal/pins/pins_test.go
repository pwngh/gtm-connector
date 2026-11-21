package pins

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type manifest struct {
	Go struct {
		Version  string            `json:"version"`
		Archives map[string]string `json:"archives"`
		Modules  string            `json:"modules"`
	} `json:"go"`
	Node struct {
		Version  string            `json:"version"`
		Archives map[string]string `json:"archives"`
		Packages string            `json:"packages"`
	} `json:"node"`
	Python struct {
		Oracle   string   `json:"oracle"`
		CI       []string `json:"ci"`
		Packages []string `json:"packages"`
	} `json:"python"`
	Images struct {
		Postgres         string `json:"postgres"`
		PostgresFallback string `json:"postgres_fallback"`
		GoBuild          string `json:"go_build"`
		WebBuild         string `json:"web_build"`
		Runtime          string `json:"runtime"`
	} `json:"images"`
	Actions      map[string]string `json:"actions"`
	MCPInspector string            `json:"mcp_inspector"`
	TestedWith   map[string]string `json:"tested_with"`
}

// root is the repository root: the folder holding go.mod.
func root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent != dir {
			dir = parent
		} else {
			t.Fatal("no go.mod above the test")
		}
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// all gives the first group of every match of pattern in text.
func all(text, pattern string) []string {
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

func load(t *testing.T) manifest {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader([]byte(read(t, "pins.json"))))
	decoder.DisallowUnknownFields()
	var m manifest
	if err := decoder.Decode(&m); err != nil {
		t.Fatalf("pins.json: %v", err)
	}
	return m
}

var (
	digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	image  = regexp.MustCompile(`^[a-z0-9.-]+:[0-9a-z.-]+@sha256:[0-9a-f]{64}$`)
	commit = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Every pin names an exact version and, where one exists, a digest.
func TestThePinsAreExact(t *testing.T) {
	m := load(t)
	for name, files := range map[string]map[string]string{"go": m.Go.Archives, "node": m.Node.Archives} {
		if len(files) == 0 {
			t.Errorf("%s has no archive digests", name)
		}
		for file, sum := range files {
			version := map[string]string{"go": "go" + m.Go.Version + ".", "node": "node-v" + m.Node.Version + "-"}[name]
			if !digest.MatchString(sum) || !strings.HasPrefix(file, version) {
				t.Errorf("%s archive %s: %s", name, file, sum)
			}
		}
	}
	for _, ref := range []string{m.Images.Postgres, m.Images.PostgresFallback, m.Images.GoBuild, m.Images.WebBuild,
		m.Images.Runtime} {
		if !image.MatchString(ref) {
			t.Errorf("image %q is not pinned by digest", ref)
		}
	}
	if !strings.HasPrefix(m.Images.GoBuild, "golang:"+m.Go.Version+"-") ||
		!strings.HasPrefix(m.Images.WebBuild, "node:"+m.Node.Version+"-") {
		t.Errorf("the build images are not of the pinned Go %s and Node %s", m.Go.Version, m.Node.Version)
	}
	for action, sha := range m.Actions {
		if !commit.MatchString(sha) {
			t.Errorf("%s is not pinned to a commit: %s", action, sha)
		}
	}
	for _, name := range append([]string{m.Go.Modules, m.Node.Packages}, m.Python.Packages...) {
		if _, err := os.Stat(filepath.Join(root(t), filepath.FromSlash(name))); err != nil {
			t.Errorf("the lock file %s: %v", name, err)
		}
	}
	for _, tool := range []string{"colima", "docker_cli", "docker_engine", "docker_compose"} {
		if m.TestedWith[tool] == "" {
			t.Errorf("no tested-with version for %s", tool)
		}
	}
}

// The Dockerfile, compose.yaml, the Makefile, .nvmrc, go.mod, the oracle's
// manifest, the Inspector checks and both workflows all use the pins.
func TestTheBuildFilesUseThePins(t *testing.T) {
	m := load(t)
	check := func(file, what string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: %s is %q, pins.json says %q", file, what, got, want)
		}
	}
	check("Dockerfile", "FROM", all(read(t, "Dockerfile"), `(?m)^FROM (\S+)`),
		m.Images.WebBuild, m.Images.GoBuild, m.Images.Runtime)

	check("compose.yaml", "the PostgreSQL image", all(read(t, "compose.yaml"), `\$\{GTM_POSTGRES_IMAGE:-([^}]+)\}`),
		m.Images.Postgres)

	makefile := read(t, "Makefile")
	check("Makefile", "the fallback PostgreSQL image", all(makefile, `(?m)^export GTM_POSTGRES_IMAGE := (\S+)$`),
		m.Images.PostgresFallback)
	check("Makefile", "GO_VERSION", all(makefile, `(?m)^GO_VERSION := (\S+)$`), m.Go.Version)
	check("Makefile", "NODE_VERSION", all(makefile, `(?m)^NODE_VERSION := (.+)$`), "$(shell cat .nvmrc 2>/dev/null)")
	check(".nvmrc", "the Node version", []string{read(t, ".nvmrc")}, m.Node.Version+"\n")
	major, minor, _ := strings.Cut(m.Go.Version, ".")
	minor, _, _ = strings.Cut(minor, ".")
	check("go.mod", "the go line", all(read(t, "go.mod"), `(?m)^go (\S+)$`), major+"."+minor)

	var oracle struct {
		PythonVersion string `json:"python_version"`
	}
	if err := json.Unmarshal([]byte(read(t, "testdata/oracle/manifest.json")), &oracle); err != nil {
		t.Fatal(err)
	}
	check("testdata/oracle/manifest.json", "python_version", []string{oracle.PythonVersion}, m.Python.Oracle)
	for _, file := range []string{"internal/transport/mcp/inspector_test.go", "cmd/gtm-connector/inspector_test.go"} {
		check(file, "inspector", all(read(t, file), `const inspector = "([^"]+)"`), m.MCPInspector)
	}

	workflow := read(t, ".github/workflows/test.yml")
	const ci = ".github/workflows/test.yml"
	check(ci, "the Go job's PostgreSQL images", all(workflow, `(?m)^\s+- (postgres:\S+)$`),
		m.Images.Postgres, m.Images.PostgresFallback)
	check(ci, "the Inspector job's PostgreSQL image", all(workflow, `(?m)^\s+image: (postgres:\S+)$`), m.Images.Postgres)
	check(ci, "the smoke job's PostgreSQL versions", all(workflow, `(?m)^\s+postgres: (\[.+\])$`),
		"['"+strings.Join([]string{versionOf(m.Images.Postgres), versionOf(m.Images.PostgresFallback)}, "', '")+"']")
	pythons := all(workflow, `(?m)^\s+python-version: (\[.+\]|'[^']+')$`)
	want := "['" + strings.Join(m.Python.CI, "', '") + "']"
	for _, p := range pythons {
		if p != want && !slices.Contains(m.Python.CI, strings.Trim(p, "'")) {
			t.Errorf("%s: python-version %s is not one of %v", ci, p, m.Python.CI)
		}
	}
	if !slices.Contains(m.Python.CI, m.Python.Oracle) {
		t.Errorf("CI does not run the oracle's Python %s", m.Python.Oracle)
	}
	// The test workflow and the release workflow set up Go and Node from the
	// pins, and every action either uses is pinned to its commit.
	used := map[string]bool{}
	for _, file := range []string{ci, ".github/workflows/release.yml"} {
		workflow := read(t, file)
		versions := all(workflow, `(?m)^\s+go-version: '([^']+)'$`)
		if len(versions) == 0 {
			t.Errorf("%s: no setup-go names its version", file)
		}
		for _, version := range versions {
			check(file, "go-version", []string{version}, m.Go.Version)
		}
		if strings.Contains(workflow, "go-version-file") {
			t.Errorf("%s: setup-go reads a version range from a file", file)
		}
		nodeFiles := all(workflow, `(?m)^\s+node-version-file: (\S+)$`)
		if len(nodeFiles) == 0 || strings.Contains(workflow, "node-version:") {
			t.Errorf("%s: setup-node does not read .nvmrc", file)
		}
		for _, nodeFile := range nodeFiles {
			check(file, "node-version-file", []string{nodeFile}, ".nvmrc")
		}
		for _, use := range all(workflow, `(?m)^\s+- uses: (\S+)`) {
			action, sha, _ := strings.Cut(use, "@")
			used[action] = true
			if m.Actions[action] != sha {
				t.Errorf("%s: %s, pins.json says %s", file, use, m.Actions[action])
			}
		}
	}
	if names := slices.Sorted(maps.Keys(m.Actions)); !slices.Equal(names, slices.Sorted(maps.Keys(used))) {
		t.Errorf("pins.json lists actions %v; the workflows use %v", names, slices.Sorted(maps.Keys(used)))
	}
}

// versionOf is the version in an image reference: postgres:19.0-alpine3.24@... gives 19.0.
func versionOf(ref string) string {
	_, tag, _ := strings.Cut(ref, ":")
	version, _, _ := strings.Cut(tag, "-")
	return version
}
