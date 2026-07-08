package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"deeperseek/backend/internal/core"
)

// handleConversations: GET lists the caller's conversations, POST creates one.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	switch r.Method {
	case http.MethodGet:
		convs, err := s.svc.ListConversations(token)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if convs == nil {
			convs = []core.Conversation{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // title optional
		conv, err := s.svc.CreateConversation(token, body.Title)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, conv)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// handleConversationItem: GET/PATCH/DELETE /api/conversations/{id}.
func (s *Server) handleConversationItem(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	id := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		conv, msgs, err := s.svc.GetConversation(token, id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if msgs == nil {
			msgs = []core.ConversationMessage{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversation": conv, "messages": msgs})
	case http.MethodPatch:
		var body struct {
			Title    *string `json:"title"`
			Archived *bool   `json:"archived"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.Title != nil {
			if err := s.svc.RenameConversation(token, id, *body.Title); err != nil {
				writeServiceError(w, err)
				return
			}
		}
		if body.Archived != nil {
			if err := s.svc.SetConversationArchived(token, id, *body.Archived); err != nil {
				writeServiceError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case http.MethodDelete:
		if err := s.svc.DeleteConversation(token, id); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
