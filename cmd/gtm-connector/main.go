// Command gtm-connector is the connector and the workflow application.
//
//	gtm-connector serve              run the HTTPS server (sets up first)
//	gtm-connector setup              make credentials, migrate, seed the demo workspace
//	gtm-connector backup --out FILE  write PostgreSQL and the credentials folder to FILE
//	gtm-connector restore --in FILE  restore FILE into an empty database
//	gtm-connector health             check that the local server answers
//
// Settings come from the environment: GTM_DATABASE_URL, GTM_CREDENTIALS_DIR,
// GTM_LISTEN, GTM_PUBLIC_ORIGIN, GTM_ALLOWED_HOSTS and GTM_WEB_DIR.
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
	"strings"
	"syscall"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
)

const usage = `usage: gtm-connector <command> [flags]

commands:
  serve              run the HTTPS server; sets up first
  setup              make missing credentials, migrate, seed the demo workspace
  backup --out FILE  write PostgreSQL and the credentials folder to FILE
  restore --in FILE  restore FILE into an empty database and credentials folder
  health             check that the local server answers
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
	case "backup":
		err = backupCommand(ctx, args)
	case "restore":
		err = restoreCommand(ctx, args)
	case "health":
		err = health(ctx, args)
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

// config is the install's settings, from the environment.
type config struct {
	databaseURL    string
	credentialsDir string
	listen         string
	publicOrigin   string
	allowedHosts   []string
	webDir         string
}

func loadConfig() (config, error) {
	c := config{
		databaseURL:    os.Getenv("GTM_DATABASE_URL"),
		credentialsDir: os.Getenv("GTM_CREDENTIALS_DIR"),
		listen:         envOr("GTM_LISTEN", "127.0.0.1:8443"),
		webDir:         os.Getenv("GTM_WEB_DIR"),
	}
	if c.databaseURL == "" {
		return c, errors.New("set GTM_DATABASE_URL to the PostgreSQL URL")
	}
	if c.credentialsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, errors.New("set GTM_CREDENTIALS_DIR to the private credentials folder")
		}
		c.credentialsDir = filepath.Join(home, ".config", "gtm-connector", "credentials")
	}
	_, port, err := net.SplitHostPort(c.listen)
	if err != nil {
		return c, fmt.Errorf("GTM_LISTEN %q is not host:port", c.listen)
	}
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
	if c.webDir == "" {
		if info, err := os.Stat(filepath.Join("web", "dist", "index.html")); err == nil && info.Mode().IsRegular() {
			c.webDir = filepath.Join("web", "dist")
		}
	}
	return c, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// setUp makes missing credentials, migrates the database and seeds the demo
// workspace with its demo principal. It is safe to run again.
func setUp(ctx context.Context, c config) (*postgres.Store, credentials, error) {
	creds, err := openCredentials(c.credentialsDir, true)
	if err != nil {
		return nil, credentials{}, err
	}
	demo, err := creds.setUp(certificateHosts(c.allowedHosts))
	if err != nil {
		return nil, credentials{}, err
	}
	store, err := openStore(ctx, c.databaseURL)
	if err != nil {
		return nil, credentials{}, err
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		return nil, credentials{}, err
	}
	workspace, err := store.EnsureWorkspace(ctx, "demo", "demo", "Demo workspace")
	if err == nil {
		_, err = store.EnsureDemoPrincipal(ctx, workspace, demo.Login, "Demo reviewer", demoDigest(demo))
	}
	if err != nil {
		store.Close()
		return nil, credentials{}, err
	}
	return store, creds, nil
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
	store, _, err := setUp(ctx, c)
	if err != nil {
		return err
	}
	store.Close()
	fmt.Printf("Set up. Credentials: %s\nDemo sign-in: login %s, password in %s\n",
		c.credentialsDir, demoLogin, filepath.Join(c.credentialsDir, demoFile))
	return nil
}
