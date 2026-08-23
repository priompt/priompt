package server

import (
	"bytes"
	"encoding/base64"
	store "priomptdb"
	"strings"
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

	c, err := NewRedisCache("redis://"+mr.Addr(), time.Minute, nil)
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

// With a key configured, what lands in Redis must be ciphertext. Redis is a
// second persistent store on a second host — it snapshots to disk — so leaving
// prompt bodies in cleartext there would void the at-rest guarantee for exactly
// the prompts an attacker would most want: the recently read ones.
func TestRedisCacheSealsValues(t *testing.T) {
	t.Setenv("PRIOMPT_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	sealer, err := store.NewSealer()
	if err != nil {
		t.Fatal(err)
	}
	if !sealer.Enabled() {
		t.Fatal("sealer not enabled with a key set")
	}

	mr := miniredis.RunT(t)
	c, err := NewRedisCache("redis://"+mr.Addr(), time.Minute, sealer)
	if err != nil {
		t.Fatal(err)
	}

	const secret = "TOPSECRET launch codes for {org}"
	uri := "priompt://acme/vault/secret"
	c.Put(uri, &pb.GetPromptResponse{Uri: uri, Template: secret, Slots: []string{"org"}})

	raw, err := mr.Get(redisKey(uri))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "TOPSECRET") {
		t.Error("prompt plaintext is stored in redis despite an encryption key")
	}
	if strings.Contains(raw, "{org}") {
		t.Error("template body is stored in redis in cleartext")
	}

	// And it must still round-trip.
	got, ok := c.Get(uri)
	if !ok {
		t.Fatal("sealed entry did not round-trip")
	}
	if got.GetTemplate() != secret {
		t.Fatalf("round-trip = %q, want %q", got.GetTemplate(), secret)
	}
}

// An entry written under a different key must read as a miss, not a panic and
// not a wrong answer.
func TestRedisCacheWrongKeyIsAMiss(t *testing.T) {
	mr := miniredis.RunT(t)
	uri := "priompt://acme/vault/secret"

	t.Setenv("PRIOMPT_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	s1, _ := store.NewSealer()
	c1, err := NewRedisCache("redis://"+mr.Addr(), time.Minute, s1)
	if err != nil {
		t.Fatal(err)
	}
	c1.Put(uri, &pb.GetPromptResponse{Uri: uri, Template: "hello {org}", Slots: []string{"org"}})

	t.Setenv("PRIOMPT_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	s2, _ := store.NewSealer()
	c2, err := NewRedisCache("redis://"+mr.Addr(), time.Minute, s2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Get(uri); ok {
		t.Error("an entry sealed with a different key was served")
	}
}

// A failed invalidation must be reported, not swallowed. Silently leaving every
// node serving superseded content is worse than failing the publish.
func TestInvalidateReportsFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := NewRedisCache("redis://"+mr.Addr(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri := "priompt://acme/x/y"
	c.Put(uri, &pb.GetPromptResponse{Uri: uri, Template: "hi"})

	if err := c.Invalidate(uri); err != nil {
		t.Fatalf("healthy invalidate = %v, want nil", err)
	}
	mr.Close() // the cache is now unreachable
	if err := c.Invalidate(uri); err == nil {
		t.Error("invalidate against a downed redis returned nil; the failure was swallowed")
	}
}
