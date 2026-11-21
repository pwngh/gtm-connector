package salesforce

import (
	"slices"
	"strings"
	"testing"
)

// sf is given none of the connector's settings or credentials: not a token handed to the connector, its
// file or its address, not the database URL, and not libpq's password. It gets the rest, with its own
// telemetry, updates and log files off. The command is only built here; no sf runs.
func TestSFIsGivenNoCredentialOfTheConnector(t *testing.T) {
	for name, value := range map[string]string{
		TokenVariable: "invented-handed-token-0123456789", TokenFileVariable: "/run/salesforce/token",
		InstanceVariable: "https://invented-books.my.salesforce.com", "GTM_DATABASE_URL": "postgres://invented",
		"PGPASSWORD": "invented-password", "SLACK_TOKEN": "invented-slack-token-0123456789",
		"HTTPS_PROXY": "http://invented-proxy:3128",
	} {
		t.Setenv(name, value)
	}
	command := Exec{Path: "/invented/sf"}.command([]string{"org", "display", "--target-org", "invented", "--json"})
	environment := strings.Join(command.Env, "\n")
	for _, secret := range []string{"invented-handed-token", "/run/salesforce", "invented-books.my", "postgres://",
		"invented-password", "invented-slack-token", "SALESFORCE_", "GTM_", "PGPASSWORD"} {
		if strings.Contains(environment, secret) {
			t.Errorf("sf is given %q", secret)
		}
	}
	for _, kept := range []string{"HTTPS_PROXY=http://invented-proxy:3128", "SF_DISABLE_TELEMETRY=true",
		"SFDX_DISABLE_LOG_FILE=true"} {
		if !slices.Contains(command.Env, kept) {
			t.Errorf("sf is not given %q", kept)
		}
	}
	if command.Path != "/invented/sf" || len(command.Args) != 6 {
		t.Errorf("the command %q %q", command.Path, command.Args)
	}
}
