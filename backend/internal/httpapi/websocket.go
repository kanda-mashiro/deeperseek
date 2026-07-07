package httpapi

import (
	"net/http"
	"sync"

	"deeperseek/backend/internal/core"
)

type wsInbound struct {
	Type      string `json:"type"`
	ClientSeq int64  `json:"client_seq,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (s *Server) handleAnswerWebSocket(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		token = s.svc.GuestSession("Guest Operator").Token
	}

	sessionID, assignments, err := s.svc.RegisterResponder(token)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.svc.UnregisterResponder(sessionID)
		return
	}
	defer conn.Close()
	defer s.svc.UnregisterResponder(sessionID)

	var writeMu sync.Mutex
	write := func(value any) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := writeWSJSON(conn, value); err != nil {
			return false
		}
		return true
	}

	done := make(chan struct{})
	go func() {
		for assignment := range assignments {
			if !write(map[string]any{
				"type":       "assigned",
				"request_id": assignment.RequestID,
				"messages":   assignment.Messages,
				"created_at": assignment.CreatedAt,
			}) {
				return
			}
		}
	}()

	for {
		var msg wsInbound
		if err := conn.ReadJSON(&msg); err != nil {
			close(done)
			return
		}
		switch msg.Type {
		case "available":
			err = s.svc.MarkResponderAvailable(sessionID)
			if err == nil {
				write(wsOK("available_ack"))
			}
		case "fragment":
			var fragment core.Fragment
			var duplicate bool
			fragment, duplicate, err = s.svc.SubmitFragment(sessionID, msg.ClientSeq, msg.Text)
			if err == nil {
				write(map[string]any{
					"type":       "fragment_ack",
					"client_seq": msg.ClientSeq,
					"fragment":   fragment.Text,
					"duplicate":  duplicate,
				})
			}
		case "finish":
			err = s.svc.Finish(sessionID)
			if err == nil {
				write(wsOK("finish_ack"))
			}
		case "skip":
			err = s.svc.Skip(sessionID)
			if err == nil {
				write(wsOK("skip_ack"))
			}
		case "ping":
			write(map[string]string{"type": "pong"})
		default:
			err = core.ErrResponderUnavailable
		}
		if err != nil {
			write(wsError(serverError(err)))
		}
		select {
		case <-done:
			return
		default:
		}
	}
}
