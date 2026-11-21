// Package childenv is what a program the connector starts is given of the connector's environment. A child,
// such as sf or the desktop's browser opener, never needs the connector's own settings or credentials, so
// none of them passes.
package childenv

import "strings"

// secret is whether a variable names the connector's settings or a credential: GTM_* (the database URLs
// and the connector's settings), PG* (libpq's host, user, password and password file), the Salesforce
// token, its file and its instance address, and any *_TOKEN or *_TOKEN_FILE.
func secret(name string) bool {
	switch {
	case strings.HasPrefix(name, "GTM_"), strings.HasPrefix(name, "PG"),
		strings.HasSuffix(name, "_TOKEN"), strings.HasSuffix(name, "_TOKEN_FILE"):
		return true
	}
	return name == "SALESFORCE_INSTANCE_URL"
}

// WithoutSecrets is environ, each entry NAME=VALUE, without the variables that name the connector's
// settings or a credential. Everything else passes in its order.
func WithoutSecrets(environ []string) []string {
	kept := []string{}
	for _, entry := range environ {
		if name, _, _ := strings.Cut(entry, "="); !secret(name) {
			kept = append(kept, entry)
		}
	}
	return kept
}
