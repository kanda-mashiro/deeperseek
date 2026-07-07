package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

func (w *world) check() *report {
	w.mu.Lock()
	defer w.mu.Unlock()

	r := &report{Scenario: w.sc.Name, AckErrors: w.ackErrors, Assignments: w.assignments, Probes: append([]string(nil), w.probes...)}

	for _, res := range w.results {
		r.Requests++
		if res.cancelled {
			r.Cancelled++
			continue
		}
		if res.stalled {
			r.Stalled = append(r.Stalled, res.question)
			continue
		}
		if !res.done {
			if res.errText != "" {
				r.Violations = append(r.Violations, fmt.Sprintf("transport error on %q: %s", res.question, res.errText))
			}
			continue
		}
		r.Completed++
		if !res.blocking && !res.firstDelta.IsZero() {
			r.FirstDelta = append(r.FirstDelta, res.firstDelta.Sub(res.createdAt))
		}
		if res.maxGap > r.MaxGap {
			r.MaxGap = res.maxGap
		}
		if res.requestID == "" {
			r.Violations = append(r.Violations, fmt.Sprintf("completed without request id: %q", res.question))
			continue
		}

		if w.sc.UseFallback {
			if res.content == "" {
				r.Violations = append(r.Violations, fmt.Sprintf("fallback answer empty for %q", res.question))
			}
			for _, runes := range res.deltaRunes {
				if runes > 5 {
					r.Violations = append(r.Violations, fmt.Sprintf("fallback pacing violated: %d-rune delta on %s", runes, res.requestID))
					break
				}
			}
			continue
		}

		expected := joinAcked(w.acked[res.requestID])
		if expected == "" && res.content != "" {
			r.Violations = append(r.Violations, fmt.Sprintf("content without acked fragments on %s", res.requestID))
			continue
		}
		if res.content != expected {
			detail := describeMismatch(expected, res.content)
			if w.sc.SlowReaderMode {
				r.Probes = append(r.Probes, fmt.Sprintf("slow-reader drop reproduced on %s: %s", res.requestID, detail))
			} else {
				r.Mismatches = append(r.Mismatches, res.requestID)
				r.Violations = append(r.Violations, fmt.Sprintf("content mismatch on %s: %s", res.requestID, detail))
			}
		} else if w.sc.SlowReaderMode {
			r.Probes = append(r.Probes, "slow-reader probe: no fragment drops observed (environment-dependent)")
		}
	}

	for _, question := range r.Stalled {
		r.Violations = append(r.Violations, fmt.Sprintf("stalled >90s (不回复): %q", question))
	}

	for requestID, frags := range w.acked {
		var order []string
		for _, frag := range frags {
			if len(order) == 0 || order[len(order)-1] != frag.session {
				order = append(order, frag.session)
			}
		}
		seen := map[string]bool{}
		for i, session := range order {
			if seen[session] {
				r.Overlaps = append(r.Overlaps, requestID)
				r.Violations = append(r.Violations, fmt.Sprintf("interleaved committers on %s: %v", requestID, order))
				break
			}
			if i > 0 {
				seen[order[i-1]] = true
			}
		}
	}
	return r
}

func joinAcked(frags []ackedFrag) string {
	var b strings.Builder
	for _, frag := range frags {
		b.WriteString(frag.text)
	}
	return b.String()
}

func describeMismatch(expected, got string) string {
	i := 0
	for i < len(expected) && i < len(got) && expected[i] == got[i] {
		i++
	}
	return fmt.Sprintf("acked %d runes, received %d runes, first divergence at byte %d",
		utf8.RuneCountInString(expected), utf8.RuneCountInString(got), i)
}

// runSlowReaderProbe opens the SSE stream over a raw HTTP/1.0 socket with a tiny
// receive buffer and refuses to read for a while, forcing server-side write
// backpressure while the responder commits 120 large fragments.
func (w *world) runSlowReaderProbe(ctx context.Context) {
	token, err := w.guestToken()
	if err != nil {
		w.record(&requesterResult{question: "slow-reader", errText: "guest: " + err.Error()})
		return
	}
	res := &requesterResult{question: "slow-reader", createdAt: time.Now()}
	defer w.record(res)

	parsed, err := url.Parse(w.base)
	if err != nil {
		res.errText = err.Error()
		return
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		res.errText = err.Error()
		return
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4096)
	}

	payload := `{"model":"deeperseek-human","stream":true,"messages":[{"role":"user","content":"slow reader probe"}]}`
	_, err = fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.0\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		parsed.Host, token, len(payload), payload)
	if err != nil {
		res.errText = err.Error()
		return
	}

	// stall: no reads while the responder floods fragments
	select {
	case <-ctx.Done():
		return
	case <-time.After(8 * time.Second):
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	raw, err := io.ReadAll(conn)
	if err != nil && len(raw) == 0 {
		res.errText = err.Error()
		return
	}
	body := string(raw)
	if i := strings.Index(body, "\r\n\r\n"); i >= 0 {
		body = body[i+4:]
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			res.done = true
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				Delta map[string]string `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if res.requestID == "" {
			res.requestID = strings.TrimPrefix(chunk.ID, "chatcmpl_")
		}
		for _, choice := range chunk.Choices {
			res.content += choice.Delta["content"]
		}
	}
	if res.firstDelta.IsZero() && res.content != "" {
		res.firstDelta = time.Now()
	}
}
