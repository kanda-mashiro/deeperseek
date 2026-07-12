package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegisterGrantsTwentyPoints(t *testing.T) {
	svc := NewService()
	auth, err := svc.Register("alice", "Alice", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if auth.Balance.Total != 20 || auth.Balance.Available != 20 || auth.Balance.Held != 0 {
		t.Fatalf("unexpected balance: %+v", auth.Balance)
	}
}

func TestQuestionFreezesThenChargesOnFirstFragment(t *testing.T) {
	svc := NewService()
	requester, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	responder, _ := svc.Register("bob", "Bob", "pass1234", "pass1234")

	sessionID, assignments, err := svc.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("available: %v", err)
	}

	req, err := svc.CreateRequest(context.Background(), requester.Token, "deeperseek-human", []Message{{Role: "user", Content: "hello"}}, 0)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	me, _ := svc.Me(requester.Token)
	if me.Balance.Total != 20 || me.Balance.Held != 5 || me.Balance.Available != 15 {
		t.Fatalf("expected frozen points, got %+v", me.Balance)
	}

	select {
	case assignment := <-assignments:
		if assignment.RequestID != req.ID {
			t.Fatalf("assignment request mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("expected assignment")
	}

	if _, _, err := svc.SubmitFragment(sessionID, 1, "human answer"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	me, _ = svc.Me(requester.Token)
	if me.Balance.Total != 15 || me.Balance.Held != 0 || me.Balance.Available != 15 {
		t.Fatalf("expected charged points, got %+v", me.Balance)
	}
}

func TestGuestCanCreateRequestWithoutPoints(t *testing.T) {
	svc := NewService()
	requester := svc.GuestSession("")
	responder, _ := svc.Register("bob", "Bob", "pass1234", "pass1234")

	sessionID, assignments, err := svc.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("available: %v", err)
	}

	req, err := svc.CreateRequest(context.Background(), requester.Token, "deeperseek-human", []Message{{Role: "user", Content: "hello"}}, 0)
	if err != nil {
		t.Fatalf("guest create request: %v", err)
	}
	if req.FrozenPoints != 0 || !req.RequesterGuest {
		t.Fatalf("guest request should not freeze points: %+v", req)
	}
	select {
	case assignment := <-assignments:
		if assignment.RequestID != req.ID {
			t.Fatalf("assignment request mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("expected assignment")
	}
	if _, _, err := svc.SubmitFragment(sessionID, 1, "human answer"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	if err := svc.Finish(sessionID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := svc.React(requester.Token, req.ID, ReactionLike); err != nil {
		t.Fatalf("guest reaction: %v", err)
	}
}

func TestFIFOAssignmentAndSingleActiveResponder(t *testing.T) {
	svc := NewService()
	requester, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	responder, _ := svc.Register("bob", "Bob", "pass1234", "pass1234")

	req1, err := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "first"}}, 0)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	req2, err := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "second"}}, 0)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	sessionID, assignments, _ := svc.RegisterResponder(responder.Token)
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("available: %v", err)
	}
	got := <-assignments
	if got.RequestID != req1.ID {
		t.Fatalf("expected FIFO assignment %s, got %s", req1.ID, got.RequestID)
	}
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("second available should be harmless: %v", err)
	}
	select {
	case extra := <-assignments:
		t.Fatalf("responder should not receive second active assignment: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	snap, _, _ := svc.RequestSnapshot(req2.ID)
	if snap.Status != StatusQueued {
		t.Fatalf("second request should remain queued, got %s", snap.Status)
	}
}

func TestFragmentIdempotencyAndAppendOnly(t *testing.T) {
	svc, sessionID := assignedService(t)

	first, duplicate, err := svc.SubmitFragment(sessionID, 1, "hello")
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	again, duplicate, err := svc.SubmitFragment(sessionID, 1, "hello")
	if err != nil {
		t.Fatalf("submit duplicate: %v", err)
	}
	if !duplicate || again.ID != first.ID {
		t.Fatalf("expected idempotent duplicate, got duplicate=%v first=%s again=%s", duplicate, first.ID, again.ID)
	}
	_, text, _ := svc.RequestSnapshot(first.RequestID)
	if text != "hello" {
		t.Fatalf("expected one fragment, got %q", text)
	}
}

func TestSkipBeforeFirstFragmentOnly(t *testing.T) {
	svc, sessionID := assignedService(t)
	if err := svc.Skip(sessionID); err != nil {
		t.Fatalf("skip before fragment: %v", err)
	}

	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("available again: %v", err)
	}
	if _, _, err := svc.SubmitFragment(sessionID, 1, "locked"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	if err := svc.Skip(sessionID); !errors.Is(err, ErrCannotSkipCommitted) {
		t.Fatalf("expected cannot skip after committed, got %v", err)
	}
}

func TestFallbackAssignmentOnlyTakesQueuedRequest(t *testing.T) {
	svc := NewService()
	requester := svc.GuestSession("")
	human := svc.GuestSession("human")
	humanSessionID, _, err := svc.RegisterResponder(human.Token)
	if err != nil {
		t.Fatalf("register human responder: %v", err)
	}

	humanReq, err := svc.CreateRequest(context.Background(), requester.Token, "deeperseek-human", []Message{{Role: "user", Content: "human"}}, 0)
	if err != nil {
		t.Fatalf("create human request: %v", err)
	}
	if err := svc.MarkResponderAvailable(humanSessionID); err != nil {
		t.Fatalf("mark human available: %v", err)
	}
	if _, _, ok := svc.AcquireFallbackAssignment(humanReq.ID); ok {
		t.Fatal("fallback should not take a request already assigned to a human responder")
	}

	queuedReq, err := svc.CreateRequest(context.Background(), requester.Token, "deeperseek-human", []Message{{Role: "user", Content: "queued"}}, 0)
	if err != nil {
		t.Fatalf("create queued request: %v", err)
	}
	fallbackSessionID, assignment, ok := svc.AcquireFallbackAssignment(queuedReq.ID)
	if !ok {
		t.Fatal("fallback should take queued request")
	}
	if fallbackSessionID == "" || assignment.RequestID != queuedReq.ID {
		t.Fatalf("unexpected fallback assignment: session=%q assignment=%+v", fallbackSessionID, assignment)
	}
	if _, _, err := svc.SubmitFragment(fallbackSessionID, 1, "fallback"); err != nil {
		t.Fatalf("fallback submit fragment: %v", err)
	}
	if err := svc.Finish(fallbackSessionID); err != nil {
		t.Fatalf("fallback finish: %v", err)
	}
}

func TestAssignedTimeoutRequeuesRequestForFallback(t *testing.T) {
	svc := NewService()
	requester := svc.GuestSession("")
	responder := svc.GuestSession("silent responder")
	responderSessionID, _, err := svc.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}

	req, err := svc.CreateRequest(context.Background(), requester.Token, "deeperseek-human", []Message{{Role: "user", Content: "will stall"}}, 0)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := svc.MarkResponderAvailable(responderSessionID); err != nil {
		t.Fatalf("mark available: %v", err)
	}
	if _, _, ok := svc.AcquireFallbackAssignment(req.ID); ok {
		t.Fatal("fallback should not acquire a request while it is assigned")
	}

	changes := svc.SweepTimeouts(time.Now().UTC().Add(AssignedTimeout+time.Second), AssignedTimeout, StreamingInactivityTimeout)
	if len(changes) != 1 {
		t.Fatalf("expected one timeout change, got %v", changes)
	}
	fallbackSessionID, assignment, ok := svc.AcquireFallbackAssignment(req.ID)
	if !ok {
		t.Fatal("fallback should acquire request after assigned timeout requeue")
	}
	if fallbackSessionID == "" || assignment.RequestID != req.ID {
		t.Fatalf("unexpected fallback assignment: session=%q assignment=%+v", fallbackSessionID, assignment)
	}
}

