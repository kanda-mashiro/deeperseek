package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"deeperseek/backend/internal/core"

	"github.com/gorilla/websocket"
)

// pinger is implemented by backends with external dependencies (the pgredis
// backend); the memory backend does not, so it is always ready.
type pinger interface {
	Ping(context.Context) error
}

type Server struct {
	svc       core.Backend
	upgrader  websocket.Upgrader
	fallback  FallbackConfig
	staticDir string
	mode      string
	ready     func(context.Context) error
}

func NewServer(svc core.Backend) *Server {
	return NewServerWithOptions(svc, ServerOptions{})
}

type ServerOptions struct {
	Fallback  FallbackConfig
	StaticDir string
}

func NewServerWithOptions(svc core.Backend, options ServerOptions) *Server {
	fallback := options.Fallback.withDefaults()
	mode := "memory"
	var ready func(context.Context) error
	if p, ok := svc.(pinger); ok {
		mode = "pgredis"
		ready = p.Ping
	}
	return &Server{
		svc:       svc,
		fallback:  fallback,
		staticDir: options.StaticDir,
		mode:      mode,
		ready:     ready,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func DefaultFallbackConfigFromEnv() FallbackConfig {
	return FallbackConfig{
		Enabled:        os.Getenv("DEEPERSEEK_FALLBACK_API_KEY") != "",
		BaseURL:        os.Getenv("DEEPERSEEK_FALLBACK_BASE_URL"),
		APIKey:         os.Getenv("DEEPERSEEK_FALLBACK_API_KEY"),
		Model:          os.Getenv("DEEPERSEEK_FALLBACK_MODEL"),
		Delay:          parseFallbackDelay(os.Getenv("DEEPERSEEK_FALLBACK_DELAY")),
		ChunkDelay:     parseFallbackDelay(os.Getenv("DEEPERSEEK_FALLBACK_CHUNK_DELAY")),
		MaxChunkRunes:  parseFallbackMaxChunkRunes(os.Getenv("DEEPERSEEK_FALLBACK_MAX_CHUNK_RUNES")),
		MaxAnswerRunes: parseFallbackMaxChunkRunes(os.Getenv("DEEPERSEEK_FALLBACK_MAX_ANSWER_RUNES")),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/ready", s.handleReady)
	mux.HandleFunc("/api/register", s.handleRegister)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/guest", s.handleGuest)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/points/ledger", s.handleLedger)
	mux.HandleFunc("/api/answers/", s.handleAnswerRoutes)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/ws/answer", s.handleAnswerWebSocket)
	mux.HandleFunc("/", s.handleFrontend)
	return withCORS(mux)
}

// handleHealth is liveness: the process is up. It never touches dependencies.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": s.mode})
}

// handleReady is readiness: in distributed mode it verifies Postgres + Redis are
// reachable so a pod that lost a dependency is pulled from rotation. In memory
// mode there are no dependencies, so it is always ready.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "mode": s.mode})
}

func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/ws/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if s.staticDir == "" {
		writeError(w, http.StatusNotFound, "not_found", "frontend is not configured")
		return
	}

	cleanPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if cleanPath == "." || cleanPath == "/" {
		cleanPath = "index.html"
	}
	target := filepath.Join(s.staticDir, cleanPath)
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		target = filepath.Join(s.staticDir, "index.html")
	}
	http.ServeFile(w, r, target)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var body struct {
		AccountName    string `json:"account_name"`
		Nickname       string `json:"nickname"`
		Password       string `json:"password"`
		RepeatPassword string `json:"repeat_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	auth, err := s.svc.Register(body.AccountName, body.Nickname, body.Password, body.RepeatPassword)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, auth)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var body struct {
		AccountName string `json:"account_name"`
		Password    string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	auth, err := s.svc.Login(body.AccountName, body.Password)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, auth)
}

func (s *Server) handleGuest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var body struct {
		Nickname string `json:"nickname"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusCreated, s.svc.GuestSession(body.Nickname))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	auth, err := s.svc.Me(bearerToken(r))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, auth)
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	entries, balance, err := s.svc.LedgerForUser(bearerToken(r))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "balance": balance})
}

func (s *Server) handleAnswerRoutes(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/reaction") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	requestID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/answers/"), "/reaction")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "request id is required")
		return
	}
	var body struct {
		Reaction core.Reaction `json:"reaction"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	balance, err := s.svc.React(bearerToken(r), requestID, body.Reaction)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance": balance, "reaction": body.Reaction})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write json", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, typ string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"type":    typ,
			"message": message,
		},
	})
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
	case errors.Is(err, core.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case errors.Is(err, core.ErrInsufficientPoints):
		writeError(w, http.StatusPaymentRequired, "insufficient_points", err.Error())
	case errors.Is(err, core.ErrInputTooLarge):
		writeError(w, http.StatusBadRequest, "context_length_exceeded", err.Error())
	case errors.Is(err, core.ErrOutputTooLarge):
		writeError(w, http.StatusBadRequest, "output_length_exceeded", err.Error())
	case errors.Is(err, core.ErrAccountExists):
		writeError(w, http.StatusConflict, "account_exists", err.Error())
	case errors.Is(err, core.ErrRequestNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeWSJSON(conn *websocket.Conn, value any) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(value)
}

func wsError(message string) map[string]string {
	return map[string]string{"type": "error", "message": message}
}

func wsOK(kind string) map[string]string {
	return map[string]string{"type": kind, "status": "ok"}
}

func serverError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
