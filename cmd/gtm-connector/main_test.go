package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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

// A backup carries the credential files setup makes and leaves a workspace's Slack sign-in and its lock out.
// A copy of the sign-in could still be valid, a live Slack credential in a file that is copied and kept; and
// Slack spends a refresh token once, so a restored copy could as well be one Slack no longer accepts. A
// restore puts no sign-in back.
func TestABackupLeavesTheSlackSignInOut(t *testing.T) {
	f := postgrestest.New(t)
	source := privateDir(t)
	creds, err := openCredentials(source, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts([]string{"127.0.0.1:8443"})); err != nil {
		t.Fatal(err)
	}
	const saved = "invented-slack-sign-in-0000000000000000"
	for _, name := range []string{"slack-acme.json", "slack-acme.lock"} {
		if err := creds.write(name, []byte(saved)); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup(ctx, f.Store, creds, archive); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	zipped, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	entries := tar.NewReader(zipped)
	carried := map[string]bool{}
	for {
		header, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(entries)
		if err != nil {
			t.Fatal(err)
		}
		carried[header.Name] = true
		if strings.Contains(header.Name, "slack-acme") || bytes.Contains(body, []byte("slack-acme")) ||
			bytes.Contains(body, []byte(saved)) {
			t.Errorf("the backup carries the Slack sign-in in %s", header.Name)
		}
	}
	for _, name := range credentialFiles {
		if !carried["credentials/"+name] {
			t.Errorf("the backup does not carry %s", name)
		}
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := restore(ctx, openFresh(t), target, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range restored {
		if strings.HasPrefix(entry.Name(), "slack-") {
			t.Errorf("the restore wrote %s", entry.Name())
		}
	}
}

// testdata/backup-schema1.tar.gz was written by the schema 1 binary (main at
// b342ab7): setup on a fresh database, a grant, a refused call and a brief
// whose child read bound a record, then backup. Every later binary restores
// it, migrates it, and signs in its demo principal.
func TestASchemaOneBackupRestores(t *testing.T) {
	const (
		workspace = "5ec3cc99-ec0b-4c64-a26c-fb31fba45e3c"
		principal = "9a8f9f7f-0865-4c43-8b1d-cdc6e5c91678"
		childRead = "66666666-6666-4666-8666-666666666666"
	)
	fresh := openFresh(t)
	target := filepath.Join(t.TempDir(), "restored")
	if err := restore(ctx, fresh, target, filepath.Join("testdata", "backup-schema1.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if v, err := fresh.SchemaVersion(ctx); err != nil || v != postgres.LatestSchemaVersion() {
		t.Fatalf("restored at schema %d (%v)", v, err)
	}
	creds, err := openCredentials(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	demo, err := parseDemo(must(creds.read(demoFile)))
	if err != nil {
		t.Fatal(err)
	}
	p, digest, err := fresh.DemoPrincipalByLogin(ctx, demo.Login)
	if err != nil || p.ID != principal || p.WorkspaceID != workspace || !bytes.Equal(digest, demoDigest(demo)) {
		t.Fatalf("the demo sign-in after restore: %+v %v", p, err)
	}
	if _, _, err := serving(creds); err != nil {
		t.Fatalf("the restored TLS pair and session key: %v", err)
	}
	read, err := fresh.ViewReceipt(ctx, postgres.Viewer{WorkspaceID: workspace, PrincipalID: principal}, childRead)
	if err != nil || read.Status != "ok" || len(read.Rows) != 1 || read.Rows[0] != "crm/accounts/acc-003" {
		t.Fatalf("the child read after restore: %+v %v", read, err)
	}
	refused, err := fresh.ReceiptByOperation(ctx, workspace, "refused")
	if err != nil || string(refused.Arguments) != `{"a": 2, "b": 1}` || *refused.HTTPStatus != 403 {
		t.Fatalf("the refused call after restore: %s %v", refused.Arguments, err)
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

// Setup takes the app role's name and password from GTM_DATABASE_URL: it makes the role with that password,
// and run again with another password in the URL, it sets the role's password to that one. The old password
// is then refused.
func TestSetupSetsTheAppRolePasswordFromTheURL(t *testing.T) {
	ownerURL := pgtest.URL(t)
	suffix := make([]byte, 6)
	rand.Read(suffix)
	role := "gtm_app_" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		if conn, err := pgx.Connect(context.Background(), ownerURL); err == nil {
			defer conn.Close(context.Background())
			conn.Exec(context.Background(), "DROP OWNED BY "+role)
			conn.Exec(context.Background(), "DROP ROLE "+role)
		}
	})
	appURL := func(password string) string {
		parsed, err := url.Parse(ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.UserPassword(role, password)
		return parsed.String()
	}
	opens := func(password string) error {
		store, err := openAppStore(ctx, config{databaseURL: appURL(password)})
		if err == nil {
			store.Close()
		}
		return err
	}
	cfg := config{ownerDatabaseURL: ownerURL, credentialsDir: privateDir(t), allowedHosts: []string{"127.0.0.1:8443"}}
	for _, password := range []string{"first-app-password-for-this-test", "second-app-password-for-this-test"} {
		cfg.databaseURL = appURL(password)
		if err := setUp(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if err := opens(password); err != nil {
			t.Fatalf("after setup, the app role does not open with its URL: %v", err)
		}
	}
	if err := opens("first-app-password-for-this-test"); err == nil || !strings.Contains(err.Error(), "(28P01)") {
		t.Fatalf("the old password: %v", err)
	}
	// A URL that gives no password, with no password file or PGPASSWORD to give one, is refused.
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
	cfg.databaseURL = strings.Replace(appURL("x"), ":x@", "@", 1)
	if err := setUp(ctx, cfg); err == nil || err.Error() != "the app role needs a password" {
		t.Fatalf("a URL with no password: %v", err)
	}
}

// Setup and restore refuse a database that is not encoded as UTF-8, with one line, and install no schema in it.
func TestSetupRefusesADatabaseNotInUTF8(t *testing.T) {
	url := pgtest.URLEncoded(t, "SQL_ASCII")
	cfg := config{ownerDatabaseURL: url, databaseURL: url, credentialsDir: privateDir(t),
		allowedHosts: []string{"127.0.0.1:8443"}}
	err := setUp(ctx, cfg)
	want := "the database is encoded as SQL_ASCII; create it with ENCODING 'UTF8' TEMPLATE template0"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.SchemaVersion(ctx); err != nil || version != 0 {
		t.Fatalf("schema %d after the refusal (%v)", version, err)
	}
	// A restore refuses it too.
	_, archive := backedUp(t)
	if err := restore(ctx, store, privateDir(t), archive); err == nil || err.Error() != want {
		t.Fatalf("restore: got %v, want %s", err, want)
	}
	if version, err := store.SchemaVersion(ctx); err != nil || version != 0 {
		t.Fatalf("schema %d after the restore was refused (%v)", version, err)
	}
}
