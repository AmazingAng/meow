package push

import (
	"encoding/json"
	"strings"
)

// notificationPreview is mirrored by NotificationContent.preview on iOS.
// Only the banner is condensed; stored notification bodies are untouched.
func notificationPreview(body string) string {
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		return ""
	}
	if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Valid([]byte(text)) {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		text = strings.Join(lines[:3], "\n") + "…"
	}
	return truncate(text, maxAlertBody)
}
