package oidc

import (
	"context"
	"crypto/subtle"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// verify checks an ID token, a JWS in compact form, and gives its claims: the header first, then the
// signature, then each claim. Its header and its claims are read as strictjson reads JSON, so a member
// named twice refuses the token.
func (c *Client) verify(ctx context.Context, raw, nonce string) (*strictjson.Object, error) {
	if len(raw) > maxToken {
		return nil, ErrMalformed
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	headerBytes, err1 := strictBase64.DecodeString(parts[0])
	payloadBytes, err2 := strictBase64.DecodeString(parts[1])
	signature, err3 := strictBase64.DecodeString(parts[2])
	header, ok1 := objectOf(headerBytes)
	claims, ok2 := objectOf(payloadBytes)
	if err1 != nil || err2 != nil || err3 != nil || !ok1 || !ok2 {
		return nil, ErrMalformed
	}
	value, _ := header.Get("alg")
	alg, _ := value.(string)
	a, known := algorithms[alg]
	if !known {
		return nil, ErrAlgorithm
	}
	// A critical extension is one this package would have to understand, and it understands none. A type,
	// when given, says the token is a JWT, which an access token of RFC 9068 does not.
	if _, critical := header.Get("crit"); critical {
		return nil, ErrMalformed
	}
	if value, present := header.Get("typ"); present {
		if typ, _ := value.(string); asciiLower(typ) != "jwt" {
			return nil, ErrMalformed
		}
	}
	kid := ""
	if value, present := header.Get("kid"); present {
		var isText bool
		if kid, isText = value.(string); !isText || kid == "" {
			return nil, ErrMalformed
		}
	}
	if len(signature) == 0 {
		return nil, ErrSignature
	}
	if err := c.checkSignature(ctx, alg, a, kid, []byte(parts[0]+"."+parts[1]), signature); err != nil {
		return nil, err
	}
	return claims, c.checkClaims(claims, nonce)
}

func objectOf(document []byte) (*strictjson.Object, bool) {
	value, err := strictjson.Loads(document)
	object, isObject := value.(*strictjson.Object)
	return object, err == nil && isObject && object != nil
}

// checkClaims checks the claims of a token whose signature verified, as OpenID Connect Core 3.1.3.7 has a
// client check them, with every audience but this client refused.
func (c *Client) checkClaims(claims *strictjson.Object, nonce string) error {
	text := func(name string) (string, bool) {
		value, _ := claims.Get(name)
		s, isText := value.(string)
		return s, isText
	}
	if issuer, _ := text("iss"); subtle.ConstantTimeCompare([]byte(issuer), []byte(c.cfg.Issuer)) != 1 {
		return ErrIssuer
	}
	value, _ := claims.Get("aud")
	var audiences []any
	switch aud := value.(type) {
	case string:
		audiences = []any{aud}
	case []any:
		audiences = aud
	}
	if len(audiences) == 0 {
		return ErrAudience
	}
	for _, audience := range audiences {
		if audience != c.cfg.ClientID {
			return ErrAudience
		}
	}
	if _, present := claims.Get("azp"); present {
		if party, _ := text("azp"); party != c.cfg.ClientID {
			return ErrAuthorizedParty
		}
	}
	now := c.now()
	expires, ok := numericDate(claims, "exp")
	if !ok || !now.Before(expires.Add(Leeway)) {
		return ErrExpired
	}
	issued, ok := numericDate(claims, "iat")
	if !ok || issued.After(now.Add(Leeway)) || issued.Before(now.Add(-MaxTokenAge-Leeway)) {
		return ErrIssuedAt
	}
	if _, present := claims.Get("nbf"); present {
		if notBefore, ok := numericDate(claims, "nbf"); !ok || now.Before(notBefore.Add(-Leeway)) {
			return ErrNotYetValid
		}
	}
	if given, _ := text("nonce"); nonce == "" || subtle.ConstantTimeCompare([]byte(given), []byte(nonce)) != 1 {
		return ErrNonce
	}
	if subject, _ := text("sub"); !printableASCII(subject, 255) {
		return ErrSubject
	}
	return nil
}

// latestDate is the last second of the year 9999, past which no NumericDate is read.
const latestDate = 253402300799

// numericDate reads a claim that is a NumericDate: seconds since the epoch, whole or with a fraction.
func numericDate(claims *strictjson.Object, name string) (time.Time, bool) {
	value, _ := claims.Get(name)
	switch n := value.(type) {
	case *big.Int:
		if n.Sign() < 0 || n.Cmp(big.NewInt(latestDate)) > 0 {
			return time.Time{}, false
		}
		return time.Unix(n.Int64(), 0), true
	case float64:
		if math.IsNaN(n) || n < 0 || n > latestDate {
			return time.Time{}, false
		}
		whole, fraction := math.Modf(n)
		return time.Unix(int64(whole), int64(fraction*1e9)), true
	}
	return time.Time{}, false
}
