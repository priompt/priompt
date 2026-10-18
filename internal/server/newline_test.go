package server

import (
	"context"
	"strings"
	"testing"

	pb "github.com/priompt/proto/gen/priompt/v1"
	"github.com/priompt/proto/semdiff"
)

// A trailing newline (added by files and `echo`) must not change the diff:
// stored "a" vs edited "a\n" is no change, and a one-line edit stays one change.
func TestDiffIgnoresTrailingNewline(t *testing.T) {
	s, _ := newTestServer(t)
	s.Embedder = semdiff.LexicalEmbedder{}
	ctx := context.Background()
	const uri = "priompt://acme/onboarding/welcome"
	if _, err := s.PublishPrompt(ctx, &pb.PublishPromptRequest{
		Uri: uri, Template: "Hi {name}, welcome to {org}!", Slots: []string{"name", "org"},
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, edited string
		wantChanges  int
	}{
		{"same text plus newline", "Hi {name}, welcome to {org}!\n", 0},
		{"same text plus CRLF", "Hi {name}, welcome to {org}!\r\n", 0},
		{"one-line edit plus newline", "Hi {name}, welcome to {org}! Glad you are here.\n", 1},
		{"one-line edit, no newline", "Hi {name}, welcome to {org}! Glad you are here.", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := s.DiffPrompt(ctx, &pb.DiffPromptRequest{Uri: uri, NewTemplate: c.edited})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(resp.GetChanges()); got != c.wantChanges {
				t.Fatalf("got %d changes, want %d: %v", got, c.wantChanges, resp.GetChanges())
			}
			for _, ch := range resp.GetChanges() {
				if ch.GetKind() != "replace" {
					t.Errorf("change kind = %q, want replace", ch.GetKind())
				}
			}
		})
	}
}

func TestSplitLinesTrailingNewline(t *testing.T) {
	for _, pair := range [][2]string{{"a", "a\n"}, {"a", "a\r\n"}, {"a\nb", "a\nb\n"}, {"a\r\nb", "a\nb\n"}} {
		x, y := splitLines(pair[0]), splitLines(pair[1])
		if strings.Join(x, "|") != strings.Join(y, "|") {
			t.Errorf("splitLines(%q) = %q, splitLines(%q) = %q; want equal", pair[0], x, pair[1], y)
		}
	}
	// A blank last line is content, not the terminator.
	if a, b := splitLines("a\n"), splitLines("a\n\n"); len(a) == len(b) {
		t.Errorf("splitLines dropped a blank line: %q vs %q", a, b)
	}
}
