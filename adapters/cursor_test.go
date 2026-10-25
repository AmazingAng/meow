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
		t.Fatalf("grok envelope should be ignored by the cursor adapter: %+v %v", r, err)
	}
}

func TestGrokbotEvents(t *testing.T) {
	stop := []byte(`{"hook_event_name":"Stop","hookEventName":"stop","sessionId":"s","promptId":"p","cwd":"/work/meow","reason":"end_turn","lastAssistantMessage":"Finished the migration"}`)
	r, err := Build("grokbot-stop", stop, Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Event != "result" || r.Notification.Source != "Grok" || r.Notification.Body != "Finished the migration" {
		t.Fatalf("%+v %v", r, err)
	}
	if r.Notification.Context == nil || r.Notification.Context.Workspace != "meow" || r.Notification.Context.SessionID != "s" {
		t.Fatalf("context=%+v", r.Notification.Context)
	}
	again, err := Build("grokbot-stop", stop, Options{IncludeSummary: true})
	if err != nil || again.IdempotencyKey == "" || again.IdempotencyKey != r.IdempotencyKey {
		t.Fatal("same turn is not stable")
	}
	for _, raw := range []string{
		`{"hook_event_name":"Stop","sessionId":"s","promptId":"p","reason":"end_turn","lastAssistantMessage":"  "}`,
		`{"hook_event_name":"Stop","sessionId":"s","promptId":"p","reason":"shutdown","lastAssistantMessage":"bye"}`,
		`{"hookEventName":"stop","session_id":"s","prompt_id":"p","reason":"channel_closed","last_assistant_message":"bye"}`,
		`{"hook_event_name":"Stop","sessionId":"s","promptId":"p","reason":"end_turn","subagentType":"explore","lastAssistantMessage":"inner"}`,
	} {
		r, err = Build("grokbot-stop", []byte(raw), Options{IncludeSummary: true})
		if err != nil || r.Send {
			t.Fatalf("notified %s: %+v %v", raw, r, err)
		}
	}
	r, err = Build("grokbot-stop", []byte(`{"hookEventName":"stop","session_id":"snake","prompt_id":"turn","last_assistant_message":"from snake_case"}`), Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Body != "from snake_case" || r.Notification.Context.SessionID != "snake" {
		t.Fatalf("snake=%+v %v", r, err)
	}
	r, err = Build("grokbot-failure", []byte(`{"hook_event_name":"StopFailure","sessionId":"s","promptId":"p","error":"rate_limit","errorDetails":"Try later"}`), Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Event != "failure" || r.Notification.Body != "rate_limit\nTry later" || r.Notification.Priority != "high" {
		t.Fatalf("failure=%+v %v", r, err)
	}
	r, err = Build("grokbot-notify", []byte(`{"hookEventName":"notification","sessionId":"s","notification_type":"permission_prompt","message":"Allow the deploy?"}`), Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Event != "attention" || r.Notification.Body != "Allow the deploy?" || r.IdempotencyKey != "" {
		t.Fatalf("permission=%+v %v", r, err)
	}
	r, err = Build("grokbot-notify", []byte(`{"hook_event_name":"Notification","sessionId":"s","notificationType":"idle_prompt","message":"waiting"}`), Options{IncludeSummary: true})
	if err != nil || r.Send {
		t.Fatalf("idle=%+v %v", r, err)
	}
	if _, err = Build("grokbot-stop", []byte(`{"hook_event_name":"Notification","sessionId":"s","message":"x"}`), Options{}); err == nil {
		t.Fatal("mismatched event accepted")
	}
}
