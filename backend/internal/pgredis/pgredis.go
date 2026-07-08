// Package pgredis is the horizontally-scalable Backend implementation: durable
// state in Postgres, realtime coordination in Redis. It is selected at boot when
// both DATABASE_URL and REDIS_URL are set; otherwise the in-memory core.Service
// remains the default. Implementation of core.Backend is filled in across
// Phase 1; this file owns connection lifecycle and migrations.
package pgredis

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces every Redis key this backend owns.
const keyPrefix = "ds:"

type Backend struct {
	pool  *pgxpool.Pool
	rdb   *redis.Client
	clock func() time.Time
}

// New connects to Postgres and Redis, verifies both, and applies migrations.
func New(ctx context.Context, databaseURL, redisURL string) (*Backend, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pg pool: %w", err)
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("redis url: %w", err)
	}

	b := &Backend{pool: pool, rdb: redis.NewClient(opt), clock: func() time.Time { return time.Now().UTC() }}
	if err := b.Ping(ctx); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.migrate(ctx); err != nil {
		b.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return b, nil
}

// Ping verifies both backends are reachable; used by the /api/ready probe so a
// pod that lost Postgres or Redis is pulled out of rotation.
func (b *Backend) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := b.pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("pg ping: %w", err)
	}
	if err := b.rdb.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

func (b *Backend) Close() {
	if b.pool != nil {
		b.pool.Close()
	}
	if b.rdb != nil {
		_ = b.rdb.Close()
	}
}

func (b *Backend) key(parts ...string) string {
	s := keyPrefix
	for i, p := range parts {
		if i > 0 {
			s += ":"
		}
		s += p
	}
	return s
}
