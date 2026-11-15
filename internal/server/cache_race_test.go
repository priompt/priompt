package server

import (
	"context"
	"testing"
	"time"

	pb "github.com/priompt/proto/gen/priompt/v1"
)

// racingCache runs beforePut once, just before storing, to land a publish
// between GetPrompt's store read and its cache Put.
type racingCache struct {
	Cache
	beforePut func()
}

func (c *racingCache) Put(uri string, r *pb.GetPromptResponse) {
	if f := c.beforePut; f != nil {
		c.beforePut = nil
		f()
	}
	c.Cache.Put(uri, r)
}

// A publish that commits and invalidates while a cache-miss read is in flight
// must not leave that read's old version cached.
func TestGetPromptDoesNotCacheVersionSupersededMidRead(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"
	publish := func(tmpl string) {
		t.Helper()
		if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: tmpl}); err != nil {
			t.Fatal(err)
		}
	}
	publish("v1")
	rc := &racingCache{Cache: NewMemCache(time.Minute)}
	rc.beforePut = func() { publish("v2") }
	s.Cache = rc

	if _, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPrompt(ctx, &pb.GetPromptRequest{Uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTemplate() != "v2" {
		t.Fatalf("after publishing v2 mid-read, GetPrompt serves %q, want v2", got.GetTemplate())
	}
}
