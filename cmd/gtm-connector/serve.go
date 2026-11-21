package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
	"github.com/pwngh/gtm-connector/web"
)

// openAppStore connects as the app role, as serve and demo do. It refuses a
// role that owns the tables, a database setup has not migrated, and one
// whose restore has not installed its credentials.
func openAppStore(ctx context.Context, c config) (*postgres.Store, error) {
	if c.databaseURL == "" {
		return nil, errors.New("set GTM_DATABASE_URL to the app role's PostgreSQL URL")
	}
	store, err := openStore(ctx, c.databaseURL)
	if err != nil {
		return nil, err
	}
	check := func() error {
		if err := store.CheckAppRole(ctx); err != nil {
			return err
		}
		if version, err := store.SchemaVersion(ctx); err != nil {
			return err
		} else if version != postgres.LatestSchemaVersion() {
			return fmt.Errorf("the database is at schema %d, not %d; run setup first", version,
				postgres.LatestSchemaVersion())
		}
		if pending, err := store.PendingRestore(ctx); err != nil {
			return err
		} else if pending != "" {
			return errors.New("a restore has not installed its credentials; run restore again with the same backup")
		}
		return nil
	}
	if err := check(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// serve runs the HTTPS server as the app role. It refuses to start before
// setup has run, while a restore is unfinished, or as a role that owns the
// tables. With --selection it reads the selection file first, before the
// database or any credential, and never reads it again.
func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	selectionFile := flags.String("selection", "", "a selection `file`: MCP clients read only its accounts, and get the two brief tools")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	var selection cards.Selection
	if *selectionFile != "" {
		if selection, err = loadSelection(*selectionFile); err != nil {
			return err
		}
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := openAppStore(ctx, c)
	if err != nil {
		return err
	}
	defer store.Close()
	creds, err := openCredentials(c.credentialsDir, false)
	if err != nil {
		return fmt.Errorf("%w (run setup first)", err)
	}
	pair, key, err := serving(creds)
	creds.Close()
	if err != nil {
		return err
	}
	app := web.App()
	surface, err := newSurface(ctx, store, c, key, selection)
	if err != nil {
		return err
	}
	defer surface.Close()

	listener, err := net.Listen("tcp", c.listen)
	if err != nil {
		return err
	}
	server := newServer(surface, pair, logger)
	go housekeeping(ctx, store, logger)
	served := make(chan error, 1)
	go func() { served <- server.ServeTLS(listener, "", "") }()
	from := c.webDir
	switch {
	case from != "":
	case app != nil:
		from = "the build inside the binary"
	default:
		from = "none (API only)"
	}
	logger.Info("serving", "origin", c.publicOrigin, "listen", c.listen, "web", from,
		"mcp", c.publicOrigin+"/mcp", "selected_accounts", len(selection), "credentials", c.credentialsDir)
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger.Info("stopping")
	return server.Shutdown(shutdown)
}

// newSurface is everything serve answers: the browser surface, with the
// MCP gateway mounted at /mcp.
func newSurface(ctx context.Context, store *postgres.Store, c config, sessionKey []byte, selection cards.Selection) (*httptransport.Server, error) {
	gateway, err := mcpGateway(ctx, store, c.publicOrigin, selection)
	if err != nil {
		return nil, err
	}
	return httptransport.New(httptransport.Config{
		PublicOrigin: c.publicOrigin,
		AllowedHosts: c.allowedHosts,
		Web:          web.App(),
		WebDir:       c.webDir,
		SessionKey:   sessionKey,
		MCP:          gateway,
	}, store, authz.Evidence{Store: store})
}

// serving reads the TLS pair and the session key.
func serving(creds *credentials) (tls.Certificate, []byte, error) {
	certPEM, err := creds.read(tlsCertFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM, err := creds.read(tlsKeyFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("TLS key pair: %w", err)
	}
	key, err := creds.sessionKey()
	return pair, key, err
}

// newServer is the HTTPS server. Go's own answer to "OPTIONS *" is off, so
// that request meets the Host check like any other.
func newServer(handler http.Handler, pair tls.Certificate, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		TLSConfig:                    &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}},
		ReadHeaderTimeout:            10 * time.Second,
		ReadTimeout:                  30 * time.Second,
		WriteTimeout:                 60 * time.Second,
		IdleTimeout:                  2 * time.Minute,
		MaxHeaderBytes:               64 << 10,
		ErrorLog:                     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// housekeeping finishes receipts whose worker's lease expired and removes
// ended sessions, once at start and then every 30 seconds.
func housekeeping(ctx context.Context, store *postgres.Store, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if n, err := store.AbandonExpired(ctx, 100); err != nil && ctx.Err() == nil {
			logger.Warn("finishing interrupted receipts", "error", err)
		} else if n > 0 {
			logger.Info("finished interrupted receipts", "count", n)
		}
		if _, err := store.DeleteEndedSessions(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("removing ended sessions", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// health asks the local server's /healthz over TLS, trusting only this
// install's certificate, with the public origin's Host.
func health(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	creds, err := openCredentials(c.credentialsDir, false)
	if err != nil {
		return err
	}
	certPEM, err := creds.read(tlsCertFile)
	creds.Close()
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		return errors.New("the TLS certificate cannot be read")
	}
	_, port, _ := net.SplitHostPort(c.listen)
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13},
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	if origin, err := urlHost(c.publicOrigin); err == nil {
		request.Host = origin
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %d: %s", response.StatusCode, bytes.TrimSpace(body))
	}
	fmt.Printf("%s", body)
	return nil
}
