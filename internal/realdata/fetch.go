package realdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sync"
	"time"
)

var (
	// ErrNeedsOwner is an owner dataset whose files are not all in the cache.
	ErrNeedsOwner = errors.New("needs the owner")
	// ErrNotPinned is a dataset with files that have no digest yet.
	ErrNotPinned = errors.New("not pinned yet")
)

// workers is how many files are fetched at once.
const workers = 8

// Fetcher downloads a dataset's files into its folder in the cache.
type Fetcher struct {
	Client *http.Client
	Cache  string
	Log    func(format string, args ...any)
	// HubAPI is the Hugging Face Hub's datasets API, which pin lists a Hub dataset through; no call leaves
	// its host.
	HubAPI string
	// GitProtocols are the protocols git may use to list a repository.
	GitProtocols []string
}

// hostsKey carries, in a request's context, the hosts its redirects may reach.
type hostsKey struct{}

// withHosts lets a request's redirects reach the dataset's own host and the hosts it names.
func withHosts(ctx context.Context, d Dataset, file File) context.Context {
	hosts := map[string]bool{}
	if u, err := url.Parse(file.URL); err == nil {
		hosts[u.Host] = true
	}
	for _, host := range d.RedirectHosts {
		hosts[host] = true
	}
	return context.WithValue(ctx, hostsKey{}, hosts)
}

// NewFetcher gives a Fetcher that follows a redirect only over https and only to a host its request allows
// (the first request's host when it names none), and sends no identifying header.
func NewFetcher(cache string, log func(string, ...any)) *Fetcher {
	client := &http.Client{
		Timeout: 60 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("a redirect to %s, which is not https", req.URL.Scheme)
			}
			if len(via) >= 5 {
				return errors.New("more than five redirects")
			}
			hosts, _ := req.Context().Value(hostsKey{}).(map[string]bool)
			if hosts == nil {
				hosts = map[string]bool{via[0].URL.Host: true}
			}
			if !hosts[req.URL.Host] {
				return fmt.Errorf("a redirect to %s, which the dataset does not name", req.URL.Host)
			}
			return nil
		},
	}
	return &Fetcher{Client: client, Cache: cache, Log: log, HubAPI: "https://huggingface.co/api/datasets/",
		GitProtocols: []string{"https"}}
}

// Fetch makes sure each of the dataset's files is in the cache and matches its pin. A file already there is
// checked, not fetched again. A mismatch is an error; a cached file that does not match is left for a
// person to look at, and a fresh download that does not match is removed.
func (f *Fetcher) Fetch(ctx context.Context, d Dataset) error {
	if d.Access == Owner {
		return f.ownerFiles(d)
	}
	if !d.Pinned() {
		f.Log("%s: skipped, %s; `realdata pin %s` fetches it and records its digests in the repository", d.ID,
			ErrNotPinned, d.ID)
		return ErrNotPinned
	}
	root, err := datasetDir(f.Cache, d.ID)
	if err != nil {
		return err
	}
	defer root.Close()
	var cached, fetched int
	var bytes int64
	var mu sync.Mutex
	err = each(ctx, d.Files, func(ctx context.Context, file File) error {
		name := "files/" + file.Path
		if _, err := root.Lstat(name); err == nil {
			if err := verifyIn(root, name, file); err != nil {
				return fmt.Errorf("%w; remove it to fetch it again", err)
			}
			mu.Lock()
			cached++
			mu.Unlock()
			return nil
		}
		if _, err := f.download(withHosts(ctx, d, file), file, root, name, true); err != nil {
			return err
		}
		mu.Lock()
		fetched++
		bytes += file.Bytes
		mu.Unlock()
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", d.ID, err)
	}
	f.Log("%s: %d files match their pins: %d were in the cache, %d fetched (%d bytes)", d.ID, cached+fetched, cached,
		fetched, bytes)
	return nil
}

func (f *Fetcher) ownerFiles(d Dataset) error {
	dir := filesDir(f.Cache, d)
	if !d.Pinned() {
		f.Log("%s: skipped, it %s. %s Then place its files in %s and record them in pins/%s.tsv.", d.ID,
			ErrNeedsOwner, d.OwnerSteps, dir, d.ID)
		return ErrNeedsOwner
	}
	root, err := datasetDir(f.Cache, d.ID)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range d.Files {
		name := "files/" + file.Path
		if _, err := root.Lstat(name); err != nil {
			f.Log("%s: skipped, it %s. %s Then place %s in %s.", d.ID, ErrNeedsOwner, d.OwnerSteps, file.Path, dir)
			return ErrNeedsOwner
		}
		if err := verifyIn(root, name, file); err != nil {
			return fmt.Errorf("%s: %w", d.ID, err)
		}
	}
	f.Log("%s: the owner's files are in the cache and match their pins", d.ID)
	return nil
}

// each runs fn on every file, workers at a time, and stops at the first error.
func each(ctx context.Context, files []File, fn func(context.Context, File) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan File)
	var first error
	var once sync.Once
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range jobs {
				if err := fn(ctx, file); err != nil {
					once.Do(func() { first = err; cancel() })
				}
			}
		}()
	}
