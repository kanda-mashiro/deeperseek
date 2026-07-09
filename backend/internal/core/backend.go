package core

import (
	"context"
	"time"
)

// Backend is the seam between the HTTP/WS/SSE layer and the state engine. The
// in-memory Service (this package's default) implements it, and a Postgres+Redis
// implementation is selected at boot when DATABASE_URL and REDIS_URL are set.
//
// Methods are transaction-shaped: each mirrors an operation that is atomic today
// under Service's single mutex, so a distributed implementation can honor the
// same invariant in one PG transaction plus its realtime side effect. The
// channel-returning methods (RegisterResponder, Subscribe) keep their signatures
// across implementations; a distributed backend feeds those channels from Redis
// subscriptions instead of in-process fan-out.
type Backend interface {
	Register(accountName, nickname, password, repeated string) (AuthResult, error)
	Login(accountName, password string) (AuthResult, error)
	GuestSession(nickname string) AuthResult
	PersonaSession(nickname string) AuthResult
	Me(token string) (AuthResult, error)

	CreateRequest(ctx context.Context, token, model string, messages []Message, maxOutputChars int) (*Request, error)
	Subscribe(requestID string) (<-chan StreamEvent, func(), error)
	RequestSnapshot(requestID string) (*Request, string, error)
	CancelBeforeFirstFragment(requestID string) bool

	RegisterResponder(token string) (string, <-chan AssignedRequest, error)
	UnregisterResponder(sessionID string)
	MarkResponderAvailable(sessionID string) error
	SubmitFragment(sessionID string, clientSeq int64, text string) (Fragment, bool, error)
	Finish(sessionID string) error
	Skip(sessionID string) error

	AcquireFallbackAssignment(requestID string) (string, AssignedRequest, bool)
	FallbackStillWanted(requestID string) bool

	RunTimeoutSweeper(ctx context.Context, interval time.Duration)
	SweepTimeouts(now time.Time, assignedTimeout, streamingTimeout time.Duration) []string

	React(token, requestID string, reaction Reaction) (Balance, error)
	LedgerForUser(token string) ([]PointEntry, Balance, error)

	Board(limit int) ([]BoardTicket, error)

	// persona subsystem primitives
	OnlineResponderCount() int
	QueuedRequestCount() int
	TryPersonaLeader(podID string, ttl time.Duration) bool

	CreateConversation(token, title string) (Conversation, error)
	ListConversations(token string) ([]Conversation, error)
	GetConversation(token, id string) (Conversation, []ConversationMessage, error)
	RenameConversation(token, id, title string) error
	SetConversationArchived(token, id string, archived bool) error
	DeleteConversation(token, id string) error
	AppendConversationMessage(token, conversationID, role, content, sourceKind, requestID string) (ConversationMessage, error)
}

var _ Backend = (*Service)(nil)
