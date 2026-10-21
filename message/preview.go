package message

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const MaxPreviewRunes = 180
const MaxPreviewLines = 3

// Preview condenses banners only. The original body is preserved for the inbox
// and Markdown detail renderer. Swift uses the same cross-language fixtures.
func Preview(body string) string {
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
	if len(lines) > MaxPreviewLines {
		text = strings.Join(lines[:MaxPreviewLines], "\n") + "…"
	}
	return Truncate(text, MaxPreviewRunes)
}

func Truncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}
