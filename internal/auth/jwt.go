package auth

// JWT verification for tokens minted by the priompt-auth service (the auth
// repo). The claims contract is defined once, in the shared proto repo
// (priomptproto/claims), and imported by both the issuer and this verifier.
// The public keys come from the service's /jwks endpoint; an unknown kid
// triggers one rate-limited re-fetch, so key rotation needs no server restart.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"priomptproto/claims"
)

// Claims is the payload of a priompt-auth JWT — the shared contract.
type Claims = claims.Claims

// KeySet caches the Ed25519 public keys of a priompt-auth /jwks endpoint.
type KeySet struct {
	url    string
	client *http.Client

	mu      sync.Mutex
	keys    map[string]ed25519.PublicKey // kid -> key
	fetched time.Time
}

// refreshInterval is the minimum time between JWKS re-fetches, so a flood of
// bad-kid tokens cannot hammer the auth service.
const refreshInterval = 30 * time.Second

// NewKeySet points at a priompt-auth /jwks URL. The first fetch happens lazily
// on first use (and retries on unknown kid), so the server starts fine even if
// the auth service is briefly down.
func NewKeySet(url string) *KeySet {
	return &KeySet{url: url, client: &http.Client{Timeout: 5 * time.Second}, keys: map[string]ed25519.PublicKey{}}
}

// jwks is the wire shape of the /jwks document (RFC 7517, OKP/Ed25519 keys).
type jwks struct {
	Keys []struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		Kid string `json:"kid"`
		X   string `json:"x"`
	} `json:"keys"`
}

// key returns the public key for kid, re-fetching the JWKS (rate-limited) when
// the kid is unknown — the rotation path.
func (k *KeySet) key(kid string) (ed25519.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if pub, ok := k.keys[kid]; ok {
		return pub, nil
	}
	if time.Since(k.fetched) < refreshInterval {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	k.fetched = time.Now()
	resp, err := k.client.Get(k.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: %s", resp.Status)
	}
	var doc jwks
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	for _, jk := range doc.Keys {
		if jk.Kty != "OKP" || jk.Crv != "Ed25519" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(jk.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		k.keys[jk.Kid] = ed25519.PublicKey(raw)
	}
	if pub, ok := k.keys[kid]; ok {
		return pub, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

// Verify checks an EdDSA compact JWT and returns its claims. It enforces the
// signature, the EdDSA algorithm, and a present, unexpired exp.
func (k *KeySet) Verify(raw string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("not a compact JWT")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, err
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		return Claims{}, err
	}
	if header.Alg != "EdDSA" {
		return Claims{}, fmt.Errorf("unsupported alg %q", header.Alg)
	}
	pub, err := k.key(header.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, err
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return Claims{}, errors.New("bad signature")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, err
	}
	var c Claims
	if err := json.Unmarshal(cb, &c); err != nil {
		return Claims{}, err
	}
	if c.Exp == 0 {
		return Claims{}, errors.New("missing exp")
	}
	if time.Now().Unix() > c.Exp {
		return Claims{}, errors.New("token expired")
	}
	return c, nil
}
