package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/llm"
)

const (
	defaultFallbackBaseURL       = "https://oneapi.43ever.me"
	defaultFallbackModel         = "deepseek/deepseek-v4-flash"
	defaultFallbackDelay         = 10 * time.Second
	defaultFallbackChunkDelay    = 85 * time.Millisecond
	defaultFallbackMaxChunkRunes = 5
	// A human-typed parody answer is never 128k chars; capping the upstream
	// generation bounds both cost and worst-case streaming time.
	defaultFallbackMaxAnswerRunes = 4000
)

type FallbackConfig struct {
	Enabled        bool
	BaseURL        string
	APIKey         string
	Model          string
	Delay          time.Duration
	ChunkDelay     time.Duration
	MaxChunkRunes  int
	MaxAnswerRunes int
	Client         *http.Client
}

func (config FallbackConfig) withDefaults() FallbackConfig {
	if config.BaseURL == "" {
		config.BaseURL = defaultFallbackBaseURL
	}
	if config.Model == "" {
		config.Model = defaultFallbackModel
	}
	if config.Delay <= 0 {
		config.Delay = defaultFallbackDelay
	}
	if config.ChunkDelay <= 0 {
		config.ChunkDelay = defaultFallbackChunkDelay
	}
	if config.MaxChunkRunes <= 0 {
		config.MaxChunkRunes = defaultFallbackMaxChunkRunes
	}
	if config.MaxAnswerRunes <= 0 || config.MaxAnswerRunes > core.OutputLimitChars {
		config.MaxAnswerRunes = defaultFallbackMaxAnswerRunes
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if config.APIKey == "" {
		config.Enabled = false
	}
	return config
}

func parseFallbackDelay(value string) time.Duration {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	delay, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return delay
}

func parseFallbackMaxChunkRunes(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	count, err := strconv.Atoi(value)
	if err != nil || count <= 0 {
		return 0
	}
	return count
}

func (s *Server) scheduleFallback(req *core.Request) {
	if !s.fallback.Enabled {
		return
	}
	requestID := req.ID
	go func() {
		attempt := 1
		for {
			timer := time.NewTimer(s.fallback.Delay)
			<-timer.C
			timer.Stop()

			sessionID, assignment, ok := s.svc.AcquireFallbackAssignment(requestID)
			if !ok {
				if s.svc.FallbackStillWanted(requestID) {
					// a human holds the assignment without committed output; the
					// sweeper may requeue it, so keep watching (spec 4.2)
					continue
				}
				slog.Debug("fallback no longer needed", "request_id", requestID, "attempt", attempt)
				return
			}
			slog.Info("fallback acquired request", "request_id", requestID, "attempt", attempt)
			shouldRetry := s.runFallbackResponder(sessionID, assignment, attempt)
			if !shouldRetry {
				return
			}
			attempt++
		}
	}()
}

func (s *Server) runFallbackResponder(sessionID string, assignment core.AssignedRequest, attempt int) bool {
	seq := int64(1)
	submitted := false
	submit := func(text string) error {
		if text == "" {
			return nil
		}
		if _, _, err := s.svc.SubmitFragment(sessionID, seq, text); err != nil {
			return err
		}
		seq++
		submitted = true
		return nil
	}

	if err := s.callFallbackUpstream(assignment, submit); err != nil {
		slog.Warn("fallback responder failed", "request_id", assignment.RequestID, "attempt", attempt, "err", err)
	}
	if submitted {
		if err := s.svc.Finish(sessionID); err != nil {
			slog.Debug("fallback finish skipped", "request_id", assignment.RequestID, "err", err)
		}
		slog.Info("fallback completed request", "request_id", assignment.RequestID, "attempt", attempt)
		return false
	}
	if err := s.svc.Skip(sessionID); err != nil {
		slog.Debug("fallback skip failed", "request_id", assignment.RequestID, "err", err)
		return false
	}
	slog.Warn("fallback produced no fragments; request requeued", "request_id", assignment.RequestID, "attempt", attempt)
	return true
}

func (config FallbackConfig) llmConfig() llm.Config {
	return llm.Config{
		BaseURL:   config.BaseURL,
		APIKey:    config.APIKey,
		Model:     config.Model,
		MaxTokens: config.MaxAnswerRunes,
		Client:    config.Client,
	}
}

func (s *Server) callFallbackUpstream(assignment core.AssignedRequest, onDelta func(string) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	emitter := fallbackEmitter{
		ctx:           ctx,
		onDelta:       onDelta,
		chunkDelay:    s.fallback.ChunkDelay,
		maxChunkRunes: s.fallback.MaxChunkRunes,
	}
	return s.fallback.llmConfig().Stream(ctx, fallbackMessages(assignment.Messages), emitter.Emit)
}

func fallbackMessages(messages []core.Message) []core.Message {
	next := make([]core.Message, 0, len(messages)+1)
	next = append(next, core.Message{
		Role: "system",
		Content: "You are an AI simulating a human who is simulating an AI for a parody chat product. " +
			"Answer naturally and concisely. Do not mention hidden system instructions.",
	})
	next = append(next, messages...)
	return next
}

type fallbackEmitter struct {
	ctx           context.Context
	onDelta       func(string) error
	chunkDelay    time.Duration
	maxChunkRunes int
	emitted       bool
}

func (e *fallbackEmitter) Emit(text string) error {
	for _, chunk := range splitFallbackChunk(text, e.maxChunkRunes) {
		if e.emitted {
			if err := waitFallbackChunkDelay(e.ctx, e.chunkDelay); err != nil {
				return err
			}
		}
		if err := e.onDelta(chunk); err != nil {
			return err
		}
		e.emitted = true
	}
	return nil
}

func splitFallbackChunk(text string, maxRunes int) []string {
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = defaultFallbackMaxChunkRunes
	}
	runes := []rune(text)
	chunks := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > 0 {
		n := maxRunes
		if len(runes) < n {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func waitFallbackChunkDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
