package pgredis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"deeperseek/backend/internal/core"

	"github.com/jackc/pgx/v5"
)

const assignTickInterval = 200 * time.Millisecond

type responderConn struct {
	stop func()
}

// --- request creation ---

func (b *Backend) CreateRequest(ctx context.Context, token, model string, messages []core.Message, maxOutputChars int) (*core.Request, error) {
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Content)
	}
	if total > core.InputLimitChars {
		return nil, core.ErrInputTooLarge
	}
	if maxOutputChars <= 0 || maxOutputChars > core.OutputLimitChars {
		maxOutputChars = core.OutputLimitChars
	}

	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	frozen := 0
	if !sess.Guest {
		// serialize concurrent creates for this user so the balance check is race-free
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, sess.UserID); err != nil {
			return nil, err
		}
		var total, held int
		_ = tx.QueryRow(ctx, `SELECT COALESCE(SUM(delta), 0) FROM point_ledger WHERE user_id = $1`, sess.UserID).Scan(&total)
		_ = tx.QueryRow(ctx, `SELECT COALESCE(SUM(frozen_points), 0) FROM requests WHERE requester_id = $1 AND frozen_points > 0 AND `+notTerminalSQL, sess.UserID).Scan(&held)
		if total-held < core.QuestionCost {
			return nil, core.ErrInsufficientPoints
		}
		frozen = core.QuestionCost
	}

	msgs, _ := json.Marshal(messages)
	reqID := newID("req")
	category := core.QuestionCategory(messages)
	boardEligible := sess.Guest && sess.Kind != core.KindAIPersona // real guests only; personas never on the board
	if _, err := tx.Exec(ctx,
		`INSERT INTO requests (id, requester_id, requester_session_id, requester_guest, requester_kind, messages, model,
			status, frozen_points, output_limit, reaction, board_eligible, question_category, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'queued', $8, $9, 'none', $10, $11, $12, $12)`,
		reqID, sess.UserID, sess.ID, sess.Guest, core.KindOrHuman(sess.Kind), msgs, model, frozen, maxOutputChars, boardEligible, category, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Coordination must outlive the requester's HTTP request: enqueue + drain on
	// a background context so a client disconnect can't strand the queued request
	// or abort an unrelated request mid-assignment.
	bg := context.Background()
	if err := b.enqueueRequest(bg, reqID); err != nil {
		return nil, err
	}
	b.drainAssignments(bg)

	return &core.Request{
		ID: reqID, RequesterID: sess.UserID, RequesterSessionID: sess.ID, RequesterGuest: sess.Guest,
		RequesterKind: core.KindOrHuman(sess.Kind), BoardEligible: boardEligible, Messages: messages, Model: model,
		Status: core.StatusQueued, FrozenPoints: frozen, OutputLimit: maxOutputChars, Reaction: core.ReactionNone,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// --- fragment submission ---

func (b *Backend) SubmitFragment(sessionID string, clientSeq int64, text string) (core.Fragment, bool, error) {
	if clientSeq <= 0 {
		return core.Fragment{}, false, fmt.Errorf("client_seq must be positive")
	}
	if text == "" {
		return core.Fragment{}, false, fmt.Errorf("fragment text is required")
	}
	ctx := context.Background()
	now := b.clock()

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return core.Fragment{}, false, err
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx,
		`SELECT `+requestCols+` FROM requests WHERE responder_session_id = $1 AND `+notTerminalSQL+` ORDER BY updated_at DESC LIMIT 1 FOR UPDATE`,
		sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Fragment{}, false, core.ErrNoActiveAssignment
	}
	if err != nil {
		return core.Fragment{}, false, err
	}

	// idempotent retry: same (session, client_seq) returns the stored fragment
	var existing core.Fragment
	var exCreated time.Time
	err = tx.QueryRow(ctx,
		`SELECT id, request_id, responder_session_id, client_seq, text, created_at FROM fragments
		 WHERE request_id = $1 AND responder_session_id = $2 AND client_seq = $3`,
		req.ID, sessionID, clientSeq).Scan(&existing.ID, &existing.RequestID, &existing.ResponderSessionID, &existing.ClientSeq, &existing.Text, &exCreated)
	if err == nil {
		existing.CreatedAt = exCreated
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return core.Fragment{}, false, err
	}

	var answerRunes int
	_ = tx.QueryRow(ctx, `SELECT COALESCE(SUM(char_length(text)), 0) FROM fragments WHERE request_id = $1`, req.ID).Scan(&answerRunes)
	if answerRunes+utf8.RuneCountInString(text) > req.OutputLimit {
		return core.Fragment{}, false, core.ErrOutputTooLarge
	}

	var ordinal int
	_ = tx.QueryRow(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1 FROM fragments WHERE request_id = $1`, req.ID).Scan(&ordinal)
	frag := core.Fragment{ID: newID("frg"), RequestID: req.ID, ResponderSessionID: sessionID, ClientSeq: clientSeq, Text: text, CreatedAt: now}
	if _, err := tx.Exec(ctx,
		`INSERT INTO fragments (id, request_id, responder_session_id, client_seq, ordinal, text, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		frag.ID, frag.RequestID, sessionID, clientSeq, ordinal, text, now); err != nil {
		return core.Fragment{}, false, err
	}

	if !req.QuestionCharged {
		if _, err := tx.Exec(ctx, `UPDATE requests SET question_charged = TRUE, frozen_points = 0 WHERE id = $1`, req.ID); err != nil {
			return core.Fragment{}, false, err
		}
		if !req.RequesterGuest {
			if err := addLedgerTx(ctx, tx, req.RequesterID, req.ID, "question_charge", -core.QuestionCost, now); err != nil {
				return core.Fragment{}, false, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE requests SET status = 'streaming', updated_at = $2 WHERE id = $1`, req.ID, now); err != nil {
		return core.Fragment{}, false, err
	}

	reachedLimit := answerRunes+utf8.RuneCountInString(text) >= req.OutputLimit
	if reachedLimit {
		if err := b.completeRequestTx(ctx, tx, req, core.FinishLength, now); err != nil {
			return core.Fragment{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return core.Fragment{}, false, err
	}

	_ = b.publishStream(ctx, req.ID, streamMessage{Kind: "fragment", Text: text, Ordinal: ordinal})
	if reachedLimit {
		_ = b.publishStream(ctx, req.ID, streamMessage{Kind: "done", Finish: string(core.FinishLength)})
		_ = b.releaseLock(ctx, req.ID)
	}
	return frag, false, nil
}

// --- finish / skip ---

func (b *Backend) Finish(sessionID string) error {
	ctx := context.Background()
	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	req, err := b.activeRequestTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, req.ID).Scan(&frags)
	if frags == 0 {
		return fmt.Errorf("cannot finish before first committed fragment")
	}
	if err := b.completeRequestTx(ctx, tx, req, core.FinishStop, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = b.publishStream(ctx, req.ID, streamMessage{Kind: "done", Finish: string(core.FinishStop)})
	_ = b.releaseLock(ctx, req.ID)
	return nil
}

func (b *Backend) Skip(sessionID string) error {
	ctx := context.Background()
	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	req, err := b.activeRequestTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, req.ID).Scan(&frags)
	if frags > 0 {
		return core.ErrCannotSkipCommitted
	}
	if err := requeueRequestTx(ctx, tx, req.ID, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = b.releaseLock(ctx, req.ID)
	_ = b.enqueueRequest(ctx, req.ID)
	b.drainAssignments(ctx)
	return nil
}

// --- fallback assignment ---

func (b *Backend) AcquireFallbackAssignment(requestID string) (string, core.AssignedRequest, bool) {
	ctx := context.Background()
	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return "", core.AssignedRequest{}, false
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM requests WHERE id = $1 FOR UPDATE`, requestID))
	if err != nil {
		return "", core.AssignedRequest{}, false
	}
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, req.ID).Scan(&frags)
	if req.Status != core.StatusQueued || frags > 0 {
		return "", core.AssignedRequest{}, false
	}
	sessionID := "fallback_" + requestID
	if _, err := tx.Exec(ctx,
		`UPDATE requests SET status = 'assigned', responder_session_id = $1, responder_user_id = '', responder_guest = TRUE,
			responder_kind = 'fallback', responder_display = '回退助手', updated_at = $2 WHERE id = $3`,
		sessionID, now, req.ID); err != nil {
		return "", core.AssignedRequest{}, false
	}
	if err := tx.Commit(ctx); err != nil {
		return "", core.AssignedRequest{}, false
	}
	_ = b.removeQueued(ctx, req.ID)
	return sessionID, core.AssignedRequest{RequestID: req.ID, Messages: req.Messages, CreatedAt: req.CreatedAt}, true
}

// --- timeouts ---

func (b *Backend) SweepTimeouts(now time.Time, assignedTimeout, streamingTimeout time.Duration) []string {
	ctx := context.Background()
	var changed []string

	if assignedTimeout > 0 {
		cutoff := now.Add(-assignedTimeout)
		rows, err := b.pool.Query(ctx,
			`SELECT id FROM requests WHERE status = 'assigned' AND updated_at <= $1
			 AND (SELECT count(*) FROM fragments f WHERE f.request_id = requests.id) = 0`, cutoff)
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			for _, id := range ids {
				// re-assert the freshness predicate in the CAS so a request that was
				// legitimately re-assigned between SELECT and UPDATE is not revoked
				tag, err := b.pool.Exec(ctx,
					`UPDATE requests SET status = 'queued', responder_session_id = '', responder_user_id = '', responder_guest = FALSE,
						responder_kind = '', responder_display = '', updated_at = $2
					 WHERE id = $1 AND status = 'assigned' AND updated_at <= $3`, id, now, cutoff)
				if err == nil && tag.RowsAffected() > 0 {
					_ = b.releaseLock(ctx, id)
					_ = b.enqueueRequest(ctx, id)
					changed = append(changed, id+":assigned_timeout_requeued")
				}
			}
		}
	}

	if streamingTimeout > 0 {
		cutoff := now.Add(-streamingTimeout)
		rows, err := b.pool.Query(ctx,
			`SELECT `+requestCols+` FROM requests WHERE status = 'streaming' AND updated_at <= $1
			 AND (SELECT count(*) FROM fragments f WHERE f.request_id = requests.id) > 0`, cutoff)
		if err == nil {
			var reqs []*core.Request
			for rows.Next() {
				if r, err := scanRequest(rows); err == nil {
					reqs = append(reqs, r)
				}
			}
			rows.Close()
			for _, r := range reqs {
				tx, err := b.pool.Begin(ctx)
				if err != nil {
					continue
				}
				// re-check freshness under the row lock: a fragment committed after
				// the SELECT bumps updated_at and must cancel this completion
				var st string
				var upd time.Time
				if err := tx.QueryRow(ctx, `SELECT status, updated_at FROM requests WHERE id = $1 FOR UPDATE`, r.ID).Scan(&st, &upd); err != nil {
					_ = tx.Rollback(ctx)
					continue
				}
				if st != string(core.StatusStreaming) || upd.After(cutoff) {
					_ = tx.Rollback(ctx)
					continue
				}
				if err := b.completeRequestTx(ctx, tx, r, core.FinishPartial, now); err != nil {
					_ = tx.Rollback(ctx)
					continue
				}
				if err := tx.Commit(ctx); err != nil {
					continue
				}
				_ = b.publishStream(ctx, r.ID, streamMessage{Kind: "done", Finish: string(core.FinishPartial)})
				_ = b.releaseLock(ctx, r.ID)
				changed = append(changed, r.ID+":streaming_timeout_completed")
			}
		}
	}

	if len(changed) > 0 {
		b.drainAssignments(ctx)
	}
	return changed
}

func (b *Backend) RunTimeoutSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			b.SweepTimeouts(now.UTC(), core.AssignedTimeout, core.StreamingInactivityTimeout)
		}
	}
}

