package realdata

import (
	"strings"
	"testing"
)

func TestManifestBuiltIn(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Datasets) == 0 {
		t.Fatal("no datasets")
	}
	for _, d := range m.Datasets {
		// Every public dataset that is not opt-in is pinned, so verify-real reads exactly what was pinned.
		if d.Access == Public && d.OptIn == "" && !d.Pinned() {
			t.Errorf("%s is not pinned; run realdata pin %s", d.ID, d.ID)
		}
		for _, f := range d.Files {
			if !strings.HasPrefix(f.URL, d.BaseURL) {
				t.Errorf("%s: %s is not under the base URL", d.ID, f.Path)
			}
		}
	}
}

const goodManifest = `{"schema": 2, "datasets": [{
 "id": "set-1", "name": "A set", "url": "https://data.example/set", "version": "1",
 "licence": "CC BY 4.0", "licence_source": "https://data.example/licence", "access": "public", "kind": "natural",
 "converter": "ami-words", "base_url": "https://data.example/files/"}]}`

const goodPins = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\t3\ta/b.zip\n"

func parse(manifest, pins string) (*Manifest, error) {
	return ParseManifest([]byte(manifest), func(string) ([]byte, error) {
		if pins == "" {
			return nil, nil
		}
		return []byte(pins), nil
	})
}

func TestManifestRefuses(t *testing.T) {
	m, err := parse(goodManifest, goodPins)
	if err != nil {
		t.Fatalf("the good manifest: %v", err)
	}
	if f := m.Datasets[0].Files[0]; f.URL != "https://data.example/files/a/b.zip" || f.Bytes != 3 || !m.Datasets[0].Pinned() {
		t.Errorf("%+v", f)
	}
	for name, change := range map[string][2]string{
		"an http base URL":           {`"https://data.example/files/"`, `"http://data.example/files/"`},
		"a base URL without /":       {`"https://data.example/files/"`, `"https://data.example/files"`},
		"an http licence source":     {`"https://data.example/licence"`, `"http://data.example/licence"`},
		"an id with a slash":         {`"id": "set-1"`, `"id": "set/1"`},
		"an id going up":             {`"id": "set-1"`, `"id": ".."`},
		"an unknown converter":       {`"ami-words"`, `"nothing"`},
		"no converter and no note":   {`"converter": "ami-words", `, ``},
		"an unknown kind":            {`"kind": "natural"`, `"kind": "real"`},
		"an unknown access":          {`"access": "public"`, `"access": "open"`},
		"an owner with no steps":     {`"access": "public"`, `"access": "owner"`},
		"an unknown field":           {`"schema": 2,`, `"schema": 2, "extra": true,`},
		"another schema":             {`"schema": 2`, `"schema": 1`},
		"no licence":                 {`"licence": "CC BY 4.0"`, `"licence": ""`},
		"credentials in a URL":       {`"https://data.example/files/"`, `"https://user:pw@data.example/files/"`},
		"steps on a public dataset":  {`"access": "public"`, `"access": "public", "owner_steps": "sign up"`},
		"a redirect URL, not a host": {`"base_url"`, `"redirect_hosts": ["https://cdn.example/"], "base_url"`},
		"a listing with no source":   {`"base_url"`, `"listing": {"revision": "` + strings.Repeat("a", 40) + `", "include": ["*"]}, "base_url"`},
		"a short revision":           {`"base_url"`, `"listing": {"git": "https://git.example/r.git", "revision": "abc", "include": ["*"]}, "base_url"`},
		"a listing with no include":  {`"base_url"`, `"listing": {"git": "https://git.example/r.git", "revision": "` + strings.Repeat("a", 40) + `", "include": []}, "base_url"`},
	} {
		text := strings.Replace(goodManifest, change[0], change[1], 1)
		if text == goodManifest {
			t.Fatalf("%s: the change did not apply", name)
		}
		if _, err := parse(text, goodPins); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parse(goodManifest, ""); err == nil {
		t.Error("a public dataset with no files and no listing: accepted")
	}
	twice := strings.Replace(goodManifest, `"datasets": [{`, `"datasets": [{"id": "set-1"}, {`, 1)
	if _, err := parse(twice, goodPins); err == nil {
		t.Error("a dataset listed twice: accepted")
	}
}

func TestPins(t *testing.T) {
	files, err := ParsePins([]byte("# a comment\n"+goodPins+"-\t-\tc/d_e.txt\n"), "https://data.example/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1].Pinned() || files[1].URL != "https://data.example/c/d_e.txt" {
		t.Fatalf("%+v", files)
	}
	if got := string(FormatPins(files)); got != goodPins+"-\t-\tc/d_e.txt\n" {
		t.Errorf("formatted: %q", got)
	}
	for name, line := range map[string]string{
		"a path going up":    "-\t-\t../x\n",
		"an absolute path":   "-\t-\t/x\n",
		"a dot file":         "-\t-\ta/.x\n",
		"a space":            "-\t-\ta b\n",
		"an empty segment":   "-\t-\ta//x\n",
		"a short digest":     "abc\t3\tx\n",
		"a size and no hash": "-\t3\tx\n",
		"two columns":        "-\tx\n",
		"a path given twice": goodPins + goodPins,
		"a negative size":    strings.Replace(goodPins, "\t3\t", "\t-3\t", 1),
	} {
		if _, err := ParsePins([]byte(line), "https://data.example/"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// An owner dataset needs no converter or files until the owner has them.
func TestManifestOwnerDataset(t *testing.T) {
	text := strings.Replace(goodManifest, `"access": "public"`, `"access": "owner", "owner_steps": "Register, then download a.zip."`, 1)
	text = strings.Replace(text, `"converter": "ami-words", "base_url": "https://data.example/files/"`, `"base_url": "https://data.example/files/"`, 1)
	m, err := parse(text, "")
	if err != nil || m.Datasets[0].Pinned() {
		t.Fatal(err)
	}
}

func TestSelectSkipsOptIn(t *testing.T) {
	text := strings.Replace(goodManifest, `"kind": "natural"`, `"kind": "natural", "opt_in": "a large download"`, 1)
	m, err := parse(text, goodPins)
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := m.Select(nil); len(all) != 0 {
		t.Error("an opt-in dataset was selected without its name")
	}
	if named, err := m.Select([]string{"set-1"}); err != nil || len(named) != 1 {
		t.Errorf("by name: %v", err)
	}
}
