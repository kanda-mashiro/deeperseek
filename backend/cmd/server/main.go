package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/httpapi"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	svc := core.NewService()
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
