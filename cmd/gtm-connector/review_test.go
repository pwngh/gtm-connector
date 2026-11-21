package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// edit copies an archive, changing files and recomputing every digest in
// the manifest, as anyone who can write the file can.
func edit(t *testing.T, from string, change func(files map[string][]byte)) string {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	zr, _ := gzip.NewReader(in)
	tr := tar.NewReader(zr)
	files := map[string][]byte{}
	var order []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		files[h.Name] = body
		order = append(order, h.Name)
	}
	change(files)
	var m manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	sum := func(entry *fileEntry) {
		body := files[entry.Name]
		digest := sha256.Sum256(body)
		entry.Size, entry.SHA256 = int64(len(body)), hex.EncodeToString(digest[:])
	}
	for i := range m.Tables {
		sum(&m.Tables[i].File)
		m.Tables[i].Rows = int64(bytes.Count(files[m.Tables[i].File.Name], []byte("\n")))
	}
	for i := range m.Credentials {
		sum(&m.Credentials[i])
	}
	files["manifest.json"], _ = json.MarshalIndent(m, "", "  ")
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, name := range order {
		if _, kept := files[name]; !kept {
			continue
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(files[name])), Typeflag: tar.TypeReg})
		tw.Write(files[name])
	}
	tw.Close()
	gz.Close()
	to := filepath.Join(t.TempDir(), "edited.tar.gz")
	os.WriteFile(to, out.Bytes(), 0o600)
	return to
}

