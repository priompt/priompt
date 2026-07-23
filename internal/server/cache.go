package server

import (
	"sync"
	"time"

	pb "priomptproto/gen/priompt/v1"
)

// Cache is the L2 (server-side) cache. Two implementations: an in-process map
// (the zero-dependency default) and Redis (for multi-node deployments). Only
// validated prompts are ever Put. TTL is the convergence mechanism — a republish
// is reflected within ttl, and a changed prompt yields a new version_hash.
type Cache interface {
	Get(uri string) (*pb.GetPromptResponse, bool)
	Put(uri string, resp *pb.GetPromptResponse)
	Invalidate(uri string)
}

// NewMemCache returns the in-process cache, or nil (caching disabled) when ttl
// is non-positive.
// ponytail: unbounded map (entries bounded by prompt count, which is small).
// Use Redis or add LRU only when serving goes multi-node or memory gets tight.
func NewMemCache(ttl time.Duration) Cache {
	if ttl <= 0 {
		return nil
	}
	return &ttlCache{ttl: ttl, m: map[string]cacheEntry{}}
}

type ttlCache struct {
	ttl time.Duration
	mu  sync.RWMutex
	m   map[string]cacheEntry
}

type cacheEntry struct {
	resp   *pb.GetPromptResponse
	expiry time.Time
}

func (c *ttlCache) Get(uri string) (*pb.GetPromptResponse, bool) {
	c.mu.RLock()
	e, ok := c.m[uri]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiry) {
		return nil, false
	}
	return e.resp, true
}

func (c *ttlCache) Put(uri string, resp *pb.GetPromptResponse) {
	c.mu.Lock()
	c.m[uri] = cacheEntry{resp: resp, expiry: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// Invalidate drops a URI so the next read reflects a just-published version.
func (c *ttlCache) Invalidate(uri string) {
	c.mu.Lock()
	delete(c.m, uri)
	c.mu.Unlock()
}
