package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/oracletest"
	"github.com/pwngh/gtm-connector/internal/render"
)

// The guide link every message carries opens a section of docs/deals.md, and each rule's name is the name
// that section's table gives its flag.
func TestTheGuideLinkAndTheFlagNamesAreTheGuides(t *testing.T) {
	root := filepath.Dir(filepath.Dir(filepath.Dir(oracletest.Path(t, "manifest.json"))))
	guide, err := os.ReadFile(filepath.Join(root, "docs", "deals.md"))
	if err != nil {
		t.Fatal(err)
	}
	link := render.Facts{Revision: "main"}.GuideLink()
	section, found := sectionOf(string(guide), "What flags a deal")
	if !strings.HasSuffix(link, "/docs/deals.md#what-flags-a-deal") || !found {
		t.Fatalf("the guide link %s opens no section of docs/deals.md", link)
	}
	for _, code := range render.RuleCodes {
		if name := render.RuleName(code); !strings.Contains(section, "\n| "+name+" | ") {
			t.Errorf("%s: %q is not a flag of the guide's table", code, name)
		}
	}
}

// sectionOf is the text of a guide under one second-level heading, up to the next one.
func sectionOf(guide, heading string) (string, bool) {
	_, after, found := strings.Cut(guide, "\n## "+heading+"\n")
	// The heading's own line end starts the section, so a heading right after it ends the section at once.
	section, _, _ := strings.Cut("\n"+after, "\n## ")
	return section, found
}

// A section ends at the next heading, also one that follows its own heading at once.
func TestASectionEndsAtTheNextHeading(t *testing.T) {
	guide := "# Guide\n\n## What flags a deal\n## The card\n\n| Customer silence | row |\n"
	if section, found := sectionOf(guide, "What flags a deal"); !found || strings.Contains(section, "Customer silence") {
		t.Errorf("the section holds a row of the next one: %q", section)
	}
	if section, _ := sectionOf(guide, "The card"); !strings.Contains(section, "\n| Customer silence | ") {
		t.Errorf("the section lost its own row: %q", section)
	}
}
