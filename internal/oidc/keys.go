package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// algorithm is a JWS algorithm an ID token may be signed with, and the key it needs.
type algorithm struct {
	kty, crv string
	hash     crypto.Hash // none for EdDSA, which signs the message itself
	pss      bool
	size     int // the bytes of one ECDSA coordinate
}

// algorithms are the only algorithms a token is verified with. "none" and the HMAC algorithms are not
// among them: an ID token is signed with a key only the provider holds, and a public key is never used as
// a shared secret.
var algorithms = map[string]algorithm{
	"RS256": {kty: "RSA", hash: crypto.SHA256},
	"RS384": {kty: "RSA", hash: crypto.SHA384},
	"RS512": {kty: "RSA", hash: crypto.SHA512},
	"PS256": {kty: "RSA", hash: crypto.SHA256, pss: true},
	"PS384": {kty: "RSA", hash: crypto.SHA384, pss: true},
	"PS512": {kty: "RSA", hash: crypto.SHA512, pss: true},
	"ES256": {kty: "EC", crv: "P-256", hash: crypto.SHA256, size: 32},
	"ES384": {kty: "EC", crv: "P-384", hash: crypto.SHA384, size: 48},
	"ES512": {kty: "EC", crv: "P-521", hash: crypto.SHA512, size: 66},
	"EdDSA": {kty: "OKP", crv: "Ed25519"},
	// RFC 9864's fully specified name of EdDSA over Ed25519.
	"Ed25519": {kty: "OKP", crv: "Ed25519"},
}

// sameAlgorithm is whether two names are one algorithm: EdDSA over an Ed25519 key is also named Ed25519.
func sameAlgorithm(a, b string) bool {
	ed := func(name string) bool { return name == "EdDSA" || name == "Ed25519" }
	return a == b || ed(a) && ed(b)
}

// curves are the elliptic curves of the EC keys, by their JWK names.
var curves = map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()}

// RSA keys shorter than this are not used.
const minRSABits = 2048

// key is one public key of the provider's key set.
type key struct {
	id, alg, kty, crv string
	public            crypto.PublicKey
}

// fits is whether the key may verify a token signed with alg: of the algorithm's type and curve, and, when
// the set pins the key to an algorithm, to that one.
func (k key) fits(alg string, a algorithm) bool {
	return k.kty == a.kty && k.crv == a.crv && (k.alg == "" || sameAlgorithm(k.alg, alg))
}

func digestOf(h crypto.Hash, data []byte) []byte {
	switch h {
	case crypto.SHA256:
		sum := sha256.Sum256(data)
		return sum[:]
	case crypto.SHA384:
		sum := sha512.Sum384(data)
		return sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512(data)
		return sum[:]
	}
	return nil
}

func sha256Sum(data []byte) []byte { return digestOf(crypto.SHA256, data) }

// verify is whether signature signs signed under the key with the algorithm.
func (k key) verify(a algorithm, signed, signature []byte) bool {
	switch public := k.public.(type) {
	case *rsa.PublicKey:
		digest := digestOf(a.hash, signed)
		if a.pss {
			return rsa.VerifyPSS(public, a.hash, digest, signature,
				&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: a.hash}) == nil
		}
		return rsa.VerifyPKCS1v15(public, a.hash, digest, signature) == nil
	case *ecdsa.PublicKey:
		// RFC 7518 3.4: R and S, each of the curve's size, one after the other.
		if len(signature) != 2*a.size {
			return false
		}
		r := new(big.Int).SetBytes(signature[:a.size])
		s := new(big.Int).SetBytes(signature[a.size:])
		return ecdsa.Verify(public, digestOf(a.hash, signed), r, s)
	case ed25519.PublicKey:
		return len(signature) == ed25519.SignatureSize && ed25519.Verify(public, signed, signature)
	}
	return false
}

// unusableKeys refuses a key set that is not one JSON object with a keys array.
var unusableKeys = fmt.Errorf("%w: its key set is not usable", ErrUnavailable)

// strictBase64 is unpadded base64url with no stray bits, as JOSE writes it.
var strictBase64 = base64.RawURLEncoding.Strict()

