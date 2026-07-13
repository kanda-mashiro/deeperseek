package pgredis

import (
	"context"
	"errors"
	"testing"
	"time"

	"deeperseek/backend/internal/core"
)

func waitEvent(t *testing.T, ch <-chan core.StreamEvent, d time.Duration) core.StreamEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before an event arrived")
		}
		return ev
	case <-time.After(d):
		t.Fatal("timed out waiting for a stream event")
		return core.StreamEvent{}
	}
}

func waitAssignment(t *testing.T, ch <-chan core.AssignedRequest, d time.Duration) core.AssignedRequest {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(d):
		t.Fatal("timed out waiting for an assignment")
		return core.AssignedRequest{}
	}
}

// The SPEC-9 headline: requester connected to instance A, responder to instance
// B, fragments committed through B and streamed through A.
func TestCrossInstanceRequestAnswerFlow(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	requester := a.GuestSession("asker")
	responder := bInst.GuestSession("worker")
	sid, assignCh, err := bInst.RegisterResponder(responder.Token)
	if err != nil {
		t.Fatalf("register responder: %v", err)
	}
	if err := bInst.MarkResponderAvailable(sid); err != nil {
		t.Fatalf("available: %v", err)
	}

	req, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "why is the sky blue"}}, 0)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	events, unsub, err := a.Subscribe(req.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()

	if as := waitAssignment(t, assignCh, 5*time.Second); as.RequestID != req.ID {
		t.Fatalf("responder got wrong assignment: %s", as.RequestID)
	}

	if _, _, err := bInst.SubmitFragment(sid, 1, "because rayleigh scattering"); err != nil {
		t.Fatalf("submit fragment: %v", err)
	}
	if ev := waitEvent(t, events, 5*time.Second); ev.Type != core.StreamEventFragment || ev.Text != "because rayleigh scattering" {
		t.Fatalf("unexpected fragment event on instance A: %+v", ev)
	}

	if err := bInst.Finish(sid); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if ev := waitEvent(t, events, 5*time.Second); ev.Type != core.StreamEventDone || ev.FinishReason != core.FinishStop {
		t.Fatalf("unexpected done event on instance A: %+v", ev)
	}

	snap, text, err := a.RequestSnapshot(req.ID)
	if err != nil || snap.Status != core.StatusCompleted || text != "because rayleigh scattering" {
		t.Fatalf("snapshot after finish: status=%v text=%q err=%v", snap.Status, text, err)
	}
	if snap.ResponderKind != core.KindHuman || snap.ResponderDisplay != "worker" || snap.RequesterKind != core.KindHuman {
		t.Fatalf("source tags: respKind=%q display=%q reqKind=%q", snap.ResponderKind, snap.ResponderDisplay, snap.RequesterKind)
	}
}

func TestDistributedMatchingHonorsAIParticipationPreferences(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	personaRequester := a.PersonaSession("persona requester")
	humanRequester := a.GuestSession("human requester")
	aiReq, err := a.CreateRequest(ctx, personaRequester.Token, "m", []core.Message{{Role: "user", Content: "AI question"}}, 0)
	if err != nil {
		t.Fatalf("create persona request: %v", err)
	}
	humanReq, err := a.CreateRequest(ctx, humanRequester.Token, "m", []core.Message{{Role: "user", Content: "human question"}}, 0)
	if err != nil {
		t.Fatalf("create human request: %v", err)
	}

	humanOnly := bInst.GuestSession("human-only responder")
	humanOnlyID, humanOnlyAssignments, _ := bInst.RegisterResponder(humanOnly.Token)
	if err := bInst.MarkResponderAvailable(humanOnlyID, false); err != nil {
		t.Fatalf("mark human-only available: %v", err)
	}
	if assignment := waitAssignment(t, humanOnlyAssignments, 5*time.Second); assignment.RequestID != humanReq.ID || assignment.RequesterKind != core.KindHuman {
		t.Fatalf("expected compatible human request, got %+v", assignment)
	}

	aiCapable := bInst.GuestSession("AI-capable responder")
	aiCapableID, aiCapableAssignments, _ := bInst.RegisterResponder(aiCapable.Token)
	if err := bInst.MarkResponderAvailable(aiCapableID); err != nil {
		t.Fatalf("mark AI-capable available: %v", err)
	}
	if assignment := waitAssignment(t, aiCapableAssignments, 5*time.Second); assignment.RequestID != aiReq.ID || assignment.RequesterKind != core.KindAIPersona {
		t.Fatalf("expected preserved persona request, got %+v", assignment)
	}
}

