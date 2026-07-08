package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deeperseek/backend/internal/core"
)

// handleBoard lists spectator-safe tickets for the "围观" board. Public, no auth,
// no raw question content.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	tickets, err := s.svc.Board(limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if tickets == nil {
		tickets = []core.BoardTicket{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets})
}

// handleBoardRoutes dispatches /api/board/{id}/watch.
func (s *Server) handleBoardRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/board/")
	if id, ok := strings.CutSuffix(rest, "/watch"); ok && id != "" {
		s.handleBoardWatch(w, r, id)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "route not found")
}

// handleBoardWatch streams a board-eligible request's answer read-only: it grants
// no points and, unlike the requester's own stream, never cancels the request on
// disconnect.
func (s *Server) handleBoardWatch(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	req, _, err := s.svc.RequestSnapshot(requestID)
	if err != nil || !req.BoardEligible {
		writeError(w, http.StatusNotFound, "not_found", "not available for spectating")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		return
	}
	events, unsubscribe, err := s.svc.Subscribe(requestID)
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
			return // read-only: do NOT cancel the request
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": watching\n\n")
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
