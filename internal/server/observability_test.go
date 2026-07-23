package server

import (
	"context"

	"priompt/internal/auth"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRateLimitInterceptor(t *testing.T) {
	h := func(context.Context, any) (any, error) { return nil, nil }
	info := &grpc.UnaryServerInfo{}

	// burst=2: first two calls for an org pass, third is rejected.
	rl := RateLimitInterceptor(1, 2)
	acme := auth.WithIdentity(context.Background(), auth.Identity{Org: "acme"})
	for i := 0; i < 2; i++ {
		if _, err := rl(acme, nil, info, h); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
	}
	if _, err := rl(acme, nil, info, h); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("third call should be rate-limited, got %v", err)
	}
	// Separate org has its own bucket.
	other := auth.WithIdentity(context.Background(), auth.Identity{Org: "other"})
	if _, err := rl(other, nil, info, h); err != nil {
		t.Errorf("other org should have its own bucket: %v", err)
	}

	// rps<=0 disables limiting entirely.
	off := RateLimitInterceptor(0, 0)
	for i := 0; i < 100; i++ {
		if _, err := off(acme, nil, info, h); err != nil {
			t.Fatalf("disabled limiter must always pass: %v", err)
		}
	}
}
