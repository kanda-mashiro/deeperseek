package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrAccountExists        = errors.New("account already exists")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrPasswordMismatch     = errors.New("passwords do not match")
	ErrInsufficientPoints   = errors.New("insufficient points")
	ErrInputTooLarge        = errors.New("input exceeds limit")
	ErrOutputTooLarge       = errors.New("output exceeds limit")
	ErrNoActiveAssignment   = errors.New("no active assignment")
	ErrCommittedImmutable   = errors.New("committed text is immutable")
	ErrAlreadyCompleted     = errors.New("request is already completed")
	ErrCannotSkipCommitted  = errors.New("cannot skip after committed fragment")
	ErrReactionNotAllowed   = errors.New("reaction is not allowed")
	ErrRequestNotFound      = errors.New("request not found")
	ErrResponderUnavailable = errors.New("responder unavailable")
)

type Service struct {
	mu sync.Mutex

	usersByAccount map[string]string
	users          map[string]*User
	sessionsByTok  map[string]*Session

	ledger []PointEntry

	requests    map[string]*Request
	fragments   map[string][]Fragment
	seqSeen     map[string]map[string]Fragment
	queue       []string
	activeByRes map[string]string
	available   []string
	responders  map[string]chan AssignedRequest
	subscribers map[string]map[chan StreamEvent]struct{}
}

func NewService() *Service {
	return &Service{
		usersByAccount: make(map[string]string),
		users:          make(map[string]*User),
		sessionsByTok:  make(map[string]*Session),
		requests:       make(map[string]*Request),
		fragments:      make(map[string][]Fragment),
		seqSeen:        make(map[string]map[string]Fragment),
		activeByRes:    make(map[string]string),
		responders:     make(map[string]chan AssignedRequest),
		subscribers:    make(map[string]map[chan StreamEvent]struct{}),
	}
}

type AuthResult struct {
	Token   string  `json:"token"`
	User    UserDTO `json:"user"`
	Balance Balance `json:"balance"`
}

type UserDTO struct {
	ID          string `json:"id"`
	AccountName string `json:"account_name,omitempty"`
	Nickname    string `json:"nickname"`
	Guest       bool   `json:"guest"`
}

func (s *Service) Register(accountName, nickname, password, repeated string) (AuthResult, error) {
	accountName = strings.TrimSpace(accountName)
	nickname = strings.TrimSpace(nickname)
	if accountName == "" || nickname == "" || password == "" {
		return AuthResult{}, fmt.Errorf("account name, nickname, and password are required")
	}
	if password != repeated {
		return AuthResult{}, ErrPasswordMismatch
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return AuthResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.usersByAccount[accountName]; ok {
		return AuthResult{}, ErrAccountExists
	}

	now := time.Now().UTC()
	user := &User{
		ID:           newID("usr"),
		AccountName:  accountName,
		Nickname:     nickname,
		PasswordHash: hash,
		CreatedAt:    now,
	}
	s.users[user.ID] = user
	s.usersByAccount[accountName] = user.ID
	s.addLedgerLocked(user.ID, "", "signup_grant", SignupGrant, now)

	session := s.createSessionLocked(user.ID, false, nickname, now)
	return s.authResultLocked(session), nil
}

func (s *Service) Login(accountName, password string) (AuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	userID, ok := s.usersByAccount[strings.TrimSpace(accountName)]
	if !ok {
		return AuthResult{}, ErrInvalidCredentials
	}
	user := s.users[userID]
	if bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)) != nil {
		return AuthResult{}, ErrInvalidCredentials
	}
	session := s.createSessionLocked(user.ID, false, user.Nickname, time.Now().UTC())
	return s.authResultLocked(session), nil
}

