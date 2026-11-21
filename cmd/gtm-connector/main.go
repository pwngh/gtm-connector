// Command gtm-connector is the connector and the workflow application.
//
//	gtm-connector setup              make credentials, migrate, set up the app role, seed the demo workspace
//	gtm-connector serve              run the HTTPS server, as the app role
//	gtm-connector demo [--again]     publish a card for each demo account that holds none, for the demo sign-in
//	gtm-connector backup --out FILE  write PostgreSQL and the credentials folder to FILE
//	gtm-connector restore --in FILE  restore FILE into an empty database
//	gtm-connector health             check that the local server answers
//	gtm-connector version            print the commit this binary was built from
//
// Settings come from the environment:
//
//	GTM_DATABASE_URL        the app role's URL, which serve and demo use (setup creates that role)
//	GTM_OWNER_DATABASE_URL  the owner role's URL, for setup, backup and restore
//	GTM_CREDENTIALS_DIR     the private credentials folder
//	GTM_LISTEN              a loopback host:port (default 127.0.0.1:8443)
//	GTM_CONTAINER_LISTEN    inside a container only: the address to listen on instead
//	GTM_PUBLIC_ORIGIN, GTM_ALLOWED_HOSTS
//	GTM_WEB_DIR             a folder to serve the web app from, instead of the build inside the binary
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