func TestDistributedRequestCanRejectAIAnswers(t *testing.T) {
	a := backendForTest(t)
	ctx := context.Background()
	requester := a.GuestSession("requester")
	persona := a.PersonaSession("persona responder")
	personaID, personaAssignments, _ := a.RegisterResponder(persona.Token)
	_ = a.MarkResponderAvailable(personaID)

	req, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "human only"}}, 0, false)
	if err != nil {
		t.Fatalf("create human-only request: %v", err)
	}
	select {
	case assignment := <-personaAssignments:
		t.Fatalf("persona must not receive human-only request: %+v", assignment)
	case <-time.After(400 * time.Millisecond):
	}
	if _, _, ok := a.AcquireFallbackAssignment(req.ID); ok {
		t.Fatal("fallback must not acquire human-only request")
	}

	human := a.GuestSession("human responder")
	humanID, humanAssignments, _ := a.RegisterResponder(human.Token)
	_ = a.MarkResponderAvailable(humanID)
	if assignment := waitAssignment(t, humanAssignments, 5*time.Second); assignment.RequestID != req.ID {
		t.Fatalf("human responder got wrong request: %+v", assignment)
	}
}

func TestDistributedTargetedPersonaFollowUpOnlyReachesPreferredResponder(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	other := bInst.GuestSession("other responder")
	otherID, otherAssignments, _ := bInst.RegisterResponder(other.Token)
	if err := bInst.MarkResponderAvailable(otherID); err != nil {
		t.Fatalf("mark other available: %v", err)
	}

	preferred := bInst.GuestSession("preferred responder")
	preferredID, preferredAssignments, _ := bInst.RegisterResponder(preferred.Token)
	if err := bInst.MarkResponderAvailable(preferredID); err != nil {
		t.Fatalf("mark preferred available: %v", err)
	}

	persona := a.PersonaSession("persona requester")
	messages := []core.Message{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "第二问"},
	}
	req, err := a.CreateTargetedRequest(ctx, persona.Token, "m", messages, 0, preferredID)
	if err != nil {
		t.Fatalf("create targeted follow-up: %v", err)
	}
	assignment := waitAssignment(t, preferredAssignments, 5*time.Second)
	if assignment.RequestID != req.ID || len(assignment.Messages) != 3 {
		t.Fatalf("unexpected targeted assignment: %+v", assignment)
	}
	select {
	case leaked := <-otherAssignments:
		t.Fatalf("targeted follow-up leaked to another responder: %+v", leaked)
	case <-time.After(400 * time.Millisecond):
	}
	if err := bInst.Skip(preferredID); err != nil {
		t.Fatalf("skip targeted follow-up: %v", err)
	}
	snap, _, err := a.RequestSnapshot(req.ID)
	if err != nil || snap.Status != core.StatusAbandoned {
		t.Fatalf("targeted skip should abandon request: status=%v err=%v", snap.Status, err)
	}
}

