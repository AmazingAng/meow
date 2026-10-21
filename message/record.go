package message

import "time"

// Record is the stored notification returned by inbox and detail endpoints.
type Record struct {
	Context     *Context       `json:"context,omitempty"`
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Source      string         `json:"source"`
	Priority    string         `json:"priority"`
	PushStatus  string         `json:"push_status"`
	PushError   string         `json:"push_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	DeliveredAt *time.Time     `json:"delivered_at"`
	ReadAt      *time.Time     `json:"read_at"`
	Question    *QuestionState `json:"question,omitempty"`
}

// QuestionState is the legacy read-only question summary. An alias preserves
// source compatibility with callers using an anonymous struct.
type QuestionState = struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Choices   []string  `json:"choices"`
	Answer    *string   `json:"answer"`
	ExpiresAt time.Time `json:"expires_at"`
}
