package auth

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A listing prefix must stop at the org separator. "priompt://acme" is a string
// prefix of "priompt://acmecorp/..." too, and OrgOf reads it as "acme", so
// authorizing on the raw prefix let a scoped token enumerate a neighbouring
// tenant whose name merely starts the same way.
func TestAuthorizePrefixStopsAtOrgBoundary(t *testing.T) {
	ctx := scoped("acme", true)

	allowed := []string{
		"priompt://acme",
		"priompt://acme/",
		"priompt://acme/support/",
		"priompt://acme/support/tier1/agent",
	}
	for _, p := range allowed {
		if err := AuthorizePrefix(ctx, p); err != nil {
			t.Errorf("AuthorizePrefix(%q) = %v, want allowed", p, err)
		}
	}

	denied := []string{
		"priompt://acmecorp",     // the bug: a longer org sharing the prefix
		"priompt://acmecorp/c/z", //
		"priompt://acme-other/x", //
		"priompt://beta/",        // an unrelated org
		"",                       // everything
		"priompt://",             // everything
	}
	for _, p := range denied {
		err := AuthorizePrefix(ctx, p)
		if err == nil {
			t.Errorf("AuthorizePrefix(%q) = nil, want PermissionDenied", p)
			continue
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("AuthorizePrefix(%q) = %v, want PermissionDenied", p, status.Code(err))
		}
	}
}

// An admin (empty scope) may list anything, including everything.
func TestAuthorizePrefixAdmin(t *testing.T) {
	ctx := scoped("", true)
	for _, p := range []string{"", "priompt://", "priompt://beta/x"} {
		if err := AuthorizePrefix(ctx, p); err != nil {
			t.Errorf("admin AuthorizePrefix(%q) = %v", p, err)
		}
	}
}

// InScope is the result-side filter, and must use the parsed org rather than a
// string comparison for the same reason.
func TestInScope(t *testing.T) {
	ctx := scoped("acme", true)
	if !InScope(ctx, "priompt://acme/a/x") {
		t.Error("own org filtered out")
	}
	if InScope(ctx, "priompt://acmecorp/c/z") {
		t.Error("acmecorp passed the acme scope filter")
	}
	if !InScope(scoped("", true), "priompt://anything/x") {
		t.Error("admin filtered out")
	}
}
