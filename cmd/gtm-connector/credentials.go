package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The private credentials folder: the owner secret, the demo credential, the
// session key and the TLS key and certificate. It is a real folder (not a
// link) of mode 0700 owned by this user, holding regular files of mode 0600
// with one link each. Nothing here goes into PostgreSQL, and every file goes
// into a backup.
const (
	ownerSecretFile = "owner-secret"
	demoFile        = "demo-credentials.json"
	sessionKeyFile  = "session-key"
	tlsKeyFile      = "tls.key"
	tlsCertFile     = "tls.pem"
	demoLogin       = "demo-reviewer"
)

// credentialFiles are the files a backup carries, in order.
var credentialFiles = []string{ownerSecretFile, demoFile, sessionKeyFile, tlsKeyFile, tlsCertFile}

// maxCredentialBytes is the largest credential file read, from the folder or
// from a backup. The files written here are under 1 KiB each; the largest an
// operator may put in place is a certificate with its chain, a few KiB.
const maxCredentialBytes = 64 << 10

// credentials holds a descriptor of the folder, checked when it was opened.
// Every later read and write goes through that descriptor, so swapping the
// path for a link or another folder afterwards changes nothing.
type credentials struct {
	dir string
	fd  int
}

// openCredentials checks the folder, creating it (and only it) when absent
// and create is set.
func openCredentials(dir string, create bool) (*credentials, error) {
	if dir == "" {
		return nil, errors.New("set GTM_CREDENTIALS_DIR to the private credentials folder")
	}
	dir = filepath.Clean(dir)
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("credentials folder: %w", err)
		}
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("credentials folder %s: %w", dir, describe(err))
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := private(stat, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("credentials folder %s: %w", dir, err)
	}
	return &credentials{dir: dir, fd: fd}, nil
}

func (c *credentials) Close() error { return unix.Close(c.fd) }

func describe(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return errors.New("is a symbolic link or not a folder")
	case errors.Is(err, unix.ENOENT):
		return fs.ErrNotExist
	}
	return err
}

// private checks type, mode, owner and link count from an fstat result.
func private(stat unix.Stat_t, dir bool) error {
	mode := uint32(stat.Mode)
	switch {
	case dir && mode&unix.S_IFMT != unix.S_IFDIR:
		return errors.New("is not a folder")
	case !dir && mode&unix.S_IFMT != unix.S_IFREG:
		return errors.New("is not a regular file")
	case dir && mode&0o777 != 0o700:
		return fmt.Errorf("has mode %o; it must be 700", mode&0o777)
	case !dir && mode&0o777 != 0o600:
		return fmt.Errorf("has mode %o; it must be 600", mode&0o777)
	case int(stat.Uid) != os.Getuid():
		return errors.New("belongs to another user")
	case !dir && stat.Nlink != 1:
		return errors.New("has another hard link")
	}
	return nil
}

func validName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
		return fmt.Errorf("%q is not a credential name", name)
	}
	return nil
}

// read gives a file's bytes, or fs.ErrNotExist. The checks run on the
// opened descriptor, so they describe the file actually read. A file over
// maxCredentialBytes is refused by its size, before any of it is read.
func (c *credentials) read(name string) ([]byte, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(c.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(c.dir, name), describe(err))
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if err := private(stat, false); err != nil {
		return nil, fmt.Errorf("%s %w", filepath.Join(c.dir, name), err)
	}
	if stat.Size > maxCredentialBytes {
		return nil, fmt.Errorf("%s is larger than a credential file (%d bytes)", filepath.Join(c.dir, name),
			maxCredentialBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err == nil && len(data) > maxCredentialBytes {
		return nil, fmt.Errorf("%s grew while it was read", filepath.Join(c.dir, name))
	}
	return data, err
}

// staged writes data to a new private temporary file in the folder and
// returns its name.
func (c *credentials) staged(name string, data []byte) (string, error) {
	suffix := make([]byte, 8)
	rand.Read(suffix)
	temp := "." + name + "." + hex.EncodeToString(suffix)
	fd, err := unix.Openat(c.fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), temp)
	err = unix.Fchmod(fd, 0o600)
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		unix.Unlinkat(c.fd, temp, 0)
		return "", err
	}
	return temp, nil
}

// write replaces a file atomically: a new 0600 file, then a rename.
func (c *credentials) write(name string, data []byte) error {
	if err := validName(name); err != nil {
		return err
	}
	temp, err := c.staged(name, data)
	if err != nil {
		return err
	}
	if err := unix.Renameat(c.fd, temp, c.fd, name); err != nil {
		unix.Unlinkat(c.fd, temp, 0)
		return err
	}
	return unix.Fsync(c.fd)
}