func TestFreezeChargeAndInsufficientPoints(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	requester, err := a.Register("alice", "Alice", "pass1234", "pass1234")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// with no responder, the request stays queued and holds 5 points
	req, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	me, _ := a.Me(requester.Token)
	if me.Balance.Held != 5 || me.Balance.Available != 15 {
		t.Fatalf("expected 5 held / 15 available, got %+v", me.Balance)
	}

	// a responder answers -> the freeze finalizes into a spend
	responder := bInst.GuestSession("worker")
	sid, assignCh, _ := bInst.RegisterResponder(responder.Token)
	_ = bInst.MarkResponderAvailable(sid)
	if as := waitAssignment(t, assignCh, 5*time.Second); as.RequestID != req.ID {
		t.Fatalf("wrong assignment: %s", as.RequestID)
	}
	if _, _, err := bInst.SubmitFragment(sid, 1, "answer"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	me, _ = a.Me(requester.Token)
	if me.Balance.Total != 15 || me.Balance.Held != 0 || me.Balance.Available != 15 {
		t.Fatalf("expected charged 5 (15/0/15), got %+v", me.Balance)
	}

	// drain the remaining balance into held and confirm the guard trips
	for i := 0; i < 3; i++ {
		if _, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0); !errors.Is(err, core.ErrInsufficientPoints) {
		t.Fatalf("expected ErrInsufficientPoints once available < 5, got %v", err)
	}
}

func TestReactionRewardDeltas(t *testing.T) {
	a := backendForTest(t)
	ctx := context.Background()

	requester, _ := a.Register("alice", "Alice", "pass1234", "pass1234")
	responderUser, _ := a.Register("bob", "Bob", "pass1234", "pass1234")
	sid, assignCh, _ := a.RegisterResponder(responderUser.Token)
	_ = a.MarkResponderAvailable(sid)

	req, err := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitAssignment(t, assignCh, 5*time.Second)
	if _, _, err := a.SubmitFragment(sid, 1, "answer"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := a.Finish(sid); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// base reward already paid: bob has signup 20 + reward 10
	_, balance, _ := a.LedgerForUser(responderUser.Token)
	if balance.Total != 30 {
		t.Fatalf("expected 30 after base reward, got %+v", balance)
	}

	liked, err := a.React(requester.Token, req.ID, core.ReactionLike)
	if err != nil {
		t.Fatalf("like: %v", err)
	}
	if liked.Total != 40 {
		t.Fatalf("expected 40 after like, got %+v", liked)
	}
	disliked, err := a.React(requester.Token, req.ID, core.ReactionDislike)
	if err != nil {
		t.Fatalf("dislike: %v", err)
	}
	if disliked.Total != 28 {
		t.Fatalf("expected 28 after switching to dislike, got %+v", disliked)
	}
}

// Subscribing to an already-streaming request must replay the committed prefix,
// deliver the rest, and finish with a complete (non-truncated) answer — the
// CRITICAL streaming-reconciliation fix.
func TestMidStreamSubscribeGetsFullAnswer(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	requester := a.GuestSession("asker")
	responder := bInst.GuestSession("worker")
	sid, assignCh, _ := bInst.RegisterResponder(responder.Token)
	_ = bInst.MarkResponderAvailable(sid)
	req, _ := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0)
	waitAssignment(t, assignCh, 5*time.Second)

	// two fragments committed BEFORE anyone subscribes
	if _, _, err := bInst.SubmitFragment(sid, 1, "hello "); err != nil {
		t.Fatalf("frag1: %v", err)
	}
	if _, _, err := bInst.SubmitFragment(sid, 2, "world"); err != nil {
		t.Fatalf("frag2: %v", err)
	}

	events, unsub, err := a.Subscribe(req.ID) // subscribe mid-stream on instance A
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()

	if _, _, err := bInst.SubmitFragment(sid, 3, "!"); err != nil {
		t.Fatalf("frag3: %v", err)
	}
	if err := bInst.Finish(sid); err != nil {
		t.Fatalf("finish: %v", err)
	}

	var got string
	for {
		ev := waitEvent(t, events, 6*time.Second)
		if ev.Type == core.StreamEventDone {
			break
		}
		got += ev.Text
	}
	if got != "hello world!" {
		t.Fatalf("mid-stream subscribe must yield the full answer, got %q", got)
	}
}

// Responder disconnect requeues before a fragment and partial-completes after —
// SPEC 4.5, verifying the fragment count is read inside the transaction.
func TestResponderDisconnectLifecycle(t *testing.T) {
	a := backendForTest(t)
	ctx := context.Background()

	requester := a.GuestSession("asker")
	responder := a.GuestSession("worker")
	sid, assignCh, _ := a.RegisterResponder(responder.Token)
	_ = a.MarkResponderAvailable(sid)
	req, _ := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0)
	waitAssignment(t, assignCh, 5*time.Second)

	a.UnregisterResponder(sid)
	snap, _, _ := a.RequestSnapshot(req.ID)
	if snap.Status != core.StatusQueued || snap.ResponderSessionID != "" || snap.ResponderKind != "" {
		t.Fatalf("disconnect before fragment should requeue + clear responder: %+v", snap)
	}

	responder2 := a.GuestSession("worker2")
	sid2, assignCh2, _ := a.RegisterResponder(responder2.Token)
	_ = a.MarkResponderAvailable(sid2)
	waitAssignment(t, assignCh2, 5*time.Second) // requeued request reassigned
	if _, _, err := a.SubmitFragment(sid2, 1, "partial"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	a.UnregisterResponder(sid2)
	snap2, text, _ := a.RequestSnapshot(req.ID)
	if snap2.Status != core.StatusTimeoutCompleted || snap2.FinishReason != core.FinishPartial || text != "partial" {
		t.Fatalf("disconnect after fragment should partial-complete: status=%s reason=%s text=%q", snap2.Status, snap2.FinishReason, text)
	}
}

func TestFragmentIdempotency(t *testing.T) {
	a := backendForTest(t)
	bInst := secondBackend(t)
	ctx := context.Background()

	requester := a.GuestSession("asker")
	responder := bInst.GuestSession("worker")
	sid, assignCh, _ := bInst.RegisterResponder(responder.Token)
	_ = bInst.MarkResponderAvailable(sid)
	req, _ := a.CreateRequest(ctx, requester.Token, "m", []core.Message{{Role: "user", Content: "q"}}, 0)
	waitAssignment(t, assignCh, 5*time.Second)

	first, dup, err := bInst.SubmitFragment(sid, 1, "hello")
	if err != nil || dup {
		t.Fatalf("first submit: dup=%v err=%v", dup, err)
	}
	again, dup, err := bInst.SubmitFragment(sid, 1, "hello")
	if err != nil || !dup || again.ID != first.ID {
		t.Fatalf("duplicate submit should be idempotent: dup=%v id=%s/%s err=%v", dup, again.ID, first.ID, err)
	}
	_, text, _ := a.RequestSnapshot(req.ID)
	if text != "hello" {
		t.Fatalf("duplicate must not append twice, got %q", text)
	}
}