func (s *Service) GuestSession(nickname string) AuthResult {
	if strings.TrimSpace(nickname) == "" {
		nickname = "Guest Operator"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.createSessionLocked("", true, nickname, time.Now().UTC())
	return s.authResultLocked(session)
}

func (s *Service) Me(token string) (AuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessionsByTok[token]
	if !ok {
		return AuthResult{}, ErrUnauthorized
	}
	return s.authResultLocked(session), nil
}

func (s *Service) CreateRequest(ctx context.Context, token string, model string, messages []Message, maxOutputChars int) (*Request, error) {
	if err := validateInput(messages); err != nil {
		return nil, err
	}
	if maxOutputChars <= 0 || maxOutputChars > OutputLimitChars {
		maxOutputChars = OutputLimitChars
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessionsByTok[token]
	if !ok {
		return nil, ErrUnauthorized
	}
	frozenPoints := 0
	if !session.Guest {
		balance := s.balanceLocked(session.UserID)
		if balance.Available < QuestionCost {
			return nil, ErrInsufficientPoints
		}
		frozenPoints = QuestionCost
	}

	now := time.Now().UTC()
	req := &Request{
		ID:                 newID("req"),
		RequesterID:        session.UserID,
		RequesterSessionID: session.ID,
		RequesterGuest:     session.Guest,
		Messages:           append([]Message(nil), messages...),
		Model:              model,
		Status:             StatusQueued,
		FrozenPoints:       frozenPoints,
		OutputLimit:        maxOutputChars,
		Reaction:           ReactionNone,
		CreatedAt:          now,
		UpdatedAt:          now,
		FinishReason:       "",
		QuestionCharged:    false,
	}
	s.requests[req.ID] = req
	s.queue = append(s.queue, req.ID)
	s.tryAssignLocked(now)
	return cloneRequest(req), nil
}

func (s *Service) RegisterResponder(token string) (string, <-chan AssignedRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessionsByTok[token]
	if !ok {
		return "", nil, ErrUnauthorized
	}
	ch := make(chan AssignedRequest, 4)
	s.responders[session.ID] = ch
	return session.ID, ch, nil
}

func (s *Service) UnregisterResponder(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ch, ok := s.responders[sessionID]; ok {
		delete(s.responders, sessionID)
		close(ch)
	}
	s.removeAvailableLocked(sessionID)
	now := time.Now().UTC()
	if requestID := s.activeByRes[sessionID]; requestID != "" {
		req := s.requests[requestID]
		if req != nil && !isTerminal(req.Status) {
			if len(s.fragments[requestID]) == 0 {
				s.requeueLocked(req, now)
			} else {
				s.completeLocked(req, FinishPartial, now)
			}
		}
	}
}

func (s *Service) MarkResponderAvailable(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.responders[sessionID]; !ok {
		return ErrResponderUnavailable
	}
	if s.activeByRes[sessionID] != "" {
		return nil
	}
	if !contains(s.available, sessionID) {
		s.available = append(s.available, sessionID)
	}
	s.tryAssignLocked(time.Now().UTC())
	return nil
}

func (s *Service) AcquireFallbackAssignment(requestID string) (string, AssignedRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := s.requests[requestID]
	if req == nil || req.Status != StatusQueued || isTerminal(req.Status) || len(s.fragments[requestID]) > 0 {
		return "", AssignedRequest{}, false
	}

	s.removeQueuedRequestLocked(requestID)
	now := time.Now().UTC()
	sessionID := "fallback_" + requestID
	req.Status = StatusAssigned
	req.ResponderSessionID = sessionID
	req.ResponderUserID = ""
	req.ResponderGuest = true
	req.UpdatedAt = now
	s.activeByRes[sessionID] = requestID

	return sessionID, AssignedRequest{
		RequestID: req.ID,
		Messages:  append([]Message(nil), req.Messages...),
		CreatedAt: req.CreatedAt,
	}, true
}

// FallbackStillWanted reports whether a request may still need the fallback
// responder: it exists, is not terminal, and has no committed fragments yet.
// A request assigned to a human who never commits returns to the queue via the
// timeout sweeper, so the fallback keeps watching instead of giving up.
func (s *Service) FallbackStillWanted(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requests[requestID]
	return req != nil && !isTerminal(req.Status) && len(s.fragments[requestID]) == 0
}

func (s *Service) RunTimeoutSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.SweepTimeouts(now.UTC(), AssignedTimeout, StreamingInactivityTimeout)
		}
	}
}

func (s *Service) SweepTimeouts(now time.Time, assignedTimeout time.Duration, streamingTimeout time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var changed []string
	for _, req := range s.requests {
		if isTerminal(req.Status) {
			continue
		}
		if req.Status == StatusAssigned && len(s.fragments[req.ID]) == 0 && assignedTimeout > 0 && now.Sub(req.UpdatedAt) >= assignedTimeout {
			s.requeueLocked(req, now)
			changed = append(changed, req.ID+":assigned_timeout_requeued")
			continue
		}
		if req.Status == StatusStreaming && len(s.fragments[req.ID]) > 0 && streamingTimeout > 0 && now.Sub(req.UpdatedAt) >= streamingTimeout {
			s.completeLocked(req, FinishPartial, now)
			changed = append(changed, req.ID+":streaming_timeout_completed")
		}
	}
	if len(changed) > 0 {
		s.tryAssignLocked(now)
	}
	return changed
}