const usage = `usage: gtm-connector <command> [flags]

commands:
  setup              make missing credentials, migrate, set up the app role, seed the demo workspace
  serve              run the HTTPS server, connecting as the app role
  demo [--again]     publish a card for each demo account that holds none, for the demo sign-in
  backup --out FILE  write PostgreSQL and the credentials folder to FILE
  restore --in FILE  restore FILE into an empty database and credentials folder
  health             check that the local server answers
  version            print the commit this binary was built from
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch command, args := os.Args[1], os.Args[2:]; command {
	case "serve":
		err = serve(ctx, args)
	case "setup":
		err = setupCommand(ctx, args)
	case "demo":
		err = demoCommand(ctx, args)
	case "backup":
		err = backupCommand(ctx, args)
	case "restore":
		err = restoreCommand(ctx, args)
	case "health":
		err = health(ctx, args)
	case "version":
		fmt.Printf("gtm-connector %s %s\n", buildRevision(), runtime.Version())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gtm-connector:", err)
		os.Exit(1)
	}
}

// revision is the commit this binary was built from, given at link time
// (-ldflags "-X main.revision=COMMIT"), as the image build does.
var revision string

// buildRevision is revision or, without it, the commit Go stamped from the
// checkout it built in, with "-modified" when that checkout had changes.
func buildRevision() string {
	if revision != "" {
		return revision
	}
	commit, modified := "unknown", ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch {
			case setting.Key == "vcs.revision":
				commit = setting.Value
			case setting.Key == "vcs.modified" && setting.Value == "true":
				modified = "-modified"
			}
		}
	}
	return commit + modified
}

// config is the install's settings, from the environment.
type config struct {
	databaseURL      string // the app role
	ownerDatabaseURL string
	credentialsDir   string
	listen           string
	publicOrigin     string
	allowedHosts     []string
	webDir           string
}

func loadConfig() (config, error) {
	c := config{
		databaseURL:      os.Getenv("GTM_DATABASE_URL"),
		ownerDatabaseURL: os.Getenv("GTM_OWNER_DATABASE_URL"),
		credentialsDir:   os.Getenv("GTM_CREDENTIALS_DIR"),
		webDir:           os.Getenv("GTM_WEB_DIR"),
	}
	if c.credentialsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, errors.New("set GTM_CREDENTIALS_DIR to the private credentials folder")
		}
		c.credentialsDir = filepath.Join(home, ".config", "gtm-connector", "credentials")
	}
	listen, err := listenAddress(os.Getenv("GTM_LISTEN"), os.Getenv("GTM_CONTAINER_LISTEN"))
	if err != nil {
		return c, err
	}
	c.listen = listen
	_, port, _ := net.SplitHostPort(c.listen)
	c.publicOrigin = envOr("GTM_PUBLIC_ORIGIN", "https://127.0.0.1:"+port)
	origin, err := url.Parse(c.publicOrigin)
	if err != nil || origin.Host == "" {
		return c, fmt.Errorf("GTM_PUBLIC_ORIGIN %q is not an origin", c.publicOrigin)
	}
	hosts := envOr("GTM_ALLOWED_HOSTS", origin.Host+",localhost:"+origin.Port())
	for _, host := range strings.Split(hosts, ",") {
		if host = strings.TrimSpace(host); host != "" {
			c.allowedHosts = append(c.allowedHosts, host)
		}
	}
	return c, nil
}

// listenAddress is GTM_LISTEN, which must be a loopback address, unless a
// container sets GTM_CONTAINER_LISTEN for its own network namespace (where
// Compose publishes the port on the host's loopback only).
func listenAddress(listen, container string) (string, error) {
	if container != "" {
		if _, _, err := net.SplitHostPort(container); err != nil {
			return "", fmt.Errorf("GTM_CONTAINER_LISTEN %q is not host:port", container)
		}
		return container, nil
	}
	if listen == "" {
		listen = "127.0.0.1:8443"
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("GTM_LISTEN %q is not host:port", listen)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("GTM_LISTEN %q is not a loopback address; the server is for this machine only", listen)
	}
	return listen, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (c config) owner() (string, error) {
	if c.ownerDatabaseURL == "" {
		return "", errors.New("set GTM_OWNER_DATABASE_URL to the owner role's PostgreSQL URL")
	}
	return c.ownerDatabaseURL, nil
}

// setUp makes missing credentials, migrates the database, sets up the app
// role and seeds the demo workspace with its demo principal, under the
// install lock that restore also takes. It is safe to run again.
func setUp(ctx context.Context, c config) error {
	ownerURL, err := c.owner()
	if err != nil {
		return err
	}
	if c.databaseURL == "" {
		return errors.New("set GTM_DATABASE_URL to the app role's URL; setup creates that role")
	}
	app, err := pgconn.ParseConfig(c.databaseURL)
	if err != nil {
		return errors.New("GTM_DATABASE_URL could not be parsed")
	}
	store, err := openStore(ctx, ownerURL)
	if err != nil {
		return err
	}
	defer store.Close()
	unlock, err := store.LockInstall(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if pending, err := store.PendingRestore(ctx); err != nil {
		return err
	} else if pending != "" {
		return errors.New("a restore has not installed its credentials; run restore again with the same backup")
	}
	creds, err := openCredentials(c.credentialsDir, true)
	if err != nil {
		return err
	}
	defer creds.Close()
	demo, err := creds.setUp(certificateHosts(c.allowedHosts))
	if err != nil {
		return err
	}
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	if err := store.EnsureAppRole(ctx, app.User, app.Password); err != nil {
		return err
	}
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err == nil {
		_, err = store.EnsureDemoPrincipal(ctx, workspace, demo.Login, "Demo reviewer", demoDigest(demo))
	}
	return err
}

// openStore waits up to a minute for the database, which may still be
// starting beside this process.
func openStore(ctx context.Context, url string) (*postgres.Store, error) {
	deadline := time.Now().Add(time.Minute)
	for {
		store, err := postgres.Open(ctx, url)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			return store, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func setupCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if err := setUp(ctx, c); err != nil {
		return err
	}
	fmt.Printf("Set up. Credentials: %s\nDemo sign-in: login %s, password in %s\n",
		c.credentialsDir, demoLogin, filepath.Join(c.credentialsDir, demoFile))
	return nil
}

func urlHost(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%q is not an origin", origin)
	}
	return parsed.Host, nil
}
