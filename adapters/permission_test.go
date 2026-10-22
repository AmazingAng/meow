package adapters

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"meow.blue/meow/message"
)

func permissionEvent(t *testing.T, input any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": "s1", "turn_id": "t1",
		"session_title": "修复通知渲染", "cwd": "/projects/meow", "tool_name": "Bash", "tool_input": input,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCodexPermissionShowsOnlyExplanation(t *testing.T) {
	command := "python3 - <<'PY'\nfrom pathlib import Path\nprint('PRIVATE_SCRIPT')\nPY"
	reason := "需要修正表格列宽并重新验证，是否允许继续？"
	input := permissionEvent(t, map[string]any{
		"command": command, "description": reason, "workdir": "/PRIVATE_PATH", "timeout_ms": 300000,
	})
	r, err := Build("codex-permission", input, Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Body != reason {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if r.Notification.Type != message.TypeAlert || r.Notification.Priority != message.PriorityHigh || r.Notification.Context.Session != "修复通知渲染" {
		t.Fatalf("notification metadata changed: %+v", r.Notification)
	}
	if preview := message.Preview(r.Notification.Body); preview != reason {
		t.Fatalf("preview=%q", preview)
	}
	r, err = Build("codex-permission", input, Options{IncludeSummary: false})
	if err != nil || !r.Send || r.Notification.Body != "" {
		t.Fatalf("--no-summary exposed explanation: %+v %v", r, err)
	}
}

func TestCodexPermissionInputVariants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		want  string
	}{
		{"exec justification", map[string]any{"cmd": "git status\ngit diff", "justification": "Check changes", "workdir": "/project"}, "Check changes"},
		{"serialized object", `{"command":"echo hello\necho bye","description":"Run a check"}`, "Run a check"},
		{"unknown tool", map[string]any{"path": "/tmp/test", "append": true}, ""},
		{"command only", map[string]any{"command": "git push"}, ""},
		{"argv", map[string]any{"command": []string{"printf", "two words"}}, ""},
		{"unstructured input", "python3 run.py", ""},
		{"both reasons", map[string]any{"description": " What ", "justification": "Why", "command": "true"}, "What\n\nWhy"},
		{"duplicate reasons", map[string]any{"description": " Check ", "justification": "Check"}, "Check"},
		{"empty description", map[string]any{"description": " ", "justification": "Why"}, "Why"},
		{"nontext description", map[string]any{"description": map[string]any{"command": "private"}}, ""},
		{"no input", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Build("codex-permission", permissionEvent(t, tc.input), Options{IncludeSummary: true})
			if err != nil || !r.Send || r.Notification.Body != tc.want {
				t.Fatalf("body=%q want=%q err=%v send=%v", r.Notification.Body, tc.want, err, r.Send)
			}
		})
	}
}

func TestCodexPermissionReasonBodyLimit(t *testing.T) {
	r, err := Build("codex-permission", permissionEvent(t, map[string]any{
		"command": strings.Repeat("PRIVATE_SCRIPT", 10000), "description": strings.Repeat("说明😺", 3000),
	}), Options{IncludeSummary: true})
	if err != nil || !r.Send || !utf8.ValidString(r.Notification.Body) || utf8.RuneCountInString(r.Notification.Body) != message.MaxBody || !strings.HasSuffix(r.Notification.Body, "…") {
		t.Fatalf("invalid bounded explanation: %+v %v", r, err)
	}
}
