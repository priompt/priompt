package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/priompt/auth/authn"
	pb "github.com/priompt/proto/gen/priompt/v1"
	"github.com/priompt/proto/semdiff"
)

// DiffCommits authorizes the request URI, so both commits must belong to that
// URI. A token scoped to one org must not be able to diff another org's commits
// by naming its own prompt alongside foreign commit hashes.
func TestDiffCommitsRequiresCommitsOfURI(t *testing.T) {
	s, _ := newTestServer(t)
	s.Embedder = semdiff.LexicalEmbedder{}
	admin := context.Background()

	commits := func(uri string, templates ...string) []string {
		t.Helper()
		for _, tmpl := range templates {
			if _, err := s.PublishPrompt(admin, &pb.PublishPromptRequest{Uri: uri, Template: tmpl}); err != nil {
				t.Fatal(err)
			}
		}
		h, err := s.History(admin, &pb.HistoryRequest{Uri: uri})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range h.GetCommits() {
			out = append(out, c.GetHash())
		}
		return out
	}
	const acmeURI, betaURI = "priompt://acme/r/p", "priompt://beta/r/p"
	acme := commits(acmeURI, "acme one", "acme two")
	beta := commits(betaURI, "beta one", "beta two")
	other := commits("priompt://acme/r/other", "other one")

	ctx := authn.WithIdentity(admin, authn.Identity{Org: "acme"})

	// Own commits on own prompt: allowed.
	if _, err := s.DiffCommits(ctx, &pb.DiffCommitsRequest{Uri: acmeURI, FromHash: acme[1], ToHash: acme[0]}); err != nil {
		t.Fatalf("diff of own commits: %v", err)
	}

	for name, req := range map[string]*pb.DiffCommitsRequest{
		"both foreign":          {Uri: acmeURI, FromHash: beta[1], ToHash: beta[0]},
		"from foreign":          {Uri: acmeURI, FromHash: beta[1], ToHash: acme[0]},
		"to foreign":            {Uri: acmeURI, FromHash: acme[1], ToHash: beta[0]},
		"other prompt same org": {Uri: acmeURI, FromHash: acme[1], ToHash: other[0]},
	} {
		_, err := s.DiffCommits(ctx, req)
		if status.Code(err) != codes.NotFound {
			t.Errorf("%s: code = %v (%v), want NotFound", name, status.Code(err), err)
		}
	}
	// Even an admin may only diff commits of the URI it names.
	if _, err := s.DiffCommits(admin, &pb.DiffCommitsRequest{Uri: acmeURI, FromHash: beta[1], ToHash: beta[0]}); status.Code(err) != codes.NotFound {
		t.Errorf("admin cross-URI diff: code = %v, want NotFound", status.Code(err))
	}
}
