package core

import (
	"sort"
	"strings"
	"time"
)

func NormalizeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "新对话"
	}
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:80])
	}
	return title
}

func (s *Service) ownsConversationLocked(sess *Session, c *Conversation) bool {
	if c == nil {
		return false
	}
	if sess.Guest {
		return c.GuestSessionID == sess.ID
	}
	return c.OwnerUserID != "" && c.OwnerUserID == sess.UserID
}

func (s *Service) CreateConversation(token, title string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return Conversation{}, ErrUnauthorized
	}
	now := time.Now().UTC()
	c := &Conversation{ID: newID("cnv"), Title: NormalizeTitle(title), CreatedAt: now, UpdatedAt: now}
	if sess.Guest {
		c.GuestSessionID = sess.ID
	} else {
		c.OwnerUserID = sess.UserID
	}
	s.conversations[c.ID] = c
	return *c, nil
}

func (s *Service) ListConversations(token string) ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return nil, ErrUnauthorized
	}
	var out []Conversation
	for _, c := range s.conversations {
		if s.ownsConversationLocked(sess, c) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Service) GetConversation(token, id string) (Conversation, []ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return Conversation{}, nil, ErrUnauthorized
	}
	c := s.conversations[id]
	if !s.ownsConversationLocked(sess, c) {
		return Conversation{}, nil, ErrConversationNotFound
	}
	return *c, append([]ConversationMessage(nil), s.convMessages[id]...), nil
}

func (s *Service) RenameConversation(token, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return ErrUnauthorized
	}
	c := s.conversations[id]
	if !s.ownsConversationLocked(sess, c) {
		return ErrConversationNotFound
	}
	c.Title = NormalizeTitle(title)
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *Service) SetConversationArchived(token, id string, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return ErrUnauthorized
	}
	c := s.conversations[id]
	if !s.ownsConversationLocked(sess, c) {
		return ErrConversationNotFound
	}
	c.Archived = archived
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *Service) DeleteConversation(token, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return ErrUnauthorized
	}
	c := s.conversations[id]
	if !s.ownsConversationLocked(sess, c) {
		return ErrConversationNotFound
	}
	delete(s.conversations, id)
	delete(s.convMessages, id)
	return nil
}

func (s *Service) AppendConversationMessage(token, conversationID, role, content, sourceKind, requestID string) (ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessionsByTok[token]
	if !ok {
		return ConversationMessage{}, ErrUnauthorized
	}
	c := s.conversations[conversationID]
	if !s.ownsConversationLocked(sess, c) {
		return ConversationMessage{}, ErrConversationNotFound
	}
	now := time.Now().UTC()
	msg := ConversationMessage{
		ID:         newID("msg"),
		Seq:        len(s.convMessages[conversationID]) + 1,
		Role:       role,
		Content:    content,
		SourceKind: sourceKind,
		RequestID:  requestID,
		CreatedAt:  now,
	}
	s.convMessages[conversationID] = append(s.convMessages[conversationID], msg)
	c.UpdatedAt = now
	return msg, nil
}
