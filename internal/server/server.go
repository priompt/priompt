package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"priompt/internal/auth"
	store "priomptdb"
	pb "priomptproto/gen/priompt/v1"
	"priomptproto/semdiff"
	"priomptproto/validate"
)

// Notifier publishes version-change events with the change's semantic diff
// classification. *pubsub.Bus implements it; nil disables notifications.
type Notifier interface {
	Publish(uri, versionHash, classification string) error
}

type Server struct {
	pb.UnimplementedPromptServiceServer
	Store    *store.Store
	Cache    Cache            // L2 cache (mem or Redis); nil disables it
	Embedder semdiff.Embedder // configured at startup; used by DiffPrompt
	Notifier Notifier         // Phase 4 pub/sub; nil disables it
}

// NewServer wires a server with an L2 cache (nil to disable), the embedder used
// for semantic diffs, and the pub/sub notifier (nil to disable distribution).
func NewServer(st *store.Store, cache Cache, emb semdiff.Embedder, n Notifier) *Server {
	return &Server{Store: st, Cache: cache, Embedder: emb, Notifier: n}
}

// PublishPrompt validates, stores a new prompt version, invalidates its cache
// entry, and notifies subscribers — the write-through publisher path.
func (s *Server) PublishPrompt(ctx context.Context, req *pb.PublishPromptRequest) (*pb.PublishPromptResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	if err := auth.RequireWrite(ctx); err != nil {
		return nil, err
	}
	if err := validate.Prompt(req.GetUri(), req.GetTemplate(), req.GetSlots()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid prompt: %v", err)
	}
	hash := store.Hash(req.GetTemplate(), req.GetSlots())
	branch := branchOr(req.GetBranch())
	onMain := branch == store.DefaultBranch

	// Idempotent on main: republishing the served HEAD's content is a no-op — no
	// write, no notify. Lets `promptctl push` publish every prompt; only changed
	// ones fire. (Branch publishes rely on commit-level idempotency instead.)
	// On a real change, classify the edit so the notification carries the verdict.
	class := ""
	if onMain {
		prev, err := s.Store.Get(ctx, req.GetUri())
		switch {
		case err == nil && prev.VersionHash == hash:
			return &pb.PublishPromptResponse{VersionHash: hash}, nil
		case err == nil:
			class = s.classify(prev.Template, req.GetTemplate())
		case errors.Is(err, store.ErrNotFound):
			class = "new"
		}
	}

	// Commit on the target branch. On main this also materializes the served HEAD
	// (the prompts table) in the same transaction. Author is the caller's org
	// scope; message is carried from the request.
	if _, err := s.Store.Commit(ctx, req.GetUri(), branch, req.GetTemplate(), req.GetSlots(), auth.ScopeOf(ctx), req.GetMessage()); err != nil {
		return nil, status.Errorf(codes.Internal, "store failed: %v", err)
	}
	// Cache and subscribers track the served HEAD only — branch work is invisible
	// until merged into main.
	if onMain {
		if s.Cache != nil {
			s.Cache.Invalidate(req.GetUri())
		}
		if s.Notifier != nil {
			// Best-effort: the version is durably stored even if the notify fails;
			// subscribers still converge on the next TTL poll.
			_ = s.Notifier.Publish(req.GetUri(), hash, class)
		}
	}
	return &pb.PublishPromptResponse{VersionHash: hash}, nil
}

func (s *Server) GetPrompt(ctx context.Context, req *pb.GetPromptRequest) (*pb.GetPromptResponse, error) {
	uri := req.GetUri()
	if err := auth.Authorize(ctx, uri); err != nil {
		return nil, err
	}
	// Fetch a pinned version (branch or commit) instead of the served HEAD.
	if req.GetRef() != "" {
		return s.getByRef(ctx, uri, req.GetRef())
	}
	if s.Cache != nil {
		if resp, ok := s.Cache.Get(uri); ok {
			return resp, nil
		}
	}
	p, err := s.Store.Get(ctx, uri)
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "prompt %q not found", uri)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	// Serve-time validation: never hand a malformed prompt to a production agent,
	// and never cache one (validation before caching).
	if err := validate.Prompt(p.URI, p.Template, p.Slots); err != nil {
		return nil, status.Errorf(codes.DataLoss, "stored prompt invalid: %v", err)
	}
	resp := &pb.GetPromptResponse{
		Uri:         p.URI,
		Template:    p.Template,
		Slots:       p.Slots,
		VersionHash: p.VersionHash,
	}
	if s.Cache != nil {
		s.Cache.Put(uri, resp)
	}
	return resp, nil
}

