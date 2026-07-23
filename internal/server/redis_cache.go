package server

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	pb "priomptproto/gen/priompt/v1"
)

// redisCache is the L2 cache backed by Redis — the shared cache for multi-node
// deployments. Responses are stored as marshaled protobuf with the TTL applied
// as Redis key expiry. All ops are best-effort: a Redis hiccup degrades to a
// cache miss, never a serving error.
type redisCache struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisCache connects to Redis from a redis:// URL (e.g.
// redis://:pass@host:6379/0) and verifies the connection.
func NewRedisCache(url string, ttl time.Duration) (Cache, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return &redisCache{rdb: rdb, ttl: ttl}, nil
}

func redisKey(uri string) string { return "priompt:cache:" + uri }

func (c *redisCache) Get(uri string) (*pb.GetPromptResponse, bool) {
	b, err := c.rdb.Get(context.Background(), redisKey(uri)).Bytes()
	if err != nil {
		return nil, false // miss or error — both mean "not cached"
	}
	var resp pb.GetPromptResponse
	if proto.Unmarshal(b, &resp) != nil {
		return nil, false
	}
	return &resp, true
}

func (c *redisCache) Put(uri string, resp *pb.GetPromptResponse) {
	if b, err := proto.Marshal(resp); err == nil {
		c.rdb.Set(context.Background(), redisKey(uri), b, c.ttl)
	}
}

func (c *redisCache) Invalidate(uri string) {
	c.rdb.Del(context.Background(), redisKey(uri))
}