func TestStreamingTimeoutCompletesPartial(t *testing.T) {
	svc, sessionID := assignedService(t)
	reqID := ""
	svc.mu.Lock()
	for _, value := range svc.activeByRes {
		reqID = value
	}
	svc.mu.Unlock()
	if reqID == "" {
		t.Fatal("expected active request")
	}
	events, unsubscribe, err := svc.Subscribe(reqID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	if _, _, err := svc.SubmitFragment(sessionID, 1, "partial"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	<-events

	changes := svc.SweepTimeouts(time.Now().UTC().Add(StreamingInactivityTimeout+time.Second), AssignedTimeout, StreamingInactivityTimeout)
	if len(changes) != 1 {
		t.Fatalf("expected one timeout change, got %v", changes)
	}
	event := <-events
	if event.Type != StreamEventDone || event.FinishReason != FinishPartial {
		t.Fatalf("unexpected done event: %+v", event)
	}
}

func TestLateSubscribeReplaysLongAnswerWithoutDeadlockingService(t *testing.T) {
	svc, sessionID := assignedService(t)
	requestID := svc.activeByRes[sessionID]

	for seq := int64(1); seq <= 80; seq++ {
		if _, _, err := svc.SubmitFragment(sessionID, seq, "x"); err != nil {
			t.Fatalf("submit fragment %d: %v", seq, err)
		}
	}
	if err := svc.Finish(sessionID); err != nil {
		t.Fatalf("finish: %v", err)
	}

	type subscription struct {
		events      <-chan StreamEvent
		unsubscribe func()
		err         error
	}
	result := make(chan subscription, 1)
	go func() {
		events, unsubscribe, err := svc.Subscribe(requestID)
		result <- subscription{events: events, unsubscribe: unsubscribe, err: err}
	}()

	var sub subscription
	select {
	case sub = <-result:
	case <-time.After(time.Second):
		t.Fatal("late subscription deadlocked while replaying more than 64 fragments")
	}
	if sub.err != nil {
		t.Fatalf("subscribe: %v", sub.err)
	}
	defer sub.unsubscribe()

	fragment := <-sub.events
	if fragment.Type != StreamEventFragment || len(fragment.Text) != 80 {
		t.Fatalf("unexpected replay event: type=%s len=%d", fragment.Type, len(fragment.Text))
	}
	done := <-sub.events
	if done.Type != StreamEventDone || done.FinishReason != FinishStop {
		t.Fatalf("unexpected done event: %+v", done)
	}

	guestCreated := make(chan struct{})
	go func() {
		svc.GuestSession("")
		close(guestCreated)
	}()
	select {
	case <-guestCreated:
	case <-time.After(time.Second):
		t.Fatal("service mutex remained locked after historical replay")
	}
}

func TestDisconnectBeforeAndAfterFragment(t *testing.T) {
	svc, sessionID := assignedService(t)
	requestID := svc.activeByRes[sessionID]
	svc.UnregisterResponder(sessionID)
	snap, _, _ := svc.RequestSnapshot(requestID)
	if snap.Status != StatusQueued {
		t.Fatalf("expected requeued before fragment, got %s", snap.Status)
	}

	responder, _ := svc.Register("carol", "Carol", "pass1234", "pass1234")
	sessionID, assignments, _ := svc.RegisterResponder(responder.Token)
	_ = svc.MarkResponderAvailable(sessionID)
	<-assignments
	if _, _, err := svc.SubmitFragment(sessionID, 1, "partial"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	svc.UnregisterResponder(sessionID)
	snap, text, _ := svc.RequestSnapshot(requestID)
	if snap.Status != StatusTimeoutCompleted || snap.FinishReason != FinishPartial || text != "partial" {
		t.Fatalf("expected partial completion, got status=%s reason=%s text=%q", snap.Status, snap.FinishReason, text)
	}
}

func TestReactionLedgerDeltas(t *testing.T) {
	svc := NewService()
	requester, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	responder, _ := svc.Register("bob", "Bob", "pass1234", "pass1234")
	sessionID, assignments, _ := svc.RegisterResponder(responder.Token)
	_ = svc.MarkResponderAvailable(sessionID)
	req, _ := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "q"}}, 0)
	<-assignments
	_, _, _ = svc.SubmitFragment(sessionID, 1, "a")
	if err := svc.Finish(sessionID); err != nil {
		t.Fatalf("finish: %v", err)
	}

	balance, err := svc.React(requester.Token, req.ID, ReactionLike)
	if err != nil {
		t.Fatalf("like: %v", err)
	}
	if balance.Total != 40 {
		t.Fatalf("responder should have signup 20 + liked reward 20, got %+v", balance)
	}
	balance, err = svc.React(requester.Token, req.ID, ReactionDislike)
	if err != nil {
		t.Fatalf("dislike: %v", err)
	}
	if balance.Total != 28 {
		t.Fatalf("responder should have signup 20 + disliked reward 8, got %+v", balance)
	}
}

