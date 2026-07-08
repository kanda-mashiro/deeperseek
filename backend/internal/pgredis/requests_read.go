package pgredis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"deeperseek/backend/internal/core"

	"github.com/jackc/pgx/v5"
)

// notTerminalSQL matches requests that are still live (not completed/abandoned).
const notTerminalSQL = `status NOT IN ('completed', 'timeout_completed', 'abandoned')`

const requestCols = `id, requester_id, requester_session_id, requester_guest, messages, model,
	status, responder_session_id, responder_user_id, responder_guest, frozen_points,
	question_charged, output_limit, finish_reason, reaction, created_at, updated_at, completed_at,
	requester_kind, responder_kind, responder_display`

func isTerminalStatus(status core.RequestStatus) bool {
	return status == core.StatusCompleted || status == core.StatusTimeoutCompleted || status == core.StatusAbandoned
}

func scanRequest(row pgx.Row) (*core.Request, error) {
	var r core.Request
	var msgs []byte
	var status, finish, reaction string
	var completedAt *time.Time
	if err := row.Scan(
		&r.ID, &r.RequesterID, &r.RequesterSessionID, &r.RequesterGuest, &msgs, &r.Model,
		&status, &r.ResponderSessionID, &r.ResponderUserID, &r.ResponderGuest, &r.FrozenPoints,
		&r.QuestionCharged, &r.OutputLimit, &finish, &reaction, &r.CreatedAt, &r.UpdatedAt, &completedAt,
		&r.RequesterKind, &r.ResponderKind, &r.ResponderDisplay,
	); err != nil {
		return nil, err
	}
	if len(msgs) > 0 {
		_ = json.Unmarshal(msgs, &r.Messages)
	}
	r.Status = core.RequestStatus(status)
	r.FinishReason = core.FinishReason(finish)
	r.Reaction = core.Reaction(reaction)
	if completedAt != nil {
		r.CompletedAt = *completedAt
	}
	return &r, nil
}

func (b *Backend) RequestSnapshot(requestID string) (*core.Request, string, error) {
	ctx := context.Background()
	req, err := scanRequest(b.pool.QueryRow(ctx, `SELECT `+requestCols+` FROM requests WHERE id = $1`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", core.ErrRequestNotFound
	}
	if err != nil {
		return nil, "", err
	}
	text, err := b.answerText(ctx, requestID)
	if err != nil {
		return nil, "", err
	}
	return req, text, nil
}

func (b *Backend) answerText(ctx context.Context, requestID string) (string, error) {
	var text string
	err := b.pool.QueryRow(ctx,
		`SELECT COALESCE(string_agg(text, '' ORDER BY ordinal), '') FROM fragments WHERE request_id = $1`,
		requestID).Scan(&text)
	return text, err
}

// FallbackStillWanted reports whether a request may still need the fallback: it
// exists, is not terminal, and has no committed fragments yet.
func (b *Backend) FallbackStillWanted(requestID string) bool {
	ctx := context.Background()
	var status string
	var frags int
	err := b.pool.QueryRow(ctx,
		`SELECT r.status, (SELECT count(*) FROM fragments f WHERE f.request_id = r.id) FROM requests r WHERE r.id = $1`,
		requestID).Scan(&status, &frags)
	if err != nil {
		return false
	}
	return !isTerminalStatus(core.RequestStatus(status)) && frags == 0
}

// activeRequestForResponder returns the non-terminal request currently owned by
// a responder session, or "" — the reconcile primitive for assignment recovery.
func (b *Backend) activeRequestForResponder(ctx context.Context, sid string) (string, error) {
	var id string
	err := b.pool.QueryRow(ctx,
		`SELECT id FROM requests WHERE responder_session_id = $1 AND `+notTerminalSQL+` ORDER BY updated_at DESC LIMIT 1`,
		sid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (b *Backend) fragmentCount(ctx context.Context, requestID string) (int, error) {
	var n int
	err := b.pool.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, requestID).Scan(&n)
	return n, err
}
