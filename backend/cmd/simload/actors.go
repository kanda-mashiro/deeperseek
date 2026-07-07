package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"deeperseek/backend/internal/core"

	"github.com/gorilla/websocket"
)

type world struct {
	sc          scenario
	base        string
	client      *http.Client
	wg          sync.WaitGroup
	requesterWG sync.WaitGroup
	stopAsking  atomic.Bool

	mu          sync.Mutex
	acked       map[string][]ackedFrag
	results     []*requesterResult
	ackErrors   map[string]int
	assignments int
	probes      []string
}

type ackedFrag struct {
	session string
	text    string
	at      time.Time
}

type requesterResult struct {
	requestID    string
	question     string
	content      string
	deltaRunes   []int
	done         bool
	finishReason string
	cancelled    bool
	stalled      bool
	errText      string
	createdAt    time.Time
	firstDelta   time.Time
	maxGap       time.Duration
	blocking     bool
}

func newWorld(sc scenario, base string) *world {
	return &world{
		sc:        sc,
		base:      base,
		client:    &http.Client{Timeout: 0},
		acked:     make(map[string][]ackedFrag),
		ackErrors: make(map[string]int),
	}
}

func (w *world) guestToken() (string, error) {
	resp, err := w.client.Post(w.base+"/api/guest", "application/json", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var auth struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return "", err
	}
	return auth.Token, nil
}

// ---------- requester ----------

func (w *world) runRequester(ctx context.Context, idx int) {
	defer w.wg.Done()
	defer w.requesterWG.Done()

	if w.sc.SlowReaderMode {
		w.runSlowReaderProbe(ctx)
		return
	}

	token, err := w.guestToken()
	if err != nil {
		w.record(&requesterResult{errText: "guest: " + err.Error()})
		return
	}
	blocking := rand.Float64() < w.sc.BlockingRatio

	var history []core.Message
	for turn := 0; ; turn++ {
		if w.stopAsking.Load() || ctx.Err() != nil {
			return
		}
		question := fmt.Sprintf("Q%d-%d 请假装聪明地回答 %x", idx, turn, rand.Int63())
		res := w.askOnce(ctx, token, question, history, blocking)
		w.record(res)
		if res.done && len(history) == 0 && rand.Float64() < 0.2 {
			history = []core.Message{
				{Role: "user", Content: question},
				{Role: "assistant", Content: res.content},
			}
		} else {
			history = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(300+rand.Intn(1200)) * time.Millisecond):
		}
	}
}

func (w *world) askOnce(ctx context.Context, token, question string, history []core.Message, blocking bool) *requesterResult {
	res := &requesterResult{question: question, createdAt: time.Now(), blocking: blocking}
	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	payload, _ := json.Marshal(map[string]any{
		"model":    "deeperseek-human",
		"stream":   !blocking,
		"messages": append(append([]core.Message(nil), history...), core.Message{Role: "user", Content: question}),
	})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, w.base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		res.errText = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := w.client.Do(req)
	if err != nil {
		res.errText = classifyAbort(reqCtx, res, err)
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.errText = fmt.Sprintf("http %d", resp.StatusCode)
		return res
	}

	if blocking {
		var body struct {
			ID      string `json:"id"`
			Choices []struct {
				Message      *struct{ Content string } `json:"message"`
				FinishReason *string                   `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			res.errText = classifyAbort(reqCtx, res, err)
			return res
		}
		res.requestID = strings.TrimPrefix(body.ID, "chatcmpl_")
		if len(body.Choices) > 0 && body.Choices[0].Message != nil {
			res.content = body.Choices[0].Message.Content
			res.done = true
			if body.Choices[0].FinishReason != nil {
				res.finishReason = *body.Choices[0].FinishReason
			}
		}
		return res
	}

	// optional cancel-while-queued
	if rand.Float64() < w.sc.CancelProb {
		delay := time.Duration(500+rand.Intn(1000)) * time.Millisecond
		timer := time.AfterFunc(delay, func() {
			if res.firstDelta.IsZero() {
				res.cancelled = true
				cancel()
			}
		})
		defer timer.Stop()
	}

	w.readSSE(reqCtx, resp, res)
	return res
}

func (w *world) readSSE(reqCtx context.Context, resp *http.Response, res *requesterResult) {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lastDelta := time.Time{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			res.done = true
			return
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				Delta        map[string]string `json:"delta"`
				FinishReason *string           `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			res.errText = "bad chunk: " + err.Error()
			return
		}
		if res.requestID == "" {
			res.requestID = strings.TrimPrefix(chunk.ID, "chatcmpl_")
		}
		for _, choice := range chunk.Choices {
			if delta := choice.Delta["content"]; delta != "" {
				now := time.Now()
				if res.firstDelta.IsZero() {
					res.firstDelta = now
				} else if gap := now.Sub(lastDelta); gap > res.maxGap {
					res.maxGap = gap
				}
				lastDelta = now
				res.content += delta
				res.deltaRunes = append(res.deltaRunes, len([]rune(delta)))
			}
			if choice.FinishReason != nil {
				res.finishReason = *choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		res.errText = classifyAbort(reqCtx, res, err)
	} else if !res.done {
		res.errText = "stream ended without [DONE]"
	}
	if reqCtx.Err() == context.DeadlineExceeded && !res.cancelled {
		res.stalled = true
	}
}

func classifyAbort(reqCtx context.Context, res *requesterResult, err error) string {
	if res.cancelled {
		return ""
	}
	if reqCtx.Err() == context.DeadlineExceeded {
		res.stalled = true
		return "deadline"
	}
	return err.Error()
}

func (w *world) record(res *requesterResult) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.results = append(w.results, res)
}

// ---------- responder ----------

type wsServerMsg struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Messages  []core.Message `json:"messages"`
	Fragment  string         `json:"fragment"`
	ClientSeq int64          `json:"client_seq"`
	Message   string         `json:"message"`
}

func (w *world) runResponder(ctx context.Context, idx int) {
	defer w.wg.Done()
	token, err := w.guestToken()
	if err != nil {
		return
	}
	for ctx.Err() == nil {
		w.serveConnection(ctx, token, idx)
		if ctx.Err() != nil {
			return
		}
		// churn or crash recovery: come back after a short break
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(500+rand.Intn(1500)) * time.Millisecond):
		}
	}
}

