package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// noDelCache cannot invalidate, like a Redis node whose DEL fails.
type noDelCache struct{}

func (noDelCache) Get(string) (*pb.GetPromptResponse, bool) { return nil, false }
func (noDelCache) Put(string, *pb.GetPromptResponse)        {}
func (noDelCache) Invalidate(string) error                  { return errors.New("cache down") }

// A merge into main whose cache clear fails is reported UNAVAILABLE so the
// caller retries, but the retry brings no new content and does not notify, so
// the original call must still send the event.
func TestMergeNotifiesWhenCacheClearFails(t *testing.T) {
	s, n := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v1 {x}", Slots: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch(ctx, &pb.CreateBranchRequest{Uri: uri, Name: "feature"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "v2 {x}", Slots: []string{"x"}, Branch: "feature"}); err != nil {
		t.Fatal(err)
	}
	n.events = nil
	s.Cache = noDelCache{}
	merge := &pb.MergeBranchRequest{Uri: uri, From: "feature"}
	if _, err := s.MergeBranch(ctx, merge); status.Code(err) != codes.Unavailable {
		t.Fatalf("merge with a failing cache: err = %v, want Unavailable", err)
	}
	_, _ = s.MergeBranch(ctx, merge) // the caller's retry
	if len(n.events) != 1 {
		t.Fatalf("events = %v; want exactly one for the merge", n.events)
	}
}
