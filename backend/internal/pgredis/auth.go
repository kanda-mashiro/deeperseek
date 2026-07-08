package pgredis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"deeperseek/backend/internal/core"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type session struct {
	ID        string
	Token     string
	UserID    string
	Guest     bool
	Nickname  string
	CreatedAt time.Time
}

func (b *Backend) Register(accountName, nickname, password, repeated string) (core.AuthResult, error) {
	accountName = strings.TrimSpace(accountName)
	nickname = strings.TrimSpace(nickname)
	if accountName == "" || nickname == "" || password == "" {
		return core.AuthResult{}, fmt.Errorf("account name, nickname, and password are required")
	}
	if password != repeated {
		return core.AuthResult{}, core.ErrPasswordMismatch
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return core.AuthResult{}, err
	}

	ctx := context.Background()
	now := b.clock()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return core.AuthResult{}, err
	}
	defer tx.Rollback(ctx)

	userID := newID("usr")
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, account_name, nickname, password_hash, created_at) VALUES ($1, $2, $3, $4, $5)`,
		userID, accountName, nickname, hash, now); err != nil {
		if isUniqueViolation(err) {
			return core.AuthResult{}, core.ErrAccountExists
		}
		return core.AuthResult{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO point_ledger (id, user_id, kind, delta, created_at) VALUES ($1, $2, 'signup_grant', $3, $4)`,
		newID("pts"), userID, core.SignupGrant, now); err != nil {
		return core.AuthResult{}, err
	}
	sess := session{ID: newID("ses"), Token: newID("tok"), UserID: userID, Guest: false, Nickname: nickname, CreatedAt: now}
	if err := insertSessionTx(ctx, tx, sess); err != nil {
		return core.AuthResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return core.AuthResult{}, err
	}
	return b.authResult(ctx, sess)
}

func (b *Backend) Login(accountName, password string) (core.AuthResult, error) {
	ctx := context.Background()
	var userID, nickname string
	var hash []byte
	err := b.pool.QueryRow(ctx,
		`SELECT id, nickname, password_hash FROM users WHERE account_name = $1`,
		strings.TrimSpace(accountName)).Scan(&userID, &nickname, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.AuthResult{}, core.ErrInvalidCredentials
	}
	if err != nil {
		return core.AuthResult{}, err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return core.AuthResult{}, core.ErrInvalidCredentials
	}
	sess, err := b.createSession(ctx, userID, false, nickname)
	if err != nil {
		return core.AuthResult{}, err
	}
	return b.authResult(ctx, sess)
}

func (b *Backend) GuestSession(nickname string) core.AuthResult {
	if strings.TrimSpace(nickname) == "" {
		nickname = "Guest Operator"
	}
	ctx := context.Background()
	sess, err := b.createSession(ctx, "", true, nickname)
	if err != nil {
		return core.AuthResult{}
	}
	result, _ := b.authResult(ctx, sess)
	return result
}

func (b *Backend) Me(token string) (core.AuthResult, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil {
		return core.AuthResult{}, err
	}
	return b.authResult(ctx, sess)
}

func (b *Backend) LedgerForUser(token string) ([]core.PointEntry, core.Balance, error) {
	ctx := context.Background()
	sess, err := b.sessionByToken(ctx, token)
	if err != nil || sess.Guest {
		return nil, core.Balance{}, core.ErrUnauthorized
	}
	rows, err := b.pool.Query(ctx,
		`SELECT id, user_id, request_id, kind, delta, created_at FROM point_ledger WHERE user_id = $1 ORDER BY created_at, id`,
		sess.UserID)
	if err != nil {
		return nil, core.Balance{}, err
	}
	defer rows.Close()
	var entries []core.PointEntry
	for rows.Next() {
		var e core.PointEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.RequestID, &e.Kind, &e.Delta, &e.CreatedAt); err != nil {
			return nil, core.Balance{}, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, core.Balance{}, err
	}
	return entries, b.balance(ctx, sess.UserID), nil
}

// --- helpers ---

func (b *Backend) createSession(ctx context.Context, userID string, guest bool, nickname string) (session, error) {
	sess := session{ID: newID("ses"), Token: newID("tok"), UserID: userID, Guest: guest, Nickname: nickname, CreatedAt: b.clock()}
	_, err := b.pool.Exec(ctx,
		`INSERT INTO sessions (id, token, user_id, guest, nickname, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.ID, sess.Token, sess.UserID, sess.Guest, sess.Nickname, sess.CreatedAt)
	return sess, err
}

func insertSessionTx(ctx context.Context, tx pgx.Tx, sess session) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO sessions (id, token, user_id, guest, nickname, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.ID, sess.Token, sess.UserID, sess.Guest, sess.Nickname, sess.CreatedAt)
	return err
}

func (b *Backend) sessionByToken(ctx context.Context, token string) (session, error) {
	var s session
	err := b.pool.QueryRow(ctx,
		`SELECT id, token, user_id, guest, nickname, created_at FROM sessions WHERE token = $1`,
		token).Scan(&s.ID, &s.Token, &s.UserID, &s.Guest, &s.Nickname, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return session{}, core.ErrUnauthorized
	}
	if err != nil {
		return session{}, err
	}
	return s, nil
}

func (b *Backend) authResult(ctx context.Context, sess session) (core.AuthResult, error) {
	dto := core.UserDTO{ID: sess.ID, Nickname: sess.Nickname, Guest: sess.Guest}
	balance := core.Balance{}
	if !sess.Guest {
		var accountName, nickname string
		if err := b.pool.QueryRow(ctx, `SELECT account_name, nickname FROM users WHERE id = $1`, sess.UserID).Scan(&accountName, &nickname); err != nil {
			return core.AuthResult{}, err
		}
		dto = core.UserDTO{ID: sess.UserID, AccountName: accountName, Nickname: nickname}
		balance = b.balance(ctx, sess.UserID)
	}
	return core.AuthResult{Token: sess.Token, User: dto, Balance: balance}, nil
}

// balance derives points from the ledger minus points still frozen on
// non-terminal requests, matching the in-memory model exactly.
func (b *Backend) balance(ctx context.Context, userID string) core.Balance {
	var total, held int
	_ = b.pool.QueryRow(ctx, `SELECT COALESCE(SUM(delta), 0) FROM point_ledger WHERE user_id = $1`, userID).Scan(&total)
	_ = b.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(frozen_points), 0) FROM requests
		 WHERE requester_id = $1 AND frozen_points > 0
		 AND status NOT IN ('completed', 'timeout_completed', 'abandoned')`,
		userID).Scan(&held)
	return core.Balance{Total: total, Held: held, Available: total - held}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