func TestCancelBeforeFirstFragmentReleasesHold(t *testing.T) {
	svc := NewService()
	requester, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	req, err := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "q"}}, 0)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	me, _ := svc.Me(requester.Token)
	if me.Balance.Held != QuestionCost || me.Balance.Available != SignupGrant-QuestionCost {
		t.Fatalf("expected frozen points, got %+v", me.Balance)
	}
	if !svc.CancelBeforeFirstFragment(req.ID) {
		t.Fatal("expected cancellation to succeed")
	}
	me, _ = svc.Me(requester.Token)
	if me.Balance.Held != 0 || me.Balance.Available != SignupGrant {
		t.Fatalf("expected released hold, got %+v", me.Balance)
	}
	snap, _, _ := svc.RequestSnapshot(req.ID)
	if snap.Status != StatusAbandoned {
		t.Fatalf("expected abandoned, got %s", snap.Status)
	}
}

func TestResponderSourceTags(t *testing.T) {
	svc, sessionID := assignedService(t)
	reqID := svc.activeByRes[sessionID]
	snap, _, _ := svc.RequestSnapshot(reqID)
	if snap.RequesterKind != KindHuman || snap.ResponderKind != KindHuman || snap.ResponderDisplay != "Bob" {
		t.Fatalf("expected human tags, got reqKind=%q respKind=%q display=%q", snap.RequesterKind, snap.ResponderKind, snap.ResponderDisplay)
	}

	svc2 := NewService()
	requester := svc2.GuestSession("")
	req, _ := svc2.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "q"}}, 0)
	if _, _, ok := svc2.AcquireFallbackAssignment(req.ID); !ok {
		t.Fatal("fallback acquire")
	}
	snap2, _, _ := svc2.RequestSnapshot(req.ID)
	if snap2.ResponderKind != KindFallback {
		t.Fatalf("expected fallback kind, got %q", snap2.ResponderKind)
	}
}

