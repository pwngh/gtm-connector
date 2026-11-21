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
	if !strings.HasSuffix(link, "/docs/deals.md#what-flags-a-deal") ||
		!strings.Contains(string(guide), "\n## What flags a deal\n") {
		t.Errorf("the guide link %s opens no section of docs/deals.md", link)
	}
	for _, code := range render.RuleCodes {
		if name := render.RuleName(code); !strings.Contains(string(guide), "\n| "+name+" | ") {
			t.Errorf("%s: %q is not a flag of the guide's table", code, name)
		}
	}
}
