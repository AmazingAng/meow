package adapters

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCursorStopStatuses(t *testing.T) {
	base := map[string]any{"hook_event_name": "stop", "conversation_id": "conv", "generation_id": "gen", "workspace_roots": []string{"", "/work/meow"}, "transcript_path": "/tmp/transcript.jsonl"}
	for _, tc := range []struct {
		status string
		send   bool
		event  string
		err    bool
	}{
		{"completed", true, "result", false},
		{"error", true, "failure", false},
		{"aborted", false, "", false},
		{"future", false, "", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			in := map[string]any{}
			for k, v := range base {
				in[k] = v
			}
			in["status"] = tc.status
			raw, _ := json.Marshal(in)
			r, err := Build("cursor-stop", raw, Options{IncludeSummary: true, Language: "en"})
			if tc.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || r.Send != tc.send {
				t.Fatalf("send=%v err=%v", r.Send, err)
			}
			if !tc.send {
				return
			}
			if string(r.Notification.Event) != tc.event || r.Notification.Source != "Cursor" || r.Notification.Body != "" {
				t.Fatalf("%+v", r.Notification)
			}
			if r.Notification.Context == nil || r.Notification.Context.Workspace != "meow" || r.Notification.Context.SessionID != "conv" {
				t.Fatalf("context=%+v", r.Notification.Context)
			}
			if !strings.HasPrefix(r.Notification.Title, "New result") && !strings.HasPrefix(r.Notification.Title, "Run failed") {
				t.Fatal(r.Notification.Title)
			}
			if r.IdempotencyKey == "" {
				t.Fatal("missing delivery key")
			}
		})
	}
	if _, err := Build("cursor-stop", []byte(`{"hook_event_name":"stop","status":"completed"}`), Options{}); err == nil {
		t.Fatal("missing conversation id")
	}
	r, err := Build("cursor-stop", []byte(`{"hook_event_name":"Stop","hookEventName":"stop","sessionId":"s","lastAssistantMessage":"done","reason":"end_turn"}`), Options{IncludeSummary: true})
	if err != nil || r.Send {
		t.Fatalf("non-Cursor envelope should be ignored: %+v %v", r, err)
	}
}
