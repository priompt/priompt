package server

import (
	"testing"
	"time"

	pb "priompt/gen/priompt/v1"
)

func TestMemCache(t *testing.T) {
	c := NewMemCache(50 * time.Millisecond)
	r := &pb.GetPromptResponse{Uri: "u"}

	if _, ok := c.Get("u"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Put("u", r)
	if got, ok := c.Get("u"); !ok || got != r {
		t.Fatal("want hit immediately after put")
	}
	c.Invalidate("u")
	if _, ok := c.Get("u"); ok {
		t.Fatal("invalidate should drop the entry")
	}

	c.Put("u", r)
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("u"); ok {
		t.Fatal("entry should expire after ttl")
	}

	// Non-positive TTL = disabled cache (nil interface).
	if NewMemCache(0) != nil {
		t.Fatal("ttl<=0 should disable the cache")
	}
}
