package pgredis

import (
	"context"
	"errors"
	"testing"

	"deeperseek/backend/internal/core"
)

func insertRequest(t *testing.T, b *Backend, id, status string) {
	t.Helper()
	_, err := b.pool.Exec(context.Background(),
		`INSERT INTO requests (id, requester_id, status, frozen_points, output_limit, messages)
		 VALUES ($1, $2, $3, 0, $4, $5)`,
		id, "usr_x", status, core.OutputLimitChars, []byte(`[{"role":"user","content":"why blue sky"}]`))
	if err != nil {
		t.Fatalf("insert request: %v", err)
	}
}

func insertFragment(t *testing.T, b *Backend, id, reqID, sid string, seq int64, ordinal int, text string) {
	t.Helper()
	_, err := b.pool.Exec(context.Background(),
		`INSERT INTO fragments (id, request_id, responder_session_id, client_seq, ordinal, text)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, reqID, sid, seq, ordinal, text)
	if err != nil {
		t.Fatalf("insert fragment: %v", err)
	}
}

func TestRequestSnapshotConcatenatesFragmentsByOrdinal(t *testing.T) {
	b := backendForTest(t)

	if _, _, err := b.RequestSnapshot("missing"); !errors.Is(err, core.ErrRequestNotFound) {
		t.Fatalf("expected ErrRequestNotFound, got %v", err)
	}

	insertRequest(t, b, "r1", string(core.StatusStreaming))
	// insert out of ordinal order to prove ordering is by ordinal, not insert time
	insertFragment(t, b, "f2", "r1", "s1", 2, 2, "world")
	insertFragment(t, b, "f1", "r1", "s1", 1, 1, "hello ")

	req, text, err := b.RequestSnapshot("r1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if req.Status != core.StatusStreaming || len(req.Messages) != 1 || req.Messages[0].Content != "why blue sky" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if text != "hello world" {
		t.Fatalf("expected concatenated answer, got %q", text)
	}
}

func TestFallbackStillWantedTransitions(t *testing.T) {
	b := backendForTest(t)

	if b.FallbackStillWanted("nope") {
		t.Fatal("missing request should not want fallback")
	}

	insertRequest(t, b, "r2", string(core.StatusQueued))
	if !b.FallbackStillWanted("r2") {
		t.Fatal("queued request with no fragments should want fallback")
	}

	insertFragment(t, b, "frag", "r2", "s1", 1, 1, "answer")
	if b.FallbackStillWanted("r2") {
		t.Fatal("request with a committed fragment should not want fallback")
	}
}

func TestBoardProjectsEligibleRequests(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	guest := b.GuestSession("")
	req, err := b.CreateRequest(ctx, guest.Token, "m", []core.Message{{Role: "user", Content: "why is the sky blue"}}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// a registered user's request must NOT appear on the public board
	reg, _ := b.Register("alice", "Alice", "pass1234", "pass1234")
	if _, err := b.CreateRequest(ctx, reg.Token, "m", []core.Message{{Role: "user", Content: "private"}}, 0); err != nil {
		t.Fatalf("registered create: %v", err)
	}

	board, err := b.Board(50)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	if len(board) != 1 || board[0].RequestID != req.ID {
		t.Fatalf("board should list only the guest request, got %+v", board)
	}
	if board[0].Category == "" || board[0].Status != core.StatusQueued {
		t.Fatalf("unexpected ticket: %+v", board[0])
	}
}

func TestActiveRequestForResponder(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()

	insertRequest(t, b, "r3", string(core.StatusAssigned))
	if _, err := b.pool.Exec(ctx, `UPDATE requests SET responder_session_id = $1 WHERE id = $2`, "sess9", "r3"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	got, err := b.activeRequestForResponder(ctx, "sess9")
	if err != nil || got != "r3" {
		t.Fatalf("expected r3, got %q err=%v", got, err)
	}

	// once terminal, the responder has no active request
	if _, err := b.pool.Exec(ctx, `UPDATE requests SET status = 'completed' WHERE id = $1`, "r3"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err = b.activeRequestForResponder(ctx, "sess9")
	if err != nil || got != "" {
		t.Fatalf("expected no active request, got %q err=%v", got, err)
	}
}
