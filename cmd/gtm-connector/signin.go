package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/oidc"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// How people sign in to the web app that serve serves: through their company's identity provider when
// GTM_OIDC_ISSUER is set, and with the demo sign-in unless GTM_DEMO_SIGN_IN is off. The client secret, for
// a confidential client, is the file oidc-client-secret in the credentials folder, never an environment
// variable; without it the client is public. Every setting is checked before serve listens, and a setting
// that is refused is never printed back: an issuer can name a company's tenant.

// oidcSecretFile is the confidential client's secret, in the credentials folder. A backup leaves it out.
const oidcSecretFile = "oidc-client-secret"

// The lifetime of a session of a person who signs in through the provider, in minutes: the default, and
// the least and the most GTM_OIDC_SESSION_MINUTES may set.
const (
	oidcSessionMinutes      = 60
	oidcSessionMinutesLeast = 5
	oidcSessionMinutesMost  = 720
)

// signInSettings are the sign-in settings from the environment.
type signInSettings struct {
	demoOff bool
	// issuer is "" when sign-in through a provider is off; the rest are its settings.
	issuer, clientID, redirectURL, workspace string
	domains                                  []string
	tenantClaim, tenant                      string
	sessionTTL                               time.Duration
}

// oidcVariables are the environment variables of sign-in through a provider, each read only with
// GTM_OIDC_ISSUER.
var oidcVariables = []string{"GTM_OIDC_CLIENT_ID", "GTM_OIDC_REDIRECT_URL", "GTM_OIDC_WORKSPACE",
	"GTM_OIDC_ALLOWED_DOMAINS", "GTM_OIDC_TENANT_CLAIM", "GTM_OIDC_TENANT", "GTM_OIDC_SESSION_MINUTES"}

// loadSignIn reads the sign-in settings and refuses, in one line, any that cannot be served.
func loadSignIn(c config) (signInSettings, error) {
	var s signInSettings
	switch demo := os.Getenv("GTM_DEMO_SIGN_IN"); demo {
	case "", "on":
	case "off":
		s.demoOff = true
	default:
		return s, errors.New("GTM_DEMO_SIGN_IN is on or off")
	}
	s.issuer = os.Getenv("GTM_OIDC_ISSUER")
	if s.issuer == "" {
		for _, name := range oidcVariables {
			if os.Getenv(name) != "" {
				return s, fmt.Errorf("%s is set, and GTM_OIDC_ISSUER is not; set it to sign in through the identity provider", name)
			}
		}
		return s, nil
	}
	s.clientID = os.Getenv("GTM_OIDC_CLIENT_ID")
	s.workspace = os.Getenv("GTM_OIDC_WORKSPACE")
	s.redirectURL = envOr("GTM_OIDC_REDIRECT_URL", c.publicOrigin+httptransport.OIDCCallbackPath)
	for _, domain := range strings.Split(os.Getenv("GTM_OIDC_ALLOWED_DOMAINS"), ",") {
		if domain = strings.TrimSpace(domain); domain != "" {
			s.domains = append(s.domains, domain)
		}
	}
	s.tenantClaim, s.tenant = os.Getenv("GTM_OIDC_TENANT_CLAIM"), os.Getenv("GTM_OIDC_TENANT")
	minutes := oidcSessionMinutes
	if text := os.Getenv("GTM_OIDC_SESSION_MINUTES"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < oidcSessionMinutesLeast || n > oidcSessionMinutesMost {
			return s, fmt.Errorf("GTM_OIDC_SESSION_MINUTES is a whole number of minutes from %d to %d",
				oidcSessionMinutesLeast, oidcSessionMinutesMost)
		}
		minutes = n
	}
	s.sessionTTL = time.Duration(minutes) * time.Minute
	switch {
	case s.clientID == "":
		return s, errors.New("GTM_OIDC_ISSUER is set, and GTM_OIDC_CLIENT_ID is not")
	case s.workspace == "":
		return s, errors.New("GTM_OIDC_ISSUER is set, and GTM_OIDC_WORKSPACE, the workspace its people sign in to, is not")
	case s.redirectURL != c.publicOrigin+httptransport.OIDCCallbackPath:
		return s, fmt.Errorf("GTM_OIDC_REDIRECT_URL must be GTM_PUBLIC_ORIGIN followed by %s, as the provider has it registered",
			httptransport.OIDCCallbackPath)
	}
	// The rest of the provider's settings are checked as the client is made, with no secret yet.
	if _, err := s.client("", nil); err != nil {
		return s, err
	}
	return s, nil
}

// client is the provider's client under these settings.
func (s signInSettings) client(secret string, transport http.RoundTripper) (*oidc.Client, error) {
	client, err := oidc.New(oidc.Config{Issuer: s.issuer, ClientID: s.clientID, ClientSecret: secret,
		RedirectURL: s.redirectURL, AllowedDomains: s.domains, TenantClaim: s.tenantClaim, Tenant: s.tenant,
		Transport: transport})
	if err != nil {
		return nil, fmt.Errorf("sign-in through the identity provider: %w", err)
	}
	return client, nil
}

// secret reads the confidential client's secret from the credentials folder, or "" for a public client.
func (s signInSettings) secret(creds *credentials) (string, error) {
	if s.issuer == "" {
		return "", nil
	}
	raw, err := creds.read(oidcSecretFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("the credential %s is empty; remove it for a public client", oidcSecretFile)
	}
	return secret, nil
}

// signInSurface is how serve's web app signs people in.
type signInSurface struct {
	oidc    *httptransport.OIDC
	demoOff bool
}

// open finds the provider's workspace and makes its client. The demo sign-in is refused beside a provider
// that signs in a live workspace: the demo sign-in is for the demo workspace alone.
func (s signInSettings) open(ctx context.Context, store *postgres.Store, secret string,
	transport http.RoundTripper) (signInSurface, error) {
	surface := signInSurface{demoOff: s.demoOff}
	if s.issuer == "" {
		return surface, nil
	}
	workspace, err := store.Workspace(ctx, workspaceSlug(s.workspace))
	if errors.Is(err, postgres.ErrNotFound) {
		return surface, errors.New("GTM_OIDC_WORKSPACE names no workspace; make it first, as connect --state-dir NAME does")
	}
	if err != nil {
		return surface, err
	}
	if workspace.Kind != "demo" && !s.demoOff {
		return surface, fmt.Errorf("the demo sign-in is on beside sign-in through the identity provider for the live "+
			"workspace %s; set GTM_DEMO_SIGN_IN=off", workspace.Slug)
	}
	client, err := s.client(secret, transport)
	if err != nil {
		return surface, err
	}
	surface.oidc = &httptransport.OIDC{Client: client, Workspace: workspace, SessionTTL: s.sessionTTL}
	return surface, nil
}

// described is how serve's start line names the sign-ins it offers.
func (s signInSurface) described() string {
	var ways []string
	if s.oidc != nil {
		kind := "public"
		if s.oidc.Client.Confidential() {
			kind = "confidential"
		}
		ways = append(ways, "identity provider ("+kind+" client) for "+s.oidc.Workspace.Slug)
	}
	if !s.demoOff {
		ways = append(ways, "demo")
	}
	if len(ways) == 0 {
		return "none"
	}
	return strings.Join(ways, ", ")
}