// install moves a staged file to name without replacing anything there.
func (c *credentials) install(temp, name string) error {
	if err := unix.Linkat(c.fd, temp, c.fd, name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%s appeared meanwhile", filepath.Join(c.dir, name))
		}
		return err
	}
	if err := unix.Unlinkat(c.fd, temp, 0); err != nil {
		return err
	}
	return unix.Fsync(c.fd)
}

func (c *credentials) discard(temp string) { unix.Unlinkat(c.fd, temp, 0) }

func randomToken() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ensure reads a file, or writes make()'s bytes when it is absent.
func (c *credentials) ensure(name string, make func() ([]byte, error)) ([]byte, error) {
	data, err := c.read(name)
	if !errors.Is(err, fs.ErrNotExist) {
		return data, err
	}
	data, err = make()
	if err != nil {
		return nil, err
	}
	return data, c.write(name, data)
}

type demoCredential struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// setUp makes whatever credentials are missing and returns the demo
// credential. hosts are the names and addresses the certificate must cover.
func (c *credentials) setUp(hosts []string) (demoCredential, error) {
	if _, err := c.ensure(ownerSecretFile, func() ([]byte, error) {
		return []byte(randomToken()), nil
	}); err != nil {
		return demoCredential{}, err
	}
	if _, err := c.ensure(sessionKeyFile, func() ([]byte, error) {
		return []byte(randomToken()), nil
	}); err != nil {
		return demoCredential{}, err
	}
	raw, err := c.ensure(demoFile, func() ([]byte, error) {
		return json.MarshalIndent(demoCredential{demoLogin, randomToken()}, "", "  ")
	})
	if err != nil {
		return demoCredential{}, err
	}
	demo, err := parseDemo(raw)
	if err != nil {
		return demoCredential{}, fmt.Errorf("%s: %w", filepath.Join(c.dir, demoFile), err)
	}
	if err := c.ensureCertificate(hosts); err != nil {
		return demoCredential{}, err
	}
	return demo, nil
}

// parseDemo reads the demo credential as the strict reader reads it: one
// object of at most maxCredentialBytes with exactly the two fields setUp
// writes, each named once.
func parseDemo(raw []byte) (demoCredential, error) {
	var demo demoCredential
	if len(raw) > maxCredentialBytes || strictjson.Decode(raw, &demo) != nil || demo.Login == "" ||
		len(demo.Password) < 32 {
		return demoCredential{}, errors.New("not a demo credential")
	}
	return demo, nil
}

// checkCredential refuses bytes that are not a usable credential of this
// name, before a restore installs them.
func checkCredential(name string, data []byte, all map[string][]byte) error {
	switch name {
	case ownerSecretFile, sessionKeyFile:
		if len(strings.TrimSpace(string(data))) < 32 {
			return fmt.Errorf("%s is too short", name)
		}
	case demoFile:
		_, err := parseDemo(data)
		return err
	case tlsKeyFile, tlsCertFile:
		if _, err := tls.X509KeyPair(all[tlsCertFile], all[tlsKeyFile]); err != nil {
			return fmt.Errorf("the TLS key and certificate do not form a pair: %w", err)
		}
	default:
		return fmt.Errorf("%s is not a credential this version knows", name)
	}
	return nil
}

// sessionKey is the key that signs pre-session tokens.
func (c *credentials) sessionKey() ([]byte, error) {
	raw, err := c.read(sessionKeyFile)
	if err != nil {
		return nil, err
	}
	key := []byte(strings.TrimSpace(string(raw)))
	if len(key) < 32 {
		return nil, errors.New("the session key is too short")
	}
	return key, nil
}

// ensureCertificate keeps a local certificate for this install that covers
// hosts and has at least 30 days left; otherwise it makes a new key and one.
func (c *credentials) ensureCertificate(hosts []string) error {
	certPEM, certErr := c.read(tlsCertFile)
	keyPEM, keyErr := c.read(tlsKeyFile)
	for _, err := range []error{certErr, keyErr} {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if certErr == nil && keyErr == nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err == nil && covers(pair.Leaf, hosts) && time.Until(pair.Leaf.NotAfter) > 30*24*time.Hour {
			return nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gtm-connector local install"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(397 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := c.write(tlsKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	return c.write(tlsCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func covers(cert *x509.Certificate, hosts []string) bool {
	for _, host := range hosts {
		if cert.VerifyHostname(host) != nil {
			return false
		}
	}
	return true
}

// certificateHosts are the host names (without ports) a certificate covers:
// loopback always, plus the allowed hosts.
func certificateHosts(allowed []string) []string {
	hosts := []string{"127.0.0.1", "::1", "localhost"}
	for _, entry := range allowed {
		host := entry
		if h, _, err := net.SplitHostPort(entry); err == nil {
			host = h
		}
		host = strings.ToLower(host)
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// demoDigest is what the database stores for the demo credential.
func demoDigest(demo demoCredential) []byte {
	sum := sha256.Sum256([]byte(demo.Password))
	return sum[:]
}
