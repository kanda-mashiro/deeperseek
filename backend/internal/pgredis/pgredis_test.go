package pgredis

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// startBackend boots a real embedded Postgres and a pure-Go miniredis, returning
// a connected Backend. Shared by all pgredis integration tests. Gated on
// DEEPERSEEK_IT so the default `go test ./...` (and CI) stays on the memory path
// with zero infra; the first run fetches the Postgres binary and needs network.
func startBackend(t *testing.T) *Backend {
	t.Helper()
	if os.Getenv("DEEPERSEEK_IT") == "" {
		t.Skip("set DEEPERSEEK_IT=1 to run pgredis integration tests")
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(9876).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		t.Fatalf("start embedded postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	mr := miniredis.RunT(t)

	b, err := New(context.Background(),
		"postgres://postgres:postgres@localhost:9876/postgres?sslmode=disable",
		"redis://"+mr.Addr())
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestBackendConnectsMigratesAndRoundTrips(t *testing.T) {
	b := startBackend(t)
	ctx := context.Background()

	if err := b.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// migrations created the schema: a user row round-trips
	if _, err := b.pool.Exec(ctx,
		`INSERT INTO users (id, account_name, nickname, password_hash) VALUES ($1, $2, $3, $4)`,
		"usr_1", "alice", "Alice", []byte("hash")); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var nickname string
	if err := b.pool.QueryRow(ctx, `SELECT nickname FROM users WHERE id = $1`, "usr_1").Scan(&nickname); err != nil {
		t.Fatalf("select user: %v", err)
	}
	if nickname != "Alice" {
		t.Fatalf("expected Alice, got %q", nickname)
	}

	// the exactly-once ledger guard rejects a second signup grant
	if _, err := b.pool.Exec(ctx, `INSERT INTO point_ledger (id, user_id, kind, delta) VALUES ($1, $2, 'signup_grant', 20)`, "pts_1", "usr_1"); err != nil {
		t.Fatalf("first signup grant: %v", err)
	}
	if _, err := b.pool.Exec(ctx, `INSERT INTO point_ledger (id, user_id, kind, delta) VALUES ($1, $2, 'signup_grant', 20)`, "pts_2", "usr_1"); err == nil {
		t.Fatal("expected the signup-once unique index to reject a duplicate grant")
	}

	// redis is reachable through miniredis
	if err := b.rdb.Set(ctx, b.key("smoke"), "ok", 0).Err(); err != nil {
		t.Fatalf("redis set: %v", err)
	}

	// migrations are idempotent on a second run
	if err := b.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}
