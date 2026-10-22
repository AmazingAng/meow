package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAutoReviewAttention(t *testing.T) {
	for _, tc := range []struct {
		name, reviewer, tool, session, turn string
		wantSend                            bool
	}{
		{"auto shell", "auto_review", "Bash", "s", "t", false},
		{"auto patch", "auto_review", "apply_patch", "s", "t", false},
		{"auto edit alias", "auto_review", "Edit", "s", "t", false},
		{"auto write alias", "auto_review", "Write", "s", "t", false},
		{"auto stdin", "auto_review", "write_stdin", "s", "t", false},
		{"auto permissions", "auto_review", "request_permissions", "s", "t", false},
		{"human", "user", "Bash", "s", "t", true},
		{"missing reviewer", "", "Bash", "s", "t", true},
		{"unknown reviewer", "future", "Bash", "s", "t", true},
		{"other session", "auto_review", "Bash", "other", "t", true},
		{"other turn", "auto_review", "Bash", "s", "other", true},
		{"MCP can require human", "auto_review", "mcp__computer__click", "s", "t", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			data := `{"type":"session_meta","payload":{"id":"` + tc.session + `"}}` + "\n" +
				`{"type":"turn_context","payload":{"turn_id":"` + tc.turn + `","approvals_reviewer":"` + tc.reviewer + `"}}` + "\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			input := codexApprovalInput(t, path, tc.tool)
			_, _, send, err := buildHookNotification("codex-permission", input, true)
			if err != nil || send != tc.wantSend {
				t.Fatalf("send=%v err=%v", send, err)
			}
			var p map[string]any
			json.Unmarshal(input, &p)
			p["hook_event_name"], p["last_assistant_message"] = "Stop", "Completed"
			stop, _ := json.Marshal(p)
			if _, _, send, err := buildHookNotification("codex-stop", stop, true); err != nil || !send {
				t.Fatalf("result suppressed: %v", err)
			}
		})
	}
}

func codexApprovalInput(t *testing.T, path, tool string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"session_id": "s", "turn_id": "t", "hook_event_name": "PermissionRequest", "transcript_path": path, "tool_name": tool, "tool_input": map[string]string{"description": "Allow this operation?"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexApprovalMetadataFailsOpen(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	auto := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	human := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n"
	for name, data := range map[string]string{
		"malformed":                "{broken\n",
		"missing identity":         auto,
		"newer human context":      header + auto + human,
		"newer unknown context":    header + auto + `{"type":"turn_context","payload":{"turn_id":"t"}}` + "\n",
		"newer other turn":         header + auto + `{"type":"turn_context","payload":{"turn_id":"other","approvals_reviewer":"auto_review"}}` + "\n",
		"newer incomplete context": header + auto + `{"type":"turn_context","payload":`,
		"outside bounded tail":     header + auto + strings.Repeat("{\"type\":\"unrelated\"}\n", 110000),
		"truncated metadata":       header + strings.TrimSuffix(auto, "}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || !send {
				t.Fatalf("send=%v err=%v", send, err)
			}
		})
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || !send {
			t.Fatalf("path=%q send=%v err=%v", path, send, err)
		}
	}
}

func TestCodexAutoReviewLargeTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	// Disk defaults deliberately disagree with this turn. Only the event's
	// rollout is authoritative, including after switching modes mid-session.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("approvals_reviewer = 'user'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout.jsonl")
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	data := header + strings.Repeat("{\"type\":\"unrelated\"}\n", 110000) +
		`{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n" +
		`{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || send {
		t.Fatalf("send=%v err=%v", send, err)
	}
}

func TestCodexAutoReviewDoesNotReachServer(t *testing.T) {
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n struct {
			Event string `json:"event"`
		}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			t.Error(err)
		}
		events = append(events, n.Event)
		io.WriteString(w, `{"id":"ntf_test"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_test")
	t.Setenv("MEOW_HOOK_DISABLED", "")
	path := filepath.Join(dir, "rollout.jsonl")
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	for _, reviewer := range []string{"auto_review", "user"} {
		data := header + `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"` + reviewer + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out := captureOutput(t, string(codexApprovalInput(t, path, "Bash")), func() {
			if cmdHook([]string{"codex-permission"}) != 0 {
				t.Error("nonzero exit")
			}
		})
		if out != "" {
			t.Fatalf("hook affected agent: %q", out)
		}
		if reviewer == "auto_review" && len(events) != 0 {
			t.Fatal("auto-review reached server")
		}
	}
	if len(events) != 1 || events[0] != "attention" {
		t.Fatalf("events=%v", events)
	}
}