func backedUp(t *testing.T) (*postgrestest.Fixture, string) {
	t.Helper()
	f := postgrestest.New(t)
	if _, err := f.Store.StartReceipt(ctx, postgres.ReceiptStart{WorkspaceID: f.Workspace.ID,
		OperationID: "never-finished", TokenID: "anonymous", Tool: "save_brief", AccountRef: postgrestest.Account,
		BriefID: "brief-1", Arguments: json.RawMessage(`{}`), Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	creds, err := openCredentials(privateDir(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts([]string{"127.0.0.1:8443"})); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup(ctx, f.Store, creds, archive); err != nil {
		t.Fatal(err)
	}
	return f, archive
}

// Item 3: an edited archive with recomputed digests is refused when it holds
// rows the write path would refuse.
func TestRestoreRefusesEditedArchives(t *testing.T) {
	f, archive := backedUp(t)
	for name, change := range map[string]func(map[string][]byte){
		"a seal pointed at a pending receipt": func(files map[string][]byte) {
			pending, err := f.Store.ReceiptByOperation(ctx, f.Workspace.ID, "never-finished")
			if err != nil {
				t.Fatal(err)
			}
			files["tables/cards.copy"] = bytes.ReplaceAll(files["tables/cards.copy"], []byte(f.Seal.ID), []byte(pending.ID))
		},
		"changed unit text": func(files map[string][]byte) {
			files["tables/evidence_units.copy"] = bytes.ReplaceAll(files["tables/evidence_units.copy"],
				[]byte("We agree."), []byte("We refuse."))
		},
		"a missing credential": func(files map[string][]byte) { delete(files, "credentials/session-key") },
		"an extra table entry": func(files map[string][]byte) {
			var m manifest
			json.Unmarshal(files["manifest.json"], &m)
			m.Tables = append(m.Tables, tableEntry{Table: "extra", File: fileEntry{Name: "tables/extra.copy"}})
			files["manifest.json"], _ = json.Marshal(m)
			files["tables/extra.copy"] = nil
		},
		"a broken TLS key": func(files map[string][]byte) { files["credentials/tls.key"] = []byte("not a key") },
	} {
		t.Run(name, func(t *testing.T) {
			edited := edit(t, archive, change)
			fresh := openFresh(t)
			target := filepath.Join(t.TempDir(), "creds")
			if err := restore(ctx, fresh, target, edited); err == nil {
				t.Fatal("restored")
			}
			if v, _ := fresh.SchemaVersion(ctx); v != 0 {
				t.Fatal("a refused restore left a schema")
			}
			if entries, _ := os.ReadDir(target); len(entries) != 0 {
				t.Fatalf("a refused restore left %d files in the credentials folder", len(entries))
			}
		})
	}
}

// Item 3: a restore stopped after the database commits can be finished,
// and nothing else starts in between.
func TestAnInterruptedRestoreResumes(t *testing.T) {
	f, archive := backedUp(t)
	fresh := openFresh(t)
	target := filepath.Join(t.TempDir(), "creds")
	installHook = func(name string) error {
		if name == tlsKeyFile {
			return errors.New("disk full")
		}
		return nil
	}
	err := restore(ctx, fresh, target, archive)
	installHook = nil
	if err == nil || !strings.Contains(err.Error(), "run restore again") {
		t.Fatalf("first attempt: %v", err)
	}
	if pending, _ := fresh.PendingRestore(ctx); pending == "" {
		t.Fatal("the restore was not left pending")
	}
	// Setup refuses while a restore is unfinished.
	url := freshURL(t, fresh)
	cfg := config{ownerDatabaseURL: url, databaseURL: url, credentialsDir: target,
		allowedHosts: []string{"127.0.0.1:8443"}}
	if err := setUp(ctx, cfg); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("setup ran during a restore: %v", err)
	}
	// Another backup cannot slip in either.
	_, another := backedUp(t)
	if err := restore(ctx, fresh, target, another); err == nil {
		t.Fatal("a second backup was restored over the first")
	}
	if err := restore(ctx, fresh, target, archive); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if pending, _ := fresh.PendingRestore(ctx); pending != "" {
		t.Fatal("still pending")
	}
	card, err := fresh.Card(ctx, f.Workspace.ID, f.Card.ID)
	if err != nil || !bytes.Equal(card.Bytes, f.Card.Bytes) {
		t.Fatal("the card did not survive")
	}
	creds, err := openCredentials(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	for _, name := range credentialFiles {
		if _, err := creds.read(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// freshURL recovers the URL of a store opened by openFresh.
var freshURLs = map[*postgres.Store]string{}

func freshURL(t *testing.T, s *postgres.Store) string {
	url, ok := freshURLs[s]
	if !ok {
		t.Fatal("not a fresh store")
	}
	return url
}

// Item 9: checks hold for the descriptor actually used.
func TestCredentialsStayAttachedToTheVerifiedFiles(t *testing.T) {
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts(nil)); err != nil {
		t.Fatal(err)
	}
	// A hard link is refused, wherever its other name is.
	other := filepath.Join(filepath.Dir(dir), "copy-of-secret")
	if err := os.Link(filepath.Join(dir, ownerSecretFile), other); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.read(ownerSecretFile); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("a hard-linked secret was read: %v", err)
	}
	os.Remove(other)
	// Swapping the folder's path for a link after opening changes nothing.
	moved := dir + "-moved"
	decoy := privateDir(t)
	os.WriteFile(filepath.Join(decoy, ownerSecretFile), []byte("decoy"), 0o600)
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, dir); err != nil {
		t.Fatal(err)
	}
	secret, err := creds.read(ownerSecretFile)
	if err != nil || string(secret) == "decoy" {
		t.Fatalf("read through the swapped path: %q %v", secret, err)
	}
	if err := creds.write(demoFile+".x", []byte("y")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, demoFile+".x")); err != nil {
		t.Fatal("a write left the verified folder")
	}
	// A backup never replaces a file that appears at its name.
	partial := filepath.Join(t.TempDir(), "partial")
	out := filepath.Join(filepath.Dir(partial), "out")
	os.WriteFile(partial, []byte("new"), 0o600)
	os.WriteFile(out, []byte("existing"), 0o600)
	if err := publish(partial, out); err == nil {
		t.Fatal("publish replaced a file")
	}
	if data, _ := os.ReadFile(out); string(data) != "existing" {
		t.Fatal("the existing file changed")
	}
}

// Item 11: the host server listens on loopback only.
func TestServeListensOnLoopbackOnly(t *testing.T) {
	for listen, ok := range map[string]bool{"127.0.0.1:8443": true, "[::1]:8443": true, "localhost:8443": true,
		"0.0.0.0:8443": false, ":8443": false, "192.168.1.5:8443": false, "[::]:8443": false} {
		if _, err := listenAddress(listen, ""); (err == nil) != ok {
			t.Errorf("%s: %v", listen, err)
		}
	}
	if got, err := listenAddress("", "0.0.0.0:8443"); err != nil || got != "0.0.0.0:8443" {
		t.Errorf("the container listener: %q %v", got, err)
	}
}

// Item 12: "OPTIONS *" reaches the Host check.
func TestOptionsStarMeetsTheHostCheck(t *testing.T) {
	dir := privateDir(t)
	creds, _ := openCredentials(dir, false)
	defer creds.Close()
	if _, err := creds.setUp(certificateHosts(nil)); err != nil {
		t.Fatal(err)
	}
	pair, key, err := serving(creds)
	if err != nil {
		t.Fatal(err)
	}
	surface, err := httptransport.New(httptransport.Config{PublicOrigin: "https://127.0.0.1:8443",
		AllowedHosts: []string{"127.0.0.1:8443"}, SessionKey: key}, schema{current: true}, authz.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(surface, pair, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go server.ServeTLS(listener, "", "")
	defer server.Close()
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("OPTIONS * HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n"))
	reply, _ := io.ReadAll(conn)
	if !bytes.HasPrefix(reply, []byte("HTTP/1.1 421")) {
		t.Fatalf("OPTIONS * with a foreign Host: %q", reply[:min(len(reply), 40)])
	}
	_ = http.MethodOptions
}
