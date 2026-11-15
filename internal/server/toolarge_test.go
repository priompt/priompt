package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/priompt/proto/gen/priompt/v1"
	"github.com/priompt/proto/semdiff"
)

// A diff request too large for semdiff is the caller's error, not the server's.
func TestDiffPromptTooLargeIsInvalidArgument(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const uri = "priompt://o/r/p"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{Uri: uri, Template: "short"}); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x\n", semdiff.MaxLines+1)
	_, err := s.DiffPrompt(ctx, &pb.DiffPromptRequest{Uri: uri, NewTemplate: huge})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized DiffPrompt: err = %v, want InvalidArgument", err)
	}
}

// A publish too large to analyze is held like a structural change; an empty
// verdict would read as safe to auto-reload.
func TestOversizedPublishClassifiedStructural(t *testing.T) {
	s, _ := newTestServer(t)
	if s.Embedder == nil {
		s.Embedder = semdiff.LexicalEmbedder{}
	}
	huge := strings.Repeat("x\n", semdiff.MaxLines+1)
	if got := s.classify("short", huge); got != "structural" {
		t.Fatalf("classify(oversized) = %q, want structural", got)
	}
}
