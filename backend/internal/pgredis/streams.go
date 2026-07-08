package pgredis

import (
	"context"
	"encoding/json"

	"deeperseek/backend/internal/core"

	"github.com/redis/go-redis/v9"
)

// streamMessage is the pub/sub payload for a request's answer stream. Ordinal is
// carried so a cross-instance subscriber can drop duplicates and detect gaps; it
// is internal (core.StreamEvent needs no change).
type streamMessage struct {
	Kind    string `json:"kind"` // "fragment" | "done"
	Text    string `json:"text,omitempty"`
	Finish  string `json:"finish,omitempty"`
	Ordinal int    `json:"ordinal,omitempty"`
}

func (b *Backend) streamKey(reqID string) string { return b.key("stream", reqID) }
func (b *Backend) assignKey(sid string) string   { return b.key("assign", sid) }

func (b *Backend) publishStream(ctx context.Context, reqID string, msg streamMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return b.rdb.Publish(ctx, b.streamKey(reqID), data).Err()
}

// streamChannel subscribes to a request's stream. The returned PubSub must be
// Closed by the caller, which also closes the message channel.
func (b *Backend) streamChannel(ctx context.Context, reqID string) (*redis.PubSub, <-chan streamMessage) {
	ps := b.rdb.Subscribe(ctx, b.streamKey(reqID))
	out := make(chan streamMessage, 64)
	go func() {
		defer close(out)
		for m := range ps.Channel() {
			var sm streamMessage
			if json.Unmarshal([]byte(m.Payload), &sm) == nil {
				out <- sm
			}
		}
	}()
	return ps, out
}

func (b *Backend) publishAssignment(ctx context.Context, sid string, a core.AssignedRequest) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return b.rdb.Publish(ctx, b.assignKey(sid), data).Err()
}

// assignmentChannel subscribes to a responder's assignment inbox. The returned
// PubSub must be Closed by the caller.
func (b *Backend) assignmentChannel(ctx context.Context, sid string) (*redis.PubSub, <-chan core.AssignedRequest) {
	ps := b.rdb.Subscribe(ctx, b.assignKey(sid))
	out := make(chan core.AssignedRequest, 8)
	go func() {
		defer close(out)
		for m := range ps.Channel() {
			var a core.AssignedRequest
			if json.Unmarshal([]byte(m.Payload), &a) == nil {
				out <- a
			}
		}
	}()
	return ps, out
}
