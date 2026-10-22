package adapters

import (
	"encoding/json"
	"strings"
)

// Meow V1 only reminds the user to return to their agent. Forward the human
// explanation, not the tool name, executable script or other tool parameters.
// A request without an explanation still has its attention title and metadata.
func permissionReason(input json.RawMessage) string {
	var text string
	if json.Unmarshal(input, &text) == nil {
		input = json.RawMessage(text)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	var reasons []string
	for _, key := range []string{"description", "justification"} {
		var reason string
		if json.Unmarshal(fields[key], &reason) != nil {
			continue
		}
		reason = strings.TrimSpace(reason)
		if reason != "" && (len(reasons) == 0 || reasons[0] != reason) {
			reasons = append(reasons, reason)
		}
	}
	return strings.Join(reasons, "\n\n")
}
