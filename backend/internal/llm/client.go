// Package llm is a tiny OpenAI-compatible streaming client shared by the
// fallback responder and the AI-persona subsystem.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"deeperseek/backend/internal/core"
)

type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Client    *http.Client
}

// Enabled reports whether the client is fully configured. All three of base URL,
// API key, and model are required — a half-configured client would fail every
// call, so callers must treat it as disabled.
func (c Config) Enabled() bool { return c.APIKey != "" && c.BaseURL != "" && c.Model != "" }

func (c Config) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// Stream sends the messages to the upstream and invokes onDelta for each content
// delta. onDelta returning an error stops the stream and is returned.
func (c Config) Stream(ctx context.Context, messages []core.Message, onDelta func(string) error) error {
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"messages":   messages,
		"stream":     true,
		"max_tokens": c.MaxTokens,
	})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("llm upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// A 200 with no SSE frames means the upstream ignored stream:true or a gateway
	// returned an error page as 200 (common with one-api style proxies). Surface it
	// instead of silently reporting an empty, successful answer.
	sawFrame, err := readSSE(resp.Body, onDelta)
	if err != nil {
		return err
	}
	if !sawFrame {
		return fmt.Errorf("llm upstream returned no SSE data (content-type %q)", resp.Header.Get("Content-Type"))
	}
	return nil
}

// Complete accumulates the streamed answer into a single string.
func (c Config) Complete(ctx context.Context, messages []core.Message) (string, error) {
	var b strings.Builder
	err := c.Stream(ctx, messages, func(delta string) error {
		b.WriteString(delta)
		return nil
	})
	return b.String(), err
}

// readSSE streams data frames to onDelta and reports whether it saw any SSE frame
// at all (so a non-SSE 200 body can be distinguished from a real, empty stream).
func readSSE(body io.Reader, onDelta func(string) error) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sawFrame := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		sawFrame = true
		if payload == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]string `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return sawFrame, err
		}
		for _, choice := range chunk.Choices {
			if err := onDelta(choice.Delta["content"]); err != nil {
				return sawFrame, err
			}
		}
	}
	return sawFrame, scanner.Err()
}
