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

func (c Config) Enabled() bool { return c.APIKey != "" }

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
	return readSSE(resp.Body, onDelta)
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

func readSSE(body io.Reader, onDelta func(string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]string `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		for _, choice := range chunk.Choices {
			if err := onDelta(choice.Delta["content"]); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
