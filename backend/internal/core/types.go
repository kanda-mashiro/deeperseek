package core

import "time"

const (
	SignupGrant      = 20
	QuestionCost     = 5
	BaseAnswerReward = 10
	InputLimitChars  = 100000
	OutputLimitChars = 128000
)

const (
	AssignedTimeout            = 30 * time.Second
	StreamingInactivityTimeout = 60 * time.Second
)

type RequestStatus string

const (
	StatusCreated          RequestStatus = "created"
	StatusQueued           RequestStatus = "queued"
	StatusAssigned         RequestStatus = "assigned"
	StatusTyping           RequestStatus = "typing"
	StatusStreaming        RequestStatus = "streaming"
	StatusCompleted        RequestStatus = "completed"
	StatusTimeoutCompleted RequestStatus = "timeout_completed"
	StatusRequeued         RequestStatus = "requeued"
	StatusAbandoned        RequestStatus = "abandoned"
)

type Reaction string

const (
	ReactionNone    Reaction = "none"
	ReactionLike    Reaction = "like"
	ReactionDislike Reaction = "dislike"
)

// Kind labels who is behind a request or an answer, so the frontend can show it
// honestly: a real human, an AI persona posing as a human, or the fallback AI.
const (
	KindHuman     = "human"
	KindAIPersona = "ai_persona"
	KindFallback  = "fallback"
)

type FinishReason string

const (
	FinishStop    FinishReason = "stop"
	FinishLength  FinishReason = "length"
	FinishPartial FinishReason = "partial"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type User struct {
	ID           string
	AccountName  string
	Nickname     string
	PasswordHash []byte
	CreatedAt    time.Time
}

type Session struct {
	ID        string
	Token     string
	UserID    string
	Guest     bool
	Nickname  string
	CreatedAt time.Time
}

type Request struct {
	ID                 string
	RequesterID        string
	RequesterSessionID string
	RequesterGuest     bool
	RequesterKind      string
	Messages           []Message
	Model              string
	Status             RequestStatus
	ResponderSessionID string
	ResponderUserID    string
	ResponderGuest     bool
	ResponderKind      string
	ResponderDisplay   string
	BoardEligible      bool
	FrozenPoints       int
	QuestionCharged    bool
	OutputLimit        int
	FinishReason       FinishReason
	Reaction           Reaction
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        time.Time
}

type Conversation struct {
	ID             string    `json:"id"`
	OwnerUserID    string    `json:"-"`
	GuestSessionID string    `json:"-"`
	Title          string    `json:"title"`
	Archived       bool      `json:"archived"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ConversationMessage struct {
	ID         string    `json:"id"`
	Seq        int       `json:"seq"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	SourceKind string    `json:"source_kind,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Fragment struct {
	ID                 string
	RequestID          string
	ResponderSessionID string
	ClientSeq          int64
	Text               string
	CreatedAt          time.Time
}

type PointEntry struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	RequestID string    `json:"request_id,omitempty"`
	Kind      string    `json:"kind"`
	Delta     int       `json:"delta"`
	CreatedAt time.Time `json:"created_at"`
}

type Balance struct {
	Total     int `json:"total"`
	Held      int `json:"held"`
	Available int `json:"available"`
}

type StreamEventType string

const (
	StreamEventFragment StreamEventType = "fragment"
	StreamEventDone     StreamEventType = "done"
)

type StreamEvent struct {
	Type         StreamEventType
	RequestID    string
	Text         string
	FinishReason FinishReason
}

type AssignedRequest struct {
	RequestID string    `json:"request_id"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
}

// BoardTicket is the spectator-safe projection of a request: no account, token,
// session id, or raw question — only a structural category, who is answering,
// and how it is going.
type BoardTicket struct {
	RequestID        string        `json:"request_id"`
	Category         string        `json:"category"`
	Status           RequestStatus `json:"status"`
	ResponderKind    string        `json:"responder_kind"`
	ResponderDisplay string        `json:"responder_display"`
	Reaction         Reaction      `json:"reaction"`
	AnswerLength     int           `json:"answer_length"`
	CreatedAt        time.Time     `json:"created_at"`
}
