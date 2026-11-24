package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/priompt/proto/gen/priompt/v1"
)

// Republishing the content a branch already holds is a no-op, as it is on
// main: no new commit on the branch.
func TestBranchRepublishIsNoop(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1 {x}", Slots: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "feature"}); err != nil {
		t.Fatal(err)
	}
	req := &pb.PublishPromptRequest{Uri: uri, Template: "v2 {x}", Slots: []string{"x"}, Branch: "feature"}
	var hashes []string
	for i := 0; i < 3; i++ {
		resp, err := s.PublishPrompt(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, resp.GetVersionHash())
	}
	if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Fatalf("version hashes differ: %v", hashes)
	}
	hist, err := s.History(ctx, &pb.HistoryRequest{Uri: uri, Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	// v1 (shared with main) + one v2 commit.
	if got := len(hist.GetCommits()); got != 2 {
		t.Fatalf("feature history = %d commits, want 2", got)
	}
}

// Publishing to a branch that does not exist on an existing prompt fails with
// NotFound instead of creating an orphan branch with no shared history: a
// typo ("deev") must not look like a successful publish to "dev".
func TestPublishToMissingBranch(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "typo", Branch: "deev"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("publish to missing branch = %v, want NotFound", err)
	}
	br, err := s.ListBranches(ctx, &pb.ListBranchesRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range br.GetBranches() {
		if b.GetName() == "deev" {
			t.Fatal("publish to missing branch created it")
		}
	}
}

// The first publish of a brand-new prompt may target any branch: there is no
// history to share, so a root commit is correct.
func TestFirstPublishOnBranch(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/new"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1", Branch: "draft"}); err != nil {
		t.Fatal(err)
	}
	if hist, err := s.History(ctx, &pb.HistoryRequest{Uri: uri, Branch: "draft"}); err != nil || len(hist.GetCommits()) != 1 {
		t.Fatalf("draft history = %v, %v; want 1 commit", hist.GetCommits(), err)
	}
}
