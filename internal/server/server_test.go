package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "priompt/gen/priompt/v1"
	"priompt/internal/semdiff"
	"priompt/internal/store"
)

// fakeNotifier records published events so the test can assert notify-on-change.
type fakeNotifier struct{ events []string }

func (f *fakeNotifier) Publish(uri, hash, class string) error {
	f.events = append(f.events, uri+" "+hash+" "+class)
	return nil
}

func newTestServer(t *testing.T) (*Server, *fakeNotifier) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := &fakeNotifier{}
	return NewServer(st, nil, nil, n), n
}

func TestPublishThenGet(t *testing.T) {
	s, n := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"

	pubResp, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{
		Uri: uri, Template: "Hi {name}", Slots: []string{"name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pubResp.GetVersionHash() == "" {
		t.Fatal("expected a version hash")
	}
	if len(n.events) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(n.events))
	}

	got, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTemplate() != "Hi {name}" || got.GetVersionHash() != pubResp.GetVersionHash() {
		t.Fatalf("GetPrompt mismatch: %+v", got)
	}

	// Republishing identical content is a no-op: no extra notification.
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{
		Uri: uri, Template: "Hi {name}", Slots: []string{"name"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 {
		t.Fatalf("identical republish should not notify; got %d events", len(n.events))
	}
}

// Exercises the collaboration loop over the wire: commit to main, branch off,
// commit on the branch (invisible to GetPrompt), then merge to promote it.
func TestBranchPublishMerge(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"

	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1 {x}", Slots: []string{"x"}, Message: "init"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "feature"}); err != nil {
		t.Fatal(err)
	}
	// Commit on the branch — the served HEAD must NOT change.
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v2 {x}", Slots: []string{"x"}, Branch: "feature", Message: "wip"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri}); got.GetTemplate() != "v1 {x}" {
		t.Fatalf("branch publish leaked to main: %q", got.GetTemplate())
	}

	// Merge promotes the branch content to the served HEAD.
	if _, err := s.MergeBranch(ctx, &pb.MergeBranchRequest{Uri: uri, From: "feature", Message: "ship"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri}); got.GetTemplate() != "v2 {x}" {
		t.Fatalf("merge did not update served HEAD: %q", got.GetTemplate())
	}
	// main history: merge, init (first-parent) — the merge's content came from the branch.
	hist, err := s.History(ctx, &pb.HistoryRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.GetCommits()) != 2 || hist.GetCommits()[0].GetParent2() == "" {
		t.Fatalf("history = %d commits, tip parent2=%q (want 2, merge tip)", len(hist.GetCommits()), hist.GetCommits()[0].GetParent2())
	}
}

// Version pinning (#1) and notification verdicts (#2): fetch an old version by
// commit, roll the served HEAD back to it, and confirm notifications carry the
// semantic classification.
func TestPinAndRollback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := &fakeNotifier{}
	// real embedder so classify() produces a verdict
	s := NewServer(st, nil, semdiff.LexicalEmbedder{}, n)
	ctx := context.Background()
	const uri = "priompt://o/r/p"

	s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "alpha {x}", Slots: []string{"x"}})
	s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "omega {x} rewritten entirely", Slots: []string{"x"}})

	// first publish is "new"; second carries a real verdict
	if len(n.events) != 2 || !strings.HasSuffix(n.events[0], " new") {
		t.Fatalf("notify events = %v; want first classified 'new'", n.events)
	}
	if strings.HasSuffix(n.events[1], " ") || strings.HasSuffix(n.events[1], " new") {
		t.Fatalf("second notify missing a verdict: %q", n.events[1])
	}

	// the two commits on main, oldest last
	hist, _ := s.History(ctx, &pb.HistoryRequest{Uri: uri})
	if len(hist.GetCommits()) != 2 {
		t.Fatalf("history = %d, want 2", len(hist.GetCommits()))
	}
	oldHash := hist.GetCommits()[1].GetHash()

	// pin: fetch the old version by commit without moving HEAD
	pinned, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri, Ref: oldHash})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.GetTemplate() != "alpha {x}" || pinned.GetCommitHash() != oldHash {
		t.Fatalf("pinned fetch wrong: %+v", pinned)
	}
	if head, _ := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri}); head.GetTemplate() != "omega {x} rewritten entirely" {
		t.Fatal("pinned fetch must not move HEAD")
	}

	// rollback: point main back at the old commit; served HEAD reverts
	if _, err := s.SetBranch(ctx, &pb.SetBranchRequest{Uri: uri, CommitHash: oldHash}); err != nil {
		t.Fatal(err)
	}
	if head, _ := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri}); head.GetTemplate() != "alpha {x}" {
		t.Fatalf("rollback did not revert HEAD: %q", head.GetTemplate())
	}

	// unknown ref -> NotFound
	if _, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri, Ref: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown ref code = %v, want NotFound", status.Code(err))
	}
}

func TestPublishInvalidRejected(t *testing.T) {
	s, _ := newTestServer(t)
	// Template uses {org} but it isn't declared -> validation must reject.
	_, err := s.PublishPrompt(context.Background(), &pb.PublishPromptRequest{
		Uri: "priompt://o/r/p", Template: "Hi {org}", Slots: []string{"name"},
	})
	if err == nil {
		t.Fatal("expected invalid prompt to be rejected")
	}
}

func TestGetNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	if _, err := s.GetPrompt(context.Background(), &pb.GetPromptRequest{Uri: "priompt://x/y/z"}); err == nil {
		t.Fatal("expected NotFound for missing prompt")
	}
}
