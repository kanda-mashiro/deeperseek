package pgredis

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	presenceTTL       = 15 * time.Second
	assignLockTTL     = 35 * time.Second
	heartbeatInterval = 5 * time.Second
)

func (b *Backend) queueKey() string            { return b.key("queue") }
func (b *Backend) availKey() string            { return b.key("avail") }
func (b *Backend) presenceKey() string         { return b.key("presence") }
func (b *Backend) lockKey(reqID string) string { return b.key("lock", reqID) }

// --- waiting queue (oldest at head) ---

func (b *Backend) enqueueRequest(ctx context.Context, reqID string) error {
	return b.rdb.RPush(ctx, b.queueKey(), reqID).Err()
}

func (b *Backend) removeQueued(ctx context.Context, reqID string) error {
	return b.rdb.LRem(ctx, b.queueKey(), 0, reqID).Err()
}

// --- responder presence (server-driven heartbeat) ---

func (b *Backend) heartbeat(ctx context.Context, sid string) error {
	return b.rdb.ZAdd(ctx, b.presenceKey(), redis.Z{Score: float64(b.clock().UnixMilli()), Member: sid}).Err()
}

func (b *Backend) dropPresence(ctx context.Context, sid string) error {
	return b.rdb.ZRem(ctx, b.presenceKey(), sid).Err()
}

func (b *Backend) onlineResponders(ctx context.Context) (int, error) {
	min := strconv.FormatInt(b.clock().Add(-presenceTTL).UnixMilli(), 10)
	n, err := b.rdb.ZCount(ctx, b.presenceKey(), min, "+inf").Result()
	return int(n), err
}

// --- available set (idempotent so a responder is never listed twice) ---

func (b *Backend) addAvailable(ctx context.Context, sid string) error {
	if err := b.rdb.LRem(ctx, b.availKey(), 0, sid).Err(); err != nil {
		return err
	}
	return b.rdb.RPush(ctx, b.availKey(), sid).Err()
}

func (b *Backend) removeAvailable(ctx context.Context, sid string) error {
	return b.rdb.LRem(ctx, b.availKey(), 0, sid).Err()
}

// assignScript atomically pairs the oldest queued request with the first
// available responder that is still present, dropping stale responders it passes
// and returning the present responder to the head if no request is waiting. A
// request is never removed from the queue without a validated responder, and the
// atomic LPOP guarantees a request is handed to exactly one responder across all
// instances (SPEC 4.3).
//
// KEYS: [1]=avail [2]=queue [3]=presence [4]=lock-prefix
// ARGV: [1]=min-fresh-score [2]=lock-ttl-seconds
var assignScript = redis.NewScript(`
local minfresh = tonumber(ARGV[1])
local lockttl = tonumber(ARGV[2])
while true do
  local sid = redis.call('LPOP', KEYS[1])
  if not sid then return nil end
  local score = redis.call('ZSCORE', KEYS[3], sid)
  if score and tonumber(score) >= minfresh then
    local reqid = redis.call('LPOP', KEYS[2])
    if not reqid then
      redis.call('LPUSH', KEYS[1], sid)
      return nil
    end
    redis.call('SET', KEYS[4] .. reqid, sid, 'EX', lockttl)
    return {reqid, sid}
  end
end
`)

// assignNext returns (reqID, sessionID, true) for a fresh pairing, or ok=false
// when nothing can be paired right now.
func (b *Backend) assignNext(ctx context.Context) (string, string, bool, error) {
	minFresh := b.clock().Add(-presenceTTL).UnixMilli()
	res, err := assignScript.Run(ctx, b.rdb,
		[]string{b.availKey(), b.queueKey(), b.presenceKey(), b.key("lock") + ":"},
		minFresh, int(assignLockTTL.Seconds())).Result()
	if err == redis.Nil || res == nil {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	pair, ok := res.([]interface{})
	if !ok || len(pair) != 2 {
		return "", "", false, nil
	}
	return toString(pair[0]), toString(pair[1]), true, nil
}

func (b *Backend) releaseLock(ctx context.Context, reqID string) error {
	return b.rdb.Del(ctx, b.lockKey(reqID)).Err()
}

func toString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return ""
	}
}
