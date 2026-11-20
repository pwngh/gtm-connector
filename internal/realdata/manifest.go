// Package realdata runs real public text through the Go port and compares it with the Python oracle (R0 in
// the backend plan). The repository holds only the manifest, the pins, the fetch, the converters and the
// comparison; the data lives in a cache outside it, checked against the pinned digests.
package realdata

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

//go:embed manifest.json pins
var embedded embed.FS

// Access says who may fetch a dataset. A public dataset is fetched here; an owner dataset needs an account,
// a registration, accepted terms or a decision only the owner can make, so only the owner fetches it and
// places its files in the cache.
const (
	Public = "public"
	Owner  = "owner"
)

// Kinds say how the text came to be, so counts can be split by it. A converter may tag each item itself,
// as AMI tags each meeting.
var knownKinds = map[string]bool{"natural": true, "role-played": true, "elicited": true, "invented": true}

// Kinds lists the kinds in the order reports give them.
var Kinds = []string{"natural", "role-played", "elicited", "invented"}

// Manifest lists the datasets.
type Manifest struct {
	Schema   int       `json:"schema"`
	Datasets []Dataset `json:"datasets"`
}

// Dataset is one corpus at one version. Its files are pins/ID.tsv: each file's SHA-256, size and path,
// fetched from BaseURL followed by the path.
type Dataset struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Version       string `json:"version"`
	Note          string `json:"note,omitempty"`
	Licence       string `json:"licence"`
	LicenceSource string `json:"licence_source"`
	Access        string `json:"access"`
	// OwnerSteps says what the owner must do to fetch an owner dataset.
	OwnerSteps string `json:"owner_steps,omitempty"`
	Kind       string `json:"kind"`
	// OptIn says why a dataset runs only when it is named.
	OptIn     string `json:"opt_in,omitempty"`
	Converter string `json:"converter,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	// RedirectHosts are the hosts besides the base URL's that a download may be redirected to.
	RedirectHosts []string `json:"redirect_hosts,omitempty"`
	Listing       *Listing `json:"listing,omitempty"`
	Files         []File   `json:"-"`
}

// Listing says where `realdata pin` finds a dataset's files when they are too many to list by hand: a git
// repository or a Hugging Face dataset at one revision, narrowed to the paths that match Include. Without
// a listing, pins/ID.tsv lists the files itself.
type Listing struct {
	Git         string   `json:"git,omitempty"`
	HuggingFace string   `json:"huggingface,omitempty"`
	Revision    string   `json:"revision"`
	Include     []string `json:"include"`
}

// File is one download. A file listed but not yet fetched has no size or digest; such a file is never read.
type File struct {
	Path   string
	URL    string
	Bytes  int64
	SHA256 string
}

// Pinned is whether the file has a size and a digest to check against.
func (f File) Pinned() bool { return f.SHA256 != "" }

// Pinned is whether every file of the dataset is pinned.
func (d Dataset) Pinned() bool {
	if len(d.Files) == 0 {
		return false
	}
	for _, f := range d.Files {
		if !f.Pinned() {
			return false
		}
	}
	return true
}

var (
	datasetID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	segment   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,127}$`)
	digest    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	revision  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// LoadManifest reads the manifest and the pins built into this program.
func LoadManifest() (*Manifest, error) {
	data, err := embedded.ReadFile("manifest.json")
	if err != nil {
		return nil, err
	}
	return ParseManifest(data, func(id string) ([]byte, error) {
		data, err := embedded.ReadFile("pins/" + id + ".tsv")
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return data, err
	})
}

// ParseManifest reads and checks a manifest. pins gives a dataset's pins file, or nil when it has none yet.
func ParseManifest(data []byte, pins func(id string) ([]byte, error)) (*Manifest, error) {
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("manifest: more than one JSON value")
	}
	if m.Schema != 2 {
		return nil, fmt.Errorf("manifest: schema %d; this program reads schema 2", m.Schema)
	}
	seen := map[string]bool{}
	for i := range m.Datasets {
		d := &m.Datasets[i]
		if !datasetID.MatchString(d.ID) {
			return nil, fmt.Errorf("manifest: dataset id %q: use lower-case letters, digits, dots and hyphens", d.ID)
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("manifest: dataset %s is listed twice", d.ID)
		}
		seen[d.ID] = true
		text, err := pins(d.ID)
		if err == nil && text != nil {
			d.Files, err = ParsePins(text, d.BaseURL)
		}
		if err == nil {
			err = d.check()
		}
		if err != nil {
			return nil, fmt.Errorf("manifest: dataset %s: %w", d.ID, err)
		}
	}
	return &m, nil
}

