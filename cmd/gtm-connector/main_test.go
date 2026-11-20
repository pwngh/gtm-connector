package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

var ctx = context.Background()

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCredentialsFolderIsPrivate(t *testing.T) {
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	hosts := certificateHosts([]string{"127.0.0.1:8443", "connector.test:8443"})
	demo, err := creds.setUp(hosts)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range credentialFiles {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
			t.Fatalf("%s: %v %v", name, info.Mode(), err)
		}
	}
	again, err := creds.setUp(hosts)
	if err != nil || again != demo {
		t.Fatal("setting up again changed the demo credential")
	}
	block, _ := pem.Decode(must(creds.read(tlsCertFile)))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.VerifyHostname("127.0.0.1") != nil || cert.VerifyHostname("connector.test") != nil ||
		cert.VerifyHostname("localhost") != nil {
		t.Fatal("the certificate does not cover the allowed hosts")
	}

	// Each refusal: a folder others can read, a link for the folder, a file
	// others can read, a link for a file.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openCredentials(dir, false); err == nil || !strings.Contains(err.Error(), "700") {
		t.Fatalf("an open folder was accepted: %v", err)
	}
	os.Chmod(dir, 0o700)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openCredentials(link, false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a linked folder was accepted: %v", err)
	}
	os.Chmod(filepath.Join(dir, ownerSecretFile), 0o644)
	if _, err := creds.read(ownerSecretFile); err == nil {
		t.Fatal("a readable secret was accepted")
	}
	if _, err := creds.setUp(hosts); err == nil {
		t.Fatal("setup went on with a readable secret")
	}
	os.Chmod(filepath.Join(dir, ownerSecretFile), 0o600)
	elsewhere := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(elsewhere, []byte("x"), 0o600)
	os.Remove(filepath.Join(dir, demoFile))
	if err := os.Symlink(elsewhere, filepath.Join(dir, demoFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.read(demoFile); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a linked credential was accepted: %v", err)
	}
}

func must(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}

func TestBackupAndRestoreKeepState(t *testing.T) {
	f := postgrestest.New(t)
	source := privateDir(t)
	creds, _ := openCredentials(source, false)
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts([]string{"127.0.0.1:8443"})); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup(ctx, f.Store, creds, archive); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(archive); info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", info.Mode())
	}
	if err := backup(ctx, f.Store, creds, archive); err == nil {
		t.Fatal("a backup overwrote a file")
	}

	// Into a fresh database and an absent credentials folder.
	fresh := openFresh(t)
	target := filepath.Join(t.TempDir(), "restored")
	if err := restore(ctx, fresh, target, archive); err != nil {
		t.Fatal(err)
	}
	card, err := fresh.Card(ctx, f.Workspace.ID, f.Card.ID)
	if err != nil || !bytes.Equal(card.Bytes, f.Card.Bytes) {
		t.Fatalf("card after restore: %v", err)
	}
	read, err := fresh.Receipt(ctx, f.Workspace.ID, f.Read.ID)
	if err != nil || read.Status != "ok" || len(read.Rows) != 2 {
		t.Fatalf("read receipt after restore: %v", err)
	}
	restored, err := openCredentials(target, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range credentialFiles {
		if !bytes.Equal(must(restored.read(name)), must(creds.read(name))) {
			t.Fatalf("%s differs after restore", name)
		}
	}

	// Identical credentials already in place are fine; different ones stop
	// the restore before the database is touched.
	if err := restore(ctx, openFresh(t), source, archive); err != nil {
		t.Fatal(err)
	}
	if err := restored.write(ownerSecretFile, []byte("another install")); err != nil {
		t.Fatal(err)
	}
	untouched := openFresh(t)
	if err := restore(ctx, untouched, target, archive); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("restored over other credentials: %v", err)
	}
	if v, _ := untouched.SchemaVersion(ctx); v != 0 {
		t.Fatal("a refused restore changed the database")
	}

	// A changed byte anywhere in the archive is refused.
	tampered := filepath.Join(t.TempDir(), "tampered.tar.gz")
	rewrite(t, archive, tampered, "tables/cards.copy")
	if err := restore(ctx, openFresh(t), filepath.Join(t.TempDir(), "x"), tampered); err == nil ||
		!strings.Contains(err.Error(), "digest") {
		t.Fatalf("a tampered backup was restored: %v", err)
	}
}

func openFresh(t *testing.T) *postgres.Store {
	t.Helper()
	url := pgtest.URL(t)
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	freshURLs[store] = url
	return store
}

// rewrite copies an archive, changing one byte of the named file.
func rewrite(t *testing.T, from, to, name string) {
	t.Helper()
	in, _ := os.Open(from)
	defer in.Close()
	zipped, _ := gzip.NewReader(in)
	reader := tar.NewReader(zipped)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	writer := tar.NewWriter(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(reader)
		if header.Name == name {
			body[0] ^= 1
		}
		writer.WriteHeader(header)
		writer.Write(body)
	}
	writer.Close()
	gz.Close()
	os.WriteFile(to, out.Bytes(), 0o600)
}
