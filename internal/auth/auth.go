// Package auth is the server's authorization policy: org scoping and the
// read/write grant.
//
// Authentication — turning a bearer credential into an identity — is not here.
// It belongs to the auth repo (priomptauth/authn), which owns the token
// formats, the JWKS, the static tokens file, and the gRPC interceptor that runs
// them. What stays in core is the half that needs to know what a prompt URI
// means: a token scoped to org "acme" may only touch priompt://acme/…, and a
// read-only token may not author. That is policy about prompts, not about who
// the caller is.
package auth

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"priomptauth/authn"
)

// RequireWrite gates mutating RPCs on the caller's write capability. No
// identity (auth disabled) permits everything; an authenticated read-only
// caller is denied.
func RequireWrite(ctx context.Context) error {
	if id, ok := authn.FromContext(ctx); ok && !id.Write {
		return status.Error(codes.PermissionDenied, "token is read-only")
	}
	return nil
}

// Scope returns the caller's raw org scope ("" when admin or auth disabled) —
// what audit logs and rate limiters key on.
func Scope(ctx context.Context) string {
	id, _ := authn.FromContext(ctx)
	return id.Org
}

// ScopeOf returns the caller's org scope as the commit author, "anonymous" when
// there is none (auth disabled, or an unscoped admin token).
func ScopeOf(ctx context.Context) string {
	if id, _ := authn.FromContext(ctx); id.Org != "" {
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
