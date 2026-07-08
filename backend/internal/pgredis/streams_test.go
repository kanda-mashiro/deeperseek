package pgredis

import (
	"context"
	"testing"
	"time"

	"deeperseek/backend/internal/core"
)

func TestStreamPubSubDeliversInOrder(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	cleanup, ch := b.streamChannel(ctx, "req1")
	defer cleanup()
	time.Sleep(100 * time.Millisecond) // let SUBSCRIBE settle before publishing

	_ = b.publishStream(ctx, "req1", streamMessage{Kind: "fragment", Text: "hello ", Ordinal: 1})
	_ = b.publishStream(ctx, "req1", streamMessage{Kind: "fragment", Text: "world", Ordinal: 2})
	_ = b.publishStream(ctx, "req1", streamMessage{Kind: "done", Finish: string(core.FinishStop)})

	got := make([]streamMessage, 0, 3)
	for len(got) < 3 {
		select {
		case m := <-ch:
			got = append(got, m)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d messages: %+v", len(got), got)
		}
	}
	if got[0].Text != "hello " || got[1].Text != "world" || got[2].Kind != "done" {
		t.Fatalf("unexpected stream order: %+v", got)
	}
}

func TestAssignmentPubSubDelivers(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	cleanup, ch := b.assignmentChannel(ctx, "sessX")
	defer cleanup()
	time.Sleep(100 * time.Millisecond)

	_ = b.publishAssignment(ctx, "sessX", core.AssignedRequest{
		RequestID: "reqA",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
	})

	select {
	case a := <-ch:
		if a.RequestID != "reqA" || len(a.Messages) != 1 || a.Messages[0].Content != "hi" {
			t.Fatalf("unexpected assignment: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for assignment")
	}
}
