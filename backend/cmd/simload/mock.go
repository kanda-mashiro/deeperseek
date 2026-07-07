package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type mockUpstream struct {
	url      string
	server   *http.Server
	listener net.Listener
}

// startMockUpstream serves an OpenAI-compatible SSE endpoint that returns the
// whole answer in two large, instant deltas — forcing the backend fallback to
// re-chunk and pace them for the requester.
func startMockUpstream() *mockUpstream {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		answer := []rune(strings.Repeat("假装深思熟虑地回答你。", 12))
		half := len(answer) / 2
		for _, delta := range []string{string(answer[:half]), string(answer[half:])} {
			payload, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": delta}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return &mockUpstream{url: "http://" + listener.Addr().String(), server: server, listener: listener}
}

func (m *mockUpstream) close() {
	_ = m.server.Close()
}
