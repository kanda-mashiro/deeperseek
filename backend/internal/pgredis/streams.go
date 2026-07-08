package pgredis

import (
	"context"
	"encoding/json"
	"sync"

	"deeperseek/backend/internal/core"
)

// streamMessage is the pub/sub payload for a request's answer stream. It is only
// a low-latency wake-up; Postgres is the source of truth for stream content.
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

// streamChannel subscribes to a request's stream and returns a channel plus a
// cleanup func. cleanup is idempotent and unblocks the forwarder even if it is
// parked on a send (so a slow/gone consumer cannot leak the goroutine or the
// PubSub); it closes the message channel.
func (b *Backend) streamChannel(ctx context.Context, reqID string) (func(), <-chan streamMessage) {
	ps := b.rdb.Subscribe(ctx, b.streamKey(reqID))
	out := make(chan streamMessage, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		defer ps.Close()
		ch := ps.Channel()
		for {
			select {
			case <-done:
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				var sm streamMessage
				if json.Unmarshal([]byte(m.Payload), &sm) != nil {
					continue
				}
				select {
				case out <- sm:
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, out
}

func (b *Backend) publishAssignment(ctx context.Context, sid string, a core.AssignedRequest) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return b.rdb.Publish(ctx, b.assignKey(sid), data).Err()
}

// assignmentChannel subscribes to a responder's assignment inbox; cleanup is
// idempotent and unblocks a parked forwarder, closing the channel.
func (b *Backend) assignmentChannel(ctx context.Context, sid string) (func(), <-chan core.AssignedRequest) {
	ps := b.rdb.Subscribe(ctx, b.assignKey(sid))
	out := make(chan core.AssignedRequest, 8)
	done := make(chan struct{})
	go func() {
		defer close(out)
		defer ps.Close()
		ch := ps.Channel()
		for {
			select {
			case <-done:
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				var a core.AssignedRequest
				if json.Unmarshal([]byte(m.Payload), &a) != nil {
					continue
				}
				select {
				case out <- a:
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, out
}
