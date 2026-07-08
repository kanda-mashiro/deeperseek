package pgredis

import (
	"errors"
	"testing"

	"deeperseek/backend/internal/core"
)

func TestConversationsPersistWithOwnershipIsolation(t *testing.T) {
	b := backendForTest(t)
	owner := b.GuestSession("")
	other := b.GuestSession("")

	conv, err := b.CreateConversation(owner.Token, "记录")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := b.AppendConversationMessage(owner.Token, conv.ID, "user", "hi", "", ""); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := b.AppendConversationMessage(owner.Token, conv.ID, "assistant", "yo", core.KindHuman, "req1"); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	_, msgs, err := b.GetConversation(owner.Token, conv.ID)
	if err != nil || len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Seq != 2 {
		t.Fatalf("get: msgs=%+v err=%v", msgs, err)
	}
	if list, _ := b.ListConversations(owner.Token); len(list) != 1 {
		t.Fatalf("owner list should have 1, got %d", len(list))
	}
	// isolation: another guest session sees nothing and cannot access it
	if _, _, err := b.GetConversation(other.Token, conv.ID); !errors.Is(err, core.ErrConversationNotFound) {
		t.Fatalf("cross-owner get should fail, got %v", err)
	}
	if list, _ := b.ListConversations(other.Token); len(list) != 0 {
		t.Fatalf("other session should see no conversations, got %d", len(list))
	}

	if err := b.DeleteConversation(owner.Token, conv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// messages cascade-deleted; conversation gone
	if _, _, err := b.GetConversation(owner.Token, conv.ID); !errors.Is(err, core.ErrConversationNotFound) {
		t.Fatalf("get after delete should fail, got %v", err)
	}
}
