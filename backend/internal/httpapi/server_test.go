package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"deeperseek/backend/internal/core"
)

func TestHealthAndReadyReportMemoryMode(t *testing.T) {
	svc := core.NewService()
	server := httptest.NewServer(NewServer(svc).Handler())
	defer server.Close()

	for _, path := range []string{"/api/health", "/api/ready"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, resp.StatusCode)
		}
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body["mode"] != "memory" {
			t.Fatalf("%s: expected memory mode, got %q", path, body["mode"])
		}
	}
}

func TestChatCompletionsStreamEmitsHumanFragments(t *testing.T) {
	svc := core.NewService()
	server := httptest.NewServer(NewServer(svc).Handler())
	defer server.Close()

	requester, err := svc.Register("alice", "Alice", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register requester: %v", err)
	}
	responder, err := svc.Register("bob", "Bob", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	responderSessionID, assignments, err := svc.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder session: %v", err)
	}
	if err := svc.MarkResponderAvailable(responderSessionID); err != nil {
		t.Fatalf("mark available: %v", err)
	}

	body := []byte(`{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"say hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requester.Token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	select {
	case <-assignments:
	case <-time.After(time.Second):
		t.Fatal("expected assignment")
	}

	if _, _, err := svc.SubmitFragment(responderSessionID, 1, "hello"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	if err := svc.Finish(responderSessionID); err != nil {
		t.Fatalf("finish: %v", err)
	}

	scanner := bufio.NewScanner(resp.Body)
	sawContent := false
	sawFinish := false
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta        map[string]string `json:"delta"`
				FinishReason *string           `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("unmarshal chunk %q: %v", payload, err)
		}
		if len(chunk.Choices) == 0 {
			t.Fatalf("missing choices")
		}
		if chunk.Choices[0].Delta["content"] == "hello" {
			sawContent = true
		}
		if chunk.Choices[0].FinishReason != nil && *chunk.Choices[0].FinishReason == string(core.FinishStop) {
			sawFinish = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !sawContent || !sawFinish || !sawDone {
		t.Fatalf("expected content, finish, done; got content=%v finish=%v done=%v", sawContent, sawFinish, sawDone)
	}
}

func TestChatCompletionsFallbackAnswersWhenNoHumanResponder(t *testing.T) {
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-fallback-key" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		var body chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if body.Model != "deepseek/deepseek-v4-flash" {
			t.Errorf("unexpected fallback model: %s", body.Model)
		}
		if !body.Stream {
			t.Error("fallback upstream request should stream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fallback \"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	svc := core.NewService()
	server := httptest.NewServer(NewServerWithOptions(svc, ServerOptions{
		Fallback: FallbackConfig{
			Enabled:       true,
			BaseURL:       upstream.URL,
			APIKey:        "test-fallback-key",
			Model:         "deepseek/deepseek-v4-flash",
			Delay:         10 * time.Millisecond,
			ChunkDelay:    time.Millisecond,
			MaxChunkRunes: 16,
			Client:        upstream.Client(),
		},
	}).Handler())
	defer server.Close()

	requester := svc.GuestSession("")
	body := []byte(`{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"nobody online"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requester.Token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	content, finish, done := readStreamForTest(t, resp)
	if content != "fallback answer" || finish != string(core.FinishStop) || !done {
		t.Fatalf("unexpected fallback stream: content=%q finish=%q done=%v", content, finish, done)
	}
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Fatalf("expected one upstream call, got %d", upstreamCalls)
	}
}

func TestChatCompletionsFallbackRecoversAfterSilentResponder(t *testing.T) {
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fallback \"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	svc := core.NewService()
	server := httptest.NewServer(NewServerWithOptions(svc, ServerOptions{
		Fallback: FallbackConfig{
			Enabled:       true,
			BaseURL:       upstream.URL,
			APIKey:        "test-fallback-key",
			Model:         "deepseek/deepseek-v4-flash",
			Delay:         10 * time.Millisecond,
			ChunkDelay:    time.Millisecond,
			MaxChunkRunes: 16,
			Client:        upstream.Client(),
		},
	}).Handler())
	defer server.Close()

	// a responder that accepts the assignment but never commits anything
	silent := svc.GuestSession("silent")
	sessionID, assignments, err := svc.RegisterResponder(silent.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	defer svc.UnregisterResponder(sessionID)
	go func() {
		for range assignments {
		}
	}()
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("mark available: %v", err)
	}

	requester := svc.GuestSession("")
	body := []byte(`{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"silent holder"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requester.Token)

	// let at least one fallback tick fail against the assigned request, then
	// simulate the assigned-timeout sweep returning it to the queue
	go func() {
		time.Sleep(60 * time.Millisecond)
		svc.SweepTimeouts(time.Now().UTC().Add(time.Hour), time.Millisecond, 0)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	content, finish, done := readStreamForTest(t, resp)
	if content != "fallback answer" || finish != string(core.FinishStop) || !done {
		t.Fatalf("unexpected fallback stream: content=%q finish=%q done=%v", content, finish, done)
	}
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Fatalf("expected one upstream call, got %d", upstreamCalls)
	}
}

func TestChatCompletionsFallbackStreamsLargeUpstreamDeltaSlowly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"abcdefghijkl\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	svc := core.NewService()
	server := httptest.NewServer(NewServerWithOptions(svc, ServerOptions{
		Fallback: FallbackConfig{
			Enabled:       true,
			BaseURL:       upstream.URL,
			APIKey:        "test-fallback-key",
			Model:         "deepseek/deepseek-v4-flash",
			Delay:         10 * time.Millisecond,
			ChunkDelay:    25 * time.Millisecond,
			MaxChunkRunes: 3,
			Client:        upstream.Client(),
		},
	}).Handler())
	defer server.Close()

	requester := svc.GuestSession("")
	body := []byte(`{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"slow fallback"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requester.Token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	started := time.Now()
	chunks, finish, done := readStreamChunksForTest(t, resp)
	elapsed := time.Since(started)
	if strings.Join(chunks, "") != "abcdefghijkl" || finish != string(core.FinishStop) || !done {
		t.Fatalf("unexpected fallback stream: chunks=%q finish=%q done=%v", chunks, finish, done)
	}
	if len(chunks) < 4 {
		t.Fatalf("expected large upstream delta to be split into at least 4 chunks, got %d: %q", len(chunks), chunks)
	}
	if elapsed < 65*time.Millisecond {
		t.Fatalf("expected throttled stream to take visible time, elapsed=%s", elapsed)
	}
}

func TestChatCompletionsFallbackRetriesAfterUpstreamFailure(t *testing.T) {
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&upstreamCalls, 1)
		if call == 1 {
			writeError(w, http.StatusBadGateway, "upstream_failed", "temporary upstream failure")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"retried\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	svc := core.NewService()
	server := httptest.NewServer(NewServerWithOptions(svc, ServerOptions{
		Fallback: FallbackConfig{
			Enabled:       true,
			BaseURL:       upstream.URL,
			APIKey:        "test-fallback-key",
			Model:         "deepseek/deepseek-v4-flash",
			Delay:         10 * time.Millisecond,
			ChunkDelay:    time.Millisecond,
			MaxChunkRunes: 16,
			Client:        upstream.Client(),
		},
	}).Handler())
	defer server.Close()

	requester := svc.GuestSession("")
	body := []byte(`{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"retry fallback"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requester.Token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	content, finish, done := readStreamForTest(t, resp)
	if content != "retried" || finish != string(core.FinishStop) || !done {
		t.Fatalf("unexpected fallback retry stream: content=%q finish=%q done=%v", content, finish, done)
	}
	if atomic.LoadInt32(&upstreamCalls) != 2 {
		t.Fatalf("expected two upstream calls, got %d", upstreamCalls)
	}
}

func TestRegisterEndpointGrantsPoints(t *testing.T) {
	svc := core.NewService()
	server := httptest.NewServer(NewServer(svc).Handler())
	defer server.Close()

	body := []byte(`{"account_name":"alice","nickname":"Alice","password":"pass1234","repeat_password":"pass1234"}`)
	resp, err := http.Post(server.URL+"/api/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var auth core.AuthResult
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		t.Fatalf("decode auth: %v", err)
	}
	if auth.Balance.Total != core.SignupGrant || auth.Balance.Available != core.SignupGrant {
		t.Fatalf("unexpected balance: %+v", auth.Balance)
	}
}

func readStreamForTest(t *testing.T, resp *http.Response) (string, string, bool) {
	t.Helper()
	chunks, finishReason, sawDone := readStreamChunksForTest(t, resp)
	return strings.Join(chunks, ""), finishReason, sawDone
}

func readStreamChunksForTest(t *testing.T, resp *http.Response) ([]string, string, bool) {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	var chunks []string
	finishReason := ""
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta        map[string]string `json:"delta"`
				FinishReason *string           `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("unmarshal chunk %q: %v", payload, err)
		}
		if len(chunk.Choices) == 0 {
			t.Fatalf("missing choices")
		}
		if delta := chunk.Choices[0].Delta["content"]; delta != "" {
			chunks = append(chunks, delta)
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return chunks, finishReason, sawDone
}
