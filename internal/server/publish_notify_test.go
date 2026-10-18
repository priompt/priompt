package server

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/priompt/proto/gen/priompt/v1"
)

// failingCache holds entries but cannot invalidate them, like a Redis node
// that accepts reads while DEL fails.
type failingCache struct {
	m map[string]*pb.GetPromptResponse
}

func (c *failingCache) Get(uri string) (*pb.GetPromptResponse, bool) { r, ok := c.m[uri]; return r, ok }
func (c *failingCache) Put(uri string, r *pb.GetPromptResponse)      { c.m[uri] = r }
func (c *failingCache) Invalidate(string) error                      { return errors.New("cache down") }

// A publish whose cache clear fails is stored but reported UNAVAILABLE so the
// caller retries. The retry takes the identical-content path, which does not
// notify, so the original call must send the event: otherwise subscribers
// never hear about a version that main now serves.
func TestPublishNotifiesWhenCacheClearFails(t *testing.T) {
	s, n := newTestServer(t)
	s.Cache = &failingCache{m: map[string]*pb.GetPromptResponse{}}
	ctx := context.Background()
	req := &pb.PublishPromptRequest{Uri: "priompt://o/r/p", Template: "Hi {name}", Slots: []string{"name"}}

	if _, err := s.PublishPrompt(ctx, req); status.Code(err) != codes.Unavailable {
		t.Fatalf("publish with a failing cache: err = %v, want Unavailable", err)
	}
	if len(n.events) != 1 {
		t.Fatalf("after the failed cache clear: %d events, want 1", len(n.events))
	}
	// The caller's retry: identical content, still no duplicate event.
	_, _ = s.PublishPrompt(ctx, req)
	if len(n.events) != 1 {
		t.Fatalf("after the retry: %d events, want 1 (%v)", len(n.events), n.events)
	}
}
