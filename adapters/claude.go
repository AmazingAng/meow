package adapters

import (
	"encoding/json"
	"fmt"
	"meow.blue/meow/message"
	"strings"
)

type claudeInput struct {
	SessionTitle         string `json:"session_title"`
	TranscriptPath       string `json:"transcript_path"`
	SessionID            string `json:"session_id"`
	PromptID             string `json:"prompt_id"`
	Cwd                  string `json:"cwd"`
	LastAssistantMessage string `json:"last_assistant_message"`
	Message              string `json:"message"`
	Title                string `json:"title"`
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
}

func decodeClaude(name string, data []byte) (Event, bool, error) {
	var in claudeInput
	if err := json.Unmarshal(data, &in); err != nil {
		return Event{}, false, fmt.Errorf("invalid Claude event: %w", err)
	}
	if in.SessionID == "" && in.Message == "" && in.Error == "" {
		return Event{}, false, fmt.Errorf("invalid Claude event: missing event fields")
	}
	e := Event{Notification: message.Notification{Source: "Claude Code"}, Origin: Origin{Cwd: in.Cwd, SessionID: in.SessionID, SessionName: in.SessionTitle, TranscriptPath: in.TranscriptPath}, EventID: in.PromptID}
	switch name {
	case "claude-stop":
		e.Template = ResultTitle
		e.DedupeNamespace = name
		e.Notification.Body = in.LastAssistantMessage
	case "claude-failure":
		e.Template = FailureTitle
		e.DedupeNamespace = name
		e.Notification.Body = strings.TrimSpace(in.Error + "\n" + in.ErrorDetails)
	case "claude-notify":
		e.Template = AttentionTitle
		e.Notification.Title = strings.TrimSpace(in.Title)
		e.Notification.Body = in.Message
	}
	if e.Template != ResultTitle {
		e.Notification.Type = message.TypeAlert
		e.Notification.Priority = message.PriorityHigh
	}
	return e, true, nil
}
