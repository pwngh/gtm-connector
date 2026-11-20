package realdata

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// listed is one file a listing found, with the digest the source gives for it, if any.
type listed struct {
	path    string
	gitSHA1 string // the git blob ID: SHA-1 of "blob <size>\0" and the bytes
	sha256  string // a Git LFS object's SHA-256
}

// Pin records a dataset's files on their first fetch: it lists them (from the dataset's listing, or from its
// pins file), downloads each file not yet in the cache, checks it against the digest the source gives where
// there is one, and returns every file with its size and SHA-256. Files already pinned are checked against
// their pins, not pinned again.
func (f *Fetcher) Pin(ctx context.Context, d Dataset) ([]File, error) {
	if d.Access == Owner {
		return nil, fmt.Errorf("%s: %w; the owner fetches it", d.ID, ErrNeedsOwner)
	}
	sources := map[string]listed{}
	files := append([]File(nil), d.Files...)
	if d.Listing != nil {
		var found []listed
		var err error
		if d.Listing.Git != "" {
			found, err = f.listGit(ctx, d)
		} else {
			found, err = f.listHuggingFace(ctx, d.Listing)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: listing: %w", d.ID, err)
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("%s: the listing matched no file", d.ID)
		}
		pinned := map[string]File{}
		for _, file := range d.Files {
			pinned[file.Path] = file
		}
		files = files[:0]
		for _, l := range found {
			sources[l.path] = l
			file, ok := pinned[l.path]
			if !ok {
				file = File{Path: l.path, URL: d.BaseURL + escapePath(l.path)}
			}
			files = append(files, file)
		}
	}
	dir := filesDir(f.Cache, d)
	var mu sync.Mutex
	pins := map[string]File{}
	err := each(ctx, files, func(ctx context.Context, file File) error {
		local := filepath.Join(dir, filepath.FromSlash(file.Path))
		if file.Pinned() {
			if _, err := os.Lstat(local); err == nil {
				return Verify(local, file)
			}
			_, err := f.download(ctx, file, local, true)
			return err
		}
		if _, err := os.Lstat(local); err == nil {
			return fmt.Errorf("%s is in the cache but has no pin; remove it to fetch it again", file.Path)
		}
		pin, err := f.download(ctx, file, local, false)
		if err == nil {
			err = checkSource(local, pin, sources[file.Path])
		}
		if err != nil {
			os.Remove(local)
			return err
		}
		mu.Lock()
		pins[pin.Path] = pin
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.ID, err)
	}
	for i, file := range files {
		if pin, ok := pins[file.Path]; ok {
			files[i] = pin
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// checkSource compares a fresh download with the digest its source lists.
func checkSource(local string, pin File, source listed) error {
	switch {
	case source.sha256 != "" && source.sha256 != pin.SHA256:
		return fmt.Errorf("%s has SHA-256 %s, not the %s its source lists", pin.Path, pin.SHA256, source.sha256)
	case source.gitSHA1 != "":
		data, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d\x00", len(data))
		blob.Write(data)
		if got := hex.EncodeToString(blob.Sum(nil)); got != source.gitSHA1 {
			return fmt.Errorf("%s is git blob %s, not the %s its source lists", pin.Path, got, source.gitSHA1)
		}
	}
	return nil
}

func included(l *Listing, p string) bool {
	for _, pattern := range l.Include {
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}

// listGit lists a repository at its revision from a clone of its trees alone: no file's content is fetched,
// so a repository that holds audio costs no more than its folder listing.
func (f *Fetcher) listGit(ctx context.Context, d Dataset) ([]listed, error) {
	bare := filepath.Join(DatasetDir(f.Cache, d), "listing.git")
	git := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-c", "credential.helper=",
			"-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}, args...)...)
		command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		out, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	if _, err := os.Stat(bare); err != nil {
		if _, err := git("init", "-q", "--bare", bare); err != nil {
			return nil, err
		}
	}
	if _, err := git("-C", bare, "fetch", "-q", "--depth=1", "--filter=blob:none", d.Listing.Git, d.Listing.Revision); err != nil {
		return nil, err
	}
	out, err := git("-C", bare, "ls-tree", "-r", "-z", "--full-tree", d.Listing.Revision)
	if err != nil {
		return nil, err
	}
	var found []listed
	for _, entry := range bytes.Split(out, []byte{0}) {
		meta, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !included(d.Listing, name) {
			continue
		}
		if !ValidPath(name) {
			return nil, fmt.Errorf("the listing has a path this program does not store")
		}
		found = append(found, listed{path: name, gitSHA1: fields[2]})
	}
	return found, nil
}

// hubEntry is one entry of the Hugging Face Hub's tree listing.
type hubEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID string `json:"oid"`
	} `json:"lfs"`
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// listHuggingFace walks a dataset's folders one level at a time, entering only the folders an include
// pattern can match, so the audio beside the transcripts is never listed. A pattern's last segment that is a
// plain name is looked up in batches rather than listed.
func (f *Fetcher) listHuggingFace(ctx context.Context, l *Listing) ([]listed, error) {
	seen := map[string]listed{}
	var named []string
	add := func(e hubEntry) error {
		if !ValidPath(e.Path) {
			return fmt.Errorf("the listing has a path this program does not store")
		}
		found := listed{path: e.Path, gitSHA1: e.OID}
		if e.LFS != nil {
			found = listed{path: e.Path, sha256: e.LFS.OID}
		}
		seen[e.Path] = found
		return nil
	}
	var walk func(folder string, segments []string) error
	walk = func(folder string, segments []string) error {
		if len(segments) == 1 && !strings.ContainsAny(segments[0], `*?[\`) {
			named = append(named, path.Join(folder, segments[0]))
			return nil
		}
		entries, err := f.hubTree(ctx, l, folder)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if ok, _ := path.Match(segments[0], path.Base(e.Path)); !ok {
				continue
			}
			switch {
			case len(segments) == 1 && e.Type == "file":
				if err := add(e); err != nil {
					return err
				}
			case len(segments) > 1 && e.Type == "directory":
				if err := walk(e.Path, segments[1:]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, pattern := range l.Include {
		if err := walk("", strings.Split(pattern, "/")); err != nil {
			return nil, err
		}
	}
	for len(named) > 0 {
		batch := named[:min(len(named), 200)]
		named = named[len(batch):]
		body, _ := json.Marshal(map[string]any{"paths": batch, "expand": false})
		var entries []hubEntry
		endpoint := "https://huggingface.co/api/datasets/" + l.HuggingFace + "/paths-info/" + l.Revision
		if _, err := f.hub(ctx, http.MethodPost, endpoint, body, &entries); err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type == "file" {
				if err := add(e); err != nil {
					return nil, err
				}
			}
		}
	}
	var found []listed
	for _, l := range seen {
		found = append(found, l)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	return found, nil
}

func (f *Fetcher) hubTree(ctx context.Context, l *Listing, folder string) ([]hubEntry, error) {
	next := "https://huggingface.co/api/datasets/" + l.HuggingFace + "/tree/" + l.Revision
	if folder != "" {
		next += "/" + escapePath(folder)
	}
	next += "?recursive=false&limit=1000"
	var entries []hubEntry
	for next != "" {
		var page []hubEntry
		header, err := f.hub(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
		next = ""
		if found := nextLink.FindStringSubmatch(header.Get("Link")); found != nil {
			next = found[1]
		}
	}
	return entries, nil
}

// hub makes one call to the Hugging Face Hub's API and decodes its JSON answer. When the Hub says to slow
// down, it waits as long as the Hub asks, up to five minutes, three times at most.
func (f *Fetcher) hub(ctx context.Context, method, endpoint string, body []byte, value any) (http.Header, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "huggingface.co" {
		return nil, errors.New("a Hub address that is not on https://huggingface.co")
	}
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", "gtm-connector-realdata")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := f.Client.Do(request)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			wait, err := strconv.Atoi(response.Header.Get("Retry-After"))
			if err != nil || wait < 1 || wait > 300 {
				wait = 60
			}
			f.Log("the Hub asks to wait %d s", wait)
			select {
			case <-time.After(time.Duration(wait) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the Hub answered HTTP %d", response.StatusCode)
		}
		if err := json.Unmarshal(data, value); err != nil {
			return nil, errors.New("the Hub's answer is not readable")
		}
		return response.Header, nil
	}
}
