package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// mint signs a compact EdDSA JWT the way priompt-auth does.
func mint(t *testing.T, priv ed25519.PrivateKey, kid string, c Claims) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	h := enc(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	p := enc(c)
	sig := ed25519.Sign(priv, []byte(h+"."+p))
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksServer serves the public key as priompt-auth's /jwks endpoint would.
func jwksServer(t *testing.T, kid string, pub ed25519.PublicKey) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":%q,"x":%q}]}`,
			kid, base64.RawURLEncoding.EncodeToString(pub))
	}))
}

func TestVerifyJWT(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := jwksServer(t, "k1", pub)
	defer srv.Close()
	ks := NewKeySet(srv.URL)

	good := mint(t, priv, "k1", Claims{Sub: "ci-bot", Org: "acme", RW: true, Exp: time.Now().Add(time.Hour).Unix()})
	c, err := ks.Verify(good)
	if err != nil || c.Org != "acme" || !c.RW {
		t.Fatalf("good token should verify with claims, got %+v err=%v", c, err)
	}

	if _, err := ks.Verify(mint(t, priv, "k1", Claims{Exp: time.Now().Add(-time.Minute).Unix()})); err == nil {
		t.Error("expired token must fail")
	}
	if _, err := ks.Verify(mint(t, priv, "k1", Claims{})); err == nil {
		t.Error("token without exp must fail")
	}

	_, otherPriv, _ := ed25519.GenerateKey(nil)
	if _, err := ks.Verify(mint(t, otherPriv, "k1", Claims{Exp: time.Now().Add(time.Hour).Unix()})); err == nil {
		t.Error("wrong-key signature must fail")
	}
	if _, err := ks.Verify(mint(t, priv, "unknown-kid", Claims{Exp: time.Now().Add(time.Hour).Unix()})); err == nil {
		t.Error("unknown kid must fail (refresh is rate-limited within the same test)")
	}
	if _, err := ks.Verify("not-a-jwt"); err == nil {
		t.Error("garbage must fail")
	}
}

// TestInterceptorJWT wires the JWT path through the real interceptor: a static
// map miss falls through to JWKS verification, and the claims land as scope.
func TestInterceptorJWT(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := jwksServer(t, "k1", pub)
	defer srv.Close()
	ks := NewKeySet(srv.URL)

	run := func(header string) (passed bool, scope string, canWrite bool) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", header))
		h := func(ctx context.Context, req any) (any, error) {
			passed = true
			scope = Scope(ctx)
			canWrite = RequireWrite(ctx) == nil
			return nil, nil
		}
		Interceptor(NewStatic(map[string]Token{"static": {Org: "legacy"}}), JWKSProvider{Keys: ks})(ctx, nil, &grpc.UnaryServerInfo{}, h)
		return
	}

	jwt := mint(t, priv, "k1", Claims{Sub: "ci-bot", Org: "acme", RW: false, Exp: time.Now().Add(time.Hour).Unix()})
	if ok, scope, w := run("Bearer " + jwt); !ok || scope != "acme" || w {
		t.Errorf("JWT should pass scoped to acme read-only, got ok=%v scope=%q write=%v", ok, scope, w)
	}
	if ok, scope, _ := run("Bearer static"); !ok || scope != "legacy" {
		t.Errorf("static token must keep working alongside JWTs, got ok=%v scope=%q", ok, scope)
	}
	if ok, _, _ := run("Bearer bad.jwt.token"); ok {
		t.Error("malformed JWT must be rejected")
	}
}
