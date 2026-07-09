package pgredis

import (
	"context"
	"testing"
	"time"
)

func TestPersonaLeaderLeaseIsOwnerChecked(t *testing.T) {
	b := backendForTest(t)

	if !b.TryPersonaLeader("podA", 15*time.Second) {
		t.Fatal("a free lease should be acquired")
	}
	if !b.TryPersonaLeader("podA", 15*time.Second) {
		t.Fatal("the incumbent should renew its own lease")
	}
	if b.TryPersonaLeader("podB", 15*time.Second) {
		t.Fatal("a different pod must not steal a held lease")
	}
}

func TestPersonaPresenceCounts(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	if b.OnlineResponderCount() != 0 || b.QueuedRequestCount() != 0 {
		t.Fatalf("expected empty counts, got online=%d queued=%d", b.OnlineResponderCount(), b.QueuedRequestCount())
	}
	_ = b.heartbeat(ctx, "r1")
	_ = b.enqueueRequest(ctx, "req1")
	_ = b.enqueueRequest(ctx, "req2")
	if b.OnlineResponderCount() != 1 {
		t.Fatalf("expected 1 online responder, got %d", b.OnlineResponderCount())
	}
	if b.QueuedRequestCount() != 2 {
		t.Fatalf("expected 2 queued, got %d", b.QueuedRequestCount())
	}
}
