package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"deeperseek/backend/internal/core"
)

func TestStreamAndComplete(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("missing auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	cfg := Config{BaseURL: upstream.URL, APIKey: "sk-test", Model: "m", MaxTokens: 100, Client: upstream.Client()}
	got, err := cfg.Complete(context.Background(), []core.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("expected concatenated stream, got %q", got)
	}
}

func TestStreamSurfacesUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
	}))
	defer upstream.Close()

	cfg := Config{BaseURL: upstream.URL, APIKey: "bad", Model: "m", Client: upstream.Client()}
	if _, err := cfg.Complete(context.Background(), []core.Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected an error on a non-2xx upstream response")
	}
}
