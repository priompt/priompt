package server

import (
	"context"
	"testing"

	"github.com/priompt/auth/authn"
	pb "github.com/priompt/proto/gen/priompt/v1"
)

// An empty prefix means "everything I can read". For an org-scoped token that
// is its own org — never another org's prompts, and not PermissionDenied.
func TestListEmptyPrefixScopedToken(t *testing.T) {
	s, _ := newTestServer(t)
	admin := context.Background()
	for _, uri := range []string{"priompt://acme/r/a", "priompt://acme/s/b", "priompt://acmecorp/r/c", "priompt://beta/r/d"} {
		if _, err := s.PublishPrompt(admin, &pb.PublishPromptRequest{Uri: uri, Template: "t {x}", Slots: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
	}
	acme := authn.WithIdentity(context.Background(), authn.Identity{Org: "acme"})
	resp, err := s.ListPrompts(acme, &pb.ListPromptsRequest{})
	if err != nil {
		t.Fatalf("scoped ListPrompts(\"\") = %v, want acme's prompts", err)
	}
	var got []string
	for _, e := range resp.GetEntries() {
		got = append(got, e.GetUri())
	}
	if len(got) != 2 || got[0] != "priompt://acme/r/a" || got[1] != "priompt://acme/s/b" {
		t.Fatalf("scoped ListPrompts(\"\") = %v, want only acme's two prompts", got)
	}

	// Admin still sees everything.
	if resp, err := s.ListPrompts(admin, &pb.ListPromptsRequest{}); err != nil || len(resp.GetEntries()) != 4 {
		t.Fatalf("admin ListPrompts(\"\") = %v, %v; want 4 entries", resp.GetEntries(), err)
	}
}
