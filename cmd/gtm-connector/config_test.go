package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type configResult struct {
	Revision    string   `json:"revision"`
	Stored      *bool    `json:"stored"`
	Active      *string  `json:"active"`
	CanActivate bool     `json:"can_activate"`
	Changed     *bool    `json:"changed"`
	Receipt     *string  `json:"receipt"`
	File        *string  `json:"file"`
	NotApplied  []string `json:"not_applied"`
	Problems    []string
	Sources     []struct {
		Name, Kind string
		Connected  bool
	}
}

// config preview reads gtm.yaml, stores it as a draft under the SHA-256 of its bytes and says what it would
// set. config activate makes a previewed file the active one by that revision, under an activate_config
// receipt. A file with a setting this build does not apply, or a source the workspace has not connected, is
// previewed and cannot be activated, and a file the reader refuses is not stored.
func TestConfigPreviewAndActivate(t *testing.T) {
	d := setUpDemo(t)
	d.connectDemo(t, defaultWorkspace)
	dir := t.TempDir()
	write := func(name, text string) (path, revision string) {
		path = filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(text))
		return path, hex.EncodeToString(sum[:])
	}
	stored := func() int { return d.count(t, "SELECT count(*) FROM config_revisions") }
	receipts := func(status string) int {
		return d.count(t, "SELECT count(*) FROM receipts WHERE tool = 'activate_config' AND status = $1", status)
	}

	if code, stdout, stderr := d.read(t, "config", "show"); code != 0 || stdout != "No file has been activated. This workspace runs on the defaults.\n" {
		t.Fatalf("config show before any file: exit %d\n%s%s", code, stdout, stderr)
	}
	// A file the reader refuses names its line, and is not stored.
	broken, _ := write("broken.yaml", "sources:\n\tcrm: {kind: demo}\n")
	if _, message, _ := d.refusal(t, 2, "config", "preview", broken); message != "gtm.yaml line 2: a tab; indent with spaces." || stored() != 0 {
		t.Errorf("a refused file: %s, %d stored", message, stored())
	}
	if _, message, _ := d.refusal(t, 2, "config", "preview", filepath.Join(dir, "missing.yaml")); message != "No file at that path." {
		t.Errorf("a missing file: %s", message)
	}
	// A folder and a pipe are no file, and a pipe is not waited on. A link to a file is read.
	if _, message, _ := d.refusal(t, 2, "config", "preview", dir); message != "That path is not a file." {
		t.Errorf("a folder: %s", message)
	}
	pipe := filepath.Join(dir, "pipe.yaml")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, message, _ := d.refusal(t, 2, "config", "preview", pipe); message != "That path is not a file." {
		t.Errorf("a pipe: %s", message)
	}
	// A refusal repeats nothing of the file.
	pasted, _ := write("pasted.yaml", "sources:\n  crm: {kind: sk-invented-secret-123}\n")
	if code, stdout, stderr := d.read(t, "config", "preview", pasted); code != 2 || strings.Contains(stdout+stderr, "sk-invented") {
		t.Errorf("a file with a pasted value: exit %d\n%s%s", code, stdout, stderr)
	}
	if stored() != 0 {
		t.Errorf("%d revisions stored from refused files", stored())
	}

	path, revision := write("gtm.yaml", "# the demo book\nsources:\n  crm: {kind: demo}\n")
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	var preview configResult
	d.result(t, &preview, "config", "preview", link)
	if preview.Revision != revision {
		t.Errorf("a link to the file: revision %s", preview.Revision)
	}
	d.result(t, &preview, "config", "preview", path)
	preview.Stored = nil
	d.result(t, &preview, "config", "preview", path)
	if preview.Revision != revision || preview.Stored == nil || *preview.Stored || preview.Active != nil || !preview.CanActivate ||
		len(preview.Sources) != 1 || preview.Sources[0].Name != "crm" || !preview.Sources[0].Connected || len(preview.NotApplied) != 0 {
		t.Fatalf("config preview: %+v", preview)
	}
	d.result(t, &preview, "config", "preview", path)
	if *preview.Stored || stored() != 1 {
		t.Errorf("the same file previewed again: stored %v, %d revisions", *preview.Stored, stored())
	}
	code, stdout, _ := d.read(t, "config", "preview", path)
	if code != 0 || !strings.Contains(stdout, "Revision\t"+revision+"\n") || !strings.Contains(stdout, "Source crm\tdemo, connected\n") ||
		!strings.Contains(stdout, "Activate it: gtm-connector config activate "+revision) {
		t.Errorf("config preview as text:\n%s", stdout)
	}

	// Activation: one receipt that names the revision, and the file is the active one.
	var active configResult
	d.result(t, &active, "config", "activate", revision)
	if active.Changed == nil || !*active.Changed || active.Receipt == nil || active.Active == nil || *active.Active != revision {
		t.Fatalf("config activate: %+v", active)
	}
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE id = $1 AND tool = 'activate_config' AND status = 'ok'
		AND principal_id IS NULL AND account_ref IS NULL AND arguments::jsonb = jsonb_build_object('revision', $2::text)`,
		*active.Receipt, revision); n != 1 {
		t.Errorf("the activation's receipt does not name the revision")
	}
	var shown configResult
	d.result(t, &shown, "config", "show")
	if shown.Revision != revision || shown.File == nil || *shown.File != "# the demo book\nsources:\n  crm: {kind: demo}\n" {
		t.Errorf("config show: %+v", shown)
	}
	// Activating the active file again changes nothing and leaves no receipt.
	d.result(t, &active, "config", "activate", revision)
	if *active.Changed || receipts("ok") != 1 {
		t.Errorf("activating the active file: changed %v, %d receipts", *active.Changed, receipts("ok"))
	}

	// A setting this build does not apply: previewed, named, and not activated.
	later, laterRevision := write("later.yaml", "approval: all\nsources:\n  crm: {kind: demo}\n")
	d.result(t, &preview, "config", "preview", later)
	if preview.CanActivate || strings.Join(preview.NotApplied, ",") != "approval" || len(preview.Problems) != 1 {
		t.Errorf("a file with a setting not applied: %+v", preview)
	}
	if _, message, _ := d.refusal(t, 3, "config", "activate", laterRevision); !strings.Contains(message, "this build does not apply: approval") {
		t.Errorf("activating it: %s", message)
	}
	refusedAs := func(revision, code string) int {
		return d.count(t, `SELECT count(*) FROM receipts WHERE tool = 'activate_config' AND status = 'error' AND error = $2
			AND principal_id IS NULL AND account_ref IS NULL AND arguments::jsonb = jsonb_build_object('revision', $1::text)`, revision, code)
	}
	if refusedAs(laterRevision, "setting_not_applied") != 1 {
		t.Errorf("the refused activation of a file with a setting not applied left no receipt of its own")
	}
	// A source the workspace has not connected.
	other, otherRevision := write("other.yaml", "sources:\n  crm: {kind: salesforce}\n")
	d.result(t, &preview, "config", "preview", other)
	if preview.CanActivate || preview.Sources[0].Connected {
		t.Errorf("a file naming a source that is not connected: %+v", preview)
	}
	if _, message, _ := d.refusal(t, 3, "config", "activate", otherRevision); !strings.Contains(message, "sources.crm: this workspace has no salesforce source connected") {
		t.Errorf("activating it: %s", message)
	}
	if refusedAs(otherRevision, "source_not_connected") != 1 {
		t.Errorf("the refused activation of a file naming a source that is not connected left no receipt of its own")
	}
	// A revision the workspace never stored is refused under a receipt, and what is no revision before any.
	unknown := strings.Repeat("a", 64)
	if _, message, _ := d.refusal(t, 2, "config", "activate", unknown); !strings.HasPrefix(message, "This workspace holds no file of that revision.") {
		t.Errorf("an unknown revision: %s", message)
	}
	if refusedAs(unknown, "revision_not_found") != 1 || receipts("error") != 3 {
		t.Errorf("the unknown revision's refusal has %d receipts, of %d refused", refusedAs(unknown, "revision_not_found"), receipts("error"))
	}
	if _, message, _ := d.refusal(t, 2, "config", "activate", "latest"); !strings.Contains(message, "64 characters") || receipts("error") != 3 {
		t.Errorf("what is no revision: %s", message)
	}
	d.result(t, &shown, "config", "show")
	if shown.Revision != revision || receipts("ok") != 1 {
		t.Errorf("after the refusals the active file is %s, with %d activations", shown.Revision, receipts("ok"))
	}

	// A second file takes its place, and history lists both, newest first.
	next, nextRevision := write("next.yaml", "sources:\n  book: {kind: demo}\n")
	d.result(t, &preview, "config", "preview", next)
	if preview.Active == nil || *preview.Active != revision {
		t.Errorf("a preview beside an active file: %+v", preview)
	}
	d.result(t, &active, "config", "activate", nextRevision)
	var history []struct {
		Revision, Receipt string
	}
	d.result(t, &history, "config", "history")
	if len(history) != 2 || history[0].Revision != nextRevision || history[1].Revision != revision {
		t.Errorf("config history: %+v", history)
	}
	code, stdout, _ = d.read(t, "config", "show", revision)
	if code != 0 || !strings.HasPrefix(stdout, "Revision "+revision+" (stored, not active)\n\n# the demo book\n") {
		t.Errorf("config show of an earlier revision:\n%s", stdout)
	}
	// A stored file is shown with anything that does not print written out, also one a writer with SQL put
	// there, which never passed the reader.
	raw := "sources: {}\n# \x1b[31mred\x1b[0m\n"
	sum := sha256.Sum256([]byte(raw))
	if n := d.count(t, `WITH put AS (INSERT INTO config_revisions (workspace_id, revision, body, settings)
		SELECT id, $1, $2, '{}' FROM workspaces WHERE slug = 'demo' RETURNING 1) SELECT count(*) FROM put`,
		hex.EncodeToString(sum[:]), []byte(raw)); n != 1 {
		t.Fatalf("the revision was not put")
	}
	code, stdout, _ = d.read(t, "config", "show", hex.EncodeToString(sum[:]))
	if code != 0 || strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, `# \u001b[31mred\u001b[0m`) {
		t.Errorf("config show of bytes with an escape character:\n%q", stdout)
	}
	d.everyToolIsNamed(t)
}