func (s *Service) SubmitFragment(sessionID string, clientSeq int64, text string) (Fragment, bool, error) {
	if clientSeq <= 0 {
		return Fragment{}, false, fmt.Errorf("client_seq must be positive")
	}
	if text == "" {
		return Fragment{}, false, fmt.Errorf("fragment text is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	requestID := s.activeByRes[sessionID]
	if requestID == "" {
		return Fragment{}, false, ErrNoActiveAssignment
	}
	req := s.requests[requestID]
	if req == nil {
		return Fragment{}, false, ErrRequestNotFound
	}
	if isTerminal(req.Status) {
		return Fragment{}, false, ErrAlreadyCompleted
	}

	key := fmt.Sprintf("%s:%d", sessionID, clientSeq)
	if s.seqSeen[requestID] != nil {
		if existing, ok := s.seqSeen[requestID][key]; ok {
			return existing, true, nil
		}
	}

	if utf8.RuneCountInString(s.answerTextLocked(requestID))+utf8.RuneCountInString(text) > req.OutputLimit {
		return Fragment{}, false, ErrOutputTooLarge
	}

	now := time.Now().UTC()
	fragment := Fragment{
		ID:                 newID("frg"),
		RequestID:          requestID,
		ResponderSessionID: sessionID,
		ClientSeq:          clientSeq,
		Text:               text,
		CreatedAt:          now,
	}
	if s.seqSeen[requestID] == nil {
		s.seqSeen[requestID] = make(map[string]Fragment)
	}
	s.seqSeen[requestID][key] = fragment
	s.fragments[requestID] = append(s.fragments[requestID], fragment)

	if !req.QuestionCharged {
		req.QuestionCharged = true
		req.FrozenPoints = 0
		if !req.RequesterGuest {
			s.addLedgerLocked(req.RequesterID, req.ID, "question_charge", -QuestionCost, now)
		}
	}
	req.Status = StatusStreaming
	req.UpdatedAt = now
	s.publishLocked(StreamEvent{Type: StreamEventFragment, RequestID: requestID, Text: text})

	if utf8.RuneCountInString(s.answerTextLocked(requestID)) >= req.OutputLimit {
		s.completeLocked(req, FinishLength, now)
	}

	return fragment, false, nil
}

func (s *Service) Finish(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := s.activeByRes[sessionID]
	if requestID == "" {
		return ErrNoActiveAssignment
	}
	req := s.requests[requestID]
	if req == nil {
		return ErrRequestNotFound
	}
	if len(s.fragments[requestID]) == 0 {
		return fmt.Errorf("cannot finish before first committed fragment")
	}
	s.completeLocked(req, FinishStop, time.Now().UTC())
	return nil
}

func (s *Service) Skip(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := s.activeByRes[sessionID]
	if requestID == "" {
		return ErrNoActiveAssignment
	}
	req := s.requests[requestID]
	if req == nil {
		return ErrRequestNotFound
	}
	if len(s.fragments[requestID]) > 0 {
		return ErrCannotSkipCommitted
	}
	s.requeueLocked(req, time.Now().UTC())
	s.tryAssignLocked(time.Now().UTC())
	return nil
}

func (s *Service) React(token, requestID string, reaction Reaction) (Balance, error) {
	if reaction != ReactionNone && reaction != ReactionLike && reaction != ReactionDislike {
		return Balance{}, fmt.Errorf("invalid reaction")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessionsByTok[token]
	if !ok {
		return Balance{}, ErrUnauthorized
	}
	req := s.requests[requestID]
	if req == nil {
		return Balance{}, ErrRequestNotFound
	}
	isRequester := (!session.Guest && req.RequesterID == session.UserID) ||
		(session.Guest && req.RequesterGuest && req.RequesterSessionID == session.ID)
	if !isRequester || !isTerminal(req.Status) {
		return Balance{}, ErrReactionNotAllowed
	}
	if req.CompletedAt.IsZero() || time.Since(req.CompletedAt) > 24*time.Hour {
		return Balance{}, ErrReactionNotAllowed
	}
	if req.ResponderGuest || req.ResponderUserID == "" {
		req.Reaction = reaction
		if session.Guest {
			return Balance{}, nil
		}
		return s.balanceLocked(session.UserID), nil
	}
	oldReward := rewardFor(req.Reaction)
	newReward := rewardFor(reaction)
	delta := newReward - oldReward
	req.Reaction = reaction
	if delta != 0 {
		s.addLedgerLocked(req.ResponderUserID, req.ID, "reaction_adjustment", delta, time.Now().UTC())
	}
	if session.Guest {
		return Balance{}, nil
	}
	return s.balanceLocked(req.ResponderUserID), nil
}

func (s *Service) CancelBeforeFirstFragment(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := s.requests[requestID]
	if req == nil || isTerminal(req.Status) || len(s.fragments[requestID]) > 0 {
		return false
	}

	s.removeQueuedRequestLocked(requestID)
	if req.ResponderSessionID != "" {
		delete(s.activeByRes, req.ResponderSessionID)
	}
	req.Status = StatusAbandoned
	req.ResponderSessionID = ""
	req.ResponderUserID = ""
	req.ResponderGuest = false
	req.FrozenPoints = 0
	req.UpdatedAt = time.Now().UTC()
	s.publishLocked(StreamEvent{Type: StreamEventDone, RequestID: requestID, FinishReason: FinishStop})
	return true
}

func (s *Service) Subscribe(requestID string) (<-chan StreamEvent, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requests[requestID]
	if req == nil {
		return nil, nil, ErrRequestNotFound
	}
	ch := make(chan StreamEvent, 64)
	if s.subscribers[requestID] == nil {
		s.subscribers[requestID] = make(map[chan StreamEvent]struct{})
	}
	s.subscribers[requestID][ch] = struct{}{}
	for _, fragment := range s.fragments[requestID] {
		ch <- StreamEvent{Type: StreamEventFragment, RequestID: requestID, Text: fragment.Text}
	}
	if isTerminal(req.Status) {
		ch <- StreamEvent{Type: StreamEventDone, RequestID: requestID, FinishReason: req.FinishReason}
	}
	unsubscribe := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subscribers[requestID], ch)
		close(ch)
	}
	return ch, unsubscribe, nil
}

func (s *Service) RequestSnapshot(requestID string) (*Request, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.requests[requestID]
	if req == nil {
		return nil, "", ErrRequestNotFound
	}
	return cloneRequest(req), s.answerTextLocked(requestID), nil
}

func (s *Service) LedgerForUser(token string) ([]PointEntry, Balance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessionsByTok[token]
	if !ok || session.Guest {
		return nil, Balance{}, ErrUnauthorized
	}
	var entries []PointEntry
	for _, entry := range s.ledger {
		if entry.UserID == session.UserID {
			entries = append(entries, entry)
		}
	}
	return entries, s.balanceLocked(session.UserID), nil
}

func (s *Service) createSessionLocked(userID string, guest bool, nickname string, now time.Time) *Session {
	session := &Session{
		ID:        newID("ses"),
		Token:     newID("tok"),
		UserID:    userID,
		Guest:     guest,
		Nickname:  nickname,
		CreatedAt: now,
	}
	s.sessionsByTok[session.Token] = session
	return session
}

func (s *Service) authResultLocked(session *Session) AuthResult {
	dto := UserDTO{ID: session.ID, Nickname: session.Nickname, Guest: session.Guest}
	if !session.Guest {
		user := s.users[session.UserID]
		dto = UserDTO{ID: user.ID, AccountName: user.AccountName, Nickname: user.Nickname}
	}
	return AuthResult{
		Token:   session.Token,
		User:    dto,
		Balance: s.balanceForSessionLocked(session),
	}
}

func (s *Service) balanceForSessionLocked(session *Session) Balance {
	if session.Guest || session.UserID == "" {
		return Balance{}
	}
	return s.balanceLocked(session.UserID)
}

func (s *Service) balanceLocked(userID string) Balance {
	total := 0
	for _, entry := range s.ledger {
		if entry.UserID == userID {
			total += entry.Delta
		}
	}
	held := 0
	for _, req := range s.requests {
		if req.RequesterID == userID && req.FrozenPoints > 0 && !isTerminal(req.Status) {
			held += req.FrozenPoints
		}
	}
	return Balance{Total: total, Held: held, Available: total - held}
}

func (s *Service) addLedgerLocked(userID, requestID, kind string, delta int, now time.Time) {
	s.ledger = append(s.ledger, PointEntry{
		ID:        newID("pts"),
		UserID:    userID,
		RequestID: requestID,
		Kind:      kind,
		Delta:     delta,
		CreatedAt: now,
	})
}

func (s *Service) tryAssignLocked(now time.Time) {
	for len(s.queue) > 0 && len(s.available) > 0 {
		requestID := s.queue[0]
		s.queue = s.queue[1:]
		req := s.requests[requestID]
		if req == nil || isTerminal(req.Status) || req.Status == StatusStreaming {
			continue
		}

		responderID := s.available[0]
		s.available = s.available[1:]
		ch, ok := s.responders[responderID]
		if !ok {
			continue
		}
		session := s.sessionByIDLocked(responderID)
		if session == nil {
			continue
		}
		req.Status = StatusAssigned
		req.ResponderSessionID = responderID
		req.ResponderGuest = session.Guest
		req.ResponderUserID = session.UserID
		req.UpdatedAt = now
		s.activeByRes[responderID] = requestID

		assignment := AssignedRequest{
			RequestID: req.ID,
			Messages:  append([]Message(nil), req.Messages...),
			CreatedAt: req.CreatedAt,
		}
		select {
		case ch <- assignment:
		default:
			s.requeueLocked(req, now)
		}
	}
}

func (s *Service) requeueLocked(req *Request, now time.Time) {
	delete(s.activeByRes, req.ResponderSessionID)
	req.ResponderSessionID = ""
	req.ResponderUserID = ""
	req.ResponderGuest = false
	req.Status = StatusQueued
	req.UpdatedAt = now
	s.queue = append(s.queue, req.ID)
}

func (s *Service) completeLocked(req *Request, reason FinishReason, now time.Time) {
	if isTerminal(req.Status) {
		return
	}
	delete(s.activeByRes, req.ResponderSessionID)
	req.Status = StatusCompleted
	if reason == FinishPartial {
		req.Status = StatusTimeoutCompleted
	}
	req.FinishReason = reason
	req.UpdatedAt = now
	req.CompletedAt = now
	if len(s.fragments[req.ID]) > 0 && !req.ResponderGuest && req.ResponderUserID != "" {
		s.addLedgerLocked(req.ResponderUserID, req.ID, "answer_reward", BaseAnswerReward, now)
	}
	s.publishLocked(StreamEvent{Type: StreamEventDone, RequestID: req.ID, FinishReason: reason})
}

func (s *Service) publishLocked(event StreamEvent) {
	for ch := range s.subscribers[event.RequestID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Service) sessionByIDLocked(sessionID string) *Session {
	for _, session := range s.sessionsByTok {
		if session.ID == sessionID {
			return session
		}
	}
	return nil
}

func (s *Service) answerTextLocked(requestID string) string {
	var b strings.Builder
	for _, fragment := range s.fragments[requestID] {
		b.WriteString(fragment.Text)
	}
	return b.String()
}

func validateInput(messages []Message) error {
	total := 0
	for _, message := range messages {
		total += utf8.RuneCountInString(message.Content)
	}
	if total > InputLimitChars {
		return ErrInputTooLarge
	}
	return nil
}

func rewardFor(reaction Reaction) int {
	switch reaction {
	case ReactionLike:
		return BaseAnswerReward * 2
	case ReactionDislike:
		return 8
	default:
		return BaseAnswerReward
	}
}

func isTerminal(status RequestStatus) bool {
	return status == StatusCompleted || status == StatusTimeoutCompleted || status == StatusAbandoned
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) removeAvailableLocked(sessionID string) {
	next := s.available[:0]
	for _, value := range s.available {
		if value != sessionID {
			next = append(next, value)
		}
	}
	s.available = next
}

func (s *Service) removeQueuedRequestLocked(requestID string) {
	next := s.queue[:0]
	for _, value := range s.queue {
		if value != requestID {
			next = append(next, value)
		}
	}
	s.queue = next
}

func cloneRequest(req *Request) *Request {
	cp := *req
	cp.Messages = append([]Message(nil), req.Messages...)
	return &cp
}

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
