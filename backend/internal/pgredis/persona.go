package pgredis

import (
	"context"
	"strconv"
	"time"

	"deeperseek/backend/internal/core"

	"github.com/redis/go-redis/v9"
)

func (b *Backend) OnlineResponderCount() int {
	n, err := b.onlineResponders(context.Background())
	if err != nil {
		return 0
	}
	return n
}

func (b *Backend) OnlineHumanResponderCount() int {
	ctx := context.Background()
	min := strconv.FormatInt(b.clock().Add(-presenceTTL).UnixMilli(), 10)
	sessionIDs, err := b.rdb.ZRangeByScore(ctx, b.presenceKey(), &redis.ZRangeBy{
		Min: min,
		Max: "+inf",
	}).Result()
	if err != nil || len(sessionIDs) == 0 {
		return 0
	}
	var count int
	if err := b.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = ANY($1::text[]) AND kind <> $2`,
		sessionIDs, core.KindAIPersona).Scan(&count); err != nil {
		return 0
	}
	return count
}

func (b *Backend) QueuedRequestCount() int {
	n, err := b.rdb.LLen(context.Background(), b.queueKey()).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// personaLeaseScript acquires or renews the leadership lease with an owner check:
// it succeeds only if the key is free or already held by this pod, so a lagging
// pod can never clobber the incumbent's lease.
var personaLeaseScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if (not cur) or cur == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
  return 1
end
return 0
`)

func (b *Backend) TryPersonaLeader(podID string, ttl time.Duration) bool {
	res, err := personaLeaseScript.Run(context.Background(), b.rdb,
		[]string{b.key("persona", "leader")}, podID, int(ttl.Seconds())).Result()
	if err != nil {
		return false
	}
	n, ok := res.(int64)
	return ok && n == 1
}