func (d *Dataset) check() error {
	switch {
	case d.Name == "" || d.Version == "" || d.Licence == "":
		return errors.New("name, version and licence are required")
	case !httpsURL(d.URL):
		return errors.New("url must be an https URL")
	case !httpsURL(d.LicenceSource):
		return errors.New("licence_source must be the https URL where the licence is stated")
	case !knownKinds[d.Kind]:
		return fmt.Errorf("kind %q: use natural, role-played, elicited or invented", d.Kind)
	}
	// A dataset with no converter yet is pinned and fetched but not compared; its note says why.
	if d.Converter != "" && converters[d.Converter] == nil {
		return fmt.Errorf("no converter named %q", d.Converter)
	}
	switch d.Access {
	case Public:
		if d.OwnerSteps != "" {
			return errors.New("owner_steps is for owner datasets")
		}
		if d.Converter == "" && d.Note == "" {
			return errors.New("a dataset with no converter says why in its note")
		}
	case Owner:
		if d.OwnerSteps == "" {
			return errors.New("an owner dataset says in owner_steps what the owner must do")
		}
	default:
		return fmt.Errorf("access %q: use %q or %q", d.Access, Public, Owner)
	}
	if d.BaseURL != "" && (!httpsURL(d.BaseURL) || !strings.HasSuffix(d.BaseURL, "/")) {
		return errors.New("base_url must be an https URL ending in /")
	}
	for _, host := range d.RedirectHosts {
		if u, err := url.Parse("https://" + host + "/"); err != nil || u.Host != host || host == "" {
			return fmt.Errorf("redirect host %q: a host name only", host)
		}
	}
	if l := d.Listing; l != nil {
		if (l.Git == "") == (l.HuggingFace == "") || !revision.MatchString(l.Revision) || len(l.Include) == 0 {
			return errors.New("a listing names one git repository or Hugging Face dataset, a full revision and include patterns")
		}
		if l.Git != "" && !httpsURL(l.Git) {
			return errors.New("listing.git must be an https URL")
		}
		for _, pattern := range l.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("include pattern %q: %w", pattern, err)
			}
		}
	}
	if d.Access == Public && d.BaseURL == "" {
		return errors.New("a public dataset has a base_url")
	}
	if d.Access == Public && d.Listing == nil && len(d.Files) == 0 {
		return errors.New("a public dataset lists its files in pins/ or has a listing")
	}
	return nil
}

// ParsePins reads a pins file: one file a line, "SHA256<tab>BYTES<tab>PATH", or "-<tab>-<tab>PATH" for a
// file listed but not yet fetched. Lines starting with # are comments.
func ParsePins(data []byte, base string) ([]File, error) {
	var files []File
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || !ValidPath(fields[2]) || seen[fields[2]] {
			return nil, fmt.Errorf("pins line %d: not SHA256, bytes and a new path, tab-separated", n)
		}
		seen[fields[2]] = true
		f := File{Path: fields[2], URL: base + escapePath(fields[2])}
		if fields[0] != "-" || fields[1] != "-" {
			size, err := strconv.ParseInt(fields[1], 10, 64)
			if !digest.MatchString(fields[0]) || err != nil || size < 0 {
				return nil, fmt.Errorf("pins line %d: the SHA-256 is 64 lower-case hex digits and the size a number", n)
			}
			f.SHA256, f.Bytes = fields[0], size
		}
		files = append(files, f)
	}
	return files, scanner.Err()
}

// FormatPins writes files as a pins file, in path order.
func FormatPins(files []File) []byte {
	var b bytes.Buffer
	for _, f := range files {
		if f.Pinned() {
			fmt.Fprintf(&b, "%s\t%d\t%s\n", f.SHA256, f.Bytes, f.Path)
		} else {
			fmt.Fprintf(&b, "-\t-\t%s\n", f.Path)
		}
	}
	return b.Bytes()
}

// ValidPath is a relative path of plain segments: no empty segment, none that starts with a dot, so none is
// "..".
func ValidPath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if !segment.MatchString(s) {
			return false
		}
	}
	return true
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func httpsURL(text string) bool {
	u, err := url.Parse(text)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// Select gives the named datasets, or every dataset that is not opt-in when no name is given.
func (m *Manifest) Select(ids []string) ([]Dataset, error) {
	if len(ids) == 0 {
		var found []Dataset
		for _, d := range m.Datasets {
			if d.OptIn == "" {
				found = append(found, d)
			}
		}
		return found, nil
	}
	var found []Dataset
	for _, id := range ids {
		i := m.index(id)
		if i < 0 {
			return nil, fmt.Errorf("no dataset %q in the manifest", id)
		}
		found = append(found, m.Datasets[i])
	}
	return found, nil
}

func (m *Manifest) index(id string) int {
	for i, d := range m.Datasets {
		if d.ID == id {
			return i
		}
	}
	return -1
}
