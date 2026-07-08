package pgredis

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"deeperseek/backend/internal/core"

	"github.com/alicebob/miniredis/v2"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Integration tests run a real embedded Postgres (no Docker) plus pure-Go
// miniredis, booted once for the package. They are gated on DEEPERSEEK_IT so the
// default `go test ./...` and CI stay on the memory path with zero infra; the
// first run fetches the Postgres binary and needs network.

var testBackend *Backend

func TestMain(m *testing.M) {
	if os.Getenv("DEEPERSEEK_IT") == "" {
		os.Exit(m.Run()) // every integration test skips itself
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(9876).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		panic("start embedded postgres: " + err.Error())
	}
	mr, err := miniredis.Run()
	if err != nil {
		_ = pg.Stop()
		panic("start miniredis: " + err.Error())
	}
	b, err := New(context.Background(),
		"postgres://postgres:postgres@localhost:9876/postgres?sslmode=disable",
		"redis://"+mr.Addr())
	if err != nil {
		mr.Close()
		_ = pg.Stop()
		panic("new backend: " + err.Error())
	}
	testBackend = b

	code := m.Run()

	b.Close()
	mr.Close()
	_ = pg.Stop()
	os.Exit(code)
}

// backendForTest returns the shared backend with a clean slate, or skips.
func backendForTest(t *testing.T) *Backend {
	t.Helper()
	if os.Getenv("DEEPERSEEK_IT") == "" {
		t.Skip("set DEEPERSEEK_IT=1 to run pgredis integration tests")
	}
	ctx := context.Background()
	if _, err := testBackend.pool.Exec(ctx, `TRUNCATE users, sessions, requests, fragments, point_ledger`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	testBackend.rdb.FlushAll(ctx)
	return testBackend
}

func TestConnectsMigratesAndReMigratesIdempotently(t *testing.T) {
	b := backendForTest(t)
	ctx := context.Background()
	if err := b.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := b.migrate(ctx); err != nil {
		t.Fatalf("re-migrate should be idempotent: %v", err)
	}
}

func TestRegisterGrantsTwentyPointsAndIsUnique(t *testing.T) {
	b := backendForTest(t)
	auth, err := b.Register("alice", "Alice", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if auth.User.AccountName != "alice" || auth.User.Guest {
		t.Fatalf("unexpected user dto: %+v", auth.User)
	}
	if auth.Balance.Total != 20 || auth.Balance.Available != 20 || auth.Balance.Held != 0 {
		t.Fatalf("unexpected balance: %+v", auth.Balance)
	}
	if _, err := b.Register("alice", "Alice2", "pass1234", "pass1234"); !errors.Is(err, core.ErrAccountExists) {
		t.Fatalf("expected ErrAccountExists, got %v", err)
	}
	if _, err := b.Register("bob", "Bob", "pass1234", "nope"); !errors.Is(err, core.ErrPasswordMismatch) {
		t.Fatalf("expected ErrPasswordMismatch, got %v", err)
	}
}

func TestLoginAndMe(t *testing.T) {
	b := backendForTest(t)
	if _, err := b.Register("carol", "Carol", "pass1234", "pass1234"); err != nil {
		t.Fatalf("register: %v", err)
	}
	auth, err := b.Login("carol", "pass1234")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if auth.Balance.Total != 20 {
		t.Fatalf("login balance: %+v", auth.Balance)
	}
	me, err := b.Me(auth.Token)
	if err != nil || me.User.ID != auth.User.ID {
		t.Fatalf("me mismatch: %+v err=%v", me, err)
	}
	if _, err := b.Login("carol", "wrong"); !errors.Is(err, core.ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	if _, err := b.Login("nobody", "pass1234"); !errors.Is(err, core.ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials for unknown account, got %v", err)
	}
	if _, err := b.Me("bogus-token"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestGuestSessionHasZeroBalance(t *testing.T) {
	b := backendForTest(t)
	guest := b.GuestSession("")
	if !guest.User.Guest || guest.Token == "" {
		t.Fatalf("unexpected guest: %+v", guest)
	}
	if guest.Balance.Total != 0 || guest.Balance.Available != 0 {
		t.Fatalf("guest balance should be zero: %+v", guest.Balance)
	}
	me, err := b.Me(guest.Token)
	if err != nil || !me.User.Guest {
		t.Fatalf("guest me mismatch: %+v err=%v", me, err)
	}
}

func TestLedgerForUser(t *testing.T) {
	b := backendForTest(t)
	auth, err := b.Register("dave", "Dave", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	entries, balance, err := b.LedgerForUser(auth.Token)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "signup_grant" || entries[0].Delta != 20 {
		t.Fatalf("unexpected ledger: %+v", entries)
	}
	if balance.Total != 20 {
		t.Fatalf("ledger balance: %+v", balance)
	}
	guest := b.GuestSession("")
	if _, _, err := b.LedgerForUser(guest.Token); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("guest ledger should be unauthorized, got %v", err)
	}
}