// parseKeySet reads a JWK Set and gives the keys a token may be verified with. A key for another use than
// signatures, of a type, curve or algorithm this package does not verify with, an RSA key shorter than
// 2048 bits, or one whose members are not as RFC 7518 writes them, is left out.
func parseKeySet(body []byte) ([]key, error) {
	document, err := strictjson.Loads(body)
	object, isObject := document.(*strictjson.Object)
	if err != nil || !isObject {
		return nil, unusableKeys
	}
	value, _ := object.Get("keys")
	list, isList := value.([]any)
	if !isList {
		return nil, unusableKeys
	}
	keys := []key{}
	for _, item := range list {
		if member, ok := item.(*strictjson.Object); ok {
			if k, ok := parseKey(member); ok {
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}

func parseKey(m *strictjson.Object) (key, bool) {
	// text is a member that must be text when present; "" when absent.
	ok := true
	text := func(name string) string {
		value, present := m.Get(name)
		s, isText := value.(string)
		if present && !isText {
			ok = false
		}
		return s
	}
	bytesOf := func(name string) []byte {
		decoded, err := strictBase64.DecodeString(text(name))
		if err != nil || len(decoded) == 0 {
			ok = false
		}
		return decoded
	}
	k := key{id: text("kid"), alg: text("alg"), kty: text("kty")}
	if use, present := m.Get("use"); present && use != "sig" {
		return key{}, false
	}
	if value, present := m.Get("key_ops"); present {
		operations, _ := value.([]any)
		verifies := false
		for _, operation := range operations {
			verifies = verifies || operation == "verify"
		}
		if !verifies {
			return key{}, false
		}
	}
	if _, known := algorithms[k.alg]; k.alg != "" && !known {
		return key{}, false
	}
	switch k.kty {
	case "RSA":
		n, e := bytesOf("n"), bytesOf("e")
		if !ok || n[0] == 0 || len(e) > 4 || e[0] == 0 {
			return key{}, false
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent<<8 | int(b)
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < minRSABits || modulus.BitLen() > 8192 || exponent < 3 || exponent%2 == 0 ||
			exponent > 1<<31-1 {
			return key{}, false
		}
		k.public = &rsa.PublicKey{N: modulus, E: exponent}
	case "EC":
		k.crv = text("crv")
		curve, known := curves[k.crv]
		x, y := bytesOf("x"), bytesOf("y")
		if !known || !ok {
			return key{}, false
		}
		size := (curve.Params().BitSize + 7) / 8
		if len(x) != size || len(y) != size {
			return key{}, false
		}
		public, err := ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
		if err != nil {
			return key{}, false
		}
		k.public = public
	case "OKP":
		k.crv = text("crv")
		x := bytesOf("x")
		if k.crv != "Ed25519" || !ok || len(x) != ed25519.PublicKeySize {
			return key{}, false
		}
		k.public = ed25519.PublicKey(x)
	default:
		return key{}, false
	}
	return k, ok
}

// Key set reading. A key set is kept for as long as the provider's Cache-Control max-age says, between a
// minute and an hour, and an hour when it says nothing. A token whose key the kept set lacks reads the set
// again, at most once in keyRereadGap, so a key the provider rotated in is found and a stream of unknown
// keys costs at most one read in that time.
const (
	keyLifeLeast  = time.Minute
	keyLifeMost   = time.Hour
	keyRereadGap  = 10 * time.Second
	keyLifeAsSaid = "max-age="
)

// keySet is the provider's keys as last read.
type keySet struct {
	keys    []key
	until   time.Time // when the set is read again before use; zero before the first read
	readAt  time.Time // when the set was last asked for, read or not
	failure failure   // the last failed read, held for failureHold
}

// keyLife is how long a key set is kept, from its answer's Cache-Control.
func keyLife(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		directive = strings.TrimSpace(asciiLower(directive))
		if seconds, found := strings.CutPrefix(directive, keyLifeAsSaid); found {
			if n, err := strconv.Atoi(seconds); err == nil && n >= 0 {
				return min(max(time.Duration(n)*time.Second, keyLifeLeast), keyLifeMost)
			}
		}
	}
	return keyLifeMost
}

// readKeys reads the key set at address and keeps it. A read that failed less than failureHold ago is
// not tried again. The caller holds c.mu.
func (c *Client) readKeys(ctx context.Context, address string) ([]key, error) {
	if err := c.keys.failure.held(c.now()); err != nil {
		return nil, err
	}
	c.keys.readAt = c.now()
	body, cacheControl, err := c.get(ctx, address, maxKeySet)
	var keys []key
	if err == nil {
		keys, err = parseKeySet(body)
	}
	if err != nil {
		c.keys.failure.remember(err, c.now())
		return nil, err
	}
	c.keys.keys, c.keys.until, c.keys.failure = keys, c.now().Add(keyLife(cacheControl)), failure{}
	return keys, nil
}

// keptKeys gives the kept key set, read first when there is none or it is too old. read says it was read
// by this call.
func (c *Client) keptKeys(ctx context.Context, address string) (keys []key, read bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.keys.until.IsZero() && c.now().Before(c.keys.until) {
		return c.keys.keys, false, nil
	}
	keys, err = c.readKeys(ctx, address)
	return keys, err == nil, err
}

// rereadKeys reads the key set again unless it was asked for less than keyRereadGap ago. read says it was
// read by this call.
func (c *Client) rereadKeys(ctx context.Context, address string) (keys []key, read bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Sub(c.keys.readAt) < keyRereadGap {
		return c.keys.keys, false, nil
	}
	keys, err = c.readKeys(ctx, address)
	return keys, err == nil, err
}

// checkSignature verifies a token's signature with a key of the provider's set that fits its algorithm
// and, when it names one, its key ID. A key found by its ID that does not verify the signature refuses it
// at once: a provider rotates keys under new IDs. A token whose key is not in the kept set, or that names
// no key and fits none of them, has the set read again once before it is refused.
func (c *Client) checkSignature(ctx context.Context, alg string, a algorithm, kid string, signed, signature []byte) error {
	meta, err := c.discovered(ctx)
	if err != nil {
		return err
	}
	try := func(keys []key) (fitting, verified bool) {
		for _, k := range keys {
			if (kid == "" || k.id == kid) && k.fits(alg, a) {
				fitting = true
				if k.verify(a, signed, signature) {
					return true, true
				}
			}
		}
		return fitting, false
	}
	keys, read, err := c.keptKeys(ctx, meta.keys)
	if err != nil {
		return err
	}
	fitting, verified := try(keys)
	switch {
	case verified:
		return nil
	case fitting && kid != "":
		return ErrSignature
	case !read:
		if keys, read, err = c.rereadKeys(ctx, meta.keys); err != nil {
			return err
		}
		if read {
			var again bool
			if again, verified = try(keys); verified {
				return nil
			}
			fitting = fitting || again
		}
	}
	if fitting {
		return ErrSignature
	}
	return ErrUnknownKey
}
