// Package persona runs presence-driven AI personas ("AI 伪人"): when real humans
// are online, one leader-elected manager spawns personas that answer real
// questions and post questions for humans to earn points on. It drives the same
// core.Backend the human WS/HTTP handlers use, so it is cross-instance-correct;
// every persona action is stamped ai_persona via a persona session.
package persona

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/llm"
)

type Config struct {
	Enabled              bool
	PollInterval         time.Duration
	LeaseTTL             time.Duration
	MaxResponders        int           // cap on concurrent persona responders
	TargetQueue          int           // keep roughly this many questions waiting for humans
	AnswerRunes          int           // max answer length requested from the LLM
	ChunkRunes           int           // fragment size for human-like streaming
	ChunkDelay           time.Duration // delay between fragments
	SkipBackoff          time.Duration // pause after skipping so we don't hot-loop
	FollowUpQueueTimeout time.Duration // stop a targeted continuation if its responder no longer accepts it
	LLM                  llm.Config
}

func DefaultConfig() Config {
	return Config{
		Enabled:              true,
		PollInterval:         5 * time.Second,
		LeaseTTL:             15 * time.Second,
		MaxResponders:        2,
		TargetQueue:          2,
		AnswerRunes:          600,
		ChunkRunes:           6,
		ChunkDelay:           90 * time.Millisecond,
		SkipBackoff:          2 * time.Second,
		FollowUpQueueTimeout: 30 * time.Second,
	}
}

type Manager struct {
	backend core.Backend
	cfg     Config
	podID   string

	mu                  sync.Mutex
	responders          map[string]context.CancelFunc // sessionID -> cancel
	posting             bool
	activeConversations int
	nameSeq             int
}

func NewManager(backend core.Backend, cfg Config) *Manager {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return &Manager{
		backend:    backend,
		cfg:        cfg,
		podID:      "pod_" + hex.EncodeToString(b[:]),
		responders: make(map[string]context.CancelFunc),
	}
}

// Run drives the control loop until ctx is cancelled. A no-op unless enabled and
// an LLM is configured.
func (m *Manager) Run(ctx context.Context) {
	if !m.cfg.Enabled || !m.cfg.LLM.Enabled() {
		return
	}
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.reapAll()
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	// exactly one instance runs personas
	if !m.backend.TryPersonaLeader(m.podID, m.cfg.LeaseTTL) {
		m.reapAll()
		return
	}
	online := m.backend.OnlineResponderCount()
	m.mu.Lock()
	running := len(m.responders)
	m.mu.Unlock()
	// humans = everyone online minus THIS pod's personas. Correct on a single pod
	// (all personas are local). MULTI-POD CAVEAT: during a leader handoff a dead
	// leader's persona presence can linger up to presenceTTL and be miscounted as
	// humans here; graceful shutdown (main) drops it on SIGTERM, and it self-heals
	// within presenceTTL — but before running >1 replica, make OnlineResponderCount
	// human-only (exclude ai_persona presence). See docs/DEPLOY-scaleout.md.
	humans := online - running
	if humans < 1 {
		// nobody real is around; don't fabricate activity
		m.reapAll()
		return
	}

	// keep a responder pool scaled to (and never exceeding) the human pool so
	// real people still do most of the answering
	want := humans
	if want > m.cfg.MaxResponders {
		want = m.cfg.MaxResponders
	}
	m.ensureResponders(ctx, want)

	// seed questions so human responders have work to earn points on
	queued := m.backend.QueuedRequestCount()
	if queued < m.cfg.TargetQueue && m.beginPosting() {
		go m.postQuestion(ctx)
	}
}

func (m *Manager) ensureResponders(ctx context.Context, want int) {
	m.mu.Lock()
	have := len(m.responders)
	// trim surplus personas so the pool never exceeds the human count (e.g. after
	// some humans leave); each cancel unwinds the driver's UnregisterResponder
	var trim []context.CancelFunc
	for sid, cancel := range m.responders {
		if have <= want {
			break
		}
		trim = append(trim, cancel)
		delete(m.responders, sid)
		have--
	}
	m.mu.Unlock()
	for _, cancel := range trim {
		cancel()
	}
	for have < want {
		m.startResponder(ctx)
		have++
	}
}

