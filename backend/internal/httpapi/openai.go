package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"deeperseek/backend/internal/core"
)

type chatCompletionRequest struct {
	Model     string         `json:"model"`
	Messages  []core.Message `json:"messages"`
	Stream    bool           `json:"stream"`
	MaxTokens int            `json:"max_tokens"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Index        int                `json:"index"`
	Message      *chatMessage       `json:"message,omitempty"`
	Delta        map[string]string  `json:"delta,omitempty"`
	FinishReason *core.FinishReason `json:"finish_reason"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var body chatCompletionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Model == "" {
		body.Model = "deeperseek-human"
	}
	req, err := s.svc.CreateRequest(r.Context(), bearerToken(r), body.Model, body.Messages, body.MaxTokens)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	slog.Info("chat request created", "request_id", req.ID, "stream", body.Stream, "model", req.Model)
	s.scheduleFallback(req)
	if body.Stream {
		s.streamChatCompletion(w, r, req)
		return
	}
	s.blockingChatCompletion(w, r, req)
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, req *core.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		return
	}
	events, unsubscribe, err := s.svc.Subscribe(req.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Info("chat stream client disconnected", "request_id", req.ID)
			s.svc.CancelBeforeFirstFragment(req.ID)
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": waiting\n\n")
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			switch event.Type {
			case core.StreamEventFragment:
				writeSSEData(w, streamChunk(req, map[string]string{"content": event.Text}, nil))
				flusher.Flush()
			case core.StreamEventDone:
				reason := event.FinishReason
				writeSSEData(w, streamChunk(req, map[string]string{}, &reason))
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

func (s *Server) blockingChatCompletion(w http.ResponseWriter, r *http.Request, req *core.Request) {
	events, unsubscribe, err := s.svc.Subscribe(req.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer unsubscribe()

	content := ""
	finishReason := core.FinishStop
	for {
		select {
		case <-r.Context().Done():
			slog.Info("blocking chat client disconnected", "request_id", req.ID)
			s.svc.CancelBeforeFirstFragment(req.ID)
			return
		case event := <-events:
			switch event.Type {
			case core.StreamEventFragment:
				content += event.Text
			case core.StreamEventDone:
				finishReason = event.FinishReason
				writeJSON(w, http.StatusOK, chatCompletionResponse{
					ID:      "chatcmpl_" + req.ID,
					Object:  "chat.completion",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []chatCompletionChoice{{
						Index:        0,
						Message:      &chatMessage{Role: "assistant", Content: content},
						FinishReason: &finishReason,
					}},
				})
				return
			}
		}
	}
}

func streamChunk(req *core.Request, delta map[string]string, finishReason *core.FinishReason) chatCompletionResponse {
	return chatCompletionResponse{
		ID:      "chatcmpl_" + req.ID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []chatCompletionChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
		}},
	}
}

func writeSSEData(w http.ResponseWriter, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}