// --- responder lifecycle ---

func (b *Backend) RegisterResponder(token string) (string, <-chan core.AssignedRequest, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return "", nil, err
	}

	cleanup, ch := b.assignmentChannel(ctx, sess.ID)
	hbCtx, cancelHB := context.WithCancel(context.Background())
	go b.heartbeatLoop(hbCtx, sess.ID)

	b.mu.Lock()
	old := b.responders[sess.ID]
	b.responders[sess.ID] = &responderConn{stop: func() {
		cancelHB()
		cleanup()
	}}
	b.mu.Unlock()
	if old != nil {
		old.stop() // reconnect on the same session: reap the stale conn's goroutines
	}

	// reconcile: if this session already owns an assignment (e.g. reconnect),
	// deliver it so a missed pub/sub assignment is recovered.
	if reqID, _ := b.activeRequestForResponder(ctx, sess.ID); reqID != "" {
		if req, _, err := b.RequestSnapshot(reqID); err == nil && req.Status == core.StatusAssigned {
			_ = b.publishAssignment(ctx, sess.ID, core.AssignedRequest{RequestID: req.ID, Messages: req.Messages, CreatedAt: req.CreatedAt})
		}
	}
	return sess.ID, ch, nil
}

func (b *Backend) heartbeatLoop(ctx context.Context, sid string) {
	_ = b.heartbeat(ctx, sid)
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = b.heartbeat(context.Background(), sid)
		}
	}
}

