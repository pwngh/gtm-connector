package web

import (
	"bytes"
	"os"
	"testing"
)

// A build empties dist and copies public/ into it. The file Git keeps in
// dist must come back unchanged, or a build would leave the checkout
// modified and a fresh checkout without it would not compile.
func TestABuildWritesTheKeptFileBack(t *testing.T) {
	kept, err := os.ReadFile("dist/.gitkeep")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("public/.gitkeep")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, source) {
		t.Error("public/.gitkeep and dist/.gitkeep differ")
	}
}
