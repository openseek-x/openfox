package teams

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"time"
)

type signingKey struct {
	key   *rsa.PublicKey
	teams bool
}
type keyCache struct {
	values      map[string]signingKey
	expires     time.Time
	nextRefresh time.Time
	refreshing  chan struct{}
}

// ValidateJWT implements only Connector-to-bot authentication. The expected
// App ID is selected by the local route, never by unverified JWT claims.
func (c *Client) ValidateJWT(ctx context.Context, authorization, appID, serviceURL string) error {
	if !GUID(appID) || !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > MaxTokenBytes+7 {
		return ErrUnauthorized
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrUnauthorized
	}
	header, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return ErrUnauthorized
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return ErrUnauthorized
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return ErrUnauthorized
	}
	var h struct {
		Algorithm string   `json:"alg"`
		KeyID     string   `json:"kid"`
		Critical  []string `json:"crit"`
		JKU       string   `json:"jku"`
		X5U       string   `json:"x5u"`
	}
	if decodeJSON(header, &h) != nil || h.Algorithm != "RS256" || !opaque(h.KeyID, 256) || len(h.Critical) > 0 || h.JKU != "" || h.X5U != "" {
		return ErrUnauthorized
	}
	var p struct {
		Issuer          string          `json:"iss"`
		Audience        string          `json:"aud"`
		Expires         json.RawMessage `json:"exp"`
		NotBefore       json.RawMessage `json:"nbf"`
		ServiceURL      string          `json:"serviceurl"`
		ServiceURLCamel string          `json:"serviceUrl"`
	}
	if decodeJSON(payload, &p) != nil || p.Issuer != connectorIssuer || p.Audience != appID {
		return ErrUnauthorized
	}
	exp, ok := numericDate(p.Expires)
	if !ok {
		return ErrUnauthorized
	}
	nbf, ok := numericDate(p.NotBefore)
	if !ok || exp <= nbf {
		return ErrUnauthorized
	}
	now := c.now().Unix()
	if now < nbf-300 || now >= exp+300 {
		return ErrUnauthorized
	}
	expected, err := ServiceURL(serviceURL)
	if err != nil {
		return ErrUnauthorized
	}
	// Prose and the current official implementation use different casing. These
	// are the two verified spellings; conflicting fields must never win by order.
	signed := p.ServiceURL
	if signed == "" {
		signed = p.ServiceURLCamel
	}
	if signed == "" {
		return ErrUnauthorized
	}
	actual, err := ServiceURL(signed)
	if err != nil || actual != expected {
		return ErrUnauthorized
	}
	if p.ServiceURL != "" && p.ServiceURLCamel != "" {
		other, e := ServiceURL(p.ServiceURLCamel)
		if e != nil || other != actual {
			return ErrUnauthorized
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	key, err := c.signingKey(ctx, h.KeyID)
	if err != nil {
		return err
	}
	if !key.teams {
		return ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key.key, crypto.SHA256, sum[:], signature) != nil {
		return ErrUnauthorized
	}
	return nil
}

func numericDate(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return 0, false
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n <= 0 || n > 253402300799 {
		return 0, false
	}
	return n, true
}

func (c *Client) signingKey(ctx context.Context, kid string) (signingKey, error) {
	for {
		c.mu.Lock()
		now := c.now()
		if now.Before(c.keys.expires) {
			if key, ok := c.keys.values[kid]; ok {
				c.mu.Unlock()
				return key, nil
			}
		}
		if pending := c.keys.refreshing; pending != nil {
			c.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return signingKey{}, ErrUnavailable
			}
		}
		if now.Before(c.keys.nextRefresh) {
			c.mu.Unlock()
			return signingKey{}, ErrUnauthorized
		}
		pending := make(chan struct{})
		c.keys.refreshing = pending
		c.keys.nextRefresh = now.Add(time.Minute)
		c.mu.Unlock()
		keys, err := c.loadKeys(ctx)
		c.mu.Lock()
		if err == nil {
			c.keys.values = keys
			c.keys.expires = c.now().Add(24 * time.Hour)
		}
		c.keys.refreshing = nil
		close(pending)
		c.mu.Unlock()
		if err != nil {
			return signingKey{}, ErrUnavailable
		}
		// The refresh is shared and limited. A missing kid does not trigger a
		// second fetch; expired old keys can never authorize a request.
		key, ok := keys[kid]
		if !ok {
			return signingKey{}, ErrUnauthorized
		}
		return key, nil
	}
}

func (c *Client) loadKeys(ctx context.Context) (map[string]signingKey, error) {
	data, err := c.request(ctx, "GET", openidURL, "", "", nil, 64<<10, false)
	if err != nil {
		return nil, err
	}
	var metadata struct {
		Issuer     string   `json:"issuer"`
		JWKS       string   `json:"jwks_uri"`
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if decodeJSON(data, &metadata) != nil || metadata.Issuer != connectorIssuer || metadata.JWKS != keysURL {
		return nil, ErrInvalid
	}
	allowed := false
	for _, a := range metadata.Algorithms {
		if a == "RS256" {
			allowed = true
		}
	}
	if !allowed {
		return nil, ErrInvalid
	}
	data, err = c.request(ctx, "GET", keysURL, "", "", nil, 1<<20, false)
	if err != nil {
		return nil, err
	}
	var document struct {
		Keys []struct {
			KeyID        string   `json:"kid"`
			Type         string   `json:"kty"`
			Use          string   `json:"use"`
			Algorithm    string   `json:"alg"`
			N            string   `json:"n"`
			E            string   `json:"e"`
			Endorsements []string `json:"endorsements"`
		} `json:"keys"`
	}
	if decodeJSON(data, &document) != nil || len(document.Keys) == 0 || len(document.Keys) > 1024 {
		return nil, ErrInvalid
	}
	keys := make(map[string]signingKey, len(document.Keys))
	for _, j := range document.Keys {
		if !opaque(j.KeyID, 256) || j.Type != "RSA" || j.Use != "" && j.Use != "sig" || j.Algorithm != "" && j.Algorithm != "RS256" {
			return nil, ErrInvalid
		}
		if _, exists := keys[j.KeyID]; exists {
			return nil, ErrInvalid
		}
		n, er := base64.RawURLEncoding.Strict().DecodeString(j.N)
		if er != nil || len(n) < 256 || len(n) > 512 {
			return nil, ErrInvalid
		}
		e, er := base64.RawURLEncoding.Strict().DecodeString(j.E)
		if er != nil || len(e) == 0 || len(e) > 4 {
			return nil, ErrInvalid
		}
		exponent := int64(0)
		for _, b := range e {
			exponent = exponent*256 + int64(b)
		}
		if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
			return nil, ErrInvalid
		}
		key := signingKey{key: &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}}
		if key.key.N.BitLen() < 2048 {
			return nil, ErrInvalid
		}
		for _, v := range j.Endorsements {
			if v == "msteams" {
				key.teams = true
			}
		}
		keys[j.KeyID] = key
	}
	return keys, nil
}