func (w *world) serveConnection(ctx context.Context, token string, idx int) {
	wsURL := "ws" + strings.TrimPrefix(w.base, "http") + "/ws/answer?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	msgs := make(chan wsServerMsg, 16)
	go func() {
		defer close(msgs)
		for {
			var msg wsServerMsg
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			msgs <- msg
		}
	}()

	session := fmt.Sprintf("res%d", idx)
	seq := int64(1)
	send := func(value any) bool { return conn.WriteJSON(value) == nil }

	if !send(map[string]any{"type": "available"}) {
		return
	}
	answered := 0
	for {
		msg, ok := w.await(ctx, msgs, "assigned")
		if !ok {
			return
		}
		w.mu.Lock()
		w.assignments++
		w.mu.Unlock()

		roll := rand.Float64()
		switch {
		case roll < w.sc.SkipProb:
			if !send(map[string]any{"type": "skip"}) {
				return
			}
			if _, ok := w.await(ctx, msgs, "skip_ack"); !ok {
				return
			}
		case roll < w.sc.SkipProb+w.sc.DropProb:
			// disconnect mid-answer: half the time after committing something
			if rand.Float64() < 0.5 {
				text := w.fragmentText(msg.RequestID, seq)
				if send(map[string]any{"type": "fragment", "client_seq": seq, "text": text}) {
					if ack, ok := w.awaitAck(ctx, msgs, session, msg.RequestID, seq); ok && ack {
						seq++
					}
				}
			}
			return
		default:
			if !w.typeAnswer(ctx, msgs, send, session, msg.RequestID, &seq) {
				return
			}
		}
		answered++
		if w.sc.ChurnProb > 0 && rand.Float64() < w.sc.ChurnProb {
			return // go offline for a bit; runResponder reconnects
		}
		if !send(map[string]any{"type": "available"}) {
			return
		}
	}
}

func (w *world) typeAnswer(ctx context.Context, msgs chan wsServerMsg, send func(any) bool, session, requestID string, seq *int64) bool {
	count := 1 + rand.Intn(5)
	interDelay := time.Duration(30+rand.Intn(200)) * time.Millisecond
	if w.sc.SlowReaderMode {
		count = 120
		interDelay = 2 * time.Millisecond
	}
	for i := 0; i < count; i++ {
		text := w.fragmentText(requestID, *seq)
		if !send(map[string]any{"type": "fragment", "client_seq": *seq, "text": text}) {
			return false
		}
		acked, ok := w.awaitAck(ctx, msgs, session, requestID, *seq)
		if !ok {
			return false
		}
		if !acked {
			// assignment died under us (cancel/timeout/complete): back to available
			return send(map[string]any{"type": "available"})
		}
		*seq++
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interDelay):
		}
	}
	if !send(map[string]any{"type": "finish"}) {
		return false
	}
	_, ok := w.await(ctx, msgs, "finish_ack")
	return ok
}

func (w *world) fragmentText(requestID string, seq int64) string {
	tail := requestID
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	if w.sc.SlowReaderMode {
		return fmt.Sprintf("R%s#%03d:%s\n", tail, seq, strings.Repeat("x", 990))
	}
	phrases := []string{"这题我熟，", "让我掐指一算，", "根据不存在的论文，", "答案显然是", "再补充一点玄学，", "总之信我。"}
	return fmt.Sprintf("R%s#%d:%s", tail, seq, phrases[rand.Intn(len(phrases))])
}

// await reads messages until the wanted type arrives; tolerates acks/pongs in between.
func (w *world) await(ctx context.Context, msgs chan wsServerMsg, wanted string) (wsServerMsg, bool) {
	for {
		select {
		case <-ctx.Done():
			return wsServerMsg{}, false
		case msg, ok := <-msgs:
			if !ok {
				return wsServerMsg{}, false
			}
			if msg.Type == wanted {
				return msg, true
			}
			if msg.Type == "error" {
				w.mu.Lock()
				w.ackErrors[msg.Message]++
				w.mu.Unlock()
			}
		}
	}
}

// awaitAck returns (acked, connectionAlive); acked=false means the assignment is gone.
func (w *world) awaitAck(ctx context.Context, msgs chan wsServerMsg, session, requestID string, seq int64) (bool, bool) {
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return false, false
		case <-deadline:
			w.mu.Lock()
			w.ackErrors["ack timeout"]++
			w.mu.Unlock()
			return false, false
		case msg, ok := <-msgs:
			if !ok {
				return false, false
			}
			switch msg.Type {
			case "fragment_ack":
				if msg.ClientSeq != seq {
					w.mu.Lock()
					w.ackErrors[fmt.Sprintf("ack seq mismatch want %d got %d", seq, msg.ClientSeq)]++
					w.mu.Unlock()
				}
				w.mu.Lock()
				w.acked[requestID] = append(w.acked[requestID], ackedFrag{session: session, text: msg.Fragment, at: time.Now()})
				w.mu.Unlock()
				return true, true
			case "error":
				w.mu.Lock()
				w.ackErrors[msg.Message]++
				w.mu.Unlock()
				return false, true
			}
		}
	}
}
