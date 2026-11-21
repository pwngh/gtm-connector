package main

import (
	"net/url"
	"os"
	"path/filepath"
)

// checkPassfile refuses the password file the PostgreSQL driver would read for a database URL when others
// can read or write it, as libpq does. The driver reads one only when neither the URL nor PGPASSWORD gives a
// password: the URL's passfile parameter, else PGPASSFILE, else ~/.pgpass. A file that does not exist is no
// refusal. The refusal names the file and its mode, never what it holds. A URL in another form is left to
// the driver.
func checkPassfile(databaseURL string) error {
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil
	}
	if _, given := parsed.User.Password(); given || os.Getenv("PGPASSWORD") != "" {
		return nil
	}
	path := parsed.Query().Get("passfile")
	if path == "" {
		path = os.Getenv("PGPASSFILE")
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".pgpass")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return local("the password file %s has mode %04o; no one but its owner may have access to it, so make it 0600", shellQuote(oneLine(path)), mode)
	}
	return nil
}
