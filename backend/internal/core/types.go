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
	Messages           []Message
	Model              string
	Status             RequestStatus
	ResponderSessionID string
	ResponderUserID    string
	ResponderGuest     bool
	FrozenPoints       int
	QuestionCharged    bool
	OutputLimit        int
	FinishReason       FinishReason
	Reaction           Reaction
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        time.Time
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
