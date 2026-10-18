package server

import (
	"context"
	"testing"

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

	// Publishing to a branch that does not exist yet still creates it.
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v3 {x}", Slots: []string{"x"}, Branch: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if hist, err := s.History(ctx, &pb.HistoryRequest{Uri: uri, Branch: "fresh"}); err != nil || len(hist.GetCommits()) != 1 {
		t.Fatalf("fresh branch history = %v, %v; want 1 commit", hist.GetCommits(), err)
	}
}