send:
	for _, file := range files {
		select {
		case jobs <- file:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return first
}

// download fetches one file over https into the dataset's folder: into a fresh temporary file, renamed
// over its name once it is complete. With check, it must match the file's pin; without, it gives the size and
// SHA-256 it found, for a pin.
func (f *Fetcher) download(ctx context.Context, file File, root *os.Root, name string, check bool) (pin File, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
	if err != nil {
		return File{}, err
	}
	if request.URL.Scheme != "https" {
		return File{}, fmt.Errorf("%s: only https is fetched", file.Path)
	}
	request.Header.Set("User-Agent", "gtm-connector-realdata")
	response, err := f.Client.Do(request)
	if err != nil {
		return File{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return File{}, fmt.Errorf("%s: HTTP %d", file.Path, response.StatusCode)
	}
	if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
		return File{}, err
	}
	out, temporary, err := createExclusive(root, name, 0o644)
	if err != nil {
		return File{}, err
	}
	defer func() {
		out.Close()
		if err != nil {
			root.Remove(temporary)
		}
	}()
	hash := sha256.New()
	body := io.Reader(response.Body)
	if check {
		// One byte more than the pin's size shows a file that is too long without reading all of it.
		body = io.LimitReader(body, file.Bytes+1)
	}
	n, err := io.Copy(io.MultiWriter(out, hash), body)
	if err != nil {
		return File{}, err
	}
	pin = File{Path: file.Path, URL: file.URL, Bytes: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	if check {
		if err := match(file, n, hash.Sum(nil)); err != nil {
			return File{}, err
		}
	}
	if err := out.Close(); err != nil {
		return File{}, err
	}
	return pin, root.Rename(temporary, name)
}

// Verify checks a file against its pin: a regular file, its size, then its SHA-256.
func Verify(path string, file File) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", file.Path)
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	return verifyOpen(in, file)
}

// verifyIn checks a file in a dataset's folder against its pin.
func verifyIn(root *os.Root, name string, file File) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", file.Path)
	}
	in, err := root.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	return verifyOpen(in, file)
}

// verifyOpen checks an open file's bytes, from its start, against its pin.
func verifyOpen(in *os.File, file File) error {
	if !file.Pinned() {
		return fmt.Errorf("%s has no pin", file.Path)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(in, 0, file.Bytes+1))
	if err != nil {
		return err
	}
	return match(file, n, hash.Sum(nil))
}

func match(file File, n int64, sum []byte) error {
	if n > file.Bytes {
		return fmt.Errorf("%s is longer than its pin's %d bytes", file.Path, file.Bytes)
	}
	if n < file.Bytes {
		return fmt.Errorf("%s has %d bytes, not its pin's %d", file.Path, n, file.Bytes)
	}
	if got := hex.EncodeToString(sum); got != file.SHA256 {
		return fmt.Errorf("%s has SHA-256 %s, not its pin's %s", file.Path, got, file.SHA256)
	}
	return nil
}
