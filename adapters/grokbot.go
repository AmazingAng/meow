package adapters

import (
	"encoding/json"
	"fmt"
	"meow.blue/meow/message"
	"strings"
)

// Grok's stdin envelope is camelCase, with Claude's snake_case aliases beside it.
// hook_event_name carries the PascalCase Claude name; hookEventName carries grok's.
type grokbotInput struct {
	HookEventName    string `json:"hook_event_name"`
	HookEvent        string `json:"hookEventName"`
	SessionID        string `json:"sessionId"`
	SessionIDSnake   string `json:"session_id"`
	PromptID         string `json:"promptId"`
	PromptIDSnake    string `json:"prompt_id"`
	Cwd              string `json:"cwd"`
	WorkspaceRoot    string `json:"workspaceRoot"`
	WorkspaceSnake   string `json:"workspace_root"`
	LastMessage      string `json:"lastAssistantMessage"`
	LastMessageSnake string `json:"last_assistant_message"`
	Reason           string `json:"reason"`
	Error            string `json:"error"`
	ErrorDetails     string `json:"errorDetails"`
	ErrorSnake       string `json:"error_details"`
	SubagentType     string `json:"subagentType"`
	SubagentSnake    string `json:"subagent_type"`
	NotifyType       string `json:"notificationType"`
	NotifySnake      string `json:"notification_type"`
	Message          string `json:"message"`
	Title            string `json:"title"`
}

func decodeGrokbot(name string, data []byte) (Event, bool, error) {
	var in grokbotInput
	if err := json.Unmarshal(data, &in); err != nil {
		return Event{}, false, fmt.Errorf("invalid Grok event: %w", err)
	}
	event := grokEventName(in)
	sessionID := firstText(in.SessionID, in.SessionIDSnake)
	switch name {
	case "grokbot-stop":
		if event != "Stop" || sessionID == "" {
			return Event{}, false, fmt.Errorf("missing or mismatched Grok event identity")
		}
		// Session teardown and subagent turns are not a new user-visible result.
		// A blank reply is not a result either; check it before --no-summary.
		if !grokTurnEnded(in.Reason) || firstText(in.SubagentType, in.SubagentSnake) != "" || firstText(in.LastMessage, in.LastMessageSnake) == "" {
			return Event{}, false, nil
		}
		return grokEvent(in, sessionID, ResultTitle, message.EventResult, "grokbot-stop", firstText(in.LastMessage, in.LastMessageSnake)), true, nil
	case "grokbot-failure":
		if event != "StopFailure" || sessionID == "" {
			return Event{}, false, fmt.Errorf("missing or mismatched Grok event identity")
		}
		e := grokEvent(in, sessionID, FailureTitle, message.EventFailure, "grokbot-failure", strings.TrimSpace(firstText(in.Error)+"\n"+firstText(in.ErrorDetails, in.ErrorSnake)))
		return e, true, nil
	case "grokbot-notify":
		if event != "Notification" {
			return Event{}, false, fmt.Errorf("missing or mismatched Grok event identity")
		}
		switch firstText(in.NotifyType, in.NotifySnake) {
		case "permission_prompt", "elicitation_dialog":
		default:
			return Event{}, false, nil
		}
		if sessionID == "" && strings.TrimSpace(in.Message) == "" {
			return Event{}, false, fmt.Errorf("invalid Grok event: missing event fields")
		}
		e := grokEvent(in, sessionID, AttentionTitle, message.EventAttention, "", in.Message)
		e.Notification.Title = strings.TrimSpace(in.Title)
		e.DedupeNamespace = ""
		return e, true, nil
	default:
		return Event{}, false, fmt.Errorf("unknown adapter %q", name)
	}
}

func grokEvent(in grokbotInput, sessionID, template string, kind message.EventKind, namespace, body string) Event {
	cwd := firstText(in.Cwd, in.WorkspaceRoot, in.WorkspaceSnake)
	e := Event{
		Notification:    message.Notification{Event: kind, Source: "Grok", Body: body},
		Origin:          Origin{Cwd: cwd, SessionID: sessionID},
		Template:        template,
		DedupeNamespace: namespace,
		EventID:         firstText(in.PromptID, in.PromptIDSnake),
	}
	if template != ResultTitle {
		e.Notification.Type = message.TypeAlert
		e.Notification.Priority = message.PriorityHigh
	}
	return e
}

func grokEventName(in grokbotInput) string {
	switch in.HookEventName {
	case "Stop", "StopFailure", "Notification":
		return in.HookEventName
	}
	switch in.HookEvent {
	case "stop":
		return "Stop"
	case "stop_failure":
		return "StopFailure"
	case "notification":
		return "Notification"
	}
	return ""
}

// A Stop hook also fires when the session closes. Only a genuine turn end is a result.
func grokTurnEnded(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "end_turn":
		return true
	default:
		return false
	}
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
