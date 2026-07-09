package pgredis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func (b *Backend) OnlineResponderCount() int {
	n, err := b.onlineResponders(context.Background())
	if err != nil {
		return 0
	}
	return n
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
