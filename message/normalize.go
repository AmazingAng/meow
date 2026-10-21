package message

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	TypeInfo       = "info"
	TypeAlert      = "alert"
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	MaxTitle       = 256
	MaxBody        = 4000
	MaxSource      = 64
	MaxContextName = 200
	MaxContextID   = 128
	MaxJSONBytes   = 64 << 10
)

// ValidationError identifies a protocol field without retaining its private value.
type ValidationError struct{ Field, Reason string }

func (e *ValidationError) Error() string { return e.Field + " " + e.Reason }

func ValidateText(field, value string, required bool, limit int) error {
	switch {
	case !utf8.ValidString(value) || strings.ContainsRune(value, 0):
		return &ValidationError{field, "must be valid UTF-8 text"}
	case required && value == "":
		return &ValidationError{field, "is required"}
	case utf8.RuneCountInString(value) > limit:
		return &ValidationError{field, fmt.Sprintf("must be at most %d characters", limit)}
	}
	return nil
}

// Normalize returns an independent value, with the same defaults and trimming
// as the V1 API. Caller-owned context is never edited, even on validation failure.
func (n Notification) Normalize() (Notification, error) {
	if n.Context != nil {
		c := *n.Context
		n.Context = &c
	}
	n.Title, n.Body, n.Source = strings.TrimSpace(n.Title), strings.TrimSpace(n.Body), strings.TrimSpace(n.Source)
	if n.Type == "" {
		n.Type = TypeInfo
	}
	if n.Priority == "" {
		n.Priority = PriorityNormal
	}
	if n.Type != TypeInfo && n.Type != TypeAlert {
		return n, &ValidationError{"type", "must be one of info, alert"}
	}
	if n.Priority != PriorityLow && n.Priority != PriorityNormal && n.Priority != PriorityHigh {
		return n, &ValidationError{"priority", "must be one of low, normal, high"}
	}
	for _, f := range []struct {
		name, value string
		required    bool
		max         int
	}{
		{"title", n.Title, true, MaxTitle}, {"body", n.Body, false, MaxBody}, {"source", n.Source, false, MaxSource},
	} {
		if err := ValidateText(f.name, f.value, f.required, f.max); err != nil {
			return n, err
		}
	}
	if n.Context != nil {
		c := n.Context
		for _, f := range []struct {
			name  string
			value *string
			max   int
		}{
			{"context.workspace", &c.Workspace, MaxContextName}, {"context.workspace_id", &c.WorkspaceID, MaxContextID},
			{"context.session", &c.Session, MaxContextName}, {"context.session_id", &c.SessionID, MaxContextID},
		} {
			*f.value = strings.TrimSpace(*f.value)
			if err := ValidateText(f.name, *f.value, false, f.max); err != nil {
				return n, err
			}
			if strings.ContainsFunc(*f.value, unicode.IsControl) {
				return n, &ValidationError{f.name, "must be a single line without control characters"}
			}
		}
	}
	return n, nil
}

// CanonicalJSON preserves the established creation-idempotency representation.
// Senders may omit optional fields on the wire; hashing must include their
// normalized values. Context stays omitted when absent, including old retries.
func (n Notification) CanonicalJSON() json.RawMessage {
	b, _ := json.Marshal(struct {
		Context  *Context `json:"context,omitempty"`
		Type     string   `json:"type"`
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Source   string   `json:"source"`
		Priority string   `json:"priority"`
	}{n.Context, n.Type, n.Title, n.Body, n.Source, n.Priority}) // closed string-only model
	return b
}

// Decode accepts one JSON object and applies V1 validation. Unknown fields are
// ignored for forward compatibility, matching the HTTP API.
func Decode(data []byte) (Notification, error) {
	if len(data) > MaxJSONBytes {
		return Notification{}, fmt.Errorf("notification JSON exceeds 64 KB")
	}
	var n *Notification
	if err := json.Unmarshal(data, &n); err != nil {
		return Notification{}, err
	}
	if n == nil {
		return Notification{}, fmt.Errorf("notification must be a JSON object")
	}
	return n.Normalize()
}

// CleanLabel is only for optional host-derived metadata. User-provided wire
// fields use Normalize, which reports invalid input instead of silently fixing it.
func CleanLabel(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	return Truncate(strings.TrimSpace(s), limit)
}

// ValidateIdempotencyKey is shared by CLI and HTTP transport. Empty means the
// caller has not supplied a key; the server and CLI retain their existing policy.
func ValidateIdempotencyKey(key string) error {
	valid := len(key) <= 128
	for i := range len(key) {
		if key[i] < 0x21 || key[i] > 0x7e {
			valid = false
		}
	}
	if !valid {
		return &ValidationError{"Idempotency-Key", "must be at most 128 printable ASCII characters"}
	}
	return nil
}
