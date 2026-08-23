package server

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	store "priomptdb"
	pb "priomptproto/gen/priompt/v1"
)

// redisCache is the L2 cache backed by Redis — the shared cache for multi-node
// deployments. Responses are stored as marshaled protobuf with the TTL applied
// as Redis key expiry.
//
// Reads are best-effort: a Redis hiccup degrades to a cache miss, never a
// serving error. Invalidation is not, and that asymmetry is deliberate — see
// Invalidate.
//
// Values are sealed with the same key as the database when encryption is
// configured. Redis is a second persistent store (it snapshots to disk), on a
// second host, usually with weaker access control; writing decrypted prompt
// bodies there would have voided the at-rest guarantee for every prompt that had
// been read recently, which is precisely the set an attacker would want.
type redisCache struct {
	rdb    *redis.Client
	ttl    time.Duration
	sealer *store.Sealer
}

// NewRedisCache connects to Redis from a redis:// URL (e.g.
// redis://:pass@host:6379/0) and verifies the connection.
func NewRedisCache(url string, ttl time.Duration, sealer *store.Sealer) (Cache, error) {
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
	return &redisCache{rdb: rdb, ttl: ttl, sealer: sealer}, nil
}

func redisKey(uri string) string { return "priompt:cache:" + uri }

func (c *redisCache) Get(uri string) (*pb.GetPromptResponse, bool) {
	b, err := c.rdb.Get(context.Background(), redisKey(uri)).Bytes()
	if err != nil {
		return nil, false // miss or error — both mean "not cached"
	}
	b, err = c.sealer.OpenBytes(b)
	if err != nil {
		return nil, false // wrong key, or a tampered entry — treat as a miss
	}
	var resp pb.GetPromptResponse
	if proto.Unmarshal(b, &resp) != nil {
		return nil, false
	}
	return &resp, true
}

func (c *redisCache) Put(uri string, resp *pb.GetPromptResponse) {
	if b, err := proto.Marshal(resp); err == nil {
		c.rdb.Set(context.Background(), redisKey(uri), c.sealer.SealBytes(b), c.ttl)
	}
}

// Invalidate drops a URI so the next read reflects the just-published version,
// and reports whether it managed to.
//
// The error return is the point. This used to discard the Del result, and the
// interface returned nothing, so a publish could not find out that invalidation
// had failed. A Redis blip during a publish therefore left every node in the
// cluster serving superseded content for the rest of the TTL, while the write
// was reported as successful and subscribers had already been told the version
// changed. The sharp edge was rollback: the emergency lever reported success and
// did nothing for readers. A publish that cannot invalidate is not complete, and
// the caller has to be told.
func (c *redisCache) Invalidate(uri string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.rdb.Del(ctx, redisKey(uri)).Err()
}
