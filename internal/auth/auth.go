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
	"priomptproto/validate"
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

// ScopeOf returns who to record as the commit author.
//
// The subject comes first: a JWT carries a real one (ci-bot, sujal@acme.com) and
// that is what an audit log is for. Falling straight through to the org meant
// every admin token authored as "anonymous" and every scoped token authored as
// its org — so a commit log could not answer "who changed this", which is half
// the reason the history exists. It also made hash collisions far likelier,
// because a constant author is one less thing distinguishing two commits.
//
// Static tokens carry no subject (the tokens file has no name field), so they
// still fall back to the org. Issue JWTs via priompt-auth for real authorship.
func ScopeOf(ctx context.Context) string {
	id, _ := authn.FromContext(ctx)
	if id.Sub != "" {
		return id.Sub
	}
	if id.Org != "" {
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

// AuthorizePrefix enforces the caller's org scope on a *prefix* — a listing, not
// a single address. It is deliberately stricter than Authorize, because a prefix
// is matched by the store with a plain string comparison and string prefixes do
// not respect the org boundary: "priompt://acme" is a prefix of both
// "priompt://acme/..." and "priompt://acmecorp/...", and OrgOf reads it as
// "acme", so a scoped token could enumerate another tenant's namespace.
//
// This is the same trap as an IAM policy written against "bucket/prefix*"
// instead of "bucket/prefix/*". The fix is to authorize on the parsed org and
// require the prefix to stop at the separator, then filter the results as well —
// authorization never rides on a string prefix alone.
func AuthorizePrefix(ctx context.Context, prefix string) error {
	scope := Scope(ctx)
	if scope == "" {
		return nil // admin, or auth disabled
	}
	want := validate.Scheme + scope
	if prefix == want || strings.HasPrefix(prefix, want+"/") {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "token not authorized for prefix %q", prefix)
}

// InScope reports whether a concrete URI belongs to the caller's org. Used to
// filter listings so a result can never escape the scope even if the prefix
// matching in the store is looser than the authorization check.
func InScope(ctx context.Context, uri string) bool {
	scope := Scope(ctx)
	return scope == "" || scope == OrgOf(uri)
}

// OrgOf returns the owning org of a prompt URI — its first path segment.
func OrgOf(uri string) string { return validate.Org(uri) }
