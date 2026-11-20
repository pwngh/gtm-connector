package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

// A backup is one gzip'd tar file, written with mode 0600:
//
//	manifest.json        format, schema version, and each file's size and SHA-256
//	credentials/<name>   every file of the credentials folder
//	tables/<name>.copy   each table in PostgreSQL's COPY text format
//
// Restore checks every digest before it changes anything.
const backupFormat = "gtm-connector backup"

type manifest struct {
	Format        string       `json:"format"`
	Version       int          `json:"version"`
	CreatedAt     string       `json:"created_at"`
	SchemaVersion int          `json:"schema_version"`
	Tables        []tableEntry `json:"tables"`
	Credentials   []fileEntry  `json:"credentials"`
}

type fileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type tableEntry struct {
	Table   string    `json:"table"`
	Columns []string  `json:"columns"`
	Rows    int64     `json:"rows"`
	File    fileEntry `json:"file"`
}

// maxBackupFile bounds each file read back from an archive.
const maxBackupFile = 4 << 30

func backupCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := flags.String("out", "", "the backup file to write; it must not exist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" || flags.NArg() > 0 {
		return errors.New("usage: gtm-connector backup --out FILE")
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	creds, err := openCredentials(c.credentialsDir, false)
	if err != nil {
		return err
	}
	store, err := postgres.Open(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := backup(ctx, store, creds, *out); err != nil {
		return err
	}
	fmt.Println("Wrote", *out)
	return nil
}

// backup dumps the tables into a private temporary folder beside out, then
// writes the archive to out.partial and renames it into place.
func backup(ctx context.Context, store *postgres.Store, creds credentials, out string) error {
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("%s already exists", out)
	}
	work, err := os.MkdirTemp(filepath.Dir(out), ".gtm-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	digests := map[string]*fileEntry{}
	version, copies, err := store.Dump(ctx, func(table string) (io.WriteCloser, error) {
		file, err := os.OpenFile(filepath.Join(work, table+".copy"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		entry := &fileEntry{Name: "tables/" + table + ".copy"}
		digests[table] = entry
		return &hashingWriter{file: file, hash: sha256.New(), entry: entry}, nil
	})
	if err != nil {
		return err
	}
	m := manifest{Format: backupFormat, Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SchemaVersion: version}
	for _, table := range copies {
		m.Tables = append(m.Tables, tableEntry{table.Name, table.Columns, table.Rows, *digests[table.Name]})
	}
	secrets := map[string][]byte{}
	for _, name := range credentialFiles {
		data, err := creds.read(name)
		if err != nil {
			return fmt.Errorf("credentials: %w", err)
		}
		secrets[name] = data
		m.Credentials = append(m.Credentials, entryFor("credentials/"+name, data))
	}
	manifestJSON, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	partial := out + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(partial)
	zipped := gzip.NewWriter(file)
	archive := tar.NewWriter(zipped)
	add := func(name string, size int64, body io.Reader) error {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size,
			ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := io.Copy(archive, body)
		return err
	}
	err = add("manifest.json", int64(len(manifestJSON)), bytes.NewReader(manifestJSON))
	for _, name := range credentialFiles {
		if err == nil {
			err = add("credentials/"+name, int64(len(secrets[name])), bytes.NewReader(secrets[name]))
		}
	}
	for _, table := range m.Tables {
		if err != nil {
			break
		}
		var body *os.File
		body, err = os.Open(filepath.Join(work, table.Table+".copy"))
		if err == nil {
			err = add(table.File.Name, table.File.Size, body)
			body.Close()
		}
	}
	for _, closer := range []io.Closer{archive, zipped} {
		if closeErr := closer.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(partial, out)
}

type hashingWriter struct {
	file  *os.File
	hash  hash.Hash
	entry *fileEntry
}

func (w *hashingWriter) Write(p []byte) (int, error) {
	n, err := w.file.Write(p)
	w.hash.Write(p[:n])
	w.entry.Size += int64(n)
	return n, err
}

func (w *hashingWriter) Close() error {
	w.entry.SHA256 = hex.EncodeToString(w.hash.Sum(nil))
	return w.file.Close()
}

func entryFor(name string, data []byte) fileEntry {
	sum := sha256.Sum256(data)
	return fileEntry{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func restoreCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	in := flags.String("in", "", "the backup file to restore")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *in == "" || flags.NArg() > 0 {
		return errors.New("usage: gtm-connector restore --in FILE")
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openStore(ctx, c.databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := restore(ctx, store, c.credentialsDir, *in); err != nil {
		return err
	}
	fmt.Println("Restored", *in)
	return nil
}

// restore unpacks a backup into a private temporary folder and checks every
// digest. Each credential must be absent or already identical. It then loads
// the tables into the empty database and writes the missing credentials.
func restore(ctx context.Context, store *postgres.Store, credentialsDir, in string) error {
	work, err := os.MkdirTemp("", "gtm-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	m, err := unpack(in, work)
	if err != nil {
		return err
	}
	creds, err := openCredentials(credentialsDir, true)
	if err != nil {
		return err
	}
	var missing []string
	for _, entry := range m.Credentials {
		name := strings.TrimPrefix(entry.Name, "credentials/")
		saved, err := os.ReadFile(filepath.Join(work, entry.Name))
		if err != nil {
			return err
		}
		current, err := creds.read(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, name)
		case err != nil:
			return err
		case !bytes.Equal(current, saved):
			return fmt.Errorf("%s differs from the backup's; move the credentials folder aside to restore it",
				filepath.Join(credentialsDir, name))
		}
	}
	copies := make([]postgres.TableCopy, len(m.Tables))
	for i, table := range m.Tables {
		copies[i] = postgres.TableCopy{Name: table.Table, Columns: table.Columns, Rows: table.Rows}
	}
	err = store.Restore(ctx, m.SchemaVersion, copies, func(table string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(work, "tables", table+".copy"))
	})
	if err != nil {
		return err
	}
	for _, name := range missing {
		saved, err := os.ReadFile(filepath.Join(work, "credentials", name))
		if err != nil {
			return err
		}
		if err := creds.write(name, saved); err != nil {
			return fmt.Errorf("the database is restored, but writing credential %s failed: %w", name, err)
		}
	}
	return nil
}

// unpack extracts exactly the files the manifest lists, as regular files, and
// checks each one's size and digest.
func unpack(in, work string) (manifest, error) {
	file, err := os.Open(in)
	if err != nil {
		return manifest{}, err
	}
	defer file.Close()
	zipped, err := gzip.NewReader(file)
	if err != nil {
		return manifest{}, fmt.Errorf("%s is not a backup: %w", in, err)
	}
	archive := tar.NewReader(zipped)
	var m manifest
	seen := map[string]fileEntry{}
	for i := 0; ; i++ {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest{}, fmt.Errorf("%s: %w", in, err)
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxBackupFile {
			return manifest{}, fmt.Errorf("%s holds an entry that is not a regular file: %q", in, header.Name)
		}
		if i == 0 {
			if header.Name != "manifest.json" || header.Size > 1<<20 {
				return manifest{}, fmt.Errorf("%s does not start with its manifest", in)
			}
			raw, err := io.ReadAll(archive)
			if err != nil {
				return manifest{}, err
			}
			if err := json.Unmarshal(raw, &m); err != nil || m.Format != backupFormat || m.Version != 1 {
				return manifest{}, fmt.Errorf("%s is not a version 1 gtm-connector backup", in)
			}
			continue
		}
		expected, ok := listed(m, header.Name)
		if !ok {
			return manifest{}, fmt.Errorf("%s holds an unlisted file %q", in, header.Name)
		}
		if _, twice := seen[header.Name]; twice {
			return manifest{}, fmt.Errorf("%s holds %q twice", in, header.Name)
		}
		target := filepath.Join(work, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return manifest{}, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return manifest{}, err
		}
		digest := sha256.New()
		size, err := io.Copy(io.MultiWriter(out, digest), io.LimitReader(archive, maxBackupFile))
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return manifest{}, err
		}
		if size != expected.Size || hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 {
			return manifest{}, fmt.Errorf("%s: %q does not match its manifest digest", in, header.Name)
		}
		seen[header.Name] = expected
	}
	if m.Format == "" {
		return manifest{}, fmt.Errorf("%s is empty", in)
	}
	for _, entry := range allListed(m) {
		if _, ok := seen[entry.Name]; !ok {
			return manifest{}, fmt.Errorf("%s is missing %q", in, entry.Name)
		}
	}
	return m, nil
}

func allListed(m manifest) []fileEntry {
	var entries []fileEntry
	for _, entry := range m.Credentials {
		entries = append(entries, entry)
	}
	for _, table := range m.Tables {
		entries = append(entries, table.File)
	}
	return entries
}

// listed finds a file in the manifest. Names are fixed by this format, so a
// path that climbs out of the work folder is never listed.
func listed(m manifest, name string) (fileEntry, bool) {
	for _, entry := range allListed(m) {
		if entry.Name != name {
			continue
		}
		base := strings.TrimPrefix(strings.TrimPrefix(name, "credentials/"), "tables/")
		if strings.HasPrefix(name, "credentials/") && slices.Contains(credentialFiles, base) ||
			strings.HasPrefix(name, "tables/") && !strings.ContainsAny(strings.TrimSuffix(base, ".copy"), "/\\.") &&
				strings.HasSuffix(base, ".copy") {
			return entry, true
		}
	}
	return fileEntry{}, false
}
