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

func TestStreamRejectsNonSSE200(t *testing.T) {
	// a gateway that ignores stream:true and returns a plain JSON/error body as 200
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient quota"}}`))
	}))
	defer upstream.Close()

	cfg := Config{BaseURL: upstream.URL, APIKey: "k", Model: "m", Client: upstream.Client()}
	if _, err := cfg.Complete(context.Background(), []core.Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("a 200 body with no SSE frames must be an error, not a silent empty success")
	}
}

func TestEnabledRequiresFullConfig(t *testing.T) {
	full := Config{BaseURL: "http://x", APIKey: "k", Model: "m"}
	if !full.Enabled() {
		t.Fatal("a fully configured client should be enabled")
	}
	for _, c := range []Config{{APIKey: "k"}, {APIKey: "k", BaseURL: "http://x"}, {BaseURL: "http://x", Model: "m"}} {
		if c.Enabled() {
			t.Fatalf("a half-configured client must be disabled: %+v", c)
		}
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
