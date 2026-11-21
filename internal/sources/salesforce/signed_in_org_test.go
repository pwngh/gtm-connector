package salesforce

import (
	"context"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
)

// sf org display names one org for the alias and sf org auth show-access-token gives a token. When that
// token opens another org, as it can when the alias changes between the two commands, the first read is
// refused and the org is not taken as this reader's.
func TestATokenForAnotherOrgThanTheAliasNamesIsRefused(t *testing.T) {
	org, sf := salesforcetest.NewOrg(), salesforcetest.NewSF()
	org.ID = "00D000000000000BBB"
	reader, err := FromSF(context.Background(), salesforcetest.Alias, Options{Environment: "production", Runner: sf,
		Transport: org})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PrepareConnection(context.Background()); codeOf(err) != authRequired {
		t.Fatal(err)
	}
	if reader.organizationID != "" || reader.editionVerified {
		t.Fatal("the other org was taken as this reader's")
	}
}
