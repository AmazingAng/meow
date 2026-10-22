package message

// EventKind describes why a sender emitted a notification, independently of urgency.
type EventKind string

const (
	EventResult    EventKind = "result"
	EventAttention EventKind = "attention"
	EventFailure   EventKind = "failure"
)

// Notification is the V1 sender request. Empty type/priority mean info/normal.
type Notification struct {
	Event    EventKind `json:"event,omitempty"`
	Context  *Context  `json:"context,omitempty"`
	Type     string    `json:"type,omitempty"` // info (default) | alert
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	Source   string    `json:"source,omitempty"`   // e.g. "Claude Code"
	Priority string    `json:"priority,omitempty"` // low | normal (default) | high
}
