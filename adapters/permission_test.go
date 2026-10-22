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

func TestCodexPermissionExplainsBeforeShowingDecodedCommand(t *testing.T) {
	// Reproduces the screenshot: a multiline Python heredoc and the human reason
	// were previously displayed as an escaped JSON blob in the notification.
	command := "python3 - <<'PY'\nfrom pathlib import Path\nprint(r'C:\\new\\test')\nPY\npython3 -B ios/scripts/verify_ui_optimization.py"
	reason := "UI 回归测试的元素查询需要一处编译修正；是否允许修正后重跑验证？"
	r, err := Build("codex-permission", permissionEvent(t, map[string]any{
		"command": command, "description": reason,
	}), Options{IncludeSummary: true})
	if err != nil || !r.Send {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	want := reason + "\n\nBash\n\n```sh\n" + command + "\n```"
	if r.Notification.Body != want {
		t.Fatalf("body=%q want=%q", r.Notification.Body, want)
	}
	if r.Notification.Type != message.TypeAlert || r.Notification.Priority != message.PriorityHigh || r.Notification.Context.Session != "修复通知渲染" {
		t.Fatalf("notification metadata changed: %+v", r.Notification)
	}
	if preview := message.Preview(r.Notification.Body); !strings.HasPrefix(preview, reason) {
		t.Fatalf("preview hides the reason: %q", preview)
	}
	r, err = Build("codex-permission", permissionEvent(t, map[string]any{"command": command, "description": reason}), Options{IncludeSummary: false})
	if err != nil || r.Notification.Body != "" {
		t.Fatalf("--no-summary exposed tool input: %+v %v", r, err)
	}
}

func TestCodexPermissionInputVariants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		want  []string
	}{
		{"exec command", map[string]any{"cmd": "git status\ngit diff", "justification": "Check changes", "workdir": "/project"}, []string{"Check changes\n\nBash", "```sh\ngit status\ngit diff\n```", `"workdir": "/project"`}},
		{"serialized object", `{"command":"echo hello\necho bye","description":"Run a check"}`, []string{"Run a check\n\nBash", "```sh\necho hello\necho bye\n```"}},
		{"unknown tool", map[string]any{"path": "/tmp/test", "append": true}, []string{"Bash\n\n```json\n{", `"append": true`, `"path": "/tmp/test"`}},
		{"argv stays structured", map[string]any{"command": []string{"printf", "two words"}}, []string{"```json", `"command": [`, `"two words"`}},
		{"plain string", "Review this action", []string{"Bash\n\n```text\nReview this action\n```"}},
		{"both reasons", map[string]any{"description": "What", "justification": "Why", "command": "true"}, []string{"What\n\nWhy\n\nBash"}},
		{"no input", nil, []string{"Bash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Build("codex-permission", permissionEvent(t, tc.input), Options{IncludeSummary: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(r.Notification.Body, want) {
					t.Fatalf("body=%q missing %q", r.Notification.Body, want)
				}
			}
		})
	}
	// Formatting must not round arbitrary tool arguments through float64.
	if body := permissionBody("Tool", json.RawMessage(`{"id":9007199254740993}`)); !strings.Contains(body, "9007199254740993") {
		t.Fatal(body)
	}
}

func TestCodexPermissionCodeFencesAndBodyLimit(t *testing.T) {
	for _, command := range []string{
		"printf '```markdown\n# inside command\n```'",
		strings.Repeat("中文😺\n", 3000),
		strings.Repeat("`", 6000),
	} {
		data := permissionEvent(t, map[string]any{"command": command, "description": strings.Repeat("说明", 3000)})
		r, err := Build("codex-permission", data, Options{IncludeSummary: true})
		if err != nil || !r.Send || !utf8.ValidString(r.Notification.Body) || utf8.RuneCountInString(r.Notification.Body) > message.MaxBody {
			t.Fatalf("invalid bounded body: %+v %v", r, err)
		}
		body := r.Notification.Body
		opening := strings.Index(body, "\n\n```")
		if opening < 0 {
			t.Fatalf("no code block: %q", body)
		}
		lines := strings.Split(body[opening+2:], "\n")
		fence := strings.TrimSuffix(lines[0], "sh")
		if lines[len(lines)-1] != fence {
			t.Fatalf("closing fence lost during truncation: %q", body)
		}
		if strings.Contains(strings.Join(lines[1:len(lines)-1], "\n"), fence) {
			t.Fatal("tool command can escape its code fence")
		}
	}
}
