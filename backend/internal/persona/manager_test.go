package persona

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/llm"
)

func fakeLLM(t *testing.T, answer string) llm.Config {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + jsonString(answer) + "}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)
	return llm.Config{BaseURL: upstream.URL, APIKey: "k", Model: "m", MaxTokens: 100, Client: upstream.Client()}
}

func jsonString(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"', '\\':
			b = append(b, '\\', byte(r))
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func testConfig(l llm.Config) Config {
	cfg := DefaultConfig()
	cfg.PollInterval = 30 * time.Millisecond
	cfg.LeaseTTL = time.Second
	cfg.ChunkDelay = time.Millisecond
	cfg.SkipBackoff = 20 * time.Millisecond
	cfg.TargetQueue = 0 // don't post questions in these tests
	cfg.LLM = l
	return cfg
}

func TestManagerPersonaAnswersHumanQuestion(t *testing.T) {
	svc := core.NewService()
	m := NewManager(svc, testConfig(fakeLLM(t, "这是伪人的回答")))

	// a human responder is online but not available, so it satisfies the
	// presence gate without competing for the question
	human := svc.GuestSession("human")
	if _, _, err := svc.RegisterResponder(human.Token); err != nil {
		t.Fatalf("register human: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// wait for a persona responder to come online
	waitFor(t, func() bool { return svc.OnlineResponderCount() >= 2 }, "persona responder online")

	asker := svc.GuestSession("asker")
	req, err := svc.CreateRequest(context.Background(), asker.Token, "m", []core.Message{{Role: "user", Content: "hi"}}, 0)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	var answer string
	waitFor(t, func() bool {
		snap, text, err := svc.RequestSnapshot(req.ID)
		if err != nil {
			return false
		}
		answer = text
		return snap.Status == core.StatusCompleted && snap.ResponderKind == core.KindAIPersona
	}, "persona answer completed")
	if answer != "这是伪人的回答" {
		t.Fatalf("unexpected persona answer %q", answer)
	}
}

func TestManagerSpawnsNoPersonasWithoutHumans(t *testing.T) {
	svc := core.NewService()
	m := NewManager(svc, testConfig(fakeLLM(t, "x")))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// let several ticks pass; with no humans online the manager must stay idle
	time.Sleep(200 * time.Millisecond)
	if n := svc.OnlineResponderCount(); n != 0 {
		t.Fatalf("expected no personas without humans, got %d online responders", n)
	}
}
