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
