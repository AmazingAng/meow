package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"meow.blue/meow/message"
)

// Only explicit user-input tools represent questions. Tool approvals, ordinary
// prose and child-agent creation are not evidence that the user needs to reply.
func decodeCodexInput(data []byte) (Event, bool, error) {
	var p CodexHookInput
	if json.Unmarshal(data, &p) != nil || p.Event != "PreToolUse" || p.SessionID == "" || p.TurnID == "" || p.ToolUseID == "" {
		return Event{}, false, fmt.Errorf("missing or mismatched Codex input identity")
	}
	switch p.ToolName {
	case "request_user_input", "request_user_input_async", "functions.request_user_input", "functions.request_user_input_async":
	default:
		return Event{}, false, nil
	}
	body, err := codexQuestions(p.ToolInput, strings.TrimPrefix(p.ToolName, "functions.") == "request_user_input")
	if err != nil {
		return Event{}, false, err
	}
	return Event{
		Notification: message.Notification{Event: message.EventAttention, Type: message.TypeAlert, Source: "Codex", Priority: message.PriorityHigh, Body: body},
		Origin:       Origin{Cwd: p.Cwd, SessionID: p.SessionID, SessionName: p.SessionTitle},
		Template:     AttentionTitle, DedupeNamespace: "codex-input",
		EventID: IdempotencyKey("codex-input-call", p.TurnID, p.ToolUseID),
	}, true, nil
}

func codexQuestions(input json.RawMessage, requireOptions bool) (string, error) {
	var encoded string
	if json.Unmarshal(input, &encoded) == nil {
		input = json.RawMessage(encoded)
	}
	var request struct {
		Questions []struct {
			Question string            `json:"question"`
			Title    string            `json:"title"`
			Options  []json.RawMessage `json:"options"`
		} `json:"questions"`
	}
	invalid := func() (string, error) { return "", fmt.Errorf("invalid Codex input questions") }
	if json.Unmarshal(input, &request) != nil || len(request.Questions) == 0 {
		return invalid()
	}
	parts := make([]string, 0, len(request.Questions))
	for _, q := range request.Questions {
		if requireOptions && len(q.Options) == 0 {
			return invalid()
		}
		question := strings.TrimSpace(q.Question)
		if question == "" {
			question = strings.TrimSpace(q.Title)
		}
		if question == "" {
			return invalid()
		}
		lines := []string{question}
		for _, raw := range q.Options {
			var label, description string
			if json.Unmarshal(raw, &label) != nil {
				var option struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				}
				if json.Unmarshal(raw, &option) != nil {
					return invalid()
				}
				label, description = option.Label, option.Description
			}
			label = strings.TrimSpace(label)
			if label == "" {
				return invalid()
			}
			line := "- " + label
			if description = strings.TrimSpace(description); description != "" {
				line += "：" + description
			}
			lines = append(lines, line)
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n"), nil
}
