package adapters

import (
	"encoding/json"
	"testing"
)

func TestAgentEventsAreExplicit(t *testing.T) {
	for _, tc := range []struct{ adapter, hook, want string }{
		{"claude-stop", "Stop", "result"}, {"claude-notify", "Notification", "attention"},
		{"claude-failure", "StopFailure", "failure"}, {"codex-stop", "Stop", "result"},
		{"codex-permission", "PermissionRequest", "attention"}, {"codex", "", "result"},
		{"cursor-stop", "stop", "result"},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			input, _ := json.Marshal(map[string]any{"session_id": "s", "sessionId": "s", "turn_id": "t", "prompt_id": "p", "promptId": "p", "hook_event_name": tc.hook, "conversation_id": "s", "generation_id": "g", "status": "completed", "last_assistant_message": "text", "lastAssistantMessage": "text", "message": "look", "notificationType": "permission_prompt", "error": "failure", "type": "agent-turn-complete", "thread-id": "s", "turn-id": "t"})
			r, err := Build(tc.adapter, input, Options{IncludeSummary: true})
			if err != nil || !r.Send {
				t.Fatalf("result=%+v %v", r, err)
			}
			data, _ := json.Marshal(r.Notification)
			var object map[string]any
			_ = json.Unmarshal(data, &object)
			if object["event"] != tc.want {
				t.Fatalf("event missing: %s", data)
			}
		})
	}
	r, err := Build("generic", []byte(`{"title":"我自己的标题","event":"failure","type":"info","priority":"low"}`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r.Notification)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	if object["event"] != "failure" || r.Notification.Title != "我自己的标题" || r.Notification.Type != "info" || r.Notification.Priority != "low" {
		t.Fatalf("generic=%s", data)
	}
}
