package pgredis

import (
	"context"
	"errors"

	"deeperseek/backend/internal/core"

	"github.com/jackc/pgx/v5"
)

const conversationCols = `id, owner_user_id, guest_session_id, title, archived, created_at, updated_at`

func ownsConversation(sess session, c core.Conversation) bool {
	if sess.Guest {
		return c.GuestSessionID == sess.ID
	}
	return c.OwnerUserID != "" && c.OwnerUserID == sess.UserID
}

func scanConversation(row pgx.Row) (core.Conversation, error) {
	var c core.Conversation
	err := row.Scan(&c.ID, &c.OwnerUserID, &c.GuestSessionID, &c.Title, &c.Archived, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ownedConversation loads a conversation and enforces that the session owns it,
// collapsing "not found" and "not yours" into ErrConversationNotFound.
func (b *Backend) ownedConversation(ctx context.Context, sess session, id string) (core.Conversation, error) {
	c, err := scanConversation(b.pool.QueryRow(ctx, `SELECT `+conversationCols+` FROM conversations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Conversation{}, core.ErrConversationNotFound
	}
	if err != nil {
		return core.Conversation{}, err
	}
	if !ownsConversation(sess, c) {
		return core.Conversation{}, core.ErrConversationNotFound
	}
	return c, nil
}

func (b *Backend) CreateConversation(token, title string) (core.Conversation, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return core.Conversation{}, err
	}
	now := b.clock()
	c := core.Conversation{ID: newID("cnv"), Title: core.NormalizeTitle(title), CreatedAt: now, UpdatedAt: now}
	if sess.Guest {
		c.GuestSessionID = sess.ID
	} else {
		c.OwnerUserID = sess.UserID
	}
	if _, err := b.pool.Exec(ctx,
		`INSERT INTO conversations (id, owner_user_id, guest_session_id, title, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $5)`,
		c.ID, c.OwnerUserID, c.GuestSessionID, c.Title, now); err != nil {
		return core.Conversation{}, err
	}
	return c, nil
}

func (b *Backend) ListConversations(token string) ([]core.Conversation, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	var rows pgx.Rows
	if sess.Guest {
		rows, err = b.pool.Query(ctx, `SELECT `+conversationCols+` FROM conversations WHERE guest_session_id = $1 ORDER BY updated_at DESC`, sess.ID)
	} else {
		rows, err = b.pool.Query(ctx, `SELECT `+conversationCols+` FROM conversations WHERE owner_user_id = $1 ORDER BY updated_at DESC`, sess.UserID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (b *Backend) GetConversation(token, id string) (core.Conversation, []core.ConversationMessage, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return core.Conversation{}, nil, err
	}
	c, err := b.ownedConversation(ctx, sess, id)
	if err != nil {
		return core.Conversation{}, nil, err
	}
	rows, err := b.pool.Query(ctx,
		`SELECT id, seq, role, content, source_kind, request_id, created_at FROM conversation_messages WHERE conversation_id = $1 ORDER BY seq`, id)
	if err != nil {
		return core.Conversation{}, nil, err
	}
	defer rows.Close()
	var msgs []core.ConversationMessage
	for rows.Next() {
		var m core.ConversationMessage
		if err := rows.Scan(&m.ID, &m.Seq, &m.Role, &m.Content, &m.SourceKind, &m.RequestID, &m.CreatedAt); err != nil {
			return core.Conversation{}, nil, err
		}
		msgs = append(msgs, m)
	}
	return c, msgs, rows.Err()
}

func (b *Backend) RenameConversation(token, id, title string) error {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return err
	}
	if _, err := b.ownedConversation(ctx, sess, id); err != nil {
		return err
	}
	_, err = b.pool.Exec(ctx, `UPDATE conversations SET title = $2, updated_at = $3 WHERE id = $1`, id, core.NormalizeTitle(title), b.clock())
	return err
}

func (b *Backend) SetConversationArchived(token, id string, archived bool) error {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return err
	}
	if _, err := b.ownedConversation(ctx, sess, id); err != nil {
		return err
	}
	_, err = b.pool.Exec(ctx, `UPDATE conversations SET archived = $2, updated_at = $3 WHERE id = $1`, id, archived, b.clock())
	return err
}

func (b *Backend) DeleteConversation(token, id string) error {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return err
	}
	if _, err := b.ownedConversation(ctx, sess, id); err != nil {
		return err
	}
	_, err = b.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, id) // messages cascade
	return err
}

func (b *Backend) AppendConversationMessage(token, conversationID, role, content, sourceKind, requestID string) (core.ConversationMessage, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return core.ConversationMessage{}, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return core.ConversationMessage{}, err
	}
	defer tx.Rollback(ctx)

	// lock the conversation row so seq assignment is serialized
	c, err := scanConversation(tx.QueryRow(ctx, `SELECT `+conversationCols+` FROM conversations WHERE id = $1 FOR UPDATE`, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ConversationMessage{}, core.ErrConversationNotFound
	}
	if err != nil {
		return core.ConversationMessage{}, err
	}
	if !ownsConversation(sess, c) {
		return core.ConversationMessage{}, core.ErrConversationNotFound
	}

	now := b.clock()
	var seq int
	_ = tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM conversation_messages WHERE conversation_id = $1`, conversationID).Scan(&seq)
	msg := core.ConversationMessage{ID: newID("msg"), Seq: seq, Role: role, Content: content, SourceKind: sourceKind, RequestID: requestID, CreatedAt: now}
	if _, err := tx.Exec(ctx,
		`INSERT INTO conversation_messages (id, conversation_id, seq, role, content, source_kind, request_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		msg.ID, conversationID, seq, role, content, sourceKind, requestID, now); err != nil {
		return core.ConversationMessage{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = $2 WHERE id = $1`, conversationID, now); err != nil {
		return core.ConversationMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return core.ConversationMessage{}, err
	}
	return msg, nil
}
