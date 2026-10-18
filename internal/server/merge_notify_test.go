package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/priompt/db-adapters"
	pb "github.com/priompt/proto/gen/priompt/v1"
	"github.com/priompt/proto/semdiff"
)

// A merge into main changes what main serves, so subscribers must hear about
// it with a verdict, the same as a publish. A merge into another branch, or one
// that leaves main's content unchanged, is not a served change.
func TestMergeIntoMainNotifies(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := &fakeNotifier{}
	s := NewServer(st, nil, semdiff.LexicalEmbedder{}, n)
	ctx := context.Background()
	const uri = "priompt://o/r/p"

	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1 {x}", Slots: []string{"x"}}))
	must(s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "feature"}))
	must(s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "other"}))
	must(s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v2 {x} rewritten entirely", Slots: []string{"x"}, Branch: "feature"}))
	n.events = nil

	// Merge into a non-main branch: main is untouched, no event.
	must(s.MergeBranch(ctx, &pb.MergeBranchRequest{Uri: uri, From: "feature", Into: "other"}))
	if len(n.events) != 0 {
		t.Fatalf("merge into a branch notified: %v", n.events)
	}

	// Merge into main: one event naming the new served version, with a verdict.
	must(s.MergeBranch(ctx, &pb.MergeBranchRequest{Uri: uri, From: "feature"}))
	head, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 || !strings.HasPrefix(n.events[0], uri+" "+head.GetVersionHash()+" ") {
		t.Fatalf("merge into main events = %v; want one for %s", n.events, head.GetVersionHash())
	}
	if strings.HasSuffix(n.events[0], " ") {
		t.Fatalf("merge event missing a verdict: %q", n.events[0])
	}

	// Merging content main already serves records a commit but changes nothing
	// readers see: no event.
	must(s.MergeBranch(ctx, &pb.MergeBranchRequest{Uri: uri, From: "other"}))
	if len(n.events) != 1 {
		t.Fatalf("merge of identical content notified: %v", n.events)
	}
}