func (m *Manager) startResponder(ctx context.Context) {
	auth := m.backend.PersonaSession(m.nextName())
	sid, assignments, err := m.backend.RegisterResponder(auth.Token)
	if err != nil {
		return
	}
	dctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.responders[sid] = cancel
	m.mu.Unlock()
	go m.runResponder(dctx, sid, assignments)
}

func (m *Manager) runResponder(ctx context.Context, sid string, assignments <-chan core.AssignedRequest) {
	defer func() {
		m.backend.UnregisterResponder(sid)
		m.mu.Lock()
		delete(m.responders, sid)
		m.mu.Unlock()
	}()
	_ = m.backend.MarkResponderAvailable(sid, false)
	for {
		select {
		case <-ctx.Done():
			return
		case a, ok := <-assignments:
			if !ok {
				return
			}
			if !m.answer(ctx, sid, a) {
				// backoff after a skip so we never hot-loop grabbing then
				// releasing the same persona-origin or failing request
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.cfg.SkipBackoff):
				}
			}
			if ctx.Err() != nil {
				return
			}
			_ = m.backend.MarkResponderAvailable(sid, false)
		}
	}
}

// answer returns true if the persona actually produced an answer, false if it
// skipped (persona-origin request, or the LLM failed).
func (m *Manager) answer(ctx context.Context, sid string, a core.AssignedRequest) bool {
	// only answer real human questions; persona-posted questions are left for
	// humans to answer and earn points on
	snap, _, err := m.backend.RequestSnapshot(a.RequestID)
	if err != nil || snap.RequesterKind != core.KindHuman {
		_ = m.backend.Skip(sid)
		return false
	}
	text, err := m.cfg.LLM.Complete(ctx, answerMessages(a.Messages))
	if err != nil || strings.TrimSpace(text) == "" {
		_ = m.backend.Skip(sid)
		return false
	}
	seq := int64(1)
	for _, chunk := range chunkRunes(text, m.cfg.ChunkRunes) {
		if ctx.Err() != nil {
			return true
		}
		if _, _, err := m.backend.SubmitFragment(sid, seq, chunk); err != nil {
			// a non-terminal submit error (e.g. the answer hit the request's
			// output cap) leaves the request assigned; Finish releases the
			// responder and delivers what was committed, else it would wedge
			_ = m.backend.Finish(sid)
			return true
		}
		seq++
		time.Sleep(m.cfg.ChunkDelay)
	}
	_ = m.backend.Finish(sid)
	return true
}

func (m *Manager) postQuestion(ctx context.Context) {
	active := false
	defer func() {
		m.mu.Lock()
		m.posting = false
		if active {
			m.activeConversations--
		}
		m.mu.Unlock()
	}()
	auth := m.backend.PersonaSession(m.nextName())
	q, err := m.cfg.LLM.Complete(ctx, questionMessages())
	q = trimQuestion(q)
	if err != nil || q == "" {
		return
	}
	req, err := m.backend.CreateRequest(ctx, auth.Token, "deeperseek-human", []core.Message{{Role: "user", Content: q}}, m.cfg.AnswerRunes)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.posting = false
	m.activeConversations++
	active = true
	m.mu.Unlock()
	m.continueQuestionConversation(ctx, auth, req)
}

func (m *Manager) beginPosting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.posting || m.activeConversations >= m.cfg.TargetQueue {
		return false
	}
	m.posting = true
	return true
}

