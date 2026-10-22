package adapters

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"meow.blue/meow/message"
)

// Permission input is a tool invocation, not prose. Decode its string values
// once so real newlines become code lines while literal backslashes survive.
// The human explanation comes first, including in the inbox/lock-screen preview.
func permissionBody(tool string, input json.RawMessage) string {
	var fields map[string]json.RawMessage
	var text string
	if json.Unmarshal(input, &text) == nil {
		input = json.RawMessage(text) // Some integrations serialize tool_input twice.
	}
	var reasons []string
	var command string
	if json.Unmarshal(input, &fields) == nil && fields != nil {
		for _, key := range []string{"description", "justification"} {
			var reason string
			if json.Unmarshal(fields[key], &reason) == nil && strings.TrimSpace(reason) != "" {
				reason = strings.TrimSpace(reason)
				if len(reasons) == 0 || reasons[0] != reason {
					reasons = append(reasons, reason)
				}
				delete(fields, key)
			}
		}
		for _, key := range []string{"command", "cmd"} {
			if json.Unmarshal(fields[key], &command) == nil && strings.TrimSpace(command) != "" {
				delete(fields, key)
				break
			}
			command = ""
		}
	}

	var parts []permissionPart
	if len(reasons) > 0 {
		parts = append(parts, permissionPart{text: strings.Join(reasons, "\n\n")})
	}
	if tool = message.CleanLabel(tool, message.MaxSource); tool != "" {
		parts = append(parts, permissionPart{text: tool})
	}
	if command != "" {
		parts = append(parts, permissionPart{text: command, language: "sh"})
	}
	if fields != nil {
		if len(fields) > 0 {
			data, _ := json.MarshalIndent(fields, "", "  ")
			parts = append(parts, permissionPart{text: string(data), language: "json"})
		}
	} else if len(input) > 0 && string(input) != "null" {
		// Unknown tools and non-object inputs remain inspectable; do not silently
		// discard parameters or guess that arbitrary JSON strings are commands.
		data := input
		if json.Valid(input) {
			// Marshal RawMessage indents without converting large numbers to float64.
			formatted, _ := json.MarshalIndent(input, "", "  ")
			data = formatted
		}
		parts = append(parts, permissionPart{text: string(data), language: "text"})
	}
	return permissionMarkdown(parts)
}

type permissionPart struct{ text, language string }

func permissionMarkdown(parts []permissionPart) string {
	var body string
	for i, part := range parts {
		separator := ""
		if body != "" {
			separator = "\n\n"
		}
		budget := message.MaxBody - utf8.RuneCountInString(body+separator)
		if budget <= 0 {
			break
		}
		text := part.text
		if part.language == "" {
			// Reserve room for the actual tool input if its explanation is huge.
			if i == 0 && len(parts) > 1 {
				budget = min(budget, message.MaxBody/2)
			}
			text = message.Truncate(text, budget)
		} else {
			text = permissionCode(text, part.language, budget)
		}
		if text != "" {
			body += separator + text
		}
	}
	return body
}

// Keep the closing fence even when a command exceeds the wire body limit.
// A longer fence prevents embedded Markdown examples from escaping code layout.
func permissionCode(text, language string, budget int) string {
	if budget < 8+len(language) {
		return ""
	}
	text = message.Truncate(text, budget-8-len(language))
	for {
		longest, run := 0, 0
		for _, r := range text {
			if r == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		overhead := 2*len(fence) + len(language) + 2
		if utf8.RuneCountInString(text)+overhead <= budget {
			return fence + language + "\n" + text + "\n" + fence
		}
		text = message.Truncate(text, max(0, budget-overhead))
	}
}
