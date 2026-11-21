package childenv

import (
	"slices"
	"testing"
)

// The connector's settings and every credential are taken out. Anything else passes in its order.
func TestNoSecretReachesAChild(t *testing.T) {
	given := []string{
		"PATH=/usr/bin", "GTM_DATABASE_URL=postgres://invented", "HOME=/home/invented",
		"PGPASSWORD=invented", "PGPASSFILE=/invented/pgpass", "SALESFORCE_ACCESS_TOKEN=invented-token-0123456789",
		"SALESFORCE_ACCESS_TOKEN_FILE=/run/salesforce/token", "SALESFORCE_INSTANCE_URL=https://invented.my.salesforce.com",
		"SLACK_TOKEN=invented", "GITHUB_TOKEN_FILE=/invented", "SF_DISABLE_TELEMETRY=true", "HTTPS_PROXY=http://proxy",
		"TOKEN=kept-as-it-names-no-credential-kind", "LANG=C.UTF-8",
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/invented", "SF_DISABLE_TELEMETRY=true", "HTTPS_PROXY=http://proxy",
		"TOKEN=kept-as-it-names-no-credential-kind", "LANG=C.UTF-8"}
	if got := WithoutSecrets(given); !slices.Equal(got, want) {
		t.Fatalf("kept %q", got)
	}
}
