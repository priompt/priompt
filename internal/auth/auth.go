// Package auth is the server's gatekeeper: bearer-token authentication (static
// tokens and priompt-auth-issued JWTs), org-scope authorization, and the
// read/write grant. The static tokens file is the zero-infrastructure default;
// the JWT path verifies short-lived tokens minted by the priompt-auth service
// (the auth repo) against its JWKS — no network call on the request path.
package auth

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type scopeKey struct{}
type writeKey struct{}

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

// Interceptor authenticates bearer tokens, rejects expired ones, and attaches
// each token's org scope to the context. Static tokens are checked first in
// constant time; anything JWT-shaped is then verified against keys (nil skips
// the JWT path). No static tokens AND nil keys disables auth entirely, leaving
// every request unscoped (full access) — the local-development mode.
func Interceptor(tokens map[string]Token, keys *KeySet) grpc.UnaryServerInterceptor {
	type entry struct {
		want []byte
		tok  Token
	}
	wants := make([]entry, 0, len(tokens))
	for t, tok := range tokens {
		wants = append(wants, entry{want: []byte("Bearer " + t), tok: tok})
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if len(wants) == 0 && keys == nil {
			return handler(ctx, req)
		}
		var got []byte
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get("authorization"); len(v) > 0 {
				got = []byte(v[0])
			}
		}
		ok := false
		var tok Token
		for _, w := range wants { // no early break: keep timing independent of which key matches
			if subtle.ConstantTimeCompare(got, w.want) == 1 {
				ok, tok = true, w.tok
			}
		}
		if !ok && keys != nil {
			if raw, found := strings.CutPrefix(string(got), "Bearer "); found && strings.Count(raw, ".") == 2 {
				if c, err := keys.Verify(raw); err == nil {
					ok, tok = true, Token{Org: c.Org, Write: c.RW, Expires: time.Unix(c.Exp, 0)}
				}
			}
		}
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "invalid or missing token")
		}
		if !tok.Expires.IsZero() && time.Now().After(tok.Expires) {
			return nil, status.Error(codes.Unauthenticated, "token expired")
		}
		ctx = context.WithValue(ctx, scopeKey{}, tok.Org)
		return handler(context.WithValue(ctx, writeKey{}, tok.Write), req)
	}
}

// RequireWrite gates mutating RPCs on the token's write capability. The key is
// absent when auth is disabled or the caller is unscoped admin (full access), so
// only an explicit read-only token (writeKey == false) is denied.
func RequireWrite(ctx context.Context) error {
	if w, ok := ctx.Value(writeKey{}).(bool); ok && !w {
		return status.Error(codes.PermissionDenied, "token is read-only")
	}
	return nil
}

// WithScope returns ctx carrying an org scope, as the interceptor would set it.
// For wiring org-scoped behavior (and its tests) without a real token.
func WithScope(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, scopeKey{}, org)
}

// Scope returns the caller's raw org scope ("" when admin or auth disabled) —
// what audit logs and rate limiters key on.
func Scope(ctx context.Context) string {
	s, _ := ctx.Value(scopeKey{}).(string)
	return s
}

// ScopeOf returns the caller's org scope as the commit author, "anonymous" when
// auth is disabled (no scope on the context).
func ScopeOf(ctx context.Context) string {
	if scope, _ := ctx.Value(scopeKey{}).(string); scope != "" {
		return scope
	}
	return "anonymous"
}

// Authorize enforces the caller's org scope: a token scoped to org "acme" may
// only touch priompt://acme/… An empty scope (admin, or auth disabled) passes.
func Authorize(ctx context.Context, uri string) error {
	scope, _ := ctx.Value(scopeKey{}).(string)
	if scope == "" || scope == OrgOf(uri) {
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
