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
func (b *Backend) responderAIKey() string      { return b.key("responder", "accept-ai") }
func (b *Backend) responderKindKey() string    { return b.key("responder", "kind") }
func (b *Backend) requestKindKey(reqID string) string {
	return b.key("request", reqID, "kind")
}
func (b *Backend) requestAIAnswerKey(reqID string) string {
	return b.key("request", reqID, "allow-ai-answer")
}

func (b *Backend) setRequestRouting(ctx context.Context, reqID, requesterKind string, allowAIAnswers bool) error {
	allow := "0"
	if allowAIAnswers {
		allow = "1"
	}
	_, err := b.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, b.requestKindKey(reqID), requesterKind, 0)
		pipe.Set(ctx, b.requestAIAnswerKey(reqID), allow, 0)
		return nil
	})
	return err
}

func (b *Backend) clearRequestRouting(ctx context.Context, reqID string) error {
	return b.rdb.Del(ctx, b.requestKindKey(reqID), b.requestAIAnswerKey(reqID)).Err()
}

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
// KEYS: [1]=avail [2]=queue [3]=presence [4]=responder-ai-pref [5]=responder-kind
// ARGV: [1]=min-fresh-score [2]=lock-ttl-seconds [3]=lock-prefix
//
//	[4]=request-prefix [5]=kind-suffix [6]=allow-ai-answer-suffix
var assignScript = redis.NewScript(`
local minfresh = tonumber(ARGV[1])
local lockttl = tonumber(ARGV[2])
local skipped = {}
local function restoreSkipped()
  for i = #skipped, 1, -1 do
    redis.call('LPUSH', KEYS[1], skipped[i])
  end
end
while true do
  local sid = redis.call('LPOP', KEYS[1])
  if not sid then
    restoreSkipped()
    return nil
  end
  local score = redis.call('ZSCORE', KEYS[3], sid)
  if score and tonumber(score) >= minfresh then
    local acceptai = redis.call('HGET', KEYS[4], sid) or '1'
    local responderkind = redis.call('HGET', KEYS[5], sid) or 'human'
    local queued = redis.call('LRANGE', KEYS[2], 0, -1)
    for _, reqid in ipairs(queued) do
      local requestkind = redis.call('GET', ARGV[4] .. reqid .. ARGV[5]) or 'human'
      local allowaianswer = redis.call('GET', ARGV[4] .. reqid .. ARGV[6]) or '1'
      local compatible = true
      if responderkind == 'ai_persona' and allowaianswer ~= '1' then
        compatible = false
      end
      if requestkind == 'ai_persona' and (acceptai ~= '1' or responderkind == 'ai_persona') then
        compatible = false
      end
      if compatible then
        redis.call('LREM', KEYS[2], 1, reqid)
        restoreSkipped()
        redis.call('SET', ARGV[3] .. reqid, sid, 'EX', lockttl)
        return {reqid, sid}
      end
    end
    table.insert(skipped, sid)
  else
    redis.call('HDEL', KEYS[4], sid)
    redis.call('HDEL', KEYS[5], sid)
  end
end
`)

// assignNext returns (reqID, sessionID, true) for a fresh pairing, or ok=false
// when nothing can be paired right now.
func (b *Backend) assignNext(ctx context.Context) (string, string, bool, error) {
	minFresh := b.clock().Add(-presenceTTL).UnixMilli()
	res, err := assignScript.Run(ctx, b.rdb,
		[]string{b.availKey(), b.queueKey(), b.presenceKey(), b.responderAIKey(), b.responderKindKey()},
		minFresh, int(assignLockTTL.Seconds()), b.key("lock")+":", b.key("request")+":", ":kind", ":allow-ai-answer").Result()
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