// ListPrompts browses a repo (a URI prefix) like a filesystem: it returns the
// served-HEAD prompts whose URI starts with the prefix. Auth scopes by the
// prefix's org, so a scoped token can only list within its own org.
func (s *Server) ListPrompts(ctx context.Context, req *pb.ListPromptsRequest) (*pb.ListPromptsResponse, error) {
	prefix := req.GetPrefix()
	if err := auth.Authorize(ctx, prefix); err != nil {
		return nil, err
	}
	prompts, err := s.Store.List(ctx, prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list failed: %v", err)
	}
	entries := make([]*pb.PromptEntry, len(prompts))
	for i, p := range prompts {
		entries[i] = &pb.PromptEntry{Uri: p.URI, VersionHash: p.VersionHash}
	}
	return &pb.ListPromptsResponse{Entries: entries}, nil
}

// DiffPrompt runs the Semantic Propagation Diff between the stored prompt (the
// original) and the supplied edited template, using the server's embedder.
func (s *Server) DiffPrompt(ctx context.Context, req *pb.DiffPromptRequest) (*pb.DiffPromptResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	p, err := s.Store.Get(ctx, req.GetUri())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "prompt %q not found", req.GetUri())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	return s.diff(p.Template, req.GetNewTemplate())
}

// branchOr returns b, or DefaultBranch when b is empty.
func branchOr(b string) string {
	if b == "" {
		return store.DefaultBranch
	}
	return b
}

func branchErr(err error) error {
	if errors.Is(err, store.ErrBranchNotFound) {
		return status.Error(codes.NotFound, "branch not found")
	}
	return status.Errorf(codes.Internal, "%v", err)
}

func (s *Server) History(ctx context.Context, req *pb.HistoryRequest) (*pb.HistoryResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	log, err := s.Store.Log(ctx, req.GetUri(), branchOr(req.GetBranch()))
	if err != nil {
		return nil, branchErr(err)
	}
	out := make([]*pb.Commit, len(log))
	for i, c := range log {
		out[i] = &pb.Commit{
			Hash: c.Hash, VersionHash: c.VersionHash, Parent: c.Parent, Parent2: c.Parent2,
			Author: c.Author, Message: c.Message, CreatedAt: c.CreatedAt.Format(time.RFC3339Nano),
		}
	}
	return &pb.HistoryResponse{Commits: out}, nil
}

func (s *Server) CreateBranch(ctx context.Context, req *pb.CreateBranchRequest) (*pb.CreateBranchResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	if err := auth.RequireWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "branch name required")
	}
	if err := s.Store.Branch(ctx, req.GetUri(), req.GetName(), branchOr(req.GetFrom())); err != nil {
		return nil, branchErr(err)
	}
	c, err := s.Store.Log(ctx, req.GetUri(), req.GetName())
	if err != nil {
		return nil, branchErr(err)
	}
	return &pb.CreateBranchResponse{CommitHash: c[0].Hash}, nil
}

func (s *Server) MergeBranch(ctx context.Context, req *pb.MergeBranchRequest) (*pb.MergeBranchResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	if err := auth.RequireWrite(ctx); err != nil {
		return nil, err
	}
	hash, err := s.Store.Merge(ctx, req.GetUri(), branchOr(req.GetInto()), req.GetFrom(), auth.ScopeOf(ctx), req.GetMessage())
	if err != nil {
		return nil, branchErr(err)
	}
	// Merging into main moves the served HEAD — invalidate its cache.
	if s.Cache != nil && branchOr(req.GetInto()) == store.DefaultBranch {
		s.Cache.Invalidate(req.GetUri())
	}
	return &pb.MergeBranchResponse{CommitHash: hash}, nil
}

func (s *Server) DiffCommits(ctx context.Context, req *pb.DiffCommitsRequest) (*pb.DiffPromptResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	from, err := s.Store.GetCommit(ctx, req.GetFromHash())
	if err != nil {
		return nil, diffCommitErr(err)
	}
	to, err := s.Store.GetCommit(ctx, req.GetToHash())
	if err != nil {
		return nil, diffCommitErr(err)
	}
	return s.diff(from.Template, to.Template)
}

func diffCommitErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return status.Error(codes.NotFound, "commit not found")
	}
	return status.Errorf(codes.Internal, "%v", err)
}

// getByRef serves a specific version (branch tip or commit) rather than the
// served HEAD — the version-pinning path. Not cached: pinned reads are rarer
// than HEAD reads, and the cache is keyed by URI alone.
func (s *Server) getByRef(ctx context.Context, uri, ref string) (*pb.GetPromptResponse, error) {
	c, err := s.Store.Resolve(ctx, uri, ref)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBranchNotFound) {
		return nil, status.Errorf(codes.NotFound, "ref %q not found for %q", ref, uri)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve failed: %v", err)
	}
	if err := validate.Prompt(uri, c.Template, c.Slots); err != nil {
		return nil, status.Errorf(codes.DataLoss, "stored prompt invalid: %v", err)
	}
	return &pb.GetPromptResponse{Uri: uri, Template: c.Template, Slots: c.Slots, VersionHash: c.VersionHash, CommitHash: c.Hash}, nil
}

// SetBranch points a branch at an existing commit (rollback / pin). Moving main
// changes the served HEAD, so it invalidates the cache and notifies subscribers
// with the classification of the change away from the old HEAD.
func (s *Server) SetBranch(ctx context.Context, req *pb.SetBranchRequest) (*pb.SetBranchResponse, error) {
	if err := auth.Authorize(ctx, req.GetUri()); err != nil {
		return nil, err
	}
	if err := auth.RequireWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetCommitHash() == "" {
		return nil, status.Error(codes.InvalidArgument, "commit_hash required")
	}
	branch := branchOr(req.GetBranch())
	prevT := ""
	if branch == store.DefaultBranch {
		if prev, err := s.Store.Get(ctx, req.GetUri()); err == nil {
			prevT = prev.Template
		}
	}
	c, err := s.Store.SetBranch(ctx, req.GetUri(), branch, req.GetCommitHash())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "commit %q not found for %q", req.GetCommitHash(), req.GetUri())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set branch failed: %v", err)
	}
	if branch == store.DefaultBranch {
		if s.Cache != nil {
			s.Cache.Invalidate(req.GetUri())
		}
		if s.Notifier != nil {
			_ = s.Notifier.Publish(req.GetUri(), c.VersionHash, s.classify(prevT, c.Template))
		}
	}
	return &pb.SetBranchResponse{VersionHash: c.VersionHash}, nil
}

// classify returns the semantic diff verdict for an edit from oldT to newT, ""
// when there is no embedder, no prior version, or the diff errors.
func (s *Server) classify(oldT, newT string) string {
	if s.Embedder == nil || oldT == "" {
		return ""
	}
	res, err := semdiff.Analyze(s.Embedder, splitLines(oldT), splitLines(newT))
	if err != nil {
		return ""
	}
	return semdiff.Worst(res)
}

// diff runs the semantic propagation diff between two templates and maps it to
// the wire type. Shared by DiffPrompt and DiffCommits.
func (s *Server) diff(oldT, newT string) (*pb.DiffPromptResponse, error) {
	results, err := semdiff.Analyze(s.Embedder, splitLines(oldT), splitLines(newT))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "diff failed: %v", err)
	}
	resp := &pb.DiffPromptResponse{Changes: make([]*pb.Change, len(results))}
	for i, r := range results {
		resp.Changes[i] = &pb.Change{
			OldStart:       int32(r.Change.OldStart),
			OldEnd:         int32(r.Change.OldEnd),
			NewStart:       int32(r.Change.NewStart),
			NewEnd:         int32(r.Change.NewEnd),
			Kind:           semdiff.Kind(r.Change),
			PointDelta:     r.Signal2,
			Up:             toWindows(r.Up.Curve),
			Down:           toWindows(r.Down.Curve),
			UpBoundary:     r.Up.StoppedAtBoundary,
			DownBoundary:   r.Down.StoppedAtBoundary,
			Classification: r.Class,
		}
	}
	return resp, nil
}

func toWindows(ws []semdiff.Window) []*pb.Window {
	out := make([]*pb.Window, len(ws))
	for i, w := range ws {
		out[i] = &pb.Window{Radius: int32(w.Radius), Delta: w.Delta}
	}
	return out
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
