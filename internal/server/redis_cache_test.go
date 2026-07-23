package server

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	pb "priomptproto/gen/priompt/v1"
)

func TestRedisCache(t *testing.T) {
	mr, err := miniredis.Run() // in-memory Redis, no external server
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	c, err := NewRedisCache("redis://"+mr.Addr(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("priompt://o/r/p"); ok {
		t.Fatal("empty cache should miss")
	}

	r := &pb.GetPromptResponse{Uri: "priompt://o/r/p", Template: "Hi {n}", Slots: []string{"n"}, VersionHash: "abc123"}
	c.Put(r.Uri, r)

	got, ok := c.Get(r.Uri)
	if !ok {
		t.Fatal("expected hit after put")
	}
	if got.GetTemplate() != "Hi {n}" || got.GetVersionHash() != "abc123" || len(got.GetSlots()) != 1 {
		t.Fatalf("proto round-trip through Redis failed: %+v", got)
	}

	c.Invalidate(r.Uri)
	if _, ok := c.Get(r.Uri); ok {
		t.Fatal("invalidate should drop the entry")
	}
}
