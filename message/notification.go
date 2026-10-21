package message

// Notification is the V1 sender request. Empty type/priority mean info/normal.
type Notification struct {
	Context  *Context `json:"context,omitempty"`
	Type     string   `json:"type,omitempty"` // info (default) | alert
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Source   string   `json:"source,omitempty"`   // e.g. "Claude Code"
	Priority string   `json:"priority,omitempty"` // low | normal (default) | high
}
