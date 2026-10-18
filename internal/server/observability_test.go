package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/priompt/auth/authn"
)

func TestRateLimitInterceptor(t *testing.T) {
	h := func(context.Context, any) (any, error) { return nil, nil }
	info := &grpc.UnaryServerInfo{}

	// burst=2: first two calls for an org pass, third is rejected.
	rl := RateLimitInterceptor(1, 2)
	acme := authn.WithIdentity(context.Background(), authn.Identity{Org: "acme"})
	for i := 0; i < 2; i++ {
		if _, err := rl(acme, nil, info, h); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
	}
	if _, err := rl(acme, nil, info, h); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("third call should be rate-limited, got %v", err)
	}
	// Separate org has its own bucket.
	other := authn.WithIdentity(context.Background(), authn.Identity{Org: "other"})
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

// The audit line names the JWT subject when there is one, and omits the key
// for callers without one (static tokens, auth disabled).
func TestAuditInterceptorLogsSubject(t *testing.T) {
	h := func(context.Context, any) (any, error) { return nil, nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/priompt.v1.PromptService/PublishPrompt"}
	cases := []struct {
		name    string
		id      *authn.Identity
		wantSub any // nil = key absent
	}{
		{"jwt", &authn.Identity{Org: "acme", Sub: "ci-bot", Write: true}, "ci-bot"},
		{"static token", &authn.Identity{Org: "acme", Write: true}, nil},
		{"auth disabled", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			audit := AuditInterceptor(slog.New(slog.NewJSONHandler(&buf, nil)))
			ctx := context.Background()
			if c.id != nil {
				ctx = authn.WithIdentity(ctx, *c.id)
			}
			if _, err := audit(ctx, nil, info, h); err != nil {
				t.Fatal(err)
			}
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("audit line %q: %v", buf.String(), err)
			}
			if got := line["sub"]; got != c.wantSub {
				t.Errorf("sub = %v, want %v (line %s)", got, c.wantSub, buf.String())
			}
		})
	}
}
