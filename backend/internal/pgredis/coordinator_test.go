package pgredis

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestAssignmentIsAtomicOldestFirstAndSkipsStale(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	// nothing queued, nobody available
	if _, _, ok, err := b.assignNext(ctx); err != nil || ok {
		t.Fatalf("empty assign should be a no-op, ok=%v err=%v", ok, err)
	}

	if err := b.enqueueRequest(ctx, "req1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_ = b.enqueueRequest(ctx, "req2")

	// an available responder that never heartbeated is stale -> skipped and dropped
	_ = b.addAvailable(ctx, "ghost")
	if _, _, ok, _ := b.assignNext(ctx); ok {
		t.Fatal("an absent responder must not receive an assignment")
	}
	if n, _ := b.rdb.LLen(ctx, b.availKey()).Result(); n != 0 {
		t.Fatalf("stale responder should be evicted from avail, len=%d", n)
	}
	// requests were not consumed by the stale pass
	if n, _ := b.rdb.LLen(ctx, b.queueKey()).Result(); n != 2 {
		t.Fatalf("queue should be untouched, len=%d", n)
	}

	// a present responder gets the oldest request, and the lock is set
	_ = b.heartbeat(ctx, "bob")
	_ = b.addAvailable(ctx, "bob")
	reqID, sid, ok, err := b.assignNext(ctx)
	if err != nil || !ok || reqID != "req1" || sid != "bob" {
		t.Fatalf("expected req1->bob, got %s->%s ok=%v err=%v", reqID, sid, ok, err)
	}
	if got, _ := b.rdb.Get(ctx, b.lockKey("req1")).Result(); got != "bob" {
		t.Fatalf("lock should point to bob, got %q", got)
	}

	// bob is consumed from avail; nothing left to pair
	if _, _, ok, _ := b.assignNext(ctx); ok {
		t.Fatal("no available responder remains")
	}

	// addAvailable is idempotent
	_ = b.addAvailable(ctx, "bob")
	_ = b.addAvailable(ctx, "bob")
	if n, _ := b.rdb.LLen(ctx, b.availKey()).Result(); n != 1 {
		t.Fatalf("addAvailable must dedupe, len=%d", n)
	}

	reqID, sid, ok, _ = b.assignNext(ctx)
	if !ok || reqID != "req2" || sid != "bob" {
		t.Fatalf("expected req2->bob, got %s->%s ok=%v", reqID, sid, ok)
	}
}

func TestPresenceFreshnessAndOnlineCount(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	_ = b.heartbeat(ctx, "fresh")
	// a stale presence entry written directly with an old score
	staleScore := float64(b.clock().Add(-2 * presenceTTL).UnixMilli())
	_ = b.rdb.ZAdd(ctx, b.presenceKey(), redis.Z{Score: staleScore, Member: "old"}).Err()

	n, err := b.onlineResponders(ctx)
	if err != nil {
		t.Fatalf("online count: %v", err)
	}
	if n != 1 {
		t.Fatalf("only the fresh responder should count as online, got %d", n)
	}

	// a stale-but-available responder is not assignable
	_ = b.enqueueRequest(ctx, "reqX")
	_ = b.addAvailable(ctx, "old")
	if _, _, ok, _ := b.assignNext(ctx); ok {
		t.Fatal("stale responder must not be assigned")
	}

	// dropPresence removes a responder from the online set
	_ = b.dropPresence(ctx, "fresh")
	if n, _ := b.onlineResponders(ctx); n != 0 {
		t.Fatalf("dropped responder should not count, got %d", n)
	}
}
