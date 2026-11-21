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
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
	"github.com/pwngh/gtm-connector/web"
)

// openAppStore connects as the app role, as serve and the commands do. It
// refuses a role that owns the tables, a database whose migrations are not
// exactly this binary's (behind, ahead or changed), and one whose restore has
// not installed its credentials. Each of its own refusals is a failure in
// this program's words. Any other error is the driver's.
func openAppStore(ctx context.Context, c config) (*postgres.Store, error) {
	if c.databaseURL == "" {
		return nil, local("set GTM_DATABASE_URL to the app role's PostgreSQL URL")
	}
	store, err := openStore(ctx, c.databaseURL)
	if errors.Is(err, postgres.ErrDatabaseURL) {
		return nil, local("GTM_DATABASE_URL could not be parsed")
	}
	// The server answered and refused the role or the database: the codes of a wrong password, a role
	// that may not sign in, and a database that does not exist.
	var answered *pgconn.PgError
	if errors.As(err, &answered) && slices.Contains([]string{"28P01", "28000", "3D000"}, answered.Code) {
		return nil, local("PostgreSQL refused the role or the database GTM_DATABASE_URL names (%s). Run setup, "+
			"which makes the app role, or correct the URL.", answered.Code)
	}
	if err != nil {
		return nil, err
	}
	check := func() error {
		if err := store.CheckAppRole(ctx); errors.Is(err, postgres.ErrNotAppRole) {
			return local("%s", err)
		} else if err != nil {
			return err
		}
		if state, err := store.MigrationState(ctx); err != nil {
			return err
		} else if err := state.Err(); err != nil {
			return local("%s", err)
		}
		if pending, err := store.PendingRestore(ctx); err != nil {
			return err
		} else if pending != "" {
			return local("a restore has not installed its credentials; run restore again with the same backup")
		}
		return nil
	}
	if err := check(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// serve runs the HTTPS server as the app role. It refuses to start on a
// database whose migrations are behind this binary's, ahead of them or
// changed, while a restore is unfinished, or as a role that owns the tables.
// With --selection it reads the selection file first, before the database
// or any credential, and never reads it again.
func serve(ctx context.Context, args []string) error {
	selection, err := serveSelection(args)
	if err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	settings, err := loadSignIn(c)
	if err != nil {
		return err
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
	var secret string
	if err == nil {
		secret, err = settings.secret(creds)
	}
	creds.Close()
	if err != nil {
		return err
	}
	people, err := settings.open(ctx, store, secret, nil, logger)
	if err != nil {
		return err
	}
	app := web.App()
	surface, err := newSurface(ctx, store, c, key, selection, people)
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
		"mcp", c.publicOrigin+"/mcp", "selected_accounts", len(selection), "credentials", c.credentialsDir,
		"sign_in", people.described())
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

// serveSelection reads serve's command line: the selection it was given, or nil when it was given none. A
// --selection with no file name is refused, never read as "no selection", which would serve every account.
func serveSelection(args []string) (cards.Selection, error) {
	flags, file := serveFlags()
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == "selection" })
	switch {
	case flags.NArg() > 0:
		return nil, fmt.Errorf("serve takes no argument but --selection FILE, not %q", flags.Arg(0))
	case !given:
		return nil, nil
	case *file == "":
		return nil, errors.New("--selection needs the name of a selection file; an empty name is not \"no selection\"")
	}
	return loadSelection(*file)
}

// serveFlags are serve's flags.
func serveFlags() (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	return flags, flags.String("selection", "", "a selection `file`: MCP clients read only its accounts, and get the "+
		"two brief tools. It is read once, at start: restart serve to change the selection")
}

// newSurface is everything serve answers: the browser surface, with the
// MCP gateway mounted at /mcp, and the sign-ins people use.
func newSurface(ctx context.Context, store *postgres.Store, c config, sessionKey []byte, selection cards.Selection,
	people signInSurface) (*httptransport.Server, error) {
	gateway, err := mcpGateway(ctx, store, c.publicOrigin, selection)
	if err != nil {
		return nil, err
	}
	return httptransport.New(httptransport.Config{
		PublicOrigin:  c.publicOrigin,
		AllowedHosts:  c.allowedHosts,
		Web:           web.App(),
		WebDir:        c.webDir,
		SessionKey:    sessionKey,
		MCP:           gateway,
		OIDC:          people.oidc,
		DemoSignInOff: people.demoOff,
		Log:           people.log,
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
// ended sessions, once at start and then every 30 seconds. A failure is
// logged by its fixed line, as a command prints one, never by the driver's
// own text, which can name a table, a role or a host.
func housekeeping(ctx context.Context, store *postgres.Store, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if n, err := store.AbandonExpired(ctx, 100); err != nil && ctx.Err() == nil {
			logger.Warn("finishing interrupted receipts", "error", plain(err))
		} else if n > 0 {
			logger.Info("finished interrupted receipts", "count", n)
		}
		if _, err := store.DeleteEndedSessions(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("removing ended sessions", "error", plain(err))
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
