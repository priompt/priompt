package auth

import (
	"context"
	"testing"

	"priomptauth/authn"
)

// scoped builds the context the authn interceptor would hand a handler.
func scoped(org string, write bool) context.Context {
	return authn.WithIdentity(context.Background(), authn.Identity{Org: org, Write: write})
}

func TestAuthorize(t *testing.T) {
	acme := scoped("acme", false)
	if err := Authorize(acme, "priompt://acme/r/p"); err != nil {
		t.Errorf("acme token on acme prompt should pass: %v", err)
	}
	if err := Authorize(acme, "priompt://other/r/p"); err == nil {
		t.Error("acme token on other org's prompt must be denied")
	}
	// Empty scope (admin / auth disabled) can touch any org.
	if err := Authorize(context.Background(), "priompt://anything/r/p"); err != nil {
		t.Errorf("admin/unscoped should pass: %v", err)
	}
	if err := Authorize(scoped("", false), "priompt://anything/r/p"); err != nil {
		t.Errorf("admin token should pass: %v", err)
	}
}

func TestRequireWrite(t *testing.T) {
	if err := RequireWrite(scoped("acme", false)); err == nil {
		t.Error("read-only token must be denied write")
	}
	if err := RequireWrite(scoped("acme", true)); err != nil {
		t.Errorf("rw token must be allowed to write: %v", err)
	}
	if err := RequireWrite(scoped("", true)); err != nil {
		t.Errorf("admin rw token must be allowed to write: %v", err)
	}
	// No identity on the context = auth disabled = full access.
	if err := RequireWrite(context.Background()); err != nil {
		t.Errorf("auth-disabled must permit writes: %v", err)
	}
}

func TestScopeOf(t *testing.T) {
	if got := ScopeOf(scoped("acme", false)); got != "acme" {
		t.Errorf("commit author should be the org, got %q", got)
	}
	// Unscoped admin and auth-disabled both author as "anonymous".
	if got := ScopeOf(scoped("", true)); got != "anonymous" {
		t.Errorf("unscoped admin should author as anonymous, got %q", got)
	}
	if got := ScopeOf(context.Background()); got != "anonymous" {
		t.Errorf("auth-disabled should author as anonymous, got %q", got)
	}
}

func TestOrgOf(t *testing.T) {
	for uri, want := range map[string]string{
		"priompt://acme/support/greeting": "acme",
		"priompt://acme":                  "acme",
		"acme/support/greeting":           "acme",
	} {
		if got := OrgOf(uri); got != want {
			t.Errorf("OrgOf(%q) = %q, want %q", uri, got, want)
		}
	}
}
