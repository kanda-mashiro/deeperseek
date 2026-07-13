package persona

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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

func sequenceLLM(t *testing.T, responses ...string) llm.Config {
	t.Helper()
	var mu sync.Mutex
	index := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		response := responses[len(responses)-1]
		if index < len(responses) {
			response = responses[index]
			index++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + jsonString(response) + "}}]}\n\n"))
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

func TestManagerPersonaRecoversAfterOutputCap(t *testing.T) {
	svc := core.NewService()
	m := NewManager(svc, testConfig(fakeLLM(t, "这是一段明显超过十个字符的很长回答内容")))

	human := svc.GuestSession("human")
	if _, _, err := svc.RegisterResponder(human.Token); err != nil {
		t.Fatalf("register human: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitFor(t, func() bool { return svc.OnlineResponderCount() >= 2 }, "persona responder online")

	asker := svc.GuestSession("asker")
	// a tiny output cap makes SubmitFragment fail mid-answer; the persona must
	// release the assignment (not wedge) so it can serve the next question
	capped, _ := svc.CreateRequest(context.Background(), asker.Token, "m", []core.Message{{Role: "user", Content: "hi"}}, 10)
	waitFor(t, func() bool {
		snap, _, err := svc.RequestSnapshot(capped.ID)
		return err == nil && snap.Status == core.StatusCompleted
	}, "capped request terminal")

	next, _ := svc.CreateRequest(context.Background(), asker.Token, "m", []core.Message{{Role: "user", Content: "again"}}, 0)
	waitFor(t, func() bool {
		snap, _, err := svc.RequestSnapshot(next.ID)
		return err == nil && snap.Status == core.StatusCompleted && snap.ResponderKind == core.KindAIPersona
	}, "persona answers the next question (not wedged)")
}

func TestManagerTrimsPersonasWhenHumansLeave(t *testing.T) {
	svc := core.NewService()
	cfg := testConfig(fakeLLM(t, "x"))
	cfg.MaxResponders = 3
	m := NewManager(svc, cfg)

	h1 := svc.GuestSession("h1")
	h2 := svc.GuestSession("h2")
	sid1, _, _ := svc.RegisterResponder(h1.Token)
	_, _, _ = svc.RegisterResponder(h2.Token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	// 2 humans -> 2 personas -> 4 online
	waitFor(t, func() bool { return svc.OnlineResponderCount() >= 4 }, "two personas spawned")

	// one human leaves -> persona pool must trim to 1 -> 1 human + 1 persona
	svc.UnregisterResponder(sid1)
	waitFor(t, func() bool { return svc.OnlineResponderCount() == 2 }, "persona pool trims to match humans")
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

func TestManagerContinuesPersonaQuestionWithSameResponderAndFullTranscript(t *testing.T) {
	svc := core.NewService()
	cfg := testConfig(sequenceLLM(t, "第一问？", "根据第一答的第二问？"))
	cfg.TargetQueue = 1
	cfg.MaxResponders = 0
	cfg.FollowUpQueueTimeout = time.Second
	m := NewManager(svc, cfg)

	human := svc.GuestSession("human")
	sid, assignments, err := svc.RegisterResponder(human.Token)
	if err != nil {
		t.Fatalf("register human: %v", err)
	}
	if err := svc.MarkResponderAvailable(sid, true); err != nil {
		t.Fatalf("mark human available: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	first := waitPersonaAssignment(t, assignments)
	if first.RequesterKind != core.KindAIPersona || len(first.Messages) != 1 || first.Messages[0].Content != "第一问？" {
		t.Fatalf("unexpected first persona question: %+v", first)
	}
	if _, _, err := svc.SubmitFragment(sid, 1, "第一答"); err != nil {
		t.Fatalf("submit first answer: %v", err)
	}
	if err := svc.Finish(sid); err != nil {
		t.Fatalf("finish first answer: %v", err)
	}

	// The client deliberately does not declare itself generally available here.
	// The persona continuation must reserve and resume this exact responder.
	second := waitPersonaAssignment(t, assignments)
	if second.RequesterKind != core.KindAIPersona || len(second.Messages) != 3 {
		t.Fatalf("unexpected follow-up assignment: %+v", second)
	}
	want := []core.Message{
		{Role: "user", Content: "第一问？"},
		{Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "根据第一答的第二问？"},
	}
	for i := range want {
		if second.Messages[i] != want[i] {
			t.Fatalf("follow-up message %d: got %+v want %+v", i, second.Messages[i], want[i])
		}
	}
	if err := svc.Skip(sid); err != nil {
		t.Fatalf("end targeted conversation: %v", err)
	}
}

func waitPersonaAssignment(t *testing.T, assignments <-chan core.AssignedRequest) core.AssignedRequest {
	t.Helper()
	select {
	case assignment := <-assignments:
		return assignment
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for persona assignment")
		return core.AssignedRequest{}
	}
}