func TestBoardListsGuestRequestsNotRegistered(t *testing.T) {
	svc := NewService()
	guest := svc.GuestSession("")
	reg, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	gReq, err := svc.CreateRequest(context.Background(), guest.Token, "m", []Message{{Role: "user", Content: "hi"}}, 0)
	if err != nil {
		t.Fatalf("guest create: %v", err)
	}
	if _, err := svc.CreateRequest(context.Background(), reg.Token, "m", []Message{{Role: "user", Content: "private question"}}, 0); err != nil {
		t.Fatalf("registered create: %v", err)
	}
	board, err := svc.Board(50)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	if len(board) != 1 || board[0].RequestID != gReq.ID {
		t.Fatalf("board should list only the guest (public) request, got %+v", board)
	}
	if board[0].Category != "闪电问答" {
		t.Fatalf("expected short-question category, got %q", board[0].Category)
	}
}

func TestConversationsLifecycleAndOwnership(t *testing.T) {
	svc := NewService()
	owner := svc.GuestSession("")
	other := svc.GuestSession("")

	conv, err := svc.CreateConversation(owner.Token, "我的对话")
	if err != nil || conv.Title != "我的对话" {
		t.Fatalf("create: %+v err=%v", conv, err)
	}
	if _, err := svc.AppendConversationMessage(owner.Token, conv.ID, "user", "hi", "", ""); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := svc.AppendConversationMessage(owner.Token, conv.ID, "assistant", "yo", KindHuman, "req1"); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	_, msgs, err := svc.GetConversation(owner.Token, conv.ID)
	if err != nil || len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Seq != 2 {
		t.Fatalf("get: msgs=%+v err=%v", msgs, err)
	}
	if list, _ := svc.ListConversations(owner.Token); len(list) != 1 {
		t.Fatalf("list should have 1, got %d", len(list))
	}
	// another session cannot see or touch it
	if _, _, err := svc.GetConversation(other.Token, conv.ID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-owner get should fail, got %v", err)
	}
	if err := svc.RenameConversation(other.Token, conv.ID, "劫持"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-owner rename should fail, got %v", err)
	}

	if err := svc.RenameConversation(owner.Token, conv.ID, "改名"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := svc.SetConversationArchived(owner.Token, conv.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := svc.DeleteConversation(owner.Token, conv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := svc.ListConversations(owner.Token); len(list) != 0 {
		t.Fatalf("list should be empty after delete, got %d", len(list))
	}
}

func TestPersonaSessionStampsSourceAndSkipsBoard(t *testing.T) {
	svc := NewService()

	// a persona requester's request is tagged ai_persona and never board-eligible
	persona := svc.PersonaSession("深思伪人-01")
	req, err := svc.CreateRequest(context.Background(), persona.Token, "m", []Message{{Role: "user", Content: "q"}}, 0)
	if err != nil {
		t.Fatalf("persona create: %v", err)
	}
	if req.RequesterKind != KindAIPersona || req.BoardEligible {
		t.Fatalf("persona request tagging: kind=%q board=%v", req.RequesterKind, req.BoardEligible)
	}
	if board, _ := svc.Board(50); len(board) != 0 {
		t.Fatalf("persona request must not appear on the board, got %d", len(board))
	}

	// a persona responder's answer is tagged ai_persona (fresh service so the
	// persona responder can't grab the queued persona-requester request above)
	svc2 := NewService()
	guest := svc2.GuestSession("")
	responder := svc2.PersonaSession("深思伪人-02")
	sid, assignments, _ := svc2.RegisterResponder(responder.Token)
	_ = svc2.MarkResponderAvailable(sid)
	gReq, _ := svc2.CreateRequest(context.Background(), guest.Token, "m", []Message{{Role: "user", Content: "hi"}}, 0)
	<-assignments
	snap, _, _ := svc2.RequestSnapshot(gReq.ID)
	if snap.ResponderKind != KindAIPersona {
		t.Fatalf("persona responder kind: %q", snap.ResponderKind)
	}
}

func TestOnlineHumanResponderCountExcludesPersonas(t *testing.T) {
	svc := NewService()
	human := svc.GuestSession("human")
	persona := svc.PersonaSession("persona")

	humanID, _, err := svc.RegisterResponder(human.Token)
	if err != nil {
		t.Fatalf("register human: %v", err)
	}
	defer svc.UnregisterResponder(humanID)
	personaID, _, err := svc.RegisterResponder(persona.Token)
	if err != nil {
		t.Fatalf("register persona: %v", err)
	}
	defer svc.UnregisterResponder(personaID)

	if got := svc.OnlineHumanResponderCount(); got != 1 {
		t.Fatalf("expected one online human responder, got %d", got)
	}
}

func TestInputAndOutputLimits(t *testing.T) {
	svc := NewService()
	requester, _ := svc.Register("alice", "Alice", "pass1234", "pass1234")
	tooLarge := make([]rune, InputLimitChars+1)
	for i := range tooLarge {
		tooLarge[i] = 'x'
	}
	if _, err := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: string(tooLarge)}}, 0); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected input too large, got %v", err)
	}

	svc, sessionID := assignedServiceWithOutputLimit(t, 3)
	if _, _, err := svc.SubmitFragment(sessionID, 1, "abcd"); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected output too large, got %v", err)
	}
}

func assignedService(t *testing.T) (*Service, string) {
	t.Helper()
	return assignedServiceWithOutputLimit(t, 0)
}

func assignedServiceWithOutputLimit(t *testing.T, outputLimit int) (*Service, string) {
	t.Helper()
	svc := NewService()
	requester, _ := svc.Register("alice"+newID(""), "Alice", "pass1234", "pass1234")
	responder, _ := svc.Register("bob"+newID(""), "Bob", "pass1234", "pass1234")
	sessionID, assignments, err := svc.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	if err := svc.MarkResponderAvailable(sessionID); err != nil {
		t.Fatalf("available: %v", err)
	}
	if _, err := svc.CreateRequest(context.Background(), requester.Token, "m", []Message{{Role: "user", Content: "q"}}, outputLimit); err != nil {
		t.Fatalf("create request: %v", err)
	}
	select {
	case <-assignments:
	case <-time.After(time.Second):
		t.Fatal("expected assignment")
	}
	return svc, sessionID
}
