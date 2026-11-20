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
	"syscall"
	"time"
)

// The private credentials folder: the owner secret, the demo credential and
// the TLS key and certificate. It is a real folder (not a link) of mode 0700
// owned by this user, holding regular files of mode 0600. Nothing here goes
// into PostgreSQL, and every file goes into a backup.
const (
	ownerSecretFile = "owner-secret"
	demoFile        = "demo-credentials.json"
	tlsKeyFile      = "tls.key"
	tlsCertFile     = "tls.pem"
	demoLogin       = "demo-reviewer"
)

// credentialFiles are the files a backup carries, in order.
var credentialFiles = []string{ownerSecretFile, demoFile, tlsKeyFile, tlsCertFile}

type credentials struct{ dir string }

// openCredentials checks the folder, creating it (and only it) when absent
// and create is set.
func openCredentials(dir string, create bool) (credentials, error) {
	if dir == "" {
		return credentials{}, errors.New("set GTM_CREDENTIALS_DIR to the private credentials folder")
	}
	dir = filepath.Clean(dir)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return credentials{}, fmt.Errorf("credentials folder: %w", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return credentials{}, fmt.Errorf("credentials folder: %w", err)
	}
	if err := private(info, true); err != nil {
		return credentials{}, fmt.Errorf("credentials folder %s: %w", dir, err)
	}
	return credentials{dir}, nil
}

// private checks type, mode and owner from an Lstat result.
func private(info fs.FileInfo, dir bool) error {
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return errors.New("is a symbolic link")
	case dir && !mode.IsDir():
		return errors.New("is not a folder")
	case !dir && !mode.IsRegular():
		return errors.New("is not a regular file")
	case dir && mode.Perm() != 0o700:
		return fmt.Errorf("has mode %o; it must be 700", mode.Perm())
	case !dir && mode.Perm() != 0o600:
		return fmt.Errorf("has mode %o; it must be 600", mode.Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return errors.New("belongs to another user")
	}
	return nil
}

// read gives a file's bytes, or fs.ErrNotExist.
func (c credentials) read(name string) ([]byte, error) {
	path := filepath.Join(c.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := private(info, false); err != nil {
		return nil, fmt.Errorf("%s %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	return io.ReadAll(io.LimitReader(file, 1<<20))
}

// write replaces a file atomically: a new 0600 file, then a rename.
func (c credentials) write(name string, data []byte) error {
	temp, err := os.CreateTemp(c.dir, "."+name+".")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(c.dir, name))
}

func randomToken() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ensure reads a file, or writes make()'s bytes when it is absent.
func (c credentials) ensure(name string, make func() ([]byte, error)) ([]byte, error) {
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
func (c credentials) setUp(hosts []string) (demoCredential, error) {
	if _, err := c.ensure(ownerSecretFile, func() ([]byte, error) {
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
	var demo demoCredential
	if err := json.Unmarshal(raw, &demo); err != nil || demo.Login == "" || len(demo.Password) < 32 {
		return demoCredential{}, fmt.Errorf("%s is not a demo credential", filepath.Join(c.dir, demoFile))
	}
	if err := c.ensureCertificate(hosts); err != nil {
		return demoCredential{}, err
	}
	return demo, nil
}

// ensureCertificate keeps a local certificate for this install that covers
// hosts and has at least 30 days left; otherwise it makes a new key and one.
func (c credentials) ensureCertificate(hosts []string) error {
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
