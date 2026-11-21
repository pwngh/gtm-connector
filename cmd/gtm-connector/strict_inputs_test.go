package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withManifest copies an archive with its manifest's bytes changed and every other entry as it was.
func withManifest(t *testing.T, from string, change func(manifest string) string) string {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	unzipped, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zipped := gzip.NewWriter(&out)
	archive := tar.NewWriter(zipped)
	for entries := tar.NewReader(unzipped); ; {
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
		if header.Name == "manifest.json" {
			changed := change(string(body))
			if changed == string(body) {
				t.Fatal("the manifest was not changed")
			}
			body = []byte(changed)
		}
		archive.WriteHeader(&tar.Header{Name: header.Name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
		archive.Write(body)
	}
	archive.Close()
	zipped.Close()
	to := filepath.Join(t.TempDir(), "edited.tar.gz")
	if err := os.WriteFile(to, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return to
}

// refusedWhole restores an archive into a fresh database and folder, and requires that it is refused and
// leaves neither a schema nor a credential behind.
func refusedWhole(t *testing.T, archive string) {
	t.Helper()
	fresh := openFresh(t)
	target := filepath.Join(t.TempDir(), "creds")
	if err := restore(ctx, fresh, target, archive); err == nil {
		t.Fatal("restored")
	}
	if v, _ := fresh.SchemaVersion(ctx); v != 0 {
		t.Fatal("a refused restore left a schema")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("a refused restore left %d files in the credentials folder", len(entries))
	}
}

// A backup's manifest and its demo credential are read as the strict reader reads them: a field named
// twice, a field the format does not have, and a field spelled in another case are each refused, where
// each was once read by the last value, skipped, or matched whatever its case. A credential larger than
// one is read is refused from the manifest, before it is unpacked into memory.
func TestRestoreReadsItsJSONStrictly(t *testing.T) {
	_, archive := backedUp(t)
	const password = "0123456789abcdef0123456789abcdef"
	for name, change := range map[string]func(string) string{
		"a version named twice": func(m string) string {
			return strings.Replace(m, `"version": 1,`, `"version": 2, "version": 1,`, 1)
		},
		"a field the format does not have": func(m string) string {
			return strings.Replace(m, `"version": 1,`, `"version": 1, "note": "x",`, 1)
		},
		"a field in another case":   func(m string) string { return strings.Replace(m, `"format"`, `"Format"`, 1) },
		"a version with a fraction": func(m string) string { return strings.Replace(m, `"version": 1,`, `"version": 1.0,`, 1) },
		"a manifest nested past the limit": func(m string) string {
			return strings.Replace(m, `"version": 1,`,
				`"version": 1, "note": `+strings.Repeat("[", 1001)+strings.Repeat("]", 1001)+`,`, 1)
		},
		"a manifest over its size": func(m string) string { return m + strings.Repeat(" ", maxManifestBytes) },
	} {
		t.Run(name, func(t *testing.T) { refusedWhole(t, withManifest(t, archive, change)) })
	}
	for name, credential := range map[string]map[string][]byte{
		"a password named twice": {"credentials/" + demoFile: []byte(
			`{"login":"demo-reviewer","password":"` + password + `","password":"` + password + `"}`)},
		"a field a demo credential does not have": {"credentials/" + demoFile: []byte(
			`{"login":"demo-reviewer","password":"` + password + `","role":"owner"}`)},
		"a login in another case": {"credentials/" + demoFile: []byte(
			`{"Login":"demo-reviewer","password":"` + password + `"}`)},
		"a secret larger than a credential": {"credentials/" + ownerSecretFile: bytes.Repeat([]byte("a"),
			maxCredentialBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			refusedWhole(t, edit(t, archive, func(files map[string][]byte) {
				for file, body := range credential {
					files[file] = body
				}
			}))
		})
	}
	// The same archive, unchanged, restores.
	if err := restore(ctx, openFresh(t), filepath.Join(t.TempDir(), "creds"), archive); err != nil {
		t.Fatal(err)
	}
}

// A credential file over its size is refused before it is read, and the demo credential is read strictly
// from the folder too.
func TestCredentialsAreReadWithinTheirSize(t *testing.T) {
	dir := privateDir(t)
	creds, err := openCredentials(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	hosts := certificateHosts([]string{"127.0.0.1:8443"})
	demo, err := creds.setUp(hosts)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ownerSecretFile, bytes.Repeat([]byte("a"), maxCredentialBytes))
	if data, err := creds.read(ownerSecretFile); err != nil || len(data) != maxCredentialBytes {
		t.Fatalf("a secret at the limit: %d bytes, %v", len(data), err)
	}
	write(ownerSecretFile, bytes.Repeat([]byte("a"), maxCredentialBytes+1))
	if _, err := creds.read(ownerSecretFile); err == nil || !strings.Contains(err.Error(), "larger than a credential file") {
		t.Fatalf("a secret over the limit: %v", err)
	}
	write(ownerSecretFile, []byte(demo.Password))
	for name, body := range map[string]string{
		"a password named twice":   `{"login":"demo-reviewer","password":"` + demo.Password + `","password":"` + demo.Password + `"}`,
		"a field it does not have": `{"login":"demo-reviewer","password":"` + demo.Password + `","role":"owner"}`,
		"a login in another case":  `{"Login":"demo-reviewer","password":"` + demo.Password + `"}`,
	} {
		write(demoFile, []byte(body))
		if _, err := creds.setUp(hosts); err == nil || !strings.Contains(err.Error(), "not a demo credential") {
			t.Errorf("%s: %v", name, err)
		}
	}
	write(demoFile, []byte("{\n  \"login\": \"demo-reviewer\",\n  \"password\": \""+demo.Password+"\"\n}"))
	if again, err := creds.setUp(hosts); err != nil || again != demo {
		t.Fatalf("the credential as setup writes it: %v", err)
	}
}