func (b *Backend) UnregisterResponder(sessionID string) {
	b.mu.Lock()
	rc := b.responders[sessionID]
	delete(b.responders, sessionID)
	b.mu.Unlock()
	if rc != nil {
		rc.stop()
	}

	ctx := context.Background()
	now := b.clock()
	_ = b.dropPresence(ctx, sessionID)
	_ = b.removeAvailable(ctx, sessionID)

	reqID, err := b.activeRequestForResponder(ctx, sessionID)
	if err != nil || reqID == "" {
		return
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	req, err := scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM requests WHERE id = $1 FOR UPDATE`, reqID))
	if err != nil {
		return
	}
	if isTerminalStatus(req.Status) {
		return
	}
	// read the fragment count inside the tx, after FOR UPDATE, so a fragment
	// committed concurrently can't be missed (which would wrongly requeue an
	// answer that already has committed output)
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, reqID).Scan(&frags)
	var doneReason core.FinishReason
	if frags == 0 {
		if err := requeueRequestTx(ctx, tx, reqID, now); err != nil {
			return
		}
	} else {
		if err := b.completeRequestTx(ctx, tx, req, core.FinishPartial, now); err != nil {
			return
		}
		doneReason = core.FinishPartial
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}
	_ = b.releaseLock(ctx, reqID)
	if frags == 0 {
		_ = b.enqueueRequest(ctx, reqID)
	} else {
		_ = b.publishStream(ctx, reqID, streamMessage{Kind: "done", Finish: string(doneReason)})
	}
	b.drainAssignments(ctx)
}

func (b *Backend) MarkResponderAvailable(sessionID string) error {
	b.mu.Lock()
	_, ok := b.responders[sessionID]
	b.mu.Unlock()
	if !ok {
		return core.ErrResponderUnavailable
	}
	ctx := context.Background()
	if reqID, _ := b.activeRequestForResponder(ctx, sessionID); reqID != "" {
		return nil // busy: already owns a request
	}
	_ = b.heartbeat(ctx, sessionID)
	if err := b.addAvailable(ctx, sessionID); err != nil {
		return err
	}
	b.drainAssignments(ctx)
	return nil
}

// --- requester stream ---

func (b *Backend) Subscribe(requestID string) (<-chan core.StreamEvent, func(), error) {
	ctx := context.Background()
	var status string
	if err := b.pool.QueryRow(ctx, `SELECT status FROM requests WHERE id = $1`, requestID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, core.ErrRequestNotFound
		}
		return nil, nil, err
	}

	cleanup, msgCh := b.streamChannel(ctx, requestID) // subscribe BEFORE the first snapshot
	out := make(chan core.StreamEvent, 64)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done); cleanup() }) }

	// emit is cancelable, so a gone consumer can never park this goroutine.
	emit := func(ev core.StreamEvent) bool {
		select {
		case out <- ev:
			return true
		case <-done:
			return false
		}
	}

	go func() {
		defer close(out)
		lastOrdinal := 0
		// Postgres is the source of truth; pub/sub (and a slow ticker) are only
		// wake-ups. Each wake drains newly-persisted fragments, so a lost pub/sub
		// message self-heals and the stream is contiguous and gap-free.
		flush := func() bool {
			n, ok := b.drainFragmentsAfter(ctx, requestID, lastOrdinal, emit)
			lastOrdinal = n
			if !ok {
				return false
			}
			if term, finish := b.terminalState(ctx, requestID); term {
				// drain once more: the final fragment can be committed with completion
				n, ok = b.drainFragmentsAfter(ctx, requestID, lastOrdinal, emit)
				lastOrdinal = n
				if ok {
					emit(core.StreamEvent{Type: core.StreamEventDone, RequestID: requestID, FinishReason: finish})
				}
				return false
			}
			return true
		}
		if !flush() {
			return
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case _, ok := <-msgCh:
				if !ok {
					return
				}
			case <-ticker.C:
			}
			if !flush() {
				return
			}
		}
	}()

	return out, stop, nil
}

// drainFragmentsAfter emits every persisted fragment with ordinal > after, in
// order, via emit; it stops early if emit reports cancellation. Returns the new
// high-water ordinal and whether it finished without cancellation.
func (b *Backend) drainFragmentsAfter(ctx context.Context, requestID string, after int, emit func(core.StreamEvent) bool) (int, bool) {
	rows, err := b.pool.Query(ctx, `SELECT ordinal, text FROM fragments WHERE request_id = $1 AND ordinal > $2 ORDER BY ordinal`, requestID, after)
	if err != nil {
		return after, true
	}
	defer rows.Close()
	last := after
	for rows.Next() {
		var ord int
		var text string
		if rows.Scan(&ord, &text) != nil {
			continue
		}
		if !emit(core.StreamEvent{Type: core.StreamEventFragment, RequestID: requestID, Text: text}) {
			return last, false
		}
		last = ord
	}
	return last, true
}

func (b *Backend) terminalState(ctx context.Context, requestID string) (bool, core.FinishReason) {
	var status, finish string
	if err := b.pool.QueryRow(ctx, `SELECT status, finish_reason FROM requests WHERE id = $1`, requestID).Scan(&status, &finish); err != nil {
		return false, ""
	}
	return isTerminalStatus(core.RequestStatus(status)), core.FinishReason(finish)
}

func (b *Backend) CancelBeforeFirstFragment(requestID string) bool {
	ctx := context.Background()
	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM requests WHERE id = $1 FOR UPDATE`, requestID))
	if err != nil {
		return false
	}
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, req.ID).Scan(&frags)
	if isTerminalStatus(req.Status) || frags > 0 {
		return false
	}
	if _, err := tx.Exec(ctx,
		`UPDATE requests SET status = 'abandoned', responder_session_id = '', responder_user_id = '', responder_guest = FALSE,
			responder_kind = '', responder_display = '', frozen_points = 0, updated_at = $2 WHERE id = $1`,
		req.ID, now); err != nil {
		return false
	}
	if err := tx.Commit(ctx); err != nil {
		return false
	}
	_ = b.removeQueued(ctx, req.ID)
	_ = b.releaseLock(ctx, req.ID)
	_ = b.publishStream(ctx, req.ID, streamMessage{Kind: "done", Finish: string(core.FinishStop)})
	return true
}