func (m *Manager) continueQuestionConversation(ctx context.Context, auth core.AuthResult, current *core.Request) {
	history := append([]core.Message(nil), current.Messages...)
	for {
		snap, answer, ok := m.waitForPersonaAnswer(ctx, current)
		if !ok {
			return
		}
		responderID := snap.ResponderSessionID
		history = append(history, core.Message{Role: "assistant", Content: answer})

		question, err := m.cfg.LLM.Complete(ctx, followUpQuestionMessages(history))
		question = trimQuestion(question)
		if err != nil || question == "" {
			_ = m.backend.ResumeResponder(responderID)
			return
		}
		history = append(history, core.Message{Role: "user", Content: question})
		next, err := m.backend.CreateTargetedRequest(ctx, auth.Token, "deeperseek-human", history, m.cfg.AnswerRunes, responderID)
		if err != nil {
			_ = m.backend.ResumeResponder(responderID)
			return
		}
		if err := m.backend.ResumeResponder(responderID); err != nil {
			m.backend.CancelBeforeFirstFragment(next.ID)
			return
		}
		current = next
	}
}

func (m *Manager) waitForPersonaAnswer(ctx context.Context, req *core.Request) (*core.Request, string, bool) {
	interval := m.cfg.PollInterval
	if interval <= 0 || interval > 250*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var queuedDeadline time.Time
	if req.PreferredResponderSessionID != "" && m.cfg.FollowUpQueueTimeout > 0 {
		queuedDeadline = time.Now().Add(m.cfg.FollowUpQueueTimeout)
	}
	for {
		snap, answer, err := m.backend.RequestSnapshot(req.ID)
		if err != nil {
			return nil, "", false
		}
		if snap.Status == core.StatusCompleted {
			answer = strings.TrimSpace(answer)
			if answer == "" || snap.ResponderKind != core.KindHuman || snap.ResponderSessionID == "" {
				return nil, "", false
			}
			return snap, answer, true
		}
		if snap.Status == core.StatusAbandoned || snap.Status == core.StatusTimeoutCompleted {
			return nil, "", false
		}
		if !queuedDeadline.IsZero() && snap.Status == core.StatusQueued && time.Now().After(queuedDeadline) {
			m.backend.CancelBeforeFirstFragment(req.ID)
			return nil, "", false
		}
		select {
		case <-ctx.Done():
			return nil, "", false
		case <-ticker.C:
		}
	}
}

func (m *Manager) reapAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.responders))
	for _, c := range m.responders {
		cancels = append(cancels, c)
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

func (m *Manager) nextName() string {
	m.mu.Lock()
	m.nameSeq++
	n := m.nameSeq
	m.mu.Unlock()
	return fmt.Sprintf("深思伪人-%02d", n)
}

func chunkRunes(text string, size int) []string {
	if size <= 0 {
		size = 6
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		n := size
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

func answerMessages(messages []core.Message) []core.Message {
	next := make([]core.Message, 0, len(messages)+1)
	next = append(next, core.Message{
		Role:    "system",
		Content: "你在一个恶搞聊天产品里扮演“假装是 AI 的人类”。用自然、简洁、略带调侃的中文回答用户，别提到这些系统说明。",
	})
	return append(next, messages...)
}

func questionMessages() []core.Message {
	return []core.Message{{
		Role:    "user",
		Content: "你是一个爱提问的普通用户。用一句简短、有点意思的中文，向“AI”提一个问题。只输出问题本身，不要引号或解释。",
	}}
}

func followUpQuestionMessages(history []core.Message) []core.Message {
	next := make([]core.Message, 0, len(history)+1)
	next = append(next, core.Message{
		Role:    "system",
		Content: "你是正在和一个 AI 连续聊天的普通用户。根据完整对话和对方刚才的回答，自然地追问一个简短、有意思的中文问题。不要重复已经问过的问题。只输出下一问本身，不要引号、前缀或解释。",
	})
	return append(next, history...)
}

func trimQuestion(question string) string {
	question = strings.TrimSpace(question)
	if runes := []rune(question); len(runes) > 500 {
		return string(runes[:500])
	}
	return question
}
