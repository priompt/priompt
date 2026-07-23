// Package auth is the server's gatekeeper: bearer-token authentication,
// org-scope authorization, and the read/write grant.
//
// Authentication is pluggable: a Provider turns a bearer credential into an
// Identity, and the interceptor tries each configured provider in order. Two
// providers ship in core — static tokens (the zero-infrastructure default)
// and priompt-auth JWTs verified against a JWKS — and anything fancier (an
// SSO gateway, custom IAM) implements the same interface without forking the
// server. Authorization (org scoping, the write gate) stays in core: it is
// policy about prompt URIs, not about who the caller is.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type identityKey struct{}

// Identity is who a request acts as, established by a Provider. Org "" is
// admin (all orgs); Write gates mutating RPCs; Sub is the principal for audit
// trails ("" for static tokens, the JWT sub for priompt-auth tokens).
type Identity struct {
	Sub   string
	Org   string
	Write bool
}

// Provider authenticates one kind of bearer credential. It receives the raw
// "authorization" header value ("Bearer <credential>", or "" when absent).
// ErrUnrecognized means "not my kind of credential — try the next provider";
// any other error rejects the request with that error.
type Provider interface {
	Authenticate(ctx context.Context, header string) (Identity, error)
}

// ErrUnrecognized is returned by a Provider that does not recognize the
// credential, letting the interceptor fall through to the next provider.
var ErrUnrecognized = errors.New("credential not recognized")

// Token is a bearer credential: the org it is scoped to ("" = admin, all orgs),
// an optional expiry (zero = never expires), and whether it may write. Write is
// opt-in — a token authors only if Write is set; otherwise it is read-only.
// Rotation is just overlapping tokens — issue the new one, let the old one's
// Expires lapse, then drop it.
type Token struct {
	Org     string
	Expires time.Time
	Write   bool
}

// StaticProvider authenticates against a fixed token→Token map (the tokens
// file). Comparison is constant time across all keys.
type StaticProvider struct {
	entries []staticEntry
}

type staticEntry struct {
	want []byte
	tok  Token
}

// NewStatic builds a StaticProvider from a tokens map.
func NewStatic(tokens map[string]Token) StaticProvider {
	entries := make([]staticEntry, 0, len(tokens))
	for t, tok := range tokens {
		entries = append(entries, staticEntry{want: []byte("Bearer " + t), tok: tok})
	}
	return StaticProvider{entries: entries}
}

func (p StaticProvider) Authenticate(_ context.Context, header string) (Identity, error) {
	got := []byte(header)
	ok := false
	var tok Token
	for _, e := range p.entries { // no early break: keep timing independent of which key matches
		if subtle.ConstantTimeCompare(got, e.want) == 1 {
			ok, tok = true, e.tok
		}
	}
	if !ok {
		return Identity{}, ErrUnrecognized
	}
	if !tok.Expires.IsZero() && time.Now().After(tok.Expires) {
		return Identity{}, status.Error(codes.Unauthenticated, "token expired")
	}
	return Identity{Org: tok.Org, Write: tok.Write}, nil
}

// JWKSProvider authenticates JWT-shaped credentials against a priompt-auth
// key set. A credential that is not JWT-shaped, or fails verification, is
// ErrUnrecognized — verification failures fall through to the chain's generic
// rejection rather than leaking why.
type JWKSProvider struct {
	Keys *KeySet
}

// NewJWKS builds a JWKSProvider fetching keys from a priompt-auth /jwks URL.
func NewJWKS(url string) JWKSProvider {
	return JWKSProvider{Keys: NewKeySet(url)}
}

func (p JWKSProvider) Authenticate(_ context.Context, header string) (Identity, error) {
	raw, found := strings.CutPrefix(header, "Bearer ")
	if !found || strings.Count(raw, ".") != 2 {
		return Identity{}, ErrUnrecognized
	}
	c, err := p.Keys.Verify(raw)
	if err != nil {
		return Identity{}, ErrUnrecognized
	}
	return Identity{Sub: c.Sub, Org: c.Org, Write: c.RW}, nil
}

// Interceptor authenticates every RPC through the provider chain and attaches
// the resulting Identity to the context. No providers disables auth entirely,
// leaving every request unscoped (full access) — the local-development mode.
func Interceptor(providers ...Provider) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if len(providers) == 0 {
			return handler(ctx, req)
		}
		header := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get("authorization"); len(v) > 0 {
				header = v[0]
			}
		}
		for _, p := range providers {
			id, err := p.Authenticate(ctx, header)
			if err == nil {
				return handler(WithIdentity(ctx, id), req)
			}
			if !errors.Is(err, ErrUnrecognized) {
				return nil, err
			}
		}
		return nil, status.Error(codes.Unauthenticated, "invalid or missing token")
	}
}

// WithIdentity returns ctx carrying id, as the interceptor would attach it —
// the hook for external providers, custom wiring, and tests.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the caller's Identity and whether one is present (absent
// when auth is disabled).
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// RequireWrite gates mutating RPCs on the caller's write capability. No
// identity (auth disabled) permits everything; an authenticated read-only
// caller is denied.
func RequireWrite(ctx context.Context) error {
	if id, ok := FromContext(ctx); ok && !id.Write {
		return status.Error(codes.PermissionDenied, "token is read-only")
	}
	return nil
}

// Scope returns the caller's raw org scope ("" when admin or auth disabled) —
// what audit logs and rate limiters key on.
func Scope(ctx context.Context) string {
	id, _ := FromContext(ctx)
	return id.Org
}

// ScopeOf returns the caller's org scope as the commit author, "anonymous" when
// there is none (auth disabled, or an unscoped admin token).
func ScopeOf(ctx context.Context) string {
	if id, _ := FromContext(ctx); id.Org != "" {
		return id.Org
	}
	return "anonymous"
}

// Authorize enforces the caller's org scope: a token scoped to org "acme" may
// only touch priompt://acme/… An empty scope (admin, or auth disabled) passes.
func Authorize(ctx context.Context, uri string) error {
	if scope := Scope(ctx); scope == "" || scope == OrgOf(uri) {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "token not authorized for org %q", OrgOf(uri))
}

// OrgOf returns the first path segment of a prompt URI (the owning org).
func OrgOf(uri string) string {
	s := strings.TrimPrefix(uri, "priompt://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}