// --- reaction ---

func (b *Backend) React(token, requestID string, reaction core.Reaction) (core.Balance, error) {
	if reaction != core.ReactionNone && reaction != core.ReactionLike && reaction != core.ReactionDislike {
		return core.Balance{}, fmt.Errorf("invalid reaction")
	}
	ctx := context.Background()
	now := b.clock()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return core.Balance{}, err
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return core.Balance{}, err
	}
	defer tx.Rollback(ctx)

	req, err := scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM requests WHERE id = $1 FOR UPDATE`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Balance{}, core.ErrRequestNotFound
	}
	if err != nil {
		return core.Balance{}, err
	}
	isRequester := (!sess.Guest && req.RequesterID == sess.UserID) ||
		(sess.Guest && req.RequesterGuest && req.RequesterSessionID == sess.ID)
	if !isRequester || !isTerminalStatus(req.Status) {
		return core.Balance{}, core.ErrReactionNotAllowed
	}
	if req.CompletedAt.IsZero() || now.Sub(req.CompletedAt) > 24*time.Hour {
		return core.Balance{}, core.ErrReactionNotAllowed
	}

	if req.ResponderGuest || req.ResponderUserID == "" {
		if _, err := tx.Exec(ctx, `UPDATE requests SET reaction = $2 WHERE id = $1`, req.ID, string(reaction)); err != nil {
			return core.Balance{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return core.Balance{}, err
		}
		if sess.Guest {
			return core.Balance{}, nil
		}
		return b.balance(ctx, sess.UserID), nil
	}

	delta := rewardFor(reaction) - rewardFor(req.Reaction)
	if _, err := tx.Exec(ctx, `UPDATE requests SET reaction = $2 WHERE id = $1`, req.ID, string(reaction)); err != nil {
		return core.Balance{}, err
	}
	if delta != 0 {
		if err := addLedgerTx(ctx, tx, req.ResponderUserID, req.ID, "reaction_adjustment", delta, now); err != nil {
			return core.Balance{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return core.Balance{}, err
	}
	if sess.Guest {
		return core.Balance{}, nil
	}
	return b.balance(ctx, req.ResponderUserID), nil
}

// --- assignment drain ---

func (b *Backend) assignTicker(ctx context.Context) {
	ticker := time.NewTicker(assignTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.drainAssignments(context.Background())
		}
	}
}

func (b *Backend) drainAssignments(ctx context.Context) {
	for {
		reqID, sid, ok, err := b.assignNext(ctx)
		if err != nil || !ok {
			return
		}
		b.handlePair(ctx, reqID, sid)
	}
}

func (b *Backend) handlePair(ctx context.Context, reqID, sid string) {
	// Compensations run on a background context so the queue<->PG invariant is
	// restored even if ctx is cancelled: the request is popped from ds:queue but
	// still status='queued' in PG, so any early return must re-enqueue it.
	bg := context.Background()
	var userID, kind, nickname string
	var guest bool
	err := b.pool.QueryRow(ctx, `SELECT user_id, guest, kind, nickname FROM sessions WHERE id = $1`, sid).Scan(&userID, &guest, &kind, &nickname)
	if errors.Is(err, pgx.ErrNoRows) {
		// responder session gone: requeue the request, drop the responder
		_ = b.releaseLock(bg, reqID)
		_ = b.enqueueRequest(bg, reqID)
		return
	}
	if err != nil {
		// transient: both parties still valid, return them to their pools
		_ = b.releaseLock(bg, reqID)
		_ = b.enqueueRequest(bg, reqID)
		_ = b.addAvailable(bg, sid)
		return
	}
	now := b.clock()
	tag, err := b.pool.Exec(ctx,
		`UPDATE requests SET status = 'assigned', responder_session_id = $1, responder_user_id = $2, responder_guest = $3,
			responder_kind = $4, responder_display = $5, updated_at = $6
		 WHERE id = $7 AND status = 'queued'`,
		sid, userID, guest, core.KindOrHuman(kind), nickname, now, reqID)
	if err != nil {
		_ = b.releaseLock(bg, reqID)
		_ = b.enqueueRequest(bg, reqID)
		_ = b.addAvailable(bg, sid)
		return
	}
	if tag.RowsAffected() == 0 {
		// request no longer queued: responder is still good, return it to the pool
		_ = b.releaseLock(bg, reqID)
		_ = b.addAvailable(bg, sid)
		return
	}
	var msgs []byte
	var createdAt time.Time
	if err := b.pool.QueryRow(ctx, `SELECT messages, created_at FROM requests WHERE id = $1`, reqID).Scan(&msgs, &createdAt); err != nil {
		return
	}
	var messages []core.Message
	_ = json.Unmarshal(msgs, &messages)
	_ = b.publishAssignment(ctx, sid, core.AssignedRequest{RequestID: reqID, Messages: messages, CreatedAt: createdAt})
}

// --- shared tx helpers ---

func (b *Backend) activeRequestTx(ctx context.Context, tx pgx.Tx, sessionID string) (*core.Request, error) {
	req, err := scanRequest(tx.QueryRow(ctx,
		`SELECT `+requestCols+` FROM requests WHERE responder_session_id = $1 AND `+notTerminalSQL+` ORDER BY updated_at DESC LIMIT 1 FOR UPDATE`,
		sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, core.ErrNoActiveAssignment
	}
	if err != nil {
		return nil, err
	}
	return req, nil
}

func (b *Backend) completeRequestTx(ctx context.Context, tx pgx.Tx, req *core.Request, reason core.FinishReason, now time.Time) error {
	status := core.StatusCompleted
	if reason == core.FinishPartial {
		status = core.StatusTimeoutCompleted
	}
	tag, err := tx.Exec(ctx,
		`UPDATE requests SET status = $2, finish_reason = $3, updated_at = $4, completed_at = $4 WHERE id = $1 AND `+notTerminalSQL,
		req.ID, string(status), string(reason), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already terminal (another instance won); no double reward
	}
	var frags int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM fragments WHERE request_id = $1`, req.ID).Scan(&frags)
	if frags > 0 && !req.ResponderGuest && req.ResponderUserID != "" {
		if err := addLedgerTx(ctx, tx, req.ResponderUserID, req.ID, "answer_reward", core.BaseAnswerReward, now); err != nil {
			return err
		}
	}
	return nil
}

func requeueRequestTx(ctx context.Context, tx pgx.Tx, reqID string, now time.Time) error {
	_, err := tx.Exec(ctx,
		`UPDATE requests SET status = 'queued', responder_session_id = '', responder_user_id = '', responder_guest = FALSE,
			responder_kind = '', responder_display = '', updated_at = $2
		 WHERE id = $1 AND `+notTerminalSQL,
		reqID, now)
	return err
}

func addLedgerTx(ctx context.Context, tx pgx.Tx, userID, requestID, kind string, delta int, now time.Time) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO point_ledger (id, user_id, request_id, kind, delta, created_at) VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT DO NOTHING`,
		newID("pts"), userID, requestID, kind, delta, now)
	return err
}

func rewardFor(reaction core.Reaction) int {
	switch reaction {
	case core.ReactionLike:
		return core.BaseAnswerReward * 2
	case core.ReactionDislike:
		return 8
	default:
		return core.BaseAnswerReward
	}
}

var _ core.Backend = (*Backend)(nil)
