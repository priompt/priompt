package server

import (
	"context"
	"testing"

	"github.com/priompt/auth/authn"
	pb "github.com/priompt/proto/gen/priompt/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListBranches returns every branch, not just conventional names, and
// ListCommits still lists a commit after a rollback leaves it off every branch.
func TestListBranchesAndCommits(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://acme/r/p"
	pub := func(tmpl, branch string) {
		t.Helper()
		if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: tmpl, Branch: branch}); err != nil {
			t.Fatal(err)
		}
	}
	pub("v1", "")
	pub("v2", "")
	if _, err := s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "feature-x"}); err != nil {
		t.Fatal(err)
	}
	pub("v3", "feature-x")
	h, _ := s.History(ctx, &pb.HistoryRequest{Uri: uri})
	v1 := h.GetCommits()[1].GetHash()
	if _, err := s.SetBranch(ctx, &pb.SetBranchRequest{Uri: uri, CommitHash: v1}); err != nil {
		t.Fatal(err)
	}

	br, err := s.ListBranches(ctx, &pb.ListBranchesRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if b := br.GetBranches(); len(b) != 2 || b[0].GetName() != "feature-x" || b[1].GetName() != "main" || b[1].GetCommitHash() != v1 {
		t.Fatalf("branches = %v, want feature-x and main@%s", b, v1)
	}
	cs, err := s.ListCommits(ctx, &pb.ListCommitsRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.GetCommits()) != 3 || cs.GetCommits()[2].GetHash() != v1 {
		t.Fatalf("commits = %v, want 3, oldest last", cs.GetCommits())
	}

	beta := authn.WithIdentity(context.Background(), authn.Identity{Org: "beta"})
	if _, err := s.ListBranches(beta, &pb.ListBranchesRequest{Uri: uri}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("other org ListBranches = %v, want PermissionDenied", err)
	}
	if _, err := s.ListCommits(beta, &pb.ListCommitsRequest{Uri: uri}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("other org ListCommits = %v, want PermissionDenied", err)
	}
}
