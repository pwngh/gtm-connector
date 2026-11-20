package realdata

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Every write goes through an os.Root opened on the folder it belongs to, so no link, in the folder or
// above a name in it, can send it elsewhere. A file is written under a fresh, unpredictable name created
// exclusively, then renamed over its name: whatever was left at either name, link or file, is replaced and
// never written through, and the new file has the mode it was created with.

// privateDir opens the results folder of a local run. It is made 0700 when absent; one that exists must be a
// folder, not a link, owned by this user and open to no one else.
func privateDir(dir string) (*os.Root, error) {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	switch {
	case !info.IsDir():
		return nil, fmt.Errorf("%s exists and is not a folder", dir)
	case !ownedByMe(info):
		return nil, fmt.Errorf("%s belongs to another user", dir)
	case info.Mode().Perm() != 0o700:
		return nil, fmt.Errorf("%s is open to others (mode %04o); make it 0700 or remove it", dir, info.Mode().Perm())
	}
	return openSame(dir, info)
}

// cacheDir opens the cache's folder, made when absent.
func cacheDir(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// datasetDir opens a dataset's folder inside the cache, made when absent. A link there that leads out of
// the cache is refused.
func datasetDir(cache, id string) (*os.Root, error) {
	root, err := cacheDir(cache)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(id, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	dir, err := root.OpenRoot(id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return dir, nil
}

// openSame opens a folder as a root and checks that it is the folder that was examined.
func openSame(dir string, examined fs.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(examined, opened) {
		root.Close()
		return nil, fmt.Errorf("%s changed while it was opened", dir)
	}
	return root, nil
}

// createExclusive creates a new file beside name, under a name no one could have prepared.
func createExclusive(root *os.Root, name string, perm os.FileMode) (*os.File, string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	temporary := path.Join(path.Dir(name), "."+path.Base(name)+"."+hex.EncodeToString(random)+".tmp")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	return file, temporary, err
}

// writeExclusive writes a file as a fresh file renamed over its name.
func writeExclusive(root *os.Root, name string, data []byte, perm os.FileMode) error {
	file, temporary, err := createExclusive(root, name, perm)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	err = errors.Join(err, file.Close())
	if err == nil {
		err = root.Rename(temporary, name)
	}
	if err != nil {
		root.Remove(temporary)
	}
	return err
}

// insideRepository is whether a path, existing or not, lies in the repository. It compares folders by
// identity, not by spelling: the path's links are resolved as far as it exists, then each folder above it
// is compared with the repository's, so a link or another case of the same name is still inside.
func insideRepository(target, repository string) (bool, error) {
	repo, err := os.Stat(repository)
	if err != nil {
		return false, err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false, err
	}
	// Resolve the part of the path that exists; keep the rest as written.
	existing, rest := target, ""
	for {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil {
			existing = resolved
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	for dir := filepath.Join(existing, rest); ; {
		if info, err := os.Stat(dir); err == nil && os.SameFile(info, repo) {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

// RefuseInsideRepository is an error when a path lies in the repository.
func RefuseInsideRepository(what, target, repository string) error {
	inside, err := insideRepository(target, repository)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%s %s is inside the repository; keep it outside", what, target)
	}
	return nil
}
