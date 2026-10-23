package adapters

import (
	"encoding/json"
	"fmt"
	"meow.blue/meow/message"
	"strings"
)

type codexNotifyPayload struct {
	ThreadName           string  `json:"thread-name"`
	Type                 string  `json:"type"`
	ThreadID             string  `json:"thread-id"`
	TurnID               string  `json:"turn-id"`
	Cwd                  string  `json:"cwd"`
	LastAssistantMessage *string `json:"last-assistant-message"`
}

func decodeCodexNotify(data []byte) (Event, bool, error) {
	var p codexNotifyPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return Event{}, false, fmt.Errorf("invalid Codex notify: %w", err)
	}
	if p.Type != "agent-turn-complete" {
		return Event{}, false, nil
	}
	e := Event{Notification: message.Notification{Event: message.EventResult, Source: "Codex"}, Origin: Origin{Cwd: p.Cwd, SessionID: p.ThreadID, SessionName: p.ThreadName}, Template: ResultTitle, DedupeNamespace: "codex", EventID: p.TurnID}
	if p.LastAssistantMessage != nil {
		e.Notification.Body = *p.LastAssistantMessage
	}
	return e, true, nil
}

func decodeCodexHook(name string, data []byte) (Event, bool, error) {
	var p CodexHookInput
	if err := json.Unmarshal(data, &p); err != nil {
		return Event{}, false, fmt.Errorf("invalid Codex hook")
	}
	expected := "Stop"
	if name == "codex-permission" {
		expected = "PermissionRequest"
	}
	if p.Event != expected || p.SessionID == "" || p.TurnID == "" {
		return Event{}, false, fmt.Errorf("missing or mismatched Codex event identity")
	}
	if name == "codex-stop" && p.isTemporarySuggestion() {
		return Event{}, false, nil
	}
	e := Event{Notification: message.Notification{Event: message.EventResult, Source: "Codex", Body: p.LastAssistantMessage}, Origin: Origin{Cwd: p.Cwd, SessionID: p.SessionID, SessionName: p.SessionTitle}, Template: ResultTitle, DedupeNamespace: "codex", EventID: p.TurnID}
	if name == "codex-permission" {
		e.Template = AttentionTitle
		e.Notification.Event = message.EventAttention
		e.DedupeNamespace = ""
		e.Notification.Type = message.TypeAlert
		e.Notification.Priority = message.PriorityHigh
		e.Notification.Body = permissionReason(p.ToolInput)
	}
	return e, true, nil
}

type CodexHookInput struct {
	SessionTitle         string          `json:"session_title"`
	SessionID            string          `json:"session_id"`
	TurnID               string          `json:"turn_id"`
	TranscriptPath       json.RawMessage `json:"transcript_path"`
	Event                string          `json:"hook_event_name"`
	Cwd                  string          `json:"cwd"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
}

// Codex Desktop generates suggestions and checks their safety in ephemeral
// threads. Stop currently exposes no thread_source/turn_trigger, only a null
// transcript_path for these threads. Require BOTH explicit null and a recognized
// suggestion/safety envelope; ordinary chat JSON, missing metadata, prose and
// other ephemeral results must still notify. Do not infer persistence from file existence.
func (p CodexHookInput) isTemporarySuggestion() bool {
	if strings.TrimSpace(string(p.TranscriptPath)) != "null" {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(p.LastAssistantMessage), &object) != nil || len(object) != 1 {
		return false
	}
	if value, ok := object["suggestion"]; ok {
		var suggestion *string
		return json.Unmarshal(value, &suggestion) == nil
	}
	if value, ok := object["suggestions"]; ok && strings.HasPrefix(strings.TrimSpace(string(value)), "[") {
		var suggestions []map[string]json.RawMessage
		if json.Unmarshal(value, &suggestions) != nil {
			return false
		}
		for _, suggestion := range suggestions {
			var title string
			if json.Unmarshal(suggestion["title"], &title) != nil || strings.TrimSpace(title) == "" {
				return false
			}
		}
		return true
	}
	if value, ok := object["exclude"]; ok && strings.HasPrefix(strings.TrimSpace(string(value)), "[") {
		var exclusions []map[string]json.RawMessage
		if json.Unmarshal(value, &exclusions) != nil {
			return false
		}
		for _, exclusion := range exclusions {
			// Match the safety check's schema, not arbitrary exclusion lists.
			var id, reason string
			if len(exclusion) != 2 || json.Unmarshal(exclusion["id"], &id) != nil || strings.TrimSpace(id) == "" ||
				json.Unmarshal(exclusion["reason"], &reason) != nil || strings.TrimSpace(reason) == "" {
				return false
			}
		}
		return true
	}
	return false
}
