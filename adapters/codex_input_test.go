package adapters

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"meow.blue/meow/message"
)

func inputHook(t *testing.T, tool string, arguments any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "session", "turn_id": "turn", "tool_use_id": "call1", "tool_name": tool, "tool_input": arguments, "session_title": "部署检查", "cwd": "/projects/meow"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexInputQuestionsAndChoices(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args any
		want string
	}{
		{"request_user_input", map[string]any{"questions": []any{map[string]any{"question": "部署到哪里？", "options": []any{map[string]string{"label": "测试", "description": "先验证"}, map[string]string{"label": "生产", "description": "正式发布"}}}}}, "部署到哪里？\n- 测试：先验证\n- 生产：正式发布"},
		{"request_user_input_async", map[string]any{"questions": []any{map[string]any{"title": "请选择区域", "options": []string{"亚洲", "欧洲"}}, map[string]any{"title": "请填写备注"}}}, "请选择区域\n- 亚洲\n- 欧洲\n\n请填写备注"},
		{"functions.request_user_input_async", `{"questions":[{"title":"填写版本号"}]}`, "填写版本号"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			r, err := Build("codex-input", inputHook(t, tc.tool, tc.args), Options{IncludeSummary: true})
			if err != nil || !r.Send || r.Notification.Body != tc.want {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if r.Notification.Event != message.EventAttention || r.Notification.Type != message.TypeAlert || r.Notification.Priority != message.PriorityHigh || r.Notification.Context.SessionID != "session" || r.Notification.Context.Session != "部署检查" {
				t.Fatalf("wrong input reminder: %+v", r.Notification)
			}
		})
	}
}

func TestCodexInputIgnoresOtherToolsAndMalformedQuestions(t *testing.T) {
	for _, tool := range []string{"Bash", "spawn_agent", "request_permissions", "update_plan", "mcp__example__request_user_input", "request_user_input_async_extra"} {
		r, err := Build("codex-input", inputHook(t, tool, map[string]any{"questions": []any{map[string]string{"question": "Do not notify"}}}), Options{IncludeSummary: true})
		if err != nil || r.Send {
			t.Fatalf("unrelated tool %q notified: %+v %v", tool, r, err)
		}
	}
	for _, args := range []any{nil, "not JSON", map[string]any{}, map[string]any{"questions": []any{}}, map[string]any{"questions": []any{nil}}, map[string]any{"questions": []any{map[string]string{"title": "  "}}}, map[string]any{"questions": []any{map[string]any{"title": "Choose", "options": []any{map[string]any{"label": 123}}}}}} {
		r, _ := Build("codex-input", inputHook(t, "request_user_input_async", args), Options{IncludeSummary: true})
		if r.Send {
			t.Fatalf("invalid question notified: %+v", args)
		}
	}
}

func TestCodexInputIdentityReplaySummaryAndUnicode(t *testing.T) {
	input := inputHook(t, "request_user_input_async", map[string]any{"questions": []any{map[string]string{"title": strings.Repeat("选择😺", 2000)}}})
	r, err := Build("codex-input", input, Options{IncludeSummary: true})
	if err != nil || !r.Send || !utf8.ValidString(r.Notification.Body) || utf8.RuneCountInString(r.Notification.Body) != message.MaxBody || r.IdempotencyKey == "" {
		t.Fatalf("invalid bounded reminder: %+v %v", r, err)
	}
	replay, _ := Build("codex-input", input, Options{IncludeSummary: true})
	if r.IdempotencyKey != replay.IdempotencyKey {
		t.Fatal("replay changed key")
	}
	var p map[string]any
	json.Unmarshal(input, &p)
	p["tool_use_id"] = "call2"
	other, _ := json.Marshal(p)
	different, _ := Build("codex-input", other, Options{IncludeSummary: true})
	if different.IdempotencyKey == r.IdempotencyKey {
		t.Fatal("two questions in one turn reused a key")
	}
	quiet, err := Build("codex-input", input, Options{})
	if err != nil || !quiet.Send || quiet.Notification.Body != "" {
		t.Fatal("no-summary suppressed reminder or exposed text")
	}
	for _, key := range []string{"session_id", "turn_id", "tool_use_id"} {
		var missing map[string]any
		json.Unmarshal(input, &missing)
		delete(missing, key)
		b, _ := json.Marshal(missing)
		if got, err := Build("codex-input", b, Options{}); err == nil || got.Send {
			t.Fatalf("missing %s accepted", key)
		}
	}
}

func TestCodexInputRejectsNativeQuestionWithoutRequiredOptions(t *testing.T) {
	for _, options := range []any{nil, []any{}} {
		r, err := Build("codex-input", inputHook(t, "request_user_input", map[string]any{"questions": []any{map[string]any{"question": "Invalid native question", "options": options}}}), Options{IncludeSummary: true})
		if err == nil || r.Send {
			t.Fatal("Codex rejects this native call before showing a question; do not notify")
		}
	}
}
