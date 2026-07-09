package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/httpapi"
	"deeperseek/backend/internal/llm"
	"deeperseek/backend/internal/persona"
	"deeperseek/backend/internal/pgredis"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc, cleanup := buildBackend()
	defer cleanup()

	server := httpapi.NewServerWithOptions(svc, httpapi.ServerOptions{
		Fallback:       httpapi.DefaultFallbackConfigFromEnv(),
		StaticDir:      os.Getenv("STATIC_DIR"),
		RatePerMin:     envInt("DEEPERSEEK_RATE_PER_MIN", 0), // 0 = disabled; set in prod
		RateBurst:      envInt("DEEPERSEEK_RATE_BURST", 40),
		TrustedProxies: envInt("DEEPERSEEK_TRUSTED_PROXIES", 1), // Traefik appends the real client IP
	})
	go svc.RunTimeoutSweeper(ctx, time.Second)
	startPersonas(ctx, svc)

	srv := &http.Server{Addr: addr, Handler: server.Handler()}
	go func() {
		<-ctx.Done() // SIGTERM/SIGINT: stop accepting, let personas reap via ctx
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("deeperseek backend listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// startPersonas launches the AI-persona subsystem. It is on by default but inert
// unless an LLM is configured (it reuses the fallback upstream). The manager
// itself is leader-elected, so it is safe to start on every replica.
func startPersonas(ctx context.Context, backend core.Backend) {
	cfg := persona.DefaultConfig()
	cfg.Enabled = envBool("DEEPERSEEK_PERSONA_ENABLED", true)
	if n := envInt("DEEPERSEEK_PERSONA_MAX_RESPONDERS", 0); n > 0 {
		cfg.MaxResponders = n
	}
	if n := envInt("DEEPERSEEK_PERSONA_TARGET_QUEUE", -1); n >= 0 {
		cfg.TargetQueue = n
	}
	cfg.LLM = llm.Config{
		BaseURL:   os.Getenv("DEEPERSEEK_FALLBACK_BASE_URL"),
		APIKey:    os.Getenv("DEEPERSEEK_FALLBACK_API_KEY"),
		Model:     os.Getenv("DEEPERSEEK_FALLBACK_MODEL"),
		MaxTokens: cfg.AnswerRunes,
	}
	if !cfg.Enabled || !cfg.LLM.Enabled() {
		log.Printf("deeperseek personas off (enabled=%t, llm_configured=%t)", cfg.Enabled, cfg.LLM.Enabled())
		return
	}
	log.Printf("deeperseek personas on (max_responders=%d, target_queue=%d)", cfg.MaxResponders, cfg.TargetQueue)
	go persona.NewManager(backend, cfg).Run(ctx)
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// buildBackend selects the state engine from configuration: both DATABASE_URL
// and REDIS_URL set -> the distributed Postgres+Redis backend; both absent ->
// the in-memory single-node backend (the zero-infra default). Exactly one set is
// a fatal misconfiguration — silently degrading an N-replica deploy to per-pod
// memory is a split-brain, so we fail fast and let GitOps surface it.
func buildBackend() (core.Backend, func()) {
	dbURL := os.Getenv("DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL")
	switch {
	case dbURL != "" && redisURL != "":
		b, err := pgredis.New(context.Background(), dbURL, redisURL)
		if err != nil {
			log.Fatalf("pgredis backend: %v", err)
		}
		log.Printf("deeperseek using distributed pgredis backend")
		return b, b.Close
	case dbURL == "" && redisURL == "":
		log.Printf("deeperseek using in-memory backend (single node)")
		return core.NewService(), func() {}
	default:
		log.Fatalf("DATABASE_URL and REDIS_URL must be set together (db set=%t, redis set=%t)", dbURL != "", redisURL != "")
		return nil, nil
	}
}
