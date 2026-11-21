package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// No name a person types selects the workspace a folder path names: a name in the hashed form is hashed
// again, and every other slug names itself.
func TestATypedNameCannotSelectAFoldersWorkspace(t *testing.T) {
	folder := "/invented/folder"
	digest := sha256.Sum256([]byte(folder))
	hashed := "dir-" + hex.EncodeToString(digest[:16])
	if workspaceSlug(folder) != hashed {
		t.Fatalf("the folder names %s", workspaceSlug(folder))
	}
	if workspaceSlug(hashed) == hashed {
		t.Fatal("the hashed slug typed as a name selects the folder's workspace")
	}
	for _, slug := range []string{"demo", "dir-short", "team-a"} {
		if workspaceSlug(slug) != slug {
			t.Fatalf("%s names %s", slug, workspaceSlug(slug))
		}
	}
}

// A workspace name that is a URL is not printed back in the command a refusal suggests.
func TestAURLGivenAsAWorkspaceIsNotPrintedBack(t *testing.T) {
	r := &run{name: "postgres://owner:invented-password@db.example/gtm"}
	if text := r.folder(); strings.Contains(text, "invented-password") || text != " --state-dir NAME" {
		t.Fatal(text)
	}
}

// The name of a member is source text too: a control character in it is shown inert.
func TestNeutralMakesMemberNamesInert(t *testing.T) {
	shown, err := neutral(strictjson.NewObject("account‮id", "acc-001"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range shown.(*strictjson.Object).Keys {
		if strings.ContainsRune(key, '‮') {
			t.Fatalf("%q", key)
		}
	}
}
