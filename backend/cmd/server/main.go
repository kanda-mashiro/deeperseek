package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/httpapi"
	"deeperseek/backend/internal/pgredis"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	svc, cleanup := buildBackend()
	defer cleanup()

	server := httpapi.NewServerWithOptions(svc, httpapi.ServerOptions{
		Fallback:  httpapi.DefaultFallbackConfigFromEnv(),
		StaticDir: os.Getenv("STATIC_DIR"),
	})
	go svc.RunTimeoutSweeper(context.Background(), time.Second)

	log.Printf("deeperseek backend listening on %s", addr)
	if err := http.ListenAndServe(addr, server.Handler()); err != nil {
		log.Fatal(err)
	}
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
