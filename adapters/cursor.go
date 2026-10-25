package adapters

import (
	"encoding/json"
	"fmt"
	"meow.blue/meow/message"
)

// Cursor stop hooks report how the agent loop ended. They do not include the
// assistant text; that text arrives only on afterAgentResponse, which also
// fires for intermediate messages. Permission gates are not observation hooks:
// an empty stdout blocks the action, so Meow does not subscribe to them.
type cursorStopInput struct {
	ConversationID string   `json:"conversation_id"`
	GenerationID   string   `json:"generation_id"`
	HookEventName  string   `json:"hook_event_name"`
	HookEvent      string   `json:"hookEventName"`
	WorkspaceRoots []string `json:"workspace_roots"`
	TranscriptPath *string  `json:"transcript_path"`
	Status         string   `json:"status"`
}

func decodeCursorStop(data []byte) (Event, bool, error) {
	var in cursorStopInput
	if err := json.Unmarshal(data, &in); err != nil {
		return Event{}, false, fmt.Errorf("invalid Cursor event: %w", err)
	}
	// Grok executes ~/.cursor/hooks.json with its own envelope. Ignoring that
	// shape lets the grokbot adapter own it, so one turn is not notified twice
	// and a Grok payload is not logged as a broken Cursor event.
	if cursorPayloadIsGrok(in) {
		return Event{}, false, nil
	}
	if in.HookEventName != "" && in.HookEventName != "stop" {
		return Event{}, false, fmt.Errorf("missing or mismatched Cursor event identity")
	}
	if in.ConversationID == "" || in.Status == "" {
		return Event{}, false, fmt.Errorf("invalid Cursor event: missing event fields")
	}
	switch in.Status {
	case "completed", "error":
	case "aborted":
		return Event{}, false, nil
	default:
		// A status Cursor adds later is not a malformed event.
		return Event{}, false, nil
	}
	cwd := ""
	for _, root := range in.WorkspaceRoots {
		if root != "" {
			cwd = root
			break
		}
	}
	transcript := ""
	if in.TranscriptPath != nil {
		transcript = *in.TranscriptPath
	}
	e := Event{
		Notification:    message.Notification{Source: "Cursor"},
		Origin:          Origin{Cwd: cwd, SessionID: in.ConversationID, TranscriptPath: transcript},
		EventID:         in.GenerationID,
		Template:        ResultTitle,
		DedupeNamespace: "cursor-stop",
	}
	e.Notification.Event = message.EventResult
	if in.Status == "error" {
		e.Template = FailureTitle
		e.DedupeNamespace = "cursor-failure"
		e.Notification.Event = message.EventFailure
		e.Notification.Type = message.TypeAlert
		e.Notification.Priority = message.PriorityHigh
	}
	return e, true, nil
}

func cursorPayloadIsGrok(in cursorStopInput) bool {
	if in.Status != "" || in.ConversationID != "" {
		return false
	}
	switch in.HookEventName {
	case "Stop", "StopFailure", "StopCancelled", "Notification":
		return true
	}
	switch in.HookEvent {
	case "stop", "stop_failure", "stop_cancelled", "notification":
		return true
	}
	return false
}
